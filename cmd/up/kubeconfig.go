package up

import (
	"fmt"
	"os"
	"path/filepath"

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
