package batch

// The observer against a real kube-apiserver (envtest): what the pure tests
// cannot show is that the record survives a restart, that a dependency run is
// created once by the API server's own name check, and that the manager starts
// — and keeps observing runs — on a cluster where neither the BatchJob nor the
// Volcano CRD exists yet (PLAT-3.1's four envtest cases).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"adhar-io/adhar/platform/events"
)

func envtestAssets(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv("KUBEBUILDER_ASSETS"); dir != "" {
		return dir
	}
	dirs, _ := filepath.Glob(filepath.Join("..", "..", "..", "bin", "k8s", "*"))
	sort.Strings(dirs)
	for i := len(dirs) - 1; i >= 0; i-- {
		if _, err := os.Stat(filepath.Join(dirs[i], "kube-apiserver")); err == nil {
			return dirs[i]
		}
	}
	t.Fatal("no envtest binaries: set KUBEBUILDER_ASSETS or run `make setup-envtest`")
	return ""
}

// recorded is a Publisher that keeps what it was handed, and optionally hands
// it on to a real publisher too (to prove the bus being OFF changes nothing).
type recorded struct {
	mu   sync.Mutex
	got  []events.Event
	next *events.Publisher
}

func (r *recorded) Publish(ctx context.Context, e events.Event) {
	r.mu.Lock()
	r.got = append(r.got, e)
	r.mu.Unlock()
	if r.next != nil {
		r.next.Publish(ctx, e)
	}
}

func (r *recorded) types() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.got {
		out = append(out, e.Type)
	}
	return out
}

func (r *recorded) reset() {
	r.mu.Lock()
	r.got = nil
	r.mu.Unlock()
}

type apiServer struct {
	ctx context.Context
	cfg *envtest.Environment
	c   client.Client
}

func startAPIServer(t *testing.T) *apiServer {
	t.Helper()
	env := &envtest.Environment{BinaryAssetsDirectory: envtestAssets(t)}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("starting envtest: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = apiextensionsv1.AddToScheme(scheme)
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &apiServer{ctx: ctx, cfg: env, c: c}
	for _, ns := range []string{"prod", "dev"} {
		s.must(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: map[string]string{
			"adhar.io/organisation": "acme", "adhar.io/team": "payments", "adhar.io/project": "ledger",
			"adhar.io/environment": ns, "adhar.io/plane": "workload",
		}}}))
		// No controller-manager runs here, so nothing creates the default
		// ServiceAccount the pods' admission asks for.
		s.must(t, c.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: ns}}))
	}
	return s
}

func (s *apiServer) must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// installBatchJobCRD stands in for the Crossplane XRD: what the observer reads
// of a BatchJob is its spec and status, schemaless here on purpose.
func (s *apiServer) installBatchJobCRD(t *testing.T) {
	t.Helper()
	preserve := true
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "batchjobs.platform.adhar.io"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "platform.adhar.io",
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{Kind: "BatchJob", ListKind: "BatchJobList", Plural: "batchjobs", Singular: "batchjob"},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name: "v1alpha1", Served: true, Storage: true,
				Subresources: &apiextensionsv1.CustomResourceSubresources{Status: &apiextensionsv1.CustomResourceSubresourceStatus{}},
				Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
					Type: "object",
					Properties: map[string]apiextensionsv1.JSONSchemaProps{
						"spec":   {Type: "object", XPreserveUnknownFields: &preserve},
						"status": {Type: "object", XPreserveUnknownFields: &preserve},
					},
				}},
			}},
		},
	}
	s.must(t, s.c.Create(s.ctx, crd))
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		l := newList(BatchJobGVK)
		if err := s.c.List(s.ctx, l); err == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("the BatchJob CRD was never served")
}

func (s *apiServer) batchJob(t *testing.T, ns, name string, params map[string]interface{}, annotations map[string]string) *unstructured.Unstructured {
	t.Helper()
	u := newU(BatchJobGVK)
	u.SetNamespace(ns)
	u.SetName(name)
	u.SetAnnotations(annotations)
	u.Object["spec"] = map[string]interface{}{"parameters": params}
	s.must(t, s.c.Create(s.ctx, u))
	return u
}

// runTemplate writes `ConfigMap <name>-run` as the composition renders it.
func (s *apiServer) runTemplate(t *testing.T, ns, name string) {
	t.Helper()
	tpl := map[string]interface{}{
		"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]interface{}{"labels": map[string]interface{}{
			labelBatchJob: name, labelWorkload: "batch", labelTrigger: "manual", "adhar.io/application": name,
		}, "annotations": map[string]interface{}{annotationActor: ""}},
		"spec": map[string]interface{}{
			"backoffLimit": 2, "activeDeadlineSeconds": 3600, "ttlSecondsAfterFinished": 604800,
			"template": map[string]interface{}{
				"metadata": map[string]interface{}{"labels": map[string]interface{}{labelBatchJob: name, labelWorkload: "batch", labelTrigger: "manual"}},
				"spec": map[string]interface{}{
					"restartPolicy": "Never",
					"containers": []interface{}{map[string]interface{}{
						"name": "batch", "image": "harbor/" + name + "@sha256:0123",
						"env": []interface{}{
							map[string]interface{}{"name": envParams, "value": "{}"},
							map[string]interface{}{"name": envTrigger, "value": "manual"},
							map[string]interface{}{"name": envTimeZone, "value": "Asia/Singapore"},
						},
					}},
				},
			},
		},
	}
	body, _ := json.Marshal(tpl)
	s.must(t, s.c.Create(s.ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-run", Namespace: ns},
		Data: map[string]string{
			"template.yaml":   string(body),
			"naming":          "v1",
			"kind":            "Job",
			"parameters.json": `[{"name":"businessDate","type":"date","source":"schedule"},{"name":"region","type":"string","default":"sg"}]`,
		},
	}))
}

// run creates a run Job for a BatchJob, as the CLI would.
func (s *apiServer) run(t *testing.T, ns, batchJob, name, trigger string, params map[string]string) *unstructured.Unstructured {
	t.Helper()
	u := runJob(name, "", trigger, params)
	u.SetNamespace(ns)
	u.SetUID("")
	u.SetCreationTimestamp(metav1.Time{})
	l := u.GetLabels()
	l[labelBatchJob] = batchJob
	u.SetLabels(l)
	_ = unstructured.SetNestedStringMap(u.Object, map[string]string{labelBatchJob: batchJob, labelWorkload: "batch"}, "spec", "template", "metadata", "labels")
	s.must(t, s.c.Create(s.ctx, u))
	return u
}

// succeed marks a Job finished the way the Job controller does.
func (s *apiServer) succeed(t *testing.T, run *unstructured.Unstructured, start, end time.Time) {
	t.Helper()
	s.status(t, run, map[string]interface{}{
		"startTime": start.UTC().Format(time.RFC3339), "completionTime": end.UTC().Format(time.RFC3339),
		"succeeded": int64(1),
		"conditions": []interface{}{
			map[string]interface{}{"type": "SuccessCriteriaMet", "status": "True", "reason": "CompletionsReached", "lastTransitionTime": end.UTC().Format(time.RFC3339)},
			map[string]interface{}{"type": "Complete", "status": "True", "reason": "CompletionsReached", "lastTransitionTime": end.UTC().Format(time.RFC3339)},
		},
	})
}

func (s *apiServer) status(t *testing.T, run *unstructured.Unstructured, status map[string]interface{}) {
	t.Helper()
	fresh := newU(JobGVK)
	s.must(t, s.c.Get(s.ctx, client.ObjectKeyFromObject(run), fresh))
	fresh.Object["status"] = status
	s.must(t, s.c.Status().Update(s.ctx, fresh))
}

func (s *apiServer) observer(pub Publisher, now time.Time, batchJobs bool) *Reconciler {
	return &Reconciler{
		Client: s.c, Runs: s.c, APIReader: s.c, Events: pub,
		Now:             func() time.Time { return now },
		BatchJobsServed: func() bool { return batchJobs },
		VolcanoServed:   func() bool { return false },
	}
}

func reconcileJob(t *testing.T, r *Reconciler, ns, name string) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}); err != nil {
		t.Fatalf("reconcile %s/%s: %v", ns, name, err)
	}
}

func TestObserverOnARealAPIServer(t *testing.T) {
	s := startAPIServer(t)

	// A3.1, through a restart: the record is what makes "exactly one" hold
	// when the manager comes back — a new observer with no memory republishes
	// nothing that was said, and publishes what happened while it was away.
	t.Run("transitions are published once, across a restart", func(t *testing.T) {
		pub := &recorded{}
		now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
		run := s.run(t, "dev", "loader", "loader-09932cb3", "manual", map[string]string{"businessDate": "2026-11-03"})
		obs := s.observer(pub, now, false)
		reconcileJob(t, obs, "dev", "loader")
		want(t, "created", pub.types(), events.TypeBatchRunRequested)
		e := pub.got[0]
		if e.Team != "payments" || e.Environment != "dev" || e.Subject != "dev/loader" || e.Correlation == "" {
			t.Errorf("the hierarchy comes from the namespace and the correlation is the run UID: %+v", e)
		}
		reconcileJob(t, obs, "dev", "loader")
		want(t, "reconciled again", pub.types(), events.TypeBatchRunRequested)

		s.succeed(t, run, now, now.Add(10*time.Minute))
		pub.reset()
		restarted := s.observer(pub, now, false)
		reconcileJob(t, restarted, "dev", "loader")
		want(t, "finished while the observer was away", pub.types(), events.TypeBatchRunStarted, events.TypeBatchRunSucceeded)
		pub.reset()
		reconcileJob(t, s.observer(pub, now, false), "dev", "loader")
		want(t, "restarted again", pub.types())

		cm := &corev1.ConfigMap{}
		s.must(t, s.c.Get(s.ctx, types.NamespacedName{Namespace: "dev", Name: "loader-observer"}, cm))
		if st := decodeState(cm.Data[stateKey]); len(st.Runs) != 1 || !st.Runs[0].Satisfies || st.Runs[0].BusinessDate != "2026-11-03" {
			t.Errorf("record = %s", cm.Data[stateKey])
		}
	})

	// §12: a run deleted before it ended is lost, with the outcome saying so.
	t.Run("a run deleted while active is lost", func(t *testing.T) {
		pub := &recorded{}
		now := time.Date(2026, 11, 3, 11, 0, 0, 0, time.UTC)
		run := s.run(t, "dev", "sweeper", "sweeper-09932cb3", "manual", nil)
		obs := s.observer(pub, now, false)
		reconcileJob(t, obs, "dev", "sweeper")
		s.must(t, s.c.Delete(s.ctx, run, client.PropagationPolicy(metav1.DeletePropagationBackground)))
		for i := 0; i < 50; i++ {
			if err := s.c.Get(s.ctx, client.ObjectKeyFromObject(run), newU(JobGVK)); apierrors.IsNotFound(err) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		reconcileJob(t, obs, "dev", "sweeper")
		want(t, "deleted", pub.types(), events.TypeBatchRunRequested, events.TypeBatchRunLost)
		var d eventData
		_ = json.Unmarshal(pub.got[1].Data, &d)
		if d.Outcome == nil || d.Outcome.Reason != "Vanished" {
			t.Errorf("run.lost outcome = %+v", d.Outcome)
		}
		reconcileJob(t, obs, "dev", "sweeper")
		if n := len(pub.types()); n != 2 {
			t.Errorf("published %d events after the second look, want 2", n)
		}

		// In the manager the delete event hands over the run's final state (a
		// tombstone): a run that had finished is history, one that had not
		// vanished — whichever reconcile sees the deletion first.
		for _, finished := range []bool{true, false} {
			pub.reset()
			name := map[bool]string{true: "sweeper-0000000f", false: "sweeper-0000000a"}[finished]
			run := s.run(t, "dev", "sweeper", name, "manual", nil)
			reconcileJob(t, obs, "dev", "sweeper")
			if finished {
				s.succeed(t, run, now, now.Add(time.Minute))
			}
			last := newU(JobGVK)
			s.must(t, s.c.Get(s.ctx, client.ObjectKeyFromObject(run), last))
			s.must(t, s.c.Delete(s.ctx, run, client.PropagationPolicy(metav1.DeletePropagationBackground)))
			for i := 0; i < 50; i++ {
				if err := s.c.Get(s.ctx, client.ObjectKeyFromObject(run), newU(JobGVK)); apierrors.IsNotFound(err) {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			obs.bury(last)
			reconcileJob(t, obs, "dev", "sweeper")
			if finished {
				want(t, "deleted after it finished", pub.types(), events.TypeBatchRunRequested, events.TypeBatchRunStarted, events.TypeBatchRunSucceeded)
			} else {
				want(t, "deleted while active", pub.types(), events.TypeBatchRunRequested, events.TypeBatchRunLost)
			}
			reconcileJob(t, obs, "dev", "sweeper")
		}
	})

	s.installBatchJobCRD(t)

	// A3.2: B starts once for business date D only after A and C succeeded for
	// D — with the bus OFF (the local profile), because the decision is made
	// from Kubernetes objects and the observer's record, never from the bus.
	t.Run("dependsOn starts the run once, after every dependency, with the bus off", func(t *testing.T) {
		bus := events.NewPublisher(events.Options{}) // no bridge: the local profile
		defer bus.Close(context.Background())
		pub := &recorded{next: bus}
		now := time.Date(2026, 11, 4, 2, 0, 0, 0, time.UTC)
		s.batchJob(t, "prod", "extract", map[string]interface{}{"job": "extractJob"}, nil)
		s.batchJob(t, "prod", "rates", map[string]interface{}{"job": "ratesJob"}, nil)
		s.batchJob(t, "prod", "report", map[string]interface{}{"job": "reportJob", "dependsOn": []interface{}{
			map[string]interface{}{"batchJob": "extract"},
			map[string]interface{}{"batchJob": "rates"},
		}}, nil)
		s.runTemplate(t, "prod", "report")
		obs := s.observer(pub, now, true)
		// The observer has seen each job before (its first look records
		// history without acting on it).
		for _, bj := range []string{"extract", "rates", "report"} {
			reconcileJob(t, obs, "prod", bj)
		}

		reports := func() []unstructured.Unstructured {
			l := newList(JobGVK)
			s.must(t, s.c.List(s.ctx, l, client.InNamespace("prod"), client.MatchingLabels{labelBatchJob: "report"}))
			return l.Items
		}
		a := s.run(t, "prod", "extract", "extract-09932cb3", "schedule", map[string]string{"businessDate": "2026-11-03"})
		s.succeed(t, a, now.Add(-time.Hour), now.Add(-30*time.Minute))
		reconcileJob(t, obs, "prod", "extract")
		if got := reports(); len(got) != 0 {
			t.Fatalf("report started before rates succeeded: %d runs", len(got))
		}
		// A different date of the other dependency does not count.
		other := s.run(t, "prod", "rates", "rates-aaaa0001", "schedule", map[string]string{"businessDate": "2026-11-02"})
		s.succeed(t, other, now.Add(-26*time.Hour), now.Add(-25*time.Hour))
		reconcileJob(t, obs, "prod", "rates")
		if got := reports(); len(got) != 0 {
			t.Fatalf("report started on rates for another date: %d runs", len(got))
		}
		c := s.run(t, "prod", "rates", "rates-09932cb3", "schedule", map[string]string{"businessDate": "2026-11-03"})
		s.succeed(t, c, now.Add(-20*time.Minute), now.Add(-5*time.Minute))
		reconcileJob(t, obs, "prod", "rates")

		got := reports()
		if len(got) != 1 {
			t.Fatalf("report runs = %d, want exactly one", len(got))
		}
		b := got[0]
		wantName, _ := runName("report", map[string]string{"businessDate": "2026-11-03", "region": "sg"})
		if b.GetName() != wantName || b.GetLabels()[labelTrigger] != TriggerDependency ||
			envOf(&b, envParams) != `{"businessDate":"2026-11-03","region":"sg"}` || envOf(&b, envTrigger) != TriggerDependency {
			t.Errorf("dependency run %s: trigger %q, params %s", b.GetName(), b.GetLabels()[labelTrigger], envOf(&b, envParams))
		}
		if b.GetAnnotations()[annotationDependsOn] != "prod/extract,prod/rates" {
			t.Errorf("depends-on annotation = %q", b.GetAnnotations()[annotationDependsOn])
		}

		// Once: a second success of a dependency for D, a restarted observer,
		// and another look all start nothing more.
		again := s.run(t, "prod", "rates", "rates-09932cb3-r1", "restart", map[string]string{"businessDate": "2026-11-03"})
		s.succeed(t, again, now.Add(-4*time.Minute), now.Add(-time.Minute))
		restarted := s.observer(pub, now, true)
		reconcileJob(t, restarted, "prod", "rates")
		reconcileJob(t, restarted, "prod", "extract")
		if got := reports(); len(got) != 1 {
			t.Errorf("report runs after a second success = %d, want one", len(got))
		}

		pub.reset()
		reconcileJob(t, restarted, "prod", "report")
		if ts := pub.types(); len(ts) != 1 || ts[0] != events.TypeBatchRunRequested {
			t.Fatalf("the dependency run is published as requested, got %v", ts)
		}
		var d eventData
		_ = json.Unmarshal(pub.got[0].Data, &d)
		if d.Run == nil || d.Run.Trigger != TriggerDependency || d.Dependency == nil || d.Dependency.BusinessDate != "2026-11-03" || len(d.Dependency.After) != 2 {
			t.Errorf("run.requested data = %s", pub.got[0].Data)
		}
		if st := bus.Stats(); st.Dropped == 0 || st.Published != 0 {
			t.Errorf("the bus was off and the events were dropped, not sent: %+v", st)
		}
	})

	// PLAT-3.1's CRD-absent startup: the observer is registered in a manager on
	// a cluster with NO Volcano CRD and no BatchJob CRD at first. The manager
	// must start (a watch on an absent kind would block its cache sync and
	// kill it), runs must be observed from the start, and the BatchJob watch
	// must attach by itself once the CRD appears. The BatchJob CRD exists by
	// now; a fresh manager still has to discover it, and Volcano never.
	t.Run("the manager starts without the Volcano CRD and attaches watches as CRDs appear", func(t *testing.T) {
		saved := discoveryInterval
		discoveryInterval = 300 * time.Millisecond
		defer func() { discoveryInterval = saved }()

		scheme := runtime.NewScheme()
		_ = clientgoscheme.AddToScheme(scheme)
		mgr, err := manager.New(s.cfg.Config, manager.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}})
		if err != nil {
			t.Fatal(err)
		}
		pub := &recorded{}
		obs := &Reconciler{Events: pub}
		if err := obs.SetupWithManager(mgr); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(s.ctx)
		done := make(chan error, 1)
		go func() { done <- mgr.Start(ctx) }()
		defer func() {
			cancel()
			<-done
		}()

		eventually := func(what string, ok func() bool) {
			t.Helper()
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				select {
				case err := <-done:
					t.Fatalf("the manager stopped while waiting for %s: %v", what, err)
				default:
				}
				if ok() {
					return
				}
				time.Sleep(100 * time.Millisecond)
			}
			t.Fatalf("timed out waiting for %s (published %v)", what, pub.types())
		}
		has := func(typ, subject string) func() bool {
			return func() bool {
				pub.mu.Lock()
				defer pub.mu.Unlock()
				for _, e := range pub.got {
					if e.Type == typ && e.Subject == subject {
						return true
					}
				}
				return false
			}
		}
		s.run(t, "dev", "nightly", "nightly-09932cb3", "manual", nil)
		eventually("run.requested from the Job watch", has(events.TypeBatchRunRequested, "dev/nightly"))
		eventually("the BatchJob watch", obs.batchJobsServed)
		if obs.volcanoServed() {
			t.Error("Volcano is not served here and must not be watched")
		}
		s.batchJob(t, "dev", "held", map[string]interface{}{"job": "heldJob"}, map[string]string{annotationHold: "month end"})
		// The first look at a job records its state; the hold is published
		// when it changes after that.
		eventually("the first look at the held job", func() bool {
			return s.c.Get(s.ctx, types.NamespacedName{Namespace: "dev", Name: "held" + stateSuffix}, &corev1.ConfigMap{}) == nil
		})
		xr := newU(BatchJobGVK)
		s.must(t, s.c.Get(s.ctx, types.NamespacedName{Namespace: "dev", Name: "held"}, xr))
		xr.SetAnnotations(nil)
		s.must(t, s.c.Update(s.ctx, xr))
		eventually("schedule.released from the BatchJob watch", has(events.TypeBatchScheduleReleased, "dev/held"))
	})
}
