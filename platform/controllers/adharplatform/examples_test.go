package adharplatform

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The shipped examples are the first thing anyone copies, and a field that is
// spelled wrong in one is invisible: the API server PRUNES unknown fields
// instead of rejecting them, so `kubectl apply` succeeds, the object comes back
// without the setting, and the platform quietly does something else. (Writing
// these examples produced exactly that: `nodeGroups`/`desiredSize` on a
// Cluster, where the schema says `nodePools`/`count`.)
//
// So the examples are checked against the real schemas: every XRD under
// platform/controlplane/configuration/xrd and every platform CRD under
// platform/controllers/resources.

const examplesAPIGroup = "platform.adhar.io/"

func examplesDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "examples"))
	if err != nil {
		t.Fatalf("resolving examples dir: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("examples dir %s: %v", dir, err)
	}
	return dir
}

// schemaForKind is the `spec` schema of every object the platform defines,
// keyed by kind, read from the XRDs and the CRDs themselves.
func schemaForKind(t *testing.T) map[string]map[string]interface{} {
	t.Helper()
	out := map[string]map[string]interface{}{}

	xrdDir := filepath.Join(controlPlaneDir(t), "xrd")
	entries, err := os.ReadDir(xrdDir)
	if err != nil {
		t.Fatalf("reading %s: %v", xrdDir, err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".xrd.yaml") {
			continue
		}
		for _, doc := range readYAMLDocs(t, filepath.Join(xrdDir, e.Name())) {
			if doc["kind"] != "CompositeResourceDefinition" {
				continue
			}
			kind, _ := dig(doc, "spec", "names", "kind").(string)
			versions, _ := dig(doc, "spec", "versions").([]interface{})
			if kind == "" || len(versions) == 0 {
				t.Fatalf("%s: XRD has no kind or no versions", e.Name())
			}
			v, _ := versions[0].(map[string]interface{})
			spec, _ := dig(v, "schema", "openAPIV3Schema", "properties", "spec").(map[string]interface{})
			if spec == nil {
				t.Fatalf("%s: XRD %s has no spec schema", e.Name(), kind)
			}
			out[kind] = spec
		}
	}

	crdDir, err := filepath.Abs(filepath.Join("..", "resources"))
	if err != nil {
		t.Fatalf("resolving CRD dir: %v", err)
	}
	crds, err := os.ReadDir(crdDir)
	if err != nil {
		t.Fatalf("reading %s: %v", crdDir, err)
	}
	for _, e := range crds {
		if !strings.HasPrefix(e.Name(), "platform.adhar.io_") {
			continue
		}
		for _, doc := range readYAMLDocs(t, filepath.Join(crdDir, e.Name())) {
			if doc["kind"] != "CustomResourceDefinition" {
				continue
			}
			kind, _ := dig(doc, "spec", "names", "kind").(string)
			versions, _ := dig(doc, "spec", "versions").([]interface{})
			if kind == "" || len(versions) == 0 {
				continue
			}
			v, _ := versions[0].(map[string]interface{})
			spec, _ := dig(v, "schema", "openAPIV3Schema", "properties", "spec").(map[string]interface{})
			if spec != nil {
				out[kind] = spec
			}
		}
	}
	return out
}

// exampleObjects returns every platform object in examples/, by kind.
func exampleObjects(t *testing.T) map[string][]string {
	t.Helper()
	byKind := map[string][]string{}
	entries, err := os.ReadDir(examplesDir(t))
	if err != nil {
		t.Fatalf("reading examples: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		for _, doc := range readYAMLDocs(t, filepath.Join(examplesDir(t), e.Name())) {
			api, _ := doc["apiVersion"].(string)
			kind, _ := doc["kind"].(string)
			if strings.HasPrefix(api, examplesAPIGroup) && kind != "" {
				byKind[kind] = append(byKind[kind], e.Name())
			}
		}
	}
	return byKind
}

// Every object the platform defines has an example. An API nobody can see an
// example of is an API nobody uses: this is the index a person reads before
// reaching for the schema.
func TestEveryPlatformObjectHasAnExample(t *testing.T) {
	schemas := schemaForKind(t)
	examples := exampleObjects(t)

	var missing []string
	for kind := range schemas {
		if len(examples[kind]) == 0 {
			missing = append(missing, kind)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("no example in examples/ for %d platform object(s): %s\n"+
			"Add one file per object — it is the first thing anyone copies.",
			len(missing), strings.Join(missing, ", "))
	}
}

// An example naming a kind the platform does not define is worse than none: it
// cannot be applied, and it sends the reader looking for a CRD that was renamed
// or removed.
func TestNoExampleNamesAKindThePlatformDoesNotDefine(t *testing.T) {
	schemas := schemaForKind(t)
	for kind, files := range exampleObjects(t) {
		if _, ok := schemas[kind]; !ok {
			t.Errorf("%s: kind %q is in the platform.adhar.io group but no XRD or CRD defines it",
				strings.Join(files, ", "), kind)
		}
	}
}

// The field-by-field check. Unknown fields are the dangerous case (they are
// pruned, silently); missing required fields are the loud one.
func TestExamplesMatchTheirSchema(t *testing.T) {
	schemas := schemaForKind(t)
	dir := examplesDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading examples: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		for _, doc := range readYAMLDocs(t, filepath.Join(dir, e.Name())) {
			api, _ := doc["apiVersion"].(string)
			kind, _ := doc["kind"].(string)
			if !strings.HasPrefix(api, examplesAPIGroup) || kind == "" {
				continue
			}
			schema, ok := schemas[kind]
			if !ok {
				continue // reported by TestNoExampleNamesAKindThePlatformDoesNotDefine
			}
			spec, _ := doc["spec"].(map[string]interface{})
			if spec == nil {
				t.Errorf("%s: %s has no spec", e.Name(), kind)
				continue
			}
			for _, problem := range checkAgainstSchema(spec, schema, "spec") {
				t.Errorf("%s (%s): %s", e.Name(), kind, problem)
			}
		}
	}
}

// checkAgainstSchema compares one object against one OpenAPI schema and returns
// human-readable problems.
//
// Two rules, and the reasons they are the two:
//
//   - A field the schema does not declare is PRUNED by the API server, so the
//     example reads as if it configured something it does not.
//   - A required field with no default is rejected at apply time.
//
// `spec.crossplane` is always allowed: Crossplane injects that stanza into every
// XR (composition selection, revision policy) and an XRD must never declare it,
// so it appears in examples and in no schema.
func checkAgainstSchema(value interface{}, schema map[string]interface{}, path string) []string {
	var problems []string
	if schema == nil {
		return nil
	}
	if preserve, _ := schema["x-kubernetes-preserve-unknown-fields"].(bool); preserve {
		return nil
	}

	switch v := value.(type) {
	case map[string]interface{}:
		props, _ := schema["properties"].(map[string]interface{})
		additional := schema["additionalProperties"]
		for key, child := range v {
			if path == "spec" && key == "crossplane" {
				continue
			}
			childSchema, declared := props[key].(map[string]interface{})
			switch {
			case declared:
				problems = append(problems, checkAgainstSchema(child, childSchema, path+"."+key)...)
			case additional != nil:
				// A free-form map (labels, tags, features): the keys are the
				// user's, only the value shape is constrained.
				if as, ok := additional.(map[string]interface{}); ok {
					problems = append(problems, checkAgainstSchema(child, as, path+"."+key)...)
				}
			case len(props) == 0:
				// No properties and no additionalProperties: an untyped object.
			default:
				problems = append(problems, fmt.Sprintf(
					"%s.%s is not in the schema — the API server would PRUNE it, so this example "+
						"would apply cleanly and configure nothing", path, key))
			}
		}
		// Required, unless the schema defaults it (the API server defaults
		// before it validates, which is why e.g. a required-but-defaulted
		// compositionSelector may be omitted).
		required, _ := schema["required"].([]interface{})
		for _, r := range required {
			name, _ := r.(string)
			if name == "" {
				continue
			}
			if _, present := v[name]; present {
				continue
			}
			if sub, ok := props[name].(map[string]interface{}); ok {
				if _, hasDefault := sub["default"]; hasDefault {
					continue
				}
			}
			if path == "spec" && name == "crossplane" {
				continue
			}
			problems = append(problems, fmt.Sprintf("%s.%s is required and missing", path, name))
		}
	case []interface{}:
		items, _ := schema["items"].(map[string]interface{})
		for i, item := range v {
			problems = append(problems, checkAgainstSchema(item, items, fmt.Sprintf("%s[%d]", path, i))...)
		}
	}
	return problems
}
