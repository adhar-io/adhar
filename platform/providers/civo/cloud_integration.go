package civo

import (
	"fmt"

	"golang.org/x/crypto/ssh"

	provider "adhar-io/adhar/platform/providers"

	"adhar-io/adhar/globals"
)

// Cloud integration for a self-managed (kubeadm on Civo instances) cluster.
//
// It is deliberately SHORT: a Civo API secret for anything in-cluster that wants
// it, and the node-local StorageClass. No cloud-controller-manager and no Civo
// block-storage CSI — see civoComputeHasExternalCCM in compute.go for why
// claiming an external cloud provider you cannot supply strands every node.
//
// The CCM manifest URL, the CSI kustomize ref and the external-snapshotter CRD
// list used to live here as constants. They are gone rather than kept "for
// reference": nothing applied them any more, yet their presence told both the
// reader and TestProvidersEnableTheExternalCCMOnEveryJoinPath that this provider
// installs a CCM — which is exactly the premise that left a live mum1 cluster
// with a permanently tainted control plane while the test stayed green
// (2026-10-06). `clusterMode: k3s`, Civo's managed service, is the path where a
// CCM, cloud LoadBalancers and block storage all work, and it brings its own.
//
// The URL-rot lesson those pins taught is still worth keeping: both of them
// 404ed on the first live bring-up (the CCM pinned v0.0.24 against upstream
// v0.1.x and moved path; the CSI pinned v0.2.0 against v0.10.x), which fails a
// create AFTER the instances exist and bill. Any manifest URL added back here
// must be verified by FETCHING it, and is covered by
// TestPinnedUpstreamRefsStillResolve.
const civoAPIURL = "https://api.civo.com"

func (p *Provider) cloudIntegrationSteps(clusterName, clusterID string) []provider.IntegrationStep {
	return append([]provider.IntegrationStep{
		provider.StepEnsureGit(),
		provider.StepSecret("Civo API access for CCM/CSI", "kube-system", "civo-api-access", map[string]string{
			// p.token, NOT p.config.Token: see the Provider.token comment — the
			// latter is empty whenever the token came from the environment or a file.
			"api-key":    p.token,
			"api-url":    civoAPIURL,
			"region":     p.config.Region,
			"cluster-id": clusterID,
			"namespace":  "kube-system",
		}),
		// NO cloud-controller-manager, and no Civo block-storage CSI, in compute
		// mode. Both resolve CIVO_CLUSTER_ID against Civo's MANAGED Kubernetes API,
		// and a self-managed kubeadm cluster on plain instances has no such object:
		// the CCM nil-panics on "Unable to get kubernetes cluster" and the CSI
		// plugin never opens its socket (verified live on mum1, 2026-10-02).
		//
		// Installing them anyway was actively harmful rather than merely useless —
		// see civoComputeHasExternalCCM in compute.go for why an absent CCM leaves
		// every node permanently unschedulable.
		//
		// The platform does not need either: node-local storage is already the
		// default StorageClass on every self-managed cloud, and the Gateway is
		// reachable on its node ports. `clusterMode: k3s` is the path where Civo's
		// CCM and cloud LoadBalancers work.
	}, provider.StepNodeLocalStorageClass(globals.DefaultStorageClass)...)
}

func (p *Provider) installCloudIntegration(signer ssh.Signer, masterIP, clusterName, clusterID string) error {
	if err := provider.ApplyCloudIntegration(signer, computeSSHUser, masterIP, p.cloudIntegrationSteps(clusterName, clusterID)); err != nil {
		return fmt.Errorf("Civo cloud integration: %w", err)
	}
	return nil
}
