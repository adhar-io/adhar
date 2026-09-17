package kind

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"adhar-io/adhar/api/v1alpha1"
)

func TestRenderRegistryCertsDirCoversGiteaHarborAndEveryMirror(t *testing.T) {
	dir, err := renderRegistryCertsDir(v1alpha1.BuildCustomizationSpec{Host: "adhar.localtest.me", Port: "8443"})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	read := func(host string) string {
		b, err := os.ReadFile(filepath.Join(dir, host, "hosts.toml"))
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		return string(b)
	}
	if got := read("gitea.adhar.localtest.me:8443"); !strings.Contains(got, `ca = "/etc/adhar/pki/tls.crt"`) || strings.Contains(got, "skip_verify = true") {
		t.Errorf("gitea host config must verify against the platform CA:\n%s", got)
	}
	if got := read(HarborCoreRegistryHost); !strings.Contains(got, HarborCorePinnedClusterIP) {
		t.Errorf("harbor host config must dial the pinned ClusterIP:\n%s", got)
	}
	for _, m := range registryMirrors {
		got := read(m.Host)
		if !strings.Contains(got, `server = "`+m.Upstream+`"`) || !strings.Contains(got, m.MirrorURL()) {
			t.Errorf("%s: mirror config must name the cache and the upstream fallback:\n%s", m.Host, got)
		}
	}
	entries, _ := os.ReadDir(dir)
	if want := 2 + len(registryMirrors); len(entries) != want {
		t.Errorf("certs.d has %d host dirs, want %d", len(entries), want)
	}
}

func TestRenderRegistryCertsDirPathRoutingUsesTheGatewayHost(t *testing.T) {
	dir, err := renderRegistryCertsDir(v1alpha1.BuildCustomizationSpec{Host: "adhar.localtest.me", Port: "8443", UsePathRouting: true})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if _, err := os.Stat(filepath.Join(dir, "adhar.localtest.me:8443", "hosts.toml")); err != nil {
		t.Errorf("path-routing mode names the gateway host itself: %v", err)
	}
}
