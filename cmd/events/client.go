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
	"strings"
	"time"

	platformevents "adhar-io/adhar/platform/events"
)

// bridgeClient reads the event stream through the Strimzi KafkaBridge.
//
// The bridge's consumer API is stateful — create a consumer, subscribe, then
// poll — and each consumer is a real Kafka consumer group member on the broker.
// Leaving one behind means a group member that never leaves and a rebalance
// every session timeout, so the client ALWAYS deletes its consumer, including
// on Ctrl-C (see close()).
type bridgeClient struct {
	base     string
	http     *http.Client
	consumer string // the consumer's own base URI, returned by the bridge
	name     string
}

// consumerTTL is how long the bridge keeps an idle consumer. Deliberately
// short: a CLI session that is killed without cleanup should stop being a
// member of the group in a minute, not at the broker's default.
const consumerTTL = 60 * time.Second

func newBridgeClient(base string) *bridgeClient {
	// Through the API server proxy the request must carry the kubeconfig's
	// credentials and trust its CA, so the client built there is reused. A
	// plain http.Client works only for a --bridge URL the user reached
	// themselves (a port-forward they are already running).
	c := sharedTransport
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	return &bridgeClient{base: strings.TrimRight(base, "/"), http: c}
}

type createConsumerRequest struct {
	Name                     string `json:"name"`
	Format                   string `json:"format"`
	AutoOffsetReset          string `json:"auto.offset.reset"`
	EnableAutoCommit         bool   `json:"enable.auto.commit"`
	ConsumerRequestTimeoutMs int    `json:"consumer.request.timeout.ms"`
}

type createConsumerResponse struct {
	InstanceID string `json:"instance_id"`
	BaseURI    string `json:"base_uri"`
}

// open creates a consumer and subscribes it to the given topics.
//
// `fromBeginning` chooses between reading the history and following only what
// arrives next. Both are wanted: "what happened" needs the history, "what is
// happening" does not want to replay two weeks of it first.
func (c *bridgeClient) open(ctx context.Context, group string, topics []string, fromBeginning bool) error {
	offset := "latest"
	if fromBeginning {
		offset = "earliest"
	}
	c.name = fmt.Sprintf("%s-%d", group, time.Now().UnixNano())
	body, err := json.Marshal(createConsumerRequest{
		Name:   c.name,
		Format: "json",
		// The CLI is a reader, never a committer: committing offsets would make
		// one person's `adhar events tail` move the group's position and change
		// what the NEXT reader sees.
		AutoOffsetReset:          offset,
		EnableAutoCommit:         false,
		ConsumerRequestTimeoutMs: int(consumerTTL / time.Millisecond),
	})
	if err != nil {
		return err
	}
	var created createConsumerResponse
	if err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("%s/consumers/%s", c.base, group), body, &created); err != nil {
		return fmt.Errorf("creating a bridge consumer: %w", err)
	}
	// The bridge returns the URI to use for this consumer. It must be used as
	// given — it encodes the group and instance, and rebuilding it by hand is
	// how a poll ends up against a consumer that does not exist.
	c.consumer = strings.TrimRight(created.BaseURI, "/")
	if c.consumer == "" {
		return fmt.Errorf("the bridge created a consumer with no base URI")
	}
	sub, err := json.Marshal(map[string][]string{"topics": topics})
	if err != nil {
		return err
	}
	if err := c.do(ctx, http.MethodPost, c.consumer+"/subscription", sub, nil); err != nil {
		return fmt.Errorf("subscribing to %s: %w", strings.Join(topics, ", "), err)
	}
	return nil
}

// record is one message as the bridge reports it.
type record struct {
	Topic     string          `json:"topic"`
	Partition int             `json:"partition"`
	Offset    int64           `json:"offset"`
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
}

// poll returns the events available now, decoded from their CloudEvents
// envelopes. A record whose value is not a CloudEvent is REPORTED rather than
// skipped: the topics are the platform's own, so a foreign message on one is
// something an operator should know about.
func (c *bridgeClient) poll(ctx context.Context, timeout time.Duration) ([]platformevents.Event, []error, error) {
	var recs []record
	url := fmt.Sprintf("%s/records?timeout=%d", c.consumer, timeout.Milliseconds())
	if err := c.do(ctx, http.MethodGet, url, nil, &recs); err != nil {
		return nil, nil, err
	}
	var out []platformevents.Event
	var problems []error
	for _, r := range recs {
		var e platformevents.Event
		if err := json.Unmarshal(r.Value, &e); err != nil {
			problems = append(problems, fmt.Errorf("%s/%d offset %d is not a CloudEvent: %w", r.Topic, r.Partition, r.Offset, err))
			continue
		}
		if e.SpecVersion == "" {
			problems = append(problems, fmt.Errorf("%s/%d offset %d carries no specversion", r.Topic, r.Partition, r.Offset))
			continue
		}
		out = append(out, e)
	}
	return out, problems, nil
}

// close deletes the consumer. Best effort and never reported: by the time this
// runs the user has what they asked for, and an error here would replace their
// output with a complaint about cleanup.
func (c *bridgeClient) close() {
	if c.consumer == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.do(ctx, http.MethodDelete, c.consumer, nil, nil)
	c.consumer = ""
}

func (c *bridgeClient) do(ctx context.Context, method, url string, body []byte, out any) error {
	var rdr *bytes.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/vnd.kafka.v2+json")
	}
	req.Header.Set("Accept", "application/vnd.kafka.json.v2+json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return fmt.Errorf("bridge %s %s: %s: %s", method, url, resp.Status, strings.TrimSpace(buf.String()))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
