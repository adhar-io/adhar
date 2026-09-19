package up

import (
	"testing"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/config"
	"context"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"time"
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

// The apps budget must not depend on how often the controller reconciles: on an
// exhausted machine a reconcile pass took ~15 minutes, so the controller's
// once-per-pass deadline check left a 12-minute budget unhonoured for over an
// hour. watchAppsBudget therefore starts its clock when convergence first
// reports progress and cancels on its own schedule.
func TestWatchAppsBudgetCancelsOnceTheBudgetIsSpent(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	pl := &v1alpha1.AdharPlatform{ObjectMeta: metav1.ObjectMeta{Name: "adhar", Namespace: globals.AdharSystemNamespace}}
	pl.Status.GitOps = &v1alpha1.GitOpsSyncStatus{ApplicationsTotal: 15, ApplicationsHealthy: 5}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pl).WithStatusSubresource(pl).Build()

	cancelled := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stop := make(chan struct{})
	defer close(stop)

	// A zero-length budget expires on the tick after the clock starts.
	go watchAppsBudget(ctx, c, "adhar", time.Nanosecond, 10*time.Millisecond, func() { close(cancelled) }, stop)
	select {
	case <-cancelled:
	case <-ctx.Done():
		t.Fatal("the watchdog never cancelled despite an expired budget")
	}
}

func TestWatchAppsBudgetLeavesAConvergedPlatformAlone(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	pl := &v1alpha1.AdharPlatform{ObjectMeta: metav1.ObjectMeta{Name: "adhar", Namespace: globals.AdharSystemNamespace}}
	pl.Status.GitOps = &v1alpha1.GitOpsSyncStatus{ApplicationsTotal: 15, ApplicationsHealthy: 15}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pl).WithStatusSubresource(pl).Build()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	stop := make(chan struct{})
	defer close(stop)
	cancelled := false
	go watchAppsBudget(ctx, c, "adhar", time.Nanosecond, 10*time.Millisecond, func() { cancelled = true }, stop)
	<-ctx.Done()
	if cancelled {
		t.Error("a fully converged platform must be left to the controller's own shutdown")
	}
}
