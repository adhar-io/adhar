package digitalocean

import (
	"testing"

	"adhar-io/adhar/platform/types"
)

func TestDigitalOceanSizingFlowsFromTheProviderBlock(t *testing.T) {
	cfg, err := parseProviderConfig(map[string]interface{}{
		"region": "blr1",
		"config": map[string]interface{}{
			"droplet_size":            "s-8vcpu-16gb",
			"controlplanemachinetype": "g-4vcpu-16gb",
			"workermachinetype":       "m-4vcpu-32gb",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.controlPlaneSize(&types.ClusterSpec{}); got != "g-4vcpu-16gb" {
		t.Errorf("control plane = %s", got)
	}
	if got := cfg.workerSize("", "", false); got != "m-4vcpu-32gb" {
		t.Errorf("worker = %s", got)
	}
	p := &Provider{config: cfg}
	pools := p.buildDOKSNodePools(&types.ClusterSpec{NodeGroups: []types.NodeGroupSpec{{Name: "workers", Replicas: 3}}})
	if pools[0].Size != "m-4vcpu-32gb" {
		t.Errorf("DOKS pool = %s, want the configured worker size", pools[0].Size)
	}
	// The legacy droplet_size alone still sizes every droplet.
	legacy, _ := parseProviderConfig(map[string]interface{}{"config": map[string]interface{}{"droplet_size": "s-4vcpu-8gb"}})
	if legacy.controlPlaneSize(nil) != "s-4vcpu-8gb" || legacy.workerSize("", "", false) != "s-4vcpu-8gb" {
		t.Error("legacy droplet_size no longer sizes both roles")
	}
	// The in-cluster autoscaler's top-level worker size; members still win.
	top, _ := parseProviderConfig(map[string]interface{}{"workerMachineType": "s-16vcpu-32gb"})
	if top.workerSize("", "", false) != "s-16vcpu-32gb" || top.workerSize("", "s-8vcpu-16gb", false) != "s-8vcpu-16gb" {
		t.Error("top-level workerMachineType / members precedence broken")
	}
}

func TestDigitalOceanDefaultsOnlyWhenUnset(t *testing.T) {
	cfg, _ := parseProviderConfig(map[string]interface{}{"region": "nyc1"})
	if cfg.controlPlaneSize(nil) != "s-8vcpu-16gb" || cfg.workerSize("", "", false) != "s-8vcpu-16gb" {
		t.Errorf("defaults = %s / %s (were s-2vcpu-2gb)", cfg.controlPlaneSize(nil), cfg.workerSize("", "", false))
	}
	pools := (&Provider{config: cfg}).buildDOKSNodePools(&types.ClusterSpec{})
	if len(pools) != 1 || pools[0].Count != 2 || pools[0].Size != "s-8vcpu-16gb" {
		t.Errorf("default DOKS pool = %+v (was 3 x s-2vcpu-2gb)", pools[0])
	}
}

func TestDigitalOceanShippedConfigsBuildTheSameMachines(t *testing.T) {
	for _, file := range []string{"config.digitalocean.yaml", "examples/digitalocean-config.yaml"} {
		for env, workers := range map[string]int{"dev": 2, "test": 2, "prod": 4} {
			t.Run(file+"/"+env, func(t *testing.T) {
				spec, m := shippedEnv(t, file, env)
				cfg, err := parseProviderConfig(m)
				if err != nil {
					t.Fatal(err)
				}
				got := []interface{}{cfg.controlPlaneSize(spec), cfg.workerSize(spec.NodeGroups[0].InstanceType, "", false),
					spec.NodeGroups[0].Replicas, cfg.Image, cfg.VPCCIDR, cfg.Region}
				want := []interface{}{"s-8vcpu-16gb", "s-8vcpu-16gb", workers, "ubuntu-24-04-x64", "10.3.0.0/16", "blr1"}
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
