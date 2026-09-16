package provider

import (
	"strings"
	"testing"
)

func TestClusterModeIsManagedDefaultsToKubeadm(t *testing.T) {
	for _, mode := range []string{"", "compute", "droplets", "self-managed", "instances"} {
		if ClusterModeIsManaged(mode, "eks") {
			t.Errorf("mode %q must be the default kubeadm mode", mode)
		}
	}
	for _, mode := range []string{"managed", "EKS", "eks"} {
		if !ClusterModeIsManaged(mode, "eks") {
			t.Errorf("mode %q must select the managed service", mode)
		}
	}
	if ClusterModeIsManaged("aks", "eks") {
		t.Error("another cloud's alias must not select managed mode")
	}
}

func TestManagedVersionNormalisesToMajorMinor(t *testing.T) {
	cases := map[string]string{"v1.37.0": "1.37", "1.37": "1.37", "": "", " v1.33.2 ": "1.33", "latest": "latest"}
	for in, want := range cases {
		if got := ManagedVersion(in); got != want {
			t.Errorf("ManagedVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExecKubeconfigRendersExecPluginUser(t *testing.T) {
	kc := ExecKubeconfig("dev", "https://1.2.3.4", "Q0E=", "aws", []string{"eks", "get-token", "--cluster-name", "dev"}, map[string]string{"AWS_PROFILE": "adhar"}, false)
	for _, want := range []string{
		"server: https://1.2.3.4", "certificate-authority-data: Q0E=", "current-context: dev",
		"apiVersion: client.authentication.k8s.io/v1beta1", "command: aws", `- "get-token"`, `- "--cluster-name"`,
		"interactiveMode: Never", "provideClusterInfo: false", "name: AWS_PROFILE", `value: "adhar"`,
	} {
		if !strings.Contains(kc, want) {
			t.Errorf("kubeconfig lacks %q:\n%s", want, kc)
		}
	}
	gke := ExecKubeconfig("dev", "https://1.2.3.4", "Q0E=", "gke-gcloud-auth-plugin", nil, nil, true)
	if !strings.Contains(gke, "provideClusterInfo: true") || strings.Contains(gke, "args:") {
		t.Errorf("GKE kubeconfig must provide cluster info and carry no args:\n%s", gke)
	}
}
