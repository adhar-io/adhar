package kind

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"adhar-io/adhar/platform/utils"
)

// Local pull-through image cache for Kind.
//
// Why: `adhar up` used to `docker save` ~7 GB of "core" images from the host
// and `ctr import` them into the fresh node on EVERY run — three minutes on
// the critical path before the first CRD was even installed — and still only
// covered 26 of the ~90 images the curated core pulls, with the list going
// stale (it named external-secrets v2.5.0 while the stack ran v2.10.0). Every
// other image was pulled from the internet again on each run.
//
// Now: one small `registry` container per upstream registry runs on the host's
// `kind` network as a pull-through cache backed by a persistent volume, and
// the node's containerd is pointed at it through certs.d hosts.toml mirrors.
// The first `adhar up` populates the cache as a side effect of pulling; every
// later run pulls all ~90 images from local disk at LAN speed, with no
// save/load step at all. If a cache is down, containerd falls back to the
// upstream `server`, so it can never break a pull. The caches are stopped (not
// deleted) by `adhar down` and restarted by the next `adhar up`;
// `adhar down --purge-image-cache` (or `make clean-image-cache`) removes them
// and the volume.
//
// Docker, Podman and the nerdctl family spell every command used here the same
// way, so one code path serves whichever engine hosts the Kind nodes.

// RegistryMirror is an upstream registry the node pulls through a local cache.
type RegistryMirror struct {
	// Host is the registry as it appears in image references (quay.io).
	Host string
	// Upstream is the URL the cache proxies (https://quay.io).
	Upstream string
}

// registryMirrors are the registries the curated local core pulls from
// (measured on a running cluster: ghcr.io, docker.io, quay.io,
// registry.k8s.io, xpkg.upbound.io, reg.kyverno.io, docker.gitea.com,
// xpkg.crossplane.io) plus public.ecr.aws for the AWS-published charts.
var registryMirrors = []RegistryMirror{
	{Host: "docker.io", Upstream: "https://registry-1.docker.io"},
	{Host: "quay.io", Upstream: "https://quay.io"},
	{Host: "ghcr.io", Upstream: "https://ghcr.io"},
	{Host: "registry.k8s.io", Upstream: "https://registry.k8s.io"},
	{Host: "xpkg.upbound.io", Upstream: "https://xpkg.upbound.io"},
	{Host: "xpkg.crossplane.io", Upstream: "https://xpkg.crossplane.io"},
	{Host: "reg.kyverno.io", Upstream: "https://reg.kyverno.io"},
	{Host: "docker.gitea.com", Upstream: "https://docker.gitea.com"},
	{Host: "public.ecr.aws", Upstream: "https://public.ecr.aws"},
}

const (
	// RegistryCacheImage is the pinned distribution/registry image the caches run.
	RegistryCacheImage = "docker.io/library/registry:3.0.0"
	// RegistryCacheVolume is the engine volume every cache stores under (one
	// subdirectory per upstream host).
	RegistryCacheVolume = "adhar-registry-cache"
	// registryCacheNetwork is Kind's node network; the node resolves the cache
	// containers by name on it.
	registryCacheNetwork = "kind"
	registryCachePort    = 5000
	registryCachePrefix  = "adhar-registry-cache-"
	cacheCommandTimeout  = 2 * time.Minute
)

// ContainerName is the cache container for this upstream (a valid DNS label
// on the kind network, so containerd can dial it by name).
func (m RegistryMirror) ContainerName() string {
	return registryCachePrefix + strings.ReplaceAll(m.Host, ".", "-")
}

// MirrorURL is what the node's containerd dials for this registry.
func (m RegistryMirror) MirrorURL() string {
	return fmt.Sprintf("http://%s:%d", m.ContainerName(), registryCachePort)
}

// StorageRoot is the cache's directory inside the shared volume.
func (m RegistryMirror) StorageRoot() string {
	return "/var/lib/registry/" + m.Host
}

// HostsTOML renders the containerd certs.d host config: try the local cache
// first, fall back to the upstream server when it is unreachable.
func (m RegistryMirror) HostsTOML() string {
	return fmt.Sprintf(`# %s through the local pull-through cache (adhar); falls back to upstream.
server = %q

[host.%q]
  capabilities = ["pull", "resolve"]
`, m.Host, m.Upstream, m.MirrorURL())
}

// RunArgs is the engine `run` invocation that creates the cache container.
func (m RegistryMirror) RunArgs() []string {
	return []string{
		"run", "-d", "--name", m.ContainerName(),
		"--restart", "unless-stopped",
		"--network", registryCacheNetwork,
		"--label", "io.adhar.role=registry-cache",
		"-v", RegistryCacheVolume + ":/var/lib/registry",
		"-e", "REGISTRY_PROXY_REMOTEURL=" + m.Upstream,
		"-e", "REGISTRY_STORAGE_FILESYSTEM_ROOTDIRECTORY=" + m.StorageRoot(),
		"-e", "REGISTRY_STORAGE_DELETE_ENABLED=true",
		"-e", "REGISTRY_LOG_LEVEL=warn",
		// registry 3.x enables an OTLP trace exporter by default and logs a
		// connection error every ten seconds when nothing listens.
		"-e", "OTEL_TRACES_EXPORTER=none",
		RegistryCacheImage,
	}
}

// mirrorFor returns the mirror serving a registry host.
func mirrorFor(host string) (RegistryMirror, bool) {
	for _, m := range registryMirrors {
		if m.Host == host {
			return m, true
		}
	}
	return RegistryMirror{}, false
}

// cacheLinkPath is the file the registry writes once a tag is cached — the
// cheapest possible "is this image already local" probe. ok is false for a
// reference no mirror serves or with no tag.
func cacheLinkPath(ref string) (containerName, path string, ok bool) {
	host, repo, tag := splitImageRef(ref)
	m, found := mirrorFor(host)
	if !found || tag == "" {
		return "", "", false
	}
	return m.ContainerName(), fmt.Sprintf("%s/docker/registry/v2/repositories/%s/_manifests/tags/%s/current/link", m.StorageRoot(), repo, tag), true
}

// splitImageRef normalises an image reference the way containerd does:
// a bare name is docker.io/library/<name>, a single path segment on docker.io
// gets the library/ prefix, and a digest reference has no tag.
func splitImageRef(ref string) (host, repo, tag string) {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	first := ref
	rest := ""
	if i := strings.Index(ref, "/"); i >= 0 {
		first, rest = ref[:i], ref[i+1:]
	}
	if rest == "" || !(strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost") {
		host, rest = "docker.io", ref
	} else {
		host = first
	}
	if i := strings.LastIndex(rest, ":"); i >= 0 && !strings.Contains(rest[i:], "/") {
		rest, tag = rest[:i], rest[i+1:]
	}
	if host == "docker.io" && !strings.Contains(rest, "/") {
		rest = "library/" + rest
	}
	return host, rest, tag
}

// engineRunner executes a container-engine command and returns its combined
// output; injectable so the orchestration is unit-tested without an engine.
type engineRunner func(ctx context.Context, args ...string) (string, error)

func realEngineRunner(binary string) engineRunner {
	return func(ctx context.Context, args ...string) (string, error) {
		cctx, cancel := context.WithTimeout(ctx, cacheCommandTimeout)
		defer cancel()
		out, err := exec.CommandContext(cctx, binary, args...).CombinedOutput()
		return string(out), err
	}
}

// ensureCacheContainer brings one cache to "running on the kind network":
// a running container is reattached to the network if a teardown recreated
// it, a stopped one is started, a missing one is created; if a stale one
// cannot start it is replaced (the volume, and with it the cached layers,
// survives). Every command is idempotent so a retry is always safe.
func ensureCacheContainer(ctx context.Context, run engineRunner, m RegistryMirror) error {
	name := m.ContainerName()
	state, err := run(ctx, "container", "inspect", "-f", "{{.State.Running}}", name)
	switch {
	case err == nil && strings.TrimSpace(state) == "true":
		return ensureCacheNetwork(ctx, run, name)
	case err == nil:
		if _, err := run(ctx, "start", name); err == nil {
			return ensureCacheNetwork(ctx, run, name)
		}
		// A container created against a network that no longer exists: replace it.
		_, _ = run(ctx, "rm", "-f", name)
	}
	if out, err := run(ctx, m.RunArgs()...); err != nil {
		return fmt.Errorf("creating %s: %w (%s)", name, err, strings.TrimSpace(out))
	}
	return nil
}

// ensureCacheNetwork connects a container to the kind network when it is not
// on it (the network is deleted by `adhar down` and recreated by `kind create`).
func ensureCacheNetwork(ctx context.Context, run engineRunner, name string) error {
	nets, err := run(ctx, "container", "inspect", "-f", "{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}", name)
	if err == nil {
		for _, n := range strings.Fields(nets) {
			if n == registryCacheNetwork {
				return nil
			}
		}
	}
	if out, err := run(ctx, "network", "connect", registryCacheNetwork, name); err != nil {
		return fmt.Errorf("connecting %s to the %s network: %w (%s)", name, registryCacheNetwork, err, strings.TrimSpace(out))
	}
	return nil
}

// ensureRegistryCache starts every mirror's cache. Best effort and never
// fatal: a cache that fails to start just means that registry is pulled from
// upstream, exactly as before.
func (c *Cluster) ensureRegistryCache(ctx context.Context) {
	eng := utils.DetectContainerEngine()
	if !eng.Available {
		return
	}
	run := realEngineRunner(eng.Binary)
	started := 0
	for _, m := range registryMirrors {
		if err := ensureCacheContainer(ctx, run, m); err != nil {
			setupLog.V(1).Info("registry cache unavailable; that registry is pulled from upstream", "registry", m.Host, "error", err)
			continue
		}
		started++
	}
	setupLog.Info("local image cache ready: the node pulls through it and populates it on first use", "registries", started, "volume", RegistryCacheVolume)
}

// cacheHasImage reports whether the local cache already holds a tagged image.
func cacheHasImage(ctx context.Context, run engineRunner, ref string) bool {
	name, path, ok := cacheLinkPath(ref)
	if !ok {
		return false
	}
	_, err := run(ctx, "exec", name, "test", "-f", path)
	return err == nil
}

// StopRegistryCache stops (keeps) the cache containers so `adhar down` can
// remove the kind network; the next `adhar up` restarts them with their
// cached layers intact. With purge, the containers and the volume are removed.
func StopRegistryCache(ctx context.Context, purge bool) {
	eng := utils.DetectContainerEngine()
	if !eng.Available {
		return
	}
	stopRegistryCache(ctx, realEngineRunner(eng.Binary), purge)
}

func stopRegistryCache(ctx context.Context, run engineRunner, purge bool) {
	for _, m := range registryMirrors {
		if purge {
			_, _ = run(ctx, "rm", "-f", m.ContainerName())
		} else {
			_, _ = run(ctx, "stop", m.ContainerName())
		}
	}
	if purge {
		_, _ = run(ctx, "volume", "rm", RegistryCacheVolume)
	}
}
