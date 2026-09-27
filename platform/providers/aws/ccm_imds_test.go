package aws

import (
	"strings"
	"testing"

	"adhar-io/adhar/platform/providers"
)

// The AWS cloud-controller-manager must run on the HOST network.
//
// It self-identifies by reading the EC2 instance identity document from IMDS at
// 169.254.169.254. Measured on a live Singapore cluster: a host-network pod
// retrieved that document, an identical pod on the Cilium overlay timed out, and
// the CCM died with
//
//	could not init cloud provider "aws": EC2MetadataRequestError: failed to get
//	EC2 instance identity document ... status code: 401
//
// Raising the instances' IMDS hop limit is NOT the fix — it was already 2, which
// is what AWS documents for pods. The overlay is the problem.
//
// This matters more than it looks: until the CCM runs, every node keeps
// node.cloudprovider.kubernetes.io/uninitialized:NoSchedule, so NOTHING
// schedules — Argo CD and Gitea sat Pending, and `adhar get secrets` had no
// argocd-initial-admin-secret to show because Argo CD had never started.
func TestCCMRunsOnTheHostNetwork(t *testing.T) {
	p := &Provider{config: &Config{Region: "ap-southeast-1"}}

	// Assert it is set on the CCM's OWN helm install, not merely somewhere in the
	// step list. Putting it on the EBS CSI chart instead is a real mistake that a
	// substring search over every command cannot see — and that chart has a values
	// schema, so it fails the whole bring-up with
	// "Additional property hostNetworking is not allowed".
	var ccmCmd, csiCmd string
	for _, step := range p.cloudIntegrationSteps("dev") {
		switch {
		case strings.Contains(step.Desc, "cloud-controller-manager"):
			ccmCmd = step.Cmd
		case strings.Contains(step.Desc, "EBS CSI"):
			csiCmd = step.Cmd
		}
	}
	if ccmCmd == "" {
		t.Fatal("no cloud-controller-manager install step was produced")
	}
	if !strings.Contains(ccmCmd, "hostNetworking=true") {
		t.Error("the CCM chart must be installed with hostNetworking=true, or it cannot reach IMDS from the pod network")
	}
	if csiCmd != "" && strings.Contains(csiCmd, "hostNetworking") {
		t.Error("hostNetworking belongs to the CCM chart; the EBS CSI chart's schema rejects it and the bring-up fails")
	}
	// The chart's own default dnsPolicy ("Default") must stand: with
	// ClusterFirstWithHostNet the CCM resolves ec2.<region>.amazonaws.com through a
	// cluster DNS that does not exist yet, and dies with "no route to host".
	if strings.Contains(ccmCmd, "ClusterFirstWithHostNet") {
		t.Error("the CCM must not use cluster DNS; it runs before CoreDNS can exist")
	}
}

// Components that run inside the cluster cannot use the operator's credential
// chain, and this provider's nodes have no IAM instance profile. When the static
// keys are absent — the normal case for anyone who ran `aws configure` rather
// than exporting AWS_ACCESS_KEY_ID — the chain must be resolved and published,
// otherwise the aws-secret is silently skipped and the CCM has no credentials.
func TestCCMGetsCredentialsWhenOnlyTheChainHasThem(t *testing.T) {
	// Static keys present: the secret is created from them.
	withStatic := &Provider{config: &Config{
		Region: "ap-southeast-1", AccessKeyID: "AKIASTATIC", SecretAccessKey: "staticsecret",
	}}
	joined := ""
	for _, s := range withStatic.cloudIntegrationSteps("dev") {
		joined += s.Cmd + "\n"
	}
	if !strings.Contains(joined, "aws-secret") {
		t.Error("static credentials must still produce the aws-secret")
	}
	if !strings.Contains(joined, "AKIASTATIC") {
		t.Error("the configured key id should be the one published")
	}
	// The chart's value is `env`, not `extraEnv`. It was `extraEnv`, and because
	// this chart ships no values.schema.json Helm accepted the unknown key
	// silently — so the CCM got no credentials and died with
	// "NoCredentialProviders: no valid providers in chain" while
	// kube-system/aws-secret sat there looking correct.
	if strings.Contains(joined, "extraEnv") {
		t.Error("the CCM chart has no extraEnv value; unknown keys are ignored in silence, so use env[N]")
	}
	for _, want := range []string{"env[0].name=AWS_ACCESS_KEY_ID", "env[1].name=AWS_SECRET_ACCESS_KEY"} {
		if !strings.Contains(joined, want) {
			t.Errorf("credentials must be injected via the chart's env value; missing %q", want)
		}
	}

	// An instance profile means the components use it; no Secret should be made.
	withProfile := &Provider{config: &Config{Region: "ap-southeast-1", UseInstanceProfile: true}}
	joined = ""
	for _, s := range withProfile.cloudIntegrationSteps("dev") {
		joined += s.Cmd + "\n"
	}
	if strings.Contains(joined, "create secret generic aws-secret") {
		t.Error("with an instance profile the platform must not publish static keys")
	}
}

// Guard the ordering that makes the CCM install meaningful: the StorageClass the
// platform creates must not be the default, because node-local storage is.
func TestAWSBlockClassIsNotDefault(t *testing.T) {
	p := &Provider{config: &Config{Region: "ap-southeast-1"}}
	joined := ""
	for _, s := range p.cloudIntegrationSteps("dev") {
		joined += s.Cmd + "\n"
	}
	if !strings.Contains(joined, "provisioner: rancher.io/local-path") {
		t.Error("node-local provisioner must be installed")
	}
	if !strings.Contains(joined, `storageclass.kubernetes.io/is-default-class: "false"`) {
		t.Error("adhar-block must be created non-default")
	}
	_ = provider.LocalPathDataDir
}
