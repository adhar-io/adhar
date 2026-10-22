package adharplatform

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// Prometheus must never be able to preempt. Most stateful pods on this
// platform (caches, databases, the shared Kafka broker) live on node-local
// volumes at priority 0: a preempted one can only come back on the node its
// volume is on. Giving Prometheus a preempting priority (to get it scheduled
// on a full cluster) evicted the shared Kafka broker from its only node and
// left it Pending (2026-10-23). A Prometheus that cannot schedule is a capacity
// problem for the autoscaler, not something to win by evicting stateful pods.
func TestPrometheusNeverPreemptsNodePinnedWorkloads(t *testing.T) {
	install := readStackFile(t, "observability/kube-prometheus/manifests/install.yaml")
	for _, doc := range strings.Split(install, "\n---\n") {
		if !strings.Contains(doc, "\nkind: Prometheus\n") {
			continue
		}
		var p struct {
			APIVersion string `json:"apiVersion"`
			Spec       struct {
				PriorityClassName string `json:"priorityClassName"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal([]byte(doc), &p); err != nil || p.APIVersion != "monitoring.coreos.com/v1" {
			continue
		}
		if p.Spec.PriorityClassName != "" {
			t.Errorf("the Prometheus CR sets priorityClassName %q; it would preempt node-pinned stateful pods", p.Spec.PriorityClassName)
		}
	}
	if !strings.Contains(readStackFile(t, "observability/kube-prometheus/values.yaml"), "    priorityClassName: \"\"\n") {
		t.Error("values.yaml no longer pins prometheusSpec.priorityClassName to empty")
	}
}
