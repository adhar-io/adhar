package adharplatform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"

	"sigs.k8s.io/yaml"
)

// TestStageStackRendersTemplates covers the seed-time stack staging contract:
// *.tmpl files are rendered with the platform spec (and lose the suffix), other
// files are copied verbatim (Grafana-style `{{` must survive untouched), and
// the local host convention is rewritten only for a non-default host.
func TestStageStackRendersTemplates(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "pkg/manifests/issuer.yaml.tmpl"),
		"email: {{ .ACMEEmail }}\nissuer: {{ .ACMEIssuer }}\nprovider: {{ .ExternalDNSProvider }}\nowner: {{ .TXTOwnerID }}\n{{- if .HasDNS01 }}\ndns01: true\n{{- end }}\n")
	mustWrite(t, filepath.Join(src, "pkg/manifests/dashboard.yaml"),
		"legend: \"{{instance}}\"\nurl: https://grafana.adhar.localtest.me:8443/\nhost: grafana.adhar.localtest.me\n")

	t.Run("local keeps host, renders templates", func(t *testing.T) {
		spec := v1alpha1.BuildCustomizationSpec{Protocol: "https", Host: globals.DefaultHostName, Port: "8443"}
		spec.Normalize()
		out, cleanup, err := stageStack(src, spec)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		if _, err := os.Stat(filepath.Join(out, "pkg/manifests/issuer.yaml.tmpl")); !os.IsNotExist(err) {
			t.Errorf("template suffix should be dropped in the staged stack")
		}
		got := read(t, filepath.Join(out, "pkg/manifests/issuer.yaml"))
		for _, want := range []string{"email: platform@" + globals.DefaultHostName, "issuer: adhar-selfsigned", "provider: inmemory", "owner: adhar"} {
			if !strings.Contains(got, want) {
				t.Errorf("rendered template missing %q:\n%s", want, got)
			}
		}
		if strings.Contains(got, "dns01") {
			t.Errorf("DNS-01 block must not render without a DNS provider:\n%s", got)
		}
		dash := read(t, filepath.Join(out, "pkg/manifests/dashboard.yaml"))
		if dash != "legend: \"{{instance}}\"\nurl: https://grafana.adhar.localtest.me:8443/\nhost: grafana.adhar.localtest.me\n" {
			t.Errorf("non-template file must be copied verbatim locally, got:\n%s", dash)
		}
	})

	t.Run("cloud rewrites host, renders DNS-01", func(t *testing.T) {
		spec := v1alpha1.BuildCustomizationSpec{Protocol: "https", Host: "platform.example.io", Port: "443",
			Email: "ops@example.io", DNSProvider: "digitalocean", ClusterName: "prod"}
		spec.Normalize()
		out, cleanup, err := stageStack(src, spec)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		got := read(t, filepath.Join(out, "pkg/manifests/issuer.yaml"))
		for _, want := range []string{"email: ops@example.io", "issuer: adhar-letsencrypt-dns", "provider: digitalocean", "owner: adhar-prod", "dns01: true"} {
			if !strings.Contains(got, want) {
				t.Errorf("rendered template missing %q:\n%s", want, got)
			}
		}
		dash := read(t, filepath.Join(out, "pkg/manifests/dashboard.yaml"))
		if dash != "legend: \"{{instance}}\"\nurl: https://grafana.platform.example.io/\nhost: grafana.platform.example.io\n" {
			t.Errorf("host rewrite wrong, got:\n%s", dash)
		}
	})
}

// TestStackTemplatesRender renders every *.tmpl shipped in platform/stack for
// the local and a cloud topology (with and without DNS-01) and checks the
// output is valid YAML with no directive left behind.
func TestStackTemplatesRender(t *testing.T) {
	stack, err := filepath.Abs("../../stack")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stack); err != nil {
		t.Skip("platform/stack not available")
	}
	specs := []v1alpha1.BuildCustomizationSpec{
		{Protocol: "https", Host: globals.DefaultHostName, Port: "8443"},
		{Protocol: "https", Host: "platform.example.io", Port: "443"},
		{Protocol: "https", Host: "platform.example.io", Port: "443", DNSProvider: "digitalocean", Email: "a@b.io", ClusterName: "dev"},
		{Protocol: "https", Host: "platform.example.io", Port: "443", DNSProvider: "aws", ClusterName: "dev"},
		{Protocol: "https", Host: "platform.example.io", Port: "443", DNSProvider: "gcp", ClusterName: "dev"},
		{Protocol: "https", Host: "platform.example.io", Port: "443", DNSProvider: "azure", ClusterName: "dev"},
		{Protocol: "https", Host: "platform.example.io", Port: "443", DNSProvider: "civo", ClusterName: "dev"},
		{Protocol: "https", Host: "platform.example.io", Port: "443", DNSProvider: "cloudflare", ClusterName: "dev"},
	}
	var templates []string
	_ = filepath.Walk(stack, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, stackTemplateSuffix) {
			templates = append(templates, p)
		}
		return nil
	})
	if len(templates) == 0 {
		t.Fatalf("no %s files found under %s", stackTemplateSuffix, stack)
	}
	for _, spec := range specs {
		spec.Normalize()
		out, cleanup, err := stageStack(stack, spec)
		if err != nil {
			t.Fatalf("staging stack for %+v: %v", spec, err)
		}
		for _, tpl := range templates {
			rel, _ := filepath.Rel(stack, tpl)
			rendered := read(t, filepath.Join(out, strings.TrimSuffix(rel, stackTemplateSuffix)))
			if strings.Contains(rendered, "{{") {
				t.Errorf("%s [%s/%s]: template directive survived rendering", rel, spec.Host, spec.DNSProvider)
			}
			for i, doc := range strings.Split(rendered, "\n---") {
				if strings.TrimSpace(doc) == "" {
					continue
				}
				var v any
				if err := yaml.Unmarshal([]byte(doc), &v); err != nil {
					t.Errorf("%s [%s/%s]: document %d is not valid YAML: %v", rel, spec.Host, spec.DNSProvider, i, err)
				}
			}
		}
		cleanup()
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}
