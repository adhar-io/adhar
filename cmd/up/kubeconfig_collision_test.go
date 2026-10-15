package up

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
)

func kubeconfigFor(server string) string {
	return `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster: {server: "` + server + `", insecure-skip-tls-verify: true}
users:
- name: u
  user: {token: t-` + server + `}
contexts:
- name: x
  context: {cluster: c, user: u}
current-context: x
`
}

// Two clusters named `adhar` on two clouds must not take each other's access
// away. On 2026-10-15 a Civo bring-up replaced the context and the per-cluster
// kubeconfig of the running DigitalOcean cluster of the same name.
func TestASecondClusterWithTheSameNameDoesNotEvictTheFirstFromKubeconfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KUBECONFIG", filepath.Join(home, ".kube", "config"))

	if _, _, err := persistClusterKubeconfig("adhar", kubeconfigFor("https://139.59.46.159:6443")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := persistClusterKubeconfig("adhar", kubeconfigFor("https://212.2.252.7:6443")); err != nil {
		t.Fatal(err)
	}

	cfg, err := clientcmd.LoadFromFile(filepath.Join(home, ".kube", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Clusters["adhar"].Server != "https://212.2.252.7:6443" {
		t.Errorf("the new cluster did not become `adhar`: %s", cfg.Clusters["adhar"].Server)
	}
	kept, ok := cfg.Clusters["adhar@139.59.46.159"]
	if !ok || kept.Server != "https://139.59.46.159:6443" {
		t.Fatal("the previous cluster behind the same name was evicted from kubeconfig; a running cluster is now unreachable")
	}
	if _, ok := cfg.Contexts["adhar@139.59.46.159"]; !ok {
		t.Error("the preserved cluster has no context")
	}
	if u, ok := cfg.AuthInfos["adhar@139.59.46.159"]; !ok || u.Token != "t-https://139.59.46.159:6443" {
		t.Error("the preserved cluster lost its credentials")
	}
	if _, err := os.Stat(filepath.Join(home, ".adhar", "clusters", "adhar", "kubeconfig.139.59.46.159")); err != nil {
		t.Error("the per-cluster kubeconfig of the previous server was not kept alongside the new one")
	}

	// The SAME cluster written twice (rebuild, same server) is not duplicated.
	if _, _, err := persistClusterKubeconfig("adhar", kubeconfigFor("https://212.2.252.7:6443")); err != nil {
		t.Fatal(err)
	}
	cfg, _ = clientcmd.LoadFromFile(filepath.Join(home, ".kube", "config"))
	if len(cfg.Clusters) != 2 {
		t.Errorf("re-writing the same server created extra entries: %v", keys(cfg.Clusters))
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
