package up

// The cloud checklist's title, and the thing that was duplicating it.
//
// TITLE. What `adhar up` provisions is the PLATFORM, so that is what the block
// says. It used to read "Provisioning <env>", which is wrong in the normal case
// and misleading in the common one: environments default to
// `isolation: namespace`, so dev/test/prod share ONE cluster sized from the
// production environment, and the title then named only that one —
// "Provisioning prod" — while the run was standing up the platform hosting all
// three. The environment is appended only when there really are several
// cluster-backed environments to tell apart.
//
// DUPLICATE. The tracker repositions with "\x1b[<lastLines>A\r\x1b[J" — up by
// however many lines it last drew, then clear — so any other writer to the same
// terminal makes that count wrong, the next redraw starts too low, and the
// previous block is orphaned:
//
//	Provisioning prod  2m16s     <- orphaned
//	Provisioning prod  2m16s     <- live
//
// klog (k8s.io/client-go) writes to stderr, which is where the tracker draws.
// It was already discarded in bootstrapPlatformOnCluster and in the local
// provisioner — but both run LATER. On a cloud run the tracker starts before
// ProvisionEnvironment, and the provider talks to the cluster during Preflight
// and cluster creation, so that window was unprotected. Observed live on Civo
// (2026-10-09) seconds after the nodes came up:
//
//	E1009 06:54:01 memcache.go:287] couldn't get resource list for
//	metrics.k8s.io/v1beta1: the server is currently unable to handle the request

import (
	"os"
	"strings"
	"testing"
)

func cloudProgressSource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("cloud_progress.go")
	if err != nil {
		t.Fatalf("reading cloud_progress.go: %v", err)
	}
	return string(raw)
}

// cloudProgressCode is the source with // comments stripped.
//
// Any positional check MUST use this. Both of these assertions first failed
// against correct code because the comments explaining the fix quote the very
// identifiers being searched for — the note above the klog block mentions
// `tracker.Start()`, so strings.Index found the comment, decided the ordering
// was wrong, and reported a bug that did not exist. A guard that a thorough
// comment can break is a guard that punishes documenting the fix.
func cloudProgressCode(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, line := range strings.Split(cloudProgressSource(t), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func TestCloudChecklistIsTitledForThePlatform(t *testing.T) {
	src := cloudProgressCode(t)

	if !strings.Contains(src, `title := "Provisioning Adhar Platform"`) {
		t.Error(`the cloud checklist is not titled "Provisioning Adhar Platform". The run provisions ` +
			`the platform; with the namespace-isolation default one cluster hosts dev, test and prod, ` +
			`so naming the block after a single environment misdescribes it`)
	}
	// The bare env-name title must not come back.
	if strings.Contains(src, `fmt.Sprintf("Provisioning %s", envName)`) {
		t.Error(`the title still falls back to "Provisioning <env>"; that is the form this replaced`)
	}
	// The suffix must be gated, or every single-cluster run is named after one
	// of the environments sharing it again.
	if !strings.Contains(src, "if multiEnv && envName != \"\"") {
		t.Error("the environment suffix is not gated on multiEnv; it must only appear when more " +
			"than one cluster-backed environment is being provisioned")
	}
}

func TestCloudChecklistSilencesKlogBeforeDrawing(t *testing.T) {
	src := cloudProgressCode(t)

	if !strings.Contains(src, "klog.SetOutput(io.Discard)") {
		t.Fatal("startCloudProgress does not discard klog output. klog writes to stderr, the same " +
			"stream the tracker draws on, so its discovery warnings scroll the checklist and leave " +
			"an orphaned duplicate of the title")
	}
	if !strings.Contains(src, "rest.SetDefaultWarningHandler(rest.NoWarnings{})") {
		t.Error("client-go API deprecation warnings are not suppressed; they also land on stderr")
	}

	// Ordering is the whole point: silencing after the first draw leaves the
	// Preflight window — where the warnings actually appear — unprotected.
	silence := strings.Index(src, "klog.SetOutput(io.Discard)")
	start := strings.Index(src, "tracker.Start()")
	if silence < 0 || start < 0 {
		t.Fatal("could not locate both the klog silencing and tracker.Start()")
	}
	if silence > start {
		t.Error("klog is silenced AFTER tracker.Start(). The warnings arrive during Preflight and " +
			"cluster creation, which is exactly the window between Start and the bootstrap's own " +
			"klog.SetOutput — so the duplicate title survives")
	}

	// Verbose keeps them: there the tracker degrades to plain lines and the
	// warnings are the diagnostic.
	if !strings.Contains(src, "if !verbose {") {
		t.Error("klog silencing is not gated on !verbose; verbose mode needs those warnings")
	}
}
