package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Crossplane installs a CRD only for the managed-resource kinds its default
// activation policy names (every provider here is safe-start). "*" installed
// ~230 GCP CRDs for four used kinds in one minute of a fresh `adhar up`, and
// Argo CD's cache stalled behind them for minutes (2026-10-21) — so the list
// is narrow, and these tests keep it honest in both directions.

var activationArg = regexp.MustCompile(`(?m)^\s*- --activation\s*\n\s*- "([^"]+)"`)

func crossplaneActivations(t *testing.T, file string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("resources", "crossplane", file))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range activationArg.FindAllStringSubmatch(string(b), -1) {
		out = append(out, m[1])
	}
	if len(out) == 0 {
		t.Fatalf("%s carries no --activation arguments", file)
	}
	return out
}

func TestCrossplaneDoesNotActivateEveryManagedResource(t *testing.T) {
	for _, f := range []string{"install.yaml", "install-ha.yaml"} {
		for _, a := range crossplaneActivations(t, f) {
			if a == "*" || strings.HasPrefix(a, "*.") && strings.Count(a, ".") < 3 {
				t.Errorf("%s activates %q: every provider's full catalogue becomes CRDs again", f, a)
			}
		}
	}
}

// managedKindRef matches an apiVersion/kind pair in either order, wherever it
// sits in a composition — a resource base, a go-templating string, a KCL or
// Python body.
var managedKindRef = []*regexp.Regexp{
	regexp.MustCompile(`apiVersion:\s*["']?([a-z0-9.-]+)/v[0-9a-z]+["']?\s*\n\s*kind:\s*["']?([A-Za-z0-9]+)`),
	regexp.MustCompile(`kind:\s*["']?([A-Za-z0-9]+)["']?\s*\n\s*apiVersion:\s*["']?([a-z0-9.-]+)/v[0-9a-z]+`),
}

// Provider API groups whose kinds are managed resources (and so are subject to
// activation). Crossplane's own groups and functions are not.
func isManagedResourceGroup(group string) bool {
	if strings.HasSuffix(group, ".upbound.io") {
		return true
	}
	for _, g := range []string{"kubernetes.crossplane.io", "kubernetes.m.crossplane.io", "helm.crossplane.io",
		"helm.m.crossplane.io", "kubernetes.digitalocean.m.crossplane.io", "cluster.civo.crossplane.io"} {
		if group == g {
			return true
		}
	}
	return false
}

func plural(kind string) string {
	k := strings.ToLower(kind)
	switch {
	case strings.HasSuffix(k, "y") && !strings.ContainsAny(k[len(k)-2:len(k)-1], "aeiou"):
		return strings.TrimSuffix(k, "y") + "ies"
	case strings.HasSuffix(k, "s"):
		return k + "es"
	}
	return k + "s"
}

func activated(name, group string, acts []string) bool {
	for _, a := range acts {
		if a == name || a == "*."+group {
			return true
		}
	}
	return false
}

func TestEveryComposedManagedResourceIsActivated(t *testing.T) {
	acts := crossplaneActivations(t, "install.yaml")
	root := filepath.Join("..", "..", "controlplane", "configuration", "compositions")
	used := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, re := range managedKindRef {
			for _, m := range re.FindAllStringSubmatch(string(b), -1) {
				group, kind := m[1], m[2]
				if i == 1 {
					group, kind = m[2], m[1]
				}
				if !isManagedResourceGroup(group) || strings.HasSuffix(kind, "ProviderConfig") {
					continue
				}
				used[plural(kind)+"."+group] = filepath.Base(path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(used) < 10 {
		t.Fatalf("found only %d composed managed-resource kinds; the scan is broken", len(used))
	}
	var missing []string
	for name, file := range used {
		group := name[strings.Index(name, ".")+1:]
		if !activated(name, group, acts) {
			missing = append(missing, name+" ("+file+")")
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("composed but not activated — no CRD, so the XR composing it fails: %s", m)
	}
}

// The values files are the generator's input; the manifests are its output.
// An edit to one without regenerating the other is reverted by the next run.
func TestCrossplaneActivationsMatchTheirValues(t *testing.T) {
	for values, manifest := range map[string]string{"values.yaml": "install.yaml", "values-ha.yaml": "install-ha.yaml"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "..", "hack", "crossplane", values))
		if err != nil {
			t.Fatal(err)
		}
		block := string(b)[strings.Index(string(b), "defaultActivations:"):]
		block = block[:strings.Index(block, "\nconfiguration:")]
		var want []string
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "- ") {
				want = append(want, strings.Trim(strings.TrimPrefix(line, "- "), `"`))
			}
		}
		if got := crossplaneActivations(t, manifest); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s and %s disagree; run hack/crossplane/generate-manifests.sh\n values: %v\n manifest: %v", values, manifest, want, got)
		}
	}
}
