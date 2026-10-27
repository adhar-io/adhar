package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every document the Vector composition renders must be a whole Kubernetes
// object.
//
// A whitespace trim is invisible in review: the `-}}` closing the collections
// hash swallowed the line break after the comment above it, so the next line,
// `apiVersion: kubernetes.m.crossplane.io/v1alpha1`, rendered as the tail of
// that comment. The collections Object had no apiVersion, Crossplane refused
// the XR ("cannot check if composed resource "collections" is namespaced …
// Object 'apiVersion' is missing"), and nothing of the Vector — no Qdrant, no
// API key Secret — was ever composed for the AI knowledge base (GCP,
// 2026-10-26). Only a render shows it, so this renders.
func TestVectorRendersOnlyWholeObjects(t *testing.T) {
	collections := []any{
		map[string]any{"name": "adhar_knowledge", "vectorSize": 1536, "distance": "Cosine", "onDisk": true},
		map[string]any{"name": "scratch"},
	}
	for _, tc := range []struct {
		name   string
		params map[string]any
		// composed resource names that must be present
		want []string
	}{
		{"defaults", map[string]any{}, []string{"api-key-password", "qdrant"}},
		{"collections", map[string]any{"collections": collections},
			[]string{"api-key-password", "qdrant", "collections"}},
		{"distributed, no read-only key, no metrics", map[string]any{
			"replicas": 3, "readOnlyKey": false, "metrics": false, "project": "search",
			"collections": collections,
		}, []string{"api-key-password", "qdrant", "collections"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			docs := renderGoTemplate(t, vectorRequest(tc.params), "compositions", "vector", "qdrant.yaml")
			composed := map[string]map[string]any{}
			for i, d := range docs {
				api, _ := d["apiVersion"].(string)
				kind, _ := d["kind"].(string)
				if api == "" || kind == "" {
					t.Errorf("rendered document %d has apiVersion=%q kind=%q; a line of YAML has been "+
						"swallowed (a `-}}` trim joining it onto the line before?)", i, api, kind)
					continue
				}
				if kind == "Vector" {
					continue // the XR's own status
				}
				name, _ := dig(d, "metadata", "annotations", "gotemplating.fn.crossplane.io/composition-resource-name").(string)
				if name == "" {
					t.Errorf("composed %s/%s has no composition-resource-name", api, kind)
					continue
				}
				if composed[name] != nil {
					t.Errorf("composition-resource-name %q is rendered twice", name)
				}
				composed[name] = d
			}
			for _, w := range tc.want {
				if composed[w] == nil {
					t.Errorf("no composed resource %q", w)
				}
			}
			if job := composed["collections"]; job != nil {
				m, _ := dig(job, "spec", "forProvider", "manifest").(map[string]any)
				name, _ := dig(m, "metadata", "name").(string)
				if m["kind"] != "Job" || !strings.HasPrefix(name, "kb-collections-") {
					t.Errorf("the collections Object wraps %v %q, want the kb-collections-<hash> Job", m["kind"], name)
				}
			}
		})
	}
}

func vectorRequest(params map[string]any) map[string]any {
	return map[string]any{"observed": map[string]any{"composite": map[string]any{"resource": map[string]any{
		"apiVersion": "platform.adhar.io/v1alpha1",
		"kind":       "Vector",
		"metadata":   map[string]any{"name": "kb", "namespace": "team-a"},
		"spec":       map[string]any{"parameters": params},
	}}}}
}

// The same trap in every composition, found without rendering.
//
// Between two lines of YAML, a run of template actions that print nothing
// (if/range/end/with, assignments, comments) is harmless only while one line
// break survives it. When the first action trims to its left (`{{-`), the
// last trims to its right (`-}}`) and every gap between them is trimmed too,
// the two YAML lines come out as ONE — a key glued onto a value, or, in the
// Vector's case, an `apiVersion:` glued onto a comment. Runs containing `else`
// are skipped: the lines on either side of an else never render together.
func TestNoTemplateTrimJoinsTwoLinesOfYAML(t *testing.T) {
	checked := 0
	err := filepath.WalkDir(filepath.Join(controlPlaneDir(t), "compositions"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return err
		}
		for _, doc := range readYAMLDocs(t, path) {
			if doc["kind"] != "Composition" {
				continue
			}
			pipeline, _ := dig(doc, "spec", "pipeline").([]interface{})
			for _, raw := range pipeline {
				step, _ := raw.(map[string]interface{})
				src, _ := dig(step, "input", "inline", "template").(string)
				if src == "" {
					continue
				}
				checked++
				rel, _ := filepath.Rel(controlPlaneDir(t), path)
				for _, j := range trimJoins(src) {
					t.Errorf("%s (step %v): %q and %q render as one line — the actions between them "+
						"print nothing and trim every line break; drop the `-` from the last `-}}` or "+
						"move the actions out from between the two lines", rel, step["step"], j[0], j[1])
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 10 {
		t.Fatalf("only %d go-templating steps were checked; the walk is broken", checked)
	}

	// And the detector does detect: the Vector's template as it shipped.
	shipped := "metadata:\n  labels: {a: b}\n# The Job is not TTL-deleted.\n" +
		"{{- $spec := list -}}\n{{- range $collections -}}\n{{- $c := . -}}\n{{- end -}}\n" +
		"{{- $hash := trunc 8 (sha256sum (toJson $spec)) -}}\napiVersion: kubernetes.m.crossplane.io/v1alpha1\n"
	if len(trimJoins(shipped)) != 1 {
		t.Error("trimJoins no longer finds the joined apiVersion it was written for")
	}
	if fixed := strings.Replace(shipped, "$spec)) -}}", "$spec)) }}", 1); len(trimJoins(fixed)) != 0 {
		t.Error("trimJoins flags a run whose last action keeps its line break")
	}
	if alt := "  value: a\n{{- else -}}\n  value: b\n"; len(trimJoins(alt)) != 0 {
		t.Error("trimJoins flags lines on either side of an else, which never render together")
	}
}

var (
	templateAction = regexp.MustCompile(`(?s)\{\{.*?\}\}`)
	// Actions that print nothing.
	silentAction = regexp.MustCompile(`(?s)^\{\{-?\s*(/\*.*\*/|if\s|else\b|end\b|range\s|with\s|define\s|\$[A-Za-z_][A-Za-z0-9_]*\s*:?=)`)
	elseAction   = regexp.MustCompile(`^\{\{-?\s*else\b`)
)

// trimJoins returns each pair of YAML lines the template renders as one line.
func trimJoins(src string) [][2]string {
	lines := logicalTemplateLines(src)
	var out [][2]string
	for i := 0; i < len(lines); i++ {
		l1 := strings.TrimSpace(lines[i])
		if l1 == "" || strings.HasPrefix(l1, "{{") {
			continue
		}
		var run []string
		j := i + 1
		for ; j < len(lines); j++ {
			acts, ok := actionsOnly(lines[j])
			if !ok {
				break
			}
			run = append(run, acts...)
		}
		if len(run) == 0 || j >= len(lines) {
			continue
		}
		l2 := strings.TrimSpace(lines[j])
		if l2 == "" || strings.HasPrefix(l2, "{{") {
			continue
		}
		joined := strings.HasPrefix(run[0], "{{-") && strings.HasSuffix(run[len(run)-1], "-}}")
		for k, a := range run {
			if !silentAction.MatchString(a) || elseAction.MatchString(a) {
				joined = false
			}
			// A line break survives between two actions unless one of them trims it.
			if k > 0 && !strings.HasSuffix(run[k-1], "-}}") && !strings.HasPrefix(a, "{{-") {
				joined = false
			}
		}
		if joined {
			out = append(out, [2]string{l1, l2})
		}
		i = j - 1
	}
	return out
}

// actionsOnly reports whether a line holds nothing but template actions, and
// returns them.
func actionsOnly(line string) ([]string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "{{") {
		return nil, false
	}
	acts := templateAction.FindAllString(trimmed, -1)
	if strings.TrimSpace(templateAction.ReplaceAllString(trimmed, "")) != "" {
		return nil, false
	}
	return acts, true
}

// logicalTemplateLines splits a template into lines, keeping an action that
// spans several lines (a long comment) on one. A comment runs to its `*/`
// whatever braces it quotes.
func logicalTemplateLines(src string) []string {
	var out []string
	buf, open := "", false
	for _, ln := range strings.Split(src, "\n") {
		if open {
			buf += "\n" + ln
		} else {
			buf = ln
		}
		open = strings.Count(buf, "{{") > strings.Count(buf, "}}") ||
			strings.Count(buf, "/*") > strings.Count(buf, "*/")
		if !open {
			out = append(out, buf)
		}
	}
	if open {
		out = append(out, buf)
	}
	return out
}
