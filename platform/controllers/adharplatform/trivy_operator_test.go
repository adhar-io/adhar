package adharplatform

import (
	"strings"
	"testing"
)

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
