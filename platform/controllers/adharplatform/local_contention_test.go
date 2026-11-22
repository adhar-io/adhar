package adharplatform

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Harbor's database must hold a CPU request and a liveness budget a loaded
// node can meet (2026-11-22). Without a request it got the smallest CPU share
// on the node, its healthcheck missed the 5 s timeout, kubelet killed a healthy
// Postgres five times in one local bring-up, and while it was down Harbor
// answered every registry login 401 — the kpack ClusterBuilder failed and no
// build ran.
func TestHarborDatabaseSurvivesALoadedNode(t *testing.T) {
	var found bool
	for _, doc := range yamlDocs(t, filepath.Join(stackPackagesDir(t), "application", "harbor", "manifests", "install.yaml")) {
		meta, _ := doc["metadata"].(map[string]any)
		if doc["kind"] != "StatefulSet" || meta["name"] != "harbor-database" {
			continue
		}
		found = true
		c := dig(doc, "spec", "template", "spec", "containers")
		containers, _ := c.([]any)
		for _, raw := range containers {
			ctr, _ := raw.(map[string]any)
			if ctr["name"] != "database" {
				continue
			}
			if dig(ctr, "resources", "requests", "cpu") == nil {
				t.Error("harbor-database has no CPU request: on a contended node it gets the smallest share and misses its probes")
			}
			timeout, _ := dig(ctr, "livenessProbe", "timeoutSeconds").(int)
			threshold, _ := dig(ctr, "livenessProbe", "failureThreshold").(int)
			period, _ := dig(ctr, "livenessProbe", "periodSeconds").(int)
			if budget := threshold * max(period, timeout); budget < 60 || timeout < 10 {
				t.Errorf("harbor-database liveness budget is timeout %ds x %d failures (period %ds); a loaded node needs timeout >= 10s and >= 60s of slow probes before a restart", timeout, threshold, period)
			}
		}
	}
	if !found {
		t.Fatal("no harbor-database StatefulSet in the harbor package")
	}
}

// The local budget must give a container with no CPU request a floor, not only
// cap the ones that have one: a request-less container gets 2 CPU shares
// against 51 for a capped one, and those were the pods that crash-looped on a
// busy local node (2026-11-22).
func TestTheLocalBudgetFloorsMissingCPURequests(t *testing.T) {
	policy := readStackFile(t, "security/kyverno/local-budget/cpu-request-cap.yaml")
	if !strings.Contains(policy, "name: floor-missing-container-cpu-requests") {
		t.Fatal("adhar-local-budget no longer floors containers that have no CPU request")
	}
	floor := policy[strings.Index(policy, "name: floor-missing-container-cpu-requests"):]
	if !strings.Contains(floor, "operator: Equals") || !strings.Contains(floor, `value: ""`) {
		t.Error("the floor rule must match only containers whose CPU request is empty")
	}
	if !regexp.MustCompile(`cpu: 25m`).MatchString(floor) {
		t.Error("the floor rule must set a small (25m) request; a larger one stops a 4-vCPU machine scheduling the platform")
	}
}

// The kpack nudge must not restart kpack faster than a ClusterBuilder
// reconcile can finish, and must probe Harbor where Harbor listens. Restarting
// every 30 s killed every in-flight reconcile on a loaded node (65-125 s each),
// and probing http:// on port 80 — which the Service does not serve — meant it
// never saw Harbor come up (2026-11-22).
func TestTheKpackNudgeLetsAReconcileFinish(t *testing.T) {
	job := readStackFile(t, "application/adhar-supply-chain/manifests/15-kpack-harbor-nudge.yaml")
	var code []string
	for _, line := range strings.Split(job, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			code = append(code, line)
		}
	}
	if regexp.MustCompile(`"http://(harbor-core|\$\{REG\})`).MatchString(strings.Join(code, "\n")) {
		t.Error("the nudge probes harbor-core over plain http; the Service serves only 443")
	}
	if !strings.Contains(job, "/api/v2.0/users/current") {
		t.Error("the nudge must wait until Harbor authenticates the push credential (a 401 also means its database is down)")
	}
	if strings.Contains(job, "seq 1 24") || strings.Contains(job, "sleep 30\n              done") {
		t.Error("the nudge restarts kpack in a tight loop; each restart kills the reconcile that would succeed")
	}
	if !strings.Contains(job, "for attempt in 1 2") {
		t.Error("the nudge must restart kpack-controller at most twice")
	}
}

// The local probe budget may only RAISE a probe value, only on containers that
// have that probe, and only in the local profile (2026-11-22). Lowering one
// would make a slow pod die sooner; patching a probe a container lacks would
// create a probe with no handler and the pod would be rejected; and a cloud
// node is sized for the probes its packages ship.
func TestTheLocalBudgetRelaxesProbesOnlyUpward(t *testing.T) {
	path := filepath.Join(stackPackagesDir(t), "security", "kyverno", "local-budget", "probe-budget.yaml")
	docs := yamlDocs(t, path)
	if len(docs) != 1 {
		t.Fatalf("probe-budget.yaml: want one ClusterPolicy, got %d documents", len(docs))
	}
	rules, _ := dig(docs[0], "spec", "rules").([]any)
	entries := 0
	for _, r := range rules {
		rule, _ := r.(map[string]any)
		foreach, _ := dig(rule, "mutate", "foreach").([]any)
		for _, f := range foreach {
			fe, _ := f.(map[string]any)
			entries++
			conds, _ := dig(fe, "preconditions", "all").([]any)
			var probe, field string
			var floor int
			guarded := false
			for _, c := range conds {
				cond, _ := c.(map[string]any)
				key, _ := cond["key"].(string)
				m := regexp.MustCompile(`element\.(\w+Probe)\.(\w+)`).FindStringSubmatch(key)
				if m == nil {
					continue
				}
				if m[2] == "periodSeconds" && cond["operator"] == "GreaterThan" {
					guarded = true
					continue
				}
				if cond["operator"] != "LessThan" {
					t.Errorf("%s.%s: precondition operator %v; only LessThan keeps the rule from lowering a value", m[1], m[2], cond["operator"])
				}
				probe, field = m[1], m[2]
				floor, _ = cond["value"].(int)
			}
			if !guarded {
				t.Errorf("a probe rule does not check the probe exists (periodSeconds > 0); it would add a probe with no handler")
			}
			ctrs, _ := dig(fe, "patchStrategicMerge", "spec", "containers").([]any)
			if len(ctrs) != 1 {
				t.Errorf("%s.%s: patch must name exactly one container", probe, field)
				continue
			}
			ctr, _ := ctrs[0].(map[string]any)
			set, _ := dig(ctr, probe, field).(int)
			if probe == "" || set != floor || floor == 0 {
				t.Errorf("%s.%s: patch sets %d but the floor checked is %d; a rule must set exactly the floor it tests", probe, field, set, floor)
			}
		}
	}
	if entries < 5 {
		t.Errorf("probe-budget.yaml has %d probe rules; want liveness timeout+threshold, readiness timeout, startup timeout+threshold", entries)
	}
	if !strings.Contains(readStackRootFile(t, "adhar-appset-production.yaml"), "name: \"adhar-local-budget\"\n            enabled: \"false\"") {
		t.Error("adhar-local-budget must stay disabled in the production profile; the probe budget is for a single local node")
	}
}

// The Nexus reconcile runs every 10 minutes and must not PUT a repository
// whose live configuration already holds what it wants: Nexus restarts a
// repository on every config update, so an unconditional PUT bounced
// maven-central and maven-public every ten minutes, in-flight builds got 500
// ("Invalid state: STOPPED"), Maven cached the failures and the adhar-kit
// release timed out at 1 h (2026-11-22).
func TestNexusReconcileDoesNotBounceUnchangedRepositories(t *testing.T) {
	script := readStackFile(t, "application/nexus/manifests/config-job.yaml")
	start := strings.Index(script, "    upsertrepo() {")
	if start < 0 {
		t.Fatal("config-job.yaml has no upsertrepo")
	}
	body := script[start:]
	body = body[:strings.Index(body, "\n    }\n")]
	get := strings.Index(body, `"${API}/repositories/$1/$2"`)
	contains := strings.Index(body, `contains($want)`)
	put := strings.Index(body, "-X PUT")
	if get < 0 || contains < 0 || put < 0 || !(get < contains && contains < put) {
		t.Fatalf("upsertrepo must GET the live repository and return when it already contains the desired config, BEFORE any PUT (get=%d contains=%d put=%d)", get, contains, put)
	}
	if !strings.Contains(body[contains:put], "return 0") {
		t.Error("upsertrepo must return without writing when the live config already matches")
	}
}
