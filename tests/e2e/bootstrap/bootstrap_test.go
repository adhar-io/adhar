//go:build e2e

// Package bootstrap verifies the full `adhar up` bootstrap sequence end to end
// on a local Kind cluster:
//
//	cluster create → CRDs → Cilium/Gateway → ArgoCD → Gitea → GitOps repos →
//	ApplicationSet → package sync → Ready condition → CLI status
//
// Run via `make e2e`. Environment knobs:
//
//	ADHAR_E2E_SKIP_UP=1   verify an already-running platform (no up/down) —
//	                      useful for iterating on assertions without a rebuild
//	ADHAR_E2E_KEEP=1      leave the cluster running after the test
package bootstrap

import (
	"context"
	"os"
	"testing"
	"time"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/tests/e2e"

	argov1alpha1 "github.com/cnoe-io/argocd-api/api/argo/application/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// healthProbeCandidates are lightweight, dependency-free packages that make
// good convergence probes. The actual probe set is the intersection of these
// with what the local ApplicationSet really enables (see resolveHealthProbes):
// hardcoding names drifts silently — a probe for a package that is no longer
// enabled generates no Application at all, so the wait can only ever time out.
var healthProbeCandidates = []string{
	"external-secrets", "cert-manager", "metrics-server",
	"hubble", "kyverno", "valkey", "alloy",
}

// localAppSetPath is the ApplicationSet the controller applies for Kind.
const localAppSetPath = "../../../platform/stack/adhar-appset-local.yaml"

// resolveHealthProbes returns the candidates that the local ApplicationSet
// actually enables, so the probe set follows the curated core as it changes.
func resolveHealthProbes(t *testing.T) []string {
	t.Helper()

	raw, err := os.ReadFile(localAppSetPath)
	require.NoError(t, err, "reading %s", localAppSetPath)

	var doc struct {
		Spec struct {
			Generators []struct {
				List struct {
					Elements []struct {
						Name    string `json:"name"`
						Enabled string `json:"enabled"`
					} `json:"elements"`
				} `json:"list"`
			} `json:"generators"`
		} `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &doc), "parsing %s", localAppSetPath)
	require.NotEmpty(t, doc.Spec.Generators, "no generators in %s", localAppSetPath)

	enabled := map[string]bool{}
	for _, e := range doc.Spec.Generators[0].List.Elements {
		if e.Enabled == "true" {
			enabled[e.Name] = true
		}
	}

	var probes []string
	for _, c := range healthProbeCandidates {
		if enabled[c] {
			probes = append(probes, c)
		}
	}
	// An empty probe set would make this phase vacuous, so say so loudly
	// rather than passing on nothing.
	require.NotEmpty(t, probes,
		"none of the health-probe candidates %v are enabled in %s — update healthProbeCandidates",
		healthProbeCandidates, localAppSetPath)
	return probes
}

// Timeout budgets. A cold bootstrap is dominated by container image pulls for
// the whole curated core (Keycloak, Harbor, kube-prometheus, Vault, SPIRE, …):
// measured well over an hour on a laptop with an empty image cache, so the
// defaults are generous and every budget is overridable for CI tuning.
//
//	ADHAR_E2E_TIMEOUT       overall test budget (default 90m)
//	ADHAR_E2E_UP_TIMEOUT    `adhar up` budget (default 60m)
//	ADHAR_E2E_HEALTH_TIMEOUT package-health wait (default 25m)
var (
	overallTimeout = envDuration("ADHAR_E2E_TIMEOUT", 90*time.Minute)
	upTimeout      = envDuration("ADHAR_E2E_UP_TIMEOUT", 60*time.Minute)
	healthTimeout  = envDuration("ADHAR_E2E_HEALTH_TIMEOUT", 25*time.Minute)
)

// envDuration reads a Go duration string from env, falling back to def.
func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func Test_FullBootstrapSequence(t *testing.T) {
	skipUp := os.Getenv("ADHAR_E2E_SKIP_UP") != ""
	keep := os.Getenv("ADHAR_E2E_KEEP") != ""

	ctx, cancel := context.WithTimeout(context.Background(), overallTimeout)
	defer cancel()

	if !skipUp {
		t.Logf("running adhar up --recreate (full bootstrap, budget %s)", upTimeout)
		b, err := e2e.RunAdhar(ctx, upTimeout, "up", "--recreate")
		require.NoError(t, err, "adhar up failed: %s", string(b))

		if !keep {
			defer func() {
				t.Log("tearing down with adhar down")
				downCtx, downCancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer downCancel()
				out, dErr := e2e.RunAdhar(downCtx, 5*time.Minute, "down")
				if dErr != nil {
					t.Logf("adhar down failed (cluster may need manual cleanup): %v, %s", dErr, out)
				}
			}()
		}
	}

	kubeClient, err := e2e.GetKubeClient()
	require.NoError(t, err, "getting kube client for context %s", e2e.KubeContext)

	// Phase 1 — the platform CR converges: aggregate Ready condition True.
	t.Run("platform reaches Ready condition", func(t *testing.T) {
		waitCtx, waitCancel := context.WithTimeout(ctx, 10*time.Minute)
		defer waitCancel()
		if skipUp {
			// An already-running platform may predate the conditions feature;
			// require the CR to exist, and only require Ready if conditions are set.
			platforms := &v1alpha1.AdharPlatformList{}
			require.NoError(t, kubeClient.List(waitCtx, platforms, client.InNamespace(e2e.PlatformNamespace)))
			require.NotEmpty(t, platforms.Items, "no AdharPlatform resource found")
			if len(platforms.Items[0].Status.Conditions) == 0 {
				t.Skip("platform predates status conditions; skipping Ready wait")
			}
		}
		require.NoError(t, e2e.WaitForPlatformReady(waitCtx, kubeClient))
	})

	// Phase 2 — foundation components: CRDs, core deployments, Gateway wiring.
	t.Run("foundation components are up", func(t *testing.T) {
		for _, dep := range []string{e2e.ArgoCDServerDeployment, e2e.GiteaDeployment} {
			depCtx, depCancel := context.WithTimeout(ctx, 3*time.Minute)
			assert.NoError(t, e2e.WaitForDeploymentAvailable(depCtx, kubeClient, e2e.PlatformNamespace, dep), dep)
			depCancel()
		}

		// The Gateway service must exist with the pinned node ports that Kind's
		// host-port mapping depends on.
		svc := corev1.Service{}
		require.NoError(t, kubeClient.Get(ctx,
			client.ObjectKey{Namespace: e2e.PlatformNamespace, Name: e2e.GatewayService}, &svc))
		ports := map[int32]int32{}
		for _, p := range svc.Spec.Ports {
			ports[p.Port] = p.NodePort
		}
		assert.EqualValues(t, e2e.GatewayHTTPNodePort, ports[80], "HTTP node port must be pinned")
		assert.EqualValues(t, e2e.GatewayHTTPSNodePort, ports[443], "HTTPS node port must be pinned")

		// Platform CRDs are installed and serving.
		for _, list := range []client.ObjectList{&v1alpha1.GitRepositoryList{}, &v1alpha1.CustomPackageList{}} {
			assert.NoError(t, kubeClient.List(ctx, list, client.InNamespace(e2e.PlatformNamespace)))
		}

		// SPIFFE workload identity ships in the foundation (ADR-0022/P2.4):
		// the SPIRE server runs in the platform namespace.
		spire := appsv1.StatefulSet{}
		assert.NoError(t, kubeClient.Get(ctx,
			client.ObjectKey{Namespace: e2e.PlatformNamespace, Name: "spire-server"}, &spire),
			"SPIRE server must be part of the foundation")

		// ArgoCD must carry the fast CNPG health script: the bundled community
		// Lua times out under load and wedges every app that owns a database.
		argocdCM := corev1.ConfigMap{}
		require.NoError(t, kubeClient.Get(ctx,
			client.ObjectKey{Namespace: e2e.PlatformNamespace, Name: "argocd-cm"}, &argocdCM))
		assert.Contains(t, argocdCM.Data, "resource.customizations.health.postgresql.cnpg.io_Cluster",
			"argocd-cm must define CNPG Cluster health")
	})

	// Phase 3 — GitOps content: the seeded Gitea repos exist and are reachable
	// through the external (Gateway-routed) URL.
	t.Run("gitea serves the seeded platform repos", func(t *testing.T) {
		names, err := e2e.GiteaListRepoNames(ctx, kubeClient)
		require.NoError(t, err)

		found := map[string]bool{}
		for _, n := range names {
			found[n] = true
		}
		for _, want := range []string{"environments", "packages"} {
			assert.True(t, found[want], "expected gitea repo %q, have %v", want, names)
		}

		// gitea-credential must carry an admin API token: Lighthouse's oauth
		// secret and the preview-environment PR generator both read the
		// `token` key, which the chart secret does not ship on its own.
		creds := corev1.Secret{}
		require.NoError(t, kubeClient.Get(ctx,
			client.ObjectKey{Namespace: e2e.PlatformNamespace, Name: e2e.GiteaCredentialSecret}, &creds))
		assert.NotEmpty(t, creds.Data["token"], "gitea-credential must carry an API token")
	})

	// Phase 4 — ArgoCD API is reachable through the Gateway and accepts the
	// bootstrap admin credentials.
	t.Run("argocd api authenticates", func(t *testing.T) {
		token, err := e2e.ArgoCDSessionToken(ctx, kubeClient)
		require.NoError(t, err)
		assert.NotEmpty(t, token)
	})

	// Phase 5 — GitOps takes over: the ApplicationSet exists, applications are
	// generated, and representative packages converge to Healthy.
	t.Run("applicationset deploys enabled packages", func(t *testing.T) {
		appset := argov1alpha1.ApplicationSet{}
		require.NoError(t, kubeClient.Get(ctx,
			client.ObjectKey{Namespace: e2e.PlatformNamespace, Name: e2e.PlatformAppSet}, &appset))

		apps := argov1alpha1.ApplicationList{}
		require.NoError(t, kubeClient.List(ctx, &apps, client.InNamespace(e2e.PlatformNamespace)))
		assert.NotEmpty(t, apps.Items, "ApplicationSet generated no applications")

		probes := resolveHealthProbes(t)
		t.Logf("probing convergence of enabled packages: %v", probes)

		healthCtx, healthCancel := context.WithTimeout(ctx, healthTimeout)
		defer healthCancel()
		assert.NoError(t, e2e.WaitForAppsHealthy(healthCtx, kubeClient, probes))

		for i := range apps.Items {
			app := apps.Items[i]
			t.Logf("package %-25s health=%-12s sync=%s", app.Name, app.Status.Health.Status, app.Status.Sync.Status)
		}
	})

	// Phase 6 — the CLI surfaces the platform state.
	t.Run("cli reports platform status", func(t *testing.T) {
		out, err := e2e.RunAdhar(ctx, 30*time.Second, "version")
		assert.NoError(t, err, string(out))

		out, err = e2e.RunAdhar(ctx, 2*time.Minute, "get", "status")
		assert.NoError(t, err, string(out))
		assert.Contains(t, string(out), "Platform Packages", "get status should show the package dashboard")
		if !skipUp {
			assert.Contains(t, string(out), "Platform Conditions", "get status should show the conditions section")
		}
	})
}
