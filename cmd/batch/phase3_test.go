package batch

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	platformevents "adhar-io/adhar/platform/events"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

const backfillParams = `[{"name":"businessDate","type":"date","identifying":true,"source":"schedule"},{"name":"region","identifying":false,"default":"sg"}]`

func fastPolls(t *testing.T) {
	t.Helper()
	prev := pollInterval
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = prev })
}

func dateRun(t *testing.T, date string) string {
	t.Helper()
	n, err := runName("importer", map[string]string{"businessDate": date})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// world is the Job controller and the runs' pods, as far as a backfill can
// tell: every time the runs are listed, ONE released run that has not ended
// finishes. Each release is checked as it is made: the in-flight count after
// it (A3.3) and the order of release.
type world struct {
	t        *testing.T
	dc       *dynamicfake.FakeDynamicClient
	names    []string
	created  map[string]*unstructured.Unstructured
	released []string
	creates  int
	maxLive  int
	patched  map[string]bool
}

func newWorld(t *testing.T, dc *dynamicfake.FakeDynamicClient, existing ...string) *world {
	w := &world{t: t, dc: dc, names: existing, created: map[string]*unstructured.Unstructured{}, patched: map[string]bool{}}
	dc.PrependReactor("create", "jobs", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.GetResource() != jobGVR {
			return false, nil, nil
		}
		obj := a.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		w.creates++
		w.names = append(w.names, obj.GetName())
		w.created[obj.GetName()] = obj.DeepCopy()
		return false, nil, nil
	})
	dc.PrependReactor("list", "jobs", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.GetResource() == jobGVR {
			w.finishOne()
		}
		return false, nil, nil
	})
	dc.PrependReactor("patch", "jobs", func(a k8stesting.Action) (bool, runtime.Object, error) {
		p := a.(k8stesting.PatchAction)
		var body map[string]map[string]interface{}
		_ = json.Unmarshal(p.GetPatch(), &body)
		if body["spec"]["suspend"] == false {
			w.patched[p.GetName()] = true
			w.released = append(w.released, p.GetName())
			live := 1
			for _, n := range w.names {
				if n != p.GetName() && w.live(n) {
					live++
				}
			}
			if live > w.maxLive {
				w.maxLive = live
			}
		}
		return false, nil, nil
	})
	return w
}

func (w *world) get(name string) *unstructured.Unstructured {
	obj, err := w.dc.Tracker().Get(jobGVR, "prod", name)
	if err != nil {
		return nil
	}
	return obj.(*unstructured.Unstructured)
}

// live is released and not ended.
func (w *world) live(name string) bool {
	run := w.get(name)
	if run == nil {
		return false
	}
	suspended, _, _ := unstructured.NestedBool(run.Object, "spec", "suspend")
	phase := runPhaseOf(run)
	return !suspended && phase != phaseSucceeded && phase != phaseFailed
}

// finishOne ends the EARLIEST released run still live, so a later one stays
// in flight across polls with whatever stale Suspended condition it carries.
func (w *world) finishOne() {
	for _, n := range append(append([]string{}, w.released...), w.names...) {
		if !w.live(n) {
			continue
		}
		run := w.get(n)
		run.Object["status"] = map[string]interface{}{"succeeded": int64(1), "startTime": testNow.Format(time.RFC3339),
			"conditions": []interface{}{map[string]interface{}{"type": "Complete", "status": "True"}}}
		if err := w.dc.Tracker().Update(jobGVR, run, "prod"); err != nil {
			w.t.Errorf("finishing %s: %v", n, err)
		}
		return
	}
}

// A3.3: a backfill of seven dates with --parallel 2 creates one run per date,
// every one SUSPENDED, releases them in date order and never has more than two
// in flight; asked again, it creates and releases nothing.
func TestBackfillNeverHasMoreThanParallelInFlightAndIsIdempotent(t *testing.T) {
	fixedClock(t)
	quietOutput(t)
	fastPolls(t)
	dc := fakeCluster(batchJobXR("importer", "prod", "", ""), templateConfigMap(backfillParams))
	w := newWorld(t, dc)
	ctx := context.Background()

	report, err := backfill(ctx, dc, "prod", "importer", "2026-11-01", "2026-11-07", nil, 2, true, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for d := 1; d <= 7; d++ {
		want = append(want, dateRun(t, "2026-11-0"+string(rune('0'+d))))
	}
	if len(w.created) != 7 {
		t.Fatalf("one run per date: created %d", len(w.created))
	}
	for name, obj := range w.created {
		if s, _, _ := unstructured.NestedBool(obj.Object, "spec", "suspend"); !s {
			t.Errorf("%s was created running; a native backfill creates every run suspended", name)
		}
		if obj.GetLabels()[labelTrigger] != triggerBackfill || envOf(obj, envTrigger) != triggerBackfill {
			t.Errorf("%s carries trigger %q/%q, want backfill", name, obj.GetLabels()[labelTrigger], envOf(obj, envTrigger))
		}
	}
	if w.maxLive != 2 {
		t.Errorf("at most --parallel runs in flight, and the parallelism used: max %d, want 2", w.maxLive)
	}
	if strings.Join(w.released, ",") != strings.Join(want, ",") {
		t.Errorf("released %v, want date order %v", w.released, want)
	}
	if report.exitCode() != 0 || len(report.Runs) != 7 {
		t.Fatalf("report = %+v", report)
	}
	for _, it := range report.Runs {
		if it.Action != "created" || it.Status != "Succeeded" {
			t.Errorf("%s: action %s status %s", it.BusinessDate, it.Action, it.Status)
		}
	}
	got := map[string]string{}
	for _, n := range want {
		got[envOf(w.created[n], envParams)] = n
	}
	if got[`{"businessDate":"2026-11-03","region":"sg"}`] != dateRun(t, "2026-11-03") {
		t.Errorf("each date's run carries its own date and the defaults: %v", got)
	}

	// Again: every date completed, nothing created, nothing released.
	created, released := w.creates, len(w.released)
	again, err := backfill(ctx, dc, "prod", "importer", "2026-11-01", "2026-11-07", nil, 2, true, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if w.creates != created || len(w.released) != released {
		t.Errorf("a repeated backfill started work: %d creates, %d releases", w.creates-created, len(w.released)-released)
	}
	for _, it := range again.Runs {
		if it.Action != "completed" {
			t.Errorf("%s: %s on a repeat, want completed", it.BusinessDate, it.Action)
		}
	}
}

// A backfill that was interrupted, or that meets a person's runs, continues
// rather than repeats: a completed date is left alone, a failed one restarted
// as -r1 (suspended like the rest), one still queued is released, and one a
// person stopped after it started is never touched. Without --wait the command
// ends when the last run is released.
func TestBackfillContinuesWhatExists(t *testing.T) {
	fixedClock(t)
	quietOutput(t)
	fastPolls(t)
	at := testNow.Add(-2 * time.Hour)
	d1, d2, d3, d4 := dateRun(t, "2026-11-01"), dateRun(t, "2026-11-02"), dateRun(t, "2026-11-03"), dateRun(t, "2026-11-04")
	queued := jobObject(d3, "prod", "importer", "stopped", at)
	delete(queued.Object["status"].(map[string]interface{}), "startTime")
	stopped := jobObject(d4, "prod", "importer", "stopped", at)
	stopped.Object["status"].(map[string]interface{})["startTime"] = at.Format(time.RFC3339)
	dc := fakeCluster(batchJobXR("importer", "prod", "", ""), templateConfigMap(backfillParams),
		jobObject(d1, "prod", "importer", "succeeded", at), jobObject(d2, "prod", "importer", "failed", at), queued, stopped)
	w := newWorld(t, dc, d1, d2, d3, d4)

	report, err := backfill(context.Background(), dc, "prod", "importer", "2026-11-01", "2026-11-06", nil, 2, false, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]string{}
	for _, it := range report.Runs {
		actions[it.BusinessDate] = it.Action
	}
	wantActions := map[string]string{"2026-11-01": "completed", "2026-11-02": "restarted", "2026-11-03": "exists", "2026-11-04": "stopped", "2026-11-05": "created", "2026-11-06": "created"}
	for d, a := range wantActions {
		if actions[d] != a {
			t.Errorf("%s: %s, want %s", d, actions[d], a)
		}
	}
	if _, ok := w.created[d1]; ok {
		t.Error("a completed date was run again")
	}
	r1 := w.created[d2+"-r1"]
	if r1 == nil || envOf(r1, envRestartOf) != d2 || r1.GetLabels()[labelTrigger] != "restart" {
		t.Fatalf("the failed date is restarted as %s-r1: %v", d2, r1)
	}
	if s, _, _ := unstructured.NestedBool(r1.Object, "spec", "suspend"); !s {
		t.Error("the restart is queued like every other date")
	}
	if !w.patched[d3] || !w.patched[d2+"-r1"] {
		t.Errorf("the queued runs are released: %v", w.released)
	}
	if w.patched[d4] {
		t.Error("a run a person stopped was resumed by the backfill")
	}
	if w.maxLive > 2 {
		t.Errorf("%d in flight with --parallel 2", w.maxLive)
	}
	if report.exitCode() != 0 {
		t.Errorf("without --wait the exit code says nothing about the runs: %d", report.exitCode())
	}
}

// On Volcano every run is created at once, none suspended, and nothing is
// released: the queue throttles.
func TestBackfillOnVolcanoCreatesEveryRunAndLeavesThrottlingToTheQueue(t *testing.T) {
	fixedClock(t)
	quietOutput(t)
	tplRun := volcanoRun("", "", testNow)
	delete(tplRun.Object, "status")
	meta := tplRun.Object["metadata"].(map[string]interface{})
	delete(meta, "name")
	delete(meta, "uid")
	delete(meta, "creationTimestamp")
	cm := templateConfigMap(`[{"name":"businessDate","type":"date","identifying":true,"source":"schedule"}]`)
	cm.SetName("settle-run")
	data := cm.Object["data"].(map[string]interface{})
	data["template.yaml"], data["kind"] = toJSON(tplRun.Object), "VolcanoJob"
	dc := fakeCluster(batchJobXR("settle", "prod", "", ""), cm)
	dc.PrependReactor("patch", "*", func(a k8stesting.Action) (bool, runtime.Object, error) {
		t.Errorf("a Volcano backfill released %s; the queue decides", a.(k8stesting.PatchAction).GetName())
		return true, nil, nil
	})
	report, err := backfill(context.Background(), dc, "prod", "settle", "2026-11-01", "2026-11-03", nil, 1, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := dc.Resource(volcanoJobGVR).Namespace("prod").List(context.Background(), metav1.ListOptions{})
	if len(list.Items) != 3 || report.Scheduler != schedulerVolcano {
		t.Fatalf("all three runs created as Volcano Jobs: %d (%s)", len(list.Items), report.Scheduler)
	}
	for _, it := range list.Items {
		if _, found, _ := unstructured.NestedFieldNoCopy(it.Object, "spec", "suspend"); found {
			t.Errorf("%s carries spec.suspend, which a Volcano Job does not have", it.GetName())
		}
	}
}

// Every date is checked before anything is created; the business date is the
// command's, not -p's; a job with no date parameter has nothing to backfill by.
func TestBackfillRefusesBeforeCreatingAnything(t *testing.T) {
	fixedClock(t)
	quietOutput(t)
	ctx := context.Background()
	dc := fakeCluster(batchJobXR("importer", "prod", "", ""), templateConfigMap(backfillParams))
	cases := []struct {
		from, to string
		extra    map[string]string
		want     string
	}{
		{"2026-11-07", "2026-11-01", nil, "before"},
		{"2026-02-30", "2026-03-01", nil, "not a date"},
		{"2026-01-01", "2027-06-01", nil, "more than"},
		{"2026-11-01", "2026-11-02", map[string]string{"businessDate": "2026-01-01"}, "--from and --to"},
		{"2026-11-01", "2026-11-02", map[string]string{"nope": "x"}, "not a parameter"},
	}
	for _, c := range cases {
		if _, err := backfill(ctx, dc, "prod", "importer", c.from, c.to, c.extra, 1, false, 0); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s..%s %v: %v, want %q", c.from, c.to, c.extra, err, c.want)
		}
	}
	nodate := fakeCluster(batchJobXR("importer", "prod", "", ""), templateConfigMap(`[{"name":"region"}]`))
	if _, err := backfill(ctx, nodate, "prod", "importer", "2026-11-01", "2026-11-02", nil, 1, false, 0); err == nil || !strings.Contains(err.Error(), "business-date") {
		t.Errorf("no date parameter: %v", err)
	}
	if list, _ := dc.Resource(jobGVR).Namespace("prod").List(ctx, metav1.ListOptions{}); len(list.Items) != 0 {
		t.Errorf("a refused backfill created %d runs", len(list.Items))
	}
	if f, ok := businessDateField([]paramField{{Name: "asOf", Type: "date", Source: "schedule"}, {Name: "day", Type: "date"}}); !ok || f.Name != "asOf" {
		t.Errorf("without businessDate the first date the schedule supplies: %v %v", f, ok)
	}
}

/* ─────────────────────────── smoke (D6) ─────────────────────────── */

const (
	digestA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	digestB = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

func imageAt(digest string) string {
	return "harbor.adhar.localtest.me/adhar/importer@sha256:" + digest
}

func smokeTemplate(image string) *unstructured.Unstructured {
	cm := templateConfigMap(backfillParams)
	data := cm.Object["data"].(map[string]interface{})
	data["template.yaml"] = strings.Replace(importerTemplateYAML, "harbor.adhar.localtest.me/adhar/importer@sha256:aaaa", image, 1)
	return cm
}

func smokeJob(image string, smoke map[string]interface{}) *unstructured.Unstructured {
	xr := batchJobXR("importer", "test", "", "")
	xr.SetNamespace("test")
	_ = unstructured.SetNestedField(xr.Object, image, "spec", "parameters", "image")
	if smoke != nil {
		_ = unstructured.SetNestedField(xr.Object, smoke, "spec", "parameters", "smoke")
	}
	return xr
}

func inTest(cm *unstructured.Unstructured) *unstructured.Unstructured {
	cm.SetNamespace("test")
	return cm
}

// D6: `run --smoke` is <job>-smoke-<sha7 of the template's digest>, with the
// smoke parameters and trigger smoke; a second verification of the same build
// reuses the run; a failed one is retried as -r1, still a smoke run.
func TestSmokeRunIsNamedForTheBuildAndReused(t *testing.T) {
	fixedClock(t)
	quietOutput(t)
	fastPolls(t)
	ctx := context.Background()
	smoke := map[string]interface{}{"parameters": map[string]interface{}{"businessDate": "2026-11-01"}}
	dc := fakeCluster(smokeJob(imageAt(digestA), smoke), inTest(smokeTemplate(imageAt(digestA))))

	out, err := startSmokeRun(ctx, dc, nil, "test", "importer", false, 0)
	if err != nil || out.Run != "importer-smoke-0123456" {
		t.Fatalf("smoke = %+v %v", out, err)
	}
	run := getJob(t, dc, "test", "importer-smoke-0123456")
	if run.GetLabels()[labelTrigger] != triggerSmoke || envOf(run, envTrigger) != triggerSmoke || envOf(run, envParams) != `{"businessDate":"2026-11-01","region":"sg"}` {
		t.Errorf("the smoke run = labels %v trigger %q params %q", run.GetLabels(), envOf(run, envTrigger), envOf(run, envParams))
	}

	// The same build verified again: the completed run passes, nothing new.
	run.Object["status"] = map[string]interface{}{"succeeded": int64(1), "conditions": []interface{}{map[string]interface{}{"type": "Complete", "status": "True"}}}
	_, _ = dc.Resource(jobGVR).Namespace("test").Update(ctx, run, metav1.UpdateOptions{})
	out, err = startSmokeRun(ctx, dc, nil, "test", "importer", true, time.Minute)
	if err != nil || out.Status != "Succeeded" || out.ExitCode != 0 {
		t.Fatalf("re-verified = %+v %v", out, err)
	}
	if list, _ := dc.Resource(jobGVR).Namespace("test").List(ctx, metav1.ListOptions{}); len(list.Items) != 1 {
		t.Errorf("a re-verified build ran again: %d runs", len(list.Items))
	}

	// Failed: retried as -r1, trigger still smoke.
	run.Object["status"] = map[string]interface{}{"failed": int64(1), "conditions": []interface{}{map[string]interface{}{"type": "Failed", "status": "True"}}}
	_, _ = dc.Resource(jobGVR).Namespace("test").Update(ctx, run, metav1.UpdateOptions{})
	_, _ = startSmokeRun(ctx, dc, nil, "test", "importer", true, time.Millisecond)
	retry := getJob(t, dc, "test", "importer-smoke-0123456-r1")
	if retry.GetLabels()[labelTrigger] != triggerSmoke || envOf(retry, envRestartOf) != "importer-smoke-0123456" {
		t.Errorf("the retry = labels %v restartOf %q", retry.GetLabels(), envOf(retry, envRestartOf))
	}
}

// The gate must verify the build it was asked about: while the run template
// still carries the previous image, --smoke waits for it, and does not pass on
// the previous build's successful smoke run. A template that never catches up
// fails the gate; an image without a digest, or a job without smoke, is refused.
func TestSmokeRunWaitsForTheTemplateToCarryTheNewBuild(t *testing.T) {
	fixedClock(t)
	quietOutput(t)
	fastPolls(t)
	prevWait, prevPoll := smokeTemplateWait, smokePoll
	smokePoll = time.Millisecond
	t.Cleanup(func() { smokeTemplateWait, smokePoll = prevWait, prevPoll })
	ctx := context.Background()
	smoke := map[string]interface{}{"parameters": map[string]interface{}{"businessDate": "2026-11-01"}}

	oldPassed := jobObject("importer-smoke-0123456", "test", "importer", "succeeded", testNow.Add(-time.Hour))
	dc := fakeCluster(smokeJob(imageAt(digestB), smoke), inTest(smokeTemplate(imageAt(digestA))), oldPassed)
	smokeTemplateWait = 5 * time.Second
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _ = dc.Resource(configMapGVR).Namespace("test").Update(ctx, inTest(smokeTemplate(imageAt(digestB))), metav1.UpdateOptions{})
	}()
	out, err := startSmokeRun(ctx, dc, nil, "test", "importer", false, 0)
	if err != nil || out.Run != "importer-smoke-fedcba9" {
		t.Fatalf("the smoke run is named for the NEW build once the template carries it: %+v %v", out, err)
	}

	smokeTemplateWait = 10 * time.Millisecond
	stale := fakeCluster(smokeJob(imageAt(digestB), smoke), inTest(smokeTemplate(imageAt(digestA))), oldPassed)
	if _, err := startSmokeRun(ctx, stale, nil, "test", "importer", true, time.Minute); err == nil || !strings.Contains(err.Error(), "has not reconciled") {
		t.Errorf("a template that stays behind fails the gate: %v", err)
	}

	tagged := fakeCluster(smokeJob("harbor.adhar.localtest.me/adhar/importer:1.2", smoke), inTest(smokeTemplate("harbor.adhar.localtest.me/adhar/importer:1.2")))
	if _, err := startSmokeRun(ctx, tagged, nil, "test", "importer", false, 0); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Errorf("an undigested image: %v", err)
	}
	none := fakeCluster(smokeJob(imageAt(digestA), nil), inTest(smokeTemplate(imageAt(digestA))))
	if _, err := startSmokeRun(ctx, none, nil, "test", "importer", false, 0); err == nil || !strings.Contains(err.Error(), "no smoke run") {
		t.Errorf("no smoke declared: %v", err)
	}
	bad := fakeCluster(smokeJob(imageAt(digestA), map[string]interface{}{"parameters": map[string]interface{}{"nope": "x"}}), inTest(smokeTemplate(imageAt(digestA))))
	if _, err := startSmokeRun(ctx, bad, nil, "test", "importer", false, 0); err == nil || !strings.Contains(err.Error(), "smoke parameters") {
		t.Errorf("refused smoke parameters: %v", err)
	}
}

/* ─────────────────────────── ops (abandon, recover, dlq-replay) ─────────────────────────── */

func asOps(job *unstructured.Unstructured, verb, id string) *unstructured.Unstructured {
	labels := job.GetLabels()
	labels[labelTrigger] = triggerOps
	job.SetLabels(labels)
	c := batchContainerOf(job)
	env, _ := c["env"].([]interface{})
	env = setEnv(env, envCommand, verb, true)
	c["env"] = setEnv(env, envExecutionID, id, true)
	return job
}

func TestOpsRunNamesKeepTheVerbAndTheExecution(t *testing.T) {
	if got := opsRunName("importer", opsAbandon, 412); got != "importer-ops-abandon-412" {
		t.Errorf("ops name = %s", got)
	}
	long := strings.Repeat("a", 40)
	got := opsRunName(long, opsDLQReplay, 1234567890123)
	if !strings.HasSuffix(got, "-ops-dlq-replay-1234567890123") || len(got)+len("-r99") > maxRunName {
		t.Errorf("a long name is cut in the job prefix, leaving room for -r99: %s (%d)", got, len(got))
	}
	if r, _ := restartName(got, 99); len(r) > maxRunName {
		t.Errorf("the last retry %s is %d characters", r, len(r))
	}
}

// abandon/recover start an ops Job from the template with the command and the
// execution — in the batch container only, with no parameters — named
// <job>-ops-<verb>-<id>; asked again it shows that Job; with --wait a failed
// one is retried as -r1 and keeps its command. The gates are a person's: an
// active run, a completed one, an ops Job itself, and a run that names no
// execution are refused.
func TestOpsCommandsStartAnOpsJobFromTheTemplate(t *testing.T) {
	fixedClock(t)
	quietOutput(t)
	fastPolls(t)
	ctx := context.Background()
	at := testNow.Add(-time.Hour)
	failed := jobObject("importer-09932cb3", "prod", "importer", "failed", at)
	pod := podObject("importer-09932cb3-p", "prod", "importer-09932cb3", 3, map[string]interface{}{"v": 1, "outcome": "failed", "exitCode": 3, "executionId": 12}, at)
	dc := fakeCluster(batchJobXR("importer", "prod", "", ""), templateConfigMap(backfillParams), failed, pod,
		jobObject("importer-running", "prod", "importer", "running", at),
		jobObject("importer-done", "prod", "importer", "succeeded", at),
		jobObject("importer-nosummary", "prod", "importer", "failed", at),
		asOps(jobObject("importer-ops-recover-3", "prod", "importer", "failed", at), opsRecover, "3"))

	out, err := startOpsOnRun(ctx, dc, nil, "prod", "importer-09932cb3", opsAbandon, 0, false, 0)
	if err != nil || out.Run != "importer-ops-abandon-12" {
		t.Fatalf("abandon = %+v %v", out, err)
	}
	ops := getJob(t, dc, "prod", "importer-ops-abandon-12")
	if ops.GetLabels()[labelTrigger] != triggerOps || envOf(ops, envTrigger) != triggerOps || envOf(ops, envCommand) != opsAbandon || envOf(ops, envExecutionID) != "12" || envOf(ops, envParams) != "{}" {
		t.Errorf("the ops Job = labels %v env %v", ops.GetLabels(), batchContainerOf(ops)["env"])
	}
	for _, c := range containersOf(ops) {
		if c["name"] != batchContainer && hasEnv(c["env"].([]interface{}), envCommand) {
			t.Error("the sidecar received the command")
		}
	}
	if _, err := startOpsOnRun(ctx, dc, nil, "prod", "importer-09932cb3", opsAbandon, 0, false, 0); err != nil {
		t.Fatal(err)
	}
	if list, _ := dc.Resource(jobGVR).Namespace("prod").List(ctx, metav1.ListOptions{LabelSelector: labelTrigger + "=ops"}); len(list.Items) != 2 {
		t.Errorf("asking again started a second ops Job: %d", len(list.Items))
	}

	// --wait on a failed ops Job retries it, with its command.
	ops.Object["status"] = map[string]interface{}{"failed": int64(1), "conditions": []interface{}{map[string]interface{}{"type": "Failed", "status": "True"}}}
	_, _ = dc.Resource(jobGVR).Namespace("prod").Update(ctx, ops, metav1.UpdateOptions{})
	_, _ = startOpsOnRun(ctx, dc, nil, "prod", "importer-09932cb3", opsAbandon, 0, true, time.Millisecond)
	retry := getJob(t, dc, "prod", "importer-ops-abandon-12-r1")
	if envOf(retry, envCommand) != opsAbandon || envOf(retry, envExecutionID) != "12" || retry.GetLabels()[labelTrigger] != triggerOps {
		t.Errorf("the retry lost its command: labels %v env %v", retry.GetLabels(), batchContainerOf(retry)["env"])
	}

	if out, err := startOpsOnRun(ctx, dc, nil, "prod", "importer-nosummary", opsRecover, 7, false, 0); err != nil || out.Run != "importer-ops-recover-7" {
		t.Errorf("recover with --execution = %+v %v", out, err)
	}
	refused := map[string]string{"importer-running": "stop", "importer-done": "nothing to", "importer-nosummary": "--execution", "importer-ops-recover-3": "itself an ops Job"}
	for run, want := range refused {
		if _, err := startOpsOnRun(ctx, dc, nil, "prod", run, opsRecover, 0, false, 0); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", run, err, want)
		}
	}
}

// `restart` builds a run from the template's parameters, so it must refuse an
// ops Job (it would run the business job without the command) and a smoke
// run (whose chain is <job>-smoke-<sha7>-r<n>).
func TestRestartRefusesOpsAndSmokeRuns(t *testing.T) {
	fixedClock(t)
	quietOutput(t)
	at := testNow.Add(-time.Hour)
	smoke := jobObject("importer-smoke-0123456", "prod", "importer", "failed", at)
	smoke.SetLabels(map[string]string{labelBatchJob: "importer", labelTrigger: triggerSmoke})
	dc := fakeCluster(batchJobXR("importer", "prod", "", ""), templateConfigMap(backfillParams), smoke,
		asOps(jobObject("importer-ops-abandon-12", "prod", "importer", "failed", at), opsAbandon, "12"))
	for run, want := range map[string]string{"importer-ops-abandon-12": "ops Job", "importer-smoke-0123456": "--smoke"} {
		if _, _, err := restartRun(context.Background(), dc, "prod", run); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("restart %s: %v, want %q", run, err, want)
		}
	}
	if list, _ := dc.Resource(jobGVR).Namespace("prod").List(context.Background(), metav1.ListOptions{}); len(list.Items) != 2 {
		t.Errorf("a refused restart created a run: %d", len(list.Items))
	}
}

/* ─────────────────────────── dlq and history ─────────────────────────── */

func batchEvent(t *testing.T, typ, at string, data map[string]interface{}) platformevents.Event {
	t.Helper()
	b, _ := json.Marshal(data)
	return platformevents.Event{SpecVersion: "1.0", ID: typ + at, Source: "batch-observer", Type: typ, Subject: "prod/importer", Time: at, Data: b}
}

func stubHistory(t *testing.T, evs []platformevents.Event, err error) {
	t.Helper()
	prev := readBatchHistory
	readBatchHistory = func(context.Context, string, string, time.Time, int) ([]platformevents.Event, []error, error) {
		return evs, nil, err
	}
	t.Cleanup(func() { readBatchHistory = prev })
}

// The counts join the runs' summaries with the observer's end events, once per
// execution, never counting ops Jobs; each shows its latest replay. --replay
// starts one dlq-replay ops Job per execution and shows the one that exists.
// Without the bus the runs still count, and the report says so.
func TestDeadLettersJoinSummariesAndEventsAndReplayPerExecution(t *testing.T) {
	fixedClock(t)
	quietOutput(t)
	ctx := context.Background()
	at := testNow.Add(-time.Hour)
	run := jobObject("importer-09932cb3", "prod", "importer", "succeeded", at)
	pod := podObject("importer-09932cb3-p", "prod", "importer-09932cb3", 0, map[string]interface{}{"v": 1, "outcome": "completed", "exitCode": 0, "executionId": 10, "deadLettered": 3}, at)
	replayed := asOps(jobObject("importer-ops-dlq-replay-10", "prod", "importer", "succeeded", at), opsDLQReplay, "10")
	// The replay's own summary counts what it dead-lettered again, under its
	// own execution: an ops Job is never a row of the queue it drains.
	replayPod := podObject("importer-ops-dlq-replay-10-p", "prod", "importer-ops-dlq-replay-10", 0, map[string]interface{}{"v": 1, "outcome": "completed", "exitCode": 0, "executionId": 99, "deadLettered": 2}, at)
	dc := fakeCluster(batchJobXR("importer", "prod", "", ""), templateConfigMap(backfillParams), run, pod, replayed, replayPod)
	counts := func(n int64) map[string]interface{} { return map[string]interface{}{"deadLettered": n} }
	stubHistory(t, []platformevents.Event{
		batchEvent(t, platformevents.TypeBatchRunSucceeded, "2026-11-01T02:00:00Z", map[string]interface{}{"run": map[string]interface{}{"name": "importer-09932cb3"}, "springBatch": map[string]interface{}{"executionId": 10}, "counts": counts(3)}),
		batchEvent(t, platformevents.TypeBatchRunFailed, "2026-10-01T02:00:00Z", map[string]interface{}{"run": map[string]interface{}{"name": "importer-aaaaaaaa"}, "springBatch": map[string]interface{}{"executionId": 7, "businessDate": "2026-09-30"}, "counts": counts(5)}),
		batchEvent(t, platformevents.TypeBatchRunSucceeded, "2026-10-02T02:00:00Z", map[string]interface{}{"springBatch": map[string]interface{}{"executionId": 8}, "counts": counts(0)}),
		batchEvent(t, platformevents.TypeBatchRunStarted, "2026-10-03T02:00:00Z", map[string]interface{}{"springBatch": map[string]interface{}{"executionId": 9}, "counts": counts(4)}),
		batchEvent(t, platformevents.TypeBatchRunSucceeded, "2026-10-04T02:00:00Z", map[string]interface{}{"run": map[string]interface{}{"name": "importer-ops-dlq-replay-11", "trigger": "ops"}, "springBatch": map[string]interface{}{"executionId": 11}, "counts": counts(5)}),
	}, nil)

	report, err := deadLetters(ctx, dc, "prod", "importer", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Total != 8 || len(report.Entries) != 2 || report.Events != "read" {
		t.Fatalf("report = %+v", report)
	}
	first, second := report.Entries[0], report.Entries[1]
	if first.Source != "run" || *first.ExecutionID != 10 || first.DeadLettered != 3 || first.Replay != "Succeeded" {
		t.Errorf("the held run speaks for execution 10, with its replay: %+v", first)
	}
	if second.Source != "event" || *second.ExecutionID != 7 || second.BusinessDate != "2026-09-30" || second.Replay != "" {
		t.Errorf("the gone run comes from its event: %+v", second)
	}

	if err := replayDeadLetters(ctx, dc, report, 0, false, 0); err != nil {
		t.Fatal(err)
	}
	var started []string
	for _, o := range report.Replays {
		started = append(started, o.Run+"="+o.Status)
	}
	sort.Strings(started)
	if strings.Join(started, ",") != "importer-ops-dlq-replay-10=Succeeded,importer-ops-dlq-replay-7=Running" {
		t.Errorf("one replay per execution, the existing one shown: %v", started)
	}
	if envOf(getJob(t, dc, "prod", "importer-ops-dlq-replay-7"), envCommand) != opsDLQReplay {
		t.Error("the replay carries the dlq-replay command")
	}

	stubHistory(t, nil, errors.New("no event bridge on this cluster"))
	offline, err := deadLetters(ctx, dc, "prod", "importer", "", true)
	if err != nil || len(offline.Entries) != 1 || !strings.HasPrefix(offline.Events, "unavailable") {
		t.Errorf("without the bus the runs still count: %+v %v", offline, err)
	}
	if err := replayDeadLetters(ctx, dc, &dlqReport{BatchJob: "importer", Namespace: "prod"}, 0, false, 0); err == nil {
		t.Error("a replay with nothing known to replay must say so")
	}
}

func TestHistoryReadsOneJobsEventsAndSaysWhatHappened(t *testing.T) {
	for in, want := range map[string]time.Duration{"": 0, "30d": 30 * 24 * time.Hour, "2w": 14 * 24 * time.Hour, "90m": 90 * time.Minute} {
		if got, err := parseSince(in); err != nil || got != want {
			t.Errorf("parseSince(%q) = %v %v, want %v", in, got, err, want)
		}
	}
	if _, err := parseSince("yesterday"); err == nil {
		t.Error("--since yesterday is not a duration")
	}
	keep := subjectIs("prod/importer")
	for subject, want := range map[string]bool{"prod/importer": true, "prod/importer-eu": false, "preprod/importer": false} {
		if keep(platformevents.Event{Subject: subject}) != want {
			t.Errorf("history of prod/importer keeps %s: %v", subject, !want)
		}
	}
	rows := historyRowsOf([]platformevents.Event{
		batchEvent(t, platformevents.TypeBatchRunLost, "2026-11-01T02:00:00Z", map[string]interface{}{"run": map[string]interface{}{"name": "importer-09932cb3"}, "springBatch": map[string]interface{}{"businessDate": "2026-10-31"}, "outcome": map[string]interface{}{"reason": "OOMKilled", "exitCode": 137}, "pod": "importer-09932cb3-x"}),
		batchEvent(t, platformevents.TypeBatchScheduleHeld, "2026-11-01T03:00:00Z", map[string]interface{}{"schedule": map[string]interface{}{"holdReason": "feed late"}}),
	})
	if rows[0].Event != "run.lost" || rows[0].Run != "importer-09932cb3" || rows[0].BusinessDate != "2026-10-31" || !strings.Contains(rows[0].Detail, "OOMKilled") || !strings.Contains(rows[0].Detail, "exit 137") {
		t.Errorf("run.lost row = %+v", rows[0])
	}
	if rows[1].Event != "schedule.held" || !strings.Contains(rows[1].Detail, "feed late") {
		t.Errorf("schedule.held row = %+v", rows[1])
	}
}

// A smoke run's parameters are the BatchJob's: -p beside --smoke is refused
// before anything is read, rather than silently ignored.
func TestRunSmokeRefusesParameters(t *testing.T) {
	prevSmoke, prevParams := runSmoke, runParamArgs
	t.Cleanup(func() { runSmoke, runParamArgs = prevSmoke, prevParams })
	runSmoke, runParamArgs = true, []string{"businessDate=2026-11-01"}
	if err := runCmd.RunE(runCmd, []string{"importer"}); err == nil || !strings.Contains(err.Error(), "spec.parameters.smoke") {
		t.Errorf("--smoke with -p: %v", err)
	}
}
