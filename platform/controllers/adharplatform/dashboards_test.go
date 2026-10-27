package adharplatform

// Grafana dashboard filing rules.
//
// The platform shipped 131 dashboards. Before 2026-10-09 all but two of them were
// annotated `grafana_folder: "Platform"` — a single flat folder 119 entries
// deep, which is the same as no organisation at all — while `adhar-ai` and
// falco's dashboard carried no folder annotation and landed at the root by
// accident rather than by choice.
//
// The rule now: exactly ONE dashboard sits at the root — the platform front
// page people open first. Every other dashboard is filed under one of a
// fixed set of folders. Both halves need a guard: the failure mode of the first
// is a root that silently fills up until it is as useless as the flat folder
// was, and the failure mode of the second is a dashboard that vanishes into a
// folder nobody created on purpose (Grafana creates folders on demand, so a
// typo makes a new one rather than an error).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// rootDashboards is the only dashboard allowed outside a folder, by title.
//
// Adhar AI sat here too for one day (2026-10-09). It moved into the AI folder
// on 2026-10-10: the platform page is the one front door, and the AI plane
// reads better as the head of its own folder beside agentgateway and vLLM.
var rootDashboards = map[string]bool{
	"Adhar Platform": true,
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
	"Resilience":    true, // chaos engineering (Chaos Mesh); moved out of Applications 2026-10-23
	"Applications":  true, // per-application dashboards + Application Logs — TestOnlyApplicationDashboardsUseTheApplicationsFolder
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
	if len(out) < 20 {
		t.Fatalf("found only %d dashboards under %s; this guard is not looking at the stack", len(out), root)
	}
	return out
}

// Named by the header of ai/adhar-ai/manifests/dashboard.yaml.
func TestExactlyOneDashboardSitsOutsideAFolder(t *testing.T) {
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

// ---------------------------------------------------------------------------------------
// A few great dashboards per folder, not a hundred overlapping ones (2026-10-23).
//
// 130 dashboards shipped before the aggregation: four views of Kubernetes compute, three
// of Redis, three of Kafka, five of Loki, a dozen for packages no profile enables, and
// many reading metrics this platform does not export. Each folder now holds a small set
// (build-service-dashboards.py, dash_<folder>.py), each a health row on top and one
// collapsible row per component. These guards keep it that way: the failure mode is the
// slow drift back, one "just add the community dashboard" at a time.
// ---------------------------------------------------------------------------------------

// maxDashboardsPerFolder caps every folder except Applications (one per application by
// design). A sixth dashboard in a folder belongs as a row in one of the five.
const maxDashboardsPerFolder = 5

func TestEachFolderHoldsAFewDashboards(t *testing.T) {
	byFolder := map[string][]string{}
	for _, d := range shippedDashboards(t) {
		folder := strings.TrimSpace(d.folder)
		if folder == "" || folder == "Applications" {
			continue
		}
		byFolder[folder] = append(byFolder[folder], d.title)
	}
	for folder, titles := range byFolder {
		if len(titles) > maxDashboardsPerFolder {
			sort.Strings(titles)
			t.Errorf("folder %q holds %d dashboards (at most %d): %v. Fold the new one into an existing "+
				"dashboard as a collapsible row (dash_%s.py) instead of adding a dashboard",
				folder, len(titles), maxDashboardsPerFolder, titles, strings.ToLower(folder))
		}
	}
	for folder := range dashboardFolders {
		if folder != "Applications" && len(byFolder[folder]) == 0 {
			t.Errorf("folder %q holds no dashboard — a folder in dashboardFolders that nothing is filed under", folder)
		}
	}
}

// dashboardCoverage names, for every dashboard kube-prometheus ships (always deployed,
// whatever is enabled), the packages it is about (ApplicationSet element names). A
// dashboard whose packages are disabled in BOTH profiles is a page of "No data" on every
// cluster — MySQL, MinIO, Spark, Chaos Mesh, VictoriaMetrics, Cosign, Kubescape,
// Terraform, Argo Workflows, Pyroscope, Beyla and Fluent Bit all had one until 2026-10-23.
// A dashboard shipped INSIDE a package (e.g. ai/vllm) is deployed only with its package,
// so it is exempt by construction.
var dashboardCoverage = map[string][]string{
	"k8s-cluster-overview":    {"kube-prometheus"},
	"k8s-nodes":               {"kube-prometheus"},
	"k8s-workloads":           {"kube-prometheus", "keda"},
	"k8s-control-plane":       {"kube-prometheus", "metrics-server"},
	"k8s-storage":             {"kube-prometheus"},
	"net-cilium":              {"hubble"},
	"net-edge":                {"external-dns", "cert-manager"},
	"net-traffic":             {"kube-prometheus"},
	"cloudnativepg":           {"cnpg"},
	"redis":                   {"valkey"},
	"kafka-cluster":           {"kafka-operator", "kafka-ui"},
	"rabbitmq-overview":       {"rabbitmq"},
	"data-analytics":          {"opensearch", "posthog", "open-metadata", "trino", "rustfs", "jupyterhub"},
	"delivery-gitops":         {"argo-rollout", "kargo"},
	"delivery-ci":             {"tekton", "argo-events", "harbor", "nexus", "buildpack"},
	"delivery-dev-platform":   {"crossplane", "coder", "dapr"},
	"delivery-dapr":           {"dapr"},
	"security-policy":         {"kyverno", "trivy"},
	"security-runtime":        {"falco", "tetragon"},
	"security-identity":       {"keycloak", "external-secrets", "openbao", "cert-manager"},
	"observability-telemetry": {"alloy", "faro"},
	"observability-lgtm":      {"kube-prometheus", "mimir", "loki", "tempo"},
	"observability-cost":      {"opencost"},
	"observability-jvm":       {"kube-prometheus"},
	"ai":                      {"adhar-ai", "agentgateway", "k8sgpt"},
	"resilience":              {"velero", "litmus", "k6"},
	"app-logs":                {"alloy", "loki"},
}

func TestNoDashboardServesOnlyDisabledPackages(t *testing.T) {
	enabled := map[string]bool{}
	known := map[string]bool{}
	for _, appset := range []string{"adhar-appset-local.yaml", "adhar-appset-production.yaml"} {
		for _, e := range loadAppSetElements(t, appset) {
			known[e.Name] = true
			if e.Enabled == "true" {
				enabled[e.Name] = true
			}
		}
	}
	dir := filepath.Join(stackRoot(t), "packages", "observability", "kube-prometheus", "manifests")
	files, err := filepath.Glob(filepath.Join(dir, "dashboard-*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no dashboards under %s (%v)", dir, err)
	}
	for _, f := range files {
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "dashboard-"), ".yaml")
		if name == "adhar-platform" {
			continue
		}
		pkgs, ok := dashboardCoverage[name]
		if !ok {
			t.Errorf("dashboard-%s.yaml is not in dashboardCoverage: say which packages it is about, so a "+
				"dashboard for a package no profile enables cannot slip in", name)
			continue
		}
		live := false
		for _, p := range pkgs {
			if !known[p] {
				t.Errorf("dashboard %s names package %q, which no ApplicationSet element is called", name, p)
			}
			live = live || enabled[p]
		}
		if !live {
			t.Errorf("dashboard %s is about %v, all disabled in BOTH profiles — it would read 'No data' on "+
				"every cluster. Drop it, or ship it inside the package so it deploys only when enabled", name, pkgs)
		}
	}
	for name := range dashboardCoverage {
		if _, err := os.Stat(filepath.Join(dir, "dashboard-"+name+".yaml")); err != nil {
			t.Errorf("dashboardCoverage lists %s, which kube-prometheus does not ship", name)
		}
	}
}

var dashboardLinkRef = regexp.MustCompile(`/d/([A-Za-z0-9_-]+)`)

// Every link between dashboards must land on one: a dashboard link, a panel link or a
// table data link naming a uid nothing ships is a 404 in the middle of an incident. The
// retired uids (Compute Resources / Pod, ...) live on as their successors' uids or are
// rewritten by dash_common.RETIRED; this proves no link was left behind. Tag dropdowns
// must name a tag some dashboard carries, or the dropdown is empty.
func TestEveryDashboardLinkResolves(t *testing.T) {
	uids := map[string]string{}
	tagged := map[string]bool{}
	type linkDoc struct {
		where string
		body  string
	}
	var docs []linkDoc
	for _, d := range shippedDashboards(t) {
		raw, err := os.ReadFile(d.path)
		if err != nil {
			t.Fatal(err)
		}
		dec := yaml.NewDecoder(strings.NewReader(string(raw)))
		for {
			var doc struct {
				Metadata struct {
					Name   string            `yaml:"name"`
					Labels map[string]string `yaml:"labels"`
				} `yaml:"metadata"`
				Data map[string]string `yaml:"data"`
			}
			if err := dec.Decode(&doc); err != nil {
				break
			}
			if doc.Metadata.Labels["grafana_dashboard"] != "1" {
				continue
			}
			for key, body := range doc.Data {
				if !strings.HasSuffix(key, ".json") {
					continue
				}
				var dash struct {
					UID   string   `json:"uid"`
					Tags  []string `json:"tags"`
					Links []struct {
						Type string   `json:"type"`
						Tags []string `json:"tags"`
						URL  string   `json:"url"`
					} `json:"links"`
				}
				if json.Unmarshal([]byte(body), &dash) != nil {
					continue
				}
				uids[dash.UID] = shortStackPath(d.path)
				for _, tg := range dash.Tags {
					tagged[tg] = true
				}
				docs = append(docs, linkDoc{where: shortStackPath(d.path), body: body})
				for _, l := range dash.Links {
					if l.Type == "dashboards" {
						for _, tg := range l.Tags {
							docs = append(docs, linkDoc{where: shortStackPath(d.path) + " tag-dropdown", body: "\x00tag:" + tg})
						}
					}
				}
			}
		}
	}
	// The per-application dashboards link to the front page; they are composed, not shipped.
	comp := readConfigurationFile(t, "compositions", "apps", "argocd-multienv.yaml")
	docs = append(docs, linkDoc{where: "compositions/apps/argocd-multienv.yaml", body: comp})

	checked := 0
	for _, d := range docs {
		if tag, ok := strings.CutPrefix(d.body, "\x00tag:"); ok {
			if !tagged[tag] {
				t.Errorf("%s: a dashboards dropdown lists tag %q, which no shipped dashboard carries", d.where, tag)
			}
			continue
		}
		for _, m := range dashboardLinkRef.FindAllStringSubmatch(d.body, -1) {
			uid := m[1]
			if strings.HasPrefix(uid, "app-") || uid == "app" {
				continue // per-application dashboards are composed per Application
			}
			checked++
			if _, ok := uids[uid]; !ok {
				t.Errorf("%s links to /d/%s, which no shipped dashboard has as its uid", d.where, uid)
			}
		}
	}
	if checked < 50 {
		t.Fatalf("only %d dashboard links found; this guard is not reading the dashboards", checked)
	}
}
