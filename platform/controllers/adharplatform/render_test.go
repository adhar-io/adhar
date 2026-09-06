package adharplatform

import (
	"embed"
	"io/fs"
	"strings"
	"testing"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/platform/utils/files"

	"sigs.k8s.io/yaml"
)

// TestEmbeddedFoundationRenders guards the foundation templating contract:
//
//   - every embedded foundation manifest must render to valid YAML with no
//     `{{ … }}` left behind, for BOTH the local (kind) and a cloud topology —
//     a directive that survives means a manifest is being applied raw (this is
//     exactly how argocd-server once received a literal `{{ .Host }}` URL);
//   - the Kind-only gateway.yaml is applied RAW by design, so it must be valid
//     YAML as-is and must never contain template directives (adding one broke
//     `adhar up` locally at the Cilium & Gateway stage).
func TestEmbeddedFoundationRenders(t *testing.T) {
	specs := map[string]v1alpha1.BuildCustomizationSpec{
		"local": {Protocol: "https", Host: "adhar.localtest.me", Port: "8443"},
		"cloud": {Protocol: "https", Host: "platform.example.io", Port: "443"},
	}
	for name := range specs {
		s := specs[name]
		s.Normalize()
		specs[name] = s
	}

	trees := []struct {
		fsys embed.FS
		root string
	}{
		{argoCDFS, "resources/argocd"},
		{giteaFS, "resources/gitea"},
		{gatewayFS, "resources/gateway"},
	}

	for _, tree := range trees {
		entries, err := fs.ReadDir(tree.fsys, tree.root)
		if err != nil {
			t.Fatalf("reading %s: %v", tree.root, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			path := tree.root + "/" + e.Name()
			raw, err := fs.ReadFile(tree.fsys, path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}

			// Kind gateway.yaml: raw-applied, so it must be clean YAML on its own.
			if path == "resources/gateway/gateway.yaml" {
				if strings.Contains(string(raw), "{{") {
					t.Errorf("%s is applied raw (Kind path) and must not contain template directives", path)
				}
				assertValidYAML(t, path+" (raw)", raw)
				continue
			}

			for topo, spec := range specs {
				out, err := files.ApplyTemplate(raw, spec)
				if err != nil {
					t.Errorf("%s [%s]: render error: %v", path, topo, err)
					continue
				}
				if strings.Contains(string(out), "{{") {
					t.Errorf("%s [%s]: template directive survived rendering", path, topo)
				}
				assertValidYAML(t, path+" ["+topo+"]", out)
				// Host must actually land in the output for templated files.
				if strings.Contains(string(raw), "{{ .Host }}") && !strings.Contains(string(out), spec.Host) {
					t.Errorf("%s [%s]: rendered output does not contain host %q", path, topo, spec.Host)
				}
			}
		}
	}
}

func assertValidYAML(t *testing.T, label string, data []byte) {
	t.Helper()
	for i, doc := range strings.Split(string(data), "\n---") {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var v any
		if err := yaml.Unmarshal([]byte(doc), &v); err != nil {
			t.Errorf("%s: document %d is not valid YAML: %v", label, i, err)
		}
	}
}
