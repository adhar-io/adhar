package gcp

import (
	"strings"
	"testing"
)

func TestCloudIntegrationStepsInstallCCMAndPDCSI(t *testing.T) {
	p := &Provider{config: &Config{ProjectID: "proj", Zone: "us-central1-a", ServiceAccountKey: `{"type":"service_account"}`}}
	steps, err := p.cloudIntegrationSteps()
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, s := range steps {
		joined += s.Cmd + "\n"
	}
	for _, want := range []string{
		"apt-get install -y -qq git", "apply -f '" + gcpCCMManifestURL + "'", "apply -k '" + gcpPDCSIRef + "'",
		"create secret generic cloud-sa", "--from-literal='cloud-sa.json={",
		"get daemonset csi-gce-pd-node", "node.adhar.io/csi-not-ready",
		"provisioner: pd.csi.storage.gke.io", `type: "pd-balanced"`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("GCP integration lacks %q", want)
		}
	}
	noKey := &Provider{config: &Config{ProjectID: "proj", UseApplicationDefault: true}}
	steps, _ = noKey.cloudIntegrationSteps()
	for _, s := range steps {
		if strings.Contains(s.Cmd, "cloud-sa") {
			t.Fatal("application-default credentials must not write a service-account secret")
		}
	}
}

func TestClusterModeAndGKENames(t *testing.T) {
	p := &Provider{config: &Config{ProjectID: "proj", Zone: "europe-west1-b", ClusterMode: "gke"}}
	if !p.isManagedMode() || (&Provider{config: &Config{}}).isManagedMode() {
		t.Error("clusterMode must default to kubeadm and opt into GKE with \"gke\"")
	}
	if got := p.gkeClusterName("gcp/proj/dev"); got != "projects/proj/locations/europe-west1-b/clusters/dev" {
		t.Errorf("gkeClusterName = %q", got)
	}
}

func TestExtractClusterNameHandlesProjectScopedIDs(t *testing.T) {
	for in, want := range map[string]string{"gcp/proj/dev": "dev", "gcp-dev": "dev", "dev": "dev"} {
		if got := extractClusterName(in); got != want {
			t.Errorf("extractClusterName(%q) = %q, want %q", in, got, want)
		}
	}
}
