package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// Design 0025 §11.4, PLAT-3.1: the batch observer's SLA gauges
// (adhar_batch_deadline_seconds_remaining, _projected_slack_seconds) are served
// by the in-cluster manager's metrics endpoint and read by the Mimir ruler
// (observability/mimir/manifests/batch-app-alerts.yaml). Three things have to
// agree for BatchDeadlineEndangered/Missed to fire at all, and each one missing
// reads exactly like a batch platform that meets every deadline:
//
//   - the manager serves metrics: the flag's default is "0", which is how the
//     gauges shipped — registered, and served to nobody;
//   - the pod declares that port and asks Alloy to scrape it — Alloy's
//     annotated_pods scrape, not a PodMonitor, is what carries a pod's metrics
//     to Mimir (Prometheus does not remote-write);
//   - Alloy still has that scrape, forwarding to the hub's Mimir.
const (
	observerManagerManifest = "../resources/manager/manager.yaml"
	observerAlloyValues     = "observability/alloy/values.yaml"
	observerControllerCmd   = "../../../cmd/controller/controller.go"
)

var observerManagerTemplateVar = regexp.MustCompile(`\{\{[^}]*\}\}`)

func observerManagerDeployment(t *testing.T) map[string]interface{} {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(observerManagerManifest))
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range strings.Split(observerManagerTemplateVar.ReplaceAllString(string(raw), "x"), "\n---") {
		var obj map[string]interface{}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatalf("%s: %v", observerManagerManifest, err)
		}
		if obj["kind"] == "Deployment" {
			return obj
		}
	}
	t.Fatalf("%s has no Deployment", observerManagerManifest)
	return nil
}

func observerDig(obj interface{}, path ...string) interface{} {
	for _, p := range path {
		m, ok := obj.(map[string]interface{})
		if !ok {
			return nil
		}
		obj = m[p]
	}
	return obj
}

func TestTheBatchObserversGaugesAreScrapedIntoMimir(t *testing.T) {
	deploy := observerManagerDeployment(t)
	containers, _ := observerDig(deploy, "spec", "template", "spec", "containers").([]interface{})
	if len(containers) == 0 {
		t.Fatal("the manager Deployment has no containers")
	}
	manager, _ := containers[0].(map[string]interface{})

	port := ""
	args, _ := manager["args"].([]interface{})
	for _, a := range args {
		if s, _ := a.(string); strings.HasPrefix(s, "--metrics-bind-address=") {
			addr := strings.TrimPrefix(s, "--metrics-bind-address=")
			if i := strings.LastIndex(addr, ":"); i >= 0 {
				port = addr[i+1:]
			}
		}
	}
	if port == "" || port == "0" {
		t.Fatalf("the manager does not serve metrics (--metrics-bind-address unset or 0, args %v): the SLA gauges are registered and scraped by nobody", args)
	}
	cmd, err := os.ReadFile(filepath.FromSlash(observerControllerCmd))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cmd), `"metrics-bind-address"`) {
		t.Errorf("%s no longer defines --metrics-bind-address, which the manager Deployment passes", observerControllerCmd)
	}

	declared := false
	ports, _ := manager["ports"].([]interface{})
	for _, p := range ports {
		m, _ := p.(map[string]interface{})
		if v, ok := m["containerPort"].(float64); ok && observerPort(v) == port {
			declared = true
		}
	}
	if !declared {
		t.Errorf("the manager container does not declare port %s: Alloy's pod discovery makes a target per declared port, so an undeclared one is never scraped", port)
	}

	ann, _ := observerDig(deploy, "spec", "template", "metadata", "annotations").(map[string]interface{})
	if ann["prometheus.io/scrape"] != "true" {
		t.Errorf("the manager pod is not annotated prometheus.io/scrape: \"true\" (annotations %v): Alloy's annotated_pods scrape keeps only annotated pods", ann)
	}
	if ann["prometheus.io/port"] != port {
		t.Errorf("prometheus.io/port = %v, want %s (the metrics port)", ann["prometheus.io/port"], port)
	}

	alloy, err := os.ReadFile(filepath.Join(stackPackagesDir(t), filepath.FromSlash(observerAlloyValues)))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`prometheus.scrape "annotated_pods"`, "__meta_kubernetes_pod_annotation_prometheus_io_scrape", "prometheus.remote_write.hub.receiver"} {
		if !strings.Contains(string(alloy), want) {
			t.Errorf("%s no longer has %q: nothing carries the manager's metrics to Mimir", observerAlloyValues, want)
		}
	}
}

func observerPort(v float64) string {
	b, _ := yaml.Marshal(int64(v))
	return strings.TrimSpace(string(b))
}
