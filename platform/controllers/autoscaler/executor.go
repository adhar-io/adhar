/*
Copyright 2025.

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

package autoscaler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"adhar-io/adhar/globals"
	pfactory "adhar-io/adhar/platform/providers"
)

// errNoClusterSpec is returned when the cloud facts the CLI records at
// bootstrap are missing, so the autoscaler cannot reach the provider. It is a
// configuration problem, surfaced in status rather than retried forever.
var errNoClusterSpec = fmt.Errorf("no %s ConfigMap in the platform namespace: this cluster was not bootstrapped with cloud provider details, so nodes cannot be added or removed automatically", globals.ClusterSpecConfigMapName)

// clusterSpec is the bootstrap-time description of the cloud cluster, read
// from the adhar-cluster-spec ConfigMap. The autoscaler cannot infer any of
// this from inside the cluster.
type clusterSpec struct {
	Provider          string
	Region            string
	ClusterName       string
	NodeGroup         string
	Size              string
	KubernetesVersion string
	// ProviderConfig is the credential-free provider map the CLI built from
	// config.yaml, so the provider client is constructed exactly as the CLI
	// constructs it for `adhar cluster scale`.
	ProviderConfig map[string]interface{}
}

// loadClusterSpec reads the ConfigMap the CLI writes at bootstrap.
func loadClusterSpec(ctx context.Context, c client.Client, namespace string) (*clusterSpec, error) {
	cm := &corev1.ConfigMap{}
	if err := c.Get(ctx, types.NamespacedName{Name: globals.ClusterSpecConfigMapName, Namespace: namespace}, cm); err != nil {
		return nil, err
	}
	spec := &clusterSpec{
		Provider:          cm.Data["provider"],
		Region:            cm.Data["region"],
		ClusterName:       cm.Data["clusterName"],
		NodeGroup:         cm.Data["nodeGroup"],
		Size:              cm.Data["size"],
		KubernetesVersion: cm.Data["kubernetesVersion"],
	}
	if raw := cm.Data["providerConfig"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &spec.ProviderConfig); err != nil {
			return nil, fmt.Errorf("parsing providerConfig in %s: %w", globals.ClusterSpecConfigMapName, err)
		}
	}
	if spec.Provider == "" || spec.ClusterName == "" {
		return nil, fmt.Errorf("%s is missing provider/clusterName", globals.ClusterSpecConfigMapName)
	}
	return spec, nil
}

// Canonical provider names, as recorded in the cluster-spec ConfigMap and
// registered with the provider factory.
const (
	providerDigitalOcean = "digitalocean"
	providerAWS          = "aws"
	providerGCP          = "gcp"
	providerAzure        = "azure"
	providerCivo         = "civo"
)

// credentialKeys maps a provider to the Secret its Crossplane ProviderConfig
// uses — the same Secret `adhar up` materialises (see cmd/up/crossplane_creds.go),
// reused here rather than storing a second copy of the same credential.
func credentialSecretName(provider string) string {
	switch provider {
	case providerDigitalOcean, providerAWS, providerGCP, providerAzure, providerCivo:
		return provider + "-credentials"
	default:
		return ""
	}
}

// providerCredentials turns the credentials Secret into the config-map keys
// each provider's constructor reads (the shapes are fixed by
// config.ConfigProviderConfig.ToProviderMap).
func providerCredentials(provider string, data map[string][]byte) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	switch provider {
	case providerDigitalOcean, providerCivo:
		token := strings.TrimSpace(string(data["token"]))
		if token == "" {
			return nil, fmt.Errorf("secret %s-credentials has no 'token'", provider)
		}
		out["token"] = token
	case providerAWS:
		// The Secret holds an AWS credentials file; the provider constructor
		// wants the two values.
		id, secret := parseAWSCredentialsINI(string(data["credentials"]))
		if id == "" || secret == "" {
			return nil, fmt.Errorf("secret aws-credentials has no usable [default] profile")
		}
		out["accessKeyId"] = id
		out["secretAccessKey"] = secret
	case providerGCP:
		key := strings.TrimSpace(string(data["credentials"]))
		if key == "" {
			return nil, fmt.Errorf("secret gcp-credentials has no 'credentials'")
		}
		out["serviceAccountKey"] = key
	case providerAzure:
		var creds map[string]string
		if err := json.Unmarshal(data["credentials"], &creds); err != nil {
			return nil, fmt.Errorf("secret azure-credentials is not the expected JSON: %w", err)
		}
		for k, v := range creds {
			out[k] = v
		}
	default:
		return nil, fmt.Errorf("provider %q has no credential mapping", provider)
	}
	return out, nil
}

// parseAWSCredentialsINI pulls the default profile's key pair out of an AWS
// shared-credentials file.
func parseAWSCredentialsINI(s string) (string, string) {
	var id, secret string
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "aws_access_key_id":
			id = v
		case "aws_secret_access_key":
			secret = v
		}
	}
	return id, secret
}

// newProvider builds the cloud provider client the scaling operations run
// through: the bootstrap-time provider config, the credentials Secret, and the
// cluster's SSH key materialised where the kubeadm helpers expect it.
func (r *Reconciler) newProvider(ctx context.Context, spec *clusterSpec) (pfactory.Provider, error) {
	cfg := map[string]interface{}{}
	for k, v := range spec.ProviderConfig {
		cfg[k] = v
	}
	if spec.Region != "" {
		cfg["region"] = spec.Region
	}
	// Keep the node size in the nested config section the providers read, so a
	// node added by the autoscaler matches the ones `adhar up` created.
	if spec.Size != "" {
		section, _ := cfg["config"].(map[string]interface{})
		if section == nil {
			section = map[string]interface{}{}
		}
		if _, ok := section["droplet_size"]; !ok {
			section["droplet_size"] = spec.Size
		}
		cfg["config"] = section
	}

	if name := credentialSecretName(spec.Provider); name != "" {
		secret := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: r.Namespace}, secret); err != nil {
			return nil, fmt.Errorf("reading %s: %w", name, err)
		}
		creds, err := providerCredentials(spec.Provider, secret.Data)
		if err != nil {
			return nil, err
		}
		for k, v := range creds {
			cfg[k] = v
		}
	}

	if err := r.ensureClusterSSHKey(ctx, spec.ClusterName); err != nil {
		return nil, err
	}

	return pfactory.DefaultFactory.CreateProvider(spec.Provider, cfg)
}

// ensureClusterSSHKey writes the cluster's kubeadm SSH key to the on-disk
// location the provider helpers load it from (~/.adhar/clusters/<name>/).
// Growing a self-managed cluster means running `kubeadm token create` on the
// control plane and `kubeadm join` on the new machine, both over SSH — the key
// normally only exists on the laptop that ran `adhar up`, so the CLI mirrors
// it into a Secret for the in-cluster manager. Managed-Kubernetes clusters
// have no such Secret and need none.
func (r *Reconciler) ensureClusterSSHKey(ctx context.Context, clusterName string) error {
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: globals.ClusterSSHSecretName, Namespace: r.Namespace}, secret)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", globals.ClusterSSHSecretName, err)
	}
	key := secret.Data[globals.ClusterSSHSecretKey]
	if len(key) == 0 {
		return nil
	}
	dir, err := pfactory.ClusterStateDir(clusterName)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, globals.ClusterSSHSecretKey)
	if existing, err := os.ReadFile(path); err == nil && string(existing) == string(key) {
		return nil
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return fmt.Errorf("materialising cluster SSH key: %w", err)
	}
	return nil
}

// scaleUp adds one worker through the provider, using the same node-group
// scaling path as `adhar cluster scale` (which prepares the machine and joins
// it with kubeadm, including the standard node-prep script).
func (r *Reconciler) scaleUp(ctx context.Context, spec *clusterSpec, nodeGroup string, current int32) error {
	prov, err := r.newProvider(ctx, spec)
	if err != nil {
		return err
	}
	clusterID := resolveClusterID(ctx, prov, spec.ClusterName)
	log.FromContext(ctx).Info("scaling up worker node group", "cluster", clusterID, "nodeGroup", nodeGroup, "desired", current+1)
	return prov.ScaleNodeGroup(ctx, clusterID, nodeGroup, int(current)+1)
}

// scaleDown drains the chosen worker and then removes the machine behind it.
// The provider must be able to remove that specific node: a desired-count API
// would delete whichever instance it likes, not the one we drained.
func (r *Reconciler) scaleDown(ctx context.Context, spec *clusterSpec, nodeName string) error {
	prov, err := r.newProvider(ctx, spec)
	if err != nil {
		return err
	}
	remover, ok := prov.(pfactory.NodeRemover)
	if !ok {
		return fmt.Errorf("provider %s cannot remove an individual node; scale down manually with `adhar cluster scale`", spec.Provider)
	}
	clusterID := resolveClusterID(ctx, prov, spec.ClusterName)

	if err := r.drainNode(ctx, nodeName); err != nil {
		// The node stays cordoned: capacity is already withheld, and the next
		// tick retries the drain rather than removing a machine that still
		// hosts workloads.
		return err
	}
	if err := remover.RemoveWorkerNode(ctx, clusterID, nodeName); err != nil {
		return err
	}
	// The provider's removal path deletes the Node object through the control
	// plane, but that is best-effort; make sure no ghost node is left behind.
	node := &corev1.Node{}
	if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, node); err == nil {
		if err := r.Delete(ctx, node); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting node object %s: %w", nodeName, err)
		}
	}
	return nil
}

// resolveClusterID maps the platform's cluster name onto the provider-native
// ID, exactly as `adhar cluster scale` resolves it. Self-managed clusters are
// identified by name, so a listing failure is not fatal — the name is then
// used directly.
func resolveClusterID(ctx context.Context, prov pfactory.Provider, name string) string {
	clusters, err := prov.ListClusters(ctx)
	if err != nil {
		return name
	}
	for _, c := range clusters {
		if c.Name == name || c.ID == name {
			return c.ID
		}
	}
	return name
}

// drainNode cordons a node and evicts every pod that can move, honouring
// PodDisruptionBudgets through the eviction API. DaemonSet and static pods are
// left alone: nothing can reschedule them, and the machine is about to go.
func (r *Reconciler) drainNode(ctx context.Context, nodeName string) error {
	logger := log.FromContext(ctx)

	node := &corev1.Node{}
	if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
		return fmt.Errorf("getting node %s: %w", nodeName, err)
	}
	if !node.Spec.Unschedulable {
		patched := node.DeepCopy()
		patched.Spec.Unschedulable = true
		if err := r.Patch(ctx, patched, client.MergeFrom(node)); err != nil {
			return fmt.Errorf("cordoning node %s: %w", nodeName, err)
		}
	}

	deadline := time.Now().Add(r.drainTimeout())
	for {
		pods := &corev1.PodList{}
		if err := r.List(ctx, pods); err != nil {
			return fmt.Errorf("listing pods on %s: %w", nodeName, err)
		}
		remaining := 0
		for i := range pods.Items {
			p := pods.Items[i]
			// Filtered client-side: a field-selector index would have to be
			// registered on the manager cache, and the pod list is already
			// cached in memory.
			if p.Spec.NodeName != nodeName || isDaemonSetPod(p) || isMirrorPod(p) || isTerminated(p) {
				continue
			}
			remaining++
			if p.DeletionTimestamp != nil {
				continue
			}
			evict := &policyv1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: p.Name, Namespace: p.Namespace}}
			if err := r.SubResource("eviction").Create(ctx, &p, evict); err != nil {
				// 429 means a PodDisruptionBudget is holding the pod back —
				// exactly what we want; retry on the next pass.
				if apierrors.IsTooManyRequests(err) || apierrors.IsConflict(err) {
					logger.V(1).Info("eviction blocked by disruption budget; retrying", "pod", p.Namespace+"/"+p.Name)
					continue
				}
				if apierrors.IsNotFound(err) {
					continue
				}
				return fmt.Errorf("evicting %s/%s: %w", p.Namespace, p.Name, err)
			}
		}
		if remaining == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("drain of %s timed out with %d pod(s) still running", nodeName, remaining)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
}

func (r *Reconciler) drainTimeout() time.Duration {
	if r.DrainTimeout > 0 {
		return r.DrainTimeout
	}
	return 5 * time.Minute
}
