package adharplatform

// Guards added by the docs lane's verification (2026-11-14): two places where the batch
// design described less than the tree ships. Each was mutation-tested (the design row
// removed, the test seen failing, restored).

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Design §11.3 is the alert catalogue an on-call engineer reads. Every cluster-wide
// batch queue alert that observability/kube-prometheus ships must be a row there, or
// the page it sends names an alert the design never explains.
func TestBatchDesignDocListsEveryVolcanoQueueAlert(t *testing.T) {
	alerts := batchDocSection(t, batchDoc(t, batchDesignDoc), "### 11.3 ")
	rules := readStackFile(t, "observability/kube-prometheus/manifests/volcano-alerts.yaml")
	names := regexp.MustCompile(`(?m)^\s*-\s*alert:\s*(\S+)\s*$`).FindAllStringSubmatch(rules, -1)
	if len(names) == 0 {
		t.Fatal("observability/kube-prometheus/manifests/volcano-alerts.yaml declares no alerts; the file layout changed")
	}
	for _, m := range names {
		if batchDocTableRow(alerts, "`"+m[1]+"`") == nil {
			t.Errorf("design 0025 §11.3 has no row for %s, which observability/kube-prometheus ships", m[1])
		}
	}
}

// D18 turned Kyverno's platform-wide fail-open override off. A ClusterPolicy that a
// composition emits without `failurePolicy: Ignore` therefore fails CLOSED now; while
// any composition does, the design's drift table must name it, so "every other policy
// fails open" is never read as true for the composed ones.
func TestBatchDesignDocRecordsComposedPoliciesThatFailClosed(t *testing.T) {
	drift := batchDocSection(t, batchDoc(t, batchDesignDoc), "## 19. ")
	root := filepath.Join(stackRoot(t), "..", "controlplane", "configuration", "compositions")
	declares := regexp.MustCompile(`(?m)^\s*apiVersion\s*[:=]\s*"?kyverno\.io/v1"?\s*$`)
	states := regexp.MustCompile(`(?m)^\s*failurePolicy\s*[:=]\s*"?Ignore"?\s*$`)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if len(declares.FindAll(b, -1)) > len(states.FindAll(b, -1)) {
			rel, _ := filepath.Rel(root, path)
			if !strings.Contains(drift, filepath.ToSlash(rel)) {
				t.Errorf("compositions/%s emits a Kyverno policy with no failurePolicy: Ignore (Fail since D18), and design 0025 §19 does not record it", filepath.ToSlash(rel))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// §5.10 is the kit's side of the partition channel (KIT-2.x builds to it). Every
// ADHAR_BATCH_PARTITION_* variable the BatchJob composition sets on a partitioned run
// must be named there, or the kit lane guesses what the platform passes.
func TestBatchDesignDocNamesThePartitionEnvTheCompositionSets(t *testing.T) {
	partitioning := batchDocSection(t, batchDoc(t, batchDesignDoc), "### 5.10 ")
	composition := readRepoFile(t, "platform", "controlplane", "configuration", "compositions", "batchjob", "kubernetes.yaml")
	vars := regexp.MustCompile(`"(ADHAR_BATCH_PARTITION_[A-Z_]+)"`).FindAllStringSubmatch(composition, -1)
	if len(vars) == 0 {
		t.Fatal("compositions/batchjob/kubernetes.yaml sets no ADHAR_BATCH_PARTITION_* variable; the composition changed")
	}
	for _, m := range vars {
		if !strings.Contains(partitioning, "`"+m[1]) {
			t.Errorf("design 0025 §5.10 does not name %s, which the BatchJob composition sets on partitioned runs", m[1])
		}
	}
}
