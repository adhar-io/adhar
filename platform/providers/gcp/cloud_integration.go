package gcp

import (
	"fmt"
	"os"

	"golang.org/x/crypto/ssh"

	provider "adhar-io/adhar/platform/providers"
)

// Cloud integration for a self-managed (kubeadm on Compute Engine) cluster:
// cloud-provider-gcp's controller manager, the Persistent Disk CSI driver, a
// default pd-balanced StorageClass and the CSI-startup-taint toleration.
// Neither ships as a Helm chart, so these are pinned manifests / remote
// kustomizations (git is installed on the control plane for the latter).
const (
	gcpCCMManifestURL = "https://raw.githubusercontent.com/kubernetes/cloud-provider-gcp/master/deploy/packages/default/manifest.yaml"
	gcpPDCSIRef       = "github.com/kubernetes-sigs/gcp-compute-persistent-disk-csi-driver/deploy/kubernetes/overlays/stable-master?ref=v1.15.0"
)

// serviceAccountKeyJSON returns the service-account key the PD CSI driver
// authenticates with (kube-system/cloud-sa), from the provider config.
func (p *Provider) serviceAccountKeyJSON() (string, error) {
	if p.config.ServiceAccountKey != "" {
		return p.config.ServiceAccountKey, nil
	}
	if p.config.ServiceAccountKeyPath != "" {
		b, err := os.ReadFile(p.config.ServiceAccountKeyPath)
		if err != nil {
			return "", fmt.Errorf("reading service account key %s: %w", p.config.ServiceAccountKeyPath, err)
		}
		return string(b), nil
	}
	return "", nil
}

func (p *Provider) cloudIntegrationSteps() ([]provider.IntegrationStep, error) {
	steps := []provider.IntegrationStep{
		provider.StepEnsureGit(),
		provider.StepApplyURL("GCP cloud-controller-manager", gcpCCMManifestURL),
	}
	// The PD CSI controller needs a key; with application-default credentials
	// (the instances' own service account) the secret is left out and the
	// driver uses the metadata server.
	if key, err := p.serviceAccountKeyJSON(); err != nil {
		return nil, err
	} else if key != "" {
		steps = append(steps, provider.StepSecret("GCP service-account key for the PD CSI driver", "gce-pd-csi-driver", "cloud-sa", map[string]string{"cloud-sa.json": key}))
	}
	steps = append(steps,
		provider.StepApplyKustomize("Persistent Disk CSI driver", gcpPDCSIRef),
		provider.StepWaitDaemonSet("gce-pd-csi-driver", "csi-gce-pd-node"),
		provider.StepTolerateCSIStartupTaint("gce-pd-csi-driver", "csi-gce-pd-node"),
		provider.StepDefaultStorageClass("adhar-block", "pd.csi.storage.gke.io", map[string]string{"type": "pd-balanced"}),
	)
	return steps, nil
}

func (p *Provider) installCloudIntegration(signer ssh.Signer, masterIP string) error {
	steps, err := p.cloudIntegrationSteps()
	if err != nil {
		return fmt.Errorf("GCP cloud integration: %w", err)
	}
	if err := provider.ApplyCloudIntegration(signer, gcpSSHUser, masterIP, steps); err != nil {
		return fmt.Errorf("GCP cloud integration: %w", err)
	}
	return nil
}
