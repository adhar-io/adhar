package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Pipeline stability invariants for the paved-road CI.
//
// These exist because the Tekton pipelines shipped with NO stability primitives
// at all — `retries` appeared zero times across 12 pipelines and 56 tasks, and
// no Trivy invocation carried a `--timeout`. The result, measured on a live GCP
// cluster on 2026-10-03:
//
//   - `release-adhar-ui` failed twice, once at `scan` with
//     "semaphore acquire: context deadline exceeded" — Trivy had spent 4m20s of
//     its 5-minute DEFAULT timeout downloading the 120 MiB vulnerability DB,
//     leaving ~40s to walk the tree — and once at `build` on a single dropped
//     TCP read while fetching an npm package. A rerun of the same revision
//     passed, which is the signature of flakiness rather than a broken build.
//   - `app-ci-angular-app` then failed with a 404 for `@adhar-ui/angular`,
//     because the release that publishes it had never got as far as publishing.
//     One dropped read took out an unrelated application pipeline.
//
// A fix that is only applied once rots. These assert the policy instead.

// pipelineDirs are the packages that own the paved-road pipelines.
func pipelineDirs(t *testing.T) []string {
	t.Helper()
	root := filepath.Join(stackRoot(t), "packages", "application")
	return []string{
		filepath.Join(root, "adhar-supply-chain", "manifests"),
		filepath.Join(root, "adhar-libraries", "manifests"),
	}
}

func yamlFilesIn(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	return out
}

// Every Trivy invocation must bound itself explicitly.
//
// Trivy's default is 5 MINUTES TOTAL, and that budget has to cover downloading
// the vulnerability database before any scanning starts. Whether a run passes
// therefore depends on how fast the DB downloads, which is not a property of
// the code under test. `--timeout` is the difference between a scan that is slow
// and a pipeline that is unreliable.
func TestEveryTrivyScanSetsAnExplicitTimeout(t *testing.T) {
	// `trivy convert` reads a report off disk — no network, no DB, nothing to
	// bound. Every other subcommand walks a tree or pulls an image.
	invocation := regexp.MustCompile(`\btrivy\s+(fs|image|repo|config|rootfs|filesystem)\b`)

	for _, dir := range pipelineDirs(t) {
		for _, path := range yamlFilesIn(t, dir) {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			lines := strings.Split(string(b), "\n")
			for i, l := range lines {
				if strings.HasPrefix(strings.TrimSpace(l), "#") || !invocation.MatchString(l) {
					continue
				}
				// A shell invocation may continue over several lines with `\`.
				cmd := l
				for j := i; j < len(lines)-1 && strings.HasSuffix(strings.TrimRight(cmd, " "), "\\"); j++ {
					cmd += lines[j+1]
				}
				if !strings.Contains(cmd, "--timeout") && !strings.Contains(cmd, "TRIVY_TIMEOUT") {
					t.Errorf("%s:%d: trivy invocation without an explicit --timeout; the 5m default "+
						"is consumed by the vulnerability-DB download and the scan then dies with "+
						"'context deadline exceeded'\n\t%s",
						filepath.Base(path), i+1, strings.TrimSpace(l))
				}
			}
		}
	}
}

// retryPolicy is the line between infrastructure and judgement.
//
// A task that fails because a TCP read dropped, a registry 503'd or a 120 MiB
// database came down slowly should be retried: nothing about the code changed.
// A task that renders a VERDICT about the code must fail on its first attempt —
// retrying tests until they pass is how a flaky test becomes permanent, and it
// is the one "fix" that makes a pipeline less trustworthy rather than more.
var (
	retryableTasks = map[string]bool{
		"clone": true, "build": true, "scan": true, "source-scan": true,
		"sbom": true, "sign": true, "attest": true, "verify": true,
		"publish": true, "deploy": true, "scaffold": true, "assemble": true,
	}
	verdictTasks = map[string]bool{"test": true, "lint": true, "secrets": true}
)

// Network-bound pipeline tasks carry `retries`; verdict tasks never do.
//
// Parsed by hand rather than with a YAML decoder on purpose: these files are
// heavily commented and the comments are load-bearing documentation, so the
// fixtures are read as text everywhere else in this suite too.
func TestPipelineTasksFollowTheRetryPolicy(t *testing.T) {
	taskName := regexp.MustCompile(`^    - name: (\S+)\s*$`)

	for _, dir := range pipelineDirs(t) {
		for _, path := range yamlFilesIn(t, dir) {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			lines := strings.Split(string(b), "\n")
			inTasks := false
			for i, l := range lines {
				if l == "  tasks:" {
					inTasks = true
					continue
				}
				// Any other key at indent 2 closes the block — `finally:`,
				// `workspaces:`, `params:`. Without this the `- name:` entries
				// under those sections look exactly like pipeline tasks.
				if inTasks && strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "   ") &&
					strings.HasSuffix(strings.TrimRight(l, " "), ":") {
					inTasks = false
				}
				if !inTasks {
					continue
				}
				m := taskName.FindStringSubmatch(l)
				if m == nil {
					continue
				}
				name := m[1]
				// `retries` sits among the task's own keys, which start on the
				// next line at indent 6 and end at the next task or block.
				hasRetries := false
				for j := i + 1; j < len(lines); j++ {
					if strings.HasPrefix(lines[j], "    - ") || !strings.HasPrefix(lines[j], "      ") {
						break
					}
					if strings.HasPrefix(strings.TrimSpace(lines[j]), "retries:") {
						hasRetries = true
						break
					}
				}
				switch {
				case verdictTasks[name] && hasRetries:
					t.Errorf("%s:%d: task %q renders a verdict about the code and must NOT be retried — "+
						"retrying a failing test until it passes hides the defect",
						filepath.Base(path), i+1, name)
				case retryableTasks[name] && !hasRetries:
					t.Errorf("%s:%d: task %q is network-bound and needs `retries` — a dropped read, a "+
						"registry 503 or a slow vulnerability-DB download should not fail a release",
						filepath.Base(path), i+1, name)
				}
			}
		}
	}
}

// The dependency fetch in the Deno release is retried in-step.
//
// `release-adhar-ui` died on `Failed caching npm package '@esbuild/linux-x64'
// … error reading a body from connection`. The Maven release never had that
// class of failure because it already passed
// `-Daether.connector.http.retryHandler.count=5`; the Deno path had no
// equivalent, so the two library pipelines had visibly different reliability on
// the same cluster. The in-step retry recovers in seconds where a Tekton task
// retry restarts the whole stage.
func TestDenoDependencyFetchIsRetried(t *testing.T) {
	path := filepath.Join(stackRoot(t), "packages", "application", "adhar-libraries", "manifests", "npm-release.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	s := string(b)
	if !strings.Contains(s, "until deno install; do") {
		t.Error("npm-release.yaml must retry `deno install`: one dropped read during dependency " +
			"resolution otherwise fails the whole release and then 404s every app that depends on it")
	}
	// The BUILD must not be swept into the same retry: a compile error is a
	// verdict, and retrying it three times only delays the report.
	if strings.Contains(s, "until deno $(params.command)") {
		t.Error("the build command must not be retried — only the dependency fetch is transient")
	}
}

// Platform npm scopes resolve from the platform's registry, not from whatever an
// application's own .npmrc happens to name.
//
// `app-ci` for a scaffolded Angular app failed with a 404 for
// `@adhar-ui/angular` against Gitea's package registry, while the library
// release pipeline publishes to Nexus (`repository/npm-hosted/`) — where the
// package demonstrably was. The app's .npmrc even asserted in a comment that the
// pipeline published to Gitea, which was never true, and every app scaffolded
// from that template inherited the same dead pointer. A registry hostname is a
// property of the platform, so the build now sets it.
func TestBuildpacksBuildRoutesPlatformScopesToTheClusterRegistry(t *testing.T) {
	path := filepath.Join(stackRoot(t), "packages", "application", "adhar-supply-chain",
		"manifests", "60-build-pipelines.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	s := string(b)

	for _, want := range []string{
		// The scopes are configurable, not hardcoded in the script.
		"name: platformNpmScopes",
		// Sourced from the same Secret the release pipeline publishes with, so
		// the two cannot point at different registries.
		"key: npm-hosted",
		// Existing mappings are deleted before ours are appended, so the result
		// does not depend on npm's last-key-wins parsing.
		`sed -i "\|^${scope}:registry=|d" "$NPMRC"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("buildpacks-build is missing the platform npm registry injection (%q)", want)
		}
	}

	// A cluster without the nexus package must still build: every secret ref is
	// optional and the step skips when the URL is unset.
	if strings.Count(s, "optional: true") < 3 {
		t.Error("the nexus-credentials refs must be optional — a profile without nexus must still build")
	}
	if !strings.Contains(s, `if [ -z "${NEXUS_NPM:-}" ]; then`) {
		t.Error("the injection must no-op when nexus-credentials is absent rather than fail the build")
	}
	// always-auth would send Nexus credentials to npmjs for every public package.
	// Matched as an ASSIGNMENT, not as the bare word: the script carries a comment
	// explaining why the option is absent, and a substring check on "always-auth"
	// fails on that comment — a test that cannot pass however correct the code is.
	if strings.Contains(s, "always-auth=") {
		t.Error("always-auth must not be set: it would leak the registry credential to npmjs")
	}
}
