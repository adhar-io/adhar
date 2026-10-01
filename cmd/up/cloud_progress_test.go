package up

import (
	"strings"
	"testing"
)

// The controller-driven stages are advanced by index (pollPlatformStages does
// Done(base), Done(base+1), …), so the base and the stage list have to agree. If
// someone inserts a stage above the base, the poller silently ticks the wrong
// lines — Cilium's tick landing on "Kubeconfig & TLS" and so on — and the
// checklist lies without failing.
func TestCloudControllerStageBasePointsAtTheFirstControllerStage(t *testing.T) {
	stages := cloudStages("adhar", "digitalocean", "blr1")
	if got := stages[cloudControllerStageBase].Label; got != "Cilium & Gateway" {
		t.Fatalf("stage[%d] = %q, want the first controller-driven stage", cloudControllerStageBase, got)
	}
}

// The tail must be in the exact order pollPlatformStages advances it.
func TestCloudStagesTailMatchesThePollOrder(t *testing.T) {
	want := []string{"Cilium & Gateway", "ArgoCD", "Gitea", "GitOps repos", "Crossplane", "GitOps sync - platform stack"}
	stages := cloudStages("adhar", "aws", "ap-southeast-1")
	tail := stages[cloudControllerStageBase:]
	if len(tail) != len(want) {
		t.Fatalf("controller-driven tail has %d stages, want %d", len(tail), len(want))
	}
	if len(want) != ControllerStageCount {
		t.Fatalf("ControllerStageCount is %d but the poll advances %d stages", ControllerStageCount, len(want))
	}
	for i, w := range want {
		if tail[i].Label != w {
			t.Errorf("stage[%d] = %q, want %q", cloudControllerStageBase+i, tail[i].Label, w)
		}
	}
}

// Both paths do exactly three things before the controller takes over, so the two
// bases coincide. Kept as a test rather than one shared constant because the
// equality is a fact about the two checklists, not a requirement — if a path grows
// a fourth pre-stage, this should fail and be updated, not silently drag the other.
func TestLocalAndCloudBasesAgree(t *testing.T) {
	if localControllerStageBase != cloudControllerStageBase {
		t.Errorf("local base %d != cloud base %d; pollPlatformStages is called with each, so check both call sites",
			localControllerStageBase, cloudControllerStageBase)
	}
}

// The cluster stage names WHAT is being built and WHERE, because the log line that
// used to say it ("Creating cluster 'adhar' … using digitalocean provider in region
// blr1") was removed as a duplicate of this checklist. The cluster name matters
// most: since `--name` exists it is no longer implied by the environment.
func TestCloudClusterStageNamesTheTarget(t *testing.T) {
	got := cloudStages("adhar", "digitalocean", "blr1")[1].Detail
	for _, want := range []string{"adhar", "digitalocean", "blr1"} {
		if !strings.Contains(got, want) {
			t.Errorf("cluster stage detail = %q, want it to name %q", got, want)
		}
	}
	// Missing parts must not leave dangling separators.
	if got := clusterTargetLabel("adhar", "kind", ""); got != "adhar · kind" {
		t.Errorf("label = %q, want no trailing separator when the region is absent", got)
	}
	if got := clusterTargetLabel("", "kind", ""); got != "kind" {
		t.Errorf("label = %q, want just the provider", got)
	}
}

// The GitOps tick is captioned, because `adhar up` now hands over as soon as the
// console serves — so that stage is routinely ticked with a tail still in flight,
// and a bare tick beside "26/75" would read as a completed sync.
func TestFinalGitOpsDetailDoesNotClaimACompleteSync(t *testing.T) {
	if got := finalGitOpsDetail(26, 75); !strings.Contains(got, "background") {
		t.Errorf("a partial sync must say it continues: %q", got)
	}
	if got := finalGitOpsDetail(75, 75); strings.Contains(got, "background") {
		t.Errorf("a complete sync must not claim to still be running: %q", got)
	}
	if got := finalGitOpsDetail(75, 75); !strings.Contains(got, "75/75") {
		t.Errorf("a complete sync should still show the count: %q", got)
	}
	// Nothing reported at all is its own case: 0/0 is not a finished sync.
	if got := finalGitOpsDetail(0, 0); strings.Contains(got, "0/0") {
		t.Errorf("no reported apps should not render as a ratio: %q", got)
	}
}

// The kube-context must not stutter. `adhar up` names the cluster `adhar` by
// default, and prefixing that unconditionally produced context "adhar-adhar" —
// which reads as a bug in whatever generated it.
func TestKubeContextNameDoesNotStutter(t *testing.T) {
	if got := KubeContextName("adhar"); got != "adhar" {
		t.Errorf("KubeContextName(%q) = %q, want no prefix when the name already is the platform's", "adhar", got)
	}
	// A custom --name still gets the prefix, so a platform cluster stays
	// recognisable among an engineer's other contexts.
	if got := KubeContextName("prod-eu"); got != "adhar-prod-eu" {
		t.Errorf("KubeContextName(%q) = %q, want the platform prefix", "prod-eu", got)
	}
}
