package digitalocean

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/digitalocean/godo"

	provider "adhar-io/adhar/platform/providers"
)

func droplet(id int, name, priv, created string) godo.Droplet {
	return godo.Droplet{ID: id, Name: name, Created: created, Networks: &godo.Networks{V4: []godo.NetworkV4{
		{IPAddress: priv, Type: "private"},
		{IPAddress: "203.0.113." + strings.TrimPrefix(priv, "10.3.0."), Type: "public"},
	}}}
}

func names(ds []godo.Droplet) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Name)
	}
	return out
}

// The DigitalOcean cluster of 2026-10-22: nodes workers-1, -2, -4 (10.3.0.6)
// and -5, plus a droplet ALSO named workers-4 (10.3.0.7) whose join failed on
// the duplicate name an hour earlier. Counting droplets made every "scale to
// 5" a silent no-op while builds waited for memory.
func TestTheOrphanIsFoundByAddressNotName(t *testing.T) {
	now := time.Date(2026, 10, 22, 8, 10, 0, 0, time.UTC)
	old := "2026-10-22T06:59:00Z"
	workers := []godo.Droplet{
		droplet(1, "adhar-adhar-workers-1", "10.3.0.3", "2026-10-22T06:38:00Z"),
		droplet(2, "adhar-adhar-workers-2", "10.3.0.4", "2026-10-22T06:38:00Z"),
		droplet(4, "adhar-adhar-workers-4", "10.3.0.6", "2026-10-22T06:38:00Z"),
		droplet(7, "adhar-adhar-workers-4", "10.3.0.7", old),
		droplet(8, "adhar-adhar-workers-5", "10.3.0.8", "2026-10-22T07:05:00Z"),
	}
	joined := provider.JoinedNodes{
		Names:     map[string]bool{"adhar-adhar-master-1": true, "adhar-adhar-workers-1": true, "adhar-adhar-workers-2": true, "adhar-adhar-workers-4": true, "adhar-adhar-workers-5": true},
		Addresses: map[string]bool{"10.3.0.2": true, "10.3.0.3": true, "10.3.0.4": true, "10.3.0.6": true, "10.3.0.8": true},
	}
	s := reconcileWorkers(workers, joined, now)
	if len(s.Members) != 4 || len(s.Stale) != 1 || s.Stale[0].ID != 7 || len(s.InFlight) != 0 {
		t.Fatalf("want 4 members and droplet 7 stale; got members %v, stale %v, in flight %v", names(s.Members), names(s.Stale), names(s.InFlight))
	}
	// The same orphan, still within the grace period, is a join in progress:
	// counted, never discarded.
	young := reconcileWorkers(workers, joined, time.Date(2026, 10, 22, 7, 5, 0, 0, time.UTC))
	if len(young.InFlight) != 1 || young.InFlight[0].ID != 7 || len(young.Stale) != 0 {
		t.Errorf("a droplet younger than the grace period must be in flight; got %+v", young)
	}
	// No node list means "could not ask", not "everything is an orphan".
	if blind := reconcileWorkers(workers, provider.JoinedNodes{}, now); len(blind.Members) != 5 || len(blind.Stale) != 0 {
		t.Errorf("an unreadable control plane must leave every droplet a member; got %+v", blind)
	}
}

// Numbering from the droplet COUNT reused workers-4 when workers-3 was gone.
func TestNewWorkersTakeNamesNothingUses(t *testing.T) {
	inUse := []string{"adhar-adhar-workers-1", "adhar-adhar-workers-2", "adhar-adhar-workers-4", "adhar-adhar-workers-5", "adhar-adhar-master-1"}
	if got := nextWorkerNames("adhar-adhar-workers", inUse, 2); !reflect.DeepEqual(got, []string{"adhar-adhar-workers-6", "adhar-adhar-workers-7"}) {
		t.Errorf("got %v", got)
	}
	if got := nextWorkerNames("adhar-adhar-workers", nil, 1); !reflect.DeepEqual(got, []string{"adhar-adhar-workers-1"}) {
		t.Errorf("first worker got %v", got)
	}
	// Another group's indices are not this group's.
	if got := nextWorkerNames("adhar-adhar-gpu", inUse, 1); !reflect.DeepEqual(got, []string{"adhar-adhar-gpu-1"}) {
		t.Errorf("gpu group got %v", got)
	}
}

func TestScaleDownRetiresTheHighestIndexFirst(t *testing.T) {
	ds := sortedByIndex([]godo.Droplet{{Name: "w-10"}, {Name: "w-9"}, {Name: "w-2"}})
	if got := names(ds); !reflect.DeepEqual(got, []string{"w-2", "w-9", "w-10"}) {
		t.Errorf("got %v", got)
	}
}

// The scale path must use the reconciliation, the unique names, and discard
// a droplet whose join failed — each was missing on 2026-10-22.
func TestScaleComputeWorkersReconcilesBeforeDeciding(t *testing.T) {
	b, err := os.ReadFile("compute.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "func (p *Provider) scaleComputeWorkers(")
	j := strings.Index(src[i:], "\n}\n")
	fn := src[i : i+j]
	for _, want := range []string{
		"provider.KubeadmJoinedNodes(signer, computeSSHUser, masterIP)",
		"reconcileWorkers(workers, joined, time.Now())",
		"current := len(set.Members) + len(set.InFlight)",
		"nextWorkerNames(",
		"p.client.Droplets.Delete(ctx, d.ID)",
		"p.retireComputeWorker(ctx, signer, masterIP, set.Members[i])",
	} {
		if !strings.Contains(fn, want) {
			t.Errorf("scaleComputeWorkers lacks %q", want)
		}
	}
	if strings.Contains(fn, "current := len(workers)") {
		t.Error("scaleComputeWorkers counts droplets again; a droplet that never joined is not a worker")
	}
}
