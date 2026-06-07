/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the file at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package traces

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Default in-cluster Tempo query-frontend endpoint. Tempo exposes a
// Jaeger-compatible/native HTTP API on this service. Override with --tempo-url.
const defaultTempoURL = "http://tempo.monitoring.svc:3100"

// httpTimeout parses the --timeout flag, falling back to 30s on error.
func httpTimeout() time.Duration {
	if timeout == "" {
		return 30 * time.Second
	}
	d, err := time.ParseDuration(timeout)
	if err != nil {
		return 30 * time.Second
	}
	return d
}

// joinURL joins a base URL with a path, tolerating trailing/leading slashes.
func joinURL(base, p string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid base URL %q: %w", base, err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(p, "/")
	return u.String(), nil
}

// tempoGet performs a GET against the Tempo HTTP API and returns the raw body.
// query is an optional already-encoded query string (without leading '?').
func tempoGet(ctx context.Context, base, path, query string) ([]byte, error) {
	endpoint, err := joinURL(base, path)
	if err != nil {
		return nil, err
	}
	if query != "" {
		endpoint += "?" + query
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: httpTimeout()}
	resp, err := client.Do(req)
	if err != nil {
		return nil, unreachable(endpoint, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return body, fmt.Errorf("tempo returned 404 for %s (not found)", endpoint)
	}
	if resp.StatusCode != http.StatusOK {
		return body, fmt.Errorf("tempo API returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// unreachable wraps a connection error with a friendly hint.
func unreachable(endpoint string, err error) error {
	return fmt.Errorf("could not reach %s: %w\n  hint: Tempo may not be running, or use --tempo-url / port-forward (kubectl -n monitoring port-forward svc/tempo 3100:3100)", endpoint, err)
}

// trunc shortens s to n runes, appending an ellipsis when truncated.
func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// tempoSearchResponse models the Tempo /api/search response.
type tempoSearchResponse struct {
	Traces  []tempoTraceSummary `json:"traces"`
	Metrics struct {
		InspectedTraces int `json:"inspectedTraces"`
		InspectedBytes  int `json:"inspectedBytes"`
	} `json:"metrics"`
}

// tempoTraceSummary is one entry from a Tempo search result.
type tempoTraceSummary struct {
	TraceID           string `json:"traceID"`
	RootServiceName   string `json:"rootServiceName"`
	RootTraceName     string `json:"rootTraceName"`
	StartTimeUnixNano string `json:"startTimeUnixNano"`
	DurationMs        int    `json:"durationMs"`
}

// searchTraces queries Tempo's /api/search endpoint, filtering by
// service/operation and any extra tag filters.
func searchTraces(ctx context.Context, base, svc, op, extraTags string, limit int) (*tempoSearchResponse, error) {
	q := url.Values{}
	// Tempo accepts TraceQL via "q", or simple tag filters via "tags".
	var tagFilters []string
	if svc != "" {
		tagFilters = append(tagFilters, "service.name="+svc)
	}
	if op != "" {
		tagFilters = append(tagFilters, "name="+op)
	}
	if extraTags != "" {
		tagFilters = append(tagFilters, extraTags)
	}
	if len(tagFilters) > 0 {
		q.Set("tags", strings.Join(tagFilters, " "))
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}

	body, err := tempoGet(ctx, base, "/api/search", q.Encode())
	if err != nil {
		return nil, err
	}
	var res tempoSearchResponse
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("decoding Tempo search response: %w", err)
	}
	return &res, nil
}

// getTrace fetches a single trace by ID from Tempo's /api/traces/{id} endpoint
// and returns the raw JSON payload.
func getTrace(ctx context.Context, base, id string) ([]byte, error) {
	return tempoGet(ctx, base, "/api/traces/"+url.PathEscape(id), "")
}

// renderTraceTable prints a table of trace summaries.
func renderTraceTable(traces []tempoTraceSummary) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%-34s %-22s %-22s %s\n", "🆔 TRACE ID", "📦 SERVICE", "🔧 OPERATION", "⏱️  DUR"))
	b.WriteString(strings.Repeat("─", 95) + "\n")
	for _, t := range traces {
		b.WriteString(fmt.Sprintf("%-34s %-22s %-22s %dms\n",
			trunc(t.TraceID, 34),
			trunc(t.RootServiceName, 22),
			trunc(t.RootTraceName, 22),
			t.DurationMs))
	}
	return b.String()
}
