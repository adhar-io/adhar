package aws

import (
	"testing"

	"adhar-io/adhar/platform/types"
)

// Every AWS size, disk and network setting flows from configuration, the
// provider block's `config:` section included (it was never read).
func TestAWSSizingFlowsFromTheProviderBlock(t *testing.T) {
	cfg, err := parseProviderConfig(map[string]interface{}{
		"region": "eu-west-1",
		"config": map[string]interface{}{ // viper lower-cases nested keys
			"controlplanemachinetype": "m6i.2xlarge",
			"workermachinetype":       "c6i.2xlarge",
			"disksizegb":              300,
			"disktype":                "io2",
		},
		"vpcConfig": map[string]interface{}{
			"cidr":              "10.9.0.0/16",
			"subnetcidrs":       []interface{}{"10.9.1.0/24", "10.9.2.0/24"},
			"availabilityzones": []interface{}{"eu-west-1b", "eu-west-1c"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.controlPlaneType(&types.ClusterSpec{}); got != "m6i.2xlarge" {
		t.Errorf("control plane = %s", got)
	}
	if got := cfg.workerType("", "", false); got != "c6i.2xlarge" {
		t.Errorf("worker = %s", got)
	}
	if got := cfg.workerType("", "", true); got != "c6i.2xlarge" {
		t.Errorf("EKS worker = %s", got)
	}
	if cfg.DiskSizeGB != 300 || cfg.DiskType != "io2" {
		t.Errorf("disk = %d %s, want 300 io2 from the provider block", cfg.DiskSizeGB, cfg.DiskType)
	}
	pub, priv := cfg.subnetCIDRs()
	if cfg.vpcCIDR() != "10.9.0.0/16" || pub != "10.9.1.0/24" || priv != "10.9.2.0/24" {
		t.Errorf("network = %s %s %s", cfg.vpcCIDR(), pub, priv)
	}
	if len(cfg.AvailabilityZones) != 2 || cfg.AvailabilityZones[0] != "eu-west-1b" {
		t.Errorf("zones = %v", cfg.AvailabilityZones)
	}

	// The environment (top level) wins over the provider block, and the node
	// group's / spec's own size wins over both.
	cfg, _ = parseProviderConfig(map[string]interface{}{
		"diskSizeGb": "512",
		"config":     map[string]interface{}{"disksizegb": 300, "workermachinetype": "c6i.2xlarge"},
	})
	if cfg.DiskSizeGB != 512 {
		t.Errorf("environment diskSizeGb must win, got %d", cfg.DiskSizeGB)
	}
	if got := cfg.workerType("g5.xlarge", "", false); got != "g5.xlarge" {
		t.Errorf("the node group's size must win, got %s", got)
	}
	if got := cfg.controlPlaneType(&types.ClusterSpec{ControlPlane: types.ControlPlaneSpec{InstanceType: "c7i.xlarge"}}); got != "c7i.xlarge" {
		t.Errorf("the spec's control-plane size must win, got %s", got)
	}
}

// Defaults apply only when nothing is configured, and come from the table.
func TestAWSDefaultsOnlyWhenUnset(t *testing.T) {
	cfg, err := parseProviderConfig(map[string]interface{}{"region": "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.controlPlaneType(nil); got != "m6i.xlarge" {
		t.Errorf("default control plane = %s", got)
	}
	if got := cfg.workerType("", "", false); got != "r6i.xlarge" {
		t.Errorf("default worker = %s", got)
	}
	if got := cfg.workerType("", "", true); got != "r6i.xlarge" {
		t.Errorf("default EKS worker = %s (was t3.large)", got)
	}
	// The network defaults are the literals this provider always used.
	pub, priv := cfg.subnetCIDRs()
	if cfg.vpcCIDR() != "10.0.0.0/16" || pub != "10.0.1.0/24" || priv != "10.0.2.0/24" || len(cfg.AvailabilityZones) != 0 {
		t.Errorf("default network changed: %s %s %s %v", cfg.vpcCIDR(), pub, priv, cfg.AvailabilityZones)
	}
}

// A scale-up copies the size the group's existing workers run (was t3.medium).
func TestAWSScaleUpCopiesTheGroupsSize(t *testing.T) {
	members := map[string]NodeInfo{
		"c-workers-2": {InstanceType: "r6i.2xlarge"},
		"c-workers-1": {InstanceType: "r6i.2xlarge"},
	}
	cfg := &Config{WorkerInstanceType: "m6i.large"}
	if got := cfg.workerType("", membersInstanceType(members), false); got != "r6i.2xlarge" {
		t.Errorf("scale-up size = %s, want the members' r6i.2xlarge", got)
	}
	if got := cfg.workerType("", membersInstanceType(nil), false); got != "m6i.large" {
		t.Errorf("an empty group takes the configured worker size, got %s", got)
	}
}

// The shipped configs build exactly what they built before.
func TestAWSShippedConfigsBuildTheSameMachines(t *testing.T) {
	for _, file := range []string{"config.aws.yaml", "examples/aws-config.yaml"} {
		for _, env := range []string{"dev", "test", "prod"} {
			t.Run(file+"/"+env, func(t *testing.T) {
				spec, m := shippedEnv(t, file, env)
				cfg, err := parseProviderConfig(m)
				if err != nil {
					t.Fatal(err)
				}
				pub, priv := cfg.subnetCIDRs()
				got := []interface{}{cfg.controlPlaneType(spec), cfg.workerType(spec.NodeGroups[0].InstanceType, "", false),
					spec.NodeGroups[0].Replicas, cfg.DiskSizeGB, cfg.DiskType, cfg.vpcCIDR(), pub, priv, len(cfg.AvailabilityZones), cfg.Region}
				// m6i.xlarge since 2026-10-30 (was r6i.large, 2 vCPU — below MinControlPlaneVCPU).
				want := []interface{}{"m6i.xlarge", "r6i.xlarge", 2, int32(256), "gp3", "10.0.0.0/16", "10.0.1.0/24", "10.0.2.0/24", 0, "ap-southeast-1"}
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
