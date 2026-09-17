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

func TestStepDefaultStorageClassWithoutParametersOmitsTheBlock(t *testing.T) {
	st := StepDefaultStorageClass("adhar-block", "example.csi", nil)
	if strings.Contains(st.Cmd, "parameters:") {
		t.Errorf("no parameters means no parameters block:\n%s", st.Cmd)
	}
	for _, want := range []string{"name: adhar-block", "provisioner: example.csi", "volumeBindingMode: WaitForFirstConsumer", "allowVolumeExpansion: true"} {
		if !strings.Contains(st.Cmd, want) {
			t.Errorf("StorageClass lacks %q:\n%s", want, st.Cmd)
		}
	}
}

func TestStepSecretQuotesValuesWithSingleQuotes(t *testing.T) {
	st := StepSecret("s", "kube-system", "creds", map[string]string{"pw": "it's"})
	if !strings.Contains(st.Cmd, `--from-literal='pw=it'\''s'`) {
		t.Errorf("single quotes must survive shell quoting:\n%s", st.Cmd)
	}
	if !strings.Contains(st.Cmd, "get namespace kube-system") || !strings.Contains(st.Cmd, "get secret creds") {
		t.Errorf("secret creation must be create-once with the namespace ensured:\n%s", st.Cmd)
	}
}

func TestStepHelmInstallOrdersSetFlagsDeterministically(t *testing.T) {
	a := StepHelmInstall("x", "rel", "repo", "https://example.com/charts", "chart", "1.0.0", "ns", map[string]string{"z": "1", "a": "2", "m": "3"})
	b := StepHelmInstall("x", "rel", "repo", "https://example.com/charts", "chart", "1.0.0", "ns", map[string]string{"m": "3", "z": "1", "a": "2"})
	if a.Cmd != b.Cmd {
		t.Error("the same values must render the same command regardless of map order")
	}
	if strings.Index(a.Cmd, "--set 'a=2'") > strings.Index(a.Cmd, "--set 'm=3'") || strings.Index(a.Cmd, "--set 'm=3'") > strings.Index(a.Cmd, "--set 'z=1'") {
		t.Errorf("set flags must be sorted:\n%s", a.Cmd)
	}
	if !strings.Contains(a.Cmd, "helm upgrade --install 'rel' repo/chart --version '1.0.0' --namespace 'ns' --create-namespace") {
		t.Errorf("unexpected helm invocation:\n%s", a.Cmd)
	}
}

func TestStepApplyKustomizeQuotesTheRef(t *testing.T) {
	st := StepApplyKustomize("csi", "github.com/org/repo/deploy?ref=v1.0.0")
	if !strings.HasSuffix(st.Cmd, " apply -k 'github.com/org/repo/deploy?ref=v1.0.0'") {
		t.Errorf("unexpected: %s", st.Cmd)
	}
	if !strings.Contains(StepEnsureGit().Cmd, "command -v git") || !strings.Contains(StepEnsureHelm().Cmd, "DESIRED_VERSION="+HelmVersion) {
		t.Error("git/helm ensure steps must be no-ops when the tool exists and pin helm otherwise")
	}
}
