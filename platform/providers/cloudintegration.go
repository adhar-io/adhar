package provider

// Cloud integration for self-managed (kubeadm on raw compute) clusters — the
// cloud-controller-manager, the CSI driver, a default StorageClass and the
// small patches that make them fit the platform — installed through the
// control plane's admin kubeconfig over SSH, exactly as the DigitalOcean
// provider has done since it was live-verified. Each provider contributes a
// list of IntegrationSteps; this file owns how they run.
//
// Every step must be IDEMPOTENT: `adhar up` re-runs against an existing
// cluster, and `adhar upgrade` re-applies integration after a version bump.

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"adhar-io/adhar/globals"
)

// KubectlAdmin is kubectl bound to the control plane's admin kubeconfig.
const KubectlAdmin = "kubectl --kubeconfig /etc/kubernetes/admin.conf"

// HelmVersion is the Helm release installed on a control plane when a cloud's
// integration ships only as a chart (AWS, Azure). Pinned like every other
// upstream artifact the platform pulls.
const HelmVersion = "v3.16.4"

// IntegrationStep is one idempotent shell command run on the control plane.
type IntegrationStep struct {
	Desc string
	Cmd  string
}

// ApplyCloudIntegration runs the steps in order on the control plane and
// fails on the first error, naming the step — so a bring-up log says
// "cloud integration step 'CSI driver' failed", not just "exit status 1".
func ApplyCloudIntegration(signer ssh.Signer, user, masterIP string, steps []IntegrationStep) error {
	for _, st := range steps {
		if out, err := SSHRun(signer, user, masterIP, st.Cmd, 10*time.Minute); err != nil {
			return fmt.Errorf("cloud integration step %q failed: %w (%s)", st.Desc, err, LastLines(out, 8))
		}
	}
	return nil
}

// ShellQuote single-quotes s for a POSIX shell.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// StepApplyURL applies a manifest published at a URL (a pinned release file).
func StepApplyURL(desc, url string) IntegrationStep {
	return IntegrationStep{Desc: desc, Cmd: KubectlAdmin + " apply -f " + ShellQuote(url)}
}

// StepApplyKustomize applies a remote kustomization (`github.com/org/repo/path?ref=vX`).
// kubectl fetches it with git, so StepEnsureGit must run first.
func StepApplyKustomize(desc, ref string) IntegrationStep {
	return IntegrationStep{Desc: desc, Cmd: KubectlAdmin + " apply -k " + ShellQuote(ref)}
}

// StepEnsureGit installs git on the control plane if it is missing (the
// node-prep image is minimal; git is only needed for remote kustomizations).
func StepEnsureGit() IntegrationStep {
	return IntegrationStep{Desc: "git for remote kustomizations", Cmd: "command -v git >/dev/null 2>&1 || (apt-get update -qq && apt-get install -y -qq git)"}
}

// StepEnsureHelm installs a pinned Helm on the control plane if it is missing.
func StepEnsureHelm() IntegrationStep {
	return IntegrationStep{Desc: "helm " + HelmVersion, Cmd: "command -v helm >/dev/null 2>&1 || (curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | DESIRED_VERSION=" + HelmVersion + " bash)"}
}

// StepHelmInstall installs or upgrades a chart release (idempotent by design).
func StepHelmInstall(desc, release, repoName, repoURL, chart, version, namespace string, sets map[string]string) IntegrationStep {
	keys := make([]string, 0, len(sets))
	for k := range sets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var setFlags []string
	for _, k := range keys {
		setFlags = append(setFlags, "--set "+ShellQuote(k+"="+sets[k]))
	}
	cmd := fmt.Sprintf("helm repo add %s %s --force-update >/dev/null && helm repo update %s >/dev/null && "+
		"helm upgrade --install %s %s/%s --version %s --namespace %s --create-namespace --kubeconfig /etc/kubernetes/admin.conf %s",
		ShellQuote(repoName), ShellQuote(repoURL), ShellQuote(repoName), ShellQuote(release), repoName, chart, ShellQuote(version), ShellQuote(namespace), strings.Join(setFlags, " "))
	return IntegrationStep{Desc: desc, Cmd: cmd}
}

// StepSecret creates a Secret from literals if it does not exist. Values are
// shell-quoted, so a credential can hold any character; the Secret is never
// updated in place (rotation is the credential-rotation package's job).
func StepSecret(desc, namespace, name string, literals map[string]string) IntegrationStep {
	keys := make([]string, 0, len(literals))
	for k := range literals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var args []string
	for _, k := range keys {
		args = append(args, "--from-literal="+ShellQuote(k+"="+literals[k]))
	}
	cmd := fmt.Sprintf("%s -n %s get secret %s >/dev/null 2>&1 || %s -n %s create secret generic %s %s",
		KubectlAdmin, namespace, name, KubectlAdmin, namespace, name, strings.Join(args, " "))
	return IntegrationStep{Desc: desc, Cmd: KubectlAdmin + " get namespace " + namespace + " >/dev/null 2>&1 || " + KubectlAdmin + " create namespace " + namespace + "; " + cmd}
}

// StepWriteNodeFile writes a file on the control-plane node itself, not into the
// cluster.
//
// Some cloud charts read their configuration from a HOST path rather than from a
// Secret, whatever a `cloudConfigSecretName` value may suggest. Azure's
// cloud-provider chart mounts `/etc/kubernetes` by hostPath and passes
// `--cloud-config=/etc/kubernetes/azure.json`, so the controller died on
//
//	Couldn't open cloud provider configuration /etc/kubernetes/azure.json
//
// while the Secret it was also told about sat unused (2026-09-26). The file has to
// be where the chart looks.
//
// A quoted heredoc carries the content, so nothing inside it can break out, and
// the mode is applied before the content lands for anything holding credentials.
func StepWriteNodeFile(desc, path, content, mode string) IntegrationStep {
	if mode == "" {
		mode = "0600"
	}
	cmd := fmt.Sprintf("sudo mkdir -p %s && sudo install -m %s /dev/null %s && "+
		"sudo tee %s >/dev/null <<'ADHAR_NODE_FILE'\n%s\nADHAR_NODE_FILE",
		ShellQuote(filepath.Dir(path)), mode, ShellQuote(path), ShellQuote(path),
		strings.TrimSpace(content))
	return IntegrationStep{Desc: desc, Cmd: cmd}
}

// StepManifest applies an inline manifest (a StorageClass, a small patch
// target) through a quoted heredoc, so no quoting in the YAML can break out.
func StepManifest(desc, manifest string) IntegrationStep {
	return IntegrationStep{Desc: desc, Cmd: KubectlAdmin + " apply -f - <<'ADHAR_MANIFEST'\n" + strings.TrimSpace(manifest) + "\nADHAR_MANIFEST"}
}

// StepDefaultStorageClass creates a StorageClass and makes it the default —
// the platform's databases and stores all use the default class.
func StepDefaultStorageClass(name, provisioner string, parameters map[string]string) IntegrationStep {
	keys := make([]string, 0, len(parameters))
	for k := range parameters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var params strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&params, "\n  %s: %q", k, parameters[k])
	}
	manifest := fmt.Sprintf(`apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: %s
  annotations:
    storageclass.kubernetes.io/is-default-class: "true"
provisioner: %s
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: true
reclaimPolicy: Delete
parameters:%s`, name, provisioner, params.String())
	if len(parameters) == 0 {
		manifest = strings.TrimSuffix(manifest, "\nparameters:")
	}
	return StepManifest("default StorageClass "+name, manifest)
}

// StepMarkDefaultStorageClass flags an existing StorageClass as the default
// (for drivers whose manifests ship their own class).
func StepMarkDefaultStorageClass(name string) IntegrationStep {
	return IntegrationStep{Desc: "default StorageClass " + name, Cmd: KubectlAdmin + ` patch storageclass ` + name + ` -p '{"metadata":{"annotations":{"storageclass.kubernetes.io/is-default-class":"true"}}}'`}
}

// StepTolerateCSIStartupTaint lets a CSI node DaemonSet run on a worker that
// still carries globals.NodeCSIStartupTaint — it is the plugin whose CSINode
// lifts the taint, so without the toleration the taint can only expire by
// the autoscaler's fallback. Idempotent: skipped when the toleration exists.
func StepTolerateCSIStartupTaint(namespace, daemonset string) IntegrationStep {
	return IntegrationStep{
		Desc: "CSI node plugin tolerates the startup taint",
		Cmd: fmt.Sprintf("%s -n %s get daemonset %s -o jsonpath='{.spec.template.spec.tolerations}' | grep -q %s || "+
			`%s -n %s patch daemonset %s --type=json -p '[{"op":"add","path":"/spec/template/spec/tolerations/-","value":{"key":"%s","operator":"Exists","effect":"NoSchedule"}}]'`,
			KubectlAdmin, namespace, daemonset, globals.NodeCSIStartupTaint, KubectlAdmin, namespace, daemonset, globals.NodeCSIStartupTaint),
	}
}

// StepWaitDaemonSet waits until a DaemonSet exists (a chart or kustomization
// creates it asynchronously) so a following patch has something to patch.
func StepWaitDaemonSet(namespace, daemonset string) IntegrationStep {
	return IntegrationStep{Desc: "wait for DaemonSet " + daemonset, Cmd: fmt.Sprintf("for i in $(seq 1 60); do %s -n %s get daemonset %s >/dev/null 2>&1 && exit 0; sleep 5; done; echo 'DaemonSet %s did not appear'; exit 1", KubectlAdmin, namespace, daemonset, daemonset)}
}
