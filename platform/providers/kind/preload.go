package kind

import (
	"context"
	"os"
	"os/exec"
	"time"

	"sigs.k8s.io/kind/pkg/cluster/nodes"
	"sigs.k8s.io/kind/pkg/cluster/nodeutils"

	"adhar-io/adhar/platform/utils"
)

// criticalPathImages are pulled on the CRITICAL PATH of `adhar up` — the Cilium
// CNI + Gateway data path that must be Ready before anything else proceeds. On a
// fresh Kind node these ~1GB are pulled before the CNI is Ready, dominating the
// "Cilium & Gateway" phase. Keep in sync with
// platform/controllers/adharplatform/resources/cilium/install.yaml.
//
// Everything else the platform pulls goes through the local registry cache
// (registrycache.go) and needs no host-side handling: the old ~7 GB
// "core images" save/load that ran here on every `adhar up` cost about three
// minutes before the first CRD was installed and is gone.
var criticalPathImages = []string{
	"quay.io/cilium/cilium:v1.20.0",
	"quay.io/cilium/cilium-envoy:v1.37.5-1782911245-7cffc778c923f68a77954a53b1a98d6b5353f004",
	"quay.io/cilium/operator-generic:v1.20.0",
}

// preloadImages returns every image the preloader will try to seed.
func preloadImages() []string {
	return append([]string{}, criticalPathImages...)
}

// preloadBootstrapImages seeds the critical-path images into the new node from
// the host's engine cache — but only the ones the local registry cache does
// not already hold, because a cached image arrives at LAN speed anyway and the
// save/load is the slower path. So: a warm cache skips this entirely; a cold
// cache on a host that has the images (`make preload-images`) still gets the
// Cilium phase from the host copy instead of the internet.
//
// It is deliberately:
//   - NON-FATAL: any error is logged and skipped; it can never fail cluster
//     creation (worst case: the image is pulled in-cluster as before).
//   - COLD-SAFE: images absent from the host are skipped (no pull here).
//   - BATCHED: all present images are saved to a single archive and loaded once
//     per node, which is far faster than a save+load per image.
func (c *Cluster) preloadBootstrapImages(ctx context.Context) {
	nodeList, err := c.provider.ListNodes(c.name)
	if err != nil || len(nodeList) == 0 {
		return
	}

	eng := utils.DetectContainerEngine()
	present := imagesToPreload(ctx, realEngineRunner(eng.Binary), preloadImages(), func(img string) bool { return hostHasImage(ctx, img) })
	if len(present) == 0 {
		setupLog.V(1).Info("preload: critical-path images are cached locally or absent from the host; nothing to seed")
		return
	}

	if err := loadImagesToNodes(ctx, present, nodeList); err != nil {
		setupLog.V(1).Info("preload: image load skipped", "error", err)
		return
	}
	setupLog.Info("seeded critical-path images from the host cache into the node", "count", len(present), "nodes", len(nodeList))
}

// imagesToPreload filters the candidates to those worth a save/load: absent
// from the local registry cache and present on the host.
func imagesToPreload(ctx context.Context, run engineRunner, candidates []string, hostHas func(string) bool) []string {
	var present []string
	for _, img := range candidates {
		if cacheHasImage(ctx, run, img) {
			continue
		}
		if hostHas(img) {
			present = append(present, img)
		}
	}
	return present
}

// hostHasImage reports whether the host's container engine already holds the
// image. The engine is whichever one hosts the Kind nodes -- Docker, Podman or
// a nerdctl-family engine -- not Docker unconditionally: on a Podman-only
// machine every lookup failed, so nothing was ever preloaded and the slow path
// was taken silently.
func hostHasImage(ctx context.Context, img string) bool {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return exec.CommandContext(cctx, utils.DetectContainerEngine().Binary, "image", "inspect", img).Run() == nil
}

// loadImagesToNodes saves the given images from the host's container engine to
// a single temp archive and loads it into each Kind node's containerd.
//
// `save -o <file> img…` is spelled identically by docker, podman and nerdctl,
// so one code path serves all three.
func loadImagesToNodes(ctx context.Context, images []string, nodeList []nodes.Node) error {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	tmp, err := os.CreateTemp("", "adhar-images-*.tar")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	// One `save img1 img2 ...` for all present images -> single archive.
	args := append([]string{"save", "-o", tmp.Name()}, images...)
	if err := exec.CommandContext(cctx, utils.DetectContainerEngine().Binary, args...).Run(); err != nil {
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
