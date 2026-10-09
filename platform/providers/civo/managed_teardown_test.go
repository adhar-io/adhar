package civo

// `adhar down` on a MANAGED cluster must remove the cluster's own volumes and
// load balancers, not just the cluster.
//
// Both sweeps were written for the compute teardown and wired only there. The
// managed DeleteCluster deleted the Civo cluster, waited, cleared local state
// and returned — so on the one mode Civo actually ships in, every CSI volume
// and every CCM load balancer survived `adhar down`. Measured on mum1
// (2026-10-09): 36 pvc-* volumes / 317 GB left on the account after a managed
// teardown, 36 of a 40-volume quota, and the next bring-up stalled with Gitea's
// PVCs Pending and `OutOfRange: Requested volume would exceed volume count
// limit quota of 40` — which reads like a storage fault and is a teardown fault.
//
// The predicates are pure functions over the API's own structs precisely so they
// can be tested without a Civo account, which is the only way this path gets
// tested at all.

import (
	"os"
	"strings"
	"testing"

	"github.com/civo/civogo"
)

func managedCluster() *civogo.KubernetesCluster {
	return &civogo.KubernetesCluster{
		ID:       "d68dcafe-0000-4000-8000-000000000001",
		Name:     "adhar",
		MasterIP: "212.2.253.56",
		Pools: []civogo.KubernetesPool{{
			ID:            "pool-1",
			InstanceNames: []string{"k3s-adhar-node-1", "k3s-adhar-node-2"},
			Instances: []civogo.KubernetesInstance{
				{ID: "inst-1", Hostname: "k3s-adhar-node-1", PublicIP: "212.2.253.56"},
				{ID: "inst-2", Hostname: "k3s-adhar-node-2"},
			},
		}},
	}
}

// The identity has to carry the Civo cluster UUID: that is what the CSI driver
// stamps on every volume and the CCM on every load balancer, and it is what
// turns a managed sweep from a guess into a positive match.
func TestManagedIdentityCarriesTheCivoClusterID(t *testing.T) {
	p := &Provider{}
	id := p.managedClusterIdentity(managedCluster())

	if id.civoClusterID != "d68dcafe-0000-4000-8000-000000000001" {
		t.Errorf("civoClusterID = %q, want the Civo cluster UUID", id.civoClusterID)
	}
	if got := id.clusterIDForMatching(); got != id.civoClusterID {
		t.Errorf("clusterIDForMatching() = %q, want the UUID %q", got, id.civoClusterID)
	}
	for _, want := range []string{"inst-1", "inst-2"} {
		if !id.instanceIDs[want] {
			t.Errorf("instance %s missing from the identity; a volume attached to it would not be attributed", want)
		}
	}
	if !id.instanceNames["k3s-adhar-node-1"] || !id.instanceIPs["212.2.253.56"] {
		t.Error("pool instance names / master IP missing; the load-balancer sweep matches on those")
	}
}

// A compute cluster is not a Civo "cluster" and has no UUID — matching must
// fall back to the name, which is what the compute path has always passed.
func TestComputeIdentityStillMatchesOnName(t *testing.T) {
	p := &Provider{}
	id := p.clusterIdentityFor("adhar-dev", []civogo.Instance{{ID: "i-1", Hostname: "h1"}})
	if id.civoClusterID != "" {
		t.Errorf("compute identity should carry no Civo cluster UUID, got %q", id.civoClusterID)
	}
	if got := id.clusterIDForMatching(); got != "adhar-dev" {
		t.Errorf("clusterIDForMatching() = %q, want the cluster name for compute", got)
	}
}

// THE HEADLINE: a managed cluster's own volumes must be deleted WITHOUT the
// operator passing --purge-orphaned-volumes, because they are positively
// identified rather than inferred. Requiring the flag is what left 317 GB
// behind — the flag exists for volumes whose cluster is already gone.
func TestManagedClusterVolumesGoWithoutThePurgeFlag(t *testing.T) {
	p := &Provider{}
	id := p.managedClusterIdentity(managedCluster())

	cases := []struct {
		name string
		vol  civogo.Volume
		want volumeAction
		why  string
	}{
		{"stamped with this cluster's id",
			civogo.Volume{Name: "pvc-aaa", ID: "v1", ClusterID: id.civoClusterID},
			volumeDelete, "the CSI driver stamps ClusterID; this is ours"},
		{"attached to one of its nodes",
			civogo.Volume{Name: "pvc-bbb", ID: "v2", InstanceID: "inst-1"},
			volumeDelete, "attached to a pool instance of this cluster"},
		{"another cluster's volume",
			civogo.Volume{Name: "pvc-ccc", ID: "v3", ClusterID: "some-other-cluster"},
			volumeKeep, "never touch another cluster's storage"},
		{"someone else's instance",
			civogo.Volume{Name: "pvc-ddd", ID: "v4", InstanceID: "inst-elsewhere"},
			volumeKeep, "attached outside this cluster"},
		{"a root disk",
			civogo.Volume{Name: "pvc-eee", ID: "v5", Bootable: true, ClusterID: id.civoClusterID},
			volumeKeep, "bootable root disks go with their instance"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// purgeOrphans = FALSE: the default teardown, no flag.
			got, why := volumeDisposition(tc.vol, id.clusterIDForMatching(), id.instanceIDs, false)
			if got != tc.want {
				t.Errorf("volumeDisposition = %v (%s), want %v — %s", got, why, tc.want, tc.why)
			}
		})
	}
}

// And its load balancers, matched on the same UUID.
func TestManagedClusterLoadBalancersAreAttributed(t *testing.T) {
	p := &Provider{}
	id := p.managedClusterIdentity(managedCluster())

	ours := civogo.LoadBalancer{ID: "lb1", Name: "anything", ClusterID: id.civoClusterID}
	if !loadBalancerBelongsToCluster(ours, id) {
		t.Error("a load balancer stamped with this cluster's UUID was not attributed to it; " +
			"it would be left behind holding a reference to the network")
	}
	theirs := civogo.LoadBalancer{ID: "lb2", Name: "other", ClusterID: "another-cluster"}
	if loadBalancerBelongsToCluster(theirs, id) {
		t.Error("another cluster's load balancer was attributed to this one — deleting it would " +
			"take that cluster's traffic down")
	}
}

// The sweep MUST run before DeleteKubernetesCluster. After it, the cluster
// record is gone: no UUID, no pool instances, no master IP, so nothing can be
// attributed and every volume looks like an orphan. Source guard, because
// p.client is a concrete *civogo.Client and ordering cannot be observed without
// a client interface.
func TestManagedTeardownSweepsBeforeDeletingTheCluster(t *testing.T) {
	raw, err := os.ReadFile("provider.go")
	if err != nil {
		t.Fatalf("reading provider.go: %v", err)
	}
	src := string(raw)
	start := strings.Index(src, "func (p *Provider) DeleteCluster(")
	if start < 0 {
		t.Fatal("DeleteCluster is gone from provider.go")
	}
	body := src[start:]
	if end := strings.Index(body, "\n// resolveCivoClusterID"); end > 0 {
		body = body[:end]
	}

	sweep := strings.Index(body, "sweepManagedClusterResources(")
	del := strings.Index(body, "DeleteKubernetesCluster(")
	if sweep < 0 {
		t.Fatal("the managed DeleteCluster does not call sweepManagedClusterResources: it deletes " +
			"the cluster and leaves every CSI volume and CCM load balancer billing")
	}
	if del < 0 {
		t.Fatal("DeleteKubernetesCluster call not found")
	}
	if sweep > del {
		t.Error("sweepManagedClusterResources runs AFTER DeleteKubernetesCluster. By then the " +
			"cluster record is gone, so no volume or load balancer can be attributed and the " +
			"sweep is a no-op — the exact bug this ordering fixes")
	}
}
