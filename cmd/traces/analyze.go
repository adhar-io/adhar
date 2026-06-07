package traces

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var analyzeCmd = &cobra.Command{
	Use:   "analyze",
	Short: "Analyze trace performance",
	Long: `Analyze a trace and identify the slowest spans.

Fetches a trace by ID from Tempo, walks its spans, and reports the span count,
total duration, and the slowest spans (likely bottlenecks). When --service is
given instead of --trace, the most recent matching trace is analyzed.

Examples:
  adhar traces analyze --trace=abc123
  adhar traces analyze --service=web`,
	RunE: runAnalyze,
}

func runAnalyze(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	id := traceID
	if id == "" {
		if service == "" {
			return fmt.Errorf("provide --trace <id> or --service <name> to analyze")
		}
		// Resolve the most recent trace for the service.
		logger.Info(fmt.Sprintf("🔍 Finding most recent trace for service %q...", service))
		res, err := searchTraces(ctx, tempoURL, service, operation, tags, 1)
		if err != nil {
			return err
		}
		if len(res.Traces) == 0 {
			return fmt.Errorf("no traces found for service %q", service)
		}
		id = res.Traces[0].TraceID
	}

	logger.Info(fmt.Sprintf("🔍 Analyzing trace %s...", id))
	body, err := getTrace(ctx, tempoURL, id)
	if err != nil {
		return err
	}

	spans, err := extractSpans(body)
	if err != nil {
		return err
	}
	if len(spans) == 0 {
		fmt.Println(helpers.CreateMuted("Trace contains no spans."))
		return nil
	}

	sort.Slice(spans, func(i, j int) bool { return spans[i].DurationNs > spans[j].DurationNs })

	if output == "json" {
		return helpers.PrintJSON(spans)
	}

	var total int64
	for _, s := range spans {
		total += s.DurationNs
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("🆔 Trace:       %s\n", id))
	b.WriteString(fmt.Sprintf("🔢 Spans:       %d\n", len(spans)))
	b.WriteString(fmt.Sprintf("⏱️  Total dur:   %.2fms", float64(total)/1e6))
	fmt.Println(helpers.BorderStyle.Render(b.String()))

	fmt.Printf("\n%s\n", helpers.TitleStyle.Render("🐢 Slowest spans"))
	var t strings.Builder
	t.WriteString(fmt.Sprintf("%-30s %-22s %s\n", "🔧 SPAN", "📦 SERVICE", "⏱️  DUR"))
	t.WriteString(strings.Repeat("─", 70) + "\n")
	max := 10
	if len(spans) < max {
		max = len(spans)
	}
	for _, s := range spans[:max] {
		t.WriteString(fmt.Sprintf("%-30s %-22s %.2fms\n", trunc(s.Name, 30), trunc(s.Service, 22), float64(s.DurationNs)/1e6))
	}
	fmt.Println(helpers.BorderStyle.Render(t.String()))
	return nil
}

// spanInfo is a flattened view of an OTLP span for analysis.
type spanInfo struct {
	Name       string `json:"name"`
	Service    string `json:"service"`
	DurationNs int64  `json:"durationNs"`
}

// extractSpans parses Tempo's OTLP JSON trace payload into a flat span list.
// The payload shape is { "batches": [ { "resource": {attributes}, "scopeSpans":
// [ { "spans": [ ... ] } ] } ] }.
func extractSpans(body []byte) ([]spanInfo, error) {
	var doc struct {
		Batches []struct {
			Resource struct {
				Attributes []otlpAttr `json:"attributes"`
			} `json:"resource"`
			ScopeSpans []struct {
				Spans []struct {
					Name              string `json:"name"`
					StartTimeUnixNano string `json:"startTimeUnixNano"`
					EndTimeUnixNano   string `json:"endTimeUnixNano"`
				} `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"batches"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("decoding trace payload: %w", err)
	}

	var spans []spanInfo
	for _, batch := range doc.Batches {
		svc := attrString(batch.Resource.Attributes, "service.name")
		for _, ss := range batch.ScopeSpans {
			for _, sp := range ss.Spans {
				start := parseUint(sp.StartTimeUnixNano)
				end := parseUint(sp.EndTimeUnixNano)
				dur := int64(0)
				if end > start {
					dur = int64(end - start)
				}
				spans = append(spans, spanInfo{Name: sp.Name, Service: svc, DurationNs: dur})
			}
		}
	}
	return spans, nil
}

// otlpAttr is an OTLP key/value resource attribute.
type otlpAttr struct {
	Key   string `json:"key"`
	Value struct {
		StringValue string `json:"stringValue"`
	} `json:"value"`
}

func attrString(attrs []otlpAttr, key string) string {
	for _, a := range attrs {
		if a.Key == key {
			return a.Value.StringValue
		}
	}
	return "unknown"
}

func parseUint(s string) uint64 {
	v, _ := strconv.ParseUint(s, 10, 64)
	return v
}
