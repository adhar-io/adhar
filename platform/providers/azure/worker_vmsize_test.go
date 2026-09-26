package azure

import "testing"

// A worker added by the autoscaler must match the node group it joins.
// Reading the provider-level VMSize instead gave every scaled worker the
// CONTROL PLANE's size: a cluster built with 4-vCPU Standard_E4bds_v5 workers
// grew 2-vCPU Standard_E2bds_v5 ones, so each new node brought half the CPU
// and — because an Azure VM's data-disk budget scales with its size — half the
// volume attach slots (4 rather than 8) that the scale-up was counting on.
func TestScaledWorkerTakesTheNodeGroupSizeNotTheControlPlaneSize(t *testing.T) {
	const prefix = "dev-worker-workers-"
	vms := []string{"dev-master-0", prefix + "0", prefix + "1"}
	sizes := map[string]string{
		"dev-master-0": "Standard_E2bds_v5",
		prefix + "0":   "Standard_E4bds_v5",
		prefix + "1":   "Standard_E4bds_v5",
	}
	got := pickWorkerVMSize(vms, prefix, "Standard_E2bds_v5", func(n string) string { return sizes[n] })
	if got != "Standard_E4bds_v5" {
		t.Fatalf("scaled worker size = %q, want the node group's Standard_E4bds_v5", got)
	}
}

func TestWorkerVMSizeFallsBackOnlyWhenTheGroupIsEmpty(t *testing.T) {
	got := pickWorkerVMSize([]string{"dev-master-0"}, "dev-worker-workers-", "Standard_E2bds_v5",
		func(string) string { return "Standard_E2bds_v5" })
	if got != "Standard_E2bds_v5" {
		t.Fatalf("empty node group should fall back to the configured size, got %q", got)
	}
}

// An unreadable member (a VM mid-delete) must not decide the size on its own.
func TestWorkerVMSizeSkipsMembersThatReportNoSize(t *testing.T) {
	const prefix = "dev-worker-workers-"
	sizes := map[string]string{prefix + "0": "", prefix + "1": "Standard_E4bds_v5"}
	got := pickWorkerVMSize([]string{prefix + "0", prefix + "1"}, prefix, "Standard_E2bds_v5",
		func(n string) string { return sizes[n] })
	if got != "Standard_E4bds_v5" {
		t.Fatalf("got %q, want the size of the member that could be read", got)
	}
}
