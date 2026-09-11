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

package up

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/config"
	pfactory "adhar-io/adhar/platform/providers"
)

// Cloud facts for the in-cluster controllers.
//
// Once `adhar up` exits, nothing inside the cluster knows which cloud, region,
// instance size or cluster name it came from — that lives in config.yaml on
// the operator's machine. The node autoscaler has to reconstruct the very same
// provider client `adhar cluster scale` uses, so the CLI records the
// credential-free facts in a ConfigMap at bootstrap (credentials stay in the
// per-cloud Secret that already exists for Crossplane).

// Keys used in the recorded cluster spec / provider map.
const (
	keyToken    = "token"
	keyProvider = "provider"
)

// credentialProviderKeys are stripped from the recorded provider map: they are
// secrets and are read from the cloud credentials Secret instead.
var credentialProviderKeys = map[string]bool{
	keyToken:            true,
	"accessKeyId":       true,
	"secretAccessKey":   true,
	"sessionToken":      true,
	"serviceAccountKey": true,
	"clientSecret":      true,
	"credentials_file":  true,
}

// clusterConfigValue returns the first matching key from the environment's
// resolved clusterConfig (config.yaml accepts several spellings per concept).
func clusterConfigValue(envConfig *config.ResolvedEnvironmentConfig, keys ...string) string {
	if envConfig == nil {
		return ""
	}
	for _, want := range keys {
		for _, kv := range envConfig.ResolvedClusterConfig {
			if kv.Key == want && kv.Value != "" {
				return kv.Value
			}
		}
	}
	return ""
}

// providerConfigValue reads a key from the provider's nested `config:` section.
func providerConfigValue(envConfig *config.ResolvedEnvironmentConfig, keys ...string) string {
	if envConfig == nil || envConfig.ProviderConfig == nil || envConfig.ProviderConfig.Config == nil {
		return ""
	}
	for _, want := range keys {
		if v, ok := envConfig.ProviderConfig.Config[want].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// ensureClusterSpecConfigMap records the provider facts the in-cluster
// controllers need. It is idempotent and carries no credentials.
func ensureClusterSpecConfigMap(
	ctx context.Context,
	c client.Client,
	envConfig *config.ResolvedEnvironmentConfig,
	clusterName string,
) error {
	providerName := ""
	if envConfig != nil {
		providerName = envConfig.ResolvedProvider
	}
	canonical := canonicalProvider(providerName)
	if canonical == "" {
		canonical = strings.ToLower(strings.TrimSpace(providerName))
	}
	if canonical == "" {
		return nil
	}

	nodeGroup := v1alpha1.DefaultAutoscalingNodeGroup
	if envConfig != nil && envConfig.Autoscaling != nil && envConfig.Autoscaling.NodeGroup != "" {
		nodeGroup = envConfig.Autoscaling.NodeGroup
	}

	data := map[string]string{
		keyProvider:   canonical,
		"clusterName": clusterName,
		"nodeGroup":   nodeGroup,
		"size": firstNonEmpty(
			clusterConfigValue(envConfig, "nodeSize", "nodeInstanceType", "instanceType", "machineType"),
			providerConfigValue(envConfig, "droplet_size", "vm_size", "machine_type", "instance_type"),
		),
		"kubernetesVersion": clusterConfigValue(envConfig, "kubeVersion", "version"),
	}
	if envConfig != nil {
		data["region"] = envConfig.ResolvedRegion
	}

	// The provider map, minus credentials, so the controller constructs the
	// provider exactly as the CLI does.
	if envConfig != nil && envConfig.ProviderConfig != nil {
		sanitized := map[string]interface{}{}
		for k, v := range envConfig.ProviderConfig.ToProviderMap() {
			if credentialProviderKeys[k] {
				continue
			}
			sanitized[k] = v
		}
		encoded, err := json.Marshal(sanitized)
		if err != nil {
			return fmt.Errorf("encoding provider config: %w", err)
		}
		data["providerConfig"] = string(encoded)
	}

	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name:      globals.ClusterSpecConfigMapName,
		Namespace: globals.AdharSystemNamespace,
	}}
	_, err := controllerutil.CreateOrUpdate(ctx, c, cm, func() error {
		if cm.Labels == nil {
			cm.Labels = map[string]string{}
		}
		cm.Labels["adhar.io/component"] = "cluster-spec"
		cm.Data = data
		return nil
	})
	if err != nil {
		return fmt.Errorf("writing %s: %w", globals.ClusterSpecConfigMapName, err)
	}
	return nil
}

// ensureClusterSSHSecret mirrors the cluster's kubeadm SSH key into the
// platform namespace. Adding a worker to a self-managed cluster means running
// kubeadm on the control plane over SSH, so the in-cluster autoscaler needs
// the same key the CLI uses; on managed Kubernetes there is no key and this is
// a no-op. Not finding the key is not an error: the cluster may have been
// created from another machine, in which case autoscaling reports why it
// cannot act instead of failing the bootstrap.
func ensureClusterSSHSecret(ctx context.Context, c client.Client, clusterName string) error {
	dir, err := pfactory.ClusterStateDir(clusterName)
	if err != nil {
		return err
	}
	key, err := os.ReadFile(filepath.Join(dir, globals.ClusterSSHSecretKey))
	if err != nil {
		return nil //nolint:nilerr // managed clusters have no kubeadm SSH key
	}

	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      globals.ClusterSSHSecretName,
		Namespace: globals.AdharSystemNamespace,
	}}
	_, err = controllerutil.CreateOrUpdate(ctx, c, secret, func() error {
		if secret.Labels == nil {
			secret.Labels = map[string]string{}
		}
		secret.Labels["adhar.io/component"] = "cluster-ssh"
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = map[string][]byte{globals.ClusterSSHSecretKey: key}
		return nil
	})
	if err != nil {
		return fmt.Errorf("writing %s: %w", globals.ClusterSSHSecretName, err)
	}
	return nil
}

// autoscalingSpecFromConfig converts the config.yaml block into the API spec,
// leaving unset fields to the API defaults. Malformed durations are reported
// rather than silently ignored — a typo would otherwise change scaling
// behaviour invisibly.
func autoscalingSpecFromConfig(a *config.AutoscalingConfig) (*v1alpha1.AutoscalingSpec, error) {
	if a == nil {
		return nil, nil
	}
	spec := &v1alpha1.AutoscalingSpec{
		Enabled:                       a.Enabled,
		MinWorkers:                    a.MinWorkers,
		MaxWorkers:                    a.MaxWorkers,
		NodeGroup:                     a.NodeGroup,
		ScaleDownUtilizationThreshold: a.ScaleDownUtilizationThreshold,
	}
	parse := func(field, value string) (metav1.Duration, error) {
		if strings.TrimSpace(value) == "" {
			return metav1.Duration{}, nil
		}
		d, err := time.ParseDuration(value)
		if err != nil {
			return metav1.Duration{}, fmt.Errorf("autoscaling.%s: %q is not a duration (e.g. \"10m\"): %w", field, value, err)
		}
		return metav1.Duration{Duration: d}, nil
	}
	var err error
	if spec.ScaleDownDelay, err = parse("scaleDownDelay", a.ScaleDownDelay); err != nil {
		return nil, err
	}
	if spec.ScaleUpCooldown, err = parse("scaleUpCooldown", a.ScaleUpCooldown); err != nil {
		return nil, err
	}
	return spec, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
