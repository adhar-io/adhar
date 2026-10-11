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
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// newID mints a CloudEvents `id`.
//
// Random rather than a counter or a hash of the content. The spec requires
// (source, id) to be unique, and a controller that restarts would restart a
// counter — producing an id a deduplicating consumer has already seen and
// therefore silently discarding a real event. Content hashing has the same
// problem in reverse: two genuinely separate occurrences of the same fact
// collapse into one.
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is not a condition worth propagating through an
		// advisory publish path; a time-ordered fallback keeps ids unique
		// enough to be useful and the event is better sent than dropped.
		return fmt.Sprintf("t-%d", Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// correlationKey is the context key carrying a correlation id. Unexported type
// so nothing outside this package can collide with it.
type correlationKey struct{}

// WithCorrelation tags a context with the id that ties every event from one
// reconcile, request or agent run together.
//
// This is what makes the bus answerable: "what happened during the reconcile
// that failed at 10:04" is a filter on one attribute, rather than a guess from
// timestamps across five components.
func WithCorrelation(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, correlationKey{}, id)
}

// CorrelationFrom reads the correlation id, or "" when the context carries
// none. Absent is normal and not an error: a correlation id is a convenience
// for tracing, never a requirement for publishing.
func CorrelationFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(correlationKey{}).(string); ok {
		return v
	}
	return ""
}

// NewCorrelation mints a correlation id for a unit of work that does not
// already have one.
func NewCorrelation() string { return newID() }
