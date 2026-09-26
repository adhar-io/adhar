package civo

import (
	"fmt"

	"golang.org/x/crypto/ssh"

	provider "adhar-io/adhar/platform/providers"

	"adhar-io/adhar/globals"
)

// Cloud integration for a self-managed (kubeadm on Civo instances) cluster:
// the Civo cloud-controller-manager, the Civo CSI driver (its manifest ships
// the `civo-volume` StorageClass), the default-class mark and the
// CSI-startup-taint toleration. Both read the API key from
// kube-system/civo-api-access. Pinned; bump with a bring-up.
const (
	civoCCMManifestURL = "https://raw.githubusercontent.com/civo/civo-cloud-controller-manager/v0.0.24/manifest/cloud-controller-manager.yaml"
	civoCSIRef         = "github.com/civo/civo-csi/deploy/kubernetes?ref=v0.2.0"
	civoAPIURL         = "https://api.civo.com"
)

func (p *Provider) cloudIntegrationSteps(clusterName, clusterID string) []provider.IntegrationStep {
	return append([]provider.IntegrationStep{
		provider.StepEnsureGit(),
		provider.StepSecret("Civo API access for CCM/CSI", "kube-system", "civo-api-access", map[string]string{
			"api-key":    p.config.Token,
			"api-url":    civoAPIURL,
			"region":     p.config.Region,
			"cluster-id": clusterID,
			"namespace":  "kube-system",
		}),
		provider.StepApplyURL("Civo cloud-controller-manager", civoCCMManifestURL),
		provider.StepApplyKustomize("Civo CSI driver", civoCSIRef),
		provider.StepWaitDaemonSet("kube-system", "civo-csi-node"),
		provider.StepTolerateCSIStartupTaint("kube-system", "civo-csi-node"),
		provider.StepClearDefaultStorageClass("civo-volume"),
	}, provider.StepNodeLocalStorageClass(globals.DefaultStorageClass)...)
}

func (p *Provider) installCloudIntegration(signer ssh.Signer, masterIP, clusterName, clusterID string) error {
	if err := provider.ApplyCloudIntegration(signer, computeSSHUser, masterIP, p.cloudIntegrationSteps(clusterName, clusterID)); err != nil {
		return fmt.Errorf("Civo cloud integration: %w", err)
	}
	return nil
}
