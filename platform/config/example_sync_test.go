package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"gopkg.in/yaml.v3"
)

// The shipped example for each cloud must have the same SHAPE as the live config
// it was derived from.
//
// The two sets are deliberately separate: `config.<cloud>.yaml` at the repo root
// holds an operator's real hostnames, account ids and zone ids and is gitignored,
// while `examples/<cloud>-config.yaml` is the placeholder-only version that ships.
// Nothing links them, so a field added to the live file while debugging a cloud is
// invisible to the example — and the example is what the next person copies. That
// happened immediately: `dnsProvider` was added to the live DigitalOcean and Civo
// configs and neither example had it, so anyone starting from the shipped file got
// self-signed TLS and no DNS records with nothing to suggest why.
//
// Values are NOT compared — placeholders differ from real values by design. Only
// the set of key paths.
//
// Skipped per-pair when the live file is absent, which is the normal case in CI and
// for any cloud an operator does not use. This catches drift where the live file
// exists, which is exactly where it is created.
func TestExamplesMatchTheLiveConfigShape(t *testing.T) {
	root := repoRoot(t)
	pairs := []struct{ live, example string }{
		{"config.aws.yaml", "examples/aws-config.yaml"},
		{"config.azure.yaml", "examples/azure-config.yaml"},
		{"config.gcp.yaml", "examples/gcp-config.yaml"},
		{"config.digitalocean.yaml", "examples/digitalocean-config.yaml"},
		{"config.civo.yaml", "examples/civo-config.yaml"},
	}

	for _, p := range pairs {
		t.Run(p.live, func(t *testing.T) {
			livePath := filepath.Join(root, p.live)
			if _, err := os.Stat(livePath); os.IsNotExist(err) {
				t.Skipf("%s is not present (gitignored; absent in CI and for unused clouds)", p.live)
			}
			live := keyPaths(t, livePath)
			example := keyPaths(t, filepath.Join(root, p.example))

			if missing := difference(live, example); len(missing) > 0 {
				t.Errorf("%s has keys the shipped example lacks — the example is what people copy, so add them (with placeholder values):\n  %v",
					p.live, missing)
			}
			if extra := difference(example, live); len(extra) > 0 {
				t.Errorf("%s has keys the live config lacks. Either the live file has fallen behind, or the example documents something no longer supported:\n  %v",
					p.example, extra)
			}
		})
	}
}

// keyPaths is every key path in the document — shape without values.
func keyPaths(t *testing.T, path string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var doc interface{}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	out := map[string]bool{}
	walk(doc, "", out)
	return out
}

func walk(node interface{}, prefix string, out map[string]bool) {
	switch v := node.(type) {
	case map[string]interface{}:
		for k, child := range v {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			out[p] = true
			walk(child, p, out)
		}
	case []interface{}:
		for _, item := range v {
			// clusterConfig is a list of {key,value} pairs; a new tuning knob is a
			// new `key`, so index them by it rather than by position.
			if m, ok := item.(map[string]interface{}); ok {
				if key, ok := m["key"]; ok {
					out[fmt.Sprintf("%s[%v]", prefix, key)] = true
					continue
				}
			}
			walk(item, prefix+"[]", out)
		}
	}
}

func difference(a, b map[string]bool) []string {
	var only []string
	for k := range a {
		if !b[k] {
			only = append(only, k)
		}
	}
	sort.Strings(only)
	return only
}

// repoRoot walks up until it finds go.mod, so the test does not depend on where it
// is run from.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find the repository root (no go.mod above the test)")
		}
		dir = parent
	}
}
