package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/types"
)

// A batch node pool (design 0025 D14) is only a pool if its nodes carry the
// label and taint FROM REGISTRATION: a taint applied after the node is Ready
// leaves a window in which general pods land on it and then stay, because
// NoSchedule evicts nothing. In compute mode the kubelet's own flags are the
// only place that happens, and the scale path (`adhar cluster scale`, the node
// autoscaler) knows the group's NAME only — so the registration is read from the
// machine name every provider builds as `<…>-<group>-<index>`.
func TestBatchPoolMachinesRegisterWithTheLabelAndTaint(t *testing.T) {
	for _, name := range []string{
		"adhar-prod-batch-1",           // DigitalOcean / Civo compute
		"prod-batch-12",                // AWS Name tag
		"prod-worker-batch-3",          // GCP / Azure
		"batch-2",                      // Civo legacy `<group>-<n>`
		"  adhar-prod-batch-4  ",       // stray whitespace from a tag
		"adhar-batch-cluster-batch-1",  // a cluster whose name contains batch
		"adhar-my-batch-team-batch-10", // …anywhere
	} {
		reg := WorkerRegistration(name)
		if reg.Labels[globals.BatchNodePoolLabel] != globals.BatchNodePoolValue {
			t.Errorf("%q: labels %v, want %s=%s", name, reg.Labels, globals.BatchNodePoolLabel, globals.BatchNodePoolValue)
		}
		if len(reg.Taints) != 1 || reg.Taints[0].Key != globals.BatchNodePoolTaintKey ||
			reg.Taints[0].Value != globals.BatchNodePoolTaintValue || reg.Taints[0].Effect != "NoSchedule" {
			t.Errorf("%q: taints %v, want %s=%s:NoSchedule", name, reg.Taints, globals.BatchNodePoolTaintKey, globals.BatchNodePoolTaintValue)
		}
	}
}

// …and nothing else does: a general worker that registered with the pool's taint
// would refuse every platform pod.
func TestGeneralWorkersRegisterWithNoPoolLabelOrTaint(t *testing.T) {
	for _, name := range []string{
		"adhar-batch-workers-1", // a CLUSTER named batch, primary group
		"adhar-prod-workers-3",
		"prod-worker-gpu-1",
		"adhar-prod-mybatch-1", // a group whose name merely ends in batch
		"adhar-prod-batch-x",   // no index
		"adhar-prod-batch",
		"",
	} {
		if reg := WorkerRegistration(name); len(reg.Labels) != 0 || len(reg.Taints) != 0 {
			t.Errorf("%q registers as a pool node: %+v", name, reg)
		}
	}
}

// The kubelet's --register-with-taints REPLACES its value on each use, so the
// pool taint and the CSI startup taint must share one flag; two flags would
// silently drop the CSI taint and let the scheduler overpack the node's volume
// slots again.
func TestPoolTaintSharesTheOneRegisterWithTaintsFlag(t *testing.T) {
	got := kubeletFlags(true, true, "10.0.0.7", BatchPoolRegistration())
	if n := strings.Count(got, "--register-with-taints="); n != 1 {
		t.Fatalf("%d --register-with-taints flags in %q, want exactly one", n, got)
	}
	for _, want := range []string{
		"--register-with-taints=" + globals.NodeCSIStartupTaint + "=:NoSchedule," + globals.BatchNodePoolTaintKey + "=" + globals.BatchNodePoolTaintValue + ":NoSchedule",
		"--node-labels=" + globals.BatchNodePoolLabel + "=" + globals.BatchNodePoolValue,
		ExternalCloudProviderFlag,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("kubelet flags %q lack %q", got, want)
		}
	}
	// Without the CSI taint (Civo compute) the pool taint stands alone.
	if got := kubeletFlags(false, false, "10.0.0.7", BatchPoolRegistration()); !strings.Contains(got,
		"--register-with-taints="+globals.BatchNodePoolTaintKey+"="+globals.BatchNodePoolTaintValue+":NoSchedule") {
		t.Errorf("pool taint missing without the CSI taint: %q", got)
	}
	// A general worker's flags are exactly what they were before pools existed.
	if a, b := kubeletFlags(true, true, "10.0.0.7", NodeRegistration{}), kubeletExtraArgs(true, true, "10.0.0.7"); a != b || strings.Contains(a, "--node-labels") {
		t.Errorf("general worker flags changed: %q vs %q", a, b)
	}
}

// Every compute-mode WORKER join goes through EnableWorkerKubelet, which reads
// the registration from the machine name. EnableExternalCloudProvider takes no
// registration, so a worker joined through it would come up as a general worker
// even in the batch group; only control-plane calls may still use it.
func TestEveryComputeWorkerJoinCarriesItsGroupRegistration(t *testing.T) {
	workerJoins := 0
	for _, dir := range []string{"aws", "azure", "civo", "digitalocean", "gcp"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for i, line := range strings.Split(string(src), "\n") {
				if strings.Contains(line, "provider.EnableWorkerKubelet(") {
					workerJoins++
					if !strings.Contains(line, "provider.WorkerRegistration(") {
						t.Errorf("%s:%d joins a worker without its node group's registration: %s", f, i+1, strings.TrimSpace(line))
					}
				}
				if strings.Contains(line, "provider.EnableExternalCloudProvider(") && !strings.Contains(strings.ToLower(line), "master") {
					t.Errorf("%s:%d joins what looks like a worker through EnableExternalCloudProvider, which drops its pool label and taint: %s",
						f, i+1, strings.TrimSpace(line))
				}
			}
		}
	}
	// Create and scale paths of five providers (Azure's scale path is one helper
	// used twice; Civo, GCP, AWS and DigitalOcean have one create and one scale site).
	if workerJoins < 9 {
		t.Errorf("found %d EnableWorkerKubelet call sites, want at least 9 (a create and a scale path per provider)", workerJoins)
	}
}

// environments[].batchPool becomes one more node group in the cluster spec, with
// the pool's label and taint (which a managed pool applies from the spec) and
// its range only where the environment autoscales.
func TestBatchPoolBecomesATaintedNodeGroupInTheClusterSpec(t *testing.T) {
	env := cloudEnv("aws", true)
	env.BatchPool = &config.BatchPoolConfig{MachineType: "c6i.2xlarge", MinNodes: 2, MaxNodes: 5}
	spec, err := buildClusterSpec(env, "c")
	if err != nil {
		t.Fatal(err)
	}
	var pool *types.NodeGroupSpec
	for i := range spec.NodeGroups {
		if spec.NodeGroups[i].Name == globals.BatchNodeGroupName {
			pool = &spec.NodeGroups[i]
		}
	}
	if pool == nil {
		t.Fatalf("no %q node group in %+v", globals.BatchNodeGroupName, spec.NodeGroups)
	}
	if pool.Replicas != 2 || pool.InstanceType != "c6i.2xlarge" {
		t.Errorf("pool group %+v: want 2 x c6i.2xlarge", pool)
	}
	if pool.Labels[globals.BatchNodePoolLabel] != globals.BatchNodePoolValue {
		t.Errorf("pool labels %v", pool.Labels)
	}
	if len(pool.Taints) != 1 || pool.Taints[0] != (types.TaintSpec{Key: globals.BatchNodePoolTaintKey, Value: globals.BatchNodePoolTaintValue, Effect: "NoSchedule"}) {
		t.Errorf("pool taints %v", pool.Taints)
	}
	if pool.AutoScaling != (types.AutoScalingSpec{}) {
		t.Errorf("autoscaling is off, yet the pool carries a range %+v", pool.AutoScaling)
	}
	if spec.NodeGroups[0].Name != config.PrimaryNodeGroupName || len(spec.NodeGroups[0].Taints) != 0 {
		t.Errorf("the primary group changed: %+v", spec.NodeGroups[0])
	}

	env.Autoscaling = &config.AutoscalingConfig{Enabled: true, MinWorkers: 2, MaxWorkers: 6}
	spec, err = buildClusterSpec(env, "c")
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range spec.NodeGroups {
		if g.Name == globals.BatchNodeGroupName && g.AutoScaling != (types.AutoScalingSpec{MinReplicas: 2, MaxReplicas: 5}) {
			t.Errorf("pool range %+v, want 2..5", g.AutoScaling)
		}
		if g.Name == config.PrimaryNodeGroupName && g.AutoScaling != (types.AutoScalingSpec{MinReplicas: 2, MaxReplicas: 6}) {
			t.Errorf("worker range %+v, want 2..6 (the pool must not change it)", g.AutoScaling)
		}
	}
}
