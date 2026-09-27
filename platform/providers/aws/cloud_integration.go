package aws

import (
	"context"
	"fmt"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"log"

	"golang.org/x/crypto/ssh"

	provider "adhar-io/adhar/platform/providers"

	"adhar-io/adhar/globals"
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
		// The CCM MUST be on the host network. It self-identifies by reading the
		// EC2 instance identity document from IMDS at 169.254.169.254, and from a
		// pod on the Cilium overlay that address is unreachable — measured on a
		// live cluster: a host-network pod retrieved the document, an identical
		// pod-network pod timed out, and the CCM died with
		// "could not init cloud provider \"aws\": EC2MetadataRequestError".
		// Raising the instances' IMDS hop limit does not help (it was already 2);
		// the overlay is the problem, not the hop count.
		//
		// Nothing in the cluster schedules until the CCM runs, because every node
		// keeps node.cloudprovider.kubernetes.io/uninitialized:NoSchedule — so this
		// one value is the difference between a working cluster and 16 Pending pods.
		// (dnsPolicy stays at the chart's own default of "Default": with
		// ClusterFirstWithHostNet the CCM tries to resolve ec2.<region>.amazonaws.com
		// through a cluster DNS that cannot exist yet.)
		"hostNetworking": "true",
	}
	csiSets := map[string]string{
		"controller.replicaCount": "1",
		// The default class is created below with the platform's own name.
		"storageClasses[0].name": "gp3",
	}
	// In-cluster components cannot use the operator's credential chain: the CCM
	// and the EBS CSI driver read a Kubernetes Secret, and these nodes have no
	// IAM instance profile to fall back on. So when the static keys are absent —
	// which is the normal case for anyone who configured the AWS CLI rather than
	// exporting AWS_ACCESS_KEY_ID — resolve the chain here and publish what it
	// yields. Without this the `aws-secret` below was silently skipped and both
	// components were left with no credentials at all.
	keyID, secretKey := p.config.AccessKeyID, p.config.SecretAccessKey
	if !p.config.UseInstanceProfile && (keyID == "" || secretKey == "") {
		if id, sec, token, err := resolveStaticCredentials(context.Background()); err == nil {
			keyID, secretKey = id, sec
			if token != "" {
				log.Printf("WARNING: the AWS credentials given to the in-cluster CCM/CSI are TEMPORARY; " +
					"they will expire and both will stop working until re-run")
			}
		} else {
			log.Printf("WARNING: no AWS credentials for the in-cluster CCM/CSI (%v) — the cloud-controller-manager "+
				"will not initialise nodes, and every node will keep its uninitialized taint so nothing can schedule", err)
		}
	}
	if !p.config.UseInstanceProfile && keyID != "" && secretKey != "" {
		steps = append(steps, provider.StepSecret("AWS credential for CCM/CSI", "kube-system", "aws-secret", map[string]string{
			"key_id":     keyID,
			"access_key": secretKey,
		}))
		// The chart's value is `env`, NOT `extraEnv`. It was `extraEnv` here, and
		// because this chart ships no values.schema.json Helm accepted the unknown
		// key in silence — so the credentials were never injected and the CCM died
		// with "NoCredentialProviders: no valid providers in chain" even though
		// kube-system/aws-secret existed and looked correct. (The EBS CSI chart
		// DOES have a schema, which is why a stray value there fails loudly
		// instead.)
		ccmSets["env[0].name"] = "AWS_ACCESS_KEY_ID"
		ccmSets["env[0].valueFrom.secretKeyRef.name"] = "aws-secret"
		ccmSets["env[0].valueFrom.secretKeyRef.key"] = "key_id"
		ccmSets["env[1].name"] = "AWS_SECRET_ACCESS_KEY"
		ccmSets["env[1].valueFrom.secretKeyRef.name"] = "aws-secret"
		ccmSets["env[1].valueFrom.secretKeyRef.key"] = "access_key"
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
		// Not the default: EBS attachments are capped per instance (~26 on
		// nitro, fewer once ENIs are counted) and the stack asks for ~90
		// volumes. Node-local storage is the default; see
		// provider.StepNodeLocalStorageClass.
		provider.StepStorageClass("adhar-block", "ebs.csi.aws.com", map[string]string{"type": "gp3", "encrypted": "true"}, false),
		provider.StepClearDefaultStorageClass("adhar-block"),
	)
	steps = append(steps, provider.StepNodeLocalStorageClass(globals.DefaultStorageClass)...)
	return steps
}

// installCloudIntegration applies the steps through the control plane.
func (p *Provider) installCloudIntegration(signer ssh.Signer, masterIP, clusterName string) error {
	if err := provider.ApplyCloudIntegration(signer, awsSSHUser, masterIP, p.cloudIntegrationSteps(clusterName)); err != nil {
		return fmt.Errorf("AWS cloud integration: %w", err)
	}
	return nil
}

// resolveStaticCredentials pulls a concrete key pair out of the AWS default
// credential chain, so the platform can hand one to components that run INSIDE
// the cluster.
//
// The CCM and the EBS CSI driver read credentials from a Kubernetes Secret and
// cannot resolve a profile, an SSO cache or an instance role themselves — and on
// a kubeadm cluster built by this provider the instances carry no IAM instance
// profile either. Resolving the chain here is what lets an operator who simply
// ran `aws configure` get a working cluster, instead of one where every node
// stays tainted `node.cloudprovider.kubernetes.io/uninitialized` because the CCM
// never authenticated.
func resolveStaticCredentials(ctx context.Context) (id, secret, sessionToken string, err error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return "", "", "", fmt.Errorf("loading AWS configuration: %w", err)
	}
	if cfg.Credentials == nil {
		return "", "", "", fmt.Errorf("no credential provider was configured")
	}
	creds, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return "", "", "", fmt.Errorf("retrieving credentials: %w", err)
	}
	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
		return "", "", "", fmt.Errorf("the credential chain returned an empty key pair")
	}
	return creds.AccessKeyID, creds.SecretAccessKey, creds.SessionToken, nil
}
