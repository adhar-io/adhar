/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batcharchive_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"adhar-io/adhar/platform/batcharchive"
	"adhar-io/adhar/platform/batcharchive/batcharchivetest"
	"adhar-io/adhar/platform/events"
)

func client(s *batcharchivetest.Store, ak, sk string) *batcharchive.Client {
	return &batcharchive.Client{
		Endpoint: s.URL(),
		Credentials: func(context.Context) (batcharchive.Credentials, error) {
			return batcharchive.Credentials{AccessKey: ak, SecretKey: sk}, nil
		},
	}
}

func event(subject, typ, id string, at time.Time) events.Event {
	return events.Event{
		SpecVersion: events.SpecVersion, ID: id, Source: "/adhar/test/batch-observer", Type: typ,
		Subject: subject, Time: events.Timestamp(at), DataContentType: "application/json",
		Data: json.RawMessage(`{"batchJob":"importer"}`),
	}
}

// The key is computed from the run's identity, and nothing a tenant writes
// can add a path segment to it: the business date comes from run parameters.
func TestTheArchiveKeyIsTheRunsIdentity(t *testing.T) {
	k, err := batcharchive.Key("team-a-prod", "importer", "2026-11-19", "importer-1a2b3c4d")
	if err != nil || k != "team-a-prod/importer/2026-11-19/importer-1a2b3c4d.json" {
		t.Fatalf("Key = %q, %v", k, err)
	}
	for _, date := range []string{"../../etc", "2026/11/19", "", "20261119", "2026-11-19/x"} {
		k, err := batcharchive.Key("ns", "job", date, "run")
		if err != nil || k != "ns/job/undated/run.json" {
			t.Errorf("date %q: Key = %q, %v; want the undated segment", date, k, err)
		}
	}
	for _, bad := range [][3]string{{"..", "job", "run"}, {"ns", "a/b", "run"}, {"ns", "job", "../x"}, {"", "job", "run"}, {"ns", "job", "a..b"}} {
		if k, err := batcharchive.Key(bad[0], bad[1], "2026-01-01", bad[2]); err == nil {
			t.Errorf("Key(%q, %q, …, %q) = %q; want refused", bad[0], bad[1], bad[2], k)
		}
	}
}

// Every call is SigV4-signed with the payload hash RustFS requires; the fake
// re-signs each request and refuses a mismatch.
func TestTheClientSignsWhatTheStoreVerifies(t *testing.T) {
	s := batcharchivetest.New("ak", "sk", batcharchive.DefaultBucket)
	defer s.Close()
	c := client(s, "ak", "sk")
	ctx := context.Background()
	if err := c.Put(ctx, "ns/job/2026-11-19/run.json", []byte(`{"a":1}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := c.Get(ctx, "ns/job/2026-11-19/run.json")
	if err != nil || string(got) != `{"a":1}` {
		t.Fatalf("Get = %q, %v", got, err)
	}
	objs, err := c.List(ctx, "ns/job/", 0)
	if err != nil || len(objs) != 1 || objs[0].Key != "ns/job/2026-11-19/run.json" || objs[0].LastModified.IsZero() {
		t.Fatalf("List = %+v, %v", objs, err)
	}
	if _, err := c.Get(ctx, "ns/job/2026-11-19/absent.json"); err != batcharchive.ErrNotFound {
		t.Fatalf("Get absent = %v; want ErrNotFound", err)
	}

	wrong := client(s, "ak", "not-the-key")
	if err := wrong.Put(ctx, "ns/job/x.json", []byte("{}")); err == nil || !strings.Contains(err.Error(), "SignatureDoesNotMatch") {
		t.Fatalf("Put with the wrong secret = %v; want SignatureDoesNotMatch", err)
	}
}

func TestListingFollowsContinuationTokens(t *testing.T) {
	s := batcharchivetest.New("ak", "sk", batcharchive.DefaultBucket)
	defer s.Close()
	s.PageSize = 3
	for i := range 10 {
		s.Put(batcharchive.DefaultBucket, fmt.Sprintf("ns/job/2026-11-%02d/run-%d.json", i+1, i), []byte("{}"), time.Now())
	}
	s.Put(batcharchive.DefaultBucket, "ns/job-eu/2026-11-01/other.json", []byte("{}"), time.Now())
	objs, err := client(s, "ak", "sk").List(context.Background(), batcharchive.JobPrefix("ns", "job"), 0)
	if err != nil || len(objs) != 10 {
		t.Fatalf("List across pages = %d objects, %v; want 10 (and not job-eu's)", len(objs), err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A re-observed run writes the SAME key, and a missing bucket is made once
// and the write retried.
func TestTheWriterIsIdempotentAndMakesTheBucket(t *testing.T) {
	s := batcharchivetest.New("ak", "sk") // no bucket yet
	defer s.Close()
	w := batcharchive.NewWriter(client(s, "ak", "sk"), batcharchive.WriterOptions{})
	defer w.Close(context.Background())
	key, _ := batcharchive.Key("ns", "importer", "2026-11-19", "importer-1a2b3c4d")
	e := event("ns/importer", events.TypeBatchRunFailed, "id-1", time.Now())
	w.Archive(context.Background(), key, e)
	e.Type, e.ID = events.TypeBatchRunSucceeded, "id-2"
	w.Archive(context.Background(), key, e)
	waitFor(t, func() bool { return w.Stats().Written == 2 })
	if !s.HasBucket(batcharchive.DefaultBucket) {
		t.Fatal("the writer did not make the missing bucket")
	}
	if keys := s.Keys(batcharchive.DefaultBucket); len(keys) != 1 || keys[0] != key {
		t.Fatalf("keys = %v; want the one run key", keys)
	}
	var got events.Event
	_ = json.Unmarshal(s.Object(batcharchive.DefaultBucket, key), &got)
	if got.Type != events.TypeBatchRunSucceeded || got.ID != "id-2" {
		t.Fatalf("archived %s/%s; want the last word on the run", got.Type, got.ID)
	}
}

// The store being down costs a counter, never a blocked caller; a full queue
// drops the OLDEST entry.
func TestTheWriterNeverBlocksAndCountsFailures(t *testing.T) {
	s := batcharchivetest.New("ak", "sk", batcharchive.DefaultBucket)
	defer s.Close()
	s.Fail = http.StatusServiceUnavailable
	w := batcharchive.NewWriter(client(s, "ak", "sk"), batcharchive.WriterOptions{QueueSize: 2, Timeout: time.Second})
	var mu sync.Mutex
	var dropped []string
	w.OnResult = func(r batcharchive.Result) {
		if r.Outcome == batcharchive.Dropped {
			mu.Lock()
			dropped = append(dropped, r.Key)
			mu.Unlock()
		}
	}
	start := time.Now()
	for i := range 50 {
		w.Archive(context.Background(), fmt.Sprintf("ns/job/undated/r%d.json", i), event("ns/job", events.TypeBatchRunFailed, "x", time.Now()))
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("50 Archive calls took %s against a failing store; the caller must never wait", d)
	}
	w.Close(context.Background())
	st := w.Stats()
	if st.Written != 0 || st.Failed == 0 || st.Failed+st.Dropped != 50 {
		t.Fatalf("stats = %+v; want every entry failed or dropped, and counted", st)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, k := range dropped {
		if k == "ns/job/undated/r49.json" {
			t.Fatal("the newest entry was dropped; a full queue drops the oldest")
		}
	}
}

func TestReadJobKeepsTheNewestWrittenNotTheNewestDated(t *testing.T) {
	s := batcharchivetest.New("ak", "sk", batcharchive.DefaultBucket)
	defer s.Close()
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	put := func(date, run string, at time.Time) {
		key, _ := batcharchive.Key("ns", "job", date, run)
		body, _ := json.Marshal(event("ns/job", events.TypeBatchRunSucceeded, run, at))
		s.Put(batcharchive.DefaultBucket, key, body, at.Add(time.Second))
	}
	// Twelve days of runs — more than one round of fetches — then a backfill
	// of an OLD date, finished last: the newest run of all.
	for d := 1; d <= 12; d++ {
		put(fmt.Sprintf("2026-11-%02d", d), fmt.Sprintf("r%02d", d), base.AddDate(0, 0, d-1))
	}
	put("2026-01-15", "backfill", base.AddDate(0, 0, 12))
	s.Put(batcharchive.DefaultBucket, "ns/job/2026-11-30/garbage.json", []byte("not json"), base.AddDate(0, 0, 13))
	c := client(s, "ak", "sk")

	evs, problems, err := batcharchive.ReadJob(context.Background(), c, "ns", "job", time.Time{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(evs); got != "r12,backfill" {
		t.Fatalf("limit 2 = %s; want r12,backfill oldest first", got)
	}
	if len(problems) != 1 || !strings.Contains(problems[0].Error(), "garbage.json") {
		t.Fatalf("problems = %v; want the one unreadable object named", problems)
	}

	evs, _, _ = batcharchive.ReadJob(context.Background(), c, "ns", "job", base.AddDate(0, 0, 10), 0)
	if got := ids(evs); got != "r11,r12,backfill" {
		t.Fatalf("since day 11 = %s; want r11,r12,backfill (the backfill's old date must not hide it)", got)
	}

	other := batcharchivetest.New("ak", "sk")
	defer other.Close()
	evs, problems, err = batcharchive.ReadJob(context.Background(), client(other, "ak", "sk"), "ns", "job", time.Time{}, 0)
	if err != nil || len(evs) != 0 || len(problems) != 0 {
		t.Fatalf("no bucket = %v, %v, %v; want an empty archive, not an error", evs, problems, err)
	}
}

func ids(evs []events.Event) string {
	var s []string
	for _, e := range evs {
		s = append(s, e.ID)
	}
	return strings.Join(s, ",")
}
