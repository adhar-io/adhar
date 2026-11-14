package batch

import (
	"os"
	"path/filepath"
	"testing"

	"adhar-io/adhar/tests/e2e"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// The canary's checks (CheckVolcanoGang, CheckIndexedFallback,
// CheckVolcanoSingle, CheckNativeRun) are what the live run templates are held
// to. testdata/volcano/ is the design's shape; these goldens are what the
// BatchJob composition actually renders for each branch (PLAT-2.5,
// platform/controllers/adharplatform/volcano_composition_test.go keeps them in
// step with the composition). Reading both here means the composition and the
// canary cannot drift apart without one of the two suites failing.

const compositionGoldens = "platform/controllers/adharplatform/testdata/batchjob-volcano"

// golden is one branch's render, as the composition lane digests it.
type golden struct {
	Composed struct {
		RunTemplate struct {
			Kind     string                 `json:"data.kind"`
			Template map[string]interface{} `json:"template.yaml"`
		} `json:"run-template"`
	} `json:"composed"`
	Status map[string]interface{} `json:"status"`
}

func readGolden(t *testing.T, name string) (*RunTemplate, golden) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(e2e.RepoRoot, compositionGoldens, name))
	require.NoError(t, err, "the composition's golden render %s", name)
	var g golden
	require.NoError(t, yaml.Unmarshal(raw, &g), name)
	require.NotEmpty(t, g.Composed.RunTemplate.Kind, "%s carries no run-template data.kind", name)
	tpl, err := yaml.Marshal(g.Composed.RunTemplate.Template)
	require.NoError(t, err, name)
	rt, err := ParseRunTemplateKinds(map[string]string{"naming": "v1", "kind": g.Composed.RunTemplate.Kind, "template.yaml": string(tpl)})
	require.NoError(t, err, name)
	return rt, g
}

// xrWorkers is spec.parameters.workers of a composition fixture.
func xrWorkers(t *testing.T, name string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(e2e.RepoRoot, compositionGoldens, name))
	require.NoError(t, err, name)
	var xr struct {
		Spec struct {
			Parameters struct {
				Workers int `json:"workers"`
			} `json:"parameters"`
		} `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &xr), name)
	require.Positive(t, xr.Spec.Parameters.Workers, "%s has no workers", name)
	return xr.Spec.Parameters.Workers
}

func TestTheCompositionsRendersPassTheCanarysChecks(t *testing.T) {
	workers := xrWorkers(t, "xr-partitioned.yaml")
	scheduledWorkers := xrWorkers(t, "xr-partitioned-scheduled.yaml")

	for _, c := range []struct {
		golden    string
		workers   int
		scheduler string
	}{
		{"golden-volcano-gang.yaml", workers, "volcano"},
		{"golden-volcano-cron.yaml", scheduledWorkers, "volcano"},
	} {
		t.Run(c.golden, func(t *testing.T) {
			rt, g := readGolden(t, c.golden)
			leaf, _ := g.Status["queue"].(string)
			require.NotEmpty(t, leaf, "a Volcano render reports status.queue")
			require.Equal(t, c.scheduler, g.Status["scheduler"])
			gang, p := CheckVolcanoGang(rt, c.workers, leaf)
			require.Empty(t, p, "the composition's gang render fails the canary's CheckVolcanoGang")
			require.Equal(t, leaf, gang.Queue, "the gang names the queue status.queue reports")
		})
	}
	for _, c := range []struct {
		golden  string
		workers int
	}{
		{"golden-native-partitioned.yaml", workers},
		// The descriptor removed (A2.10): the same partitioned job renders native.
		{"golden-volcano-absent.yaml", scheduledWorkers},
	} {
		t.Run(c.golden, func(t *testing.T) {
			rt, g := readGolden(t, c.golden)
			require.Equal(t, "kubernetes", g.Status["scheduler"])
			_, hasQueue := g.Status["queue"]
			require.False(t, hasQueue, "a native render reports no queue")
			require.Empty(t, CheckIndexedFallback(rt, c.workers), "the composition's native partitioned render fails the canary's CheckIndexedFallback")
		})
	}
	t.Run("golden-volcano-single.yaml", func(t *testing.T) {
		rt, g := readGolden(t, "golden-volcano-single.yaml")
		leaf, _ := g.Status["queue"].(string)
		require.NotEmpty(t, leaf)
		require.Empty(t, CheckVolcanoSingle(rt, leaf), "single mode on Volcano fails the canary's CheckVolcanoSingle")
		require.NotEmpty(t, CheckNativeRun(rt), "CheckNativeRun must refuse a Volcano single-mode render")
	})
	t.Run("golden-native-single.yaml", func(t *testing.T) {
		rt, _ := readGolden(t, "golden-native-single.yaml")
		require.Empty(t, CheckNativeRun(rt), "the native single-mode render fails the canary's CheckNativeRun (A2.10)")
		require.NotEmpty(t, CheckVolcanoSingle(rt, "acme-payments-prod"), "CheckVolcanoSingle must refuse a native render")
	})
}

// DEC-1: the same BatchJob takes its native class on kube-scheduler and that
// class's Volcano twin on Volcano — a critical job stays critical, a normal one
// never becomes critical — and every pod of a run names the one class.
func TestTheVolcanoRenderTakesTheTwinOfTheNativeClass(t *testing.T) {
	twin := map[string]string{"adhar-batch": ClassVolcano, "adhar-batch-critical": ClassVolcanoCritical}
	classOf := func(t *testing.T, name string) string {
		rt, _ := readGolden(t, name)
		seen := map[string]bool{}
		if c := str(rt.Manifest.Object, "spec", "priorityClassName"); c != "" {
			seen[c] = true
		}
		for _, spec := range podSpecsOf(rt) {
			seen[str(spec, "priorityClassName")] = true
		}
		require.Len(t, seen, 1, "%s: the run's pods name %v", name, seen)
		for c := range seen {
			return c
		}
		return ""
	}
	for _, pair := range [][2]string{
		{"golden-native-single.yaml", "golden-volcano-single.yaml"},
		{"golden-native-partitioned.yaml", "golden-volcano-gang.yaml"},
		{"golden-volcano-absent.yaml", "golden-volcano-cron.yaml"},
	} {
		native, volcano := classOf(t, pair[0]), classOf(t, pair[1])
		require.Contains(t, twin, native, "%s names %s, not a native batch class", pair[0], native)
		require.Equal(t, twin[native], volcano, "%s on Volcano names %s; its native render names %s (DEC-1)", pair[1], volcano, native)
	}
}
