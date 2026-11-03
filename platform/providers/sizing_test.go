package provider

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/types"
)

// The defaults table is the ONE place a built-in size lives. Every cloud must
// have both roles filled, and every control-plane default must clear the
// 4-vCPU floor the 2026-10-11 leader-election collapse set.
func TestEveryCloudHasAControlPlaneAndWorkerDefaultAboveTheFloor(t *testing.T) {
	for _, cloud := range []string{"aws", "azure", "gcp", "digitalocean", "civo"} {
		d := MachineDefaultsFor(cloud)
		if d.ControlPlaneMachineType == "" || d.WorkerMachineType == "" || d.Region == "" {
			t.Errorf("%s: incomplete defaults %+v", cloud, d)
			continue
		}
		if n := EstimateVCPU(cloud, d.ControlPlaneMachineType); n < MinControlPlaneVCPU {
			t.Errorf("%s: default control plane %s has %d vCPU, below the %d-vCPU floor", cloud, d.ControlPlaneMachineType, n, MinControlPlaneVCPU)
		}
		if d.WorkerCount != 2 {
			t.Errorf("%s: default worker count %d, want 2", cloud, d.WorkerCount)
		}
	}
	if MachineDefaultsFor("civo").ManagedWorker() == MachineDefaultsFor("civo").WorkerMachineType {
		t.Error("civo managed pools take kube sizes; they cannot share the compute instance default")
	}
}

func TestResolveMachineTypePrecedence(t *testing.T) {
	if got := ControlPlaneSize("aws", "c6i.xlarge", "m6i.2xlarge", "legacy"); got != "c6i.xlarge" {
		t.Errorf("the environment's control-plane size must win, got %s", got)
	}
	if got := ControlPlaneSize("azure", "", "Standard_D8s_v5", "Standard_E4bds_v5"); got != "Standard_D8s_v5" {
		t.Errorf("provider controlPlaneMachineType must beat the legacy key, got %s", got)
	}
	if got := ControlPlaneSize("azure", "", "", "Standard_E4bds_v5"); got != "Standard_E4bds_v5" {
		t.Errorf("the legacy single size must still size the control plane, got %s", got)
	}
	if got := ControlPlaneSize("gcp", "", "", ""); got != "e2-standard-4" {
		t.Errorf("default only when nothing is configured, got %s", got)
	}
	if got := WorkerSize("aws", false, "g5.xlarge", "r6i.xlarge", "m6i.large", ""); got != "g5.xlarge" {
		t.Errorf("the node group's own size must win, got %s", got)
	}
	if got := WorkerSize("aws", false, "", "r6i.2xlarge", "m6i.large", ""); got != "r6i.2xlarge" {
		t.Errorf("a scale-up must copy the group's existing members, got %s", got)
	}
	if got := WorkerSize("digitalocean", false, "", "", "s-4vcpu-8gb", "s-8vcpu-16gb"); got != "s-4vcpu-8gb" {
		t.Errorf("provider workerMachineType must beat the legacy key, got %s", got)
	}
	if got := WorkerSize("civo", true, "", "", "", ""); got != "g4s.kube.large" {
		t.Errorf("managed civo pools default to a kube size, got %s", got)
	}
	if got := WorkerSize("civo", false, "", "", "", ""); got != "g3.xlarge" {
		t.Errorf("compute civo workers default to an instance size, got %s", got)
	}
}

func TestConfigStringFoldsKeysAndPrefersTheTopLevel(t *testing.T) {
	m := map[string]interface{}{
		"control_plane_machine_type": "top",
		"config": map[string]interface{}{
			"controlplanemachinetype": "section", // viper lower-cases nested keys
			"workermachinetype":       "w",
			"disksizegb":              256,
			"zones":                   []interface{}{"a", "b"},
		},
	}
	if got := ConfigString(m, KeyControlPlaneMachineType); got != "top" {
		t.Errorf("top level must win, got %q", got)
	}
	if got := ConfigString(m, KeyWorkerMachineType); got != "w" {
		t.Errorf("the section must be read when the top level is silent, got %q", got)
	}
	if got := ConfigInt(m, "diskSizeGb"); got != 256 {
		t.Errorf("an int from YAML must be read, got %d", got)
	}
	if got := ConfigStringList(m, "availabilityZones", "zones"); len(got) != 2 || got[1] != "b" {
		t.Errorf("list not read: %v", got)
	}
	if got := ConfigStringList(map[string]interface{}{"zones": "a, b"}, "zones"); len(got) != 2 || got[0] != "a" {
		t.Errorf("comma-separated list not read: %v", got)
	}
}

// Two floors, each from an incident: 4 vCPU (the 2026-10-11 leader-election
// collapse) and 32 GiB (kube-apiserver at 8-12 GiB on a 16 GiB DigitalOcean
// control plane with the kubelet evicting pods, and OOM-killed five times on a
// 16 GiB AWS one). A size is flagged when it is KNOWN to miss either one.
func TestControlPlaneSizeWarning(t *testing.T) {
	for _, c := range []struct {
		cloud, size string
		cpu, mem    bool // which floor the advice must name
	}{
		{"aws", "r6i.large", true, true}, // 2 vCPU / 16 GiB
		{"aws", "m6i.xlarge", false, true},
		{"aws", "r6i.xlarge", false, false},
		{"aws", "m6i.2xlarge", false, false},
		{"azure", "Standard_D2s_v3", true, true},
		{"azure", "Standard_D4s_v5", false, true},
		{"azure", "Standard_E4bds_v5", false, false},
		{"gcp", "e2-standard-2", true, true},
		{"gcp", "e2-standard-4", false, true},
		{"gcp", "e2-standard-8", false, false},
		{"gcp", "e2-highmem-4", false, false},
		{"gcp", "custom-6-20480", false, true}, // 20 GiB, read from the name
		{"digitalocean", "s-2vcpu-4gb", true, true},
		{"digitalocean", "s-8vcpu-16gb", false, true}, // the live 2026-11-03 control plane
		{"digitalocean", "g-8vcpu-32gb", false, false},
		{"digitalocean", "m-4vcpu-32gb", false, false},
		{"civo", "g4s.kube.medium", true, true},
		{"civo", "g3.large", false, true},
		{"civo", "g3.2xlarge", false, false},
		{"gcp", "e2-medium", false, false},         // unrecognised: no advice rather than a guess
		{"aws", "x2iedn.xlarge", false, false},     // memory class not in the table: no guess
		{"azure", "Standard_B4ms", false, false},   // burstable B-series: no guess
		{"digitalocean", "", false, false},         // managed mode / nothing configured
		{"civo", "g4s.kube.unknown", false, false}, // not a listed Civo size
	} {
		w := ControlPlaneSizeWarning(c.cloud, c.size)
		if (w != "") != (c.cpu || c.mem) {
			t.Errorf("%s %s: advice %q, want advice=%v", c.cloud, c.size, w, c.cpu || c.mem)
			continue
		}
		if gotCPU := strings.Contains(w, "vCPU —"); gotCPU != c.cpu {
			t.Errorf("%s %s: names the vCPU floor=%v, want %v: %q", c.cloud, c.size, gotCPU, c.cpu, w)
		}
		if gotMem := strings.Contains(w, "GiB — kube-apiserver"); gotMem != c.mem {
			t.Errorf("%s %s: names the memory floor=%v, want %v: %q", c.cloud, c.size, gotMem, c.mem, w)
		}
		if w != "" && !strings.Contains(w, RecommendedControlPlaneMachineType[c.cloud]) {
			t.Errorf("%s %s: the advice does not name the size that fixes it (%s): %q", c.cloud, c.size, RecommendedControlPlaneMachineType[c.cloud], w)
		}
	}
}

// The memory floor is only as good as the reading of a size's NAME. Every value
// here is the cloud's published memory for that size; 0 means "do not guess".
func TestEstimateMemoryGiBReadsTheSizeName(t *testing.T) {
	for _, c := range []struct {
		cloud, size string
		gib         int
	}{
		{"aws", "m6i.xlarge", 16}, {"aws", "r6i.xlarge", 32}, {"aws", "r6i.large", 16}, {"aws", "c6i.xlarge", 8},
		{"aws", "m7i-flex.xlarge", 16}, {"aws", "m6g.2xlarge", 32}, {"aws", "t3.large", 8}, {"aws", "t3.2xlarge", 32},
		{"aws", "M6I.XLARGE", 16}, {"aws", "g5.xlarge", 0}, {"aws", "m6i.metal", 0}, {"aws", "t3.nano", 0},
		{"azure", "Standard_D4s_v5", 16}, {"azure", "Standard_E4bds_v5", 32}, {"azure", "Standard_D2s_v3", 8},
		{"azure", "Standard_E8s_v5", 64}, {"azure", "Standard_D4ls_v5", 8}, {"azure", "Standard_F4s_v2", 8},
		{"azure", "Standard_DS2_v2", 0}, {"azure", "Standard_E4-2ds_v4", 0}, {"azure", "Standard_NC6s_v3", 0},
		{"gcp", "e2-standard-4", 16}, {"gcp", "e2-standard-8", 32}, {"gcp", "e2-highmem-4", 32}, {"gcp", "n1-standard-4", 15},
		{"gcp", "n1-highmem-4", 26}, {"gcp", "n2-highcpu-8", 8}, {"gcp", "c3-highcpu-8", 16}, {"gcp", "c2-standard-8", 32},
		{"gcp", "custom-6-20480", 20}, {"gcp", "n2-custom-8-32768", 32}, {"gcp", "e2-medium", 0},
		{"digitalocean", "s-8vcpu-16gb", 16}, {"digitalocean", "g-8vcpu-32gb", 32}, {"digitalocean", "m-4vcpu-32gb", 32},
		{"digitalocean", "s-8vcpu-16gb-amd", 16}, {"digitalocean", "c-8", 16}, {"digitalocean", "s-1vcpu-512mb-10gb", 0},
		{"civo", "g3.large", 8}, {"civo", "g3.2xlarge", 32}, {"civo", "g4s.kube.large", 8}, {"civo", "nope", 0},
		{"kind", "anything", 0}, {"custom", "m6i.xlarge", 0},
	} {
		if got := EstimateMemoryGiB(c.cloud, c.size); got != c.gib {
			t.Errorf("EstimateMemoryGiB(%s, %s) = %d, want %d", c.cloud, c.size, got, c.gib)
		}
	}
}

// The advice names a size per cloud; that size must itself clear both floors,
// or following the advice would earn the same warning.
func TestRecommendedControlPlaneSizesClearBothFloors(t *testing.T) {
	for _, cloud := range []string{"aws", "azure", "gcp", "digitalocean", "civo"} {
		rec := RecommendedControlPlaneMachineType[cloud]
		if rec == "" {
			t.Errorf("%s: no recommended control-plane size", cloud)
			continue
		}
		if n := EstimateVCPU(cloud, rec); n < MinControlPlaneVCPU {
			t.Errorf("%s: recommended %s reads as %d vCPU, below %d", cloud, rec, n, MinControlPlaneVCPU)
		}
		if g := EstimateMemoryGiB(cloud, rec); g < MinControlPlaneMemoryGiB {
			t.Errorf("%s: recommended %s reads as %d GiB, below %d", cloud, rec, g, MinControlPlaneMemoryGiB)
		}
		if w := ControlPlaneSizeWarning(cloud, rec); w != "" {
			t.Errorf("%s: the recommended size %s is itself flagged: %q", cloud, rec, w)
		}
	}
}

func cloudEnv(providerType string, production bool, kvs ...string) *config.ResolvedEnvironmentConfig {
	env := &config.ResolvedEnvironmentConfig{
		Name: "e", ResolvedProvider: providerType, ResolvedRegion: "r",
		ResolvedType:   config.EnvironmentTypeNonProduction,
		ProviderConfig: &config.ConfigProviderConfig{Type: providerType},
	}
	if production {
		env.ResolvedType = config.EnvironmentTypeProduction
	}
	for i := 0; i+1 < len(kvs); i += 2 {
		env.ResolvedClusterConfig = append(env.ResolvedClusterConfig, config.KeyValueConfig{Key: kvs[i], Value: kvs[i+1]})
	}
	return env
}

// A non-production CLOUD environment without nodeCount used to get zero workers:
// a lone tainted control plane on AWS/Azure/GCP, one worker on DO/Civo.
func TestDefaultWorkerCountComesFromTheTable(t *testing.T) {
	for _, cloud := range []string{"aws", "azure", "gcp", "digitalocean", "civo"} {
		for _, prod := range []bool{false, true} {
			spec, err := buildClusterSpec(cloudEnv(cloud, prod), "c")
			if err != nil {
				t.Fatal(err)
			}
			if spec.NodeGroups[0].Replicas != 2 {
				t.Errorf("%s production=%v: %d workers, want 2", cloud, prod, spec.NodeGroups[0].Replicas)
			}
		}
	}
	// Kind keeps its own rule: the untainted control plane needs no worker locally.
	if spec, _ := buildClusterSpec(cloudEnv("kind", false), "c"); spec.NodeGroups[0].Replicas != 0 {
		t.Errorf("kind non-production: %d workers, want 0", spec.NodeGroups[0].Replicas)
	}
	if spec, _ := buildClusterSpec(cloudEnv("kind", true), "c"); spec.NodeGroups[0].Replicas != 2 {
		t.Errorf("kind production: %d workers, want 2", spec.NodeGroups[0].Replicas)
	}
	// An explicit count still wins.
	if spec, _ := buildClusterSpec(cloudEnv("aws", false, "nodeCount", "5"), "c"); spec.NodeGroups[0].Replicas != 5 {
		t.Errorf("nodeCount=5 gave %d workers", spec.NodeGroups[0].Replicas)
	}
}

func TestClusterConfigSizingKeysReachTheSpec(t *testing.T) {
	env := cloudEnv("aws", true, "control_plane_machine_type", "m6i.2xlarge", "workerMachineType", "r6i.2xlarge")
	spec, err := buildClusterSpec(env, "c")
	if err != nil {
		t.Fatal(err)
	}
	if spec.ControlPlane.InstanceType != "m6i.2xlarge" {
		t.Errorf("control_plane_machine_type was ignored (it was matched literally): %q", spec.ControlPlane.InstanceType)
	}
	if spec.NodeGroups[0].InstanceType != "r6i.2xlarge" {
		t.Errorf("workerMachineType did not size the workers: %q", spec.NodeGroups[0].InstanceType)
	}
	// Template entries are prepended; the environment's must win.
	env = cloudEnv("aws", true, "controlPlaneMachineType", "from-template", "controlPlaneMachineType", "from-env")
	if spec, _ := buildClusterSpec(env, "c"); spec.ControlPlane.InstanceType != "from-env" {
		t.Errorf("the template's control-plane size beat the environment's: %q", spec.ControlPlane.InstanceType)
	}
}

func TestAdditionalNodeGroupsReachTheSpec(t *testing.T) {
	env := cloudEnv("gcp", true, "machineType", "e2-standard-8", "nodeCount", "3")
	env.NodeGroups = []config.NodeGroupConfig{{
		Name: "gpu", MachineType: "a2-highgpu-1g", Count: 1,
		Labels: map[string]string{"adhar.io/accelerator": "nvidia"},
		Taints: []config.TaintConfig{{Key: "nvidia.com/gpu", Value: "true", Effect: "NoSchedule"}},
	}}
	env.Autoscaling = &config.AutoscalingConfig{Enabled: true, NodeGroup: "gpu", MinWorkers: 1, MaxWorkers: 4}
	spec, err := buildClusterSpec(env, "c")
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.NodeGroups) != 2 {
		t.Fatalf("node groups = %+v, want workers + gpu", spec.NodeGroups)
	}
	w, g := spec.NodeGroups[0], spec.NodeGroups[1]
	if w.Name != "workers" || w.Replicas != 3 || w.InstanceType != "e2-standard-8" {
		t.Errorf("primary group changed: %+v", w)
	}
	if g.Name != "gpu" || g.Replicas != 1 || g.InstanceType != "a2-highgpu-1g" || g.Labels["adhar.io/accelerator"] != "nvidia" ||
		len(g.Taints) != 1 || g.Taints[0] != (types.TaintSpec{Key: "nvidia.com/gpu", Value: "true", Effect: "NoSchedule"}) {
		t.Errorf("gpu group = %+v", g)
	}
	// autoscaling.nodeGroup may name an additional group.
	if g.AutoScaling.MaxReplicas != 4 || g.AutoScaling.MinReplicas != 1 {
		t.Errorf("autoscaling did not reach the named group: %+v", g.AutoScaling)
	}
	if w.AutoScaling.MaxReplicas != 0 {
		t.Errorf("autoscaling leaked onto the primary group: %+v", w.AutoScaling)
	}
}

// Every shipped config — and the operator's live ones when present — must
// resolve to exactly what it built before the sizing table existed. The
// expectations are the pre-change resolution (snapshot taken 2026-10-22 from
// buildClusterSpec + each provider's own parsing, before this change).
func TestShippedConfigsBuildTheSameMachinesAsBefore(t *testing.T) {
	type want struct{ cp, worker string }
	cases := map[string]struct {
		workers map[string]int
		sizes   want
	}{
		// The AWS control plane moved from r6i.large (2 vCPU) to m6i.xlarge on
		// 2026-10-30, deliberately: it was the one shipped config below
		// MinControlPlaneVCPU.
		"config.aws.yaml":                   {map[string]int{"dev": 2, "test": 2, "prod": 2}, want{"m6i.xlarge", "r6i.xlarge"}},
		"examples/aws-config.yaml":          {map[string]int{"dev": 2, "test": 2, "prod": 2}, want{"m6i.xlarge", "r6i.xlarge"}},
		"config.azure.yaml":                 {map[string]int{"dev": 2, "test": 2, "prod": 5}, want{"Standard_E4bds_v5", "Standard_E4bds_v5"}},
		"examples/azure-config.yaml":        {map[string]int{"dev": 2, "test": 2, "prod": 5}, want{"Standard_E4bds_v5", "Standard_E4bds_v5"}},
		"config.gcp.yaml":                   {map[string]int{"dev": 2, "test": 2, "prod": 4}, want{"e2-standard-8", "e2-standard-8"}},
		"examples/gcp-config.yaml":          {map[string]int{"dev": 2, "test": 2, "prod": 4}, want{"e2-standard-8", "e2-standard-8"}},
		"config.digitalocean.yaml":          {map[string]int{"dev": 2, "test": 2, "prod": 4}, want{"s-8vcpu-16gb", "s-8vcpu-16gb"}},
		"examples/digitalocean-config.yaml": {map[string]int{"dev": 2, "test": 2, "prod": 4}, want{"s-8vcpu-16gb", "s-8vcpu-16gb"}},
		"config.civo.yaml":                  {map[string]int{"dev": 3, "test": 3, "prod": 3}, want{"", "g4c.kube.small"}},
		"examples/civo-config.yaml":         {map[string]int{"dev": 3, "test": 3, "prod": 3}, want{"", "g4s.kube.large"}},
		"examples/config.yaml":              {map[string]int{"dev": 2, "test": 2, "staging": 3, "production": 5}, want{"", ""}},
		"examples/provided-config.yaml":     {map[string]int{"dev": 0}, want{"", ""}},
	}
	root := filepath.Join("..", "..")
	files := make([]string, 0, len(cases))
	for f := range cases {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		c := cases[f]
		t.Run(f, func(t *testing.T) {
			path := filepath.Join(root, f)
			if _, err := os.Stat(path); os.IsNotExist(err) {
				t.Skipf("%s is not present (the live configs are gitignored)", f)
			}
			cfg, err := config.LoadConfig(path)
			if err != nil {
				t.Fatalf("no longer loads: %v", err)
			}
			if err := cfg.ResolveEnvironments(); err != nil {
				t.Fatal(err)
			}
			for envName, workers := range c.workers {
				env := cfg.ResolvedEnvironments[envName]
				if env == nil {
					t.Fatalf("environment %s missing", envName)
				}
				got, err := ResolveEffectiveSizes(env)
				if err != nil {
					t.Fatal(err)
				}
				if got.Workers != workers {
					t.Errorf("%s: %d workers, built %d before", envName, got.Workers, workers)
				}
				if c.sizes.worker != "" && got.Worker != c.sizes.worker {
					t.Errorf("%s: worker %s, built %s before", envName, got.Worker, c.sizes.worker)
				}
				if c.sizes.cp != "" && got.ControlPlane != c.sizes.cp {
					t.Errorf("%s: control plane %s, built %s before", envName, got.ControlPlane, c.sizes.cp)
				}
			}
		})
	}
}
