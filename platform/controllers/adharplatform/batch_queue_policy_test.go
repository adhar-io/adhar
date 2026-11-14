package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// core/adhar-batch's Volcano policies (manifests/volcano/kyverno-queue.yaml, design
// 0025 §4.3, §10.3, PLAT-2.4) bind a namespace to the leaf queue the Team composition
// composes. The kyverno CLI proves the rules (make policy-test,
// policy-tests/queue-binding and policy-tests/volcano-pod-standards); these tests hold
// the agreements the CLI cannot see: the name both halves compute, the labels one
// writes and the other reads, where the policies are synced from, and the copy the
// CLI loads.

func queuePolicyDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(stackPackagesDir(t), "core", "adhar-batch")
}

func queuePolicies(t *testing.T) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, d := range readYAMLDocs(t, filepath.Join(queuePolicyDir(t), "manifests", "volcano", "kyverno-queue.yaml")) {
		out[dig(d, "metadata", "name").(string)] = d
	}
	return out
}

func policyRules(p map[string]any) []map[string]any {
	var out []map[string]any
	for _, r := range dig(p, "spec", "rules").([]any) {
		out = append(out, r.(map[string]any))
	}
	return out
}

// The policy computes the leaf from the namespace's organisation, team and TIER —
// exactly the names the Team composition gives its queues — in every rule, and the
// Environment composition stamps that tier on every namespace.
func TestQueueBindingComputesTheLeavesTheTeamComposes(t *testing.T) {
	const want = `join('-', [ns."adhar.io/organisation", ns."adhar.io/team", ns."adhar.io/tier"])`
	ruled := 0
	for _, name := range []string{"adhar-batch-queue-default", "adhar-batch-queue-binding"} {
		p := queuePolicies(t)[name]
		if p == nil {
			t.Fatalf("no ClusterPolicy %s", name)
		}
		for _, r := range policyRules(p) {
			found := false
			for _, c := range r["context"].([]any) {
				m := c.(map[string]any)
				if m["name"] == "leaf" {
					found = true
					if got := strings.TrimSpace(dig(m, "variable", "jmesPath").(string)); got != want {
						t.Errorf("%s/%s computes the leaf as %s", name, r["name"], got)
					}
				}
				if m["name"] == "ns" && dig(m, "apiCall", "urlPath") != "/api/v1/namespaces/{{ request.namespace }}" {
					t.Errorf("%s/%s reads the namespace from %v", name, r["name"], dig(m, "apiCall", "urlPath"))
				}
			}
			if !found {
				t.Errorf("%s/%s has no leaf", name, r["name"])
			}
			// Only namespaces that carry all three labels have a leaf.
			for _, a := range dig(r, "match", "any").([]any) {
				sel := toSlice(dig(a.(map[string]any), "resources", "namespaceSelector", "matchExpressions"))
				keys := map[string]bool{}
				for _, e := range sel {
					keys[toString(e.(map[string]any)["key"])] = true
				}
				if !keys["adhar.io/organisation"] || !keys["adhar.io/team"] || !keys["adhar.io/tier"] {
					t.Errorf("%s/%s matches namespaces by %v", name, r["name"], keys)
				}
			}
			ruled++
		}
	}
	if ruled < 8 {
		t.Errorf("only %d rules carry the leaf", ruled)
	}
	docs, _ := tqRender(t, tqXR(t), tqCluster(t, nil), nil)
	for _, tier := range []string{"dev", "test", "prod"} {
		q := tqQueues(docs)["acme-payments-"+tier]
		if q == nil {
			t.Errorf("the Team composition composes no leaf acme-payments-%s for the policy to bind", tier)
			continue
		}
		// What the-team-queue-belongs-to-the-team reads off the Queue.
		for k, v := range map[string]string{"adhar.io/organisation": "acme", "adhar.io/team": "payments"} {
			if dig(q, "metadata", "labels", k) != v {
				t.Errorf("leaf %s labels %s = %v; the binding reads it to tell the team's queue from a namesake's", tier, k, dig(q, "metadata", "labels", k))
			}
		}
	}
	env := readConfigurationFile(t, "compositions", "env", "local.yaml")
	if !strings.Contains(env, `adhar.io/tier: {{ $tier | quote }}`) {
		t.Error("the Environment composition no longer stamps adhar.io/tier on its namespace; the binding reads it")
	}
	if !regexp.MustCompile(`enum:\s*\n\s*- dev\s*\n\s*- test\s*\n\s*- prod`).MatchString(readConfigurationFile(t, "xrd", "env.xrd.yaml")) {
		t.Error("an Environment's tier is no longer exactly dev|test|prod; the Team composes one leaf per tier")
	}
}

// The controllers that execute an admitted run are excluded by username in the
// webhook AND in every rule's preconditions, as kyverno-run-integrity.yaml does;
// nothing is excluded by label. Volcano's controllers everywhere; Kubernetes' Job
// and CronJob controllers (2026-11-14) only from the binding's rules that see what
// they create (pods, batch/v1 Jobs), and only for objects whose controller owner is
// a batch/v1 Job or CronJob, whose template the binding checked: without that, a
// scheduled single run on Volcano waits for Kyverno, and with the username alone
// `system:kube-controller-manager` (every controller, without per-controller
// credentials) would carry a Deployment's pods past the checks. The binding fails
// closed and the defaulting fails open (DEC-4): a run the default could not fix
// during an outage is refused by the binding anyway. The binding's last
// matchCondition only narrows the webhook to what Volcano schedules
// (TestFailClosedPoliciesReachOnlyWhatTheyCheck proves what all three let through,
// and policy-tests/queue-binding/{job-controller,kube-controller-manager} what the
// preconditions do).
func TestQueueBindingExcludesOnlyTheControllersThatRunWhatItAdmitted(t *testing.T) {
	const sa = "system:serviceaccount:adhar-system:volcano-controllers"
	jobControllers := []string{"system:serviceaccount:kube-system:job-controller", "system:serviceaccount:kube-system:cronjob-controller", "system:kube-controller-manager"}
	failure := map[string]string{"adhar-batch-queue-default": "Ignore", "adhar-batch-queue-binding": "Fail"}
	conditions := map[string][]string{
		"adhar-batch-queue-default": {"not-volcanos-own-controllers"},
		"adhar-batch-queue-binding": {"not-volcanos-own-controllers", "not-the-job-controllers-own-runs", "only-what-volcano-schedules"},
	}
	// The owner test, in CEL (webhook) and JMESPath (preconditions).
	ownerCEL := "r.apiVersion == 'batch/v1' && (r.kind == 'Job' || r.kind == 'CronJob') && has(r.controller) && r.controller"
	ownerJMES := "request.object.metadata.ownerReferences[?apiVersion == 'batch/v1' && (kind == 'Job' || kind == 'CronJob') && controller]"
	for _, name := range []string{"adhar-batch-queue-default", "adhar-batch-queue-binding"} {
		p := queuePolicies(t)[name]
		mc := toSlice(dig(p, "spec", "webhookConfiguration", "matchConditions"))
		var names []string
		for _, c := range mc {
			names = append(names, toString(c.(map[string]any)["name"]))
		}
		if strings.Join(names, ",") != strings.Join(conditions[name], ",") {
			t.Errorf("%s matchConditions are %v, want %v", name, names, conditions[name])
			continue
		}
		if !strings.Contains(toString(mc[0].(map[string]any)["expression"]), "request.userInfo.username != '"+sa+"'") {
			t.Errorf("%s matchConditions[0] = %v", name, mc[0])
		}
		if name == "adhar-batch-queue-binding" {
			expr := strings.Join(strings.Fields(toString(mc[1].(map[string]any)["expression"])), " ")
			for _, u := range jobControllers {
				if !strings.Contains(expr, "'"+u+"'") {
					t.Errorf("%s/not-the-job-controllers-own-runs does not name %s", name, u)
				}
			}
			if !strings.Contains(expr, ownerCEL) || !strings.HasPrefix(expr, "!(") {
				t.Errorf("%s/not-the-job-controllers-own-runs does not require the controller owner to be a batch/v1 Job or CronJob: %s", name, expr)
			}
		}
		if got := dig(p, "spec", "webhookConfiguration", "failurePolicy"); got != failure[name] {
			t.Errorf("%s failurePolicy = %v, want %s (DEC-4: the binding fails closed so a person's run cannot slip past an outage; the default fails open)", name, got, failure[name])
		}
		for _, r := range policyRules(p) {
			ok, jobs := false, false
			for _, c := range toSlice(dig(r, "preconditions", "all")) {
				m := c.(map[string]any)
				if m["key"] == "{{ request.userInfo.username }}" && m["operator"] == "NotEquals" && m["value"] == sa {
					ok = true
				}
				key := strings.Join(strings.Fields(toString(m["key"])), " ")
				named := true
				for _, u := range jobControllers {
					named = named && strings.Contains(key, "'"+u+"'")
				}
				if named && strings.Contains(key, "request.userInfo.username") && strings.Contains(key, ownerJMES) && m["operator"] == "Equals" && m["value"] == false {
					jobs = true
				}
			}
			if !ok {
				t.Errorf("%s/%s does not exclude %s in its own preconditions", name, r["name"], sa)
			}
			if dig(r, "exclude") != nil {
				t.Errorf("%s/%s excludes %v; only the controllers' username may skip it", name, r["name"], r["exclude"])
			}
			// The binding's rules that see pods or batch/v1 Jobs hold the Job
			// controllers' exclusion too; no other rule does.
			sees := false
			for _, a := range toSlice(dig(r, "match", "any")) {
				for _, k := range toSlice(dig(a.(map[string]any), "resources", "kinds")) {
					sees = sees || k == "Pod" || k == "batch/v1/Job"
				}
			}
			want := name == "adhar-batch-queue-binding" && sees
			if jobs != want {
				t.Errorf("%s/%s: excludes the Job and CronJob controllers' own runs in its preconditions = %v, want %v", name, r["name"], jobs, want)
			}
		}
	}
}

// The policies name kinds only Volcano serves, and Kyverno refuses a policy naming a
// kind the cluster does not serve. adhar-batch is enabled in every profile and Volcano
// in none, so they must not be in the directory adhar-batch's element syncs — which
// the ApplicationSet syncs non-recursively.
func TestQueueBindingIsNotSyncedWithoutVolcano(t *testing.T) {
	manifests := filepath.Join(queuePolicyDir(t), "manifests")
	entries, err := os.ReadDir(manifests)
	if err != nil {
		t.Fatal(err)
	}
	volcanoKinds := regexp.MustCompile(`(batch|scheduling|bus)\.volcano\.sh/v1(alpha1|beta1)/`)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(manifests, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if volcanoKinds.Match([]byte(blankComments(string(b)))) {
			t.Errorf("manifests/%s names a Volcano kind in a policy, and adhar-batch syncs it on clusters without Volcano", e.Name())
		}
	}
	for _, appset := range []string{"adhar-appset-local.yaml", "adhar-appset-production.yaml", "adhar-appset-gitops.yaml"} {
		b, err := os.ReadFile(filepath.Join(stackPackagesDir(t), "..", appset))
		if err != nil {
			t.Fatal(err)
		}
		if regexp.MustCompile(`(?m)^\s*recurse:\s*true`).Match(b) {
			t.Errorf("%s syncs directories recursively; manifests/volcano would reach every cluster through adhar-batch", appset)
		}
	}
}

// The element that syncs manifests/volcano (`adhar-batch-volcano`, Phase 2 stage 2) is
// enabled exactly where `volcano` is: without Volcano Kyverno refuses the policies, and
// with Volcano but without them any namespace may name any queue.
func TestQueueBindingIsSyncedWhereVolcanoIs(t *testing.T) {
	// An element is `- name: "<n>"`, then any comment lines, then `enabled:`.
	enabled := func(name string) *regexp.Regexp {
		return regexp.MustCompile(`- name: "` + regexp.QuoteMeta(name) + `"\n(?:[ \t]*#[^\n]*\n)*[ \t]*enabled: "(true|false)"\n(?:[^\n]*\n){0,3}?[ \t]*manifestPath: "([^"]+)"`)
	}
	files := []string{
		"adhar-appset-local.yaml", "adhar-appset-production.yaml", "adhar-appset-gitops.yaml",
		filepath.Join("environments", "local", "config.yaml"), filepath.Join("environments", "prod", "config.yaml"),
	}
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(stackPackagesDir(t), "..", f))
		if err != nil {
			t.Fatal(err)
		}
		volcano := enabled("volcano").FindSubmatch(b)
		policies := enabled("adhar-batch-volcano").FindSubmatch(b)
		if volcano == nil {
			t.Errorf("%s: no volcano element", f)
			continue
		}
		if policies == nil {
			t.Errorf("%s has no adhar-batch-volcano element", f)
			continue
		}
		if string(policies[2]) != "core/adhar-batch/manifests/volcano" {
			t.Errorf("%s: adhar-batch-volcano syncs %s, not core/adhar-batch/manifests/volcano", f, policies[2])
		}
		if string(volcano[1]) != string(policies[1]) {
			t.Errorf("%s: adhar-batch-volcano is enabled=%s and volcano enabled=%s; they go together", f, policies[1], volcano[1])
		}
	}
}

// The kyverno CLI cannot load matchConditions (it panics), so the tests load a copy
// without them: the copy must be the shipped policies in everything else.
func TestQueueBindingFixtureIsTheShippedPolicy(t *testing.T) {
	shipped, err := os.ReadFile(filepath.Join(queuePolicyDir(t), "manifests", "volcano", "kyverno-queue.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	want = append(want, "# GENERATED by generate-policy.sh from manifests/volcano/kyverno-queue.yaml, without matchConditions.")
	skip := false
	for _, line := range strings.Split(strings.TrimSuffix(string(shipped), "\n"), "\n") {
		switch {
		case line == "    matchConditions:":
			skip = true
			continue
		case skip && line == "  rules:":
			skip = false
		}
		if !skip {
			want = append(want, line)
		}
	}
	got, err := os.ReadFile(filepath.Join(queuePolicyDir(t), "policy-tests", "queue-binding", "policy-under-test.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSuffix(string(got), "\n") != strings.Join(want, "\n") {
		t.Error("policy-tests/queue-binding/policy-under-test.yaml is not the shipped policy without matchConditions; run its generate-policy.sh")
	}
	if strings.Contains(string(got), "\n    matchConditions:") {
		t.Error("the CLI copy still carries matchConditions")
	}
	if strings.Count(string(shipped), "matchConditions:") != 2 {
		t.Errorf("kyverno-queue.yaml has %d matchConditions blocks; the default and the binding policy each need one", strings.Count(string(shipped), "matchConditions:"))
	}
}

// Kyverno's admission controller reads the leaf Queue; the grant reaches it by the
// aggregation label the shipped Kyverno selects on, and grants nothing but reading Queues.
func TestQueueBindingCanReadTheQueue(t *testing.T) {
	docs := readYAMLDocs(t, filepath.Join(queuePolicyDir(t), "manifests", "volcano", "rbac.yaml"))
	if len(docs) != 1 || docs[0]["kind"] != "ClusterRole" {
		t.Fatalf("manifests/volcano/rbac.yaml = %v", docs)
	}
	const label = "rbac.kyverno.io/aggregate-to-admission-controller"
	if dig(docs[0], "metadata", "labels", label) != "true" {
		t.Errorf("the queue reader is not aggregated to Kyverno's admission controller: %v", dig(docs[0], "metadata", "labels"))
	}
	install, err := os.ReadFile(filepath.Join(stackPackagesDir(t), "security", "kyverno", "manifests", "install.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(install), label+`: "true"`) {
		t.Errorf("the shipped Kyverno no longer aggregates %s", label)
	}
	if got := tqJSON(docs[0]["rules"]); got != `[{"apiGroups":["scheduling.volcano.sh"],"resources":["queues"],"verbs":["get","list","watch"]}]` {
		t.Errorf("the queue reader grants %s", got)
	}
}
