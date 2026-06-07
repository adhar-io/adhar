package network

import (
	"context"
	"fmt"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var trafficCmd = &cobra.Command{
	Use:   "traffic",
	Short: "Show traffic-path component status",
	Long: `Report read-only status of the components on the traffic data path:
the Cilium agent and Cilium Envoy (gateway) DaemonSets, plus the Hubble UI if
present. This command does not stream live flows.

Examples:
  adhar network traffic
  adhar network traffic --monitor`,
	RunE: runTraffic,
}

var (
	monitor bool
)

func init() {
	trafficCmd.Flags().BoolVar(&monitor, "monitor", false, "Reserved: live monitoring requires Hubble; currently reports status only")
}

func runTraffic(cmd *cobra.Command, args []string) error {
	logger.Info("📊 Inspecting traffic-path components...")

	if monitor {
		fmt.Println(helpers.CreateMuted("ℹ️  Live flow monitoring requires Hubble; showing component status instead."))
	}

	clientset, err := getClientset()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout())
	defer cancel()

	type comp struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	checks := []struct {
		label    string
		selector string
	}{
		{"cilium-agent", "app.kubernetes.io/name=cilium-agent"},
		{"cilium-envoy (gateway)", "app.kubernetes.io/name=cilium-envoy"},
	}

	var comps []comp
	for _, c := range checks {
		dsList, err := clientset.AppsV1().DaemonSets(globals.AdharSystemNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: c.selector,
		})
		status := "not found"
		if err == nil && len(dsList.Items) > 0 {
			ds := dsList.Items[0]
			status = fmt.Sprintf("%d/%d ready", ds.Status.NumberReady, ds.Status.DesiredNumberScheduled)
		}
		comps = append(comps, comp{c.label, status})
	}

	if output == "json" {
		return helpers.PrintJSON(comps)
	}
	if output == "yaml" {
		return helpers.PrintYAML(comps)
	}

	fmt.Printf("\n%s\n", helpers.TitleStyle.Render("📊 Traffic-Path Components"))
	var t strings.Builder
	t.WriteString(fmt.Sprintf("%-28s %-20s\n", "COMPONENT", "STATUS"))
	t.WriteString(strings.Repeat("─", 50) + "\n")
	for _, c := range comps {
		t.WriteString(fmt.Sprintf("%-28s %-20s\n", c.Name, c.Status))
	}
	fmt.Println(helpers.BorderStyle.Width(55).Render(t.String()))
	return nil
}
