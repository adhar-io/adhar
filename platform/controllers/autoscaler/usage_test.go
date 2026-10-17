package autoscaler

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"adhar-io/adhar/api/v1alpha1"
)

func tinyRunningPod(name, node string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "adhar-system", Name: name},
		Spec: corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{
			Name: "app",
			Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewMilliQuantity(20, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(64*1024*1024, resource.BinarySI),
			}},
		}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

// Requests of 20m per pod read as a half-empty fleet while every worker runs at
// its full CPU; the decision must see the measured figure and grow by half.
func TestASaturatedFleetScalesUpOnMeasuredCpu(t *testing.T) {
	spec := (&v1alpha1.AutoscalingSpec{Enabled: true, MinWorkers: 2, MaxWorkers: 8}).WithDefaults()
	workers := []corev1.Node{workerSized("w0", 3500, 28), workerSized("w1", 3500, 28), workerSized("w2", 3500, 28)}
	var pods []corev1.Pod
	for i, w := range workers {
		for j := 0; j < 20; j++ {
			pods = append(pods, tinyRunningPod("p"+string(rune('a'+i))+string(rune('a'+j)), w.Name))
		}
	}
	s := Snapshot{Spec: spec, Nodes: append([]corev1.Node{controlPlane()}, workers...), Pods: pods, VolumesPerNode: 8, Now: time.Now()}

	blind := Decide(s)
	if blind.Action != ActionNone {
		t.Fatalf("with requests alone the fleet looks idle, yet the decision was %s (%s)", blind.Action, blind.Reason)
	}

	s.NodeCPUUsageMilli = map[string]int64{"w0": 3450, "w1": 3400, "w2": 3380}
	d := Decide(s)
	if d.Action != ActionScaleUp {
		t.Fatalf("three workers at ~98%% measured CPU did not scale up: %s", d.Reason)
	}
	if d.ScaleUpBy != 2 {
		t.Fatalf("a saturated fleet of three should grow by half (2), got %d: %s", d.ScaleUpBy, d.Reason)
	}
	if !d.Utilization.Measured || d.Utilization.MeasuredCPU < 0.95 {
		t.Fatalf("the measured utilization was not carried in the decision: %+v", d.Utilization)
	}
}

func TestKubeletSummaryCpuIsReadInMillicores(t *testing.T) {
	raw := []byte(`{"node":{"nodeName":"w0","cpu":{"time":"2026-10-17T01:53:00Z","usageNanoCores":3974000000,"usageCoreNanoSeconds":1}}}`)
	got, err := parseSummaryCPUMilli(raw)
	if err != nil || got != 3974 {
		t.Fatalf("parseSummaryCPUMilli = %d, %v; want 3974", got, err)
	}
	if _, err := parseSummaryCPUMilli([]byte(`{"node":{}}`)); err == nil {
		t.Fatal("a summary without cpu usage must not parse as zero usage")
	}
}
