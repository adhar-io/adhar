package provider

import (
	"reflect"
	"testing"
)

func joinedOf(names []string, addrs ...string) JoinedNodes {
	j := JoinedNodes{Names: map[string]bool{}, Addresses: map[string]bool{}}
	for _, n := range names {
		j.Names[n] = true
	}
	for _, a := range addrs {
		j.Addresses[a] = true
	}
	return j
}

// A VM whose join failed is not capacity. Planned from VMs, "scale to 5" with
// four nodes and one such VM did nothing, every three minutes, for hours
// (DigitalOcean, 2026-10-22).
func TestAFailedJoinIsDiscardedAndReplaced(t *testing.T) {
	vms := []WorkerVM{{Name: "c-w-1"}, {Name: "c-w-2"}, {Name: "c-w-3"}, {Name: "c-w-4"}, {Name: "c-w-5"}}
	joined := joinedOf([]string{"c-master-1", "c-w-1", "c-w-2", "c-w-3", "c-w-4"})
	got := PlanJoinedWorkers("c-w-", vms, joined, 5)
	want := JoinedScalePlan{Add: []string{"c-w-6"}, Discard: []string{"c-w-5"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestAJoinInProgressCountsAndIsLeftAlone(t *testing.T) {
	vms := []WorkerVM{{Name: "c-w-1"}, {Name: "c-w-2", Young: true}}
	got := PlanJoinedWorkers("c-w-", vms, joinedOf([]string{"c-w-1"}), 2)
	if !reflect.DeepEqual(got, JoinedScalePlan{}) {
		t.Errorf("a young VM is a join in progress; got %+v", got)
	}
	// Scaling down never removes it: only joined members are retired.
	got = PlanJoinedWorkers("c-w-", vms, joinedOf([]string{"c-w-1"}), 1)
	if !reflect.DeepEqual(got, JoinedScalePlan{Remove: []string{"c-w-1"}}) {
		t.Errorf("got %+v", got)
	}
}

// EC2 node names are hostnames (ip-10-0-1-5), not instance names: a name-only
// match would call every AWS worker an orphan and delete it.
func TestMembersAreFoundByAddressWhenNamesDiffer(t *testing.T) {
	vms := []WorkerVM{{Name: "dev-workers-1", Addrs: []string{"10.0.1.5", "3.3.3.3"}}, {Name: "dev-workers-2", Addrs: []string{"10.0.1.6"}}}
	got := PlanJoinedWorkers("dev-workers-", vms, joinedOf([]string{"ip-10-0-1-5", "ip-10-0-1-6"}, "10.0.1.5", "10.0.1.6"), 3)
	if !reflect.DeepEqual(got, JoinedScalePlan{Add: []string{"dev-workers-3"}}) {
		t.Errorf("got %+v", got)
	}
}

// An unreadable control plane is "cannot tell", never "everything is orphaned".
func TestNoNodeListFallsBackToTheVMList(t *testing.T) {
	vms := []WorkerVM{{Name: "c-w-1"}, {Name: "c-w-2"}}
	got := PlanJoinedWorkers("c-w-", vms, JoinedNodes{}, 3)
	if !reflect.DeepEqual(got, JoinedScalePlan{Add: []string{"c-w-3"}}) {
		t.Errorf("got %+v", got)
	}
}

// A VM named after a live node but at another address is not that node.
func TestADuplicateNameIsNotMembership(t *testing.T) {
	vms := []WorkerVM{{Name: "c-w-4", Addrs: []string{"10.3.0.6"}}, {Name: "c-w-4", Addrs: []string{"10.3.0.7"}}}
	got := PlanJoinedWorkers("c-w-", vms, joinedOf([]string{"c-w-4"}, "10.3.0.6"), 2)
	if !reflect.DeepEqual(got, JoinedScalePlan{Add: []string{"c-w-1"}, Discard: []string{"c-w-4"}}) {
		t.Errorf("got %+v", got)
	}
}
