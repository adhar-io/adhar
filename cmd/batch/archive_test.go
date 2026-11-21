/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/batcharchive"
	"adhar-io/adhar/platform/batcharchive/batcharchivetest"
	platformevents "adhar-io/adhar/platform/events"
)

// An archived event the bus still carries is the same event (the observer
// archives what it publishes): shown once, as the bus's. The rest are marked
// archived, in time order, the newest `limit`.
func TestMergedHistoryShowsEachEventOnceAndMarksTheArchived(t *testing.T) {
	old := batchEvent(t, platformevents.TypeBatchRunSucceeded, "2026-05-01T02:00:00Z", nil)
	both := batchEvent(t, platformevents.TypeBatchRunFailed, "2026-11-01T02:00:00Z", nil)
	recent := batchEvent(t, platformevents.TypeBatchRunStarted, "2026-11-02T02:00:00Z", nil)
	got := mergeHistory([]platformevents.Event{both, recent}, []platformevents.Event{both, old}, 0)
	if len(got) != 3 || got[0].ID != old.ID || !got[0].Archived || got[1].ID != both.ID || got[1].Archived || got[2].ID != recent.ID {
		t.Fatalf("merged = %+v", got)
	}
	if got := mergeHistory([]platformevents.Event{both, recent}, []platformevents.Event{old}, 2); len(got) != 2 || got[0].ID != both.ID {
		t.Fatalf("limit 2 keeps the newest two: %+v", got)
	}
	b, _ := json.Marshal(mergeHistory(nil, []platformevents.Event{old}, 0))
	if !strings.Contains(string(b), `"type":"io.adhar.batch.run.succeeded"`) || !strings.Contains(string(b), `"archived":true`) {
		t.Fatalf("-o json is the CloudEvent with archived:true: %s", b)
	}
	b, _ = json.Marshal(mergeHistory([]platformevents.Event{recent}, nil, 0))
	if strings.Contains(string(b), "archived") {
		t.Fatalf("a bus event carries no archived field: %s", b)
	}
}

func archiveFixture(t *testing.T) (*batcharchivetest.Store, platformevents.Event, platformevents.Event) {
	t.Helper()
	store := batcharchivetest.New("rustfsadmin", "s3cret", batcharchive.DefaultBucket)
	t.Cleanup(store.Close)
	old := batchEvent(t, platformevents.TypeBatchRunSucceeded, "2026-03-01T02:00:00Z", map[string]interface{}{
		"run": map[string]interface{}{"name": "importer-0000000a"}, "springBatch": map[string]interface{}{"businessDate": "2026-02-28"}})
	both := batchEvent(t, platformevents.TypeBatchRunFailed, "2026-11-01T02:00:00Z", map[string]interface{}{
		"run": map[string]interface{}{"name": "importer-aaaaaaaa"}})
	for _, e := range []struct {
		date, run string
		ev        platformevents.Event
	}{{"2026-02-28", "importer-0000000a", old}, {"2026-10-31", "importer-aaaaaaaa", both}} {
		key, _ := batcharchive.Key("prod", "importer", e.date, e.run)
		body, _ := json.Marshal(e.ev)
		at, _ := time.Parse(time.RFC3339, e.ev.Time)
		store.Put(batcharchive.DefaultBucket, key, body, at.Add(time.Second))
	}
	prev := historyArchiveURL
	historyArchiveURL = store.URL()
	t.Cleanup(func() { historyArchiveURL = prev })
	return store, old, both
}

// A4.24: `adhar batch history` reads the archive as a second source: the run
// the topic no longer holds is there, marked archived; the one both hold is
// shown once. Without the bus, the archive alone is shown, with a line saying
// so; an identity that may not read the credential gets the bus and a line.
func TestHistoryFallsBackToTheArchiveBeyondTheBus(t *testing.T) {
	prevPath := helpers.KubeConfigPath
	t.Cleanup(func() { helpers.KubeConfigPath = prevPath })
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("HOME", t.TempDir())
	_, old, both := archiveFixture(t)
	creds := map[string]string{"rootUser": "rustfsadmin", "rootPassword": "s3cret"}

	cluster := &clusterDouble{bus: true, events: []platformevents.Event{both}, rootCreds: creds}
	t.Setenv("KUBECONFIG", kubeconfigAt(t, cluster.serve(t).URL))
	out, err := executeBatch(t, "history", "importer", "-n", "prod", "-o", "json")
	var got []historyEvent
	if jerr := json.Unmarshal([]byte(out), &got); err != nil || jerr != nil {
		t.Fatalf("history: %v %v\n%s", err, jerr, out)
	}
	if len(got) != 2 || got[0].ID != old.ID || !got[0].Archived || got[1].ID != both.ID || got[1].Archived {
		t.Fatalf("history = %+v; want the archived run, then the bus's (once)", got)
	}

	noBus := &clusterDouble{rootCreds: creds}
	t.Setenv("KUBECONFIG", kubeconfigAt(t, noBus.serve(t).URL))
	out, err = executeBatch(t, "history", "importer", "-n", "prod")
	if err != nil || !strings.Contains(out, "archive alone") || !strings.Contains(out, "importer-0000000a") || !strings.Contains(out, "archive") {
		t.Fatalf("history without the bus: %v\n%s", err, out)
	}

	forbidden := &clusterDouble{bus: true, events: []platformevents.Event{both}, secretForbidden: true}
	t.Setenv("KUBECONFIG", kubeconfigAt(t, forbidden.serve(t).URL))
	out, err = executeBatch(t, "history", "importer", "-n", "prod")
	if err != nil || !strings.Contains(out, "archive was not consulted") || !strings.Contains(out, "root-creds") || !strings.Contains(out, "importer-aaaaaaaa") || strings.Contains(out, "importer-0000000a") {
		t.Fatalf("history for an identity that may not read the credential: %v\n%s", err, out)
	}

	forbidden.events = []platformevents.Event{both}
	t.Cleanup(func() { historyArchive = true })
	out, err = executeBatch(t, "history", "importer", "-n", "prod", "--archive=false", "-o", "json")
	got = nil
	_ = json.Unmarshal([]byte(out), &got)
	if err != nil || len(got) != 1 {
		t.Fatalf("--archive=false reads the bus alone: %v\n%s", err, out)
	}
	for _, call := range forbidden.requests[len(forbidden.requests)-5:] {
		if strings.Contains(call, "secrets") {
			t.Errorf("--archive=false still asked for the credential: %v", call)
		}
	}
}
