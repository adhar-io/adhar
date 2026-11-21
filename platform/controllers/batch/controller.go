/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

// Package batch is the batch observer (design 0025 §12, PLAT-3.1): it watches
// the runs of every BatchJob and turns what happens to them into the platform's
// `adhar.batch` CloudEvents, starts the runs `dependsOn` asks for, and keeps the
// `finishBy` SLA.
//
// # What it reads, and why only that
//
// Runs (batch/v1 Jobs and Volcano Jobs) and their pods, through informers that
// select `adhar.io/workload=batch` — a cluster's other Jobs and pods never
// reach its cache. The BatchJob XRs, for the hold, the schedule, `dependsOn` and
// the SLA. The namespace, for the hierarchy an event belongs to: an event's
// organisation and team come from where the run IS, never from labels a tenant
// wrote on its pod (§5.14).
//
// # Two kinds that may not exist yet
//
// BatchJob is defined by a Crossplane XRD and Volcano by its own package, so
// either CRD can be absent when the manager starts — on a fresh cluster both
// are, and Volcano is off in the local profile. An informer on an absent kind
// blocks the manager's cache sync and kills it at WaitForCacheSyncTimeout,
// which has emptied Argo CD before (memory: bootstrap 2-min cache-sync death).
// So those two watches are added only after discovery shows the CRD, by a
// poller, and everything that reads them first asks whether they are watched.
//
// # The bus is advisory; dependsOn is not
//
// Publishing follows platform/events/CONTRACT.md: a bounded queue, transitions
// only, the outcome inside the event, and nothing a reconcile waits for. The
// `dependsOn` decision is made from Kubernetes objects and the observer's own
// record, never from the bus, so it works in the local profile where Kafka is
// off and through a bus outage (A3.2, F22).
package batch

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"adhar-io/adhar/platform/events"
)

var (
	// JobGVK, PodGVK, VolcanoJobGVK and BatchJobGVK are the kinds the observer
	// reads, as unstructured: a Volcano Job and a BatchJob have no Go type in
	// this repository, and one representation keeps the model code single.
	JobGVK        = schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}
	PodGVK        = schema.GroupVersionKind{Version: "v1", Kind: "Pod"}
	VolcanoJobGVK = schema.GroupVersionKind{Group: "batch.volcano.sh", Version: "v1alpha1", Kind: "Job"}
	BatchJobGVK   = schema.GroupVersionKind{Group: "platform.adhar.io", Version: "v1alpha1", Kind: "BatchJob"}
)

// WorkloadSelector selects what a run is made of. Every run Job, Volcano Job
// and pod the composition renders carries it, and adhar-batch-run-integrity
// refuses a run that changes it, so nothing else is needed to find them.
var WorkloadSelector = labels.SelectorFromSet(labels.Set{labelWorkload: "batch"})

// discoveryInterval is how often the observer asks whether BatchJob and
// Volcano are served yet. A minute late for the first BatchJob on a fresh
// cluster costs nothing; its runs are observed from their own watch.
var discoveryInterval = 30 * time.Second

// Reconciler is the batch observer. One reconcile key per BatchJob
// (namespace/name, from the run's adhar.io/batch-job label), so a job's runs,
// its hold, its schedule and its record are judged together, by one worker,
// and no two reconciles ever write one record.
type Reconciler struct {
	// Client writes (dependency runs, the observer's record) and reads the
	// namespaces.
	Client client.Client
	// Runs reads the observer's own label-selected cache: Jobs, pods, Volcano
	// Jobs and BatchJobs. Tests pass a direct client.
	Runs client.Reader
	// APIReader reads ConfigMaps live: the record must be read fresh (it is
	// what makes a transition once), and a cluster-wide ConfigMap informer for
	// a handful of run templates is memory the manager does not need.
	APIReader client.Reader
	Recorder  record.EventRecorder
	// Events is the bus; nil is a working configuration (nothing published).
	Events Publisher
	// Archive keeps each finished run's final event in the object store
	// (Phase 4); nil is a working configuration (nothing archived).
	Archive Archiver
	// Source is the CloudEvents `source` stamped on what is archived, the
	// same one the bus publisher would stamp, so both copies are one event.
	Source string
	// Now is the clock, injectable so the SLA is testable.
	Now func() time.Time

	// BatchJobsServed and VolcanoServed say the kinds are watched. Set by the
	// discovery poller; tests set them directly.
	BatchJobsServed func() bool
	VolcanoServed   func() bool
	// Discovered says discovery has ANSWERED whether BatchJob is served (it
	// is, or its group is not there). Before that the observer cannot tell a
	// run with dependents from one without: judging a success then records it
	// and starts nothing, and after a restart that is a dependency run lost for
	// good. Nil (tests) means answered.
	Discovered func() bool

	mu sync.Mutex
	// tombstones are runs and pods deleted since the job was last reconciled,
	// so a run its TTL removed the moment it finished is still observed as
	// finished, not as vanished, and its pods' summaries are still read.
	tombstones map[types.NamespacedName][]*unstructured.Unstructured
	// alive are the runs THIS process has listed while they existed. A
	// recorded run that is missing and was never among them was deleted while
	// no observer was watching, so its delete event (and final state) never
	// reached this one: it is unobserved, not vanished.
	alive map[types.UID]bool
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Reconciler) batchJobsServed() bool { return r.BatchJobsServed != nil && r.BatchJobsServed() }
func (r *Reconciler) volcanoServed() bool   { return r.VolcanoServed != nil && r.VolcanoServed() }

func newU(gvk schema.GroupVersionKind) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	return u
}

func newList(gvk schema.GroupVersionKind) *unstructured.UnstructuredList {
	l := &unstructured.UnstructuredList{}
	l.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
	return l
}

// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=batch.volcano.sh,resources=jobs,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=pods;namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;create;update
// +kubebuilder:rbac:groups=platform.adhar.io,resources=batchjobs,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile observes one BatchJob's runs.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("batchJob", req.String())
	if r.Discovered != nil && !r.Discovered() {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	now := r.now()

	var xr *unstructured.Unstructured
	if r.batchJobsServed() {
		obj := newU(BatchJobGVK)
		if err := r.Runs.Get(ctx, req.NamespacedName, obj); err == nil {
			xr = obj
		} else if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}

	runs, pods, err := r.listRuns(ctx, req.Namespace, req.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	// The tombstones as they are now, AFTER the list: a run deleted between
	// the two is judged from its tombstone (its final state, fresher than the
	// copy listed), and one buried later in this reconcile is left for the
	// next, which lists without the run and needs it.
	runs, pods, buried := r.withTombstones(req.NamespacedName, runs, pods)

	cm, state, existed, err := r.loadState(ctx, req.Namespace, req.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	before := state.encode()
	baseline := !existed

	h := r.hierarchy(ctx, req.Namespace)
	var spec *jobSpec
	var cron *cronSchedule
	if xr != nil {
		s := specOf(xr)
		spec = &s
		if s.Cron != "" {
			cron, _ = parseCron(s.Cron, zone(s.TimeZone))
		}
	}
	tz := "UTC"
	if spec != nil {
		tz = spec.TimeZone
	}

	fields, fieldsRead := []paramField(nil), false
	paramFields := func() []paramField {
		if !fieldsRead {
			fieldsRead = true
			fields = r.templateFields(ctx, req.Namespace, req.Name)
		}
		return fields
	}

	var pub []events.Event
	// final are the events in pub that end a run, and where each is archived.
	var final []archived
	ended := func(t transition, o observation) {
		if a, ok := archiveEntry(len(pub)-1, o); ok && endsRun(t) {
			final = append(final, a)
		}
	}
	var satisfied []observation
	var observed []observation
	seen := map[string]bool{}
	for _, run := range runs {
		o := observe(run, pods)
		if o.BatchJob != req.Name {
			continue
		}
		if o.BusinessDate == "" {
			o.BusinessDate = dateInZone(firstTime(o.Finished, o.Created), firstNonEmpty(envOf(run, envTimeZone), tz))
		}
		seen[o.UID] = true
		observed = append(observed, o)
		if !buried[types.UID(o.UID)] {
			r.sawAlive(types.UID(o.UID))
		}
		rec := state.record(o.UID)
		if rec == nil {
			state.Runs = append(state.Runs, runRecord{UID: o.UID, Name: o.Name, Kind: o.Kind,
				Scheduled: stamp(slotOf(o, cron, startingDeadline(spec))), Created: stamp(o.Created)})
			rec = &state.Runs[len(state.Runs)-1]
		}
		wasSatisfied := rec.Satisfies
		for _, t := range transitionsOf(o, rec, baseline) {
			pub = append(pub, runEvent(t, o, paramFields(), h))
			ended(t, o)
		}
		if rec.Satisfies && !wasSatisfied && !baseline {
			satisfied = append(satisfied, o)
		}
		// A run deleted since the last look is observed one last time from its
		// final state: if that state had ended, it is history; if not, it
		// vanished, and that is the last thing said about it.
		if buried[types.UID(o.UID)] {
			if rec.End == "" {
				t := vanished(rec)
				pub = append(pub, runEvent(t, o, paramFields(), h))
				ended(t, o)
			}
			rec.Gone = true
		}
	}
	// A recorded run that is no longer there: it finished and was cleaned up,
	// or it was deleted before it ended (vanished, published as run.lost). A
	// run of a kind that is not watched yet is neither — it is merely unseen.
	for i := range state.Runs {
		rec := &state.Runs[i]
		if seen[rec.UID] || rec.Gone {
			continue
		}
		if rec.Kind == kindVolcanoJob && !r.volcanoServed() {
			continue
		}
		if rec.End != "" {
			rec.Gone = true
			continue
		}
		// Its delete event landed after this reconcile took the tombstones:
		// the next reconcile judges it from its final state.
		if r.hasTombstone(req.NamespacedName, types.UID(rec.UID)) {
			continue
		}
		t := unobserved(rec)
		if r.wasAlive(types.UID(rec.UID)) {
			t = vanished(rec)
		}
		o := observation{UID: rec.UID, Name: rec.Name, Namespace: req.Namespace, Kind: rec.Kind, BatchJob: req.Name, BusinessDate: rec.BusinessDate}
		if spec != nil {
			o.Application = spec.Application
		}
		pub = append(pub, runEvent(t, o, nil, h))
		ended(t, o)
	}

	var result ctrl.Result
	if spec != nil {
		history := append([]observation(nil), observed...)
		for _, rec := range state.Runs {
			if rec.Gone {
				history = append(history, recordedRun(rec))
			}
		}
		outcome := evaluateSchedule(*spec, history, state, baseline, now)
		for _, t := range outcome.Transitions {
			pub = append(pub, jobEvent(t, *spec, h))
		}
		gauges.publish(req.NamespacedName, spec.Application, h, outcome.Deadlines)
		if spec.Cron != "" || spec.FinishBy != "" {
			result.RequeueAfter = outcome.RequeueAfter
		}
	} else {
		gauges.publish(req.NamespacedName, "", h, nil)
	}

	// dependsOn BEFORE the record is written: a dependency run that could not
	// be created (the API server was briefly unavailable) is retried with the
	// transition that caused it, because neither is recorded yet. Creating it
	// is idempotent — the run's name is its identity — so a retry after the
	// create succeeded and the record write failed starts nothing twice.
	for _, o := range satisfied {
		if err := r.startDependents(ctx, req.Namespace, req.Name, o, state); err != nil {
			return ctrl.Result{}, err
		}
	}
	wait, err := r.retryPending(ctx, state)
	if err != nil {
		return ctrl.Result{}, err
	}
	if wait > 0 && (result.RequeueAfter == 0 || wait < result.RequeueAfter) {
		result.RequeueAfter = wait
	}

	state.compact()
	if after := state.encode(); after != before || !existed {
		if err := r.saveState(ctx, cm, req.Namespace, req.Name, xr, after); err != nil {
			return ctrl.Result{}, err
		}
	}
	r.clearTombstones(req.NamespacedName, buried)
	r.forgetGone(state)

	// Publish last, after the record says it was published: a crash between
	// the two loses an event (which the bus contract allows) and never sends
	// one twice (which "exactly one" does not).
	for i := range pub {
		pub[i] = r.stamp(pub[i])
	}
	if r.Events != nil {
		for _, e := range pub {
			r.Events.Publish(ctx, e)
		}
	}
	// The archive takes the same events, after the bus and on the same
	// terms: once per transition, advisory, never waited for.
	if r.Archive != nil {
		for _, a := range final {
			r.Archive.Archive(ctx, a.key, pub[a.index])
		}
	}
	if len(pub) > 0 {
		logger.V(1).Info("batch transitions", "events", len(pub))
	}
	return result, nil
}

func firstTime(ts ...time.Time) time.Time {
	for _, t := range ts {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

func startingDeadline(spec *jobSpec) time.Duration {
	if spec == nil {
		return time.Hour
	}
	return spec.StartingDeadline
}

// recordedRun turns the record of a deleted run back into the little the SLA
// and missed-slot rules need: when it was for, and how it ended.
func recordedRun(rec runRecord) observation {
	o := observation{UID: rec.UID, Name: rec.Name, Kind: rec.Kind, ScheduledAt: rec.Scheduled,
		Created: parseTime(rec.Created), Finished: parseTime(rec.Finished), BusinessDate: rec.BusinessDate}
	switch rec.End {
	case endSucceeded, endSkipped:
		o.Phase = phaseSucceeded
	default:
		o.Phase = phaseFailed
	}
	if rec.Scheduled != "" {
		o.Trigger = TriggerSchedule
	}
	return o
}

func (r *Reconciler) listRuns(ctx context.Context, namespace, batchJob string) ([]*unstructured.Unstructured, []unstructured.Unstructured, error) {
	sel := client.MatchingLabels{labelBatchJob: batchJob}
	var runs []*unstructured.Unstructured
	kinds := []schema.GroupVersionKind{JobGVK}
	if r.volcanoServed() {
		kinds = append(kinds, VolcanoJobGVK)
	}
	for _, gvk := range kinds {
		l := newList(gvk)
		if err := r.Runs.List(ctx, l, client.InNamespace(namespace), sel); err != nil {
			return nil, nil, err
		}
		for i := range l.Items {
			runs = append(runs, &l.Items[i])
		}
	}
	pods := newList(PodGVK)
	if err := r.Runs.List(ctx, pods, client.InNamespace(namespace), sel); err != nil {
		return nil, nil, err
	}
	sort.SliceStable(runs, func(i, j int) bool {
		return runs[i].GetCreationTimestamp().Time.Before(runs[j].GetCreationTimestamp().Time)
	})
	return runs, pods.Items, nil
}

func (r *Reconciler) hierarchy(ctx context.Context, namespace string) hierarchy {
	ns := &corev1.Namespace{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: namespace}, ns); err != nil {
		return hierarchy{}
	}
	return hierarchyOf(ns.Labels)
}

// templateFields reads the declared parameters, for masking. A job whose
// template cannot be read still gets its events, with names only.
func (r *Reconciler) templateFields(ctx context.Context, namespace, batchJob string) []paramField {
	cm := &corev1.ConfigMap{}
	if err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: batchJob + "-run"}, cm); err != nil {
		return nil
	}
	fields, _ := parseParameterFields(cm.Data["parameters.json"])
	return fields
}

/* ─────────────────────────── the record ─────────────────────────── */

func (r *Reconciler) loadState(ctx context.Context, namespace, batchJob string) (*corev1.ConfigMap, *jobState, bool, error) {
	cm := &corev1.ConfigMap{}
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: batchJob + stateSuffix}, cm)
	switch {
	case apierrors.IsNotFound(err):
		return nil, decodeState(""), false, nil
	case err != nil:
		return nil, nil, false, err
	}
	return cm, decodeState(cm.Data[stateKey]), true, nil
}

func (r *Reconciler) saveState(ctx context.Context, cm *corev1.ConfigMap, namespace, batchJob string, xr *unstructured.Unstructured, data string) error {
	if cm == nil {
		cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
			Name:      batchJob + stateSuffix,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "adhar-batch-observer",
				"adhar.io/component":           "batch-observer",
			},
			Annotations: map[string]string{
				"adhar.io/batch-observer-of": batchJob,
			},
		}}
		if xr != nil {
			// Owned by the BatchJob, so the record goes with the job. Not a
			// controller reference: Crossplane is the job's controller.
			cm.OwnerReferences = []metav1.OwnerReference{{
				APIVersion: BatchJobGVK.GroupVersion().String(), Kind: BatchJobGVK.Kind,
				Name: xr.GetName(), UID: xr.GetUID(),
			}}
		}
		cm.Data = map[string]string{stateKey: data}
		return r.Client.Create(ctx, cm)
	}
	cm = cm.DeepCopy()
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[stateKey] = data
	return r.Client.Update(ctx, cm)
}

/* ─────────────────────────── tombstones ─────────────────────────── */

func (r *Reconciler) bury(obj client.Object) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return
	}
	bj := u.GetLabels()[labelBatchJob]
	if bj == "" {
		return
	}
	key := types.NamespacedName{Namespace: u.GetNamespace(), Name: bj}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tombstones == nil {
		r.tombstones = map[types.NamespacedName][]*unstructured.Unstructured{}
	}
	r.tombstones[key] = append(r.tombstones[key], u.DeepCopy())
}

// withTombstones adds the deleted runs and pods to what was listed, and
// returns the UIDs of the tombstones it took (runs and pods alike): only those
// are this reconcile's to judge and to clear.
func (r *Reconciler) withTombstones(key types.NamespacedName, runs []*unstructured.Unstructured, pods []unstructured.Unstructured) ([]*unstructured.Unstructured, []unstructured.Unstructured, map[types.UID]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	taken := map[types.UID]bool{}
	for _, t := range r.tombstones[key] {
		taken[t.GetUID()] = true
	}
	// A tombstone REPLACES a listed copy of the same object: the delete event
	// carries its final state, and a list that ran before the delete does not.
	keptRuns := runs[:0:0]
	for _, run := range runs {
		if !taken[run.GetUID()] {
			keptRuns = append(keptRuns, run)
		}
	}
	keptPods := pods[:0:0]
	for _, p := range pods {
		if !taken[p.GetUID()] {
			keptPods = append(keptPods, p)
		}
	}
	for _, t := range r.tombstones[key] {
		if t.GetKind() == PodGVK.Kind && t.GetAPIVersion() == "v1" {
			keptPods = append(keptPods, *t)
		} else {
			keptRuns = append(keptRuns, t)
		}
	}
	sort.SliceStable(keptRuns, func(i, j int) bool {
		return keptRuns[i].GetCreationTimestamp().Time.Before(keptRuns[j].GetCreationTimestamp().Time)
	})
	return keptRuns, keptPods, taken
}

func (r *Reconciler) sawAlive(uid types.UID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.alive == nil {
		r.alive = map[types.UID]bool{}
	}
	r.alive[uid] = true
}

func (r *Reconciler) wasAlive(uid types.UID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.alive[uid]
}

// forgetGone drops the runs whose story is over from `alive`, so it holds the
// live runs and not every run the manager has ever seen.
func (r *Reconciler) forgetGone(state *jobState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range state.Runs {
		if rec.Gone {
			delete(r.alive, types.UID(rec.UID))
		}
	}
}

func (r *Reconciler) hasTombstone(key types.NamespacedName, uid types.UID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.tombstones[key] {
		if t.GetUID() == uid {
			return true
		}
	}
	return false
}

// clearTombstones forgets the tombstones this reconcile took. One buried since
// — the delete event of a run (or of its pods, which carry its summary) that
// this reconcile listed while it still existed — stays for the next reconcile,
// which lists without it.
func (r *Reconciler) clearTombstones(key types.NamespacedName, taken map[types.UID]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var keep []*unstructured.Unstructured
	for _, t := range r.tombstones[key] {
		if !taken[t.GetUID()] {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(r.tombstones, key)
		return
	}
	r.tombstones[key] = keep
}

/* ─────────────────────────── dependsOn (§12, A3.2) ─────────────────────────── */

// startDependents starts, for every BatchJob that depends on the job whose run
// just succeeded, the run for that business date — once every job it depends
// on has succeeded for it. The run is named by naming v1 for its identity, so
// "once" is the API server's: a second create for the same date is
// AlreadyExists, whoever tries it and however often.
func (r *Reconciler) startDependents(ctx context.Context, namespace, batchJob string, done observation, own *jobState) error {
	if !r.batchJobsServed() {
		return nil
	}
	all := newList(BatchJobGVK)
	if err := r.Runs.List(ctx, all); err != nil {
		return err
	}
	me := namespace + "/" + batchJob
	for i := range all.Items {
		xr := &all.Items[i]
		spec := specOf(xr)
		var trigger *dependency
		for j := range spec.DependsOn {
			d := spec.DependsOn[j]
			if d.Namespace+"/"+d.BatchJob == me {
				trigger = &d
			}
		}
		if trigger == nil {
			continue
		}
		date := done.BusinessDate
		if date == "" {
			date = dateInZone(r.now(), spec.TimeZone)
		}
		ok, waiting, err := r.dependenciesMet(ctx, spec, me, date, own)
		if err != nil {
			return err
		}
		if !ok {
			log.FromContext(ctx).V(1).Info("dependsOn not yet met", "batchJob", spec.Namespace+"/"+spec.Name, "businessDate", date, "waitingFor", waiting)
			continue
		}
		if spec.Held {
			r.warn(xr, "DependencyHeld", "every job it depends on succeeded for %s, but the job is held (%s); no run was started", date, spec.HoldReason)
			continue
		}
		later, err := r.createDependencyRun(ctx, spec, date)
		if err != nil {
			return err
		}
		if later {
			own.pend(spec.Namespace, spec.Name, date, r.now())
		}
	}
	return nil
}

// retryPending retries the dependency runs a quota refused, each on its own
// backoff, and says when the next one is due (0: none). It gives up on a run
// after recentWindow — a quota full for a day is a decision for a person, and
// the Warning Events have said so all along.
func (r *Reconciler) retryPending(ctx context.Context, own *jobState) (time.Duration, error) {
	if len(own.Pending) == 0 {
		return 0, nil
	}
	now := r.now()
	var keep []pendingRun
	var wait time.Duration
	due := func(at time.Time) {
		if d := at.Sub(now); d > 0 && (wait == 0 || d < wait) {
			wait = d
		}
	}
	for _, p := range own.Pending {
		next := parseTime(p.Next)
		if now.Before(next) || !r.batchJobsServed() {
			keep = append(keep, p)
			due(next)
			continue
		}
		xr := newU(BatchJobGVK)
		if err := r.Runs.Get(ctx, types.NamespacedName{Namespace: p.Namespace, Name: p.BatchJob}, xr); err != nil {
			if apierrors.IsNotFound(err) {
				continue // the dependent job is gone, and its run with it
			}
			return 0, err
		}
		spec := specOf(xr)
		if now.Sub(parseTime(p.Since)) > recentWindow {
			r.warn(xr, "DependencyRunNotStarted", "gave up starting the run for %s: refused by a ResourceQuota since %s", p.BusinessDate, p.Since)
			continue
		}
		if spec.Held {
			r.warn(xr, "DependencyHeld", "every job it depends on succeeded for %s, but the job is held (%s); no run was started", p.BusinessDate, spec.HoldReason)
			continue
		}
		later, err := r.createDependencyRun(ctx, spec, p.BusinessDate)
		if err != nil {
			return 0, err
		}
		if later {
			p.Attempts++
			p.Next = stamp(now.Add(pendingBackoff(p.Attempts)))
			keep = append(keep, p)
			due(parseTime(p.Next))
		}
	}
	own.Pending = keep
	return wait, nil
}

// quotaRefused is the one Forbidden that passes by itself: a ResourceQuota is
// full (`exceeded quota: …, requested: count/jobs.batch=1`) and frees as the
// namespace's other runs end. Every other refusal — run integrity, a
// LimitRange — fails the same object the same way until Git changes.
func quotaRefused(err error) bool {
	return apierrors.IsForbidden(err) && strings.Contains(err.Error(), "exceeded quota")
}

// dependenciesMet checks every job a BatchJob depends on. The job whose run
// just succeeded is met by that run; the others by their records or their live
// runs. A dependency with sameBusinessDate: false is met by any success.
func (r *Reconciler) dependenciesMet(ctx context.Context, spec jobSpec, me, date string, own *jobState) (bool, []string, error) {
	var waiting []string
	for _, d := range spec.DependsOn {
		key := d.Namespace + "/" + d.BatchJob
		var st *jobState
		if key == me {
			st = own
		} else {
			_, loaded, _, err := r.loadState(ctx, d.Namespace, d.BatchJob)
			if err != nil {
				return false, nil, err
			}
			st = loaded
			// Runs that finished since the record was last written count too.
			runs, pods, err := r.listRuns(ctx, d.Namespace, d.BatchJob)
			if err != nil {
				return false, nil, err
			}
			for _, run := range runs {
				o := observe(run, pods)
				if satisfiesDependency(o) {
					bd := firstNonEmpty(o.BusinessDate, dateInZone(firstTime(o.Finished, o.Created), envOf(run, envTimeZone)))
					st.Runs = append(st.Runs, runRecord{UID: o.UID, Satisfies: true, BusinessDate: bd})
				}
			}
		}
		met := st.succeededFor(date)
		if !d.SameBusinessDate {
			met = st.latestSuccess() != ""
		}
		if !met {
			waiting = append(waiting, key)
		}
	}
	return len(waiting) == 0, waiting, nil
}

// createDependencyRun builds the run from the job's own template, exactly as
// `adhar batch run <job> -p businessDate=D` would, with trigger dependency.
// What makes a run impossible to start (no template yet, no business-date
// parameter, a refused value) is told on the BatchJob and not retried: the same
// inputs fail the same way until the job is changed in Git.
//
// later says the API server refused it for now (quotaRefused): the caller
// records it as pending and retries it.
func (r *Reconciler) createDependencyRun(ctx context.Context, spec jobSpec, date string) (later bool, err error) {
	cm := &corev1.ConfigMap{}
	if err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: spec.Namespace, Name: spec.Name + "-run"}, cm); err != nil {
		if apierrors.IsNotFound(err) {
			r.warn(spec.Obj, "DependencyRunNotStarted", "every job it depends on succeeded for %s, but ConfigMap %s-run does not exist yet", date, spec.Name)
			return false, nil
		}
		return false, err
	}
	t, err := parseRunTemplate(cm.Data)
	if err != nil {
		r.warn(spec.Obj, "DependencyRunNotStarted", "the run template cannot be read: %v", err)
		return false, nil
	}
	f, ok := businessDateField(t.Fields)
	if !ok {
		r.warn(spec.Obj, "DependencyRunNotStarted", "dependsOn starts a run for a business date, and this job declares no businessDate parameter")
		return false, nil
	}
	resolved := resolveParams(t.Fields, map[string]string{f.Name: date}, envOf(t.Manifest, envTimeZone), r.now())
	if len(resolved.Errors) > 0 {
		r.warn(spec.Obj, "DependencyRunNotStarted", "parameters refused for %s: %s", date, resolved.problems())
		return false, nil
	}
	name, err := runName(spec.Name, resolved.Identifying)
	if err != nil {
		r.warn(spec.Obj, "DependencyRunNotStarted", "%v", err)
		return false, nil
	}
	run := buildRun(t, name, spec.Namespace, TriggerDependency, resolved.Params)
	var after []string
	for _, d := range spec.DependsOn {
		after = append(after, d.Namespace+"/"+d.BatchJob)
	}
	ann := run.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}
	ann[annotationDependsOn] = strings.Join(after, ",")
	run.SetAnnotations(ann)
	switch err := r.Client.Create(ctx, run); {
	case err == nil:
		r.event(spec.Obj, corev1.EventTypeNormal, "DependencyRunStarted", "started %s for business date %s: %s succeeded for it", name, date, strings.Join(after, ", "))
	case apierrors.IsAlreadyExists(err):
		// Started already — by this observer before a restart, by a person, or
		// by the clock: once is once.
	case meta.IsNoMatchError(err):
		r.warn(spec.Obj, "DependencyRunNotStarted", "the run's kind is not served: %v", err)
	case quotaRefused(err):
		// A full quota frees as the namespace's runs end: retried from the
		// record (retryPending), because the success that called for this
		// run is recorded and never calls for it again.
		r.warn(spec.Obj, "DependencyRunWaiting", "%s for %s is waiting for quota: %v", name, date, err)
		return true, nil
	case apierrors.IsForbidden(err) || apierrors.IsInvalid(err):
		// Admission refused it (run integrity, a LimitRange): retrying the
		// same object changes nothing.
		r.warn(spec.Obj, "DependencyRunNotStarted", "%s for %s was refused: %v", name, date, err)
	default:
		return false, fmt.Errorf("starting dependency run %s/%s: %w", spec.Namespace, name, err)
	}
	return false, nil
}

func (r *Reconciler) warn(obj *unstructured.Unstructured, reason, format string, args ...interface{}) {
	r.event(obj, corev1.EventTypeWarning, reason, format, args...)
}

func (r *Reconciler) event(obj *unstructured.Unstructured, typ, reason, format string, args ...interface{}) {
	if r.Recorder != nil && obj != nil {
		r.Recorder.Eventf(obj, typ, reason, format, args...)
	}
}

/* ─────────────────────────── wiring ─────────────────────────── */

// splitReader reads BatchJobs from their cache and everything else from the
// label-selected run cache.
type splitReader struct{ runs, batchJobs client.Reader }

func (s splitReader) pick(obj runtime.Object) client.Reader {
	if obj.GetObjectKind().GroupVersionKind().GroupKind() == BatchJobGVK.GroupKind() ||
		obj.GetObjectKind().GroupVersionKind().Kind == BatchJobGVK.Kind+"List" && obj.GetObjectKind().GroupVersionKind().Group == BatchJobGVK.Group {
		return s.batchJobs
	}
	return s.runs
}

func (s splitReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return s.pick(obj).Get(ctx, key, obj, opts...)
}

func (s splitReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return s.pick(list).List(ctx, list, opts...)
}

// ownCache puts the observer's cache in the manager's cache group, so it is
// started and synced before any controller, like the manager's own.
type ownCache struct{ cache.Cache }

func (c ownCache) GetCache() cache.Cache { return c.Cache }

// SetupWithManager registers the observer. Its watches on Jobs and pods start
// with the manager; BatchJob and Volcano Job watches are added by a poller once
// discovery shows their CRDs (see the package comment).
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	registerMetrics.Do(func() { ctrlmetrics.Registry.MustRegister(deadlineRemaining, deadlineSlack, archiveWrites) })

	// Two caches, both built with nothing that needs a REST mapping up front:
	// naming a kind in ByObject resolves it when the cache is BUILT, so a
	// Volcano Job entry fails the whole manager on a cluster without Volcano
	// (TestObserverOnARealAPIServer caught exactly that). The run cache selects
	// adhar.io/workload=batch for every kind it is ever asked for, Volcano Jobs
	// included once they are watched; BatchJobs carry no such label and live
	// in a cache of their own.
	runCache, err := cache.New(mgr.GetConfig(), cache.Options{
		Scheme: mgr.GetScheme(), Mapper: mgr.GetRESTMapper(), DefaultLabelSelector: WorkloadSelector,
	})
	if err != nil {
		return fmt.Errorf("building the batch observer cache: %w", err)
	}
	jobCache, err := cache.New(mgr.GetConfig(), cache.Options{Scheme: mgr.GetScheme(), Mapper: mgr.GetRESTMapper()})
	if err != nil {
		return fmt.Errorf("building the BatchJob cache: %w", err)
	}
	for _, c := range []cache.Cache{runCache, jobCache} {
		if err := mgr.Add(ownCache{c}); err != nil {
			return err
		}
	}
	if r.Client == nil {
		r.Client = mgr.GetClient()
	}
	if r.Runs == nil {
		r.Runs = splitReader{runs: runCache, batchJobs: jobCache}
	}
	if r.APIReader == nil {
		r.APIReader = mgr.GetAPIReader()
	}
	if r.Recorder == nil {
		r.Recorder = mgr.GetEventRecorderFor("batch-observer") //nolint:staticcheck // the record API the other controllers use
	}

	c, err := controller.New("batch-observer", mgr, controller.Options{Reconciler: r, MaxConcurrentReconciles: 1})
	if err != nil {
		return err
	}
	toJob := r.runHandler()
	if err := c.Watch(source.Kind(runCache, client.Object(newU(JobGVK)), toJob)); err != nil {
		return err
	}
	if err := c.Watch(source.Kind(runCache, client.Object(newU(PodGVK)), toJob)); err != nil {
		return err
	}

	dc, err := discovery.NewDiscoveryClientForConfig(mgr.GetConfig())
	if err != nil {
		return err
	}
	w := &kindWatcher{
		discovery: dc,
		watch: func(gvk schema.GroupVersionKind) error {
			h := toJob
			if gvk == BatchJobGVK {
				h = handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
					return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: o.GetNamespace(), Name: o.GetName()}}}
				})
			}
			from := runCache
			if gvk == BatchJobGVK {
				from = jobCache
			}
			return c.Watch(source.Kind(from, client.Object(newU(gvk)), h))
		},
	}
	r.BatchJobsServed = func() bool { return w.served(BatchJobGVK) }
	r.VolcanoServed = func() bool { return w.served(VolcanoJobGVK) }
	r.Discovered = func() bool { return w.discovered(BatchJobGVK) }
	return mgr.Add(w)
}

// runHandler maps a run or a pod to its BatchJob, and keeps what was deleted
// so the next reconcile still sees it once.
func (r *Reconciler) runHandler() handler.EventHandler {
	mapped := func(o client.Object) []reconcile.Request {
		bj := o.GetLabels()[labelBatchJob]
		if bj == "" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: o.GetNamespace(), Name: bj}}}
	}
	add := func(q workqueue.TypedRateLimitingInterface[reconcile.Request], o client.Object) {
		for _, req := range mapped(o) {
			q.Add(req)
		}
	}
	return handler.Funcs{
		CreateFunc: func(_ context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			add(q, e.Object)
		},
		UpdateFunc: func(_ context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			add(q, e.ObjectNew)
		},
		DeleteFunc: func(_ context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			r.bury(e.Object)
			add(q, e.Object)
		},
	}
}

// kindWatcher adds the BatchJob and Volcano Job watches once their CRDs are
// served, and never before (the package comment says why).
type kindWatcher struct {
	discovery discovery.DiscoveryInterface
	watch     func(schema.GroupVersionKind) error

	mu      sync.Mutex
	watched map[schema.GroupVersionKind]bool
	// answered are the kinds discovery has definitively reported as not served.
	answered map[schema.GroupVersionKind]bool
}

// NeedLeaderElection: the watches belong to a controller that runs only on the
// leader, and adding them is what makes reads of those kinds safe.
func (w *kindWatcher) NeedLeaderElection() bool { return true }

func (w *kindWatcher) served(gvk schema.GroupVersionKind) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.watched[gvk]
}

func (w *kindWatcher) Start(ctx context.Context) error {
	for {
		w.poll(ctx)
		// The observer waits for the first answer about BatchJob (see
		// Reconciler.Discovered), so a failed first call is retried soon.
		next := discoveryInterval
		if !w.discovered(BatchJobGVK) && next > 2*time.Second {
			next = 2 * time.Second
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(next):
		}
	}
}

// discovered says discovery has answered for the kind at least once: it is
// served, or its group-version is not (404). A failed discovery call is no
// answer.
func (w *kindWatcher) discovered(gvk schema.GroupVersionKind) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.watched[gvk] || w.answered[gvk]
}

func (w *kindWatcher) poll(ctx context.Context) {
	for _, gvk := range []schema.GroupVersionKind{BatchJobGVK, VolcanoJobGVK} {
		if w.served(gvk) {
			continue
		}
		served, answered := kindServed(w.discovery, gvk)
		if answered && !served {
			w.mu.Lock()
			if w.answered == nil {
				w.answered = map[schema.GroupVersionKind]bool{}
			}
			w.answered[gvk] = true
			w.mu.Unlock()
		}
		if !served {
			continue
		}
		if err := w.watch(gvk); err != nil {
			log.FromContext(ctx).Error(err, "batch observer: starting a watch", "kind", gvk.String())
			continue
		}
		w.mu.Lock()
		if w.watched == nil {
			w.watched = map[schema.GroupVersionKind]bool{}
		}
		w.watched[gvk] = true
		w.mu.Unlock()
		log.FromContext(ctx).Info("batch observer: watching", "kind", gvk.String())
	}
}

// kindServed asks discovery for one group-version's resources. Served is
// "watch it now"; answered says the server replied at all — a 404 (the group
// is not served) is an answer, an error of any other kind (discovery briefly
// unavailable, an overloaded API server) is not, and either way it is asked
// again on the next poll.
func kindServed(dc discovery.DiscoveryInterface, gvk schema.GroupVersionKind) (served, answered bool) {
	list, err := dc.ServerResourcesForGroupVersion(gvk.GroupVersion().String())
	if err != nil {
		return false, apierrors.IsNotFound(err)
	}
	if list == nil {
		return false, true
	}
	for _, res := range list.APIResources {
		if res.Kind == gvk.Kind && !strings.Contains(res.Name, "/") {
			return true, true
		}
	}
	return false, true
}
