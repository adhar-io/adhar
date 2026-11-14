package batch

import (
	"os"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// The DEC-3 subtests size their BatchJobs from the leaf they find, so each
// lands on exactly one of the composition's QueueFits verdicts: the
// composition checks capability (ANY dimension) before deserved (EVERY
// dimension), so a run meant to read ExceedsDeserved must stay within the
// capability, and the old "each worker the whole deserved share" did not
// once deserved exceeded half the capability (D8 caps it AT the capability).

func resources(cpu, mem string) corev1.ResourceList {
	l := corev1.ResourceList{}
	if cpu != "" {
		l[corev1.ResourceCPU] = resource.MustParse(cpu)
	}
	if mem != "" {
		l[corev1.ResourceMemory] = resource.MustParse(mem)
	}
	return l
}

// verdict is the composition's QueueFits order (kubernetes.yaml, DEC-3)
// applied to an ask, written down here so the sizing is checked against it.
func verdict(ask Room, deserved, capability corev1.ResourceList) string {
	c := roomOf(capability)
	if _, ok := capability[corev1.ResourceCPU]; ok && ask.MilliCPU > c.MilliCPU {
		return "ExceedsCapability"
	}
	if _, ok := capability[corev1.ResourceMemory]; ok && ask.Memory > c.Memory {
		return "ExceedsCapability"
	}
	d, compared, exceeded := roomOf(deserved), 0, 0
	if _, ok := deserved[corev1.ResourceCPU]; ok {
		compared++
		if ask.MilliCPU > d.MilliCPU {
			exceeded++
		}
	}
	if _, ok := deserved[corev1.ResourceMemory]; ok {
		compared++
		if ask.Memory > d.Memory {
			exceeded++
		}
	}
	if compared > 0 && compared == exceeded {
		return "ExceedsDeserved"
	}
	return "Fits"
}

func TestTheOversizeProdRunExceedsDeservedAndNotCapability(t *testing.T) {
	for _, c := range []struct{ name, dCPU, dMem, cCPU, cMem string }{
		{"the descriptor's default share under a roomy ceiling", "2", "4Gi", "12", "24Gi"},
		{"deserved more than half the capability", "6", "12Gi", "8", "16Gi"},
		{"an odd millicore share", "1501m", "3001Mi", "4", "8Gi"},
		{"a ceiling only on CPU", "2", "4Gi", "5", ""},
		{"no ceiling at all", "2", "4Gi", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			deserved, capability := resources(c.dCPU, c.dMem), resources(c.cCPU, c.cMem)
			run, err := RunOverDeserved(deserved, capability)
			if err != nil {
				t.Fatalf("RunOverDeserved(%v, %v): %v", deserved, capability, err)
			}
			if got := verdict(run.Ask(), deserved, capability); got != "ExceedsDeserved" {
				t.Errorf("a run of %s (workers %d x %s) under deserved %v / capability %v reads %s, want ExceedsDeserved", run.Ask(), run.Workers, run.Worker, deserved, capability, got)
			}
			if run.Workers < 1 || run.Worker.MilliCPU <= 0 || run.Worker.Memory <= 0 || run.Worker.Memory%(1<<20) != 0 {
				t.Errorf("worker %s x %d is not a run the XRD takes (workers ≥ 1, whole Mi)", run.Worker, run.Workers)
			}
			res := run.Resources()
			for k, want := range map[string]int64{"cpu": QueueFitsManager.MilliCPU, "workerCpu": run.Worker.MilliCPU} {
				q := resource.MustParse(res[k].(string))
				if q.MilliValue() != want {
					t.Errorf("resources.%s = %s, want %dm", k, res[k], want)
				}
			}
			for k, want := range map[string]int64{"memory": QueueFitsManager.Memory, "workerMemory": run.Worker.Memory} {
				q := resource.MustParse(res[k].(string))
				if q.Value() != want {
					t.Errorf("resources.%s = %s, want %d bytes", k, res[k], want)
				}
			}
		})
	}
}

func TestAProdLeafWithNoRoomPastDeservedIsSaidSo(t *testing.T) {
	for _, c := range []struct{ name, dCPU, dMem, cCPU, cMem string }{
		{"deserved capped at the capability (CPU)", "4", "4Gi", "4", "16Gi"},
		{"deserved capped at the capability (memory)", "2", "8Gi", "16", "8Gi"},
		{"no deserved CPU", "", "4Gi", "8", "16Gi"},
		{"no deserved memory", "2", "", "8", "16Gi"},
		{"a zero share", "0", "4Gi", "8", "16Gi"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if run, err := RunOverDeserved(resources(c.dCPU, c.dMem), resources(c.cCPU, c.cMem)); err == nil {
				t.Errorf("RunOverDeserved sized %s (workers %d x %s): the composition would not say ExceedsDeserved for it", run.Ask(), run.Workers, run.Worker)
			}
		})
	}
}

func TestTheOverCapabilityRunExceedsItOnAnyTier(t *testing.T) {
	for _, c := range []struct{ name, dCPU, dMem, cCPU, cMem string }{
		{"a dev leaf (no deserved)", "", "", "12", "24Gi"},
		{"a prod leaf, deserved under capability", "2", "4Gi", "12", "24Gi"},
		{"an odd ceiling", "", "", "3333m", "7Gi"},
		{"a leaf capped only on memory", "", "", "", "8Gi"},
	} {
		t.Run(c.name, func(t *testing.T) {
			deserved, capability := resources(c.dCPU, c.dMem), resources(c.cCPU, c.cMem)
			run, err := RunOverCapability(capability)
			if err != nil {
				t.Fatalf("RunOverCapability(%v): %v", capability, err)
			}
			if got := verdict(run.Ask(), deserved, capability); got != "ExceedsCapability" {
				t.Errorf("a run of %s over capability %v reads %s, want ExceedsCapability", run.Ask(), capability, got)
			}
			if run.Worker.MilliCPU <= 0 || run.Worker.Memory <= 0 {
				t.Errorf("worker %s is not a run the XRD takes", run.Worker)
			}
		})
	}
	if run, err := RunOverCapability(corev1.ResourceList{}); err == nil {
		t.Errorf("a leaf with no capability was given an over-capability run %s", run.Ask())
	}
}

// The live subtests size from the leaf through these helpers, not by hand,
// and both DEC-3 verdicts are run.
func TestTheDEC3SubtestsSizeFromTheLeaf(t *testing.T) {
	raw, err := os.ReadFile("volcano_e2e_test.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		"RunOverDeserved(q.Deserved, q.Capability)",
		"RunOverCapability(q.Capability)",
		`assert.Equal(t, "ExceedsDeserved", reason)`,
		`assert.Equal(t, "ExceedsCapability", reason)`,
		"v.prodLeafDoesNotFit)",
		"v.leafCapabilityExceeded)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("volcano_e2e_test.go no longer carries %q", want)
		}
	}
}
