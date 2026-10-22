package civo

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/civo/civogo"

	provider "adhar-io/adhar/platform/providers"
)

func civoWorker(id, host, priv string, created time.Time) civogo.Instance {
	return civogo.Instance{ID: id, Hostname: host, PrivateIP: priv, PublicIP: "74.220.0." + strings.TrimPrefix(priv, "192.168.1."), CreatedAt: created}
}

// A worker whose join failed must not count as capacity forever; one that is
// still joining, or that cannot be dated, must never be discarded.
func TestCivoWorkerVMsFeedTheJoinedPlanner(t *testing.T) {
	now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	prefix := "adhar-dev-worker-"
	workers := []civogo.Instance{
		civoWorker("i1", "adhar-dev-worker-1", "192.168.1.11", now.Add(-48*time.Hour)),
		civoWorker("i2", "adhar-dev-worker-2", "192.168.1.12", now.Add(-2*time.Hour)),   // never joined
		civoWorker("i3", "adhar-dev-worker-3", "192.168.1.13", now.Add(-5*time.Minute)), // joining
		civoWorker("i4", "adhar-dev-worker-4", "192.168.1.14", time.Time{}),             // undatable
		civoWorker("i9", "adhar-dev-gpu-1", "192.168.1.19", now.Add(-48*time.Hour)),     // another group
	}
	vms := civoWorkerVMs(prefix, workers, now)
	want := []provider.WorkerVM{
		{Name: "adhar-dev-worker-1", Addrs: []string{"192.168.1.11", "74.220.0.11"}},
		{Name: "adhar-dev-worker-2", Addrs: []string{"192.168.1.12", "74.220.0.12"}},
		{Name: "adhar-dev-worker-3", Addrs: []string{"192.168.1.13", "74.220.0.13"}, Young: true},
		{Name: "adhar-dev-worker-4", Addrs: []string{"192.168.1.14", "74.220.0.14"}, Young: true},
	}
	if !reflect.DeepEqual(vms, want) {
		t.Fatalf("got %+v\nwant %+v", vms, want)
	}
	joined := provider.JoinedNodes{
		Names:     map[string]bool{"adhar-dev-master-1": true, "adhar-dev-worker-1": true, "adhar-dev-gpu-1": true},
		Addresses: map[string]bool{"192.168.1.2": true, "192.168.1.11": true, "192.168.1.19": true},
	}
	plan := provider.PlanJoinedWorkers(prefix, vms, joined, 4)
	if !reflect.DeepEqual(plan.Discard, []string{"adhar-dev-worker-2"}) || !reflect.DeepEqual(plan.Add, []string{"adhar-dev-worker-5"}) {
		t.Fatalf("want worker-2 discarded and worker-5 added; got %+v", plan)
	}
	got := civoInstancesToDiscard(plan.Discard, workers, joined, now)
	if len(got) != 1 || got[0].ID != "i2" {
		t.Errorf("want instance i2 discarded; got %+v", got)
	}
}

// Hostnames need not be unique: a discard by NAME must never resolve to the
// instance of a live node, or to one still joining.
func TestCivoDiscardNeverResolvesToALiveOrYoungInstance(t *testing.T) {
	now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	workers := []civogo.Instance{
		civoWorker("live", "adhar-dev-worker-7", "192.168.1.17", now.Add(-48*time.Hour)),
		civoWorker("orphan", "adhar-dev-worker-7", "192.168.1.27", now.Add(-3*time.Hour)),
		civoWorker("young", "adhar-dev-worker-7", "192.168.1.37", now.Add(-time.Minute)),
		civoWorker("undated", "adhar-dev-worker-7", "192.168.1.47", time.Time{}),
	}
	joined := provider.JoinedNodes{Names: map[string]bool{"x": true}, Addresses: map[string]bool{"192.168.1.17": true}}
	got := civoInstancesToDiscard([]string{"adhar-dev-worker-7"}, workers, joined, now)
	if len(got) != 1 || got[0].ID != "orphan" {
		t.Errorf("only the old unjoined instance may be discarded; got %+v", got)
	}
}

func civoFunc(t *testing.T, file, name string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var code strings.Builder
	for _, line := range strings.Split(string(b), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		code.WriteString(line + "\n")
	}
	src := code.String()
	i := strings.Index(src, "func (p *Provider) "+name+"(")
	if i < 0 {
		t.Fatalf("%s has moved; this guard needs updating", name)
	}
	body := src[i:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}
	return body
}

// scaleComputeWorkers needs live Civo and SSH, so its wiring is pinned at the source.
func TestCivoScalePlansAgainstJoinedNodes(t *testing.T) {
	body := civoFunc(t, "compute.go", "scaleComputeWorkers")
	if strings.Contains(body, "provider.WorkerScalePlan(") {
		t.Error("scaleComputeWorkers still plans from the instance list (WorkerScalePlan); an unjoined instance then counts as a worker forever")
	}
	ji := strings.Index(body, "provider.KubeadmJoinedNodes(")
	pi := strings.Index(body, "provider.PlanJoinedWorkers(prefix, civoWorkerVMs(")
	if ji < 0 || pi < 0 || ji > pi {
		t.Fatal("scaleComputeWorkers must ask the control plane for its nodes and then plan with PlanJoinedWorkers over civoWorkerVMs")
	}
	d := strings.Index(body, "range civoInstancesToDiscard(plan.Discard")
	if d < 0 {
		t.Fatal("scaleComputeWorkers ignores plan.Discard; orphans would stay forever")
	}
	discard := body[d:]
	discard = discard[:strings.Index(discard, "\n\t}\n")]
	if !strings.Contains(discard, "p.client.DeleteInstance(inst.ID)") {
		t.Error("a discarded worker's instance must be deleted")
	}
	if strings.Contains(discard, "RetireWorker") {
		t.Error("a discarded worker has no node; retiring it by name could drain a real node")
	}
	j := strings.Index(body, "prepareAndJoinCivoWorker(")
	if j < 0 {
		t.Fatal("scaleComputeWorkers no longer joins through prepareAndJoinCivoWorker; this guard needs updating")
	}
	failed := body[j:]
	failed = failed[:strings.Index(failed, `return fmt.Errorf("new worker`)]
	if !strings.Contains(failed, "p.client.DeleteInstance(inst.ID)") {
		t.Error("a worker whose join failed must be deleted before returning, or it becomes the next orphan")
	}
}
