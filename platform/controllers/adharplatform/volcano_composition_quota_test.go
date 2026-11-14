package adharplatform

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// vcQuota returns the cluster with the namespace's ResourceQuota set to hard.
func vcQuota(cluster []map[string]any, quota string, hard map[string]any) []map[string]any {
	return vcEdit(cluster, quota, func(d map[string]any) { d["spec"].(map[string]any)["hard"] = hard })
}

// §6.4 gang fit, on every key the quota sets: an environment quota caps requests.cpu
// as tightly as memory, and a gang over it is admitted by its leaf, creates its pods,
// is forbidden the last of them, and holds the rest of the namespace's quota while it
// never reaches minAvailable. The design's own partitioned example (500m + 4 × 1 CPU)
// is the case: an environment composed with its default cpuQuota of 4 cannot run it.
func TestPartitionedRunMustFitEveryQuotaKey(t *testing.T) {
	xr := vcXR(t, "xr-partitioned.yaml") // 500m/1Gi + 4 × 1/1536Mi: 4500m, 7Gi, 5 pods
	room := map[string]any{"requests.cpu": "16", "limits.cpu": "16", "requests.memory": "32Gi", "limits.memory": "32Gi", "pods": "100"}
	with := func(k string, v any) map[string]any {
		h := map[string]any{}
		for kk, vv := range room {
			h[kk] = vv
		}
		h[k] = v
		return h
	}
	for _, c := range []struct {
		name    string
		params  map[string]any
		hard    map[string]any
		native  bool
		refused string // the over-quota entry the refusal names; empty: accepted
	}{
		{"requests.cpu over the env default of 4", nil, with("requests.cpu", "4"), false, "requests.cpu 4500m > 4"},
		{"requests.cpu exactly", nil, with("requests.cpu", "4500m"), false, ""},
		{"requests.cpu a milli short", nil, with("requests.cpu", "4499m"), false, "requests.cpu 4500m > 4499m"},
		// The Indexed fallback sizes every index like the larger pod: 5 × 1 CPU.
		{"indexed requests.cpu", nil, with("requests.cpu", "4500m"), true, "requests.cpu 5000m > 4500m"},
		{"the bare cpu key", nil, with("cpu", "4"), false, "cpu 4500m > 4"},
		// limits.cpu is the request unless cpuLimit raises it on every pod.
		{"limits.cpu follows the request", nil, with("limits.cpu", "4500m"), false, ""},
		{"limits.cpu with cpuLimit", map[string]any{"resources": map[string]any{"cpu": "500m", "memory": "1Gi", "workerCpu": "1", "workerMemory": "1536Mi", "cpuLimit": "2"}},
			with("limits.cpu", "8"), false, "limits.cpu 10000m > 8"},
		{"requests.memory", nil, with("requests.memory", "6Gi"), false, "requests.memory 7168Mi > 6Gi"},
		{"the bare memory key", nil, with("memory", "6Gi"), false, "memory 7168Mi > 6Gi"},
		{"pods", nil, with("pods", "4"), false, "pods 5 > 4"},
		{"pods exactly", nil, with("pods", "5"), false, ""},
		// A quantity the composition cannot read is left to the API server.
		{"an unreadable quota", nil, with("requests.cpu", "1e3m"), false, ""},
	} {
		cluster := vcCluster(t)
		if c.native {
			cluster = vcNoVolcano(t)
		}
		params := map[string]any{}
		for k, v := range c.params {
			params[k] = v
		}
		_, err := vcRender(t, bjWithParams(xr, params), vcQuota(cluster, "dev-quota", c.hard), nil)
		switch {
		case c.refused == "" && err != nil:
			t.Errorf("%s: refused: %v", c.name, err)
		case c.refused != "" && err == nil:
			t.Errorf("%s: accepted; the gang could never start", c.name)
		case c.refused != "" && (!strings.Contains(err.Error(), c.refused) || !strings.Contains(err.Error(), "dev-quota")):
			t.Errorf("%s: the refusal does not say %q of dev-quota: %v", c.name, c.refused, err)
		}
	}
	// Every key over is named, not only the first.
	_, err := vcRender(t, xr, vcQuota(vcCluster(t), "dev-quota", map[string]any{"requests.cpu": "4", "limits.cpu": "4", "requests.memory": "4Gi", "limits.memory": "4Gi", "pods": "4"}), nil)
	for _, k := range []string{"requests.cpu", "limits.cpu", "requests.memory", "limits.memory", "pods"} {
		if err == nil || !strings.Contains(err.Error(), k+" ") {
			t.Errorf("a gang over every key: the refusal does not name %s: %v", k, err)
		}
	}
}

// Every run pod carries a CPU limit: left off, the environment's LimitRange defaults
// it to 500m and the API server rejects a pod whose request is larger, when the Job
// controller creates it, so a default run (1 CPU) would never start. The limit is the
// request unless cpuLimit raises it, and a cpuLimit below a request is refused.
func TestEveryRunPodCarriesACPULimitAtOrAboveItsRequest(t *testing.T) {
	for _, b := range []struct {
		name    string
		xr      map[string]any
		cluster []map[string]any
		want    map[string]string // role -> limits.cpu with no cpuLimit
	}{
		{"single", bjXR(t, "xr-ondemand.yaml"), bjCluster(t), map[string]string{"single": "1"}},
		{"single on volcano", vcXR(t, "xr-partitioned.yaml"), vcCluster(t), nil},
		{"gang", vcXR(t, "xr-partitioned.yaml"), vcCluster(t), map[string]string{"manager": "500m", "worker": "1"}},
		{"indexed", vcXR(t, "xr-partitioned.yaml"), vcNoVolcano(t), map[string]string{"indexed": "1"}},
	} {
		if b.want == nil {
			b.xr = bjWithParams(b.xr, map[string]any{"mode": "single", "workers": nil, "gridSize": nil})
			b.want = map[string]string{"single": "500m"}
		}
		run, _ := vcTemplate(t, vcMustRender(t, b.xr, b.cluster, nil))
		pods := vcPods(run)
		if len(pods) != len(b.want) {
			t.Errorf("%s: pods %v, want roles %v", b.name, pods, b.want)
		}
		for role, lim := range b.want {
			c, _ := vcContainerEnv(t, pods[role])
			if got := dig(c, "resources", "limits", "cpu"); got != lim || dig(c, "resources", "requests", "cpu") != lim {
				t.Errorf("%s %s: cpu request/limit = %v, want both %s", b.name, role, c["resources"], lim)
			}
		}
		// cpuLimit sets every pod's limit, and leaves the requests alone.
		res := map[string]any{}
		declared, _ := dig(b.xr, "spec", "parameters", "resources").(map[string]any)
		for k, v := range declared {
			res[k] = v
		}
		res["cpuLimit"] = "2"
		run, _ = vcTemplate(t, vcMustRender(t, bjWithParams(b.xr, map[string]any{"resources": res}), b.cluster, nil))
		for role, pod := range vcPods(run) {
			c, _ := vcContainerEnv(t, pod)
			if dig(c, "resources", "limits", "cpu") != "2" || dig(c, "resources", "requests", "cpu") != b.want[role] {
				t.Errorf("%s %s with cpuLimit 2: resources = %v", b.name, role, c["resources"])
			}
		}
	}
	// A cpuLimit below a request is refused, naming the request it is below: the
	// worker's when the worker asks for more than the manager.
	for _, c := range []struct {
		name    string
		xr      map[string]any
		res     map[string]any
		refused string
	}{
		{"single below", bjXR(t, "xr-ondemand.yaml"), map[string]any{"cpu": "1", "memory": "1536Mi", "cpuLimit": "500m"}, "cpuLimit 500m is below the CPU request 1"},
		{"single equal", bjXR(t, "xr-ondemand.yaml"), map[string]any{"cpu": "1", "memory": "1536Mi", "cpuLimit": "1000m"}, ""},
		{"below the worker", vcXR(t, "xr-partitioned.yaml"), map[string]any{"cpu": "500m", "memory": "1Gi", "workerCpu": "1", "workerMemory": "1536Mi", "cpuLimit": "750m"}, "below the CPU request 1"},
		{"below the manager", vcXR(t, "xr-partitioned.yaml"), map[string]any{"cpu": "2", "memory": "1Gi", "workerCpu": "500m", "workerMemory": "1Gi", "cpuLimit": "1"}, "below the CPU request 2"},
		{"at the larger", vcXR(t, "xr-partitioned.yaml"), map[string]any{"cpu": "500m", "memory": "1Gi", "workerCpu": "1", "workerMemory": "1536Mi", "cpuLimit": "1"}, ""},
	} {
		cluster := bjCluster(t)
		if dig(c.xr, "spec", "parameters", "mode") == "partitioned" {
			cluster = vcCluster(t)
		}
		_, err := vcRender(t, bjWithParams(c.xr, map[string]any{"resources": c.res}), cluster, nil)
		switch {
		case c.refused == "" && err != nil:
			t.Errorf("%s: refused: %v", c.name, err)
		case c.refused != "" && (err == nil || !strings.Contains(err.Error(), c.refused)):
			t.Errorf("%s: want a refusal saying %q, got %v", c.name, c.refused, err)
		}
	}
}

// The same refusal through the real engine: it reads the quota, which is asked for on
// a pass of its own, so it must fire on the pass that has it and not before.
func TestQuotaRefusalPassesWithCrossplane(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: skipping `crossplane render`, which starts the function containers")
	}
	bin, err := exec.LookPath("crossplane")
	if err != nil {
		t.Skip("the crossplane CLI is not installed; the in-process renders still ran")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("Docker is not available; `crossplane render` runs each function in a container")
	}
	dir := t.TempDir()
	write := func(name string, docs ...map[string]any) string {
		var parts []string
		for _, d := range docs {
			y, _ := yaml.Marshal(d)
			parts = append(parts, string(y))
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(strings.Join(parts, "---\n")), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	render := func(name string, cluster []map[string]any) (string, error) {
		out, err := exec.Command(bin, "render", write(name+"-xr.yaml", vcXR(t, "xr-partitioned.yaml")),
			filepath.Join(controlPlaneDir(t), bjComposition), filepath.Join(bjFixtureDir, "functions.yaml"),
			"--required-resources="+write(name+"-required.yaml", cluster...)).CombinedOutput()
		return string(out), err
	}
	over := map[string]any{"requests.cpu": "4", "limits.cpu": "16", "requests.memory": "32Gi", "limits.memory": "32Gi", "pods": "100"}
	for _, c := range []struct {
		name    string
		cluster []map[string]any
	}{{"gang", vcCluster(t)}, {"indexed", vcNoVolcano(t)}} {
		if out, err := render(c.name+"-over", vcQuota(c.cluster, "dev-quota", over)); err == nil || !strings.Contains(out, "requests.cpu") {
			t.Errorf("%s over requests.cpu 4: the real engine did not refuse it: %v\n%s", c.name, err, out)
		}
		if out, err := render(c.name+"-fits", c.cluster); err != nil {
			t.Errorf("%s within the quota: refused by the real engine: %v\n%s", c.name, err, out)
		}
	}
}

// A run pod as the Job controller would create it passes the environment's
// LimitRange on the live API server (server-side dry run, never a write). Opt-in:
// ADHAR_LIVE_DRY_RUN=1 with a kube context whose cluster has a `prod` namespace.
// Without its own CPU limit the LimitRange defaults one of 500m and the pod's 1-CPU
// request is rejected: the run would never start.
func TestRunPodsPassTheEnvironmentLimitRange(t *testing.T) {
	if os.Getenv("ADHAR_LIVE_DRY_RUN") != "1" {
		t.Skip("ADHAR_LIVE_DRY_RUN=1 runs `kubectl apply --dry-run=server` against the current context")
	}
	dir := t.TempDir()
	for _, b := range []struct{ name, xr string }{{"single", "xr-scheduled"}, {"indexed", "xr-partitioned-scheduled.yaml"}} {
		xr := bjWithParams(vcBranchXR(t, b.xr), map[string]any{"image": "registry.k8s.io/pause:3.10"})
		run, _ := vcTemplate(t, vcMustRender(t, xr, vcNoVolcano(t), nil))
		for role, tpl := range vcPods(run) {
			md, _ := tpl["metadata"].(map[string]any)
			if md == nil {
				md = map[string]any{}
			}
			md["name"] = "dry-run-" + b.name + "-" + role
			// The run's ServiceAccount is composed with the BatchJob and is not on the
			// cluster; ServiceAccount admission would answer before the LimitRange.
			spec := tpl["spec"].(map[string]any)
			spec["serviceAccountName"] = "default"
			pod := map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": md, "spec": spec}
			y, _ := yaml.Marshal(pod)
			p := filepath.Join(dir, b.name+"-"+role+".yaml")
			if err := os.WriteFile(p, y, 0o644); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command("kubectl", "apply", "--dry-run=server", "--validate=strict", "-n", "prod", "-f", p).CombinedOutput()
			// The LimitRanger and ResourceQuota admission run before the webhooks, so a
			// webhook's answer means they passed; a policy that denies a bare Pod outside
			// a Job is not what this checks.
			if err != nil && !strings.Contains(string(out), "admission webhook") {
				t.Errorf("%s %s: the environment's LimitRange or quota rejects the run pod: %v\n%s", b.name, role, err, out)
			}
			t.Logf("%s %s:\n%s", b.name, role, out)
		}
	}
}
