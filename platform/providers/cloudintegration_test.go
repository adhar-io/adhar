package provider

import (
	"strings"
	"testing"
)

func TestShellQuoteSurvivesCredentialsWithQuotes(t *testing.T) {
	got := ShellQuote(`p'a$s"w`)
	if got != `'p'\''a$s"w'` {
		t.Fatalf("got %s", got)
	}
}

func TestStepSecretIsCreateOnceAndQuoted(t *testing.T) {
	st := StepSecret("token", "kube-system", "cloud-token", map[string]string{"api-key": "ab'c", "region": "lon1"})
	for _, want := range []string{"get secret cloud-token", "create secret generic cloud-token", `--from-literal='api-key=ab'\''c'`, "--from-literal='region=lon1'", "create namespace kube-system"} {
		if !strings.Contains(st.Cmd, want) {
			t.Errorf("secret step lacks %q:\n%s", want, st.Cmd)
		}
	}
}

func TestStepHelmInstallIsIdempotentAndPinned(t *testing.T) {
	st := StepHelmInstall("ccm", "aws-ccm", "aws-cloud-controller-manager", "https://kubernetes.github.io/cloud-provider-aws", "aws-cloud-controller-manager", "0.0.9", "kube-system", map[string]string{"b": "2", "a": "1"})
	for _, want := range []string{"helm repo add", "helm upgrade --install", "--version '0.0.9'", "--set 'a=1' --set 'b=2'", "--kubeconfig /etc/kubernetes/admin.conf"} {
		if !strings.Contains(st.Cmd, want) {
			t.Errorf("helm step lacks %q:\n%s", want, st.Cmd)
		}
	}
}

func TestDefaultStorageClassManifest(t *testing.T) {
	st := StepDefaultStorageClass("managed-csi", "disk.csi.azure.com", map[string]string{"skuName": "StandardSSD_LRS"})
	for _, want := range []string{`is-default-class: "true"`, "provisioner: disk.csi.azure.com", `skuName: "StandardSSD_LRS"`, "WaitForFirstConsumer", "ADHAR_MANIFEST"} {
		if !strings.Contains(st.Cmd, want) {
			t.Errorf("storage class step lacks %q:\n%s", want, st.Cmd)
		}
	}
	plain := StepDefaultStorageClass("local-path", "rancher.io/local-path", nil)
	if strings.Contains(plain.Cmd, "parameters:") {
		t.Errorf("a parameterless class must not emit an empty parameters map:\n%s", plain.Cmd)
	}
}

func TestTolerateStartupTaintIsIdempotent(t *testing.T) {
	st := StepTolerateCSIStartupTaint("kube-system", "ebs-csi-node")
	if !strings.Contains(st.Cmd, "grep -q node.adhar.io/csi-not-ready ||") || !strings.Contains(st.Cmd, `"operator":"Exists"`) {
		t.Fatalf("unexpected taint step:\n%s", st.Cmd)
	}
}
