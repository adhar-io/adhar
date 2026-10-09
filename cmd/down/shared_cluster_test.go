package down

// A cluster shared by several environments is torn down ONCE.
//
// Environments default to `isolation: namespace`: dev, test and prod live as
// namespaces on one cluster. `adhar up` knows this — partitionEnvironments and
// sharedClusterEnvironment in cmd/up/production.go provision a single cluster —
// but `adhar down` looped over every environment name and called DeleteCluster
// for each, so it attempted the same delete three times.
//
// Why that is worse than merely redundant: on a healthy account the 2nd and 3rd
// attempts find a cluster that no longer exists, and "any lookup error counts as
// already gone" is precisely the trap that once let a whole GCP cluster keep
// running behind a green teardown. Three deletes of one cluster means two of
// them are reasoning about a cluster that is already gone.
//
// Caught live on Civo (2026-10-09), where a suspended account made every delete
// fail and the single cluster id appeared three times in one error:
//
//	teardown failed for: dev: failed to delete Civo cluster dc72b5ee-…;
//	prod: failed to delete Civo cluster dc72b5ee-…;
//	test: failed to delete Civo cluster dc72b5ee-…
//
// Without the suspension the same bug would have been invisible: delete once,
// then two "not found" lookups reported as success.

import (
	"os"
	"strings"
	"testing"
)

// downSource is cmd/down/down.go with // comments stripped.
//
// Stripped because the explanation above quotes the very identifiers being
// searched for, and a positional or presence check that matches its own
// documentation is a guard that punishes writing the documentation.
func downSource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("down.go")
	if err != nil {
		t.Fatalf("reading down.go: %v", err)
	}
	var b strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func TestTeardownDeletesASharedClusterOnlyOnce(t *testing.T) {
	src := downSource(t)

	if !strings.Contains(src, "handledCluster") {
		t.Fatal("the teardown loop does not track which clusters it has already handled, so a " +
			"cluster shared by namespace-isolation environments is deleted once per environment")
	}

	// The guard has to be keyed on the resolved cluster ID. Keying on the
	// environment or the cluster NAME would miss it: the environments differ by
	// definition, and each resolves its own candidate names.
	if !strings.Contains(src, "handledCluster[found.Cluster.ID]") {
		t.Error("the dedupe is not keyed on found.Cluster.ID. The environments differ by name and " +
			"each resolves its own cluster-name candidates, so the cluster ID is the only identity " +
			"a shared cluster presents to all of them")
	}

	// And it must short-circuit BEFORE DeleteCluster, or it records the work
	// without preventing it.
	guard := strings.Index(src, "if owner, done := handledCluster[found.Cluster.ID]; done {")
	del := strings.Index(src, "found.Provider.DeleteCluster(ctx, found.Cluster.ID)")
	if guard < 0 {
		t.Fatal("no short-circuit on an already-handled cluster")
	}
	if del < 0 {
		t.Fatal("DeleteCluster call not found in down.go")
	}
	if guard > del {
		t.Error("the already-handled check runs AFTER DeleteCluster, so the duplicate delete still " +
			"happens and only the bookkeeping is fixed")
	}
}

// An environment sharing an already-torn-down cluster must still be reported as
// torn down. Leaving it out of outcome.Deleted would make `adhar down` claim it
// removed fewer environments than it did, and the final screen is what an
// operator trusts when deciding whether anything is still billing.
func TestSharedEnvironmentsAreStillReportedAsTornDown(t *testing.T) {
	src := downSource(t)
	guard := strings.Index(src, "if owner, done := handledCluster[found.Cluster.ID]; done {")
	if guard < 0 {
		t.Fatal("no short-circuit on an already-handled cluster")
	}
	// Look at the short-circuit block only.
	block := src[guard:]
	if end := strings.Index(block, "\n\t\t}"); end > 0 {
		block = block[:end]
	}
	if !strings.Contains(block, "outcome.Deleted = append(outcome.Deleted, envName)") {
		t.Error("an environment whose shared cluster was already deleted is not added to " +
			"outcome.Deleted, so the teardown summary under-reports what it removed")
	}
	if !strings.Contains(block, "continue") {
		t.Error("the short-circuit does not `continue`; it would fall through to the delete")
	}
}
