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
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"adhar-io/adhar/platform/events"
)

// The writer is advisory in exactly the sense the event bus is
// (platform/events/CONTRACT.md): Archive returns nothing, does not block, and
// a store that is down, slow, full or absent costs a counter — never a
// reconcile. A bounded queue that drops the OLDEST entry when full, and one
// worker, so a store outage cannot grow the manager's memory and writes land
// in the order the runs finished.

// EnvURL switches the archive on: the object store's base URL. Opt-in, like
// ADHAR_EVENTS_BRIDGE_URL, so a cluster without RustFS is silent about it.
const EnvURL = "ADHAR_BATCH_ARCHIVE_URL"

// EnvBucket overrides the bucket (default adhar-batch-archive).
const EnvBucket = "ADHAR_BATCH_ARCHIVE_BUCKET"

// EnvCredentialSecret overrides the Secret the credential is read from (in the
// manager's namespace; default root-creds, keys rootUser / rootPassword).
const EnvCredentialSecret = "ADHAR_BATCH_ARCHIVE_CREDENTIALS"

// Outcome is what happened to one archive entry, for the counter.
type Outcome string

const (
	Written Outcome = "written"
	Failed  Outcome = "failed"
	Dropped Outcome = "dropped"
)

type entry struct {
	key, environment string
	body             []byte
}

// Result is what happened to one archive entry. Environment is the run's
// (the event's `adharenvironment`), so a store refusing prod's runs can be
// told from one refusing dev's.
type Result struct {
	Key, Environment string
	Outcome          Outcome
	Err              error
}

// Writer archives run summaries in the background.
type Writer struct {
	client  *Client
	queue   chan entry
	done    chan struct{}
	once    sync.Once
	wg      sync.WaitGroup
	timeout time.Duration

	written, failed, dropped atomic.Int64
	// OnResult is called after each attempt (tests wait on it; the observer
	// counts with it).
	OnResult func(Result)
}

// WriterOptions configure a Writer.
type WriterOptions struct {
	QueueSize int
	// Timeout bounds one write, bucket creation included. Default 10s.
	Timeout time.Duration
}

// NewWriter starts a writer over c.
func NewWriter(c *Client, opts WriterOptions) *Writer {
	if opts.QueueSize <= 0 {
		opts.QueueSize = 256
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	w := &Writer{client: c, queue: make(chan entry, opts.QueueSize), done: make(chan struct{}), timeout: opts.Timeout}
	w.wg.Add(1)
	go w.run()
	return w
}

// FromEnv builds the writer's client from the manager's environment, or nil
// when the archive is not switched on. credentials reads the Secret it names.
func FromEnv(credentials func(ctx context.Context, secret string) (Credentials, error)) *Client {
	endpoint := strings.TrimRight(strings.TrimSpace(os.Getenv(EnvURL)), "/")
	if endpoint == "" {
		return nil
	}
	secret := strings.TrimSpace(os.Getenv(EnvCredentialSecret))
	if secret == "" {
		secret = CredentialSecret
	}
	return &Client{
		Endpoint: endpoint,
		Bucket:   strings.TrimSpace(os.Getenv(EnvBucket)),
		Credentials: func(ctx context.Context) (Credentials, error) {
			return credentials(ctx, secret)
		},
	}
}

// Archive queues a run's final event under key. It never blocks and never
// fails; an event that cannot be encoded is counted as failed.
func (w *Writer) Archive(_ context.Context, key string, e events.Event) {
	en := entry{key: key, environment: e.Environment}
	body, err := json.Marshal(e)
	if err != nil {
		w.result(en, Failed, err)
		return
	}
	en.body = body
	select {
	case w.queue <- en:
		return
	default:
	}
	// Full: make room by discarding the OLDEST, like the bus.
	select {
	case old := <-w.queue:
		w.result(old, Dropped, nil)
	default:
	}
	select {
	case w.queue <- en:
	default:
		w.result(en, Dropped, nil)
	}
}

// Stats is a snapshot of the counters.
type Stats struct{ Written, Failed, Dropped int64 }

// Stats reports what has happened so far.
func (w *Writer) Stats() Stats {
	return Stats{Written: w.written.Load(), Failed: w.failed.Load(), Dropped: w.dropped.Load()}
}

// Close drains what is queued, within ctx, and stops the worker.
func (w *Writer) Close(ctx context.Context) {
	w.once.Do(func() {
		close(w.done)
		stopped := make(chan struct{})
		go func() { w.wg.Wait(); close(stopped) }()
		select {
		case <-stopped:
		case <-ctx.Done():
		}
	})
}

func (w *Writer) run() {
	defer w.wg.Done()
	for {
		select {
		case en := <-w.queue:
			w.write(en)
		case <-w.done:
			for {
				select {
				case en := <-w.queue:
					w.write(en)
				default:
					return
				}
			}
		}
	}
}

func (w *Writer) write(en entry) {
	ctx, cancel := context.WithTimeout(context.Background(), w.timeout)
	defer cancel()
	err := w.client.Put(ctx, en.key, en.body)
	// A missing bucket is made once and the write retried: the archive may be
	// switched on before the rustfs package's bucket Job has run again.
	if errors.Is(err, ErrNoSuchBucket) {
		if err = w.client.MakeBucket(ctx); err == nil {
			err = w.client.Put(ctx, en.key, en.body)
		}
	}
	if err != nil {
		w.result(en, Failed, err)
		return
	}
	w.result(en, Written, nil)
}

func (w *Writer) result(en entry, o Outcome, err error) {
	switch o {
	case Written:
		w.written.Add(1)
	case Failed:
		w.failed.Add(1)
	case Dropped:
		w.dropped.Add(1)
	}
	if w.OnResult != nil {
		w.OnResult(Result{Key: en.key, Environment: en.environment, Outcome: o, Err: err})
	}
}
