/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batcharchive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"adhar-io/adhar/platform/events"
)

// maxListed bounds one job's listing: over ten years of a job that runs ten
// times a day. A listing that reaches it is reported as a problem, because the
// store lists in key order and what is cut is the latest-dated keys.
const maxListed = 50_000

// ReadJob reads one BatchJob's archived runs: the newest `limit` (0: all) whose
// event time is not before `since` (zero: no bound), oldest first. Objects that
// are not an `adhar.batch` event for this job are skipped and reported in
// problems, never fatal — one bad object must not hide the rest of a history.
func ReadJob(ctx context.Context, c *Client, namespace, batchJob string, since time.Time, limit int) ([]events.Event, []error, error) {
	objs, err := c.List(ctx, JobPrefix(namespace, batchJob), maxListed)
	if err != nil {
		if errors.Is(err, ErrNoSuchBucket) {
			// Nothing was ever archived on this cluster.
			return nil, nil, nil
		}
		return nil, nil, err
	}
	var problems []error
	if len(objs) >= maxListed {
		problems = append(problems, fmt.Errorf("the archive holds at least %d runs of %s/%s; only the first %d in key order were considered", maxListed, namespace, batchJob, maxListed))
	}
	// Ordered and bounded by WHEN each object was written, never by the
	// business date in its key: a backfill writes an old date today, and a
	// run for tomorrow's date may finish tonight. An object written before
	// `since` holds an event that ended before it (the object is written after
	// the run ended), so it is skipped unread; one with no LastModified is read.
	sort.SliceStable(objs, func(i, j int) bool { return objs[i].LastModified.After(objs[j].LastModified) })
	keys := make([]string, 0, len(objs))
	for _, o := range objs {
		if !since.IsZero() && !o.LastModified.IsZero() && o.LastModified.Before(since) {
			continue
		}
		keys = append(keys, o.Key)
	}

	subject := namespace + "/" + batchJob
	var (
		mu  sync.Mutex
		out []events.Event
	)
	fetch := func(k string) {
		raw, err := c.Get(ctx, k)
		var e events.Event
		if err == nil {
			err = json.Unmarshal(raw, &e)
		}
		if err == nil && (e.Subject != subject || len(e.Type) == 0) {
			err = errors.New("not an adhar.batch event of this job")
		}
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			problems = append(problems, fmt.Errorf("archive object %s: %w", k, err))
			return
		}
		if !since.IsZero() {
			if t, perr := time.Parse(time.RFC3339Nano, e.Time); perr == nil && t.Before(since) {
				return
			}
		}
		out = append(out, e)
	}
	// Fetch in rounds of a few at a time until `limit` events are kept: keys
	// the since-window drops do not count against it.
	const parallel = 8
	for i := 0; i < len(keys); i += parallel {
		if limit > 0 && len(out) >= limit {
			break
		}
		var wg sync.WaitGroup
		for _, k := range keys[i:min(i+parallel, len(keys))] {
			wg.Add(1)
			go func(k string) { defer wg.Done(); fetch(k) }(k)
		}
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return nil, problems, err
		}
	}
	SortByTime(out)
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, problems, nil
}

// SortByTime orders events oldest first; an unreadable time sorts first.
func SortByTime(evs []events.Event) {
	at := func(e events.Event) time.Time {
		t, _ := time.Parse(time.RFC3339Nano, e.Time)
		return t
	}
	sort.SliceStable(evs, func(i, j int) bool { return at(evs[i]).Before(at(evs[j])) })
}
