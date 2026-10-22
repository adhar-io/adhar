package adharplatform

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The console opens Plane support tickets for platform failures and attaches
// the evidence (apps/console/app/server/support/ in the adhar-console repo):
// the failing object and its Events, its pods and their logs, PromQL
// snapshots, Tempo traces and the Argo CD / Kargo delivery context. It
// collects all of it as the `console` ServiceAccount, in the background, with
// no user request in flight — so a grant or a URL that goes missing here does
// not fail anything loudly. Each evidence section just says "unavailable" on
// every ticket, and the tickets stop being worth opening.
//
// This pins what that collector depends on in the console package.

// What the evidence collector reads, as (group, resource[/subresource]) → verb.
var consoleEvidenceReads = []struct {
	group, resource, verb string
}{
	{"", "pods", "list"},
	{"", "pods/log", "get"},
	{"", "events", "list"},
	{"apps", "deployments", "get"},
	{"apps", "statefulsets", "get"},
	{"batch", "jobs", "get"},
	{"argoproj.io", "applications", "list"},
	{"argoproj.io", "rollouts", "get"},
	{"tekton.dev", "pipelineruns", "get"},
	{"kargo.akuity.io", "promotions", "list"},
	{"kargo.akuity.io", "stages", "get"},
	{"platform.adhar.io", "databases", "get"},
	{"platform.adhar.io", "adharplatforms", "get"},
}

// The backends and the ticket destination, by the env the console reads.
var consoleEvidenceEnv = []string{"LOKI_URL", "PROMETHEUS_URL", "TEMPO_URL", "GRAFANA_URL", "ARGOCD_URL", "KARGO_URL"}

const consolePlaneSecret = "plane-api-token"

type rbacRule struct{ groups, resources, verbs []string }

func matchesAny(list []string, want string) bool {
	for _, v := range list {
		if v == "*" || v == want {
			return true
		}
	}
	return false
}

// matchesResource follows RBAC: `*` is every resource and subresource,
// `pods/*` every subresource of pods.
func matchesResource(list []string, want string) bool {
	base, sub, _ := strings.Cut(want, "/")
	for _, v := range list {
		if v == "*" || v == want || (sub != "" && v == base+"/*") {
			return true
		}
	}
	return false
}

// consoleSARules returns every rule of every ClusterRole bound to the console
// ServiceAccount, and the console Deployment's container.
func consoleSARules(t *testing.T) ([]rbacRule, map[string]any) {
	t.Helper()
	src := readStackFile(t, "core/adhar-console/manifests/install.yaml")
	roles := map[string][]rbacRule{}
	bound := map[string]bool{}
	var container map[string]any
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
			for _, raw := range toSlice(doc["rules"]) {
				r, _ := raw.(map[string]any)
				var rule rbacRule
				for _, g := range toSlice(r["apiGroups"]) {
					rule.groups = append(rule.groups, toString(g))
				}
				for _, x := range toSlice(r["resources"]) {
					rule.resources = append(rule.resources, toString(x))
				}
				for _, v := range toSlice(r["verbs"]) {
					rule.verbs = append(rule.verbs, toString(v))
				}
				roles[toString(meta["name"])] = append(roles[toString(meta["name"])], rule)
			}
		case "ClusterRoleBinding":
			ref, _ := doc["roleRef"].(map[string]any)
			for _, raw := range toSlice(doc["subjects"]) {
				s, _ := raw.(map[string]any)
				if toString(s["kind"]) == "ServiceAccount" && toString(s["name"]) == "console" && toString(s["namespace"]) == "adhar-system" {
					bound[toString(ref["name"])] = true
				}
			}
		case "Deployment":
			if toString(meta["name"]) != "adhar-console" {
				continue
			}
			spec, _ := doc["spec"].(map[string]any)
			tpl, _ := spec["template"].(map[string]any)
			pspec, _ := tpl["spec"].(map[string]any)
			for _, raw := range toSlice(pspec["containers"]) {
				c, _ := raw.(map[string]any)
				if toString(c["name"]) == "adhar-console" {
					container = c
				}
			}
		}
	}
	var rules []rbacRule
	for name := range bound {
		rules = append(rules, roles[name]...)
	}
	if container == nil {
		t.Fatal("no adhar-console container in the console package")
	}
	return rules, container
}

func TestConsoleCanCollectSupportTicketEvidence(t *testing.T) {
	rules, container := consoleSARules(t)
	for _, want := range consoleEvidenceReads {
		ok := false
		for _, r := range rules {
			if matchesAny(r.groups, want.group) && matchesResource(r.resources, want.resource) && matchesAny(r.verbs, want.verb) {
				ok = true
				break
			}
		}
		if !ok {
			g := want.group
			if g == "" {
				g = "core"
			}
			t.Errorf("the console ServiceAccount cannot %s %s/%s — every support ticket's evidence section for it would read \"unavailable\"", want.verb, g, want.resource)
		}
	}

	env := map[string]bool{}
	for _, raw := range toSlice(container["env"]) {
		e, _ := raw.(map[string]any)
		env[toString(e["name"])] = true
	}
	for _, name := range consoleEvidenceEnv {
		if !env[name] {
			t.Errorf("the console container has no %s — support tickets lose that backend (and its deep links)", name)
		}
	}

	plane := false
	for _, raw := range toSlice(container["envFrom"]) {
		e, _ := raw.(map[string]any)
		ref, _ := e["secretRef"].(map[string]any)
		if toString(ref["name"]) == consolePlaneSecret {
			plane = true
		}
	}
	if !plane {
		t.Errorf("the console container does not mount Secret %s (PLANE_URL / PLANE_TOKEN / PLANE_WORKSPACE) — no support ticket can be filed", consolePlaneSecret)
	}
}
