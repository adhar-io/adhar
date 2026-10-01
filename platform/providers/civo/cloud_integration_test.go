package civo

import (
	"strings"
	"testing"
)

// Civo COMPUTE mode installs neither the Civo cloud-controller-manager nor the
// Civo block-storage CSI, and that is deliberate.
//
// Both resolve CIVO_CLUSTER_ID against Civo's MANAGED Kubernetes API, and a
// self-managed kubeadm cluster on plain instances has no such object. Verified
// live on mum1 (2026-10-02): the CCM logs "Unable to get kubernetes cluster" and
// nil-panics in the service controller; the CSI plugin never opens its socket.
//
// Installing them was worse than pointless. `--cloud-provider=external` makes
// kubelet add node.cloudprovider.kubernetes.io/uninitialized:NoSchedule, which
// only a CCM removes — so a CCM that cannot start leaves EVERY node permanently
// unschedulable and the platform never comes up at all.
func TestComputeModeInstallsNoCivoCCMOrCSI(t *testing.T) {
	p := &Provider{config: &Config{Token: "tok", Region: "mum1"}, token: "tok"}
	var joined string
	for _, s := range p.cloudIntegrationSteps("dev", "adhar-cluster-dev") {
		joined += s.Cmd + "\n"
	}

	// Still needed: the API secret (other tooling reads it) and node-local storage.
	for _, want := range []string{
		"create secret generic civo-api-access", "--from-literal='api-key=tok'",
		"--from-literal='region=mum1'", "--from-literal='cluster-id=adhar-cluster-dev'",
		"provisioner: rancher.io/local-path",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("Civo integration lacks %q", want)
		}
	}

	// Deliberately absent — each of these broke the cluster.
	for _, unwanted := range []string{
		"civo-cloud-controller-manager",
		"civo-csi",
		"external-snapshotter",
	} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("compute mode must not install %q: it needs a Civo MANAGED cluster that does not exist here", unwanted)
		}
	}
}

// An absent CCM and a node told to expect one is the combination that bricks the
// cluster, so the flag must stay false for compute mode.
func TestComputeModeDoesNotClaimAnExternalCloudProvider(t *testing.T) {
	if civoComputeHasExternalCCM {
		t.Error("compute mode has no working CCM, so kubelet must not get --cloud-provider=external: " +
			"the uninitialized taint it adds would never be removed and every node would stay unschedulable")
	}
}

// The CCM/CSI Secret must carry a real token however the token was supplied.
//
// This is the bug that broke the first live Civo cluster. The integration read
// config.Token, which is only set when the token is written INLINE in the config
// file — and every shipped example uses `useEnvironment: true` instead, precisely
// to keep a credential out of a committed file. So the Secret got an empty
// api-key, the CCM crash-looped on "CIVO_API_KEY ... must be set", the
// uninitialized taint was never lifted, and the Gateway's LoadBalancer never got
// an address: a cluster with no reachable URL, from one empty string.
func TestCCMSecretCarriesTheTokenHoweverItWasSupplied(t *testing.T) {
	t.Setenv("CIVO_TOKEN", "from-environment")

	for _, tc := range []struct {
		name   string
		config *Config
		want   string
	}{
		{"inline token", &Config{Token: "inline-token", Region: "mum1"}, "inline-token"},
		{"useEnvironment", &Config{UseEnvironment: true, Region: "mum1"}, "from-environment"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewProvider(tc.config)
			if err != nil {
				t.Fatalf("NewProvider: %v", err)
			}
			var joined string
			for _, s := range p.cloudIntegrationSteps("dev", "adhar-cluster-dev") {
				joined += s.Cmd + "\n"
			}
			if !strings.Contains(joined, "api-key="+tc.want) {
				t.Errorf("the civo-api-access Secret does not carry the %s token; the CCM will crash-loop on an empty CIVO_API_KEY", tc.name)
			}
			if strings.Contains(joined, "api-key='") && strings.Contains(joined, "api-key=''") {
				t.Error("the api-key is empty")
			}
		})
	}
}
