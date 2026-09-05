package kind

import (
	"context"
	"os"
	"os/exec"
	"time"

	"sigs.k8s.io/kind/pkg/cluster/nodes"
	"sigs.k8s.io/kind/pkg/cluster/nodeutils"
)

// criticalPathImages are pulled on the CRITICAL PATH of `adhar up` — the Cilium
// CNI + Gateway data path that must be Ready before anything else proceeds. On a
// fresh Kind node these ~1GB are pulled from the internet before the CNI is
// Ready, dominating the "Cilium & Gateway" phase. Keep in sync with
// platform/controllers/adharplatform/resources/cilium/install.yaml.
var criticalPathImages = []string{
	"quay.io/cilium/cilium:v1.20.0",
	"quay.io/cilium/cilium-envoy:v1.37.5-1782911245-7cffc778c923f68a77954a53b1a98d6b5353f004",
	"quay.io/cilium/operator-generic:v1.20.0",
}

// coreImages are the heavy, early platform components whose readiness gates most
// other apps (databases, secrets, SSO, observability). Preloading them from the
// host cache lets the GitOps sync bring the platform to health far sooner — the
// difference between "pull ~5GB from the internet" and "copy from host". Keep in
// sync with the bootstrap installs + the enabled local packages.
var coreImages = []string{
	// adhar-console — the platform's own UI, which should be reachable as soon as
	// the GitOps sync starts. It is imagePullPolicy: Always (tracks :latest), so
	// preloading turns its first pod start from a full layer download into a quick
	// digest check; busybox is its CA-bundle init container. Its data deps (CNPG,
	// ESO, ArgoCD, Gitea, Keycloak) are all preloaded below, so the whole console
	// dependency chain comes up from cache.
	"ghcr.io/adhar-io/adhar-console:latest",
	"busybox:1.36",
	// GitOps engine + git server (bootstrap). Gitea ships its own PostgreSQL +
	// Valkey (bitnami subcharts) — preload those too or Gitea's DB/cache pull on
	// cold start and dominate the "Gitea" stage of `adhar up`.
	"quay.io/argoproj/argocd:v3.5.1",
	"docker.gitea.com/gitea:1.27.0-rootless",
	"docker.io/bitnami/postgresql:latest",
	"docker.io/bitnami/valkey:latest",
	// Databases: CNPG operator + the Postgres image its Clusters run.
	"ghcr.io/cloudnative-pg/cloudnative-pg:1.30.0",
	"ghcr.io/cloudnative-pg/postgresql:16-bookworm",
	// Secrets + SSO: ESO, Vault, Keycloak — the auth/secret backbone many apps wait on.
	"ghcr.io/external-secrets/external-secrets:v2.5.0",
	"hashicorp/vault:1.21.2",
	"hashicorp/vault-k8s:1.7.2",
	"quay.io/keycloak/keycloak:26.7.1",
	// Observability core (kube-prometheus stack + Hubble relay/UI GitOps package).
	"docker.io/grafana/grafana:13.1.3",
	"quay.io/prometheus/prometheus:v3.4.0",
	"quay.io/prometheus-operator/prometheus-operator:v0.93.0",
	"quay.io/prometheus/alertmanager:v0.28.1",
	"quay.io/prometheus/node-exporter:v1.12.1-distroless",
	"registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.19.1",
	"quay.io/cilium/hubble-relay:v1.20.0",
	"quay.io/cilium/hubble-ui:v0.13.5",
	"quay.io/cilium/hubble-ui-backend:v0.13.5",
}

// preloadImages returns every image the preloader will try to seed.
func preloadImages() []string {
	return append(append([]string{}, criticalPathImages...), coreImages...)
}

// preloadBootstrapImages best-effort loads any preloadImages() already present in
// the host Docker daemon into the cluster's Kind nodes, so both the critical
// Cilium/Gateway phase AND the subsequent GitOps sync of the core components
// start from the node cache instead of pulling from the internet — the main
// lever for getting the whole platform healthy quickly on repeat runs.
//
// It is deliberately:
//   - NON-FATAL: any error is logged and skipped; it can never fail cluster
//     creation (worst case: the image is pulled in-cluster as before).
//   - COLD-SAFE: images absent from the host are skipped (no pull here), so it
//     never slows a first run on a machine with an empty Docker cache. Populate
//     the cache once with `make preload-images` to unlock the speed-up.
//   - BATCHED: all present images are saved to a single archive and loaded once
//     per node, which is far faster than a save+load per image.
func (c *Cluster) preloadBootstrapImages(ctx context.Context) {
	nodeList, err := c.provider.ListNodes(c.name)
	if err != nil || len(nodeList) == 0 {
		return
	}

	var present []string
	for _, img := range preloadImages() {
		if hostHasImage(ctx, img) {
			present = append(present, img)
		}
	}
	if len(present) == 0 {
		setupLog.V(1).Info("preload: no bootstrap/core images in host cache; skipping (run 'make preload-images' to warm it)")
		return
	}

	if err := loadImagesToNodes(ctx, present, nodeList); err != nil {
		setupLog.V(1).Info("preload: image load skipped", "error", err)
		return
	}
	setupLog.Info("preloaded platform images from host cache; Cilium & Gateway and core-component readiness will be much faster",
		"count", len(present), "nodes", len(nodeList))
}

// hostHasImage reports whether the host Docker daemon already holds the image.
func hostHasImage(ctx context.Context, img string) bool {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return exec.CommandContext(cctx, "docker", "image", "inspect", img).Run() == nil
}

// loadImagesToNodes saves the given images from the host Docker daemon to a
// single temp archive and loads it into each Kind node's containerd.
func loadImagesToNodes(ctx context.Context, images []string, nodeList []nodes.Node) error {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	tmp, err := os.CreateTemp("", "adhar-images-*.tar")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	// One `docker save img1 img2 ...` for all present images -> single archive.
	args := append([]string{"save", "-o", tmp.Name()}, images...)
	if err := exec.CommandContext(cctx, "docker", args...).Run(); err != nil {
		return err
	}

	for _, n := range nodeList {
		f, err := os.Open(tmp.Name())
		if err != nil {
			return err
		}
		err = nodeutils.LoadImageArchive(n, f)
		_ = f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
