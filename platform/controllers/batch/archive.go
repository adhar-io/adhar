/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"context"
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"adhar-io/adhar/platform/batcharchive"
	"adhar-io/adhar/platform/events"
)

// The run-summary archive (design 0025 Phase 4, A4.20–A4.24). The observer
// already sees every run end and holds the event that says how; the archive
// keeps that event in the object store after the topic's 90 days, at a key
// computed from the run (platform/batcharchive). It is advisory in the bus's
// sense: Archive returns nothing and never blocks, and a store that is down
// costs `adhar_batch_archive_writes_total{result="failed"}`, never a reconcile.

// Archiver is the one call the observer makes to the archive.
// *batcharchive.Writer satisfies it; tests record instead.
type Archiver interface {
	Archive(ctx context.Context, key string, e events.Event)
}

// archiveLabels: the run's environment (the namespace's adhar.io/environment,
// as on the deadline gauges) so the Mimir alert is critical for prod's runs,
// and the result. Never the job or the run: a store outage is one fact about
// the platform, not one per job.
var archiveLabels = []string{"adhar_environment", "result"}

var archiveWrites = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "adhar_batch_archive_writes_total",
	Help: "Run summaries handed to the object-store archive, by the run's environment and result: written, failed " +
		"(the store refused or did not answer) or dropped (the queue was full and the oldest entry made room).",
}, archiveLabels)

// CountArchive is the writer's OnResult in the manager: it counts.
func CountArchive(r batcharchive.Result) {
	archiveWrites.WithLabelValues(r.Environment, string(r.Outcome)).Inc()
}

// NewArchive is the archive the manager runs, or nil when it is not switched
// on (ADHAR_BATCH_ARCHIVE_URL unset). The credential is read LIVE from the
// Secret on every write, through a reader that does not cache: the manager is
// deployed before RustFS publishes root-creds, so an env var resolved at pod
// start would leave the archive keyless until a restart, and a cluster-wide
// Secret informer is memory spent on one object.
func NewArchive(reader client.Reader, namespace string) *batcharchive.Writer {
	c := batcharchive.FromEnv(func(ctx context.Context, name string) (batcharchive.Credentials, error) {
		var s corev1.Secret
		if err := reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &s); err != nil {
			return batcharchive.Credentials{}, fmt.Errorf("reading the archive credential %s/%s: %w", namespace, name, err)
		}
		return batcharchive.Credentials{
			AccessKey: string(s.Data[batcharchive.CredentialAccessKey]),
			SecretKey: string(s.Data[batcharchive.CredentialSecretKey]),
		}, nil
	})
	if c == nil {
		return nil
	}
	w := batcharchive.NewWriter(c, batcharchive.WriterOptions{})
	w.OnResult = CountArchive
	return w
}

// endsRun says a transition is the run's last word: the archive keeps one
// object per run, holding the event that says how it ended. A `run.lost` for
// one attempt (an OOM-killed pod the Job retries) is not; the `run.lost` of a
// run that vanished or went unobserved is.
func endsRun(t transition) bool {
	switch t.Type {
	case events.TypeBatchRunSucceeded, events.TypeBatchRunFailed, events.TypeBatchRunSkipped:
		return true
	case events.TypeBatchRunLost:
		return t.Lost != nil && (t.Lost.Reason == "Vanished" || t.Lost.Reason == "Unobserved")
	}
	return false
}

// stamp fills what Publish would, so the archived copy and the published one
// are the same event — same id, time and source.
func (r *Reconciler) stamp(e events.Event) events.Event {
	if e.SpecVersion == "" {
		e.SpecVersion = events.SpecVersion
	}
	if e.ID == "" {
		e.ID = events.NewID()
	}
	if e.Time == "" {
		e.Time = events.Timestamp(events.Now())
	}
	if e.Source == "" {
		e.Source = r.Source
	}
	return e
}

// archived is a published event that is also archived, at key.
type archived struct {
	index int
	key   string
}

// archiveEntry says where a run's final event goes; a run whose name cannot be
// a key (none can, in practice) is not archived.
func archiveEntry(index int, o observation) (archived, bool) {
	key, err := batcharchive.Key(o.Namespace, o.BatchJob, o.BusinessDate, o.Name)
	if err != nil {
		return archived{}, false
	}
	return archived{index: index, key: key}, true
}
