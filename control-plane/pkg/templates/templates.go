package templates

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"text/template"

	sprig "github.com/Masterminds/sprig/v3"
	"gopkg.in/yaml.v3"
)

// compositionPipeline mirrors the sections we need from a composition manifest.
type compositionPipeline struct {
	Spec struct {
		Pipeline []struct {
			Input struct {
				Spec struct {
					Template string `yaml:"template"`
				} `yaml:"spec"`
			} `yaml:"input"`
		} `yaml:"pipeline"`
	} `yaml:"spec"`
}

// LoadTemplate pulls the first pipeline template from the supplied composition file.
func LoadTemplate(path string) (string, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("open composition: %w", err)
	}
	defer func() { _ = f.Close() }()

	content, err := io.ReadAll(f)
	if err != nil {
		return "", fmt.Errorf("read composition: %w", err)
	}

	var comp compositionPipeline
	if err := yaml.Unmarshal(content, &comp); err != nil {
		return "", fmt.Errorf("unmarshal composition: %w", err)
	}

	if len(comp.Spec.Pipeline) == 0 {
		return "", fmt.Errorf("composition %s has no pipeline entries", path)
	}

	tmpl := comp.Spec.Pipeline[0].Input.Spec.Template
	if tmpl == "" {
		return "", fmt.Errorf("composition %s does not define a template", path)
	}

	return tmpl, nil
}

// Render executes a go-templating template with sprig helpers and missingkey=error semantics.
func Render(tmpl string, data any) (string, error) {
	funcs := sprig.TxtFuncMap()
	funcs["required"] = required
	funcs["toYaml"] = toYAML

	t, err := template.New("control-plane").Funcs(funcs).Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}

	var out bytesBuffer
	if err := t.Execute(&out, data); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}

	return out.String(), nil
}

func required(message string, values ...any) (any, error) {
	if len(values) == 0 {
		return nil, errors.New(message)
	}

	for _, v := range values {
		if isEmpty(v) {
			return nil, errors.New(message)
		}
	}

	return values[0], nil
}

func isEmpty(v any) bool {
	if v == nil {
		return true
	}

	switch val := v.(type) {
	case string:
		return strings.TrimSpace(val) == ""
	case bool:
		return !val
	}

	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map, reflect.String:
		return rv.Len() == 0
	case reflect.Ptr, reflect.Interface:
		if rv.IsNil() {
			return true
		}
		return isEmpty(rv.Elem().Interface())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return rv.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return rv.Float() == 0
	}

	return false
}

func toYAML(v any) (string, error) {
	bytes, err := yaml.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

// bytesBuffer provides a minimal buffer implementation to avoid importing bytes directly.
type bytesBuffer struct {
	buf []byte
}

func (b *bytesBuffer) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *bytesBuffer) String() string {
	return string(b.buf)
}
