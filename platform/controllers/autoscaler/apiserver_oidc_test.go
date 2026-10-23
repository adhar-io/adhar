package autoscaler

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"

	"adhar-io/adhar/globals"
	pfactory "adhar-io/adhar/platform/providers"
)

// A fresh DigitalOcean cluster came up with no OIDC trust at all: `adhar up`
// tries once, and Keycloak was not serving yet (2026-10-23). The manager must
// finish the job, retrying until it succeeds, and then stop.
func oidcReconciler(t *testing.T, cm *corev1.ConfigMap, calls *int, fail *bool, now *time.Time) *Reconciler {
	t.Helper()
	r := newReconciler(t, nil, cm)
	r.RESTConfig = &rest.Config{Host: "https://example.invalid"}
	r.Now = func() time.Time { return *now }
	r.ProviderFor = func(context.Context, *clusterSpec) (pfactory.Provider, error) { return nil, nil }
	r.EnableOIDC = func(ctx context.Context, _ pfactory.Provider, clusterName string, _ *rest.Config) (string, error) {
		*calls++
		if *fail {
			return "", errors.New("Keycloak's HTTPRoute is not there yet")
		}
		// What EnableClusterOIDCFromREST does on success: record the issuer.
		live := &corev1.ConfigMap{}
		if err := r.Get(ctx, types.NamespacedName{Name: globals.ClusterSpecConfigMapName, Namespace: globals.AdharSystemNamespace}, live); err != nil {
			return "", err
		}
		live.Data[pfactory.APIServerOIDCMarkerKey] = "https://keycloak.example/realms/adhar"
		return "configured", r.Update(ctx, live)
	}
	return r
}

func TestTheManagerFinishesTheAPIServerOIDCTrustAndThenStops(t *testing.T) {
	calls, fail, now := 0, true, testNow
	r := oidcReconciler(t, clusterSpecConfigMap(), &calls, &fail, &now)

	r.ensureAPIServerOIDC(context.Background())
	if calls != 1 {
		t.Fatalf("first tick should attempt once, got %d", calls)
	}
	// Keycloak not serving: no hammering before the retry interval.
	now = now.Add(oidcRetryInterval / 2)
	r.ensureAPIServerOIDC(context.Background())
	if calls != 1 {
		t.Fatalf("retried inside the interval (%d attempts)", calls)
	}
	// Keycloak up: the next attempt after the interval succeeds...
	fail = false
	now = now.Add(oidcRetryInterval)
	r.ensureAPIServerOIDC(context.Background())
	if calls != 2 {
		t.Fatalf("did not retry after the interval (%d attempts)", calls)
	}
	// ...and once recorded, it never runs again.
	now = now.Add(24 * time.Hour)
	r.ensureAPIServerOIDC(context.Background())
	if calls != 2 {
		t.Fatalf("ran again after success (%d attempts)", calls)
	}
}

func TestTheAPIServerOIDCTrustIsLeftAloneWhereItIsNotOurs(t *testing.T) {
	for _, mode := range []string{"managed", "provided"} {
		calls, fail, now := 0, false, testNow
		cm := clusterSpecConfigMap()
		cm.Data["clusterMode"] = mode
		r := oidcReconciler(t, cm, &calls, &fail, &now)
		r.ensureAPIServerOIDC(context.Background())
		if calls != 0 {
			t.Errorf("clusterMode %s: configured a control plane the platform does not own", mode)
		}
	}
	calls, fail, now := 0, false, testNow
	cm := clusterSpecConfigMap()
	cm.Data[pfactory.APIServerOIDCMarkerKey] = "https://keycloak.example/realms/adhar"
	r := oidcReconciler(t, cm, &calls, &fail, &now)
	r.ensureAPIServerOIDC(context.Background())
	if calls != 0 {
		t.Error("re-ran although adhar-cluster-spec records the trust as done")
	}
}

// Wired into every tick, before the autoscaling gate (Headlamp needs it
// whether or not the cluster autoscales).
func TestEveryTickEnsuresTheAPIServerOIDCTrust(t *testing.T) {
	calls, fail, now := 0, false, testNow
	r := oidcReconciler(t, clusterSpecConfigMap(), &calls, &fail, &now)
	tick(t, r) // no AdharPlatform: autoscaling cannot run, the trust still must
	if calls != 1 {
		t.Fatalf("a tick did not attempt the OIDC trust (%d attempts)", calls)
	}
}
