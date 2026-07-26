package adharplatform

import (
	"strings"
	"testing"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/platform/utils/files"
)

// The enableHAMode gate selects the pre-rendered install-ha.yaml variants for
// ArgoCD and Gitea (roadmap P1.2). These tests guard the invariants that make
// that selection meaningful: both variants exist, and the HA renderings carry
// replicas and disruption budgets.
func TestArgoCDHAManifestInvariants(t *testing.T) {
	base, err := argoCDFS.ReadFile("resources/argocd/install.yaml")
	if err != nil {
		t.Fatalf("reading install.yaml: %v", err)
	}
	ha, err := argoCDFS.ReadFile("resources/argocd/install-ha.yaml")
	if err != nil {
		t.Fatalf("reading install-ha.yaml: %v", err)
	}

	haStr := string(ha)
	if !strings.Contains(haStr, "kind: PodDisruptionBudget") {
		t.Error("ArgoCD HA manifest must contain PodDisruptionBudgets")
	}
	if !strings.Contains(haStr, "replicas: 2") && !strings.Contains(haStr, "replicas: 3") {
		t.Error("ArgoCD HA manifest must scale components beyond one replica")
	}
	// Both variants must be rendered from the same chart version, or an HA
	// toggle silently up/downgrades ArgoCD.
	baseChart := chartLine(string(base))
	haChart := chartLine(haStr)
	if baseChart == "" || baseChart != haChart {
		t.Errorf("chart version mismatch between install.yaml (%q) and install-ha.yaml (%q)", baseChart, haChart)
	}
	// SSO parity (roadmap P1.4): an HA toggle must never silently drop the
	// OIDC wiring.
	for name, content := range map[string]string{"install.yaml": string(base), "install-ha.yaml": haStr} {
		if !strings.Contains(content, "oidc.config: |") {
			t.Errorf("%s must carry the Keycloak oidc.config block", name)
		}
	}
}

func TestGiteaHAManifestInvariants(t *testing.T) {
	if _, err := giteaFS.ReadFile("resources/gitea/install.yaml"); err != nil {
		t.Fatalf("reading install.yaml: %v", err)
	}
	ha, err := giteaFS.ReadFile("resources/gitea/install-ha.yaml")
	if err != nil {
		t.Fatalf("reading install-ha.yaml: %v", err)
	}
	haStr := string(ha)
	if !strings.Contains(haStr, "kind: PodDisruptionBudget") {
		t.Error("Gitea HA manifest must contain PodDisruptionBudgets")
	}
	// In HA mode Gitea's database is the CNPG-managed gitea-db cluster
	// (P1.2b); the chart-bundled PostgreSQL must be gone or the two fight
	// over the same database identity.
	if !strings.Contains(haStr, "HOST=gitea-db-rw.adhar-system.svc.cluster.local:5432") {
		t.Error("Gitea HA manifest must point at the CNPG gitea-db-rw service")
	}
	if strings.Contains(haStr, "gitea-postgresql") {
		t.Error("Gitea HA manifest must not contain chart-bundled PostgreSQL resources")
	}
}

func TestCNPGBootstrapManifests(t *testing.T) {
	if _, err := cnpgFS.ReadFile("resources/cnpg/install.yaml"); err != nil {
		t.Fatalf("reading cnpg install.yaml: %v", err)
	}
	db, err := cnpgFS.ReadFile("resources/cnpg/gitea-db.yaml")
	if err != nil {
		t.Fatalf("reading gitea-db.yaml: %v", err)
	}
	dbStr := string(db)
	for _, want := range []string{"kind: Cluster", "name: gitea-db", "instances: 2", "name: gitea-db-credentials"} {
		if !strings.Contains(dbStr, want) {
			t.Errorf("gitea-db.yaml missing %q", want)
		}
	}
}

func TestGatewayCloudManifest(t *testing.T) {
	raw, err := gatewayFS.ReadFile("resources/gateway/gateway-cloud.yaml")
	if err != nil {
		t.Fatalf("reading gateway-cloud.yaml: %v", err)
	}
	rendered, err := files.ApplyTemplate(raw, v1alpha1.BuildCustomizationSpec{Host: "example.com"})
	if err != nil {
		t.Fatalf("rendering gateway-cloud.yaml: %v", err)
	}
	s := string(rendered)
	for _, want := range []string{
		"type: LoadBalancer",
		`hostname: "*.example.com"`,
		"cert-manager.io/cluster-issuer: adhar-selfsigned",
		"name: adhar-gateway",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("gateway-cloud.yaml missing %q", want)
		}
	}
	if strings.Contains(s, "port: 8443") {
		t.Error("gateway-cloud.yaml must not carry the Kind-specific 8443 listener")
	}
}

func TestAppSetFileForProvider(t *testing.T) {
	cases := map[v1alpha1.EnvironmentProvider]string{
		v1alpha1.ProviderKind:   "adhar-appset-local.yaml",
		"":                      "adhar-appset-local.yaml",
		v1alpha1.ProviderAWS:    "adhar-appset-production.yaml",
		v1alpha1.ProviderGKE:    "adhar-appset-production.yaml",
		v1alpha1.ProviderAzure:  "adhar-appset-production.yaml",
		v1alpha1.ProviderDO:     "adhar-appset-production.yaml",
		v1alpha1.ProviderCivo:   "adhar-appset-production.yaml",
		v1alpha1.ProviderCustom: "adhar-appset-production.yaml",
	}
	for provider, want := range cases {
		if got := appSetFileForProvider(provider); got != want {
			t.Errorf("provider %q: got %s, want %s", provider, got, want)
		}
	}
}

// chartLine returns the first helm.sh/chart label value found in the manifest.
func chartLine(s string) string {
	for _, line := range strings.SplitN(s, "\n", 40) {
		if idx := strings.Index(line, "helm.sh/chart:"); idx >= 0 {
			return strings.TrimSpace(line[idx+len("helm.sh/chart:"):])
		}
	}
	return ""
}
