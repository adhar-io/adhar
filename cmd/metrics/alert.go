package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var alertCmd = &cobra.Command{
	Use:   "alert",
	Short: "List active alerts",
	Long: `List active alerts reported by Prometheus.

This queries the Prometheus HTTP API (/api/v1/alerts) for currently firing and
pending alerts. Use --prometheus-url to point at your Prometheus endpoint.

Examples:
  adhar metrics alert
  adhar metrics alert --prometheus-url http://localhost:9090`,
	RunE: runAlert,
}

func runAlert(cmd *cobra.Command, args []string) error {
	logger.Info("🚨 Querying active alerts from Prometheus...")
	ctx := context.Background()

	endpoint, err := joinURL(prometheusURL, "/api/v1/alerts")
	if err != nil {
		return err
	}
	data, err := promGet(ctx, endpoint)
	if err != nil {
		return err
	}

	var payload struct {
		Alerts []promAlert `json:"alerts"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("decoding alerts response: %w", err)
	}

	switch output {
	case "json":
		return helpers.PrintJSON(payload.Alerts)
	case "yaml":
		return helpers.PrintYAML(payload.Alerts)
	}

	if len(payload.Alerts) == 0 {
		fmt.Println(helpers.CreateSuccess("✅ No active alerts."))
		return nil
	}

	// Sort firing before pending, then by name.
	sort.SliceStable(payload.Alerts, func(i, j int) bool {
		if payload.Alerts[i].State != payload.Alerts[j].State {
			return payload.Alerts[i].State == "firing"
		}
		return payload.Alerts[i].name() < payload.Alerts[j].name()
	})

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%-30s %-12s %-10s %s\n", "🏷️  ALERT", "📊 STATE", "⚠️  SEV", "📦 INSTANCE"))
	b.WriteString(strings.Repeat("─", 90) + "\n")
	for _, a := range payload.Alerts {
		state := a.State
		switch a.State {
		case "firing":
			state = "🔴 firing"
		case "pending":
			state = "🟡 pending"
		}
		b.WriteString(fmt.Sprintf("%-30s %-12s %-10s %s\n",
			trunc(a.name(), 30), state, trunc(a.Labels["severity"], 10), trunc(a.instance(), 30)))
	}
	fmt.Println(helpers.BorderStyle.Render(b.String()))
	fmt.Println(helpers.CreateMuted(fmt.Sprintf("%d active alert(s)", len(payload.Alerts))))
	return nil
}

// promAlert models an alert from the Prometheus /api/v1/alerts response.
type promAlert struct {
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	State       string            `json:"state"`
	ActiveAt    string            `json:"activeAt"`
	Value       string            `json:"value"`
}

func (a promAlert) name() string {
	if n, ok := a.Labels["alertname"]; ok {
		return n
	}
	return "(unnamed)"
}

func (a promAlert) instance() string {
	if i, ok := a.Labels["instance"]; ok {
		return i
	}
	if i, ok := a.Labels["namespace"]; ok {
		return i
	}
	return "-"
}
