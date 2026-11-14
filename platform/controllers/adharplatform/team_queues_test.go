package adharplatform

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"
)

// The Team composition composes the team's Volcano leaf queues where Volcano runs
// (design 0025 §4.3, PLAT-2.3): `<organisation>-<team>-<dev|test|prod>` under the
// root queue, capped by the smaller of the team's allowance and the batch ceiling,
// with only prod declaring a deserved share; a ClusterRole/Binding granting the
// team's group `get` on exactly those three; and the BatchQueuesReady condition,
// QueueRejected when Volcano's queue webhook refuses one. These tests render it the
// way function-go-templating does, satisfying its required resources pass by pass
// as Crossplane does, and through `crossplane render` when Docker is available.

const tqFixtureDir = "testdata/team"

func tqXR(t *testing.T) map[string]any {
	t.Helper()
	docs := readYAMLDocs(t, filepath.Join(tqFixtureDir, "xr.yaml"))
	if len(docs) != 1 || docs[0]["kind"] != "Team" {
		t.Fatal("xr.yaml must hold exactly one Team")
	}
	return docs[0]
}

// tqCluster is required-resources.yaml without the named objects, plus extra ones.
func tqCluster(t *testing.T, without []string, extra ...map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
docs:
	for _, d := range readYAMLDocs(t, filepath.Join(tqFixtureDir, "required-resources.yaml")) {
		for _, w := range without {
			if dig(d, "metadata", "name") == w {
				continue docs
			}
		}
		out = append(out, d)
	}
	return append(out, extra...)
}

func tqObserved(t *testing.T, file string) map[string]any {
	t.Helper()
	out := map[string]any{}
	for _, d := range readYAMLDocs(t, filepath.Join(tqFixtureDir, file)) {
		out[dig(d, "metadata", "annotations", "crossplane.io/composition-resource-name").(string)] = map[string]any{"resource": d}
	}
	return out
}

// tqRequirements is the rendered ExtraResources' requirements, keyed by name.
func tqRequirements(docs []map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, d := range docs {
		if d["kind"] != "ExtraResources" {
			continue
		}
		reqs, _ := d["requirements"].(map[string]any)
		for k, v := range reqs {
			out[k], _ = v.(map[string]any)
		}
	}
	return out
}

// tqRender calls the step until what it requires stops changing, handing it what
// the cluster holds each time — Crossplane's loop.
func tqRender(t *testing.T, xr map[string]any, cluster []map[string]any, observed map[string]any) ([]map[string]any, int) {
	t.Helper()
	req := map[string]any{"observed": map[string]any{"composite": map[string]any{"resource": xr}, "resources": observed}}
	prev := ""
	for pass := 1; pass <= 5; pass++ {
		docs := renderGoTemplate(t, req, "compositions", "team", "local.yaml")
		reqs := tqRequirements(docs)
		sig, _ := json.Marshal(reqs)
		if string(sig) == prev {
			return docs, pass
		}
		prev = string(sig)
		required := map[string]any{}
		for key, sel := range reqs {
			var items []any
			for _, obj := range cluster {
				if obj["apiVersion"] == sel["apiVersion"] && obj["kind"] == sel["kind"] &&
					dig(obj, "metadata", "name") == sel["matchName"] &&
					fmt.Sprint(dig(obj, "metadata", "namespace")) == fmt.Sprint(sel["namespace"]) {
					items = append(items, map[string]any{"resource": obj})
				}
			}
			required[key] = map[string]any{"items": items}
		}
		req["requiredResources"] = required
	}
	t.Fatal("the Team composition's requirements never stabilise; Crossplane gives up after a few passes")
	return nil, 0
}

func tqResource(docs []map[string]any, name string) map[string]any {
	for _, d := range docs {
		if dig(d, "metadata", "annotations", "gotemplating.fn.crossplane.io/composition-resource-name") == name {
			return d
		}
	}
	return nil
}

func tqCondition(t *testing.T, docs []map[string]any) map[string]any {
	t.Helper()
	for _, d := range docs {
		if d["kind"] != "ClaimConditions" {
			continue
		}
		for _, c := range d["conditions"].([]any) {
			if m := c.(map[string]any); m["type"] == "BatchQueuesReady" {
				return m
			}
		}
	}
	t.Fatal("the Team composition sets no BatchQueuesReady condition")
	return nil
}

func tqQueues(docs []map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, m := range manifestsOfKind(docs, "Queue") {
		out[dig(m, "metadata", "name").(string)] = m
	}
	return out
}

func tqQueue(name string, spec map[string]any, labels map[string]any) map[string]any {
	return map[string]any{"apiVersion": "scheduling.volcano.sh/v1beta1", "kind": "Queue",
		"metadata": map[string]any{"name": name, "labels": labels}, "spec": spec}
}

func tqJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// Without Volcano nothing is composed and nothing of Volcano's is even asked for:
// a required resource of a kind the cluster does not serve fails the whole reconcile,
// so the Queue lookups wait for the descriptor AND the CRD.
func TestTeamWithoutVolcanoComposesNoQueues(t *testing.T) {
	for _, c := range []struct {
		why     string
		without []string
	}{
		{"no descriptor and no CRD (every profile today)", []string{"adhar-batch-scheduler", "queues.scheduling.volcano.sh"}},
		{"a descriptor but no Queue CRD yet", []string{"queues.scheduling.volcano.sh"}},
		{"the CRD but no descriptor (Volcano being removed, A2.10)", []string{"adhar-batch-scheduler"}},
	} {
		docs, _ := tqRender(t, tqXR(t), tqCluster(t, c.without), nil)
		if q := tqQueues(docs); len(q) != 0 {
			t.Errorf("%s: composes queues %v", c.why, q)
		}
		for _, n := range []string{"batch-queue-reader", "batch-queue-reader-binding"} {
			if tqResource(docs, n) != nil {
				t.Errorf("%s: composes %s", c.why, n)
			}
		}
		for key, sel := range tqRequirements(docs) {
			if sel["kind"] == "Queue" {
				t.Errorf("%s: requires Queue %v (%s) on a cluster that may not serve the kind", c.why, sel["matchName"], key)
			}
		}
		if cond := tqCondition(t, docs); cond["status"] != "True" || cond["reason"] != "NativeScheduler" {
			t.Errorf("%s: BatchQueuesReady = %v", c.why, cond)
		}
		if st := docOfKind(docs, "Team"); dig(st, "status", "batchQueues") != nil {
			t.Errorf("%s: publishes batchQueues %v", c.why, dig(st, "status", "batchQueues"))
		}
	}
	// A descriptor that names the native scheduler is the native path too.
	desc := tqCluster(t, nil)[0]
	native := map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": desc["metadata"],
		"data": map[string]any{"scheduler": "kubernetes", "rootQueue": "adhar-batch"}}
	docs, _ := tqRender(t, tqXR(t), tqCluster(t, []string{"adhar-batch-scheduler"}, native), nil)
	if len(tqQueues(docs)) != 0 || tqCondition(t, docs)["reason"] != "NativeScheduler" {
		t.Errorf("scheduler kubernetes in the descriptor still composes queues: %v", tqCondition(t, docs))
	}
}

// §4.3: three leaves, the team × tier, under the descriptor's root queue.
func TestTeamComposesItsThreeLeafQueues(t *testing.T) {
	docs, passes := tqRender(t, tqXR(t), tqCluster(t, nil), tqObserved(t, "observed-open.yaml"))
	if passes < 3 {
		t.Errorf("rendered in %d passes; the root queue and leaves can only be asked for after the descriptor and CRD are seen", passes)
	}
	labels := func(tier string) map[string]any {
		return map[string]any{"adhar.io/organisation": "acme", "adhar.io/team": "payments", "adhar.io/batch-queue": "true", "adhar.io/tier": tier}
	}
	capability := map[string]any{"cpu": "8", "memory": "24Gi"}
	want := map[string]map[string]any{
		"acme-payments-dev": tqQueue("acme-payments-dev", map[string]any{"parent": "adhar-batch", "capability": capability,
			"reclaimable": true, "priority": float64(100)}, labels("dev")),
		"acme-payments-test": tqQueue("acme-payments-test", map[string]any{"parent": "adhar-batch", "capability": capability,
			"reclaimable": true, "priority": float64(200)}, labels("test")),
		"acme-payments-prod": tqQueue("acme-payments-prod", map[string]any{"parent": "adhar-batch", "capability": capability,
			"deserved": map[string]any{"cpu": "2", "memory": "4Gi"}, "reclaimable": false, "priority": float64(300)}, labels("prod")),
	}
	got := tqQueues(docs)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("leaf queues:\n got %s\nwant %s", tqJSON(got), tqJSON(want))
	}
	for _, tier := range []string{"dev", "test", "prod"} {
		o := tqResource(docs, "batch-queue-"+tier)
		if o == nil {
			t.Fatalf("no batch-queue-%s", tier)
		}
		if dig(o, "spec", "deletionPolicy") != "Orphan" {
			t.Errorf("batch-queue-%s deletionPolicy = %v: deleting an Open queue is refused by Volcano and would wedge the Team", tier, dig(o, "spec", "deletionPolicy"))
		}
		if dig(o, "spec", "providerConfigRef", "name") != "kubernetes-provider" || dig(o, "spec", "providerConfigRef", "kind") != "ClusterProviderConfig" {
			t.Errorf("batch-queue-%s providerConfigRef = %v", tier, dig(o, "spec", "providerConfigRef"))
		}
	}
	st := docOfKind(docs, "Team")
	if got := tqJSON(dig(st, "status", "batchQueues")); got != `["acme-payments-dev","acme-payments-test","acme-payments-prod"]` {
		t.Errorf("status.batchQueues = %s", got)
	}
	if st == nil || dig(st, "status", "group") != "ws-team-acme.payments" {
		t.Errorf("the team's own status is lost: %v", st)
	}
}

// Capability is the smaller of the allowance and the ceiling per axis, as the quota
// webhook reads the allowance: the team's own entry, else `default`; an absent or
// zero axis is unlimited. Prod's deserved never exceeds the leaf's capability.
func TestTeamQueueCapabilityIsTheSmallerOfAllowanceAndCeiling(t *testing.T) {
	quotas := func(data map[string]any) map[string]any {
		return map[string]any{"apiVersion": "v1", "kind": "ConfigMap",
			"metadata": map[string]any{"name": "adhar-tenant-quotas", "namespace": "adhar-system"}, "data": data}
	}
	root := func(capability map[string]any) map[string]any {
		return map[string]any{"apiVersion": "scheduling.volcano.sh/v1beta1", "kind": "Queue",
			"metadata": map[string]any{"name": "adhar-batch"}, "spec": map[string]any{"capability": capability}}
	}
	for _, c := range []struct {
		why                   string
		quotas, root          map[string]any
		capability, prodShare string
	}{
		{"no allowance ConfigMap: the ceiling", nil, nil,
			`{"cpu":"22","memory":"24Gi"}`, `{"cpu":"2","memory":"4Gi"}`},
		{"only a default entry applies to a team without its own", quotas(map[string]any{"default": "cpu: \"32\"\nmemory: 512Mi\n"}), nil,
			`{"cpu":"22","memory":"512Mi"}`, `{"cpu":"2","memory":"512Mi"}`},
		{"the team's entry wins over default", quotas(map[string]any{"default": "cpu: 1\n", "payments": "cpu: 4\n"}), nil,
			`{"cpu":"4","memory":"24Gi"}`, `{"cpu":"2","memory":"4Gi"}`},
		{"zero is unlimited, as the webhook reads it", quotas(map[string]any{"payments": "cpu: 0\nmemory: \"0\"\n"}), nil,
			`{"cpu":"22","memory":"24Gi"}`, `{"cpu":"2","memory":"4Gi"}`},
		{"units are compared, not strings", quotas(map[string]any{"payments": "cpu: 21500m\nmemory: 20480Mi\n"}), nil,
			`{"cpu":"21500m","memory":"20480Mi"}`, `{"cpu":"2","memory":"4Gi"}`},
		{"decimal and binary suffixes", quotas(map[string]any{"payments": "cpu: \"1.5\"\nmemory: 25G\n"}), nil,
			`{"cpu":"1.5","memory":"25G"}`, `{"cpu":"1.5","memory":"4Gi"}`},
		{"an equal allowance keeps the ceiling's spelling", quotas(map[string]any{"payments": "memory: 25769803776\n"}), nil,
			`{"cpu":"22","memory":"24Gi"}`, `{"cpu":"2","memory":"4Gi"}`},
		{"an unreadable allowance never undercuts the ceiling", quotas(map[string]any{"payments": "cpu: lots\nmemory: 2Zi\n"}), nil,
			`{"cpu":"22","memory":"24Gi"}`, `{"cpu":"2","memory":"4Gi"}`},
		{"a numeric ceiling (protojson numbers) and other axes pass through", nil, root(map[string]any{"cpu": float64(16), "memory": "32Gi", "nvidia.com/gpu": float64(2)}),
			`{"cpu":16,"memory":"32Gi","nvidia.com/gpu":2}`, `{"cpu":"2","memory":"4Gi"}`},
		// A bare byte count arrives as a float64 that toString spells in e-notation;
		// read as unreadable, it handed the leaf the whole ceiling (2026-11-14).
		{"a numeric ceiling of a million or more still yields to the allowance", quotas(map[string]any{"payments": "cpu: 4\nmemory: 6Gi\n"}), root(map[string]any{"cpu": float64(22), "memory": float64(25769803776)}),
			`{"cpu":"4","memory":"6Gi"}`, `{"cpu":"2","memory":"4Gi"}`},
		{"prod's deserved is clamped to a numeric ceiling", nil, root(map[string]any{"cpu": float64(22), "memory": float64(2147483648)}),
			`{"cpu":22,"memory":2147483648}`, `{"cpu":"2","memory":2147483648}`},
		{"a ceiling without a memory axis takes the allowance's", quotas(map[string]any{"payments": "memory: 6Gi\n"}), root(map[string]any{"cpu": "10"}),
			`{"cpu":"10","memory":"6Gi"}`, `{"cpu":"2","memory":"4Gi"}`},
		{"prod's deserved is clamped to the capability", quotas(map[string]any{"payments": "cpu: 500m\nmemory: 1Gi\n"}), nil,
			`{"cpu":"500m","memory":"1Gi"}`, `{"cpu":"500m","memory":"1Gi"}`},
	} {
		without := []string{"adhar-tenant-quotas"}
		var extra []map[string]any
		if c.quotas != nil {
			extra = append(extra, c.quotas)
		}
		if c.root != nil {
			without = append(without, "adhar-batch")
			extra = append(extra, c.root)
		}
		docs, _ := tqRender(t, tqXR(t), tqCluster(t, without, extra...), nil)
		q := tqQueues(docs)
		for _, n := range []string{"acme-payments-dev", "acme-payments-test", "acme-payments-prod"} {
			if got := tqJSON(dig(q[n], "spec", "capability")); got != c.capability {
				t.Errorf("%s: %s capability = %s, want %s", c.why, n, got, c.capability)
			}
		}
		if got := tqJSON(dig(q["acme-payments-prod"], "spec", "deserved")); got != c.prodShare {
			t.Errorf("%s: prod deserved = %s, want %s", c.why, got, c.prodShare)
		}
		for _, n := range []string{"acme-payments-dev", "acme-payments-test"} {
			if d := dig(q[n], "spec", "deserved"); d != nil {
				t.Errorf("%s: %s declares deserved %v; only prod does, which is what lets prod reclaim from it", c.why, n, d)
			}
		}
	}
	// The descriptor's share is what prod asks for.
	desc := tqCluster(t, nil)[0]
	data := map[string]any{}
	for k, v := range desc["data"].(map[string]any) {
		data[k] = v
	}
	data["prodDeservedCPU"], data["prodDeservedMemory"] = "3", "6Gi"
	custom := map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": desc["metadata"], "data": data}
	docs, _ := tqRender(t, tqXR(t), tqCluster(t, []string{"adhar-batch-scheduler"}, custom), nil)
	if got := tqJSON(dig(tqQueues(docs)["acme-payments-prod"], "spec", "deserved")); got != `{"cpu":"3","memory":"6Gi"}` {
		t.Errorf("prod deserved from the descriptor = %s", got)
	}
}

// The composition has no quantity type, so it parses quantities itself; it must
// order them as Kubernetes does — the quota webhook reads the same allowance with
// resource.ParseQuantity — or a team's leaf takes the ceiling it was never given.
// Each row is (allowance, ceiling) on both axes, and the expected capability comes
// from resource.Quantity, not from the row. (The composition compares at milli
// granularity, rounding up; no row is closer than that.)
func TestTeamQueueQuantitiesCompareAsKubernetesComparesThem(t *testing.T) {
	pairs := [][2]string{
		// A fractional Ti of four or more decimals overflowed int64 and read as
		// unlimited, so the ceiling won (2026-11-14).
		{"0.123456Ti", "200Gi"}, {"0.1234567891Ti", "127Gi"}, {"1.999999Ti", "2Ti"},
		// Suffixes and spellings the webhook accepts and the composition once did not.
		{"1e3", "24Gi"}, {"1.5e9", "2G"}, {"2E3", "24Gi"}, {"5e-1", "1"}, {"3000000n", "4m"},
		{"1500u", "3m"}, {"1Pi", "2Pi"}, {"2P", "1Pi"}, {"7Pi", "1E"}, {"+2", "3"}, {".5", "1"},
		{"1.", "2"}, {"0.001Ei", "1Ei"},
		// An 18-digit mantissa with a binary suffix and pending decimals: the ×2 steps
		// would overflow unless a pending ÷10 is taken first.
		{"0.123456789012345678Ki", "127"}, {"0.123456789012345678Ki", "126"},
		// The allowance above the ceiling: the ceiling stays.
		{"25Gi", "24Gi"}, {"3", "2500m"}, {"1E", "8Pi"}, {"2.5e10", "24Gi"},
		// Long and padded mantissas.
		{"000000000000000000000001.5", "2"}, {"1.000000000000000000000001", "2"},
		{"0.000000000000000000000000001", "1"}, {"5.0000000000000000000001", "4"},
		// Under 1m reads as 1m, as Quantity.MilliValue rounds it; flooring it to 0
		// read the allowance as unlimited.
		{"500u", "1"}, {"1n", "1"}, {"0.0001", "1"}, {"1e-50", "1"},
	}
	for _, p := range pairs {
		a, b := resource.MustParse(p[0]), resource.MustParse(p[1])
		want := p[1]
		if a.Sign() > 0 && a.Cmp(b) < 0 {
			want = p[0]
		}
		quotas := map[string]any{"apiVersion": "v1", "kind": "ConfigMap",
			"metadata": map[string]any{"name": "adhar-tenant-quotas", "namespace": "adhar-system"},
			"data":     map[string]any{"payments": fmt.Sprintf("cpu: %q\nmemory: %q\n", p[0], p[0])}}
		root := map[string]any{"apiVersion": "scheduling.volcano.sh/v1beta1", "kind": "Queue",
			"metadata": map[string]any{"name": "adhar-batch"},
			"spec":     map[string]any{"capability": map[string]any{"cpu": p[1], "memory": p[1]}}}
		docs, _ := tqRender(t, tqXR(t), tqCluster(t, []string{"adhar-tenant-quotas", "adhar-batch"}, quotas, root), nil)
		capability, _ := dig(tqQueues(docs)["acme-payments-dev"], "spec", "capability").(map[string]any)
		for _, axis := range []string{"cpu", "memory"} {
			if got := dig(capability, axis); got != want {
				t.Errorf("allowance %s, ceiling %s: %s capability = %v, want %s (Kubernetes orders %s %s %s)",
					p[0], p[1], axis, got, want, p[0], map[int]string{-1: "<", 0: "=", 1: ">"}[a.Cmp(b)], p[1])
			}
		}
	}
	// Past int64 milli-units (~9.2 PB) both sides saturate and read as equal, and an
	// equal allowance keeps the ceiling — the one Volcano has already accepted. Never
	// negative, never the wrapped value.
	quotas := map[string]any{"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": "adhar-tenant-quotas", "namespace": "adhar-system"},
		"data":     map[string]any{"payments": "memory: 0.5Ei\n"}}
	root := map[string]any{"apiVersion": "scheduling.volcano.sh/v1beta1", "kind": "Queue",
		"metadata": map[string]any{"name": "adhar-batch"},
		"spec":     map[string]any{"capability": map[string]any{"cpu": "22", "memory": "1Ei"}}}
	docs, _ := tqRender(t, tqXR(t), tqCluster(t, []string{"adhar-tenant-quotas", "adhar-batch"}, quotas, root), nil)
	if got := tqJSON(dig(tqQueues(docs)["acme-payments-dev"], "spec", "capability")); got != `{"cpu":"22","memory":"1Ei"}` {
		t.Errorf("two saturated quantities: capability = %s, want the ceiling", got)
	}
}

// The team's people may read their own three queues and nobody else's.
func TestTeamQueueReaderGrantsOnlyItsLeaves(t *testing.T) {
	docs, _ := tqRender(t, tqXR(t), tqCluster(t, nil), nil)
	role := composedManifest(docs, "ClusterRole", "adhar-batch-queues-acme-payments")
	if role == nil {
		t.Fatal("no ClusterRole adhar-batch-queues-acme-payments")
	}
	if got := tqJSON(role["rules"]); got != `[{"apiGroups":["scheduling.volcano.sh"],"resourceNames":["acme-payments-dev","acme-payments-test","acme-payments-prod"],"resources":["queues"],"verbs":["get"]}]` {
		t.Errorf("the queue reader grants %s", got)
	}
	binding := composedManifest(docs, "ClusterRoleBinding", "adhar-batch-queues-acme-payments")
	if binding == nil {
		t.Fatal("no ClusterRoleBinding adhar-batch-queues-acme-payments")
	}
	if got := tqJSON(binding["roleRef"]); got != `{"apiGroup":"rbac.authorization.k8s.io","kind":"ClusterRole","name":"adhar-batch-queues-acme-payments"}` {
		t.Errorf("roleRef = %s", got)
	}
	if got := bindingSubjects(binding); fmt.Sprint(got) != "[oidc:ws-team-acme.payments]" {
		t.Errorf("subjects = %v; the team is the organisation-qualified Keycloak group", got)
	}
}

// BatchQueuesReady follows the composed Objects; a refusal by Volcano's webhook is
// QueueRejected with Volcano's message, and none of it holds the Team unready.
func TestTeamBatchQueuesReadyFollowsTheQueues(t *testing.T) {
	const ready = "gotemplating.fn.crossplane.io/ready"
	for _, c := range []struct {
		observed, status, reason string
		message                  []string
	}{
		{"", "False", "Creating", []string{"waiting for acme-payments-dev, acme-payments-test, acme-payments-prod"}},
		{"observed-open.yaml", "True", "QueuesOpen", []string{"acme-payments-dev, acme-payments-test, acme-payments-prod under adhar-batch"}},
		{"observed-rejected.yaml", "False", "QueueRejected", []string{"acme-payments-prod: cannot create object: admission webhook \"validatequeue.volcano.sh\" denied the request",
			"exceeds its deserved", "Queue adhar-batch", `{"cpu":"2","memory":"4Gi"}`}},
		{"observed-closed.yaml", "False", "QueueNotOpen", []string{"acme-payments-test is Closed"}},
	} {
		var observed map[string]any
		if c.observed != "" {
			observed = tqObserved(t, c.observed)
		}
		docs, _ := tqRender(t, tqXR(t), tqCluster(t, nil), observed)
		cond := tqCondition(t, docs)
		if cond["status"] != c.status || cond["reason"] != c.reason {
			t.Errorf("%q: BatchQueuesReady = %v/%v, want %s/%s (%v)", c.observed, cond["status"], cond["reason"], c.status, c.reason, cond["message"])
		}
		for _, m := range c.message {
			if !strings.Contains(fmt.Sprint(cond["message"]), m) {
				t.Errorf("%q: message %q lacks %q", c.observed, cond["message"], m)
			}
		}
		if cond["target"] != "Composite" {
			t.Errorf("%q: the condition targets %v", c.observed, cond["target"])
		}
		want := "True"
		if c.observed == "" {
			want = "False"
		}
		for _, tier := range []string{"dev", "test", "prod"} {
			if got := dig(tqResource(docs, "batch-queue-"+tier), "metadata", "annotations", ready); got != want {
				t.Errorf("%q: batch-queue-%s ready = %v, want %s: a queue Volcano refuses must not hold the Team unready", c.observed, tier, got, want)
			}
		}
	}
	// A sync error that is not an admission refusal is not called one.
	observed := tqObserved(t, "observed-rejected.yaml")
	prod := observed["batch-queue-prod"].(map[string]any)["resource"].(map[string]any)
	prod["status"].(map[string]any)["conditions"].([]any)[1].(map[string]any)["message"] = "cannot get object: connection refused"
	if cond := tqCondition(t, func() []map[string]any { d, _ := tqRender(t, tqXR(t), tqCluster(t, nil), observed); return d }()); cond["reason"] != "QueueNotSynced" {
		t.Errorf("a connection error reads as %v", cond)
	}
}

// `<org>-<team>` is ambiguous: a leaf name another (organisation, team) already holds
// — or that nobody labelled — is refused, never adopted.
func TestTeamRefusesALeafAnotherTeamHolds(t *testing.T) {
	held := func(labels map[string]any) map[string]any {
		return map[string]any{"apiVersion": "scheduling.volcano.sh/v1beta1", "kind": "Queue",
			"metadata": map[string]any{"name": "acme-payments-test", "labels": labels}, "spec": map[string]any{"parent": "adhar-batch"}}
	}
	for _, c := range []struct {
		why    string
		labels map[string]any
		taken  bool
	}{
		{"(acme-pay, ments) spells the same leaf", map[string]any{"adhar.io/organisation": "acme-pay", "adhar.io/team": "ments"}, true},
		{"a hand-made queue nobody labelled", nil, true},
		{"its own queue, kept after a Team was re-created (Orphan)", map[string]any{"adhar.io/organisation": "acme", "adhar.io/team": "payments"}, false},
	} {
		docs, _ := tqRender(t, tqXR(t), tqCluster(t, nil, held(c.labels)), nil)
		cond := tqCondition(t, docs)
		if c.taken {
			if len(tqQueues(docs)) != 0 || tqResource(docs, "batch-queue-reader") != nil {
				t.Errorf("%s: composes over it", c.why)
			}
			if cond["status"] != "False" || cond["reason"] != "QueueNameTaken" || !strings.Contains(fmt.Sprint(cond["message"]), "acme-payments-test is held by") {
				t.Errorf("%s: BatchQueuesReady = %v", c.why, cond)
			}
		} else if len(tqQueues(docs)) != 3 {
			t.Errorf("%s: composes %d queues", c.why, len(tqQueues(docs)))
		}
	}
}

// Volcano without its root queue: nothing to hang the leaves on.
func TestTeamWithoutTheRootQueueSaysSo(t *testing.T) {
	docs, _ := tqRender(t, tqXR(t), tqCluster(t, []string{"adhar-batch"}), nil)
	if len(tqQueues(docs)) != 0 {
		t.Error("composes leaves without their parent; Volcano's webhook refuses a queue whose parent does not exist")
	}
	if cond := tqCondition(t, docs); cond["status"] != "False" || cond["reason"] != "RootQueueMissing" {
		t.Errorf("BatchQueuesReady = %v", cond)
	}
}

// The status field the composition writes is in the (structural, strict) schema, or
// the API server prunes it.
func TestTeamXRDCarriesBatchQueues(t *testing.T) {
	x := readYAMLDocs(t, filepath.Join(controlPlaneDir(t), "xrd/team.xrd.yaml"))[0]
	versions := dig(x, "spec", "versions").([]any)
	props := dig(versions[0].(map[string]any), "schema", "openAPIV3Schema", "properties", "status", "properties").(map[string]any)
	if dig(props, "batchQueues", "type") != "array" || dig(props, "batchQueues", "items", "type") != "string" {
		t.Errorf("status.batchQueues = %v", props["batchQueues"])
	}
}

// Golden renders of the queue half of the composition (the rest of the Team is
// covered elsewhere): Volcano with every leaf open, and with prod refused.
// ADHAR_UPDATE_GOLDEN=1 rewrites them.
func TestTeamQueueGoldenRenders(t *testing.T) {
	for _, c := range []struct{ observed, golden string }{
		{"observed-open.yaml", "golden-open.yaml"},
		{"observed-rejected.yaml", "golden-rejected.yaml"},
	} {
		docs, _ := tqRender(t, tqXR(t), tqCluster(t, nil), tqObserved(t, c.observed))
		var keep []map[string]any
		for _, d := range docs {
			name, _ := dig(d, "metadata", "annotations", "gotemplating.fn.crossplane.io/composition-resource-name").(string)
			if strings.HasPrefix(name, "batch-queue") || d["kind"] == "ClaimConditions" || d["kind"] == "ExtraResources" {
				keep = append(keep, d)
			}
		}
		sort.SliceStable(keep, func(i, j int) bool { return tqJSON(keep[i]) < tqJSON(keep[j]) })
		var out bytes.Buffer
		out.WriteString("# Generated by TestTeamQueueGoldenRenders from " + c.observed + "; ADHAR_UPDATE_GOLDEN=1 rewrites it.\n")
		for i, d := range keep {
			b, err := yaml.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			if i > 0 {
				out.WriteString("---\n")
			}
			out.Write(b)
		}
		path := filepath.Join(tqFixtureDir, c.golden)
		if os.Getenv("ADHAR_UPDATE_GOLDEN") == "1" {
			if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v (ADHAR_UPDATE_GOLDEN=1 writes it)", err)
		}
		if !bytes.Equal(want, out.Bytes()) {
			t.Errorf("%s differs from the render; review the change and rerun with ADHAR_UPDATE_GOLDEN=1:\n%s", c.golden, out.String())
		}
	}
}

// The identity that applies composed Objects is provider-kubernetes': it must be
// able to manage Queues, ClusterRoles and ClusterRoleBindings, and — since a
// ClusterRole can grant only what its creator holds — hold what the reader grants.
func TestTeamQueueObjectsAreGrantedToProviderKubernetes(t *testing.T) {
	type rule struct {
		APIGroups     []string `json:"apiGroups"`
		Resources     []string `json:"resources"`
		ResourceNames []string `json:"resourceNames"`
		Verbs         []string `json:"verbs"`
	}
	var rules []rule
	for _, d := range readYAMLDocs(t, filepath.Join(controlPlaneDir(t), "providers/provider-runtime-rbac.yaml")) {
		if d["kind"] == "ClusterRole" && dig(d, "metadata", "name") == "crossplane-provider-kubernetes-compose" {
			raw, _ := json.Marshal(d["rules"])
			if err := json.Unmarshal(raw, &rules); err != nil {
				t.Fatal(err)
			}
		}
	}
	holds := func(group, resource, verb string) bool {
		for _, r := range rules {
			if len(r.ResourceNames) == 0 && fnContains(r.APIGroups, group) && fnContains(r.Resources, resource) && fnContains(r.Verbs, verb) {
				return true
			}
		}
		return false
	}
	for _, k := range [][2]string{{"scheduling.volcano.sh", "queues"}, {"rbac.authorization.k8s.io", "clusterroles"}, {"rbac.authorization.k8s.io", "clusterrolebindings"}} {
		for _, v := range []string{"get", "list", "watch", "create", "update", "patch", "delete"} {
			if !holds(k[0], k[1], v) {
				t.Errorf("provider-kubernetes may not %s %s.%s, which the Team composition emits", v, k[1], k[0])
			}
		}
	}
	docs, _ := tqRender(t, tqXR(t), tqCluster(t, nil), nil)
	role := composedManifest(docs, "ClusterRole", "adhar-batch-queues-acme-payments")
	for _, r := range role["rules"].([]any) {
		m := r.(map[string]any)
		for _, g := range m["apiGroups"].([]any) {
			for _, res := range m["resources"].([]any) {
				for _, v := range m["verbs"].([]any) {
					if !holds(g.(string), res.(string), v.(string)) {
						t.Errorf("the queue reader grants %s on %s.%s, which provider-kubernetes does not hold: refused as privilege escalation", v, res, g)
					}
				}
			}
		}
	}
}

// Crossplane itself fetches the root queue and the leaves as required resources
// (rbac/local-compose-rbac.yaml, design 0025 §16.2 PLAT-2.3; granted in Phase 2
// stage 2). A required resource it may not read fails the whole reconcile.
func TestTeamQueueLookupsAreGrantedToCrossplane(t *testing.T) {
	var rules []struct {
		APIGroups []string `json:"apiGroups"`
		Resources []string `json:"resources"`
		Verbs     []string `json:"verbs"`
	}
	for _, d := range readYAMLDocs(t, filepath.Join(controlPlaneDir(t), "rbac/local-compose-rbac.yaml")) {
		if dig(d, "metadata", "name") == "crossplane-compose-local" {
			raw, _ := json.Marshal(d["rules"])
			if err := json.Unmarshal(raw, &rules); err != nil {
				t.Fatal(err)
			}
		}
	}
	var missing []string
	for _, v := range []string{"get", "list", "watch"} {
		ok := false
		for _, r := range rules {
			if fnContains(r.APIGroups, "scheduling.volcano.sh") && fnContains(r.Resources, "queues") && fnContains(r.Verbs, v) {
				ok = true
			}
		}
		if !ok {
			missing = append(missing, v)
		}
	}
	if len(missing) > 0 {
		t.Errorf("crossplane-compose-local lacks %v on queues.scheduling.volcano.sh: every Team (and every BatchJob) on a cluster running Volcano fails to fetch its required resources", missing)
	}
}

// The real function containers, the path Crossplane takes.
func TestTeamQueuesRenderWithCrossplane(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: skipping `crossplane render`, which starts the function containers")
	}
	bin, err := exec.LookPath("crossplane")
	if err != nil {
		t.Skip("the crossplane CLI is not installed; the in-process renders still ran")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("Docker is not available; `crossplane render` runs each function in a container")
	}
	composition := filepath.Join(controlPlaneDir(t), "compositions/team/local.yaml")
	render := func(extra ...string) []map[string]any {
		t.Helper()
		args := append([]string{"render", filepath.Join(tqFixtureDir, "xr.yaml"), composition, filepath.Join(bjFixtureDir, "functions.yaml"),
			"--required-resources=" + filepath.Join(tqFixtureDir, "required-resources.yaml")}, extra...)
		var stdout, stderr bytes.Buffer
		cmd := exec.Command(bin, args...)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("crossplane render: %v\n%s", err, stderr.String())
		}
		var docs []map[string]any
		for _, d := range strings.Split(stdout.String(), "\n---") {
			var m map[string]any
			if err := yaml.Unmarshal([]byte(d), &m); err == nil && len(m) > 0 {
				docs = append(docs, m)
			}
		}
		return docs
	}
	condition := func(docs []map[string]any) map[string]any {
		for _, d := range docs {
			if d["kind"] != "Team" {
				continue
			}
			for _, c := range dig(d, "status", "conditions").([]any) {
				if m := c.(map[string]any); m["type"] == "BatchQueuesReady" {
					return m
				}
			}
		}
		t.Fatal("the rendered Team has no BatchQueuesReady condition")
		return nil
	}
	open := render("--observed-resources=" + filepath.Join(tqFixtureDir, "observed-open.yaml"))
	var names []string
	for _, d := range open {
		if n, _ := dig(d, "metadata", "annotations", "crossplane.io/composition-resource-name").(string); strings.HasPrefix(n, "batch-queue") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	if got := strings.Join(names, ","); got != "batch-queue-dev,batch-queue-prod,batch-queue-reader,batch-queue-reader-binding,batch-queue-test" {
		t.Errorf("composes %s", got)
	}
	if c := condition(open); c["status"] != "True" || c["reason"] != "QueuesOpen" {
		t.Errorf("BatchQueuesReady = %v", c)
	}
	// The real engine's leaves are the in-process ones.
	inProcess, _ := tqRender(t, tqXR(t), tqCluster(t, nil), tqObserved(t, "observed-open.yaml"))
	if got, want := tqJSON(tqQueues(open)), tqJSON(tqQueues(inProcess)); got != want {
		t.Errorf("crossplane render composes %s, the in-process render %s", got, want)
	}
	if c := condition(render("--observed-resources=" + filepath.Join(tqFixtureDir, "observed-rejected.yaml"))); c["status"] != "False" || c["reason"] != "QueueRejected" {
		t.Errorf("a refused prod leaf through the real functions: BatchQueuesReady = %v", c)
	}
}
