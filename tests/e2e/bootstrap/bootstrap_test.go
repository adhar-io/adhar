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
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// healthProbePackages are curated-core packages whose ArgoCD Applications must
// reach Healthy — lightweight, dependency-free representatives of the stack.
var healthProbePackages = []string{"cert-manager", "external-secrets", "metrics-server"}

func Test_FullBootstrapSequence(t *testing.T) {
	skipUp := os.Getenv("ADHAR_E2E_SKIP_UP") != ""
	keep := os.Getenv("ADHAR_E2E_KEEP") != ""

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	if !skipUp {
		t.Log("running adhar up --recreate (full bootstrap)")
		b, err := e2e.RunAdhar(ctx, 15*time.Minute, "up", "--recreate")
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

		healthCtx, healthCancel := context.WithTimeout(ctx, 8*time.Minute)
		defer healthCancel()
		assert.NoError(t, e2e.WaitForAppsHealthy(healthCtx, kubeClient, healthProbePackages))

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
