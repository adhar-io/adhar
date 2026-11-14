package up

import (
	"context"
	"os"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/platform/config"
)

func batchNode(name string, cp bool, cpu, mem string, taints ...corev1.Taint) *corev1.Node {
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{}}}
	if cp {
		n.Labels["node-role.kubernetes.io/control-plane"] = ""
	}
	n.Spec.Taints = taints
	n.Status.Allocatable = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem)}
	return n
}

// The batch ceiling is measured from the cluster's workers (design 0025 D17): the
// smallest schedulable worker, times the most workers the cluster may have.
// Mutation-tested: the control-plane skip, the taint skip, the smallest-node
// choice and the autoscaler's maxWorkers were each reverted and a row seen failing.
func TestBatchQueuesAreSizedFromTheClustersWorkers(t *testing.T) {
	noSchedule := corev1.Taint{Key: "node-role.kubernetes.io/control-plane", Effect: corev1.TaintEffectNoSchedule}
	cases := []struct {
		name        string
		nodes       []*corev1.Node
		autoscaling *v1alpha1.AutoscalingSpec
		want        config.BatchScheduler
	}{
		{
			// The reference cluster, autoscaled to 10 workers of 7500m / 12.5Gi: the
			// control plane and its taint are not batch room.
			name: "workers, a tainted control plane, the autoscaler's maxWorkers",
			nodes: []*corev1.Node{
				batchNode("cp", true, "3500m", "13Gi", noSchedule),
				batchNode("w1", false, "7500m", "12800Mi"),
				batchNode("w2", false, "9", "13Gi"),
				// A tainted pool (GPU, a dedicated tenant) is not batch room.
				batchNode("gpu", false, "1", "2Gi", corev1.Taint{Key: "nvidia.com/gpu", Effect: corev1.TaintEffectNoSchedule}),
			},
			autoscaling: &v1alpha1.AutoscalingSpec{Enabled: true, MaxWorkers: 10},
			want:        config.BatchScheduler{CeilingCPU: "22", CeilingMemory: "25Gi", ProdDeservedCPU: "5500m", ProdDeservedMemory: "7Gi", Derived: true},
		},
		{
			name:  "a fixed-size cluster counts the workers it has",
			nodes: []*corev1.Node{batchNode("w1", false, "15500m", "60Gi"), batchNode("w2", false, "15500m", "60Gi")},
			want:  config.BatchScheduler{CeilingCPU: "9", CeilingMemory: "24Gi", ProdDeservedCPU: "4500m", ProdDeservedMemory: "7Gi", Derived: true},
		},
		{
			// An untainted control plane beside real workers is not what the
			// autoscaler adds, and it would make the smallest node the master.
			name:  "workers beside a schedulable control plane",
			nodes: []*corev1.Node{batchNode("cp", true, "2", "4Gi"), batchNode("w1", false, "15500m", "60Gi"), batchNode("w2", false, "15500m", "60Gi")},
			want:  config.BatchScheduler{CeilingCPU: "9", CeilingMemory: "24Gi", ProdDeservedCPU: "4500m", ProdDeservedMemory: "7Gi", Derived: true},
		},
		{
			name:        "the autoscaler switched off is a fixed-size cluster",
			nodes:       []*corev1.Node{batchNode("w1", false, "15500m", "60Gi"), batchNode("w2", false, "15500m", "60Gi")},
			autoscaling: &v1alpha1.AutoscalingSpec{Enabled: false, MaxWorkers: 10},
			want:        config.BatchScheduler{CeilingCPU: "9", CeilingMemory: "24Gi", ProdDeservedCPU: "4500m", ProdDeservedMemory: "7Gi", Derived: true},
		},
		{
			// Kind: one untainted control-plane node is the whole cluster.
			name:  "a single schedulable control-plane node",
			nodes: []*corev1.Node{batchNode("kind", true, "10", "15Gi")},
			want:  config.BatchScheduler{CeilingCPU: "3", CeilingMemory: "3Gi", ProdDeservedCPU: "3", ProdDeservedMemory: "3Gi", Derived: true},
		},
		{
			name:  "no schedulable node yet falls back to the reference sizing",
			nodes: []*corev1.Node{batchNode("cp", true, "4", "16Gi", noSchedule)},
			want:  config.BatchScheduler{CeilingCPU: "22", CeilingMemory: "24Gi", ProdDeservedCPU: "5500m", ProdDeservedMemory: "7Gi"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := fake.NewClientBuilder()
			for _, n := range c.nodes {
				b = b.WithObjects(n)
			}
			got, _, err := batchSchedulerSizing(context.Background(), b.Build(), nil, nil, c.autoscaling)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("got %+v\nwant %+v", got, c.want)
			}
		})
	}
}

// An unmeasurable cluster is estimated from the worker machine type before it
// falls back: 8 vCPU / 16 GiB less the kubelet reserve platform/providers
// kubeadm.go configures (200m+300m CPU; 1Gi + 1536Mi + a 1Gi eviction margin).
func TestEstimatedWorkerAllocatableSubtractsTheKubeletReserve(t *testing.T) {
	if nodeReservedCPUMilli != 500 || nodeReservedMemory != 3584<<20 {
		t.Errorf("reserve %dm / %d bytes; kubeadm.go reserves 500m and 3.5Gi", nodeReservedCPUMilli, nodeReservedMemory)
	}
	src, err := os.ReadFile("../../platform/providers/kubeadm.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"systemReserved:\n  cpu: 200m\n  memory: 1Gi"`, `"kubeReserved:\n  cpu: 300m\n  memory: 1536Mi"`, `memory.available: 1Gi`} {
		if !strings.Contains(string(src), want) {
			t.Errorf("platform/providers/kubeadm.go no longer configures %s; update nodeReserved* to match", want)
		}
	}
}

// The sizing reaches the stack: bootstrap records it on the template data BEFORE
// the controllers that stage and seed the stack start, on both paths.
func TestBatchSizingIsRecordedBeforeTheStackIsSeeded(t *testing.T) {
	for file, data := range map[string]string{"bootstrap.go": "&templateData", "local.go": "&lp.options.TemplateData"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		s := string(src)
		rec := strings.Index(s, "withBatchSizing("+data)
		run := strings.Index(s, "RunControllers(")
		if file == "local.go" {
			run = strings.Index(s, "lp.RunControllers(")
		}
		if rec < 0 || run < 0 || rec > run {
			t.Errorf("%s: withBatchSizing(%s, …) at %d must come before RunControllers at %d", file, data, rec, run)
		}
	}
}

func TestWithBatchSizingFillsEveryField(t *testing.T) {
	var spec v1alpha1.BuildCustomizationSpec
	withBatchSizing(&spec, config.BatchScheduler{CeilingCPU: "1", CeilingMemory: "2Gi", ProdDeservedCPU: "3", ProdDeservedMemory: "4Gi"})
	if spec.BatchCeilingCPU != "1" || spec.BatchCeilingMemory != "2Gi" || spec.BatchProdDeservedCPU != "3" || spec.BatchProdDeservedMemory != "4Gi" {
		t.Errorf("spec = %+v", spec)
	}
}
