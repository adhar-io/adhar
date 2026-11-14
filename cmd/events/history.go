/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package events

import (
	"context"
	"fmt"
	"time"

	platformevents "adhar-io/adhar/platform/events"
)

// HistoryQuery is what another command asks of the retained event stream:
// `adhar batch history` and `adhar batch dlq` read adhar.batch the way
// `adhar events list` does — through the bridge, from the oldest retained
// record, filtered here because Kafka has no query language.
type HistoryQuery struct {
	// Bridge is a bridge base URL the caller already reaches; empty reaches
	// the platform's own through the API server's service proxy.
	Bridge string
	// Topics to read; at least one.
	Topics []string
	// Since drops events older than this instant; zero keeps everything retained.
	Since time.Time
	// Keep is the caller's filter; nil keeps every event.
	Keep func(platformevents.Event) bool
	// Limit keeps only the NEWEST Limit matches (0: all of them). The stream is
	// read oldest first, so the limit cannot stop the read early: the newest
	// events are the last ones a partition yields.
	Limit int
}

// historyPollTimeout is one poll's long-poll budget; tests shorten it.
var historyPollTimeout = 2 * time.Second

// ReadHistory returns the matching events, oldest first, and ends when the
// stream goes quiet (two empty polls in a row, as `list` decides). Records that
// are not CloudEvents are returned as problems rather than dropped: the topics
// are the platform's own, so a foreign record is worth saying.
func ReadHistory(ctx context.Context, q HistoryQuery) ([]platformevents.Event, []error, error) {
	if len(q.Topics) == 0 {
		return nil, nil, fmt.Errorf("no topics to read")
	}
	base := q.Bridge
	if base == "" {
		var cleanup func()
		var err error
		base, cleanup, err = portForwardBridge(ctx)
		if err != nil {
			return nil, nil, err
		}
		defer cleanup()
	}
	client := newBridgeClient(base)
	// The consumer is deleted on every exit path: one left behind is a group
	// member that never leaves.
	defer client.close()
	if err := client.open(ctx, "adhar-cli", q.Topics, true); err != nil {
		return nil, nil, err
	}
	var out []platformevents.Event
	var problems []error
	empties := 0
	for empties < 2 {
		if ctx.Err() != nil {
			return out, problems, ctx.Err()
		}
		batch, bad, err := client.poll(ctx, historyPollTimeout)
		if err != nil {
			return out, problems, err
		}
		problems = append(problems, bad...)
		if len(batch) == 0 {
			empties++
			continue
		}
		empties = 0
		for _, e := range batch {
			if !q.Since.IsZero() {
				// An event whose time cannot be read is KEPT, as `list` keeps it:
				// a filter that hides malformed events hides the ones worth seeing.
				if ts, err := time.Parse(time.RFC3339Nano, e.Time); err == nil && ts.Before(q.Since) {
					continue
				}
			}
			if q.Keep != nil && !q.Keep(e) {
				continue
			}
			out = append(out, e)
			if q.Limit > 0 && len(out) > q.Limit {
				out = out[len(out)-q.Limit:]
			}
		}
	}
	return out, problems, nil
}
