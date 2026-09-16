package aws

import (
	"strings"
	"testing"
)

func TestCloudIntegrationStepsInstallCCMAndEBSCSIWithStaticCreds(t *testing.T) {
	p := &Provider{config: &Config{Region: "us-east-1", AccessKeyID: "AKIA", SecretAccessKey: "secret"}}
	steps := p.cloudIntegrationSteps("dev")
	joined := ""
	for _, s := range steps {
		joined += s.Desc + "\n" + s.Cmd + "\n"
	}
	for _, want := range []string{
		"helm repo add 'aws-cloud-controller-manager' 'https://kubernetes.github.io/cloud-provider-aws'",
		"--version '" + awsCCMChartVersion + "'",
		"helm repo add 'aws-ebs-csi-driver' 'https://kubernetes-sigs.github.io/aws-ebs-csi-driver'",
		"--version '" + awsEBSCSIChartVersion + "'",
		"--configure-cloud-routes=false", "--cluster-name=dev",
		"create secret generic aws-secret", "--from-literal='key_id=AKIA'", "--from-literal='access_key=secret'",
		"provisioner: ebs.csi.aws.com", "is-default-class",
		"get daemonset ebs-csi-node", "node.adhar.io/csi-not-ready",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("AWS integration lacks %q", want)
		}
	}
}

func TestCloudIntegrationStepsSkipSecretWithInstanceProfile(t *testing.T) {
	p := &Provider{config: &Config{Region: "us-east-1", UseInstanceProfile: true}}
	for _, s := range p.cloudIntegrationSteps("dev") {
		if strings.Contains(s.Cmd, "aws-secret") {
			t.Fatalf("instance-profile clusters must not write a static credential: %s", s.Desc)
		}
	}
}

func TestClusterModeDefaultsToComputeAndOptsIntoEKS(t *testing.T) {
	if (&Provider{config: &Config{}}).isManagedMode() {
		t.Error("empty clusterMode must be kubeadm on EC2")
	}
	for _, mode := range []string{"eks", "managed", "EKS"} {
		if !(&Provider{config: &Config{ClusterMode: mode}}).isManagedMode() {
			t.Errorf("clusterMode %q must select EKS", mode)
		}
	}
	clusterRole, nodeRole := eksRoleNames("dev")
	if clusterRole != "adhar-dev-eks-cluster" || nodeRole != "adhar-dev-eks-node" {
		t.Errorf("unexpected role names %s / %s", clusterRole, nodeRole)
	}
}
