package adharplatform

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	sigsyaml "sigs.k8s.io/yaml"

	"adhar-io/adhar/globals"
)

// The batch node pool is spelled in three places that must agree (design 0025
// D14): the kubelet flags a pool node registers with (platform/providers,
// globals.BatchNodePool*), the node autoscaler that tells pool nodes apart, and
// adhar-batch-node-pool, the policy that sends batch runs there. A label or a
// taint spelled differently in the policy leaves every run off the pool — or
// worse, tolerating a taint no node carries — with nothing failing at admission.
// The policy's behaviour itself is proven by policy-tests/node-pool (make
// policy-test); this holds its SHAPE to the constants and to Kyverno's RBAC.
func TestBatchNodePoolPolicyMatchesThePoolNodes(t *testing.T) {
	dir := filepath.Join(stackPackagesDir(t), "core", "adhar-batch")
	raw, err := os.ReadFile(filepath.Join(dir, "manifests", "kyverno-node-pool.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var policy map[string]any
	if err := sigsyaml.Unmarshal(raw, &policy); err != nil {
		t.Fatal(err)
	}
	if got := toString(dig(policy, "metadata", "annotations", "pod-policies.kyverno.io/autogen-controllers")); got != "none" {
		t.Errorf("autogen-controllers = %q, want none: mutating a Job or CronJob template would diff against Argo CD and against the run template run integrity compares", got)
	}
	if got := toString(dig(policy, "spec", "webhookConfiguration", "failurePolicy")); got != "Ignore" {
		t.Errorf("failurePolicy = %q, want Ignore (a run admitted during an outage runs on the general workers)", got)
	}
	rules, _ := dig(policy, "spec", "rules").([]any)
	if len(rules) != 1 {
		t.Fatalf("%d rules, want 1", len(rules))
	}
	rule, _ := rules[0].(map[string]any)
	anyOf, _ := dig(rule, "match", "any").([]any)
	if len(anyOf) != 1 {
		t.Fatalf("match.any has %d entries, want 1", len(anyOf))
	}
	res, _ := dig(anyOf[0].(map[string]any), "resources").(map[string]any)
	if kinds, _ := res["kinds"].([]any); len(kinds) != 1 || kinds[0] != "Pod" {
		t.Errorf("kinds %v, want [Pod]", kinds)
	}
	if ops, _ := res["operations"].([]any); len(ops) != 1 || ops[0] != "CREATE" {
		t.Errorf("operations %v, want [CREATE]", ops)
	}
	if got := toString(dig(res, "selector", "matchLabels", "adhar.io/workload")); got != "batch" {
		t.Errorf("selector adhar.io/workload = %q, want batch: only batch run pods may be sent to the pool", got)
	}

	ctx := map[string]any{}
	for _, c := range rule["context"].([]any) {
		m := c.(map[string]any)
		ctx[toString(m["name"])] = m
	}
	lookup := toString(dig(ctx["poolNodes"].(map[string]any), "apiCall", "jmesPath"))
	wantLabel := `"` + globals.BatchNodePoolLabel + `" == '` + globals.BatchNodePoolValue + `'`
	if !strings.Contains(lookup, wantLabel) {
		t.Errorf("the pool lookup %q does not select %s=%s", lookup, globals.BatchNodePoolLabel, globals.BatchNodePoolValue)
	}
	if def := dig(ctx["poolNodes"].(map[string]any), "apiCall", "default"); fmt.Sprint(def) != "0" {
		t.Errorf("the pool lookup's default is %v, want 0: an unreadable node list must mutate nothing", def)
	}
	tol, _ := dig(ctx["poolToleration"].(map[string]any), "variable", "value").([]any)
	if len(tol) != 1 {
		t.Fatalf("poolToleration %v", tol)
	}
	tm := tol[0].(map[string]any)
	if tm["key"] != globals.BatchNodePoolTaintKey || tm["value"] != globals.BatchNodePoolTaintValue || tm["effect"] != "NoSchedule" || tm["operator"] != "Equal" {
		t.Errorf("the toleration %v does not tolerate the pool's taint %s=%s:NoSchedule", tm, globals.BatchNodePoolTaintKey, globals.BatchNodePoolTaintValue)
	}
	pref, _ := dig(ctx["poolPreference"].(map[string]any), "variable", "value").([]any)
	if len(pref) != 1 {
		t.Fatalf("poolPreference %v", pref)
	}
	exprs, _ := dig(pref[0].(map[string]any), "preference", "matchExpressions").([]any)
	if len(exprs) != 1 {
		t.Fatalf("preference %v", pref)
	}
	e := exprs[0].(map[string]any)
	if vals, _ := e["values"].([]any); e["key"] != globals.BatchNodePoolLabel || e["operator"] != "In" || len(vals) != 1 || vals[0] != globals.BatchNodePoolValue {
		t.Errorf("the preferred affinity %v does not name the pool's label %s=%s", e, globals.BatchNodePoolLabel, globals.BatchNodePoolValue)
	}
	// PREFERRED, never required: a full pool must not strand a run a general
	// worker could take.
	if strings.Contains(string(raw), "requiredDuringScheduling") {
		t.Error("the policy writes a REQUIRED affinity; a full pool would strand runs a general worker could take")
	}
	// The precondition that keeps a cluster without a pool unaffected.
	if !regexp.MustCompile(`key: "\{\{ poolNodes \}\}"\s+operator: GreaterThan\s+value: 0`).Match(raw) {
		t.Error("the rule no longer applies only while a pool node exists")
	}

	// Kyverno's admission controller can list nodes, or the lookup returns its
	// default and the policy never applies.
	rbac, err := os.ReadFile(filepath.Join(dir, "manifests", "rbac.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, chunk := range regexp.MustCompile(`(?m)^---\s*$`).Split(string(rbac), -1) {
		var doc map[string]any
		if err := sigsyaml.Unmarshal([]byte(chunk), &doc); err != nil || doc == nil || toString(dig(doc, "metadata", "name")) != "adhar-batch-kyverno-nodes" {
			continue
		}
		found = true
		if toString(dig(doc, "metadata", "labels", "rbac.kyverno.io/aggregate-to-admission-controller")) != "true" {
			t.Error("adhar-batch-kyverno-nodes does not aggregate into Kyverno's admission controller")
		}
		ok := false
		for _, r := range doc["rules"].([]any) {
			rm := r.(map[string]any)
			if strings.Contains(fmt.Sprint(rm["resources"]), "nodes") && strings.Contains(fmt.Sprint(rm["verbs"]), "list") {
				ok = true
			}
		}
		if !ok {
			t.Error("adhar-batch-kyverno-nodes does not grant list on nodes")
		}
	}
	if !found {
		t.Error("rbac.yaml has no adhar-batch-kyverno-nodes ClusterRole; Kyverno could not list nodes for adhar-batch-node-pool")
	}
}
