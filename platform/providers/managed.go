package provider

import (
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Managed-Kubernetes helpers shared by the providers that offer the cloud's
// hosted control plane (EKS, AKS, GKE, DOKS, Civo k3s) as the opt-in
// alternative to the default kubeadm-on-raw-compute mode. Every other platform
// behaviour is identical in both modes; only how the cluster comes into being
// differs.

// ClusterModeIsManaged reports whether a provider `clusterMode` value selects
// the cloud's managed Kubernetes service. The empty value and the compute
// spellings are the default (kubeadm) mode.
func ClusterModeIsManaged(mode string, managedAliases ...string) bool {
	m := strings.ToLower(strings.TrimSpace(mode))
	if m == "managed" {
		return true
	}
	for _, a := range managedAliases {
		if m == strings.ToLower(a) {
			return true
		}
	}
	return false
}

// ManagedVersion normalises a requested Kubernetes version ("v1.37.0",
// "1.37", "") into the "<major>.<minor>" form the managed services accept.
// The empty string means "let the service pick its default".
func ManagedVersion(requested string) string {
	v := strings.TrimPrefix(strings.TrimSpace(requested), "v")
	if v == "" {
		return ""
	}
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return v
	}
	return parts[0] + "." + parts[1]
}

// ExecKubeconfig renders a kubeconfig whose user authenticates through a
// client-go credential exec plugin — the standard shape for EKS
// (`aws eks get-token`) and GKE (`gke-gcloud-auth-plugin`). caB64 is the
// base64-encoded cluster CA exactly as the cloud API returns it.
func ExecKubeconfig(name, endpoint, caB64, command string, args []string, env map[string]string, provideClusterInfo bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "apiVersion: v1\nkind: Config\nclusters:\n- name: %s\n  cluster:\n    server: %s\n    certificate-authority-data: %s\n", name, endpoint, caB64)
	fmt.Fprintf(&b, "contexts:\n- name: %s\n  context:\n    cluster: %s\n    user: %s\ncurrent-context: %s\n", name, name, name, name)
	fmt.Fprintf(&b, "users:\n- name: %s\n  user:\n    exec:\n      apiVersion: client.authentication.k8s.io/v1beta1\n      command: %s\n      interactiveMode: Never\n      provideClusterInfo: %t\n", name, command, provideClusterInfo)
	if len(args) > 0 {
		b.WriteString("      args:\n")
		for _, a := range args {
			fmt.Fprintf(&b, "      - %q\n", a)
		}
	}
	if len(env) > 0 {
		b.WriteString("      env:\n")
		for k, v := range env {
			fmt.Fprintf(&b, "      - name: %s\n        value: %q\n", k, v)
		}
	}
	return b.String()
}

// NodeNameForIP resolves the Kubernetes node name registered for an address
// (InternalIP or ExternalIP) through the control plane's admin kubeconfig.
// Empty when no node carries the address.
func NodeNameForIP(signer ssh.Signer, user, masterIP, ip string) string {
	out, err := SSHRun(signer, user, masterIP,
		`kubectl --kubeconfig /etc/kubernetes/admin.conf get nodes -o jsonpath='{range .items[*]}{.metadata.name}{range .status.addresses[*]} {.address}{end}{"\n"}{end}'`,
		2*time.Minute)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		for _, addr := range fields[1:] {
			if addr == ip {
				return fields[0]
			}
		}
	}
	return ""
}
