package adharplatform

// The verifier's attempts to break `mode: chunked` and `lineage` (design 0025 D11,
// §5.11, A4.10–A4.16): a Queue that exists only in another namespace, gridSize
// reaching the composition, every native fallback, the runner's variables smuggled
// in through `envFrom`, and lineage with no OpenMetadata on a scheduled gang. Each
// guard here was mutation-tested: the fix reverted, the test seen failing, restored.

import (
	"fmt"
	"strings"
	"testing"
)

// A Queue is looked up in the job's OWN namespace. One of the same name in another
// environment's namespace is not this job's broker and must read as missing.
func TestChunkedQueueInAnotherNamespaceIsMissing(t *testing.T) {
	elsewhere := func(d map[string]any) { d["metadata"].(map[string]any)["namespace"] = "prod" }
	cluster := vcEdit(vcEdit(chNative(t), "ledger-chunks", elsewhere), "ledger-replies", elsewhere)
	scheduled := bjWithParams(chXR(t), map[string]any{"schedule": map[string]any{"cron": "0 2 * * *"}})
	docs := vcMustRender(t, scheduled, cluster, nil)
	prereq := bjCondition(t, docs, "PrerequisitesReady")
	if prereq["status"] != "False" || prereq["reason"] != "ChunkQueueUnavailable" ||
		!strings.Contains(fmt.Sprint(prereq["message"]), "Queue ledger-chunks, which does not exist in namespace dev") ||
		!strings.Contains(fmt.Sprint(prereq["message"]), "Queue ledger-replies, which does not exist in namespace dev") {
		t.Errorf("Queues only in namespace prod: PrerequisitesReady = %v", prereq)
	}
	if dig(bjMustResource(t, docs, "schedule-native"), "spec", "suspend") != true {
		t.Error("a chunked job whose Queues live in another namespace keeps its schedule running")
	}
}

// gridSize with chunked is the XRD's to refuse (A4.10, envtest). If one reaches the
// composition anyway it must not turn the chunked run into a partitioned one.
func TestChunkedGridSizeReachingTheCompositionAddsNoPartitionChannel(t *testing.T) {
	for _, cl := range []struct {
		name    string
		cluster []map[string]any
	}{{"native", chNative(t)}, {"volcano", chCluster(t)}} {
		t.Run(cl.name, func(t *testing.T) {
			run, _ := vcTemplate(t, vcMustRender(t, bjWithParams(chXR(t), map[string]any{"gridSize": 16}), cl.cluster, nil))
			for role, pod := range vcPods(run) {
				chAssertChunkedPod(t, role, pod)
			}
		})
	}
}

// Every way a chunked job ends up on the native path is the partitioned job's: the
// same Indexed Job, the same condition, and the chunked env on every index.
func TestChunkedNativeFallbacksMatchPartitioned(t *testing.T) {
	gangOff := vcEdit(chCluster(t), "adhar-batch-scheduler", func(d map[string]any) { d["data"].(map[string]any)["gang"] = "false" })
	for _, c := range []struct {
		name      string
		set       map[string]any
		cluster   []map[string]any
		wantKind  string
		wantInMsg string
	}{
		{"Volcano not installed, scheduler auto", nil, chNative(t), "IndexedJob", ""},
		{"Volcano installed, scheduler kubernetes", map[string]any{"scheduler": "kubernetes"}, chCluster(t), "IndexedJob", ""},
		{"the descriptor says gang false, scheduler auto", nil, gangOff, "IndexedJob", "and a chunked job on Volcano is a gang"},
		{"Volcano not installed, scheduler volcano", map[string]any{"scheduler": "volcano"}, chNative(t), "IndexedJob", "scheduler volcano cannot run this job"},
	} {
		t.Run(c.name, func(t *testing.T) {
			chunked := bjWithParams(chXR(t), c.set)
			docs := vcMustRender(t, chunked, c.cluster, nil)
			run, kind := vcTemplate(t, docs)
			if kind != c.wantKind {
				t.Fatalf("kind %s, want %s", kind, c.wantKind)
			}
			chAssertChunkedPod(t, "indexed", dig(run, "spec", "template").(map[string]any))
			if c.wantInMsg != "" && !strings.Contains(fmt.Sprint(bjStatus(t, docs)["message"]), c.wantInMsg) &&
				!strings.Contains(tqJSON(docs), c.wantInMsg) {
				t.Errorf("nothing says why it runs natively; want %q", c.wantInMsg)
			}
			// The partitioned job in the same place resolves the same way.
			pset := map[string]any{"mode": "partitioned", "chunking": nil}
			for k, v := range c.set {
				pset[k] = v
			}
			pDocs := vcMustRender(t, bjWithParams(chXR(t), pset), c.cluster, nil)
			if _, pKind := vcTemplate(t, pDocs); pKind != kind {
				t.Errorf("chunked resolves to %s, partitioned to %s", kind, pKind)
			}
			for _, typ := range []string{"PrerequisitesReady", "QueueFits", "ScheduleReady"} {
				cc, pc := bjCondition(t, docs, typ), bjCondition(t, pDocs, typ)
				if cc["status"] != pc["status"] || cc["reason"] != pc["reason"] {
					t.Errorf("%s: chunked %v/%v, partitioned %v/%v", typ, cc["status"], cc["reason"], pc["status"], pc["reason"])
				}
			}
			if c.set == nil { // the helper renders the partitioned job without overrides
				chAssertSameShapeAsPartitioned(t, c.cluster, docs)
			}
		})
	}
}

// The runner's variables cannot be set through `env` (refused since Phase 1), and
// they must not arrive through `envFrom` either: a prefix of ADHAR_BATCH_ (or one an
// ADHAR_BATCH_ name starts with) would let a Secret key MODE become ADHAR_BATCH_MODE,
// which Spring binds to adhar.batch.mode and which a chunked run must never carry;
// and while lineage is on, a prefix of OPENLINEAGE_ would fight the platform's
// variables. The run template is the contract Kyverno compares against, so what the
// composition admits here a person's run carries without question.
func TestChunkedEnvFromCannotCarryTheRunnersVariables(t *testing.T) {
	for _, c := range []struct {
		name    string
		envFrom []any
		want    string
	}{
		{"ADHAR_BATCH_ prefix", []any{map[string]any{"prefix": "ADHAR_BATCH_", "configMapRef": map[string]any{"name": "mode"}}}, "envFrom prefix ADHAR_BATCH_ is owned by the runner's contract"},
		{"a longer ADHAR_BATCH_ prefix", []any{map[string]any{"prefix": "ADHAR_BATCH_CHUNKING_REQUESTS_", "secretRef": map[string]any{"name": "other-queue"}}}, "envFrom prefix ADHAR_BATCH_CHUNKING_REQUESTS_ is owned by the runner's contract"},
		{"a shorter prefix that reaches ADHAR_BATCH_", []any{map[string]any{"prefix": "ADHAR_", "secretRef": map[string]any{"name": "x"}}}, "envFrom prefix ADHAR_ is owned by the runner's contract"},
		{"OPENLINEAGE_ prefix with lineage on", []any{map[string]any{"prefix": "OPENLINEAGE_", "secretRef": map[string]any{"name": "ol"}}}, "envFrom prefix OPENLINEAGE_ contradicts lineage.enabled"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := vcRender(t, bjWithParams(chXR(t), map[string]any{"envFrom": c.envFrom}), chNative(t), nil)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("refusal = %v, want %q", err, c.want)
			}
		})
	}
	// What stays the job owner's: an unprefixed source, an unrelated prefix, and
	// OPENLINEAGE_ while lineage is off. Theirs come first, the Queues' after.
	ok := bjWithParams(chXR(t), map[string]any{
		"lineage": map[string]any{"enabled": false},
		"envFrom": []any{
			map[string]any{"secretRef": map[string]any{"name": "app-settings"}},
			map[string]any{"prefix": "APP_", "configMapRef": map[string]any{"name": "tuning"}},
			map[string]any{"prefix": "OPENLINEAGE_", "secretRef": map[string]any{"name": "marquez"}},
		},
	})
	run, _ := vcTemplate(t, vcMustRender(t, ok, chNative(t), nil))
	c, _ := vcContainerEnv(t, dig(run, "spec", "template").(map[string]any))
	want := `[{"secretRef":{"name":"app-settings"}},{"configMapRef":{"name":"tuning"},"prefix":"APP_"},{"prefix":"OPENLINEAGE_","secretRef":{"name":"marquez"}},` +
		`{"prefix":"ADHAR_BATCH_CHUNKING_REQUESTS_","secretRef":{"name":"ledger-chunks-queue"}},{"prefix":"ADHAR_BATCH_CHUNKING_REPLIES_","secretRef":{"name":"ledger-replies-queue"}}]`
	if got := tqJSON(chEnvFrom(c)); got != want {
		t.Errorf("envFrom = %s\nwant %s", got, want)
	}
}

// Lineage with no OpenMetadata on a scheduled job, on Volcano and natively: a
// warning, never a missing prerequisite, and the schedule keeps running.
func TestLineageWithoutOpenMetadataNeverStopsTheSchedule(t *testing.T) {
	scheduled := bjWithParams(chXR(t), map[string]any{"schedule": map[string]any{"cron": "0 2 * * *"}})
	for _, c := range []struct {
		name     string
		cluster  []map[string]any
		schedule string
	}{
		{"native", chNative(t, "openmetadata"), "schedule-native"},
		{"volcano", chCluster(t, "openmetadata"), "schedule-volcano"},
	} {
		t.Run(c.name, func(t *testing.T) {
			docs := vcMustRender(t, scheduled, c.cluster, nil)
			if p := bjCondition(t, docs, "PrerequisitesReady"); p["status"] != "True" {
				t.Errorf("PrerequisitesReady = %v; a missing lineage receiver is advisory", p)
			}
			if s := dig(bjMustResource(t, docs, c.schedule), "spec", "suspend"); s != false {
				t.Errorf("suspend = %v with OpenMetadata absent", s)
			}
			if msg := fmt.Sprint(bjStatus(t, docs)["message"]); !strings.Contains(msg, "OpenMetadata is not installed") {
				t.Errorf("status.message = %q", msg)
			}
		})
	}
}
