package provider_test

// Every provider's managed teardown must sweep the resources its in-cluster
// controllers created — and the two that attribute them by LIVE cluster
// membership must sweep before the cluster is destroyed.
//
// THE INVARIANT, stated precisely. A cluster's cloud footprint comes from two
// parties: the provider code, which records what it makes, and the controllers
// inside the cluster, which do not. The CSI driver provisions a volume per
// PersistentVolume; the cloud-controller-manager provisions a load balancer per
// Service of type LoadBalancer. Neither is in any tracker, so a teardown that
// does not sweep leaves them billing.
//
// The ordering requirement is NOT universal, and getting that wrong is how this
// guard was nearly written as a false alarm against Azure. It depends on how
// each cloud lets you attribute a resource to a cluster:
//
//   aws    EBS volumes and ELBs carry `kubernetes.io/cluster/<name>` tags, which
//          outlive the cluster. sweepInClusterResources can run after.
//   gcp    sweepLoadBalancers/sweepOrphanedDisks work from the recorded
//          ResourceTracker, which also outlives the cluster.
//   azure  DeleteCluster deletes the whole RESOURCE GROUP, which is itself the
//          sweep for everything inside it; the subscription-level disk sweep
//          runs afterwards on purpose, to catch disks CSI placed outside it.
//   civo   attribution is the Civo cluster UUID stamped on volumes/LBs and the
//          pool instances they attach to — ALL of which vanish with the cluster.
//   do     attribution is the node-pool droplets a volume is attached to and a
//          load balancer fronts — same thing.
//
// So Civo and DigitalOcean must gather identity and sweep BEFORE the delete.
// They were the two providers that did not sweep in managed mode at all: both
// delegate compute teardown to a path that DID sweep, which made the gap
// invisible from either half.
//
// Measured consequence (Civo mum1, 2026-10-09): after a managed `adhar down`,
// 36 pvc-* volumes totalling 317 GB remained — 36 of a 40-volume quota — and
// the next bring-up stalled with Gitea's PVCs Pending and the CSI driver
// answering `OutOfRange: Requested volume would exceed volume count limit quota
// of 40`. Confirmed against the live API: the 4 volumes of the running cluster
// carried its `cluster_id`, and all 36 leftovers carried neither a cluster_id
// nor an instance — the fingerprint of volumes whose cluster was deleted without
// sweeping them.
//
// Source-level, because none of these paths can run in CI: they delete real
// infrastructure.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// sweepCall is any call whose job is removing untracked cloud resources.
var sweepCall = regexp.MustCompile(
	`p\.(sweep\w*|purge\w*|delete\w*(?:Volume|Disk|LoadBalancer|Volumes|Disks|LoadBalancers)\w*)\(`)

// funcBody returns the source of one method, up to the next top-level func.
func funcBody(t *testing.T, path, signature string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(".", path))
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	src := string(raw)
	i := strings.Index(src, signature)
	if i < 0 {
		t.Fatalf("%s has no %q", path, signature)
	}
	body := src[i+len(signature):]
	if m := regexp.MustCompile(`\nfunc `).FindStringIndex(body); m != nil {
		body = body[:m[0]]
	}
	return body
}

// Every provider must sweep somewhere in its managed teardown.
func TestEveryManagedTeardownSweepsUntrackedResources(t *testing.T) {
	for _, tc := range []struct{ provider, file string }{
		{"aws", "aws/provider_cluster.go"},
		{"azure", "azure/provider.go"},
		{"gcp", "gcp/provider.go"},
		{"civo", "civo/provider.go"},
		{"digitalocean", "digitalocean/provider.go"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			body := funcBody(t, tc.file, "func (p *Provider) DeleteCluster(")
			if !sweepCall.MatchString(body) {
				t.Errorf("%s: DeleteCluster never sweeps untracked resources. Every CSI volume and "+
					"CCM load balancer the cluster created survives `adhar down` and keeps billing",
					tc.provider)
			}
		})
	}
}

// And the two whose attribution dies with the cluster must sweep first.
func TestAttachmentAttributedProvidersSweepBeforeDeleting(t *testing.T) {
	for _, tc := range []struct{ provider, file, clusterDelete, why string }{
		{"civo", "civo/provider.go", "DeleteKubernetesCluster(",
			"volumes and LBs are matched by the Civo cluster UUID and its pool instances"},
		{"digitalocean", "digitalocean/provider.go", "Kubernetes.Delete(",
			"volumes and LBs are matched by the cluster's node-pool droplets"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			body := funcBody(t, tc.file, "func (p *Provider) DeleteCluster(")

			sweep := sweepCall.FindStringIndex(body)
			if sweep == nil {
				t.Fatalf("%s: no sweep in DeleteCluster", tc.provider)
			}
			del := strings.Index(body, tc.clusterDelete)
			if del < 0 {
				t.Fatalf("%s: cluster-delete call %q not found — it was renamed, and this guard "+
					"can no longer verify the ordering it exists to protect",
					tc.provider, tc.clusterDelete)
			}
			if sweep[0] > del {
				t.Errorf("%s: the sweep runs AFTER %s. By then the cluster is gone and so is the "+
					"attribution — %s — so every volume becomes an unclaimed orphan and the sweep "+
					"is a no-op", tc.provider, tc.clusterDelete, tc.why)
			}
		})
	}
}
