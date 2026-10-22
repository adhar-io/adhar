package gcp

import (
	"reflect"
	"strings"
	"testing"
	"time"

	provider "adhar-io/adhar/platform/providers"
)

func TestGCPCreationTimeParsesWhatGCPReports(t *testing.T) {
	got := gcpCreationTime("2026-10-22T01:02:03.456-07:00")
	if want := time.Date(2026, 10, 22, 8, 2, 3, 456e6, time.UTC); !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for _, s := range []string{"", "yesterday"} {
		if !gcpCreationTime(s).IsZero() {
			t.Errorf("%q must parse as unknown (zero)", s)
		}
	}
}

// A worker whose join failed must not be counted as capacity forever, and a
// worker that cannot be dated must never be discarded.
func TestGCPWorkerVMsFeedTheJoinedPlanner(t *testing.T) {
	now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	prefix := "prod-worker-workers-"
	nodes := []NodeInfo{
		{InstanceName: "prod-worker-workers-1", PrivateIP: "10.0.0.11", PublicIP: "34.1.1.1", CreatedAt: now.Add(-48 * time.Hour)},
		{InstanceName: "prod-worker-workers-2", PrivateIP: "10.0.0.12", CreatedAt: now.Add(-2 * time.Hour)},   // old, never joined
		{InstanceName: "prod-worker-workers-3", PrivateIP: "10.0.0.13", CreatedAt: now.Add(-5 * time.Minute)}, // joining
		{InstanceName: "prod-worker-workers-4", PrivateIP: "10.0.0.14"},                                       // undatable, not a node
		{InstanceName: "prod-worker-gpu-1", PrivateIP: "10.0.0.21", CreatedAt: now.Add(-48 * time.Hour)},      // another group
	}
	vms := gcpWorkerVMs(prefix, nodes, now)
	want := []provider.WorkerVM{
		{Name: "prod-worker-workers-1", Addrs: []string{"10.0.0.11", "34.1.1.1"}, Young: false},
		{Name: "prod-worker-workers-2", Addrs: []string{"10.0.0.12"}, Young: false},
		{Name: "prod-worker-workers-3", Addrs: []string{"10.0.0.13"}, Young: true},
		{Name: "prod-worker-workers-4", Addrs: []string{"10.0.0.14"}, Young: true},
	}
	if !reflect.DeepEqual(vms, want) {
		t.Fatalf("got %+v\nwant %+v", vms, want)
	}
	joined := provider.JoinedNodes{
		Names:     map[string]bool{"prod-master-1": true, "prod-worker-workers-1": true},
		Addresses: map[string]bool{"10.0.0.2": true, "10.0.0.11": true},
	}
	plan := provider.PlanJoinedWorkers(prefix, vms, joined, 4)
	if !reflect.DeepEqual(plan.Discard, []string{"prod-worker-workers-2"}) {
		t.Errorf("only the old unjoined worker may be discarded; got %v", plan.Discard)
	}
	// 1 member + 2 in flight = 3 of 4: one new worker, at an index no VM uses.
	if !reflect.DeepEqual(plan.Add, []string{"prod-worker-workers-5"}) {
		t.Errorf("want one new worker workers-5; got %v", plan.Add)
	}
}

func TestWithoutWorkerLeavesTheCallersListIntact(t *testing.T) {
	nodes := []NodeInfo{{InstanceName: "a", Zone: "z1"}, {InstanceName: "b", Zone: "z2"}, {InstanceName: "c"}}
	kept, zone := withoutWorker(nodes, "b", "def")
	if zone != "z2" || len(kept) != 2 || kept[0].InstanceName != "a" || kept[1].InstanceName != "c" {
		t.Errorf("got %+v in %s", kept, zone)
	}
	if nodes[1].InstanceName != "b" {
		t.Errorf("the original list was mutated: %+v", nodes)
	}
	if _, zone := withoutWorker(nodes, "c", "def"); zone != "def" {
		t.Errorf("an instance with no zone falls back to the provider's; got %s", zone)
	}
}

// scaleWorkers needs live GCP and SSH, so its wiring is pinned at the source.
func TestScaleWorkersPlansAgainstJoinedNodes(t *testing.T) {
	body := gcpFunc(t, "scaleWorkers")
	if strings.Contains(body, "provider.WorkerScalePlan(") {
		t.Error("scaleWorkers still plans from the instance list (WorkerScalePlan); an unjoined instance then counts as a worker forever")
	}
	ji := strings.Index(body, "provider.KubeadmJoinedNodes(")
	pi := strings.Index(body, "provider.PlanJoinedWorkers(")
	if ji < 0 || pi < 0 || ji > pi {
		t.Fatal("scaleWorkers must ask the control plane for its nodes (KubeadmJoinedNodes) and then plan with PlanJoinedWorkers")
	}
	if !strings.Contains(body, "gcpWorkerVMs(prefix, infra.WorkerNodes") {
		t.Error("scaleWorkers must build the planner's VMs with gcpWorkerVMs")
	}
	d := strings.Index(body, "range plan.Discard")
	if d < 0 {
		t.Fatal("scaleWorkers ignores plan.Discard; orphans would stay forever")
	}
	discard := body[d:]
	discard = discard[:strings.Index(discard, "\n\t}\n")]
	if !strings.Contains(discard, "p.deleteInstance(ctx, name") {
		t.Error("a discarded worker's instance must be deleted")
	}
	if strings.Contains(discard, "RetireWorker") {
		t.Error("a discarded worker has no node; retiring it by name could drain a real node")
	}
	if !strings.Contains(discard, "infra.WorkerNodes = kept") {
		t.Error("a discarded worker must leave the recorded worker list")
	}
	j := strings.Index(body, "prepareAndJoinGCPWorker(")
	if j < 0 {
		t.Fatal("scaleWorkers no longer joins through prepareAndJoinGCPWorker; this guard needs updating")
	}
	failed := body[j:]
	failed = failed[:strings.Index(failed, `return fmt.Errorf("new worker`)]
	if !strings.Contains(failed, "p.deleteInstance(ctx, name, nodeInfo.Zone)") {
		t.Error("a worker whose join failed must be deleted before returning, or it becomes the next orphan")
	}
}

// Without a creation time every worker is Young and no orphan is ever discarded.
func TestGCPWorkerDiscoveryRecordsCreationTime(t *testing.T) {
	if !strings.Contains(gcpFunc(t, "getClusterInfrastructure"), `Role: "worker", CreatedAt: created`) {
		t.Error("discovered workers carry no creation time")
	}
	if !strings.Contains(gcpFunc(t, "createComputeInstance"), "CreatedAt:    gcpCreationTime(instance.GetCreationTimestamp())") {
		t.Error("a created instance records no creation time")
	}
	body := gcpFunc(t, "scaleWorkers")
	if d, p := strings.Index(body, "p.dateWorkers(ctx, prefix, infra.WorkerNodes)"), strings.Index(body, "provider.PlanJoinedWorkers("); d < 0 || d > p {
		t.Error("workers recorded without a creation time must be dated before planning")
	}
}
