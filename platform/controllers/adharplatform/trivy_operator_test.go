package adharplatform

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Completed CI pods are not workloads. A Tekton TaskRun pod has no built-in
// controller, so trivy-operator scanned it as a bare Pod — every init and step
// container into its own VulnerabilityReport AND SbomReport — and kept them for
// as long as the finished pod existed: 257 of 700 VulnerabilityReports and 236
// of 673 SbomReports on 2026-11-03, all for Succeeded/Failed pods. Those
// reports are the bulk of what kube-apiserver holds decoded in its watch cache,
// and they took a 16 GiB control plane to 108% memory. skipResourceByLabels
// matches label KEYS on the scanned resource.
//
// The operator reads the `trivy-operator` ConfigMap ONCE, at startup, and the
// chart's checksum annotation covers only the other ConfigMap
// (trivy-operator-config) — so the Deployment carries the sha256 of this one,
// or a change to it sits unread until the pod happens to restart.
func TestTrivyOperatorSkipsCIPodsAndRollsWhenItsConfigMapChanges(t *testing.T) {
	var data, annotations map[string]interface{}
	for _, d := range readYAMLDocs(t, filepath.Join(stackPackagesDir(t), "security/trivy/manifests/install.yaml")) {
		switch {
		case d["kind"] == "ConfigMap" && dig(d, "metadata", "name") == "trivy-operator":
			data, _ = d["data"].(map[string]interface{})
		case d["kind"] == "Deployment" && dig(d, "metadata", "name") == "trivy-operator":
			annotations, _ = dig(d, "spec", "template", "metadata", "annotations").(map[string]interface{})
		}
	}
	if data == nil || annotations == nil {
		t.Fatal("security/trivy install.yaml has no trivy-operator ConfigMap data or Deployment pod annotations")
	}
	skip, _ := data["skipResourceByLabels"].(string)
	for _, key := range []string{"tekton.dev/taskRun", "kpack.io/build"} {
		if !slices.Contains(strings.Split(skip, ","), key) {
			t.Errorf("trivy-operator scans pods labelled %s (skipResourceByLabels=%q): a completed CI pod's reports are never read and the API server holds them all", key, skip)
		}
	}

	canonical, err := json.Marshal(data) // map keys are sorted
	if err != nil {
		t.Fatal(err)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(canonical))
	if got := annotations["adhar.io/operator-configmap-sha256"]; got != sum {
		t.Errorf("the trivy-operator Deployment's adhar.io/operator-configmap-sha256 is %v, the ConfigMap it reads at startup hashes to %s: set podAnnotations in security/trivy/values.yaml and the Deployment in install.yaml to it, or the change is never read", got, sum)
	}
	values := readStackFile(t, "security/trivy/values.yaml")
	for _, want := range []string{`skipResourceByLabels: "` + skip + `"`, `adhar.io/operator-configmap-sha256: "` + sum + `"`} {
		if !strings.Contains(values, want) {
			t.Errorf("security/trivy/values.yaml lacks %s; the next generate-manifests.sh run would drop it", want)
		}
	}
}

// The console's runtime findings read trivy-operator's VulnerabilityReports and
// its license inventory reads SbomReports. The package was disabled in
// production for no recorded reason (2026-08-30), so both pages said
// "trivy-operator is not installed" (GCP, 2026-10-21). Its scan load is
// bounded through the keys the chart actually reads — the concurrency limit
// had sat under a `trivyOperator:` key the chart never had, leaving the
// default of ten concurrent scans.
func TestTrivyOperatorRunsInProductionWithBoundedScans(t *testing.T) {
	appset := readStackFile(t, "../adhar-appset-production.yaml")
	if !strings.Contains(appset, "- name: \"trivy\"\n            enabled: \"true\"") {
		t.Error("trivy-operator is not enabled in the production profile")
	}
	install := readStackFile(t, "security/trivy/manifests/install.yaml")
	for _, want := range []string{
		`OPERATOR_CONCURRENT_SCAN_JOBS_LIMIT: "3"`,
		`OPERATOR_BUILT_IN_TRIVY_SERVER: "true"`,
		`OPERATOR_SBOM_GENERATION_ENABLED: "true"`,
		`trivy.mode: "ClientServer"`,
	} {
		if !strings.Contains(install, want) {
			t.Errorf("trivy-operator's rendered config lacks %s", want)
		}
	}
}

// On a single 4-vCPU Kind node the packages' CPU REQUESTS reserved 98% of the
// node, so the console, Prometheus, RustFS and Keycloak waited in
// "Insufficient cpu" and E2E failed on scheduling races on both engines
// (2026-10-21). The local profile caps requests; production must not.
func TestTheLocalProfileCapsCPURequestsAndProductionDoesNot(t *testing.T) {
	local := readStackFile(t, "../adhar-appset-local.yaml")
	prod := readStackFile(t, "../adhar-appset-production.yaml")
	if !strings.Contains(local, "- name: \"adhar-local-budget\"\n            enabled: \"true\"") {
		t.Error("the local profile does not enable adhar-local-budget")
	}
	if !strings.Contains(prod, "- name: \"adhar-local-budget\"\n            enabled: \"false\"") {
		t.Error("production enables adhar-local-budget; capping requests there would starve the node autoscaler of its signal")
	}
	policy := readStackFile(t, "security/kyverno/local-budget/cpu-request-cap.yaml")
	for _, want := range []string{"failurePolicy: Ignore", "cpu: 50m", "operator: GreaterThan"} {
		if !strings.Contains(policy, want) {
			t.Errorf("the local CPU request cap lacks %q", want)
		}
	}
	if strings.Contains(policy, "limits:") {
		t.Error("the local CPU request cap touches limits; it must only lower requests")
	}
}
