package aws

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	provider "adhar-io/adhar/platform/providers"
)

// awsMethodSource returns one Provider method's source from file, comments
// stripped, so prose describing a past mistake is not mistaken for it.
func awsMethodSource(t *testing.T, file, name string) string {
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

// On AWS the node is "ip-10-0-1-80.<region>.compute.internal" while the
// instance's Name tag is "dev-workers-1": the two never match by name, so a
// joined worker must be recognised by its address — or every worker would
// look like an orphan and be terminated.
func TestAWSWorkersAreMatchedToNodesByAddress(t *testing.T) {
	now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	prefix := "dev-workers-"
	members := map[string]NodeInfo{
		"dev-workers-1": {InstanceId: "i-1", PrivateIP: "10.0.1.80", PublicIP: "54.1.1.1", PrivateDNSName: "ip-10-0-1-80.ap-southeast-1.compute.internal", CreatedAt: now.Add(-72 * time.Hour)},
		"dev-workers-2": {InstanceId: "i-2", PrivateIP: "10.0.1.81", PrivateDNSName: "ip-10-0-1-81.ap-southeast-1.compute.internal", CreatedAt: now.Add(-3 * time.Hour)},
		"dev-workers-3": {InstanceId: "i-3", PrivateIP: "10.0.1.82", CreatedAt: now.Add(-2 * time.Minute)},
		"dev-workers-4": {InstanceId: "i-4", PrivateIP: "10.0.1.83"}, // LaunchTime unknown
		"other-group-1": {InstanceId: "i-9", PrivateIP: "10.0.1.99", CreatedAt: now.Add(-72 * time.Hour)},
	}
	vms := awsWorkerVMs(prefix, members, now)
	want := []provider.WorkerVM{
		{Name: "dev-workers-1", Addrs: []string{"10.0.1.80", "54.1.1.1", "ip-10-0-1-80.ap-southeast-1.compute.internal"}},
		{Name: "dev-workers-2", Addrs: []string{"10.0.1.81", "ip-10-0-1-81.ap-southeast-1.compute.internal"}},
		{Name: "dev-workers-3", Addrs: []string{"10.0.1.82"}, Young: true},
		{Name: "dev-workers-4", Addrs: []string{"10.0.1.83"}, Young: true},
	}
	if !reflect.DeepEqual(vms, want) {
		t.Fatalf("got %+v\nwant %+v", vms, want)
	}
	// What the control plane reports: names are EC2 hostnames, and only
	// workers-1 ever joined.
	joined := provider.JoinedNodes{
		Names:     map[string]bool{"ip-10-0-1-10.ap-southeast-1.compute.internal": true, "ip-10-0-1-80.ap-southeast-1.compute.internal": true},
		Addresses: map[string]bool{"10.0.1.10": true, "10.0.1.80": true},
	}
	plan := provider.PlanJoinedWorkers(prefix, vms, joined, 3)
	if !reflect.DeepEqual(plan.Discard, []string{"dev-workers-2"}) {
		t.Errorf("the joined worker must be kept by address and only the old unjoined one discarded; got discard %v", plan.Discard)
	}
	if len(plan.Remove) != 0 || len(plan.Add) != 0 {
		t.Errorf("1 member + 2 in flight is 3 of 3; got %+v", plan)
	}
}

func TestAWSRetiresTheNodeByItsRegisteredName(t *testing.T) {
	n := NodeInfo{PrivateDNSName: "ip-10-0-1-80.ap-southeast-1.compute.internal"}
	if got := awsNodeName("dev-workers-1", n); got != n.PrivateDNSName {
		t.Errorf("got %s; the node is registered under the private DNS name", got)
	}
	if got := awsNodeName("dev-workers-1", NodeInfo{}); got != "dev-workers-1" {
		t.Errorf("with no DNS name the tag is the only name there is; got %s", got)
	}
}

// ScaleNodeGroup needs live EC2 and SSH, so its wiring is pinned at the source.
func TestAWSScaleNodeGroupPlansAgainstJoinedNodes(t *testing.T) {
	body := awsMethodSource(t, "provider_compute.go", "ScaleNodeGroup")
	if strings.Contains(body, "provider.WorkerScalePlan(") {
		t.Error("ScaleNodeGroup still plans from the instance list (WorkerScalePlan); an unjoined instance then counts as a worker forever")
	}
	ji := strings.Index(body, "provider.KubeadmJoinedNodes(")
	pi := strings.Index(body, "provider.PlanJoinedWorkers(prefix, awsWorkerVMs(")
	if ji < 0 || pi < 0 || ji > pi {
		t.Fatal("ScaleNodeGroup must ask the control plane for its nodes and then plan with PlanJoinedWorkers over awsWorkerVMs")
	}
	d := strings.Index(body, "range plan.Discard")
	if d < 0 {
		t.Fatal("ScaleNodeGroup ignores plan.Discard; orphans would stay forever")
	}
	discard := body[d:]
	discard = discard[:strings.Index(discard, "\n\t}\n")]
	if !strings.Contains(discard, "TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{members[name].InstanceId}})") {
		t.Error("a discarded worker's instance must be terminated")
	}
	if strings.Contains(discard, "RetireWorker") {
		t.Error("a discarded worker has no node; retiring it by name could drain a real node")
	}
	j := strings.Index(body, "prepareAndJoinAWSWorker(")
	if j < 0 {
		t.Fatal("ScaleNodeGroup no longer joins through prepareAndJoinAWSWorker; this guard needs updating")
	}
	failed := body[j:]
	failed = failed[:strings.Index(failed, `return fmt.Errorf("new worker`)]
	if !strings.Contains(failed, "InstanceIds: []string{node.InstanceId}") {
		t.Error("a worker whose join failed must be terminated before returning, or it becomes the next orphan")
	}
	if !strings.Contains(body, "provider.RetireWorker(signer, awsSSHUser, masterIP, awsNodeName(name, members[name]))") {
		t.Error("a removed worker must be retired under its node name (private DNS), not its Name tag")
	}
}

// Without a LaunchTime every worker is Young and no orphan is ever discarded.
func TestAWSWorkerDiscoveryRecordsLaunchTime(t *testing.T) {
	for _, name := range []string{"nodeGroupInstances", "runWorkerInstance", "getClusterInfrastructure"} {
		if !strings.Contains(awsMethodSource(t, "provider_compute.go", name), ".LaunchTime)") {
			t.Errorf("%s builds NodeInfo without the instance's LaunchTime", name)
		}
	}
}
