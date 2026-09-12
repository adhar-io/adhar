package up

import (
	"testing"

	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/config"
)

// The Kubernetes version an environment ends up with has a strict precedence:
// an explicitly typed --kube-version, then the environment's own clusterConfig,
// then the platform default. These tests pin that order, because getting it
// wrong silently provisions a different Kubernetes than the operator asked for.
func TestApplyKubeVersionOverride(t *testing.T) {
	t.Cleanup(func() {
		kubeVersionExplicit = false
		kubeVersion = globals.DefaultKubernetesVersion
	})

	t.Run("flag not given leaves the environment's pin alone", func(t *testing.T) {
		kubeVersionExplicit = false
		kubeVersion = globals.DefaultKubernetesVersion
		env := &config.ResolvedEnvironmentConfig{
			ResolvedClusterConfig: []config.KeyValueConfig{{Key: "kubeVersion", Value: "v1.36.4"}},
		}
		applyKubeVersionOverride(env)
		if got := env.ResolvedClusterConfig[0].Value; got != "v1.36.4" {
			t.Fatalf("environment pin was overwritten: got %q", got)
		}
	})

	t.Run("explicit flag replaces the environment's pin", func(t *testing.T) {
		kubeVersionExplicit = true
		kubeVersion = "v1.37.0"
		env := &config.ResolvedEnvironmentConfig{
			ResolvedClusterConfig: []config.KeyValueConfig{{Key: "version", Value: "v1.36.4"}},
		}
		applyKubeVersionOverride(env)
		if got := env.ResolvedClusterConfig[0].Value; got != "v1.37.0" {
			t.Fatalf("explicit flag ignored: got %q", got)
		}
	})

	t.Run("explicit flag is added when the environment pins nothing", func(t *testing.T) {
		kubeVersionExplicit = true
		kubeVersion = "v1.37.0"
		env := &config.ResolvedEnvironmentConfig{}
		applyKubeVersionOverride(env)
		if len(env.ResolvedClusterConfig) != 1 ||
			env.ResolvedClusterConfig[0].Key != "kubeVersion" ||
			env.ResolvedClusterConfig[0].Value != "v1.37.0" {
			t.Fatalf("flag not applied: %+v", env.ResolvedClusterConfig)
		}
	})

	t.Run("platform default is the 1.37 series", func(t *testing.T) {
		if got := globals.DefaultKubernetesVersion; got != "v1.37.0" {
			t.Fatalf("platform default moved to %q; update the docs and the kubeadm minor guard with it", got)
		}
	})
}
