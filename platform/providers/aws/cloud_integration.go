package aws

import (
	"fmt"

	"golang.org/x/crypto/ssh"

	provider "adhar-io/adhar/platform/providers"
)

// Cloud integration for a self-managed (kubeadm on EC2) cluster: the AWS
// cloud-controller-manager, the EBS CSI driver, a default gp3 StorageClass and
// the CSI-startup-taint toleration — the DigitalOcean provider's integration,
// spelled for AWS. Versions are pinned; bump them deliberately and re-run the
// AWS bring-up, exactly as the DigitalOcean CCM/CSI pins are handled.
const (
	awsCCMChartVersion    = "0.0.9"  // chart aws-cloud-controller-manager (kubernetes/cloud-provider-aws)
	awsEBSCSIChartVersion = "2.40.0" // chart aws-ebs-csi-driver (kubernetes-sigs/aws-ebs-csi-driver)
)

// cloudIntegrationSteps lists the idempotent steps run on the control plane
// after `kubeadm init` and the first joins. Credentials: with an instance
// profile (the recommended setup — `useInstanceProfile: true` in the provider
// config) nothing is written to the cluster; otherwise the static key pair
// from the provider config is stored once as kube-system/aws-secret, which
// both the CCM and the CSI driver read.
func (p *Provider) cloudIntegrationSteps(clusterName string) []provider.IntegrationStep {
	steps := []provider.IntegrationStep{provider.StepEnsureHelm()}
	ccmSets := map[string]string{
		"args[0]": "--v=2",
		"args[1]": "--cloud-provider=aws",
		// Cilium owns pod routing (ADR: eBPF datapath); the CCM must not
		// programme VPC route tables for it.
		"args[2]": "--configure-cloud-routes=false",
		"args[3]": "--cluster-name=" + clusterName,
	}
	csiSets := map[string]string{
		"controller.replicaCount": "1",
		// The default class is created below with the platform's own name.
		"storageClasses[0].name": "gp3",
	}
	if !p.config.UseInstanceProfile && p.config.AccessKeyID != "" && p.config.SecretAccessKey != "" {
		steps = append(steps, provider.StepSecret("AWS credential for CCM/CSI", "kube-system", "aws-secret", map[string]string{
			"key_id":     p.config.AccessKeyID,
			"access_key": p.config.SecretAccessKey,
		}))
		ccmSets["extraEnv[0].name"] = "AWS_ACCESS_KEY_ID"
		ccmSets["extraEnv[0].valueFrom.secretKeyRef.name"] = "aws-secret"
		ccmSets["extraEnv[0].valueFrom.secretKeyRef.key"] = "key_id"
		ccmSets["extraEnv[1].name"] = "AWS_SECRET_ACCESS_KEY"
		ccmSets["extraEnv[1].valueFrom.secretKeyRef.name"] = "aws-secret"
		ccmSets["extraEnv[1].valueFrom.secretKeyRef.key"] = "access_key"
		csiSets["awsAccessSecret.name"] = "aws-secret"
		csiSets["awsAccessSecret.keyId"] = "key_id"
		csiSets["awsAccessSecret.accessKey"] = "access_key"
	}
	steps = append(steps,
		provider.StepHelmInstall("AWS cloud-controller-manager", "aws-cloud-controller-manager", "aws-cloud-controller-manager",
			"https://kubernetes.github.io/cloud-provider-aws", "aws-cloud-controller-manager", awsCCMChartVersion, "kube-system", ccmSets),
		provider.StepHelmInstall("EBS CSI driver", "aws-ebs-csi-driver", "aws-ebs-csi-driver",
			"https://kubernetes-sigs.github.io/aws-ebs-csi-driver", "aws-ebs-csi-driver", awsEBSCSIChartVersion, "kube-system", csiSets),
		provider.StepWaitDaemonSet("kube-system", "ebs-csi-node"),
		provider.StepTolerateCSIStartupTaint("kube-system", "ebs-csi-node"),
		provider.StepDefaultStorageClass("adhar-block", "ebs.csi.aws.com", map[string]string{"type": "gp3", "encrypted": "true"}),
	)
	return steps
}

// installCloudIntegration applies the steps through the control plane.
func (p *Provider) installCloudIntegration(signer ssh.Signer, masterIP, clusterName string) error {
	if err := provider.ApplyCloudIntegration(signer, awsSSHUser, masterIP, p.cloudIntegrationSteps(clusterName)); err != nil {
		return fmt.Errorf("AWS cloud integration: %w", err)
	}
	return nil
}
