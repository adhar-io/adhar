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

// The cluster mode vocabulary, identical for every provider. Two values, and
// the provider decides what "managed" means for its own cloud: EKS, AKS, GKE,
// DOKS or Civo k3s.
//
// It used to be per-provider — `clusterMode: eks|aks|gke|doks|k3s`, plus a
// `useManagedK8s: true` boolean, plus Civo spelling the key `cluster_mode` and
// accepting only "compute" or "k3s". Five ways to say one thing meant a config
// could not be moved between clouds, and the service name in the value was
// redundant with the provider that was already chosen.
const (
	// ClusterModeCompute: the platform builds the cluster itself, kubeadm on raw
	// instances it creates.
	ClusterModeCompute = "compute"
	// ClusterModeManaged: the cloud builds and runs the control plane (EKS, AKS,
	// GKE, DOKS, Civo k3s). The platform creates the cluster through the cloud's
	// Kubernetes API and still owns its lifecycle.
	ClusterModeManaged = "managed"
	// ClusterModeProvided: the cluster ALREADY EXISTS and the platform only
	// installs itself onto it. Nothing about the cluster is created, and — the
	// part that matters — nothing about it is ever deleted: `adhar down` removes
	// the platform's own resources and leaves the cluster running. Anything else
	// would destroy infrastructure the operator did not ask this tool to manage.
	ClusterModeProvided = "provided"
)

// NormalizeClusterMode canonicalises a user-supplied cluster mode. Empty means
// the default, compute.
//
// It REJECTS the old per-cloud spellings rather than mapping them, because
// mapping silently is the dangerous option: a stale `clusterMode: gke` that
// quietly resolved to compute would build a kubeadm cluster on Compute Engine
// for someone who asked for GKE, and they would not find out until the bill or
// the first missing load balancer.
func NormalizeClusterMode(raw string) (string, error) {
	switch m := strings.ToLower(strings.TrimSpace(raw)); m {
	case "", ClusterModeCompute:
		return ClusterModeCompute, nil
	case ClusterModeManaged:
		return ClusterModeManaged, nil
	case ClusterModeProvided:
		return ClusterModeProvided, nil
	case "eks", "aks", "gke", "doks", "k3s":
		return "", fmt.Errorf("clusterMode %q names a cloud service; the platform has three modes — %q, %q and %q — "+
			"use %q to get this cloud's managed Kubernetes", m, ClusterModeCompute, ClusterModeManaged, ClusterModeProvided, ClusterModeManaged)
	default:
		return "", fmt.Errorf("unknown clusterMode %q (expected %q, %q or %q)",
			raw, ClusterModeCompute, ClusterModeManaged, ClusterModeProvided)
	}
}

// ClusterModeIsProvided reports whether the cluster already exists and the
// platform is only installing onto it.
func ClusterModeIsProvided(mode string) bool {
	return strings.EqualFold(strings.TrimSpace(mode), ClusterModeProvided)
}

// ClusterLifecycleIsOurs reports whether the platform may create and delete the
// cluster itself. False for a provided cluster — and callers must check this
// before any destructive call, because "we did not build it" is the only thing
// standing between `adhar down` and someone else's production cluster.
func ClusterLifecycleIsOurs(mode string) bool {
	return !ClusterModeIsProvided(mode)
}

// ParseClusterMode reads the cluster mode out of a provider's config map and
// validates it, so every provider accepts exactly the same spelling.
//
// It looks for `clusterMode` and the snake_case `cluster_mode`, at the top level
// and inside a nested `config:` section, because the provider blocks differ in
// where they put their settings (Civo and DigitalOcean nest, AWS flattens).
//
// A `useManagedK8s` key is an ERROR, not something to ignore. It used to be the
// boolean that opted into the managed service, and silently dropping it would
// turn a config that asked for EKS into a kubeadm cluster on EC2 — the failure
// would surface as a missing load balancer or an unexpected bill, long after the
// create. Removing a key safely means refusing to run until it is gone.
func ParseClusterMode(config map[string]interface{}) (string, error) {
	sections := []map[string]interface{}{config}
	if nested, ok := config["config"].(map[string]interface{}); ok {
		sections = append(sections, nested)
	}
	for _, section := range sections {
		if _, ok := section["useManagedK8s"]; ok {
			return "", fmt.Errorf("useManagedK8s has been removed: set %q: %q (or %q) instead",
				"clusterMode", ClusterModeManaged, ClusterModeCompute)
		}
	}
	for _, section := range sections {
		for _, key := range []string{"clusterMode", "cluster_mode"} {
			if v, ok := section[key].(string); ok && strings.TrimSpace(v) != "" {
				return NormalizeClusterMode(v)
			}
		}
	}
	return ClusterModeCompute, nil
}

// ClusterModeIsManaged reports whether the mode selects the cloud's managed
// Kubernetes service. Anything that is not exactly "managed" is compute; a bad
// value is caught by NormalizeClusterMode when the config is parsed, so this
// never has to guess.
func ClusterModeIsManaged(mode string) bool {
	return strings.EqualFold(strings.TrimSpace(mode), ClusterModeManaged)
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
