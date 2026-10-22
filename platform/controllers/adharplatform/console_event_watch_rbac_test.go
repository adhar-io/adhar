package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The console's notification watcher (apps/console/app/server/events/ in the
// adhar-console repo) runs as the `console` ServiceAccount with no request in
// flight, and turns state transitions on these resources into notifications.
// A resource it cannot list does not fail anything: `runWatch` logs a single
// 403 and disables that source, and the Notification Center simply stops
// mentioning promotions, or rollouts, or a self-service Database failing.
//
// This is the console's watch list, outside the control-plane XRs (those are
// read from the XRD directory, so a new XRD cannot be added without the
// console being granted it). `namespaces` is read by the watcher's tenant
// resolution, not watched, but it needs the same grant.
var consoleWatchedResources = []groupResource{
	{"", "events"},
	{"", "namespaces"},
	{"", "nodes"},
	{"batch", "jobs"},
	{"argoproj.io", "applications"},
	{"argoproj.io", "rollouts"},
	{"tekton.dev", "pipelineruns"},
	{"kargo.akuity.io", "freights"},
	{"kargo.akuity.io", "promotions"},
	{"kargo.akuity.io", "stages"},
}

const consoleEventWatcherRole = "console-event-watcher"

type groupResource struct{ group, resource string }

func (g groupResource) String() string {
	if g.group == "" {
		return "core/" + g.resource
	}
	return g.group + "/" + g.resource
}

// xrdPlurals returns the plural of every platform.adhar.io XRD the control
// plane defines.
func xrdPlurals(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("../../controlplane/configuration/xrd/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no XRDs found under platform/controlplane/configuration/xrd: %v", err)
	}
	var out []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		dec := yaml.NewDecoder(strings.NewReader(string(b)))
		for {
			var doc map[string]any
			if err := dec.Decode(&doc); err != nil {
				break
			}
			if doc == nil || doc["kind"] != "CompositeResourceDefinition" {
				continue
			}
			spec, _ := doc["spec"].(map[string]any)
			if toString(spec["group"]) != "platform.adhar.io" {
				continue
			}
			names, _ := spec["names"].(map[string]any)
			if p := toString(names["plural"]); p != "" {
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out
}

// consoleRBAC returns what the console-event-watcher role grants (group/resource
// → verbs) and whether a ClusterRoleBinding binds it to the console SA.
func consoleRBAC(t *testing.T) (map[groupResource]map[string]bool, bool) {
	t.Helper()
	src := readStackFile(t, "core/adhar-console/manifests/install.yaml")
	grants := map[groupResource]map[string]bool{}
	bound := false
	found := false
	dec := yaml.NewDecoder(strings.NewReader(src))
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc == nil {
			continue
		}
		meta, _ := doc["metadata"].(map[string]any)
		switch doc["kind"] {
		case "ClusterRole":
			if toString(meta["name"]) != consoleEventWatcherRole {
				continue
			}
			found = true
			for _, raw := range toSlice(doc["rules"]) {
				rule, _ := raw.(map[string]any)
				verbs := map[string]bool{}
				for _, v := range toSlice(rule["verbs"]) {
					verbs[toString(v)] = true
				}
				for _, g := range toSlice(rule["apiGroups"]) {
					for _, r := range toSlice(rule["resources"]) {
						gr := groupResource{toString(g), toString(r)}
						if grants[gr] == nil {
							grants[gr] = map[string]bool{}
						}
						for v := range verbs {
							grants[gr][v] = true
						}
					}
				}
			}
		case "ClusterRoleBinding":
			ref, _ := doc["roleRef"].(map[string]any)
			if toString(ref["name"]) != consoleEventWatcherRole || toString(ref["kind"]) != "ClusterRole" {
				continue
			}
			for _, raw := range toSlice(doc["subjects"]) {
				s, _ := raw.(map[string]any)
				if toString(s["kind"]) == "ServiceAccount" && toString(s["name"]) == "console" && toString(s["namespace"]) == "adhar-system" {
					bound = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("no ClusterRole %s in the console package — the notification watcher has no grant of its own", consoleEventWatcherRole)
	}
	return grants, bound
}

func TestConsoleWatchedResourcesAreGranted(t *testing.T) {
	grants, bound := consoleRBAC(t)
	if !bound {
		t.Errorf("ClusterRole %s is not bound to ServiceAccount adhar-system/console", consoleEventWatcherRole)
	}

	want := append([]groupResource{}, consoleWatchedResources...)
	for _, p := range xrdPlurals(t) {
		want = append(want, groupResource{"platform.adhar.io", p})
	}
	for _, gr := range want {
		verbs := grants[gr]
		for _, v := range []string{"get", "list", "watch"} {
			if !verbs[v] {
				t.Errorf("%s does not grant %s on %s — the console's notification watcher disables that source on a 403", consoleEventWatcherRole, v, gr)
			}
		}
	}

	// Read-only, and nothing it was not asked for: this role is the
	// watcher's, and a write verb that drifts onto it is a privilege the
	// console's request handlers would quietly inherit.
	wanted := map[groupResource]bool{}
	for _, gr := range want {
		wanted[gr] = true
	}
	for gr, verbs := range grants {
		if !wanted[gr] {
			t.Errorf("%s grants %s, which the watcher does not read", consoleEventWatcherRole, gr)
		}
		for v := range verbs {
			if v != "get" && v != "list" && v != "watch" {
				t.Errorf("%s grants %q on %s; it must stay read-only", consoleEventWatcherRole, v, gr)
			}
		}
	}
}

// consoleEventsDir locates the console's events module, when a checkout of the
// console repo is available: $ADHAR_CONSOLE_DIR, else the workspace layout the
// two repos are developed in. Absent, the cross-check is skipped — the role is
// still pinned against the Go list and the XRDs above.
func consoleEventsDir(t *testing.T) string {
	t.Helper()
	candidates := []string{}
	if d := os.Getenv("ADHAR_CONSOLE_DIR"); d != "" {
		candidates = append(candidates, d)
	}
	candidates = append(candidates, "../../../../../Anication/AdharWS/adhar-console")
	for _, c := range candidates {
		dir := filepath.Join(c, "apps/console/app/server/events")
		if _, err := os.Stat(filepath.Join(dir, "platform-events.ts")); err == nil {
			return dir
		}
	}
	return ""
}

var (
	tsSourceRe = regexp.MustCompile(`group:\s*'([^']*)',\s*version:\s*'[^']*',\s*resource:\s*'([^']+)'`)
	tsXRKindRe = regexp.MustCompile(`\{\s*kind:\s*'[A-Za-z]+',\s*plural:\s*'([a-z]+)'\s*\}`)
)

// The Go list above mirrors the console; this proves the mirror, by reading
// what the console actually watches out of its TypeScript.
func TestConsoleWatchListMatchesTheConsoleSource(t *testing.T) {
	dir := consoleEventsDir(t)
	if dir == "" {
		t.Skip("adhar-console checkout not found; set ADHAR_CONSOLE_DIR to cross-check the watch list")
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.ts"))
	if err != nil {
		t.Fatal(err)
	}
	watched := map[groupResource]bool{}
	var xrKinds []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.ts") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range tsSourceRe.FindAllStringSubmatch(string(b), -1) {
			watched[groupResource{m[1], m[2]}] = true
		}
		for _, m := range tsXRKindRe.FindAllStringSubmatch(string(b), -1) {
			xrKinds = append(xrKinds, m[1])
		}
	}
	if len(watched) == 0 || len(xrKinds) == 0 {
		t.Fatalf("parsed nothing from %s — the source/XR_KINDS literal shape changed; update the regexps", dir)
	}

	known := map[groupResource]bool{}
	for _, gr := range consoleWatchedResources {
		known[gr] = true
	}
	for gr := range watched {
		if !known[gr] {
			t.Errorf("the console watches %s but consoleWatchedResources (and so the ClusterRole check) does not list it", gr)
		}
	}

	sort.Strings(xrKinds)
	xrds := xrdPlurals(t)
	have := map[string]bool{}
	for _, p := range xrKinds {
		have[p] = true
	}
	for _, p := range xrds {
		if !have[p] {
			t.Errorf("XRD %s.platform.adhar.io exists but the console's XR_KINDS does not watch it — its created/ready/failed/deleted would never notify", p)
		}
	}
	defined := map[string]bool{}
	for _, p := range xrds {
		defined[p] = true
	}
	for _, p := range xrKinds {
		if !defined[p] {
			t.Errorf("the console's XR_KINDS watches %s.platform.adhar.io, which no XRD defines", p)
		}
	}
}
