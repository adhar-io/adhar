package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// libredb-studio mirrors the credentials of every platform database into one
// ExternalSecret each, all in sync wave -2. A mirror whose source Secret does
// not exist is Degraded, and a wave waits on a Degraded resource forever: with
// open-metadata switched off in production, libredb-studio never got past wave
// -2 and had no Deployment at all for two days (GCP, 2026-10-26). A mirror for
// an OPTIONAL package's database must therefore be `adhar.io/optional`, which
// the platform's ExternalSecret health check reports Healthy while its source is
// missing; a mirror for a database that is always there must stay a gate, since
// the container reads its environment once, at start.
func TestLibreDBOptionalDatasourcesNeverHoldTheSync(t *testing.T) {
	// Source Secret -> the package that creates it. "" marks the platform's own
	// databases, present wherever Studio runs.
	owner := map[string]string{
		"gitea-postgresql":            "",
		"keycloak-config":             "",
		"console-env-vars":            "",
		"libredb-db-credentials":      "",
		"posthog-db-credentials":      "posthog",
		"metabase-db-credentials":     "metabase",
		"openmetadata-db-credentials": "open-metadata",
	}

	contract := docsOf(t, "data/libredb-studio/adhar-package.yaml")[0]
	optional := map[string]bool{}
	for _, raw := range toSlice(contract["dependencies"]) {
		dep, _ := raw.(map[string]any)
		if dep["optional"] == true {
			optional[toString(dep["name"])] = true
		}
	}

	seen := 0
	for _, es := range docsOf(t, "data/libredb-studio/manifests/datasources.yaml") {
		if es["kind"] != "ExternalSecret" {
			continue
		}
		seen++
		name := toString(sgDig(es, "metadata", "name"))
		isOptional := sgDig(es, "metadata", "annotations", "adhar.io/optional") == "true"
		sources := map[string]bool{}
		for _, raw := range toSlice(sgDig(es, "spec", "data")) {
			sources[toString(sgDig(raw, "remoteRef", "key"))] = true
		}
		for _, key := range keys(sources) {
			pkg, known := owner[key]
			switch {
			case !known:
				t.Errorf("%s reads %s, which this test does not know: add it to the owner map, "+
					"naming the package that creates it (\"\" if it always exists)", name, key)
			case pkg != "" && !optional[pkg]:
				t.Errorf("%s reads %s from %s, which adhar-package.yaml does not list as an optional "+
					"dependency; declare it, or treat it as always present", name, key, pkg)
			case pkg != "" && !isOptional:
				t.Errorf("%s mirrors %s's database without adhar.io/optional: \"true\"; with %s switched "+
					"off the mirror is Degraded and the whole package waits at its sync wave forever", name, pkg, pkg)
			case pkg == "" && isOptional:
				t.Errorf("%s mirrors an always-present database as optional; Studio could then start "+
					"before the Secret exists and never list that connection", name)
			}
		}
		ann, _ := sgDig(es, "metadata", "annotations").(map[string]any)
		if _, bogus := ann["argocd.argoproj.io/ignore-healthcheck"]; bogus {
			t.Errorf("%s still carries argocd.argoproj.io/ignore-healthcheck, which Argo CD does not implement", name)
		}
	}
	if seen == 0 {
		t.Fatal("no ExternalSecrets in data/libredb-studio/manifests/datasources.yaml")
	}

	// The annotation is only as good as the health check that reads it, in
	// both rendered Argo CD manifests and both values files they come from.
	for _, f := range []string{
		"resources/argocd/install.yaml",
		"resources/argocd/install-ha.yaml",
		"../../../hack/argocd/values.yaml",
		"../../../hack/argocd/values-ha.yaml",
	} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		i := strings.Index(string(b), "resource.customizations.health.external-secrets.io_ExternalSecret")
		if i < 0 || !strings.Contains(string(b)[i:], `annotations["adhar.io/optional"] == "true"`) {
			t.Errorf("%s: the ExternalSecret health check no longer honours adhar.io/optional", f)
		}
	}
}

// argoCDKeys are the argocd.argoproj.io/ annotation and label keys Argo CD
// implements and this stack has any use for. A key Argo CD does not know is not
// an error anywhere — it is silently ignored, which is how libredb-studio
// shipped `argocd.argoproj.io/ignore-healthcheck` for months believing it kept a
// missing optional database from holding the sync.
var argoCDKeys = map[string]bool{
	"sync-wave":               true,
	"sync-options":            true,
	"hook":                    true,
	"hook-delete-policy":      true,
	"compare-options":         true,
	"managed-by":              true,
	"tracking-id":             true,
	"refresh":                 true,
	"skip-reconcile":          true,
	"manifest-generate-paths": true,
	"application-set-refresh": true,
	"instance":                true,
	"secret-type":             true,
	"application-set-name":    true,
}

var argoCDKeyRe = regexp.MustCompile(`(?m)^\s*["']?argocd\.argoproj\.io/([A-Za-z0-9._-]+)["']?\s*:`)

func TestStackUsesOnlyArgoCDKeysArgoCDHas(t *testing.T) {
	unknown := map[string]map[string]bool{}
	err := filepath.WalkDir(stackPackagesDir(t), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !(strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".yaml.tmpl")) {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range argoCDKeyRe.FindAllStringSubmatch(string(b), -1) {
			if !argoCDKeys[m[1]] {
				rel, _ := filepath.Rel(stackPackagesDir(t), p)
				if unknown[m[1]] == nil {
					unknown[m[1]] = map[string]bool{}
				}
				unknown[m[1]][rel] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(unknown))
	for k := range unknown {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		t.Errorf("argocd.argoproj.io/%s is not a key Argo CD implements; it is ignored without a word (used in %s)",
			k, strings.Join(keys(unknown[k]), ", "))
	}
}
