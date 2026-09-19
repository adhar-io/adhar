package gcp

import (
	"strings"
	"testing"
)

func TestCloudIntegrationStepsInstallCCMAndPDCSI(t *testing.T) {
	p := &Provider{config: &Config{ProjectID: "proj", Zone: "us-central1-a", ServiceAccountKey: `{"type":"service_account"}`}}
	steps, err := p.cloudIntegrationSteps("production")
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, s := range steps {
		joined += s.Cmd + "\n"
	}
	for _, want := range []string{
		"apt-get install -y -qq git",
		// NOT a plain `apply -f <url>`: the upstream manifest ships empty args
		// and must be rewritten before it is applied (see the test below).
		"curl -fsSL '" + gcpCCMManifestURL + "'", "--cloud-provider=gce",
		"apply -k '" + gcpPDCSIRef + "'",
		"create secret generic cloud-sa", "--from-literal='cloud-sa.json={",
		"get daemonset csi-gce-pd-node", "node.adhar.io/csi-not-ready",
		"provisioner: pd.csi.storage.gke.io", `type: "pd-balanced"`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("GCP integration lacks %q", want)
		}
	}
	noKey := &Provider{config: &Config{ProjectID: "proj", UseApplicationDefault: true}}
	steps, _ = noKey.cloudIntegrationSteps("production")
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

// The upstream cloud-provider-gcp manifest ships `args: []` with a comment
// saying args must be replaced by tooling. Applying it raw started the
// cloud-controller-manager with no flags, it crash-looped with
// `"cloud-controller-manager" does not take any arguments`, node providerIDs
// were never set, and the Gateway's LoadBalancer Service stayed <pending> so the
// platform was unreachable from the internet (found on the first GCP cluster
// that reached bootstrap, 2026-09-19).
func TestCCMStepReplacesTheEmptyUpstreamArgs(t *testing.T) {
	p := &Provider{config: &Config{ProjectID: "adhar-cloud", Region: "asia-southeast1", Zone: "asia-southeast1-a"}}
	cmd := p.ccmStep().Cmd

	for _, want := range []string{
		"--cloud-provider=gce",
		"--use-service-account-credentials=true",
		// Cilium owns pod IPAM and routing. node-ipam must be DISABLED, not told
		// not to allocate: --allocate-node-cidrs=false makes that controller
		// refuse to start and the whole manager exit.
		"--controllers=*,-node-ipam-controller",
		"--configure-cloud-routes=false",
		"apply -f -",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("the cloud-controller-manager step is missing %q", want)
		}
	}
	if strings.Contains(cmd, `args: []`) {
		t.Error("the empty upstream args must be rewritten, never applied as published")
	}
	// Rewriting the manifest in flight rather than patching afterwards matters:
	// a DaemonSet that runs once with empty args crash-loops and has to be
	// waited out twice.
	if !strings.Contains(cmd, "sed") {
		t.Error("the manifest must be rewritten before it is applied")
	}
	// The manifest hardcodes 127.0.0.1, which on kubeadm is both the wrong port
	// and absent from the apiserver's serving certificate.
	if !strings.Contains(cmd, kubernetesServiceIP) {
		t.Errorf("the API server address must be rewritten to %s", kubernetesServiceIP)
	}
}

// The PersistentVolume disk type must follow the NODE disk type. They draw on
// different GCP quotas: pd-balanced and pd-ssd against SSD_TOTAL_GB (250 GB by
// default), pd-standard against DISKS_TOTAL_GB (2 TB). Hardcoding pd-balanced
// meant a cluster moved to pd-standard to fit the SSD quota still created every
// PersistentVolume as pd-balanced, exhausted 250 GB after about 35 volumes, and
// left every remaining stateful application Pending on "binding volumes: context
// deadline exceeded" — a message that never mentions quota (2026-09-19).
func TestStorageClassDiskTypeFollowsTheNodeDiskType(t *testing.T) {
	cases := map[string]string{
		"pd-standard": "pd-standard",
		"pd-ssd":      "pd-ssd",
		"":            "pd-balanced", // unset keeps the better default
	}
	for nodeType, want := range cases {
		p := &Provider{config: &Config{ProjectID: "proj", Zone: "us-central1-a", DiskType: nodeType}}
		steps, err := p.cloudIntegrationSteps("production")
		if err != nil {
			t.Fatal(err)
		}
		joined := ""
		for _, s := range steps {
			joined += s.Cmd + "\n"
		}
		if !strings.Contains(joined, `type: "`+want+`"`) {
			t.Errorf("node disk type %q should give PersistentVolumes %q", nodeType, want)
		}
	}
}
