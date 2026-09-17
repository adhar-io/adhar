package kind

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
)

// The registries the curated local core was measured to pull from on a running
// cluster; every one must be mirrored or its images bypass the cache.
var measuredRegistries = []string{"ghcr.io", "docker.io", "quay.io", "registry.k8s.io", "xpkg.upbound.io", "reg.kyverno.io", "docker.gitea.com", "xpkg.crossplane.io"}

func TestRegistryMirrorsCoverTheCuratedCoreAndAreAddressable(t *testing.T) {
	byHost := map[string]RegistryMirror{}
	names := map[string]bool{}
	label := regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	for _, m := range registryMirrors {
		byHost[m.Host] = m
		if !label.MatchString(m.ContainerName()) {
			t.Errorf("%s: container name %q is not a DNS label containerd can dial", m.Host, m.ContainerName())
		}
		if names[m.ContainerName()] {
			t.Errorf("duplicate container name %q", m.ContainerName())
		}
		names[m.ContainerName()] = true
		if !strings.HasPrefix(m.Upstream, "https://") {
			t.Errorf("%s: upstream %q must be https", m.Host, m.Upstream)
		}
		if want := "http://" + m.ContainerName() + ":5000"; m.MirrorURL() != want {
			t.Errorf("%s: MirrorURL = %q, want %q", m.Host, m.MirrorURL(), want)
		}
		if m.StorageRoot() != "/var/lib/registry/"+m.Host {
			t.Errorf("%s: StorageRoot = %q", m.Host, m.StorageRoot())
		}
	}
	for _, h := range measuredRegistries {
		if _, ok := byHost[h]; !ok {
			t.Errorf("registry %s is pulled by the curated core but has no mirror", h)
		}
	}
	if byHost["docker.io"].Upstream != "https://registry-1.docker.io" {
		t.Errorf("docker.io must proxy registry-1.docker.io, got %q", byHost["docker.io"].Upstream)
	}
}

func TestHostsTOMLTriesTheCacheAndFallsBackToUpstream(t *testing.T) {
	m, _ := mirrorFor("quay.io")
	toml := m.HostsTOML()
	for _, want := range []string{`server = "https://quay.io"`, `[host."http://adhar-registry-cache-quay-io:5000"]`, `capabilities = ["pull", "resolve"]`} {
		if !strings.Contains(toml, want) {
			t.Errorf("hosts.toml lacks %q:\n%s", want, toml)
		}
	}
	if strings.Contains(toml, "skip_verify = true") {
		t.Error("the mirror config must never disable TLS verification")
	}
}

func TestRunArgsPinImageVolumeNetworkAndPerHostRoot(t *testing.T) {
	m, _ := mirrorFor("ghcr.io")
	args := strings.Join(m.RunArgs(), " ")
	for _, want := range []string{
		"run -d --name adhar-registry-cache-ghcr-io", "--restart unless-stopped", "--network kind",
		"-v adhar-registry-cache:/var/lib/registry", "REGISTRY_PROXY_REMOTEURL=https://ghcr.io",
		"REGISTRY_STORAGE_FILESYSTEM_ROOTDIRECTORY=/var/lib/registry/ghcr.io", "OTEL_TRACES_EXPORTER=none", RegistryCacheImage,
	} {
		if !strings.Contains(args, want) {
			t.Errorf("run args lack %q: %s", want, args)
		}
	}
	if !strings.HasSuffix(args, RegistryCacheImage) {
		t.Errorf("image must be the last argument: %s", args)
	}
	if !regexp.MustCompile(`:\d+\.\d+\.\d+$`).MatchString(RegistryCacheImage) {
		t.Errorf("cache image must be pinned to an exact version: %s", RegistryCacheImage)
	}
}

func TestSplitImageRefNormalisesLikeContainerd(t *testing.T) {
	cases := []struct{ ref, host, repo, tag string }{
		{"busybox:1.36", "docker.io", "library/busybox", "1.36"},
		{"hashicorp/vault:1.21.2", "docker.io", "hashicorp/vault", "1.21.2"},
		{"docker.io/bitnami/valkey:latest", "docker.io", "bitnami/valkey", "latest"},
		{"quay.io/cilium/cilium:v1.20.0", "quay.io", "cilium/cilium", "v1.20.0"},
		{"registry.k8s.io/pause:3.10", "registry.k8s.io", "pause", "3.10"},
		{"ghcr.io/sigstore/policy-controller/policy-controller@sha256:0bcd", "ghcr.io", "sigstore/policy-controller/policy-controller", ""},
		{"ghcr.io/tektoncd/pipeline/controller:v0.65.0@sha256:96cf", "ghcr.io", "tektoncd/pipeline/controller", "v0.65.0"},
		{"localhost:5000/a/b:t", "localhost:5000", "a/b", "t"},
		{"harbor-core.adhar-system.svc.cluster.local/team/app:abc", "harbor-core.adhar-system.svc.cluster.local", "team/app", "abc"},
	}
	for _, c := range cases {
		h, r, tg := splitImageRef(c.ref)
		if h != c.host || r != c.repo || tg != c.tag {
			t.Errorf("splitImageRef(%q) = (%q,%q,%q), want (%q,%q,%q)", c.ref, h, r, tg, c.host, c.repo, c.tag)
		}
	}
}

func TestCacheLinkPathLocatesTheRegistryTagLink(t *testing.T) {
	name, path, ok := cacheLinkPath("quay.io/cilium/cilium:v1.20.0")
	if !ok || name != "adhar-registry-cache-quay-io" || path != "/var/lib/registry/quay.io/docker/registry/v2/repositories/cilium/cilium/_manifests/tags/v1.20.0/current/link" {
		t.Errorf("unexpected (%q,%q,%v)", name, path, ok)
	}
	if _, _, ok := cacheLinkPath("harbor-core.adhar-system.svc.cluster.local/x/y:z"); ok {
		t.Error("a registry with no mirror must not report a cache path")
	}
	if _, _, ok := cacheLinkPath("ghcr.io/x/y@sha256:abc"); ok {
		t.Error("a digest reference has no tag link")
	}
}

// fakeEngine records commands and answers them from a script keyed by the
// first words of the command.
type fakeEngine struct {
	calls   []string
	answers map[string]struct {
		out string
		err error
	}
}

func (f *fakeEngine) run(_ context.Context, args ...string) (string, error) {
	cmd := strings.Join(args, " ")
	f.calls = append(f.calls, cmd)
	for prefix, a := range f.answers {
		if strings.HasPrefix(cmd, prefix) {
			return a.out, a.err
		}
	}
	return "", nil
}

func answer(out string, err error) struct {
	out string
	err error
} {
	return struct {
		out string
		err error
	}{out, err}
}

func TestEnsureCacheContainerIsIdempotentWhenRunningOnTheNetwork(t *testing.T) {
	m, _ := mirrorFor("quay.io")
	f := &fakeEngine{answers: map[string]struct {
		out string
		err error
	}{
		"container inspect -f {{.State.Running}}": answer("true\n", nil),
		"container inspect -f {{range":            answer("bridge kind \n", nil),
	}}
	if err := ensureCacheContainer(context.Background(), f.run, m); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "run ") || strings.HasPrefix(c, "start ") || strings.HasPrefix(c, "network connect") {
			t.Errorf("a running, attached cache must not be touched; got %q", c)
		}
	}
}

func TestEnsureCacheContainerReattachesAfterTheNetworkWasRecreated(t *testing.T) {
	m, _ := mirrorFor("quay.io")
	f := &fakeEngine{answers: map[string]struct {
		out string
		err error
	}{
		"container inspect -f {{.State.Running}}": answer("true", nil),
		"container inspect -f {{range":            answer("bridge", nil),
	}}
	if err := ensureCacheContainer(context.Background(), f.run, m); err != nil {
		t.Fatal(err)
	}
	if !contains(f.calls, "network connect kind "+m.ContainerName()) {
		t.Errorf("expected a network connect; calls: %v", f.calls)
	}
}

func TestEnsureCacheContainerStartsAStoppedCache(t *testing.T) {
	m, _ := mirrorFor("ghcr.io")
	f := &fakeEngine{answers: map[string]struct {
		out string
		err error
	}{
		"container inspect -f {{.State.Running}}": answer("false", nil),
		"container inspect -f {{range":            answer("kind", nil),
	}}
	if err := ensureCacheContainer(context.Background(), f.run, m); err != nil {
		t.Fatal(err)
	}
	if !contains(f.calls, "start "+m.ContainerName()) || containsPrefix(f.calls, "run ") {
		t.Errorf("a stopped cache is started, never recreated; calls: %v", f.calls)
	}
}

func TestEnsureCacheContainerCreatesAMissingCache(t *testing.T) {
	m, _ := mirrorFor("ghcr.io")
	f := &fakeEngine{answers: map[string]struct {
		out string
		err error
	}{
		"container inspect": answer("Error: No such container", errors.New("exit 1")),
	}}
	if err := ensureCacheContainer(context.Background(), f.run, m); err != nil {
		t.Fatal(err)
	}
	if !contains(f.calls, strings.Join(m.RunArgs(), " ")) {
		t.Errorf("expected the run invocation; calls: %v", f.calls)
	}
}

func TestEnsureCacheContainerReplacesAStaleCacheThatCannotStart(t *testing.T) {
	m, _ := mirrorFor("docker.io")
	f := &fakeEngine{answers: map[string]struct {
		out string
		err error
	}{
		"container inspect -f {{.State.Running}}": answer("false", nil),
		"start ": answer("network kind not found", errors.New("exit 1")),
	}}
	if err := ensureCacheContainer(context.Background(), f.run, m); err != nil {
		t.Fatal(err)
	}
	if !contains(f.calls, "rm -f "+m.ContainerName()) || !containsPrefix(f.calls, "run ") {
		t.Errorf("a cache that cannot start is removed and recreated; calls: %v", f.calls)
	}
}

func TestEnsureCacheContainerReportsCreateFailureByName(t *testing.T) {
	m, _ := mirrorFor("docker.io")
	f := &fakeEngine{answers: map[string]struct {
		out string
		err error
	}{
		"container inspect": answer("", errors.New("no such container")),
		"run ":              answer("Unable to find image", errors.New("exit 125")),
	}}
	err := ensureCacheContainer(context.Background(), f.run, m)
	if err == nil || !strings.Contains(err.Error(), m.ContainerName()) || !strings.Contains(err.Error(), "Unable to find image") {
		t.Errorf("error must name the container and carry the engine output; got %v", err)
	}
}

func TestStopRegistryCacheStopsOrPurges(t *testing.T) {
	f := &fakeEngine{}
	stopRegistryCache(context.Background(), f.run, false)
	if len(f.calls) != len(registryMirrors) || containsPrefix(f.calls, "rm ") || containsPrefix(f.calls, "volume") {
		t.Errorf("stop must only stop each cache; calls: %v", f.calls)
	}
	f = &fakeEngine{}
	stopRegistryCache(context.Background(), f.run, true)
	if !contains(f.calls, "volume rm "+RegistryCacheVolume) || !contains(f.calls, "rm -f adhar-registry-cache-quay-io") {
		t.Errorf("purge must remove the containers and the volume; calls: %v", f.calls)
	}
}

func TestCacheHasImageProbesTheTagLink(t *testing.T) {
	f := &fakeEngine{answers: map[string]struct {
		out string
		err error
	}{"exec adhar-registry-cache-quay-io test -f": answer("", nil)}}
	if !cacheHasImage(context.Background(), f.run, "quay.io/cilium/cilium:v1.20.0") {
		t.Error("expected the cached image to be reported present")
	}
	f = &fakeEngine{answers: map[string]struct {
		out string
		err error
	}{"exec": answer("", errors.New("exit 1"))}}
	if cacheHasImage(context.Background(), f.run, "quay.io/cilium/cilium:v1.20.0") {
		t.Error("a missing tag link means not cached")
	}
	if cacheHasImage(context.Background(), f.run, "harbor-core.adhar-system.svc.cluster.local/x/y:z") {
		t.Error("an unmirrored registry is never reported cached")
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func containsPrefix(list []string, prefix string) bool {
	for _, s := range list {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}
