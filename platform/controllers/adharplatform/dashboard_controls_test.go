package adharplatform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// Grafana already shows a dashboard's folder in the tree, the breadcrumb and
// every search result, so "Data / Valkey Cache" repeated it and pushed the part
// that differs off narrow lists. And a link bar of folder dropdowns plus
// "related" links wrapped the controls onto a third row (2026-10-24). These pin
// both, for the built platform dashboards and the composed application ones.

var folderPrefix = regexp.MustCompile(`^(AI|Applications|Data|Delivery|Kubernetes|Networking|Observability|Resilience|Security)\s*/`)

func TestPlatformDashboardsHavePlainTitlesAndACompactLinkBar(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("..", "..", "stack", "packages", "observability", "kube-prometheus", "manifests", "dashboard-*.yaml"))
	if len(files) < 20 {
		t.Fatalf("found %d dashboard manifests; the glob no longer matches the generator's output", len(files))
	}
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		for _, doc := range strings.Split(string(raw), "\n---\n") {
			var cm struct {
				Data map[string]string `json:"data"`
			}
			if yaml.Unmarshal([]byte(doc), &cm) != nil {
				continue
			}
			for k, v := range cm.Data {
				if !strings.HasSuffix(k, ".json") {
					continue
				}
				var d struct {
					Title string            `json:"title"`
					Links []json.RawMessage `json:"links"`
				}
				if err := json.Unmarshal([]byte(v), &d); err != nil {
					t.Fatalf("%s/%s: %v", filepath.Base(f), k, err)
				}
				if folderPrefix.MatchString(d.Title) {
					t.Errorf("%s: title %q repeats its folder", filepath.Base(f), d.Title)
				}
				if len(d.Links) > 3 {
					t.Errorf("%s (%s): %d links; more than three wraps the controls onto another row", filepath.Base(f), d.Title, len(d.Links))
				}
			}
		}
	}
}

func TestApplicationDashboardsHaveACompactLinkBar(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "controlplane", "configuration", "compositions", "apps", "argocd-multienv.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range []string{"$appLinksHost", "$appLinks"} {
		m := regexp.MustCompile(`\{\{- ` + regexp.QuoteMeta(block) + ` := ` + "`" + `(\[[\s\S]*?\])` + "`").FindStringSubmatch(string(raw))
		if m == nil {
			t.Fatalf("%s not found in the composition", block)
		}
		var links []map[string]any
		if err := json.Unmarshal([]byte(m[1]), &links); err != nil {
			t.Fatalf("%s: %v", block, err)
		}
		if len(links) > 5 {
			t.Errorf("%s: %d links; an application's controls wrap past two rows", block, len(links))
		}
	}
}

func TestATLSCacheNamesItsCertificateIssuer(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "controlplane", "configuration", "compositions", "cache", "local-valkey.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// Without an issuer the operator's Certificate is rejected and the cache never
	// starts — while the XR reads Ready, so nothing says why it is not monitored.
	if !regexp.MustCompile(`\{\{- if \$tls \}\}[\s\S]{0,900}certIssuer: adhar-selfsigned\s+certIssuerType: ClusterIssuer`).Match(raw) {
		t.Error("a tls: true cache does not name the adhar-selfsigned ClusterIssuer")
	}
}
