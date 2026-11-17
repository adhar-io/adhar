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

// Batch history in the console (design 0025 D12): `/api/batch/history` reads
// the adhar.batch topic through the API server's service proxy to the Strimzi
// bridge, as the `console` ServiceAccount. The bridge has no stateless read —
// a reader creates a consumer (POST), polls it (GET) and deletes it (DELETE) —
// and the API server authorises those as create, get and delete on
// `services/proxy`, by the name in the URL, which carries the port.
//
// Two ways this goes wrong, and neither fails loudly. A grant that is too
// narrow (the bare Service name, or `get` alone, which `read-all` already
// covers) refuses the first request and the History page reports that the
// console may not reach the bridge. A grant that is too wide — no
// resourceNames, or a ClusterRole — lets the console's request handlers POST to
// every Service in the cluster through the API server.
const (
	consoleBatchHistoryRole = "console-batch-history"
	eventBridgeProxyName    = "adhar-events-bridge-service:8080"
)

// servicesProxyGrant is one rule that names services/proxy, wherever it is.
type servicesProxyGrant struct {
	kind, name, namespace string
	resourceNames, verbs  []string
}

// consoleProxyGrants returns every rule granting on services/proxy (by name or
// by a wildcard that reaches it with a write verb) in a role bound to the
// console ServiceAccount, and whether consoleBatchHistoryRole is bound to it.
func consoleProxyGrants(t *testing.T) ([]servicesProxyGrant, bool) {
	t.Helper()
	src := readStackFile(t, "core/adhar-console/manifests/install.yaml")
	type role struct {
		kind, name, namespace string
		rules                 []any
	}
	var roles []role
	bound := map[string]bool{} // kind/namespace/name of roles bound to the console SA
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
		kind := toString(doc["kind"])
		switch kind {
		case "Role", "ClusterRole":
			roles = append(roles, role{kind, toString(meta["name"]), toString(meta["namespace"]), toSlice(doc["rules"])})
		case "RoleBinding", "ClusterRoleBinding":
			ref, _ := doc["roleRef"].(map[string]any)
			for _, raw := range toSlice(doc["subjects"]) {
				s, _ := raw.(map[string]any)
				if toString(s["kind"]) == "ServiceAccount" && toString(s["name"]) == "console" && toString(s["namespace"]) == "adhar-system" {
					ns := ""
					if kind == "RoleBinding" && toString(ref["kind"]) == "Role" {
						ns = toString(meta["namespace"])
					}
					bound[toString(ref["kind"])+"/"+ns+"/"+toString(ref["name"])] = true
				}
			}
		}
	}
	var grants []servicesProxyGrant
	for _, r := range roles {
		if !bound[r.kind+"/"+r.namespace+"/"+r.name] {
			continue
		}
		for _, raw := range r.rules {
			rule, _ := raw.(map[string]any)
			core := false
			for _, g := range toSlice(rule["apiGroups"]) {
				if s := toString(g); s == "" || s == "*" {
					core = true
				}
			}
			named, wild := false, false
			for _, res := range toSlice(rule["resources"]) {
				switch toString(res) {
				case "services/proxy", "*/proxy":
					named = true
				case "*":
					wild = true
				}
			}
			if !core || (!named && !wild) {
				continue
			}
			g := servicesProxyGrant{kind: r.kind, name: r.name, namespace: r.namespace}
			for _, v := range toSlice(rule["verbs"]) {
				g.verbs = append(g.verbs, toString(v))
			}
			for _, n := range toSlice(rule["resourceNames"]) {
				g.resourceNames = append(g.resourceNames, toString(n))
			}
			sort.Strings(g.verbs)
			// A wildcard rule that only reads (read-all) reaches the proxy with
			// GET alone, which creates nothing; it is not a proxy grant.
			if !named && !hasWriteVerb(g.verbs) {
				continue
			}
			grants = append(grants, g)
		}
	}
	return grants, bound["Role/adhar-system/"+consoleBatchHistoryRole]
}

func hasWriteVerb(verbs []string) bool {
	for _, v := range verbs {
		if v != "get" && v != "list" && v != "watch" {
			return true
		}
	}
	return false
}

func TestConsoleReachesOnlyTheEventBridgeThroughTheServiceProxy(t *testing.T) {
	grants, bound := consoleProxyGrants(t)
	if !bound {
		t.Fatalf("Role adhar-system/%s is not bound to ServiceAccount adhar-system/console — /api/batch/history cannot create its bridge consumer", consoleBatchHistoryRole)
	}
	if len(grants) != 1 {
		t.Fatalf("the console ServiceAccount holds %d grants that reach services/proxy, want exactly one (%s): %+v", len(grants), consoleBatchHistoryRole, grants)
	}
	g := grants[0]
	if g.kind != "Role" || g.name != consoleBatchHistoryRole || g.namespace != "adhar-system" {
		t.Errorf("the services/proxy grant is %s %s/%s; it must be the namespaced Role adhar-system/%s — a ClusterRole reaches every namespace's Services", g.kind, g.namespace, g.name, consoleBatchHistoryRole)
	}
	if len(g.resourceNames) != 1 || g.resourceNames[0] != eventBridgeProxyName {
		t.Errorf("%s names %v; it must name exactly %q — the API server authorises the proxy by the name in the URL, port included, and an unnamed rule is every Service in the namespace", consoleBatchHistoryRole, g.resourceNames, eventBridgeProxyName)
	}
	if got := strings.Join(g.verbs, ","); got != "create,delete,get" {
		t.Errorf("%s grants [%s] on services/proxy, want exactly create, delete and get: the bridge's consumer is created with POST, polled with GET and removed with DELETE", consoleBatchHistoryRole, got)
	}
}

// The name in the Role is the name both readers put in the URL. The CLI's is in
// this repository; the console's is checked when its checkout is beside this
// one (the same arrangement as consoleEventsDir).
func TestConsoleAndCLIReachTheEventBridgeByTheNameTheRoleGrants(t *testing.T) {
	reach, err := os.ReadFile("../../../cmd/events/reach.go")
	if err != nil {
		t.Fatal(err)
	}
	svc := regexp.MustCompile(`bridgeService\s*=\s*"([^"]+)"`).FindSubmatch(reach)
	port := regexp.MustCompile(`bridgePort\s*=\s*"([^"]+)"`).FindSubmatch(reach)
	if svc == nil || port == nil {
		t.Fatal("cmd/events/reach.go no longer declares bridgeService and bridgePort")
	}
	if got := string(svc[1]) + ":" + string(port[1]); got != eventBridgeProxyName {
		t.Errorf("the CLI reaches the bridge as %q; Role %s names %q", got, consoleBatchHistoryRole, eventBridgeProxyName)
	}

	candidates := []string{}
	if d := os.Getenv("ADHAR_CONSOLE_DIR"); d != "" {
		candidates = append(candidates, d)
	}
	candidates = append(candidates, "../../../../../Anication/AdharWS/adhar-console")
	for _, c := range candidates {
		b, err := os.ReadFile(filepath.Join(c, "apps/console/app/server/batch-history.ts"))
		if err != nil {
			continue
		}
		src := string(b)
		cs := regexp.MustCompile(`BRIDGE_SERVICE\s*=\s*'([^']+)'`).FindStringSubmatch(src)
		cp := regexp.MustCompile(`BRIDGE_PORT\s*=\s*'([^']+)'`).FindStringSubmatch(src)
		cn := regexp.MustCompile(`BRIDGE_NAMESPACE\s*=\s*'([^']+)'`).FindStringSubmatch(src)
		if cs == nil || cp == nil || cn == nil {
			t.Fatalf("%s no longer declares BRIDGE_NAMESPACE, BRIDGE_SERVICE and BRIDGE_PORT", filepath.Join(c, "apps/console/app/server/batch-history.ts"))
		}
		if got := cs[1] + ":" + cp[1]; got != eventBridgeProxyName || cn[1] != "adhar-system" {
			t.Errorf("the console reaches the bridge as %s/%s; Role adhar-system/%s names %q", cn[1], got, consoleBatchHistoryRole, eventBridgeProxyName)
		}
		return
	}
	t.Log("no adhar-console checkout found; the console's side of the name was not checked")
}
