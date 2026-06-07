package policy

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	listCmd = &cobra.Command{
		Use:   "list",
		Short: "List current policies",
		Long: `List Kyverno policies applied to the platform: cluster-wide ClusterPolicies
and namespaced Policies (kyverno.io/v1), read via the dynamic client.

Examples:
  adhar policy list                     # All Kyverno policies
  adhar policy list --namespace=prod    # Namespaced Policies in prod`,
		RunE: runListPolicies,
	}

	// List specific flags
	showDetails bool
	filterType  string
	output      string
)

func init() {
	listCmd.Flags().BoolVarP(&showDetails, "detailed", "d", false, "Show detailed policy information")
	listCmd.Flags().StringVarP(&filterType, "type", "t", "", "Filter by policy type")
	listCmd.Flags().StringVarP(&output, "output", "o", "table", "Output format: table, json, yaml")
}

type policyRow struct {
	Kind       string
	Name       string
	Namespace  string
	Background string
	Action     string
	Ready      string
	Age        string
}

func runListPolicies(cmd *cobra.Command, args []string) error {
	fmt.Println(helpers.TitleStyle.Render("📋 Kyverno Policies"))

	dyn, err := getDynamicClient()
	if err != nil {
		return unreachable(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var rows []policyRow
	var notes []string

	// Cluster-wide policies.
	cps, err := dyn.Resource(clusterPolicyGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "could not find") {
			notes = append(notes, "ClusterPolicy CRD not installed (Kyverno not present)")
		} else {
			return fmt.Errorf("failed to list ClusterPolicies: %w", err)
		}
	} else {
		for _, p := range cps.Items {
			rows = append(rows, policyRowFrom("ClusterPolicy", p.Object, p.GetName(), "", p.GetCreationTimestamp().Time))
		}
	}

	// Namespaced policies.
	pl, err := dyn.Resource(policyGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "could not find") {
			notes = append(notes, "Policy CRD not installed (Kyverno not present)")
		} else {
			return fmt.Errorf("failed to list Policies: %w", err)
		}
	} else {
		for _, p := range pl.Items {
			rows = append(rows, policyRowFrom("Policy", p.Object, p.GetName(), p.GetNamespace(), p.GetCreationTimestamp().Time))
		}
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Kind != rows[j].Kind {
			return rows[i].Kind < rows[j].Kind
		}
		return rows[i].Name < rows[j].Name
	})

	if output == "json" {
		return helpers.PrintJSON(rows)
	}
	if output == "yaml" {
		return helpers.PrintYAML(rows)
	}

	if len(rows) == 0 {
		fmt.Println(helpers.CreateMuted("   No Kyverno policies found"))
	} else {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("%-16s %-32s %-14s %-10s %-8s %-8s\n", "KIND", "NAME", "NAMESPACE", "ACTION", "READY", "AGE"))
		b.WriteString(strings.Repeat("─", 96) + "\n")
		for _, r := range rows {
			ns := r.Namespace
			if ns == "" {
				ns = "<cluster>"
			}
			b.WriteString(fmt.Sprintf("%-16s %-32s %-14s %-10s %-8s %-8s\n",
				r.Kind, truncate(r.Name, 30), truncate(ns, 12), r.Action, r.Ready, r.Age))
		}
		fmt.Print(b.String())
	}

	for _, n := range notes {
		fmt.Println(helpers.CreateMuted("   " + n))
	}
	return nil
}

func policyRowFrom(kind string, obj map[string]interface{}, name, ns string, created time.Time) policyRow {
	action := nestedString(obj, "spec", "validationFailureAction")
	if action == "" {
		action = "-"
	}
	ready := "❓"
	conds := nestedSlice(obj, "status", "conditions")
	for _, c := range conds {
		if cm, ok := c.(map[string]interface{}); ok {
			if fmt.Sprintf("%v", cm["type"]) == "Ready" {
				if fmt.Sprintf("%v", cm["status"]) == "True" {
					ready = "✅"
				} else {
					ready = "❌"
				}
			}
		}
	}
	return policyRow{
		Kind:      kind,
		Name:      name,
		Namespace: ns,
		Action:    action,
		Ready:     ready,
		Age:       policyAge(created),
	}
}

func policyAge(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
