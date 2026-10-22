package azure

import (
	"testing"

	"adhar-io/adhar/platform/types"
)

func TestAzureSizingFlowsFromTheProviderBlock(t *testing.T) {
	cfg, err := parseProviderConfig(map[string]interface{}{
		"config": map[string]interface{}{ // viper lower-cases nested keys
			"location":                "centralindia",
			"vmsize":                  "Standard_E4bds_v5",
			"controlplanemachinetype": "Standard_D8s_v5",
			"workermachinetype":       "Standard_E8bds_v5",
			"imagepublisher":          "Canonical",
			"imageoffer":              "ubuntu-24_04-lts",
			"imagesku":                "server",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.controlPlaneSize(&types.ClusterSpec{}); got != "Standard_D8s_v5" {
		t.Errorf("control plane = %s, want controlPlaneMachineType over the legacy vmSize", got)
	}
	if got := cfg.workerSize("", "", false); got != "Standard_E8bds_v5" {
		t.Errorf("worker = %s", got)
	}
	if got := (&Provider{config: cfg}).agentPoolProfile(&types.NodeGroupSpec{Name: "w"}, false); *got.VMSize != "Standard_E8bds_v5" {
		t.Errorf("AKS pool = %s, want the configured worker size", *got.VMSize)
	}
	if cfg.ImagePublisher != "Canonical" || cfg.ImageOffer != "ubuntu-24_04-lts" || cfg.ImageSKU != "server" {
		t.Errorf("image = %s:%s:%s", cfg.ImagePublisher, cfg.ImageOffer, cfg.ImageSKU)
	}

	// The legacy vmSize alone still sizes every VM.
	legacy, _ := parseProviderConfig(map[string]interface{}{"config": map[string]interface{}{"vmsize": "Standard_E4bds_v5"}})
	if legacy.controlPlaneSize(nil) != "Standard_E4bds_v5" || legacy.workerSize("", "", false) != "Standard_E4bds_v5" {
		t.Errorf("legacy vmSize no longer sizes both roles: %s / %s", legacy.controlPlaneSize(nil), legacy.workerSize("", "", false))
	}

	// The in-cluster autoscaler passes the recorded worker size at the top level.
	top, _ := parseProviderConfig(map[string]interface{}{"workerMachineType": "Standard_E16bds_v5", "config": map[string]interface{}{}})
	if got := top.workerSize("", "", false); got != "Standard_E16bds_v5" {
		t.Errorf("top-level workerMachineType ignored: %s", got)
	}
	// A scale-up copies the group's members first.
	if got := top.workerSize("", "Standard_E4bds_v5", false); got != "Standard_E4bds_v5" {
		t.Errorf("scale-up size = %s, want the members' size", got)
	}
}

func TestAzureDefaultsOnlyWhenUnset(t *testing.T) {
	cfg, err := parseProviderConfig(map[string]interface{}{"config": map[string]interface{}{"location": "eastus"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.controlPlaneSize(nil); got != "Standard_D4s_v5" {
		t.Errorf("default control plane = %s (was Standard_D2s_v3)", got)
	}
	if got := cfg.workerSize("", "", false); got != "Standard_E4bds_v5" {
		t.Errorf("default worker = %s", got)
	}
	if got := cfg.workerSize("", "", true); got != "Standard_E4bds_v5" {
		t.Errorf("default AKS worker = %s", got)
	}
}

func TestAzureShippedConfigsBuildTheSameMachines(t *testing.T) {
	for _, file := range []string{"config.azure.yaml", "examples/azure-config.yaml"} {
		for env, workers := range map[string]int{"dev": 2, "test": 2, "prod": 5} {
			t.Run(file+"/"+env, func(t *testing.T) {
				spec, m := shippedEnv(t, file, env)
				cfg, err := parseProviderConfig(m)
				if err != nil {
					t.Fatal(err)
				}
				got := []interface{}{cfg.controlPlaneSize(spec), cfg.workerSize(spec.NodeGroups[0].InstanceType, "", false),
					spec.NodeGroups[0].Replicas, cfg.DiskSizeGB, cfg.DiskType, cfg.VNetCIDR, cfg.SubnetCIDR, cfg.Location}
				want := []interface{}{"Standard_E4bds_v5", "Standard_E4bds_v5", workers, int32(256), "Premium_LRS", "10.30.0.0/16", "10.30.1.0/24", "centralindia"}
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
