package custom

import (
	"fmt"

	"golang.org/x/crypto/ssh"

	provider "adhar-io/adhar/platform/providers"
)

// Cloud integration for bring-your-own hosts: there is no cloud controller
// manager or cloud block-storage CSI, so the platform gets a default
// StorageClass from the local-path provisioner (node-local hostPath volumes).
// Pinned; bump deliberately.
const localPathProvisionerURL = "https://raw.githubusercontent.com/rancher/local-path-provisioner/v0.0.31/deploy/local-path-storage.yaml"

func (p *Provider) cloudIntegrationSteps() []provider.IntegrationStep {
	return []provider.IntegrationStep{
		provider.StepApplyURL("local-path provisioner", localPathProvisionerURL),
		provider.StepMarkDefaultStorageClass("local-path"),
	}
}

func (p *Provider) installCloudIntegration(signer ssh.Signer, masterIP string) error {
	if err := provider.ApplyCloudIntegration(signer, p.config.SSHUser, masterIP, p.cloudIntegrationSteps()); err != nil {
		return fmt.Errorf("on-prem storage integration: %w", err)
	}
	return nil
}
