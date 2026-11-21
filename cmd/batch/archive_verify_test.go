/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"encoding/json"
	"testing"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/batcharchive"
	"adhar-io/adhar/platform/batcharchive/batcharchivetest"
	platformevents "adhar-io/adhar/platform/events"
)

// A4.24 past the topic's retention: the bus has nothing older than 90 days,
// so `--since 365d` is answered by the archive alone, and the window is the
// asked-for one — a run inside the year is shown, marked archived; a run from
// before it is not, and neither is anything when the window is the bus's own.
func TestHistorySinceAYearReadsTheArchiveWindowNotTheBus(t *testing.T) {
	fixedClock(t) // 2026-11-03
	prevPath := helpers.KubeConfigPath
	t.Cleanup(func() { helpers.KubeConfigPath = prevPath })
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("HOME", t.TempDir())
	stubHistory(t, nil, nil) // the bus holds nothing this old

	store := batcharchivetest.New("rustfsadmin", "s3cret", batcharchive.DefaultBucket)
	t.Cleanup(store.Close)
	inYear := batchEvent(t, platformevents.TypeBatchRunSucceeded, "2026-03-01T02:00:00Z", map[string]interface{}{
		"run": map[string]interface{}{"name": "importer-0000000a"}, "springBatch": map[string]interface{}{"businessDate": "2026-02-28"}})
	lastYear := batchEvent(t, platformevents.TypeBatchRunFailed, "2025-06-01T02:00:00Z", map[string]interface{}{
		"run": map[string]interface{}{"name": "importer-0000000b"}, "springBatch": map[string]interface{}{"businessDate": "2025-05-31"}})
	for _, e := range []struct {
		date, run string
		ev        platformevents.Event
	}{{"2026-02-28", "importer-0000000a", inYear}, {"2025-05-31", "importer-0000000b", lastYear}} {
		key, _ := batcharchive.Key("prod", "importer", e.date, e.run)
		body, _ := json.Marshal(e.ev)
		at, _ := time.Parse(time.RFC3339, e.ev.Time)
		store.Put(batcharchive.DefaultBucket, key, body, at.Add(time.Second))
	}
	prev := historyArchiveURL
	historyArchiveURL = store.URL()
	t.Cleanup(func() { historyArchiveURL = prev })

	cluster := &clusterDouble{rootCreds: map[string]string{"rootUser": "rustfsadmin", "rootPassword": "s3cret"}}
	t.Setenv("KUBECONFIG", kubeconfigAt(t, cluster.serve(t).URL))

	read := func(since string) []historyEvent {
		t.Helper()
		out, err := executeBatch(t, "history", "importer", "-n", "prod", "--since", since, "-o", "json")
		var got []historyEvent
		if jerr := json.Unmarshal([]byte(out), &got); err != nil || jerr != nil {
			t.Fatalf("history --since %s: %v %v\n%s", since, err, jerr, out)
		}
		return got
	}
	if got := read("365d"); len(got) != 1 || got[0].ID != inYear.ID || !got[0].Archived {
		t.Fatalf("--since 365d = %+v; want the archived run from March, and not the one from 2025", got)
	}
	if got := read("90d"); len(got) != 0 {
		t.Fatalf("--since 90d = %+v; want nothing: both archived runs ended before the window", got)
	}
}
