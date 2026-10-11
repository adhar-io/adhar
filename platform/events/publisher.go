/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Publisher hands CloudEvents to the Kafka bridge without ever making the
// caller wait for Kafka.
//
// The shape is a bounded queue plus one worker, and every part of that is a
// decision about the caller rather than about throughput:
//
//   - BOUNDED: a reconcile loop can emit faster than a broker accepts for as
//     long as the broker is unhealthy. An unbounded queue turns a broker outage
//     into the controller's memory growing until it is OOM-killed, so the queue
//     has a ceiling and drops the OLDEST event when it is reached — newer
//     events describe the current state, which is what a consumer catching up
//     actually wants.
//   - ONE worker: events from one component are published in the order they
//     happened. Several workers would race and deliver `node.removed` before
//     `node.added`, and a consumer cannot reorder what it cannot timestamp
//     finely enough.
//   - NEVER an error to the caller: Publish returns nothing. There is no
//     failure mode a reconcile should react to, and an `err` that every call
//     site must remember to ignore is an invitation to handle it wrongly once.
type Publisher struct {
	// BridgeURL is the Strimzi KafkaBridge base URL, e.g.
	// http://adhar-events-bridge-service.adhar-system.svc.cluster.local:8080.
	// Empty disables publishing entirely.
	bridgeURL string
	source    string
	client    *http.Client

	queue chan Event
	once  sync.Once
	done  chan struct{}
	wg    sync.WaitGroup

	// Counters, read by tests and by the metrics exposition. Atomic because the
	// worker writes them while callers read.
	queued    atomic.Int64
	published atomic.Int64
	dropped   atomic.Int64
	failed    atomic.Int64

	// onResult is a test seam: called after each attempt with its outcome, so a
	// test can wait for delivery instead of sleeping.
	onResult func(Event, error)
}

// Options configure a Publisher. Every field has a working default, so a caller
// that only knows the bridge URL gets sane behaviour.
type Options struct {
	// BridgeURL is the KafkaBridge base URL. Empty means "no bus": every
	// Publish is counted as dropped and nothing is sent. That is the normal
	// state on a local `adhar up`, where Kafka is not enabled.
	BridgeURL string
	// Source identifies the emitter, e.g. "/adhar/prod/adharplatform-controller".
	// It becomes the CloudEvents `source` of every event this publisher sends.
	Source string
	// QueueSize is the ceiling on undelivered events. Default 256.
	QueueSize int
	// Timeout bounds ONE delivery attempt. Default 5s — long enough for a
	// healthy bridge, short enough that a hung one cannot hold the worker for a
	// whole reconcile period.
	Timeout time.Duration
	// Client is the HTTP client, injected by tests.
	Client *http.Client
}

// NewPublisher builds a Publisher and starts its worker.
//
// It never returns an error: a misconfigured or absent bus must leave the
// platform working, so the failure mode is "events are dropped", not
// "the controller does not start".
func NewPublisher(opts Options) *Publisher {
	if opts.QueueSize <= 0 {
		opts.QueueSize = 256
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Second
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: opts.Timeout}
	}
	p := &Publisher{
		bridgeURL: opts.BridgeURL,
		source:    opts.Source,
		client:    client,
		queue:     make(chan Event, opts.QueueSize),
		done:      make(chan struct{}),
	}
	p.wg.Add(1)
	go p.run()
	return p
}

// Enabled reports whether a bus is configured. Call sites do not need this —
// Publish is safe either way — but a startup log line that says which it is
// saves an operator wondering where their events went.
func (p *Publisher) Enabled() bool { return p.bridgeURL != "" }

// Publish queues an event. It does not block, does not fail, and does not wait
// for Kafka.
//
// The caller supplies the type, subject, data and hierarchy; `id`, `source`,
// `time` and `specversion` are filled in here so no call site can forget one
// and no two call sites can disagree about the format.
func (p *Publisher) Publish(ctx context.Context, e Event) {
	if e.SpecVersion == "" {
		e.SpecVersion = SpecVersion
	}
	if e.Source == "" {
		e.Source = p.source
	}
	if e.Time == "" {
		e.Time = Timestamp(Now())
	}
	if e.ID == "" {
		e.ID = newID()
	}
	if e.Correlation == "" {
		e.Correlation = CorrelationFrom(ctx)
	}
	if !p.Enabled() {
		p.dropped.Add(1)
		return
	}
	if err := e.Validate(); err != nil {
		// A malformed event is a programming error in the CALL SITE, not a
		// runtime condition, and it must not be sent: a strict consumer drops
		// the batch it arrives in, costing unrelated events.
		p.failed.Add(1)
		if p.onResult != nil {
			p.onResult(e, err)
		}
		return
	}
	select {
	case p.queue <- e:
		p.queued.Add(1)
	default:
		// Full. Make room by discarding the OLDEST, then take the slot. If
		// another goroutine wins the race, drop this one rather than spin.
		select {
		case <-p.queue:
			p.dropped.Add(1)
		default:
		}
		select {
		case p.queue <- e:
			p.queued.Add(1)
		default:
			p.dropped.Add(1)
		}
	}
}

// Stats is a snapshot of the publisher's counters.
type Stats struct {
	Queued    int64
	Published int64
	Dropped   int64
	Failed    int64
}

// Stats reports what has happened to the events handed over so far. Dropped and
// Failed are the ones worth alerting on: a bus that is quietly discarding
// everything looks exactly like a quiet platform.
func (p *Publisher) Stats() Stats {
	return Stats{
		Queued:    p.queued.Load(),
		Published: p.published.Load(),
		Dropped:   p.dropped.Load(),
		Failed:    p.failed.Load(),
	}
}

// Close drains what is already queued, within the deadline, and stops the
// worker. Called on controller shutdown so the last events of a run — which are
// usually the interesting ones — are not the ones lost.
func (p *Publisher) Close(ctx context.Context) {
	p.once.Do(func() {
		close(p.done)
		stopped := make(chan struct{})
		go func() { p.wg.Wait(); close(stopped) }()
		select {
		case <-stopped:
		case <-ctx.Done():
		}
	})
}

func (p *Publisher) run() {
	defer p.wg.Done()
	for {
		select {
		case e := <-p.queue:
			p.deliver(e)
		case <-p.done:
			// Drain whatever is already queued, then stop. Anything still
			// arriving after Close is dropped by design.
			for {
				select {
				case e := <-p.queue:
					p.deliver(e)
				default:
					return
				}
			}
		}
	}
}

// bridgeRecords is the Strimzi KafkaBridge request body. The CloudEvent is the
// record VALUE — the structured content mode of the CloudEvents Kafka binding —
// so a consumer reads one JSON document and needs no header support.
type bridgeRecords struct {
	Records []bridgeRecord `json:"records"`
}

type bridgeRecord struct {
	// Key is the event's SUBJECT, so every event about one object lands on one
	// partition and is therefore ordered with respect to the others about it.
	// Without a key the broker round-robins and `degraded` can overtake
	// `synced` for the same package.
	Key   string `json:"key,omitempty"`
	Value Event  `json:"value"`
}

func (p *Publisher) deliver(e Event) {
	topic, ok := TopicFor(e.Type)
	if !ok {
		p.failed.Add(1)
		if p.onResult != nil {
			p.onResult(e, fmt.Errorf("no topic for event type %q", e.Type))
		}
		return
	}
	body, err := json.Marshal(bridgeRecords{Records: []bridgeRecord{{Key: e.Subject, Value: e}}})
	if err != nil {
		p.failed.Add(1)
		if p.onResult != nil {
			p.onResult(e, err)
		}
		return
	}
	url := fmt.Sprintf("%s/topics/%s", p.bridgeURL, topic)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		p.failed.Add(1)
		if p.onResult != nil {
			p.onResult(e, err)
		}
		return
	}
	// The bridge's own media type, which is what it content-negotiates on; the
	// CloudEvent inside is identified by the record value's shape.
	req.Header.Set("Content-Type", "application/vnd.kafka.json.v2+json")
	req.Header.Set("Accept", "application/vnd.kafka.v2+json")

	resp, err := p.client.Do(req)
	if err != nil {
		p.failed.Add(1)
		if p.onResult != nil {
			p.onResult(e, err)
		}
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		p.failed.Add(1)
		if p.onResult != nil {
			p.onResult(e, fmt.Errorf("bridge returned %s", resp.Status))
		}
		return
	}
	p.published.Add(1)
	if p.onResult != nil {
		p.onResult(e, nil)
	}
}
