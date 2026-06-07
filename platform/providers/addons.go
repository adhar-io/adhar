package provider

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// This file provides shared, real (non-simulated) addon install/uninstall
// helpers used by the cloud provider implementations (Azure, GCP, DigitalOcean,
// custom). They drive `kubectl` and `helm` against a target cluster's
// kubeconfig.
//
// IMPORTANT — platform ingress model:
// The Adhar platform uses the Cilium Gateway API as its default ingress, NOT
// ingress-nginx. The "ingress"/"gateway" addon therefore installs the
// upstream Gateway API CRDs (served by Cilium when installed with
// gatewayAPI.enabled=true). ingress-nginx remains available only as an
// explicit, opt-in generic addon.

// Pinned versions for manifest-based addons.
const (
	gatewayAPIVersion    = "v1.1.0"
	certManagerVersion   = "v1.15.3"
	metricsServerURL     = "https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml"
	ingressNginxURL      = "https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-v1.11.2/deploy/static/provider/cloud/deploy.yaml"
	ciliumChartVersion   = "1.16.1"
	ciliumHelmRepoName   = "cilium"
	ciliumHelmRepoURL    = "https://helm.cilium.io/"
	certManagerNamespace = "cert-manager"
)

// HelmAddonOptions describes a generic Helm-chart based addon install.
type HelmAddonOptions struct {
	ReleaseName string
	RepoName    string // helm repo short name (for `helm repo add`)
	RepoURL     string // helm repo URL (empty if Chart is an OCI ref or local path)
	Chart       string // e.g. "cilium/cilium" or "oci://registry/chart"
	Version     string // chart version (optional)
	Namespace   string
	Values      map[string]interface{} // rendered as repeated --set key=value
	ExtraArgs   []string               // any additional helm args
}

// WriteKubeconfigTempFile writes kubeconfig YAML to a temp file and returns its
// path plus a cleanup func that removes it.
func WriteKubeconfigTempFile(kubeconfig string) (string, func(), error) {
	if strings.TrimSpace(kubeconfig) == "" {
		return "", func() {}, fmt.Errorf("empty kubeconfig")
	}
	f, err := os.CreateTemp("", "adhar-addon-kubeconfig-*.yaml")
	if err != nil {
		return "", func() {}, fmt.Errorf("failed to create temp kubeconfig: %w", err)
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	if _, err := f.WriteString(kubeconfig); err != nil {
		_ = f.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("failed to write temp kubeconfig: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("failed to close temp kubeconfig: %w", err)
	}
	return f.Name(), cleanup, nil
}

// runKubectl runs a kubectl command against the given kubeconfig.
func runKubectl(ctx context.Context, kubeconfigPath string, args ...string) error {
	full := append([]string{"--kubeconfig", kubeconfigPath}, args...)
	cmd := exec.CommandContext(ctx, "kubectl", full...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfigPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kubectl %s failed: %w\noutput: %s", strings.Join(args, " "), err, string(out))
	}
	return nil
}

// runHelm runs a helm command against the given kubeconfig.
func runHelm(ctx context.Context, kubeconfigPath string, args ...string) error {
	cmd := exec.CommandContext(ctx, "helm", args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfigPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("helm %s failed: %w\noutput: %s", strings.Join(args, " "), err, string(out))
	}
	return nil
}

// InstallHelmAddon installs/upgrades a Helm chart against the target cluster.
func InstallHelmAddon(ctx context.Context, kubeconfigPath string, opts HelmAddonOptions) error {
	if opts.ReleaseName == "" || opts.Chart == "" || opts.Namespace == "" {
		return fmt.Errorf("helm addon requires releaseName, chart and namespace")
	}
	// Register the chart repo when an HTTP(S) repo URL is supplied (skip for
	// OCI refs / local paths).
	if opts.RepoURL != "" && opts.RepoName != "" {
		if err := runHelm(ctx, kubeconfigPath, "repo", "add", opts.RepoName, opts.RepoURL, "--force-update"); err != nil {
			return err
		}
		if err := runHelm(ctx, kubeconfigPath, "repo", "update", opts.RepoName); err != nil {
			return err
		}
	}

	args := []string{
		"upgrade", "--install", opts.ReleaseName, opts.Chart,
		"--namespace", opts.Namespace, "--create-namespace", "--wait",
	}
	if opts.Version != "" {
		args = append(args, "--version", opts.Version)
	}
	for k, v := range opts.Values {
		args = append(args, "--set", fmt.Sprintf("%s=%v", k, v))
	}
	args = append(args, opts.ExtraArgs...)
	return runHelm(ctx, kubeconfigPath, args...)
}

// UninstallHelmAddon removes a Helm release from the target cluster.
func UninstallHelmAddon(ctx context.Context, kubeconfigPath, releaseName, namespace string) error {
	if releaseName == "" || namespace == "" {
		return fmt.Errorf("helm uninstall requires releaseName and namespace")
	}
	return runHelm(ctx, kubeconfigPath, "uninstall", releaseName, "--namespace", namespace, "--wait", "--ignore-not-found")
}

// HelmOptionsFromConfig builds HelmAddonOptions from a generic addon config map.
// Recognised keys: repoName, repoURL, chart, version, namespace, releaseName, values(map).
func HelmOptionsFromConfig(defaultRelease string, config map[string]interface{}) (HelmAddonOptions, error) {
	opts := HelmAddonOptions{ReleaseName: defaultRelease, Namespace: "default"}
	if v, ok := config["releaseName"].(string); ok && v != "" {
		opts.ReleaseName = v
	}
	if v, ok := config["repoName"].(string); ok {
		opts.RepoName = v
	}
	if v, ok := config["repoURL"].(string); ok {
		opts.RepoURL = v
	}
	if v, ok := config["chart"].(string); ok {
		opts.Chart = v
	}
	if v, ok := config["version"].(string); ok {
		opts.Version = v
	}
	if v, ok := config["namespace"].(string); ok && v != "" {
		opts.Namespace = v
	}
	if vals, ok := config["values"].(map[string]interface{}); ok {
		opts.Values = vals
	}
	if opts.Chart == "" {
		return opts, fmt.Errorf("generic helm addon requires a 'chart' value in config (e.g. repo/chart or oci://...)")
	}
	return opts, nil
}

// --- Cilium (CNI + Gateway API dataplane) ---

// InstallCiliumAddon installs Cilium via Helm. Cilium is the platform CNI and
// also provides the Gateway API implementation (gatewayAPI.enabled=true).
func InstallCiliumAddon(ctx context.Context, kubeconfigPath string, config map[string]interface{}) error {
	opts := HelmAddonOptions{
		ReleaseName: "cilium",
		RepoName:    ciliumHelmRepoName,
		RepoURL:     ciliumHelmRepoURL,
		Chart:       "cilium/cilium",
		Version:     ciliumChartVersion,
		Namespace:   "kube-system",
		Values: map[string]interface{}{
			// Enable the Cilium Gateway API implementation (platform default ingress).
			"gatewayAPI.enabled": true,
		},
	}
	// Allow caller overrides (e.g. version, extra values).
	if v, ok := config["version"].(string); ok && v != "" {
		opts.Version = v
	}
	if vals, ok := config["values"].(map[string]interface{}); ok {
		for k, val := range vals {
			opts.Values[k] = val
		}
	}
	// Gateway API CRDs must exist before Cilium can serve them.
	if err := InstallGatewayAPIAddon(ctx, kubeconfigPath); err != nil {
		return fmt.Errorf("failed to install Gateway API CRDs prerequisite for Cilium: %w", err)
	}
	return InstallHelmAddon(ctx, kubeconfigPath, opts)
}

// --- metrics-server ---

func InstallMetricsServerAddon(ctx context.Context, kubeconfigPath string) error {
	return runKubectl(ctx, kubeconfigPath, "apply", "-f", metricsServerURL)
}

func UninstallMetricsServerAddon(ctx context.Context, kubeconfigPath string) error {
	return runKubectl(ctx, kubeconfigPath, "delete", "-f", metricsServerURL, "--ignore-not-found")
}

// --- cert-manager ---

func InstallCertManagerAddon(ctx context.Context, kubeconfigPath string) error {
	url := fmt.Sprintf("https://github.com/cert-manager/cert-manager/releases/download/%s/cert-manager.yaml", certManagerVersion)
	return runKubectl(ctx, kubeconfigPath, "apply", "-f", url)
}

func UninstallCertManagerAddon(ctx context.Context, kubeconfigPath string) error {
	url := fmt.Sprintf("https://github.com/cert-manager/cert-manager/releases/download/%s/cert-manager.yaml", certManagerVersion)
	return runKubectl(ctx, kubeconfigPath, "delete", "-f", url, "--ignore-not-found")
}

// --- Gateway API (platform default ingress, served by Cilium) ---

func InstallGatewayAPIAddon(ctx context.Context, kubeconfigPath string) error {
	// Install upstream Gateway API CRDs. Cilium (gatewayAPI.enabled=true) acts
	// as the controller for these resources — this is the platform's default
	// ingress mechanism instead of ingress-nginx.
	url := fmt.Sprintf("https://github.com/kubernetes-sigs/gateway-api/releases/download/%s/standard-install.yaml", gatewayAPIVersion)
	return runKubectl(ctx, kubeconfigPath, "apply", "-f", url)
}

func UninstallGatewayAPIAddon(ctx context.Context, kubeconfigPath string) error {
	url := fmt.Sprintf("https://github.com/kubernetes-sigs/gateway-api/releases/download/%s/standard-install.yaml", gatewayAPIVersion)
	return runKubectl(ctx, kubeconfigPath, "delete", "-f", url, "--ignore-not-found")
}

// --- ingress-nginx (opt-in only; NOT the platform default) ---

func InstallIngressNginxAddon(ctx context.Context, kubeconfigPath string) error {
	// NOTE: Cilium Gateway API is the platform default ingress. ingress-nginx is
	// provided only as an explicit opt-in for users who specifically request it.
	return runKubectl(ctx, kubeconfigPath, "apply", "-f", ingressNginxURL)
}

func UninstallIngressNginxAddon(ctx context.Context, kubeconfigPath string) error {
	return runKubectl(ctx, kubeconfigPath, "delete", "-f", ingressNginxURL, "--ignore-not-found")
}
