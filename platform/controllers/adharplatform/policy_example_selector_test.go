package adharplatform

// An example that names a compositionSelector must select exactly ONE Composition
// of its kind (2026-11-14). Crossplane picks among several matches arbitrarily, so
// an ambiguous selector works or fails depending on which one it picks:
// examples/compliance-policy.yaml selected on policy.adhar.io/engine: kyverno alone,
// which both compliancepolicy-local and policy-kyverno carry, and policy-kyverno's
// KCL does not compile. Mutation-tested: the example's `provider: local` removed, the
// test fails naming both Compositions.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestExampleCompositionSelectorsSelectOneComposition(t *testing.T) {
	type composition struct {
		name, kind string
		labels     map[string]any
	}
	var comps []composition
	root := filepath.Join(stackRoot(t), "..", "controlplane", "configuration", "compositions")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return err
		}
		for _, doc := range readYAMLDocs(t, path) {
			if toString(doc["kind"]) != "Composition" || !strings.HasPrefix(toString(doc["apiVersion"]), "apiextensions.crossplane.io/") {
				continue
			}
			labels, _ := dig(doc, "metadata", "labels").(map[string]any)
			comps = append(comps, composition{name: toString(metaName(doc)), kind: toString(dig(doc, "spec", "compositeTypeRef", "kind")), labels: labels})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(comps) < 50 {
		t.Fatalf("found %d Compositions; the walk is missing some", len(comps))
	}

	entries, err := os.ReadDir(examplesDir(t))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		for _, doc := range readYAMLDocs(t, filepath.Join(examplesDir(t), e.Name())) {
			sel, _ := dig(doc, "spec", "crossplane", "compositionSelector", "matchLabels").(map[string]any)
			if len(sel) == 0 {
				continue
			}
			checked++
			kind := toString(doc["kind"])
			var matched []string
			for _, c := range comps {
				if c.kind != kind {
					continue
				}
				all := true
				for k, v := range sel {
					all = all && toString(c.labels[k]) == toString(v)
				}
				if all {
					matched = append(matched, c.name)
				}
			}
			sort.Strings(matched)
			if len(matched) != 1 {
				t.Errorf("examples/%s: %s %s selects %d Compositions by %v: %v; Crossplane would pick one arbitrarily, so name a label only the intended one carries", e.Name(), kind, toString(metaName(doc)), len(matched), sel, matched)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no example names a compositionSelector; the walk is broken")
	}
}
