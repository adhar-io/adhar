/*
Copyright 2026 The Adhar Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package down

// identity.go: who a cluster is, written down BEFORE it is deleted.
//
// The volume step lists a cluster's block volumes by its install id (and, for
// Civo and DOKS, by the cloud's own cluster UUID) — but only after the cluster
// is gone, because that is when its volumes detach. Whatever identifies the
// cluster therefore has to be on disk before DeleteCluster runs:
//
//   - The install id lives in two places: this machine's record
//     (~/.adhar/state/installs) and the cluster's own adhar-cluster-spec
//     ConfigMap. A cluster created from another machine, or recreated
//     elsewhere, is known only to the second, and the cluster is the
//     authority on the id its CSI driver was given. So it is read from the
//     cluster and adopted, while the cluster can still answer.
//   - A managed cluster's UUID was held only in memory until the volume step's
//     final bookkeeping; a crash or a Ctrl+C between the deletion and that
//     point lost the only thing its volumes could be found by.
//
// An id the cluster reports that cannot be written down stops THIS cluster's
// deletion: deleting it would destroy the only copy of what its volumes carry.
// A cluster that cannot be reached at all is torn down anyway — the operator
// asked for it, and its volumes fall back to this machine's record or to the
// legacy evidence.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	pfactory "adhar-io/adhar/platform/providers"
)

// clusterIdentityTimeout bounds asking a cluster for its install id. A cluster
// being torn down is often a broken one; the teardown must not hang on it.
const clusterIdentityTimeout = 45 * time.Second

// clusterSpecIdentity is what a cluster's adhar-cluster-spec says about itself.
type clusterSpecIdentity struct {
	InstallID   string
	Provider    string
	Region      string
	ClusterName string
}

// readClusterSpec returns the identity recorded in the cluster's
// adhar-cluster-spec ConfigMap — a zero InstallID when the cluster has none
// (created before attribution). A variable so the teardown can be driven
// against a fake cloud.
var readClusterSpec = readClusterSpecFromCluster

// rememberClusterIdentity writes down, before the cluster is deleted, the
// identities its volumes will be listed by afterwards. It returns an error only
// when an identity was learned and could NOT be written down — the caller then
// leaves the cluster alone.
func rememberClusterIdentity(ctx context.Context, found *helpers.ResolvedCluster, region, clusterName string, detail func(string, ...interface{})) error {
	if found == nil || found.Cluster == nil || strings.EqualFold(found.ProviderName, globals.CloudProviderKind) {
		return nil // Kind makes no cloud volumes
	}
	providerName := found.ProviderName

	readCtx, cancel := context.WithTimeout(ctx, clusterIdentityTimeout)
	spec, err := readClusterSpec(readCtx, found, clusterName)
	cancel()
	id := strings.TrimSpace(spec.InstallID)
	switch {
	case err != nil:
		detail("  ! could not ask the cluster for its install id (%v); its volumes are listed by the id this machine recorded, if any", err)
	case !sameCluster(spec, providerName, region, clusterName):
		// ~/.adhar/clusters/<name>/kubeconfig is keyed by NAME alone, so it can
		// reach a different cluster of the same name (another cloud, another
		// region). Its id says nothing about this one.
		detail("  ! the kubeconfig for %s reaches cluster %s on %s %s, not this one; its install id is ignored",
			clusterName, spec.ClusterName, spec.Provider, spec.Region)
	case id == "":
		detail("  ● the cluster records no install id (it predates volume attribution); its volumes are offered only on evidence")
	case !pfactory.ValidClusterInstallID(id):
		detail("  ! %s holds %q as the install id, which is not an id adhar generates; ignored", globals.ClusterSpecConfigMapName, id)
	default:
		changed, err := pfactory.AdoptClusterInstall(providerName, region, clusterName, id)
		if err != nil {
			return fmt.Errorf("the cluster's volumes carry install id %s, and it could not be recorded on this machine (%v); "+
				"the cluster was NOT deleted, because its volumes could not be found again afterwards", id, err)
		}
		if changed {
			detail("  ● recorded install id %s from the cluster's %s; its volumes are listed by it once the cluster is gone", id, globals.ClusterSpecConfigMapName)
		} else {
			detail("  ● install id %s; its volumes are listed by it once the cluster is gone", id)
		}
	}

	if pfactory.IsCloudClusterUUID(found.Cluster.ID) {
		if _, err := pfactory.RecordProviderClusterID(providerName, region, clusterName, found.Cluster.ID); err != nil {
			return fmt.Errorf("the cluster's volumes are stamped with its cloud id %s, and it could not be recorded on this machine (%v); "+
				"the cluster was NOT deleted, because its volumes could not be found again afterwards", found.Cluster.ID, err)
		}
	}
	return nil
}

// sameCluster reports whether a cluster spec describes the cluster being torn
// down. A field the spec leaves empty (a cluster from before it was recorded)
// proves nothing either way and is not held against it.
func sameCluster(spec clusterSpecIdentity, providerName, region, clusterName string) bool {
	if spec.ClusterName != "" && spec.ClusterName != clusterName {
		return false
	}
	if spec.Region != "" && region != "" && !strings.EqualFold(spec.Region, region) {
		return false
	}
	if spec.Provider != "" && providerName != "" && providerFamily(spec.Provider) != providerFamily(providerName) {
		return false
	}
	return true
}

// providerFamily folds the spellings a provider is recorded under.
func providerFamily(name string) string {
	switch n := strings.ToLower(strings.TrimSpace(name)); n {
	case "do", "doks":
		return "digitalocean"
	case "eks":
		return "aws"
	case "gke", "google":
		return "gcp"
	case "aks":
		return "azure"
	case "k3s":
		return "civo"
	default:
		return n
	}
}

// readClusterSpecFromCluster reads adhar-cluster-spec through the cluster's
// kubeconfig: the one `adhar up` saved on this machine, else the one the
// provider can fetch (a managed cluster's cloud API, a compute cluster's
// control plane over SSH).
func readClusterSpecFromCluster(ctx context.Context, found *helpers.ResolvedCluster, clusterName string) (clusterSpecIdentity, error) {
	cfg, err := clusterRESTConfig(ctx, found, clusterName)
	if err != nil {
		return clusterSpecIdentity{}, err
	}
	cfg.Timeout = 20 * time.Second
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return clusterSpecIdentity{}, err
	}
	cm, err := cs.CoreV1().ConfigMaps(globals.AdharSystemNamespace).Get(ctx, globals.ClusterSpecConfigMapName, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return clusterSpecIdentity{}, nil
	}
	if err != nil {
		return clusterSpecIdentity{}, err
	}
	return clusterSpecIdentity{
		InstallID:   cm.Data[globals.ClusterSpecInstallIDKey],
		Provider:    cm.Data["provider"],
		Region:      cm.Data["region"],
		ClusterName: cm.Data["clusterName"],
	}, nil
}

func clusterRESTConfig(ctx context.Context, found *helpers.ResolvedCluster, clusterName string) (*rest.Config, error) {
	if home, err := os.UserHomeDir(); err == nil {
		path := filepath.Join(home, ".adhar", "clusters", clusterName, "kubeconfig")
		if _, err := os.Stat(path); err == nil {
			return clientcmd.BuildConfigFromFlags("", path)
		}
	}
	if found.Provider == nil {
		return nil, fmt.Errorf("no kubeconfig for %s on this machine", clusterName)
	}
	// The provider call may dial SSH or a cloud API; it must not outlive ctx.
	type result struct {
		kubeconfig string
		err        error
	}
	ch := make(chan result, 1)
	go func() {
		kc, err := found.Provider.GetKubeconfig(ctx, found.Cluster.ID)
		ch <- result{kc, err}
	}()
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("fetching the kubeconfig of %s: %w", clusterName, ctx.Err())
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("fetching the kubeconfig of %s: %w", clusterName, r.err)
		}
		return clientcmd.RESTConfigFromKubeConfig([]byte(r.kubeconfig))
	}
}
