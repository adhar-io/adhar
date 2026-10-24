package gcp

import (
	"testing"

	"cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/protobuf/proto"

	"adhar-io/adhar/platform/types"
)

func TestGCPSizingFlowsFromTheProviderBlock(t *testing.T) {
	cfg, err := parseProviderConfig(map[string]interface{}{
		"region": "asia-southeast1",
		"config": map[string]interface{}{
			"zone":                    "asia-southeast1-b",
			"machine_type":            "e2-standard-4",
			"controlplanemachinetype": "n2-standard-4",
			"workermachinetype":       "n2-highmem-8",
			"disk_size_gb":            200,
			"disk_type":               "pd-balanced",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.controlPlaneSize(&types.ClusterSpec{}); got != "n2-standard-4" {
		t.Errorf("control plane = %s", got)
	}
	if got := cfg.workerSize("", "", false); got != "n2-highmem-8" {
		t.Errorf("worker = %s", got)
	}
	if got := (&Provider{config: cfg}).gkeNodePool(&types.NodeGroupSpec{Name: "w"}); got.Config.MachineType != "n2-highmem-8" {
		t.Errorf("GKE pool = %s, want the configured worker size", got.Config.MachineType)
	}
	if cfg.DiskSize != 200 || cfg.DiskType != "pd-balanced" || cfg.Zone != "asia-southeast1-b" {
		t.Errorf("disk/zone = %d %s %s", cfg.DiskSize, cfg.DiskType, cfg.Zone)
	}
	// The environment's diskSizeGb (the spelling every other provider uses) is read.
	env, _ := parseProviderConfig(map[string]interface{}{"diskSizeGb": "300"})
	if env.DiskSize != 300 {
		t.Errorf("diskSizeGb ignored: %d", env.DiskSize)
	}
}

func TestGCPDefaultsOnlyWhenUnset(t *testing.T) {
	cfg, err := parseProviderConfig(map[string]interface{}{"region": "us-central1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.controlPlaneSize(nil); got != "e2-standard-4" {
		t.Errorf("default control plane = %s (was e2-standard-2)", got)
	}
	if got := cfg.workerSize("", "", false); got != "e2-standard-4" {
		t.Errorf("default worker = %s (was e2-standard-2)", got)
	}
	// 1 control plane + 2 workers must fit a new project's 12-vCPU cap.
	spec := &types.ClusterSpec{NodeGroups: []types.NodeGroupSpec{{Name: "workers", Replicas: 2}}}
	if cpus, shape, ok := cfg.plannedCPUs(spec); !ok || cpus != 12 {
		t.Errorf("default cluster = %d vCPU (%s), want 12", cpus, shape)
	}
}

// The quota preflight measures each node at its own size.
func TestGCPQuotaCountsEachRoleAtItsOwnSize(t *testing.T) {
	cfg := &Config{ControlPlaneMachineType: "e2-standard-4", WorkerMachineType: "e2-standard-8"}
	spec := &types.ClusterSpec{NodeGroups: []types.NodeGroupSpec{
		{Name: "workers", Replicas: 3},
		{Name: "highmem", Replicas: 1, InstanceType: "n2-highmem-16"},
	}}
	cpus, _, ok := cfg.plannedCPUs(spec)
	if !ok || cpus != 4+3*8+16 {
		t.Errorf("planned %d vCPU, want %d", cpus, 4+3*8+16)
	}
}

// Rediscovery (the adhar-controlplane autoscaler has no state file) must find the
// workers this provider actually names, <cluster>-worker-<group>-<n>, with the
// machine type each one RUNS — a scale-up copies it.
func TestGCPRediscoveredWorkersCarryTheirMachineType(t *testing.T) {
	inst := func(name, mt string, tags ...string) *computepb.Instance {
		return &computepb.Instance{
			Name:              proto.String(name),
			MachineType:       proto.String("https://www.googleapis.com/compute/v1/projects/p/zones/z/machineTypes/" + mt),
			Tags:              &computepb.Tags{Items: tags},
			NetworkInterfaces: []*computepb.NetworkInterface{{NetworkIP: proto.String("10.0.0.5")}},
		}
	}
	got := workerNodesFromInstances("prod", "z", []*computepb.Instance{
		inst("prod-worker-workers-1", "e2-standard-8"),
		inst("prod-worker-workers-0", "e2-standard-8"),
		inst("prod-worker-gpu-0", "a2-highgpu-1g"),
		inst("prod-master-1", "e2-standard-8"),
		inst("prod-dev-worker-workers-0", "e2-standard-2"),             // another cluster's name prefix
		inst("prod-worker-x-0", "e2-standard-2", instanceTag("other")), // tagged for another cluster
	})
	if len(got) != 3 || got[0].InstanceName != "prod-worker-gpu-0" || got[1].InstanceName != "prod-worker-workers-0" {
		t.Fatalf("discovered %+v", got)
	}
	if got[1].MachineType != "e2-standard-8" || got[1].PrivateIP != "10.0.0.5" {
		t.Errorf("worker = %+v", got[1])
	}
	cfg := &Config{MachineType: "e2-standard-4"} // the provider block's legacy size
	if mt := cfg.workerSize("", workersMachineType(got, "prod-worker-workers-"), false); mt != "e2-standard-8" {
		t.Errorf("scale-up size = %s, want the running workers' e2-standard-8 (not the provider block's e2-standard-4)", mt)
	}
}

func TestGCPShippedConfigsBuildTheSameMachines(t *testing.T) {
	for _, file := range []string{"config.gcp.yaml", "examples/gcp-config.yaml"} {
		for env, workers := range map[string]int{"dev": 2, "test": 2, "prod": 4} {
			t.Run(file+"/"+env, func(t *testing.T) {
				spec, m := shippedEnv(t, file, env)
				cfg, err := parseProviderConfig(m)
				if err != nil {
					t.Fatal(err)
				}
				got := []interface{}{cfg.controlPlaneSize(spec), cfg.workerSize(spec.NodeGroups[0].InstanceType, "", false),
					spec.NodeGroups[0].Replicas, cfg.DiskSize, cfg.DiskType, cfg.Zone, cfg.SubnetCIDR, cfg.ImageFamily}
				want := []interface{}{"e2-standard-8", "e2-standard-8", workers, int32(100), "pd-standard", "asia-southeast1-a", "10.20.0.0/16", "ubuntu-2404-lts-amd64"}
				for i := range want {
					if got[i] != want[i] {
						t.Errorf("resolved %v, built %v before", got, want)
						break
					}
				}
			})
		}
	}
}
