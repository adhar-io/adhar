package civo

import (
	"testing"

	"adhar-io/adhar/platform/types"
)

func TestCivoSizingFlowsFromTheProviderBlock(t *testing.T) {
	cfg, err := parseProviderConfig(map[string]interface{}{
		"region": "LON1",
		"config": map[string]interface{}{
			"size":                    "g4s.kube.large",
			"controlplanemachinetype": "g3.xlarge",
			"workermachinetype":       "g4s.kube.xlarge",
			"default_node_count":      4, // YAML gives an int; the float64-only read dropped it
			"image":                   "ubuntu-noble",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.controlPlaneSize(&types.ClusterSpec{}); got != "g3.xlarge" {
		t.Errorf("control plane = %s", got)
	}
	if got := cfg.workerSize("", "", true); got != "g4s.kube.xlarge" {
		t.Errorf("worker = %s", got)
	}
	if cfg.DefaultNodeCount != 4 || cfg.ComputeImage != "ubuntu-noble" {
		t.Errorf("default_node_count=%d image=%q", cfg.DefaultNodeCount, cfg.ComputeImage)
	}
	pools := (&Provider{config: cfg}).buildCivoPools(&types.ClusterSpec{NodeGroups: []types.NodeGroupSpec{{Name: "workers", Replicas: 3}}})
	if pools[0].Size != "g4s.kube.xlarge" || pools[0].Count != 3 {
		t.Errorf("managed pool = %+v", pools[0])
	}
	// The quota preflight costs the control plane and workers at their own sizes.
	plan := plannedInstanceSizes(&types.ClusterSpec{NodeGroups: []types.NodeGroupSpec{{Name: "workers", Replicas: 2}}},
		cfg.controlPlaneSize(nil), cfg.workerSize("", "", false), 0)
	if len(plan) != 3 || plan[0] != "g3.xlarge" || plan[1] != "g4s.kube.xlarge" {
		t.Errorf("plan = %v", plan)
	}
}

func TestCivoDefaultsOnlyWhenUnset(t *testing.T) {
	cfg, _ := parseProviderConfig(map[string]interface{}{"region": "LON1"})
	if got := cfg.controlPlaneSize(nil); got != "g3.large" {
		t.Errorf("default control plane = %s (was g4s.kube.medium)", got)
	}
	if got := cfg.workerSize("", "", false); got != "g3.xlarge" {
		t.Errorf("default compute worker = %s (an INSTANCE size)", got)
	}
	if got := cfg.workerSize("", "", true); got != "g4s.kube.large" {
		t.Errorf("default managed worker = %s (a KUBE size)", got)
	}
	if cfg.ComputeImage != "" {
		t.Errorf("compute image must default to the built-in ubuntu-jammy, got %q", cfg.ComputeImage)
	}
}

func TestCivoShippedConfigsBuildTheSameMachines(t *testing.T) {
	for file, worker := range map[string]string{"config.civo.yaml": "g4c.kube.small", "examples/civo-config.yaml": "g4s.kube.large"} {
		for _, env := range []string{"dev", "test", "prod"} {
			t.Run(file+"/"+env, func(t *testing.T) {
				spec, m := shippedEnv(t, file, env)
				cfg, err := parseProviderConfig(m)
				if err != nil {
					t.Fatal(err)
				}
				pools := (&Provider{config: cfg}).buildCivoPools(spec)
				if len(pools) != 1 || pools[0].Size != worker || pools[0].Count != 3 {
					t.Errorf("pools = %+v, built 3 x %s before", pools, worker)
				}
				// disk_image was never read; compute mode keeps ubuntu-jammy.
				if cfg.ComputeImage != "" || cfg.DefaultNodeCount != 3 {
					t.Errorf("image=%q default_node_count=%d", cfg.ComputeImage, cfg.DefaultNodeCount)
				}
			})
		}
	}
}
