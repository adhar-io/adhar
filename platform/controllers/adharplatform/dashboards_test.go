package adharplatform

// Grafana dashboard filing rules.
//
// The platform ships 131 dashboards. Before 2026-10-09 all but two of them were
// annotated `grafana_folder: "Platform"` — a single flat folder 119 entries
// deep, which is the same as no organisation at all — while `adhar-ai` and
// falco's dashboard carried no folder annotation and landed at the root by
// accident rather than by choice.
//
// The rule now: exactly TWO dashboards sit at the root, because they are the
// front doors people open first. Every other dashboard is filed under one of a
// fixed set of folders. Both halves need a guard: the failure mode of the first
// is a root that silently fills up until it is as useless as the flat folder
// was, and the failure mode of the second is a dashboard that vanishes into a
// folder nobody created on purpose (Grafana creates folders on demand, so a
// typo makes a new one rather than an error).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// rootDashboards are the only two allowed outside a folder, by dashboard title.
var rootDashboards = map[string]bool{
	"Adhar Platform": true,
	"Adhar AI":       true,
}

// dashboardFolders is the closed set. Grafana creates a folder on demand, so a
// typo does not fail anything at runtime — it just files the dashboard
// somewhere nobody looks.
var dashboardFolders = map[string]bool{
	"Kubernetes":    true,
	"Networking":    true,
	"Observability": true,
	"Security":      true,
	"Data":          true,
	"Delivery":      true,
	"AI":            true,
	"Applications":  true,
}

type dashboardDoc struct {
	path   string
	name   string
	title  string
	folder string
	hasKey bool
}

// shippedDashboards reads every dashboard ConfigMap in the stack tree.
func shippedDashboards(t *testing.T) []dashboardDoc {
	t.Helper()
	root := filepath.Join(stackRoot(t), "packages")
	var out []dashboardDoc

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// .yaml.tmpl files are rendered at seed time and are not plain YAML.
		if d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(raw), "grafana_dashboard") {
			return nil
		}
		dec := yaml.NewDecoder(strings.NewReader(string(raw)))
		for {
			var doc struct {
				Metadata struct {
					Name        string            `yaml:"name"`
					Labels      map[string]string `yaml:"labels"`
					Annotations map[string]string `yaml:"annotations"`
				} `yaml:"metadata"`
				Data map[string]string `yaml:"data"`
			}
			if err := dec.Decode(&doc); err != nil {
				break
			}
			if doc.Metadata.Labels["grafana_dashboard"] != "1" {
				continue
			}
			folder, hasKey := doc.Metadata.Annotations["grafana_folder"]
			entry := dashboardDoc{path: path, name: doc.Metadata.Name, folder: folder, hasKey: hasKey}
			for key, body := range doc.Data {
				if !strings.HasSuffix(key, ".json") {
					continue
				}
				var dash struct {
					Title string `json:"title"`
				}
				if err := json.Unmarshal([]byte(body), &dash); err != nil {
					t.Errorf("%s: %s is not valid JSON: %v", shortStackPath(path), key, err)
					continue
				}
				entry.title = dash.Title
			}
			out = append(out, entry)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	if len(out) < 100 {
		t.Fatalf("found only %d dashboards under %s; this guard is not looking at the stack", len(out), root)
	}
	return out
}

// Named by the header of ai/adhar-ai/manifests/dashboard.yaml.
func TestExactlyTwoDashboardsSitOutsideAFolder(t *testing.T) {
	var atRoot []string
	for _, d := range shippedDashboards(t) {
		if d.hasKey && strings.TrimSpace(d.folder) != "" {
			continue
		}
		atRoot = append(atRoot, d.title+" ("+shortStackPath(d.path)+")")
		if !rootDashboards[d.title] {
			t.Errorf("%s: dashboard %q has no grafana_folder, so it lands at the Grafana root. "+
				"Only the two front doors belong there (Adhar Platform, Adhar AI) — give this one a "+
				"folder from the set in dashboardFolders", shortStackPath(d.path), d.title)
		}
	}
	if len(atRoot) != len(rootDashboards) {
		sort.Strings(atRoot)
		t.Errorf("expected exactly %d dashboards at the root (%v), found %d:\n  %s",
			len(rootDashboards), sortedKeys(rootDashboards), len(atRoot), strings.Join(atRoot, "\n  "))
	}
}

func TestEveryOtherDashboardIsFiledUnderAKnownFolder(t *testing.T) {
	counts := map[string]int{}
	for _, d := range shippedDashboards(t) {
		folder := strings.TrimSpace(d.folder)
		if folder == "" {
			continue // covered by the test above
		}
		if !dashboardFolders[folder] {
			t.Errorf("%s: dashboard %q is filed under %q, which is not one of %v. Grafana creates "+
				"folders on demand, so a typo here files it somewhere nobody opens rather than failing",
				shortStackPath(d.path), d.title, folder, sortedKeys(dashboardFolders))
			continue
		}
		counts[folder]++
	}
	// A folder that ends up holding almost everything is the flat-"Platform"
	// problem coming back under a new name.
	total := 0
	for _, n := range counts {
		total += n
	}
	for folder, n := range counts {
		if total > 0 && n*2 > total {
			t.Errorf("folder %q holds %d of %d dashboards — more than half. That is the flat "+
				"119-deep \"Platform\" folder again; spread them across the folder set", folder, n, total)
		}
	}
}

func shortStackPath(p string) string {
	if i := strings.Index(p, "platform/stack/packages/"); i >= 0 {
		return p[i+len("platform/stack/packages/"):]
	}
	return filepath.Base(p)
}
