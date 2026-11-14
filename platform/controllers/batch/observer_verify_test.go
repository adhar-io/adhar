package batch

// The verifier's break attempts (BATCH-P3 observer): the two ways a run's end
// could still be misreported, and the two shapes PLAT-3.1's own envtest does
// not reach — a dependency in ANOTHER namespace, and a manager that starts
// before the BatchJob CRD exists at all.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	fakediscovery "k8s.io/client-go/discovery/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clienttesting "k8s.io/client-go/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"adhar-io/adhar/platform/events"
)

// interrupted is a reader that runs `hook` once, right after the first call
// `at` matches has been answered: the moment a delete event lands while a
// reconcile is between reading the runs and writing its record.
type interrupted struct {
	client.Reader
	mu   sync.Mutex
	at   func(obj runtime.Object) bool
	hook func()
}

func (d *interrupted) fire(obj runtime.Object) {
	d.mu.Lock()
	hook := d.hook
	if hook != nil && d.at(obj) {
		d.hook = nil
	} else {
		hook = nil
	}
	d.mu.Unlock()
	if hook != nil {
		hook()
	}
}

func (d *interrupted) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	err := d.Reader.List(ctx, list, opts...)
	d.fire(list)
	return err
}

func (d *interrupted) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	err := d.Reader.Get(ctx, key, obj, opts...)
	if key.Name != "" {
		obj.SetName(key.Name)
	}
	d.fire(obj)
	return err
}

func jobList(obj runtime.Object) bool {
	return obj.GetObjectKind().GroupVersionKind().Kind == "JobList"
}

func theRecord(obj runtime.Object) bool {
	cm, ok := obj.(*corev1.ConfigMap)
	return ok && strings.HasSuffix(cm.Name, stateSuffix)
}

func fakeCluster(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	prod := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "prod", Labels: map[string]string{"adhar.io/team": "payments"}}}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(append([]client.Object{prod}, objs...)...).Build()
}

func reconcileOnce(t *testing.T, r *Reconciler) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "prod", Name: "importer"}})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	return res
}

// A run that finishes and is deleted (a TTL of 0, a person tidying up) while
// a reconcile of its job is in flight. The delete event's final state is the
// only record of how it ended. Before the fix, a reconcile that had listed the
// run while it was still active either judged that stale copy as "deleted
// before it ended" (the delete landed before it took the tombstones) or threw
// the tombstone away unread (it landed after): either way run.lost (Vanished)
// for a run that SUCCEEDED, and its dependents never started.
func TestADeletionDuringAReconcileIsJudgedFromItsFinalState(t *testing.T) {
	for _, tc := range []struct {
		name string
		// at is where in the reconcile the delete event lands.
		at func(runtime.Object) bool
		// gone says the cache had already dropped the run when the reconcile
		// listed (the informer updates its store before it calls the handler).
		gone bool
		// the events of the reconcile it lands in, and of the next one
		during, next []string
	}{
		{"after the runs are listed", jobList, false, []string{events.TypeBatchRunStarted, events.TypeBatchRunSucceeded}, nil},
		{"after the tombstones are taken", theRecord, false, nil, []string{events.TypeBatchRunStarted, events.TypeBatchRunSucceeded}},
		{"after the store dropped the run", theRecord, true, nil, []string{events.TypeBatchRunStarted, events.TypeBatchRunSucceeded}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			active := runJob("importer-09932cb3", "uid-1", "manual", map[string]string{"businessDate": "2026-11-03"})
			setStatus(active, "active", int64(1))
			c := fakeCluster(t, active.DeepCopy())
			runs := &interrupted{Reader: c, at: tc.at}
			pub := &recorded{}
			r := &Reconciler{Client: c, Runs: runs, APIReader: runs, Events: pub, Now: func() time.Time { return t0.Add(time.Hour) }}

			reconcileOnce(t, r)
			want(t, "first look", pub.types(), events.TypeBatchRunRequested)

			finished := active.DeepCopy()
			setStatus(finished, "active", int64(0))
			setStatus(finished, "startTime", t0.Add(time.Minute).Format(time.RFC3339))
			setStatus(finished, "completionTime", t0.Add(14*time.Minute).Format(time.RFC3339))
			setCond(finished, "Complete", "CompletionsReached", t0.Add(14*time.Minute).Format(time.RFC3339))
			if tc.gone {
				if err := c.Delete(context.Background(), active.DeepCopy()); err != nil {
					t.Fatal(err)
				}
			}
			runs.hook = func() {
				if err := c.Delete(context.Background(), active.DeepCopy()); err != nil && !apierrors.IsNotFound(err) {
					t.Fatal(err)
				}
				r.bury(finished)
			}
			pub.reset()
			reconcileOnce(t, r)
			want(t, "the reconcile the deletion landed in", pub.types(), tc.during...)
			pub.reset()
			reconcileOnce(t, r)
			want(t, "the next reconcile", pub.types(), tc.next...)
			pub.reset()
			reconcileOnce(t, r)
			want(t, "and the one after", pub.types())

			cm := &corev1.ConfigMap{}
			if err := c.Get(context.Background(), types.NamespacedName{Namespace: "prod", Name: "importer" + stateSuffix}, cm); err != nil {
				t.Fatal(err)
			}
			if st := decodeState(cm.Data[stateKey]); len(st.Runs) != 1 || st.Runs[0].End != endSucceeded || !st.Runs[0].Satisfies || !st.Runs[0].Gone {
				t.Errorf("the record must say the run succeeded for its date and is gone: %s", cm.Data[stateKey])
			}
		})
	}
}

// Until discovery has ANSWERED whether BatchJob is served, the observer cannot
// tell a run with dependents from one without: judging it then records the
// success and starts nothing (startDependents sees no BatchJob kind), so after
// a manager restart — or a discovery error at start, which waits a whole poll
// interval — a dependency run is lost for good (A3.2). It must wait instead.
func TestTheObserverJudgesNothingBeforeDiscoveryHasAnswered(t *testing.T) {
	done := runJob("importer-09932cb3", "uid-1", "manual", map[string]string{"businessDate": "2026-11-03"})
	setStatus(done, "completionTime", t0.Add(14*time.Minute).Format(time.RFC3339))
	setCond(done, "Complete", "CompletionsReached", t0.Add(14*time.Minute).Format(time.RFC3339))
	c := fakeCluster(t, done)
	pub := &recorded{}
	answered := false
	r := &Reconciler{Client: c, Runs: c, APIReader: c, Events: pub, Now: func() time.Time { return t0.Add(time.Hour) },
		Discovered: func() bool { return answered }}

	res := reconcileOnce(t, r)
	if res.RequeueAfter <= 0 {
		t.Errorf("a reconcile before discovery must come back, got %+v", res)
	}
	err := c.Get(context.Background(), types.NamespacedName{Namespace: "prod", Name: "importer" + stateSuffix}, &corev1.ConfigMap{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("nothing may be recorded before discovery has answered (err=%v)", err)
	}
	want(t, "before discovery", pub.types())

	answered = true
	reconcileOnce(t, r)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "prod", Name: "importer" + stateSuffix}, &corev1.ConfigMap{}); err != nil {
		t.Errorf("once discovery answered, the job is judged: %v", err)
	}
}

// The poller's three answers: served, definitively not served (404 — the
// group does not exist yet), and no answer (discovery failed). Only the first
// two are an answer.
func TestDiscoveryOnlyAnswersWhenTheServerDid(t *testing.T) {
	fd := &fakediscovery.FakeDiscovery{Fake: &clienttesting.Fake{}}
	fd.PrependReactor("get", "resource", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("the server was unable to return a response in the time allotted")
	})
	w := &kindWatcher{discovery: fd, watch: func(schema.GroupVersionKind) error { return nil }}
	w.poll(context.Background())
	if w.discovered(BatchJobGVK) || w.served(BatchJobGVK) {
		t.Fatal("a discovery error is not an answer")
	}

	fd.Fake.ReactionChain = nil
	w.poll(context.Background())
	if !w.discovered(BatchJobGVK) || w.served(BatchJobGVK) {
		t.Fatal("a 404 for the group is an answer: not served")
	}

	fd.Resources = []*metav1.APIResourceList{{GroupVersion: BatchJobGVK.GroupVersion().String(),
		APIResources: []metav1.APIResource{{Name: "batchjobs", Kind: "BatchJob", Namespaced: true}}}}
	w.poll(context.Background())
	if !w.discovered(BatchJobGVK) || !w.served(BatchJobGVK) {
		t.Fatal("a listed kind is served")
	}
}

func TestObserverBreakAttemptsOnARealAPIServer(t *testing.T) {
	s := startAPIServer(t)

	// A manager started before the BatchJob CRD exists: the observer must
	// keep running, attach the BatchJob watch when the CRD appears, and from
	// then on act on BatchJobs.
	t.Run("the BatchJob CRD installed after the manager started", func(t *testing.T) {
		saved := discoveryInterval
		discoveryInterval = 300 * time.Millisecond
		defer func() { discoveryInterval = saved }()

		scheme := runtime.NewScheme()
		_ = clientgoscheme.AddToScheme(scheme)
		skip := true // TestObserverOnARealAPIServer registers the same controller name in this process
		mgr, err := manager.New(s.cfg.Config, manager.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"},
			Controller: config.Controller{SkipNameValidation: &skip}})
		if err != nil {
			t.Fatal(err)
		}
		pub := &recorded{}
		obs := &Reconciler{Events: pub}
		if err := obs.SetupWithManager(mgr); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(s.ctx)
		stopped := make(chan error, 1)
		go func() { stopped <- mgr.Start(ctx) }()
		defer func() {
			cancel()
			<-stopped
		}()
		eventually := func(what string, ok func() bool) {
			t.Helper()
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				select {
				case err := <-stopped:
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
		published := func(typ, subject string) func() bool {
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

		s.run(t, "dev", "early", "early-09932cb3", "manual", nil)
		eventually("run.requested with no BatchJob CRD", published(events.TypeBatchRunRequested, "dev/early"))
		if obs.batchJobsServed() {
			t.Fatal("BatchJob is not served yet")
		}
		time.Sleep(time.Second) // several polls against an absent group
		s.installBatchJobCRD(t)
		eventually("the BatchJob watch attaching after the CRD appeared", obs.batchJobsServed)
		s.batchJob(t, "dev", "late", map[string]interface{}{"job": "lateJob"}, map[string]string{annotationHold: "audit"})
		eventually("the first look at a BatchJob created after the CRD", func() bool {
			return s.c.Get(s.ctx, types.NamespacedName{Namespace: "dev", Name: "late" + stateSuffix}, &corev1.ConfigMap{}) == nil
		})
		xr := newU(BatchJobGVK)
		s.must(t, s.c.Get(s.ctx, types.NamespacedName{Namespace: "dev", Name: "late"}, xr))
		xr.SetAnnotations(nil)
		s.must(t, s.c.Update(s.ctx, xr))
		eventually("schedule.released through the late watch", published(events.TypeBatchScheduleReleased, "dev/late"))
	})

	// dependsOn across namespaces: `report` in prod waits for `extract` in a
	// data namespace and for `rates` beside it. The success in the OTHER
	// namespace must count, and must not start anything there.
	t.Run("a dependency on a job in another namespace", func(t *testing.T) {
		s.must(t, s.c.Create(s.ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "data"}}))
		s.must(t, s.c.Create(s.ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "data"}}))
		now := time.Date(2026, 11, 5, 2, 0, 0, 0, time.UTC)
		s.batchJob(t, "data", "extract", map[string]interface{}{"job": "extractJob"}, nil)
		s.batchJob(t, "prod", "xrates", map[string]interface{}{"job": "ratesJob"}, nil)
		s.batchJob(t, "prod", "xreport", map[string]interface{}{"job": "reportJob", "dependsOn": []interface{}{
			map[string]interface{}{"batchJob": "extract", "namespace": "data"},
			map[string]interface{}{"batchJob": "xrates"},
		}}, nil)
		s.runTemplate(t, "prod", "xreport")
		obs := s.observer(&recorded{}, now, true)
		reconcileJob(t, obs, "data", "extract")
		reconcileJob(t, obs, "prod", "xrates")
		reconcileJob(t, obs, "prod", "xreport")

		reports := func(ns string) int {
			l := newList(JobGVK)
			s.must(t, s.c.List(s.ctx, l, client.InNamespace(ns), client.MatchingLabels{labelBatchJob: "xreport"}))
			return len(l.Items)
		}
		r := s.run(t, "prod", "xrates", "xrates-09932cb3", "schedule", map[string]string{"businessDate": "2026-11-04"})
		s.succeed(t, r, now.Add(-time.Hour), now.Add(-50*time.Minute))
		reconcileJob(t, obs, "prod", "xrates")
		if n := reports("prod"); n != 0 {
			t.Fatalf("xreport started before the cross-namespace extract succeeded: %d", n)
		}
		e := s.run(t, "data", "extract", "extract-09932cb3", "schedule", map[string]string{"businessDate": "2026-11-04"})
		s.succeed(t, e, now.Add(-40*time.Minute), now.Add(-10*time.Minute))
		reconcileJob(t, obs, "data", "extract")
		if n := reports("prod"); n != 1 {
			t.Fatalf("xreport runs in prod = %d, want one once both dependencies succeeded", n)
		}
		if n := reports("data"); n != 0 {
			t.Errorf("a dependency run belongs to the dependent's namespace, found %d in data", n)
		}
		l := newList(JobGVK)
		s.must(t, s.c.List(s.ctx, l, client.InNamespace("prod"), client.MatchingLabels{labelBatchJob: "xreport"}))
		if got := l.Items[0].GetAnnotations()[annotationDependsOn]; got != "data/extract,prod/xrates" {
			t.Errorf("depends-on annotation = %q", got)
		}
	})
}
