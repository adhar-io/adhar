package v1alpha1

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// A malformed struct tag is silent, and controller-gen turns it into a schema
// change.
//
// `json:"name,omitempty'"` — one stray apostrophe — shipped in
// PackageCustomization. controller-gen does not read `omitempty'` as omitempty,
// so it generated `required: [name]` on GitRepository.spec.customization, and
// the CustomPackage controller (which creates GitRepository objects with no
// customization at all) had every one of them rejected by the API server:
// `spec.customization.name: Required value`. Custom packages could not be
// delivered, and nothing about the symptom pointed at a quote character.
//
// reflect.StructTag.Get is the exact parser Go uses, so a tag it cannot read is
// a tag that does not mean what it looks like.
func TestNoStructTagIsMalformed(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("listing the API package: %v", err)
	}

	checked := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, src, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", file, err)
		}

		ast.Inspect(parsed, func(n ast.Node) bool {
			field, ok := n.(*ast.Field)
			if !ok || field.Tag == nil {
				return true
			}
			raw, err := strconv.Unquote(field.Tag.Value)
			if err != nil {
				t.Errorf("%s: field %s has an unparseable tag %s", file, fieldNames(field), field.Tag.Value)
				return true
			}
			checked++

			for _, key := range []string{"json", "mapstructure"} {
				value, ok := reflect.StructTag(raw).Lookup(key)
				if !ok {
					continue
				}
				parts := strings.Split(value, ",")
				// The name itself: a tag name is a field name, so anything
				// outside the identifier characters is a typo.
				if strings.ContainsAny(parts[0], "'\" \t") {
					t.Errorf("%s: field %s has %s name %q — a quote or space in a tag name means the tag does not say what it looks like",
						file, fieldNames(field), key, parts[0])
				}
				for _, opt := range parts[1:] {
					switch opt {
					case "omitempty", "omitzero", "inline", "string", "squash", "remain", "":
					default:
						t.Errorf("%s: field %s has unknown %s option %q (did a quote get into the tag? `omitempty'` reads as "+
							"an unknown option, and controller-gen then marks the field REQUIRED)",
							file, fieldNames(field), key, opt)
					}
				}
			}
			return true
		})
	}

	if checked == 0 {
		t.Fatal("no struct tags were checked; this test is not looking at the API types")
	}
}

func fieldNames(field *ast.Field) string {
	var names []string
	for _, n := range field.Names {
		names = append(names, n.Name)
	}
	if len(names) == 0 {
		return "(embedded)"
	}
	return strings.Join(names, ", ")
}
