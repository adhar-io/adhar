package adharplatform

import (
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/yaml"
)

// Local–production parity is structural (pillar 4, ADR-0015): both
// ApplicationSets must wire the identical package set — same names,
// namespaces, categories and manifest paths — differing ONLY in enablement
// (a single Kind node cannot run the full catalog). Each environment config
// must mirror its ApplicationSet exactly. These tests keep that invariant
// from drifting.

type appSetElement struct {
	Name         string `json:"name"`
	Enabled      string `json:"enabled"`
	Namespace    string `json:"namespace"`
	Category     string `json:"category"`
	ManifestPath string `json:"manifestPath"`
}

func loadAppSetElements(t *testing.T, file string) []appSetElement {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "stack", file))
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}
	var doc struct {
		Spec struct {
			Generators []struct {
				List struct {
					Elements []appSetElement `json:"elements"`
				} `json:"list"`
			} `json:"generators"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing %s: %v", file, err)
	}
	if len(doc.Spec.Generators) == 0 || len(doc.Spec.Generators[0].List.Elements) == 0 {
		t.Fatalf("%s: no generator elements found", file)
	}
	return doc.Spec.Generators[0].List.Elements
}

func loadEnvPackages(t *testing.T, env string) []appSetElement {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "stack", "environments", env, "config.yaml"))
	if err != nil {
		t.Fatalf("reading %s config: %v", env, err)
	}
	var doc struct {
		Packages []appSetElement `json:"packages"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing %s config: %v", env, err)
	}
	return doc.Packages
}

// wiring is the identity of a package entry, everything except enablement.
func wiring(e appSetElement) [4]string {
	return [4]string{e.Name, e.Namespace, e.Category, e.ManifestPath}
}

func TestLocalProductionAppSetParity(t *testing.T) {
	local := loadAppSetElements(t, "adhar-appset-local.yaml")
	prod := loadAppSetElements(t, "adhar-appset-production.yaml")

	lset := map[[4]string]bool{}
	for _, e := range local {
		lset[wiring(e)] = true
	}
	pset := map[[4]string]bool{}
	for _, e := range prod {
		pset[wiring(e)] = true
	}
	for w := range lset {
		if !pset[w] {
			t.Errorf("wired locally but missing from production appset: %v", w)
		}
	}
	for w := range pset {
		if !lset[w] {
			t.Errorf("wired in production but missing from local appset: %v", w)
		}
	}

	// Every package enabled locally must be enabled in production too — the
	// curated local core is a subset of production, never a divergence.
	prodEnabled := map[string]bool{}
	for _, e := range prod {
		prodEnabled[e.Name] = e.Enabled == "true"
	}
	for _, e := range local {
		if e.Enabled == "true" && !prodEnabled[e.Name] {
			t.Errorf("package %q enabled locally but disabled in production — local must stay a subset", e.Name)
		}
	}

	// Every enabled package must have manifests on disk.
	for _, set := range [][]appSetElement{local, prod} {
		for _, e := range set {
			if e.Enabled != "true" {
				continue
			}
			dir := filepath.Join("..", "..", "stack", "packages", e.ManifestPath)
			if st, err := os.Stat(dir); err != nil || !st.IsDir() {
				t.Errorf("package %q enabled but manifests missing at %s", e.Name, e.ManifestPath)
			}
		}
	}
}

func TestEnvironmentConfigsMatchAppSets(t *testing.T) {
	cases := []struct {
		env    string
		appset string
	}{
		{"local", "adhar-appset-local.yaml"},
		{"production", "adhar-appset-production.yaml"},
	}
	for _, tc := range cases {
		elements := loadAppSetElements(t, tc.appset)
		packages := loadEnvPackages(t, tc.env)

		want := map[[4]string]string{}
		for _, e := range elements {
			want[wiring(e)] = e.Enabled
		}
		got := map[[4]string]string{}
		for _, p := range packages {
			got[wiring(p)] = p.Enabled
		}
		for w, enabled := range want {
			if got[w] != enabled {
				t.Errorf("%s: %v enabled=%q in appset but %q in environments/%s/config.yaml", tc.env, w[0], enabled, got[w], tc.env)
			}
		}
		if len(got) != len(want) {
			t.Errorf("%s: appset wires %d packages, config lists %d — keep them in sync", tc.env, len(want), len(got))
		}
	}
}
