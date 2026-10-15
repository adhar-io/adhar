package up

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"adhar-io/adhar/globals"
)

// persistClusterKubeconfig saves a freshly provisioned cluster's kubeconfig to
// ~/.adhar/clusters/<name>/kubeconfig (0600, next to the cluster's SSH key)
// and merges it into the user's default kubeconfig (~/.kube/config, or the
// first $KUBECONFIG path) as a named context "adhar-<name>", which it makes
// current. That way kubectl and `adhar get secrets` work immediately after
// `adhar up` on any provider — no manual fetching from the control plane.
//
// The standalone file is written first and is always authoritative; a merge
// failure is returned so the caller can warn, but the path stays valid.
func persistClusterKubeconfig(clusterName, kubeconfigStr string) (path, ctxName string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("resolving home dir: %w", err)
	}
	dir := filepath.Join(home, ".adhar", "clusters", clusterName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("creating %s: %w", dir, err)
	}
	path = filepath.Join(dir, "kubeconfig")
	// A cluster of the same NAME on another cloud lands in the same directory
	// and the same context. On 2026-10-15 a Civo bring-up of `adhar` overwrote
	// the DigitalOcean cluster `adhar`'s kubeconfig and context, and with the
	// cloud's SSH key gone too there was no way back into a running cluster.
	// A different server behind the same name is kept, never replaced.
	if err := preserveOtherClusterFile(path, kubeconfigStr); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(path, []byte(kubeconfigStr), 0o600); err != nil {
		return "", "", fmt.Errorf("writing %s: %w", path, err)
	}

	ctxName = KubeContextName(clusterName)
	if err := mergeIntoDefaultKubeconfig(kubeconfigStr, ctxName); err != nil {
		return path, ctxName, fmt.Errorf("merging into default kubeconfig: %w", err)
	}
	return path, ctxName, nil
}

// KubeContextName is the kube-context a provisioned cluster is merged in as.
//
// Prefixed so a platform cluster is recognisable among an engineer's other
// contexts — EXCEPT when the name already is the platform's, which is now the
// default (`adhar up` names the cluster `adhar` unless --name says otherwise).
// Prefixing unconditionally produced "adhar-adhar", and a stuttering context name
// looks like a bug in the tool that generated it.
func KubeContextName(clusterName string) string {
	if clusterName == globals.DefaultClusterName {
		return clusterName
	}
	return globals.DefaultClusterName + "-" + clusterName
}

// mergeIntoDefaultKubeconfig adds the cluster, user and context found in
// kubeconfigStr to the user's default kubeconfig under the stable name
// ctxName (so re-running `adhar up` replaces rather than duplicates entries)
// and sets it as the current context.
func mergeIntoDefaultKubeconfig(kubeconfigStr, ctxName string) error {
	src, err := clientcmd.Load([]byte(kubeconfigStr))
	if err != nil {
		return fmt.Errorf("parsing cluster kubeconfig: %w", err)
	}

	dstPath := clientcmd.NewDefaultClientConfigLoadingRules().GetDefaultFilename()
	dst, err := clientcmd.LoadFromFile(dstPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("loading %s: %w", dstPath, err)
		}
		dst = clientcmdapi.NewConfig()
	}

	// A provisioned kubeconfig (kubeadm admin.conf, managed-K8s export) carries
	// exactly one cluster/user; adopt them under the adhar-<name> identifiers.
	// Same context name, different API server: that is ANOTHER cluster that
	// happens to share the name (the same cluster rebuilt keeps its name and
	// gets a new server too, which is fine — its old entry is just stale). Keep
	// the previous entry under `<name>@<host>` so the operator is never locked
	// out of a cluster by bringing up a second one.
	if old, ok := dst.Clusters[ctxName]; ok {
		if newServer := firstServer(src); newServer != "" && old.Server != "" && old.Server != newServer {
			keep := ctxName + "@" + hostOf(old.Server)
			dst.Clusters[keep] = old
			if u, ok := dst.AuthInfos[ctxName]; ok {
				dst.AuthInfos[keep] = u
			}
			dst.Contexts[keep] = &clientcmdapi.Context{Cluster: keep, AuthInfo: keep}
		}
	}
	for _, c := range src.Clusters {
		dst.Clusters[ctxName] = c
		break
	}
	for _, u := range src.AuthInfos {
		dst.AuthInfos[ctxName] = u
		break
	}
	dst.Contexts[ctxName] = &clientcmdapi.Context{Cluster: ctxName, AuthInfo: ctxName}
	dst.CurrentContext = ctxName

	if err := os.MkdirAll(filepath.Dir(dstPath), 0o700); err != nil {
		return err
	}
	return clientcmd.WriteToFile(*dst, dstPath)
}

// firstServer returns the API server of the (single) cluster in a kubeconfig.
func firstServer(cfg *clientcmdapi.Config) string {
	for _, c := range cfg.Clusters {
		return c.Server
	}
	return ""
}

// hostOf reduces https://1.2.3.4:6443 to 1.2.3.4 — a context suffix a person
// can recognise.
func hostOf(server string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(server, "https://"), "http://")
	if i := strings.LastIndex(s, ":"); i > 0 && !strings.Contains(s[i:], "]") {
		s = s[:i]
	}
	return strings.Trim(s, "[]/")
}

// preserveOtherClusterFile keeps the per-cluster kubeconfig of a DIFFERENT
// server as kubeconfig.<host> before it is overwritten.
func preserveOtherClusterFile(path, incoming string) error {
	existing, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	oldCfg, err := clientcmd.Load(existing)
	if err != nil {
		return nil
	}
	newCfg, err := clientcmd.Load([]byte(incoming))
	if err != nil {
		return nil
	}
	oldServer, newServer := firstServer(oldCfg), firstServer(newCfg)
	if oldServer == "" || oldServer == newServer {
		return nil
	}
	keep := path + "." + hostOf(oldServer)
	if err := os.WriteFile(keep, existing, 0o600); err != nil {
		return fmt.Errorf("preserving the previous kubeconfig as %s: %w", keep, err)
	}
	return nil
}
