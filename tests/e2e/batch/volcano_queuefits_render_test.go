package batch

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"adhar-io/adhar/tests/e2e"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

// The DEC-3 subtests' sizing, rendered by the real composition: `crossplane
// render` of the BatchJob composition with the composition lane's fixture
// cluster, the leaf edited to each case's deserved share and capability, and
// the XR sized by RunOverDeserved / RunOverCapability. verdict() in
// volcano_queuefits_test.go is a transcription; this is the composition.

const (
	bjCompositionPath = "platform/controlplane/configuration/compositions/batchjob/kubernetes.yaml"
	bjFunctionsPath   = "platform/controllers/adharplatform/testdata/batchjob/functions.yaml"
	bjRequiredPath    = compositionGoldens + "/required-resources.yaml"
	bjPartitionedXR   = compositionGoldens + "/xr-partitioned.yaml"
)

// renderQueueFits renders a partitioned BatchJob of run's size in ns, whose leaf
// (acme-payments-<ns>) has the given deserved share and capability, and returns
// its QueueFits reason.
func renderQueueFits(t *testing.T, bin, ns string, run SizedRun, deserved, capability corev1.ResourceList) string {
	t.Helper()
	dir := t.TempDir()
	raw, err := os.ReadFile(filepath.Join(e2e.RepoRoot, bjRequiredPath))
	require.NoError(t, err)
	leaf, edited := "acme-payments-"+ns, false
	var docs []string
	for _, d := range strings.Split(string(raw), "\n---") {
		var m map[string]interface{}
		if err := yaml.Unmarshal([]byte(d), &m); err != nil || len(m) == 0 {
			continue
		}
		if md, _ := m["metadata"].(map[string]interface{}); m["kind"] == "Queue" && md["name"] == leaf {
			spec, _ := m["spec"].(map[string]interface{})
			delete(spec, "capability")
			delete(spec, "deserved")
			for key, l := range map[string]corev1.ResourceList{"capability": capability, "deserved": deserved} {
				if len(l) == 0 {
					continue
				}
				out := map[string]interface{}{}
				for r, q := range l {
					out[string(r)] = q.String()
				}
				spec[key] = out
			}
			edited = true
		}
		y, err := yaml.Marshal(m)
		require.NoError(t, err)
		docs = append(docs, string(y))
	}
	require.True(t, edited, "the fixture cluster has no Queue %s", leaf)
	required := filepath.Join(dir, "required.yaml")
	require.NoError(t, os.WriteFile(required, []byte(strings.Join(docs, "---\n")), 0o644))

	raw, err = os.ReadFile(filepath.Join(e2e.RepoRoot, bjPartitionedXR))
	require.NoError(t, err)
	var xr map[string]interface{}
	require.NoError(t, yaml.Unmarshal(raw, &xr))
	xr["metadata"].(map[string]interface{})["namespace"] = ns
	params := xr["spec"].(map[string]interface{})["parameters"].(map[string]interface{})
	params["workers"], params["resources"] = run.Workers, run.Resources()
	y, err := yaml.Marshal(xr)
	require.NoError(t, err)
	xrPath := filepath.Join(dir, "xr.yaml")
	require.NoError(t, os.WriteFile(xrPath, y, 0o644))

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, "render", xrPath, filepath.Join(e2e.RepoRoot, bjCompositionPath), filepath.Join(e2e.RepoRoot, bjFunctionsPath), "--required-resources="+required)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	require.NoError(t, cmd.Run(), "crossplane render: %s", stderr.String())
	for _, d := range strings.Split(stdout.String(), "\n---") {
		var m struct {
			Kind   string `json:"kind"`
			Status struct {
				Conditions []map[string]interface{} `json:"conditions"`
			} `json:"status"`
		}
		if yaml.Unmarshal([]byte(d), &m) != nil || m.Kind != "BatchJob" {
			continue
		}
		for _, c := range m.Status.Conditions {
			if c["type"] == "QueueFits" {
				t.Logf("%s asks %s against deserved %s / capability %s: QueueFits %v %v: %v", ns, run.Ask(), roomOf(deserved), roomOf(capability), c["status"], c["reason"], c["message"])
				return c["reason"].(string)
			}
		}
	}
	t.Fatalf("the render carries no QueueFits condition:\n%s", stdout.String())
	return ""
}

func TestTheDEC3SizingLandsOnTheCompositionsVerdict(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: skipping `crossplane render`, which starts the function containers")
	}
	bin, err := exec.LookPath("crossplane")
	if err != nil {
		t.Skip("the crossplane CLI is not installed; TestTheOversizeProdRunExceedsDeservedAndNotCapability still ran")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("Docker is not available; `crossplane render` runs each function in a container")
	}
	t.Run("prod, deserved more than half the capability: ExceedsDeserved", func(t *testing.T) {
		deserved, capability := resources("6", "12Gi"), resources("8", "16Gi")
		run, err := RunOverDeserved(deserved, capability)
		require.NoError(t, err)
		require.Equal(t, "ExceedsDeserved", renderQueueFits(t, bin, "prod", run, deserved, capability))
	})
	t.Run("dev, a gang past the leaf's capability: ExceedsCapability", func(t *testing.T) {
		capability := resources("8", "16Gi")
		run, err := RunOverCapability(capability)
		require.NoError(t, err)
		require.Equal(t, "ExceedsCapability", renderQueueFits(t, bin, "dev", run, nil, capability))
	})
}
