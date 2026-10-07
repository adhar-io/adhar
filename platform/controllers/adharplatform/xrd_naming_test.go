package adharplatform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The platform's self-service API is named after the THING, not after
// Crossplane's implementation of it: `Database`, not `CompositeDatabase`
// (renamed 2026-10-07). The prefix leaked an implementation detail into every
// manifest a team writes, and `kubectl get compositedatabases` is not a name
// anyone would choose.
//
// Renaming 30 kinds touched 228 files, and the failure mode of a partial rename
// is quiet: a Composition whose compositeTypeRef names a kind no XRD defines is
// simply never selected, so the XR is created, accepted, and never composes
// anything. These two tests are the agreement between the two halves.
func TestNoXRDKindCarriesTheCompositePrefix(t *testing.T) {
	for _, path := range xrdFiles(t) {
		for _, doc := range readYAMLDocs(t, path) {
			if doc["kind"] != "CompositeResourceDefinition" {
				continue
			}
			kind, _ := dig(doc, "spec", "names", "kind").(string)
			plural, _ := dig(doc, "spec", "names", "plural").(string)
			group, _ := dig(doc, "spec", "group").(string)
			name, _ := dig(doc, "metadata", "name").(string)

			base := filepath.Base(path)
			if strings.HasPrefix(kind, "Composite") {
				t.Errorf("%s: kind %q still carries the Composite prefix", base, kind)
			}
			if strings.HasPrefix(plural, "composite") {
				t.Errorf("%s: plural %q still carries the composite prefix", base, plural)
			}
			// The CRD name Kubernetes derives is <plural>.<group>; an XRD whose
			// metadata.name disagrees is rejected at install time.
			if want := plural + "." + group; name != want {
				t.Errorf("%s: metadata.name is %q, must be %q (<plural>.<group>)", base, name, want)
			}
		}
	}
}

// Every Composition must point at a kind some XRD actually defines, and every
// defaultCompositionRef at a Composition that exists.
func TestCompositionsAndXRDsAgreeOnKinds(t *testing.T) {
	kinds := map[string]bool{}
	defaultRefs := map[string]string{} // xrd file -> composition name
	for _, path := range xrdFiles(t) {
		for _, doc := range readYAMLDocs(t, path) {
			if doc["kind"] != "CompositeResourceDefinition" {
				continue
			}
			if kind, _ := dig(doc, "spec", "names", "kind").(string); kind != "" {
				kinds[kind] = true
			}
			if ref, _ := dig(doc, "spec", "defaultCompositionRef", "name").(string); ref != "" {
				defaultRefs[filepath.Base(path)] = ref
			}
		}
	}
	if len(kinds) == 0 {
		t.Fatal("no XRDs were read; this test is not looking at the control plane")
	}

	compositions := map[string]bool{}
	root := filepath.Join(controlPlaneDir(t), "compositions")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return err
		}
		for _, doc := range readYAMLDocs(t, path) {
			if doc["kind"] != "Composition" {
				continue
			}
			name, _ := dig(doc, "metadata", "name").(string)
			kind, _ := dig(doc, "spec", "compositeTypeRef", "kind").(string)
			compositions[name] = true
			if !kinds[kind] {
				t.Errorf("composition %s (%s) composes kind %q, which no XRD defines — "+
					"it will never be selected, so the XR is accepted and composes nothing",
					name, filepath.Base(path), kind)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking compositions: %v", err)
	}

	for xrd, ref := range defaultRefs {
		if !compositions[ref] {
			t.Errorf("%s: defaultCompositionRef names %q, which no Composition defines", xrd, ref)
		}
	}
}

func xrdFiles(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join(controlPlaneDir(t), "xrd")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".xrd.yaml") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	if len(out) == 0 {
		t.Fatalf("no XRDs under %s", dir)
	}
	return out
}
