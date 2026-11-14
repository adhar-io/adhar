package batch

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"adhar-io/adhar/tests/e2e"
)

// The batch-volcano job runs on a push only when its path filter matches, so a
// path the filter misses is a change the canary never sees. This runs the
// filter's own expression (POSIX ERE as written, which Go's RE2 reads the same
// for these constructs) over the files the canary's verdicts depend on, and
// over files it must not wake a five-hour job for.
func TestTheVolcanoPathFilterCoversWhatTheCanaryProves(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(e2e.RepoRoot, ".github", "workflows", "e2e.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`grep -qE '([^']+)'`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("the volcano-changes job has no `grep -qE '<paths>'` filter")
	}
	filter, err := regexp.Compile(string(m[1]))
	if err != nil {
		t.Fatalf("the path filter %q is not a regular expression Go can read: %v", m[1], err)
	}
	for _, p := range []string{
		"platform/stack/packages/core/volcano/manifests/install.yaml",
		"platform/stack/packages/core/adhar-batch/manifests/priorityclasses.yaml",
		"platform/stack/packages/core/adhar-batch/manifests/volcano/kyverno-queue.yaml",
		// DEC-4: whether the two batch policies fail closed is a Kyverno chart value.
		"platform/stack/packages/security/kyverno/values.yaml",
		"platform/controlplane/configuration/compositions/batchjob/kubernetes.yaml",
		"platform/controlplane/configuration/compositions/team/local.yaml",
		"platform/controlplane/configuration/xrd/batchjob.xrd.yaml",
		"platform/controlplane/configuration/xrd/team.xrd.yaml",
		"platform/controllers/autoscaler/decision.go",
		"tests/e2e/batch/volcano_e2e_test.go",
		"cmd/batch/run.go",
		"Makefile",
		".github/workflows/e2e.yaml",
	} {
		if _, err := os.Stat(filepath.Join(e2e.RepoRoot, p)); err != nil {
			t.Errorf("%s: the list below the filter names a file that is gone: %v", p, err)
		}
		if !filter.MatchString(p) {
			t.Errorf("a change to %s does not run the Volcano canary", p)
		}
	}
	for _, p := range []string{
		"docs/design/0025-batch-platform.md",
		"platform/stack/packages/data/cnpg/manifests/install.yaml",
		"platform/controlplane/configuration/xrd/database.xrd.yaml",
		"tests/e2e/bootstrap/bootstrap_test.go",
		// The filter is anchored: a path that merely contains a watched one is not it.
		"docs/examples/tests/e2e/batch/notes.md",
	} {
		if filter.MatchString(p) {
			t.Errorf("a change to %s runs the five-hour Volcano canary", p)
		}
	}
}
