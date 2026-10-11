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

// Package events is the platform's event bus: every notable thing the platform
// does, published once, as a CloudEvent, onto Kafka.
//
// # Why a bus at all
//
// Before this, each component told its own story in its own way — the
// controller through Kubernetes Events and logs, the console by polling the
// API, adhar-ai by asking Argo CD. Nothing could answer "what happened on this
// platform in the last hour, in order" without reconstructing it from five
// places, and nothing could REACT to a change without polling for it.
//
// # Why CloudEvents
//
// The platform is not one language. The controllers are Go, the console is
// Deno, the agent runtime is Python, and the paved-road CI is shell inside
// Tekton. CloudEvents (spec 1.0) is the one envelope all of them can produce
// and consume without sharing a library, and consumers outside Adhar — Knative,
// KEDA, Argo Events, n8n — already speak it.
//
// # Why through an HTTP bridge rather than a Kafka client
//
// Events reach Kafka through the Strimzi KafkaBridge the kafka-operator package
// runs (`POST /topics/<topic>`), in CloudEvents STRUCTURED mode: the whole
// event is the record value. That choice is about the languages above — a Kafka
// client would have to be added, configured and kept current in Go, Deno,
// Python and shell, while every one of them can already POST JSON. It also
// keeps broker credentials and TLS in exactly one deployment.
//
// # The rule that matters most
//
// Publishing is ADVISORY and must never change what the platform does. An
// emit cannot fail a reconcile, cannot block it, and cannot slow it down: it
// hands the event to a bounded queue and returns. If the bridge is down, the
// queue is full, or no bus is configured at all, the event is dropped and
// counted, and the caller never learns the difference. A platform that will not
// converge because its audit log is unavailable is a worse platform than one
// with no audit log.
package events

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SpecVersion is the CloudEvents specification this platform emits.
const SpecVersion = "1.0"

// ContentTypeStructured is the media type of a CloudEvent carried whole in a
// message body — the structured content mode of the CloudEvents Kafka binding.
const ContentTypeStructured = "application/cloudevents+json"

// Event is a CloudEvents 1.0 event.
//
// The Adhar extension attributes are flattened into the JSON object alongside
// the spec's own, which is what the spec requires: extensions are top-level
// attributes, not a nested map. Their names are lower-case alphanumeric because
// the spec restricts extension names to exactly that — `adhar-team` or
// `adhar_team` would be an invalid attribute name, and a consumer validating
// the envelope would reject the whole event.
type Event struct {
	SpecVersion     string          `json:"specversion"`
	ID              string          `json:"id"`
	Source          string          `json:"source"`
	Type            string          `json:"type"`
	Subject         string          `json:"subject,omitempty"`
	Time            string          `json:"time"`
	DataContentType string          `json:"datacontenttype,omitempty"`
	Data            json.RawMessage `json:"data,omitempty"`

	// Adhar extensions — the hierarchy an event belongs to (ADR-0026), so a
	// consumer can filter to one team's events without parsing `data`.
	Organisation string `json:"adharorganisation,omitempty"`
	Team         string `json:"adharteam,omitempty"`
	Project      string `json:"adharproject,omitempty"`
	Environment  string `json:"adharenvironment,omitempty"`
	Plane        string `json:"adharplane,omitempty"`

	// Correlation across components: the reconcile, request or run that caused
	// this event. Not a CloudEvents attribute by name, but an extension, so it
	// survives a round trip through any conformant consumer.
	Correlation string `json:"adharcorrelationid,omitempty"`
}

// Validate reports whether the event carries what the specification REQUIRES.
//
// Checked before publishing rather than trusted: an event missing `id` or
// `source` is not a CloudEvent, and a consumer that validates strictly drops
// the whole batch it arrived in — so one malformed event from one call site
// would silently cost unrelated events. Failing here instead keeps the damage
// to the caller that made the mistake.
func (e Event) Validate() error {
	var missing []string
	if e.SpecVersion == "" {
		missing = append(missing, "specversion")
	}
	if e.ID == "" {
		missing = append(missing, "id")
	}
	if e.Source == "" {
		missing = append(missing, "source")
	}
	if e.Type == "" {
		missing = append(missing, "type")
	}
	if e.Time == "" {
		missing = append(missing, "time")
	}
	if len(missing) > 0 {
		return fmt.Errorf("cloudevent is missing required attribute(s): %s", strings.Join(missing, ", "))
	}
	if e.SpecVersion != SpecVersion {
		return fmt.Errorf("unsupported cloudevents specversion %q (this platform emits %s)", e.SpecVersion, SpecVersion)
	}
	// `type` is the one attribute consumers route on, so a typo in it is a
	// silently unroutable event rather than an error. Reverse-DNS under the
	// platform's own prefix keeps the namespace ours and makes a wildcard
	// subscription (`io.adhar.*`) mean what it looks like.
	if !strings.HasPrefix(e.Type, TypePrefix) {
		return fmt.Errorf("event type %q is outside the platform's namespace %q", e.Type, TypePrefix)
	}
	if e.Data != nil && e.DataContentType == "" {
		return fmt.Errorf("event carries data but no datacontenttype")
	}
	return nil
}

// TypePrefix is the reverse-DNS namespace of every event the platform emits.
const TypePrefix = "io.adhar."

// Now is the clock the package reads, swapped in tests. A CloudEvent's `time`
// is part of its identity for a deduplicating consumer, so it must be
// deterministic under test rather than "whatever the wall clock said".
var Now = func() time.Time { return time.Now().UTC() }

// Timestamp formats a time the way the specification requires: RFC 3339.
func Timestamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}
