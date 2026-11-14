package batch

// The verifier's open items for PLAT-3.1, closed: the SLA gauges are the two
// the Mimir ruler reads, with its labels; a dependency run a full quota refused
// is retried until it starts; and a run deleted while no observer was running
// is reported as unobserved, not as "deleted before it ended".

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	"adhar-io/adhar/platform/events"
)

const mimirBatchAlertsFile = "../../stack/packages/observability/mimir/manifests/batch-app-alerts.yaml"

var (
	rawDeadlineSeries = regexp.MustCompile(`\badhar_batch_deadline_[a-z_]+`)
	byClause          = regexp.MustCompile(`\bby\s*\(([^)]*)\)`)
)

// gaugeName is a collector's metric name, from its own description.
func gaugeName(t *testing.T, c prometheus.Collector) string {
	t.Helper()
	ch := make(chan *prometheus.Desc, 1)
	c.Describe(ch)
	m := regexp.MustCompile(`fqName: "([^"]+)"`).FindStringSubmatch((<-ch).String())
	if m == nil {
		t.Fatal("collector has no fqName")
	}
	return m[1]
}

// The observer's gauges and the Mimir rules that read them are written in two
// lanes. When they disagreed (one gauge "deadline less expected end" labelled
// namespace/batch_job, against rules reading two gauges grouped by the OTLP
// resource labels), BatchDeadlineMissed fired for every merely endangered job,
// BatchDeadlineEndangered never fired, and every recorded series came out with
// no namespace and no team. This pins both halves: every adhar_batch_deadline_*
// series a rule reads is one the observer registers, every label a rule groups
// it by (bar the `cluster` Alloy adds) is one the observer sets, and every gauge
// is read by some rule.
func TestTheDeadlineGaugesAreWhatTheMimirRulesRead(t *testing.T) {
	raw, err := os.ReadFile(filepath.FromSlash(mimirBatchAlertsFile))
	if err != nil {
		t.Fatal(err)
	}
	var rule struct {
		Spec struct {
			Groups []struct {
				Rules []struct {
					Record, Alert, Expr string
				}
			}
		}
	}
	if err := yaml.Unmarshal(raw, &rule); err != nil {
		t.Fatal(err)
	}
	ours := map[string]bool{gaugeName(t, deadlineRemaining): true, gaugeName(t, deadlineSlack): true}
	labels := map[string]bool{"cluster": true}
	for _, l := range deadlineLabels {
		labels[l] = true
	}
	read := map[string]bool{}
	for _, g := range rule.Spec.Groups {
		for _, r := range g.Rules {
			names := rawDeadlineSeries.FindAllString(r.Expr, -1)
			if len(names) == 0 {
				continue
			}
			for _, n := range names {
				read[n] = true
				if !ours[n] {
					t.Errorf("%s%s reads %s, which the batch observer does not publish", r.Record, r.Alert, n)
				}
			}
			for _, m := range byClause.FindAllStringSubmatch(r.Expr, -1) {
				for _, l := range strings.Split(m[1], ",") {
					if l = strings.TrimSpace(l); l != "" && !labels[l] {
						t.Errorf("%s%s groups by %q, which the observer's gauges do not carry", r.Record, r.Alert, l)
					}
				}
			}
		}
	}
	for n := range ours {
		if !read[n] {
			t.Errorf("the observer publishes %s and no Mimir batch rule reads it", n)
		}
	}
}

type series struct {
	labels map[string]string
	value  float64
}

// gathered reads the two gauges as a scrape would.
func gathered(t *testing.T) map[string][]series {
	t.Helper()
	reg := prometheus.NewRegistry()
	reg.MustRegister(deadlineRemaining, deadlineSlack)
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]series{}
	for _, mf := range mfs {
		for _, m := range mf.Metric {
			out[mf.GetName()] = append(out[mf.GetName()], series{labels: labelMap(m), value: m.GetGauge().GetValue()})
		}
	}
	return out
}

func labelMap(m *dto.Metric) map[string]string {
	l := map[string]string{}
	for _, p := range m.Label {
		l[p.GetName()] = p.GetValue()
	}
	return l
}

func seriesOf(all map[string][]series, name, job string) []series {
	var out []series
	for _, s := range all[name] {
		if s.labels["adhar_batch_job"] == job {
			out = append(out, s)
		}
	}
	return out
}

// §11.4 through the gauges, the way the Mimir rules read them: an endangered
// deadline has time REMAINING and NEGATIVE projected slack for the same
// business date (BatchDeadlineEndangered, not Missed); a passed one has
// negative remaining and no slack (BatchDeadlineMissed); a date that succeeds
// has neither.
func TestAnEndangeredDeadlineIsNotAMissedOne(t *testing.T) {
	spec := nightly()
	spec.Name = "gauged"
	job := types.NamespacedName{Namespace: "prod", Name: "gauged"}
	h := hierarchy{Team: "payments", Environment: "prod"}
	state := withDurations(20, 25, 30, 45, 45, 40, 30, 25, 20, 45)
	run := observation{UID: "r1", Name: "gauged-x", Trigger: TriggerSchedule, Phase: phaseRunning,
		ScheduledAt: stamp(at("01:30", 4)), Created: at("01:30", 4), Started: at("01:31", 4)}
	defer gauges.publish(job, "", h, nil)

	gauges.publish(job, spec.Application, h, evaluateSchedule(spec, []observation{run}, state, false, at("01:32", 4)).Deadlines)
	got := gathered(t)
	rem, slack := seriesOf(got, "adhar_batch_deadline_seconds_remaining", "gauged"), seriesOf(got, "adhar_batch_deadline_projected_slack_seconds", "gauged")
	if len(rem) != 1 || rem[0].value != 28*60 {
		t.Fatalf("remaining = %+v, want one series at 1680 s: the deadline has NOT passed", rem)
	}
	if len(slack) != 1 || slack[0].value != -16*60 {
		t.Fatalf("projected slack = %+v, want one series at -960 s", slack)
	}
	want := map[string]string{"k8s_namespace_name": "prod", "adhar_batch_job": "gauged", "adhar_application": "importer",
		"adhar_environment": "prod", "adhar_team": "payments", "business_date": "2026-11-04"}
	for _, s := range append(rem, slack...) {
		for k, v := range want {
			if s.labels[k] != v {
				t.Errorf("label %s = %q, want %q (%v)", k, s.labels[k], v, s.labels)
			}
		}
	}

	gauges.publish(job, spec.Application, h, evaluateSchedule(spec, []observation{run}, state, false, at("02:05", 4)).Deadlines)
	got = gathered(t)
	rem, slack = seriesOf(got, "adhar_batch_deadline_seconds_remaining", "gauged"), seriesOf(got, "adhar_batch_deadline_projected_slack_seconds", "gauged")
	if len(rem) != 1 || rem[0].value != -5*60 || rem[0].labels["business_date"] != "2026-11-04" {
		t.Errorf("after the deadline: remaining = %+v, want -300 s for 2026-11-04", rem)
	}
	if len(slack) != 0 {
		t.Errorf("after the deadline the projection is over, got %+v", slack)
	}

	run.Phase, run.Finished = phaseSucceeded, at("02:10", 4)
	gauges.publish(job, spec.Application, h, evaluateSchedule(spec, []observation{run}, state, false, at("02:11", 4)).Deadlines)
	got = gathered(t)
	for _, s := range append(seriesOf(got, "adhar_batch_deadline_seconds_remaining", "gauged"), seriesOf(got, "adhar_batch_deadline_projected_slack_seconds", "gauged")...) {
		if s.labels["business_date"] == "2026-11-04" {
			t.Errorf("2026-11-04 succeeded, its series must be gone: %+v", s)
		}
	}
}

// Through Reconcile: the hierarchy labels come from the NAMESPACE (as the
// events' do), and a BatchJob that is gone takes its series with it.
func TestTheDeadlineGaugesCarryTheNamespacesHierarchy(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "prod", Labels: map[string]string{
		"adhar.io/team": "payments", "adhar.io/environment": "prod"}}}
	xr := newU(BatchJobGVK)
	xr.SetNamespace("prod")
	xr.SetName("slajob")
	xr.SetCreationTimestamp(metav1.NewTime(at("09:00", 1)))
	xr.Object["spec"] = map[string]interface{}{"parameters": map[string]interface{}{
		"application": "ledger",
		"schedule":    map[string]interface{}{"cron": "30 1 * * *", "timeZone": "Asia/Singapore"},
		"sla":         map[string]interface{}{"finishBy": "02:00"},
	}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ns).Build()
	if err := c.Create(context.Background(), xr); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Client: c, Runs: c, APIReader: c, Now: func() time.Time { return at("01:45", 4) },
		BatchJobsServed: func() bool { return true }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "prod", Name: "slajob"}}
	defer gauges.publish(req.NamespacedName, "", hierarchy{}, nil)
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	rem := seriesOf(gathered(t), "adhar_batch_deadline_seconds_remaining", "slajob")
	if len(rem) != 1 {
		t.Fatalf("remaining = %+v, want tonight's deadline", rem)
	}
	l := rem[0].labels
	if l["adhar_team"] != "payments" || l["adhar_environment"] != "prod" || l["adhar_application"] != "ledger" ||
		l["k8s_namespace_name"] != "prod" || l["business_date"] != "2026-11-04" {
		t.Errorf("labels = %v", l)
	}

	if err := c.Delete(context.Background(), xr); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if rem := seriesOf(gathered(t), "adhar_batch_deadline_seconds_remaining", "slajob"); len(rem) != 0 {
		t.Errorf("the BatchJob is gone and its deadline series stayed: %+v", rem)
	}
}

// A run that ended and was deleted while the manager was down: the restarted
// observer never saw its delete, so it cannot know how it ended. Saying
// Vanished ("deleted before it ended") claims what it did not see; it says
// Unobserved instead, and satisfies no dependsOn. The SAME process that saw the
// run alive and then finds it gone with no tombstone still says Vanished.
func TestARunDeletedWhileNoObserverRanIsUnobservedNotVanished(t *testing.T) {
	active := runJob("importer-09932cb3", "uid-1", "manual", map[string]string{"businessDate": "2026-11-03"})
	setStatus(active, "active", int64(1))
	c := fakeCluster(t, active.DeepCopy())
	clock := func() time.Time { return t0.Add(time.Hour) }
	before := &Reconciler{Client: c, Runs: c, APIReader: c, Events: &recorded{}, Now: clock}
	reconcileOnce(t, before)
	if err := c.Delete(context.Background(), active.DeepCopy()); err != nil {
		t.Fatal(err)
	}

	pub := &recorded{}
	restarted := &Reconciler{Client: c, Runs: c, APIReader: c, Events: pub, Now: clock}
	reconcileOnce(t, restarted)
	want(t, "after a restart", pub.types(), events.TypeBatchRunLost)
	var d eventData
	_ = json.Unmarshal(pub.got[0].Data, &d)
	if d.Outcome == nil || d.Outcome.Reason != "Unobserved" {
		t.Errorf("run.lost outcome = %+v, want Unobserved", d.Outcome)
	}
	cm := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "prod", Name: "importer" + stateSuffix}, cm); err != nil {
		t.Fatal(err)
	}
	if st := decodeState(cm.Data[stateKey]); len(st.Runs) != 1 || st.Runs[0].End != endUnknown || st.Runs[0].Satisfies || !st.Runs[0].Gone {
		t.Errorf("record = %s, want end unknown, gone, satisfying nothing", cm.Data[stateKey])
	}
	pub.reset()
	reconcileOnce(t, restarted)
	want(t, "looked at again", pub.types())

	// The process that saw it alive.
	again := runJob("importer-0000000a", "uid-2", "manual", map[string]string{"businessDate": "2026-11-04"})
	setStatus(again, "active", int64(1))
	if err := c.Create(context.Background(), again.DeepCopy()); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, restarted)
	if err := c.Delete(context.Background(), again.DeepCopy()); err != nil {
		t.Fatal(err)
	}
	pub.reset()
	reconcileOnce(t, restarted)
	want(t, "seen alive, then gone", pub.types(), events.TypeBatchRunLost)
	_ = json.Unmarshal(pub.got[0].Data, &d)
	if d.Outcome == nil || d.Outcome.Reason != "Vanished" {
		t.Errorf("run.lost outcome = %+v, want Vanished", d.Outcome)
	}
	if restarted.wasAlive("uid-2") {
		t.Error("a run whose story is over is still remembered as alive: the set would grow with every run the manager sees")
	}
}

func TestPendingBackoffDoublesToTenMinutes(t *testing.T) {
	var got []time.Duration
	for n := 0; n < 7; n++ {
		got = append(got, pendingBackoff(n))
	}
	wantD := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 10 * time.Minute, 10 * time.Minute}
	for i := range wantD {
		if got[i] != wantD[i] {
			t.Fatalf("backoff = %v, want %v", got, wantD)
		}
	}
}

// A dependency run the API server refused because a ResourceQuota was full
// (count/jobs.batch) is retried from the record until the quota frees, and
// then started exactly once. Before, the refusal was a Warning Event and the
// success that caused it was recorded anyway: nothing ever asked again.
func TestAQuotaRefusedDependencyRunIsStartedOnceTheQuotaFrees(t *testing.T) {
	s := startAPIServer(t)
	s.must(t, s.c.Create(s.ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "quota"}}))
	s.must(t, s.c.Create(s.ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "quota"}}))
	s.installBatchJobCRD(t)

	// No quota controller runs under envtest, so its status is written by
	// hand: the admission plugin judges against status.used.
	jobs := corev1.ResourceName("count/jobs.batch")
	q := &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "runs", Namespace: "quota"},
		Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{jobs: resource.MustParse("1")}}}
	s.must(t, s.c.Create(s.ctx, q))
	setUsed := func(n string) {
		fresh := &corev1.ResourceQuota{}
		s.must(t, s.c.Get(s.ctx, client.ObjectKeyFromObject(q), fresh))
		fresh.Status = corev1.ResourceQuotaStatus{Hard: corev1.ResourceList{jobs: resource.MustParse("1")}, Used: corev1.ResourceList{jobs: resource.MustParse(n)}}
		s.must(t, s.c.Status().Update(s.ctx, fresh))
	}
	setUsed("1")

	s.batchJob(t, "prod", "qsource", map[string]interface{}{"job": "sourceJob"}, nil)
	s.batchJob(t, "quota", "qdep", map[string]interface{}{"job": "depJob", "dependsOn": []interface{}{
		map[string]interface{}{"batchJob": "qsource", "namespace": "prod"},
	}}, nil)
	s.runTemplate(t, "quota", "qdep")
	clock := time.Date(2026, 11, 5, 2, 0, 0, 0, time.UTC)
	obs := s.observer(&recorded{}, clock, true)
	obs.Now = func() time.Time { return clock }
	reconcileJob(t, obs, "prod", "qsource")
	reconcileJob(t, obs, "quota", "qdep")

	deps := func() int {
		l := newList(JobGVK)
		s.must(t, s.c.List(s.ctx, l, client.InNamespace("quota"), client.MatchingLabels{labelBatchJob: "qdep"}))
		return len(l.Items)
	}
	record := func() *jobState {
		cm := &corev1.ConfigMap{}
		s.must(t, s.c.Get(s.ctx, types.NamespacedName{Namespace: "prod", Name: "qsource" + stateSuffix}, cm))
		return decodeState(cm.Data[stateKey])
	}
	src := s.run(t, "prod", "qsource", "qsource-09932cb3", "schedule", map[string]string{"businessDate": "2026-11-04"})
	s.succeed(t, src, clock.Add(-time.Hour), clock.Add(-50*time.Minute))
	res, err := obs.Reconcile(s.ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "prod", Name: "qsource"}})
	if err != nil {
		t.Fatal(err)
	}
	if n := deps(); n != 0 {
		t.Fatalf("the quota is full and %d dependency runs exist", n)
	}
	if p := record().Pending; len(p) != 1 || p[0].BusinessDate != "2026-11-04" || p[0].BatchJob != "qdep" {
		t.Fatalf("pending = %+v, want qdep for 2026-11-04", p)
	}
	if res.RequeueAfter <= 0 || res.RequeueAfter > 30*time.Second {
		t.Errorf("a pending run must bring the reconcile back within its backoff, got %+v", res)
	}

	// Still full at the retry: still pending, backed off further.
	clock = clock.Add(31 * time.Second)
	reconcileJob(t, obs, "prod", "qsource")
	if p := record().Pending; deps() != 0 || len(p) != 1 || p[0].Attempts != 1 {
		t.Fatalf("after a refused retry: %d runs, pending %+v", deps(), p)
	}

	setUsed("0")
	clock = clock.Add(2 * time.Minute)
	reconcileJob(t, obs, "prod", "qsource")
	if n := deps(); n != 1 {
		t.Fatalf("the quota freed and %d dependency runs exist, want 1", n)
	}
	if p := record().Pending; len(p) != 0 {
		t.Errorf("started, and still pending: %+v", p)
	}
	clock = clock.Add(10 * time.Minute)
	reconcileJob(t, obs, "prod", "qsource")
	if n := deps(); n != 1 {
		t.Errorf("dependency runs = %d after another look, want 1", n)
	}
}

// The record round-trips its pending runs (they are what survives a restart).
func TestPendingRunsSurviveTheRecord(t *testing.T) {
	s := &jobState{V: stateVersion}
	now := t0
	s.pend("quota", "qdep", "2026-11-04", now)
	s.pend("quota", "qdep", "2026-11-04", now.Add(time.Minute))
	got := decodeState(s.encode())
	if len(got.Pending) != 1 || got.Pending[0].Next != stamp(now.Add(30*time.Second)) {
		t.Errorf("pending = %+v", got.Pending)
	}
}
