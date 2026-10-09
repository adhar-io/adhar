package up

// The apps-convergence watchdog must be wired into the CLOUD bootstrap, not
// only the local one.
//
// watchAppsBudget exists to end a run whose API server has become unreachable —
// its own comment says so. For two releases it was started only by
// LocalProvisioner (cmd/up/local.go), i.e. on Kind, where the API server lives
// on the same machine as the CLI and does not vanish. The cloud path passed
// `appsTimeout` to the reconciler and nothing else, and the reconciler can only
// honour a budget when it can reach the API.
//
// Live consequence (Civo, 2026-10-09): the managed-k3s API server stopped
// serving three minutes into the GitOps phase while the provider still reported
// the cluster ACTIVE/ready=true, and `adhar up` ran 22 minutes past a 15-minute
// budget with no output and no end in sight.
//
// This is a wiring guard rather than a behaviour test because the behaviour is
// already covered (kube_version_test.go drives watchAppsBudget directly); what
// was missing was the call. A guard on the call site is what would have caught
// it.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestCloudBootstrapStartsTheAppsBudgetWatchdog(t *testing.T) {
	raw, err := os.ReadFile("bootstrap.go")
	if err != nil {
		t.Fatalf("reading bootstrap.go: %v", err)
	}
	src := string(raw)

	if !strings.Contains(src, "go watchAppsBudget(") {
		t.Fatal("cmd/up/bootstrap.go never starts watchAppsBudget: a cloud `adhar up` whose API " +
			"server disappears mid-GitOps has nothing to end it, because --apps-timeout is only " +
			"checked inside a reconcile and a reconcile needs the API server")
	}

	// It has to be gated on the flag and handed the cancel func, or it either
	// never fires or fires without stopping anything.
	//
	// The window is the CALL ONLY — up to its closing newline. A fixed-width
	// window ran past the statement into the `RunControllers(..., cancel, ...)`
	// line below it, so substituting `nil` for the cancel func still "passed".
	// Found by mutation-testing this very test.
	idx := strings.Index(src, "go watchAppsBudget(")
	call := firstLine(src[idx:])
	for _, want := range []string{"appsTimeout", "cancel"} {
		if !strings.Contains(call, want) {
			t.Errorf("the watchAppsBudget call in bootstrap.go does not pass %q; without it the "+
				"watchdog cannot bound the run:\n  %s", want, firstLine(call))
		}
	}
	if !regexp.MustCompile(`if appsTimeout > 0 \{`).MatchString(src) {
		t.Error("bootstrap.go starts the watchdog without gating on `appsTimeout > 0`; " +
			"--apps-timeout=0 means \"exit as soon as the foundation is ready\" and must not " +
			"arm a budget")
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
