package security

import (
	"context"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var policiesCmd = &cobra.Command{
	Use:   "policies",
	Short: "List Kyverno security policies and their status",
	Long: `List the Kyverno ClusterPolicies enforcing security on the platform
(kyverno.io/v1) together with their Ready status, action (Audit/Enforce) and
rule count. Read-only — apply/manage policies via 'adhar policy'.

Examples:
  adhar security policies
  adhar security policies --output=json`,
	RunE: runPolicies,
}

type securityPolicyRow struct {
	Name       string `json:"name"`
	Action     string `json:"action"`
	Background bool   `json:"background"`
	Rules      int    `json:"rules"`
	Ready      string `json:"ready"`
	Age        string `json:"age"`
}

func runPolicies(cmd *cobra.Command, args []string) error {
	fmt.Println(helpers.TitleStyle.Render("🛡️  Security Policies (Kyverno)"))

	dyn, err := getDynamicClient()
	if err != nil {
		return unreachable(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	list, err := dyn.Resource(clusterPolicyGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		if crdMissing(err) {
			fmt.Println(helpers.CreateMuted("   ClusterPolicy CRD not installed (Kyverno not present)."))
			return nil
		}
		return fmt.Errorf("list ClusterPolicies: %w", err)
	}

	var rows []securityPolicyRow
	for _, item := range list.Items {
		obj := item.Object
		action := nestedString(obj, "spec", "validationFailureAction")
		if action == "" {
			action = "-"
		}
		background, _, _ := nestedBool(obj, "spec", "background")
		ready := "❓"
		for _, c := range nestedSlice(obj, "status", "conditions") {
			if cm, ok := c.(map[string]interface{}); ok && fmt.Sprintf("%v", cm["type"]) == "Ready" {
				if fmt.Sprintf("%v", cm["status"]) == "True" {
					ready = "✅"
				} else {
					ready = "❌"
				}
			}
		}
		rows = append(rows, securityPolicyRow{
			Name:       item.GetName(),
			Action:     action,
			Background: background,
			Rules:      len(nestedSlice(obj, "spec", "rules")),
			Ready:      ready,
			Age:        age(item.GetCreationTimestamp().Time),
		})
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

	if output == "json" {
		return helpers.PrintJSON(rows)
	}
	if output == "yaml" {
		return helpers.PrintYAML(rows)
	}

	if len(rows) == 0 {
		fmt.Println(helpers.CreateMuted("   No Kyverno ClusterPolicies found."))
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tACTION\tRULES\tBACKGROUND\tREADY\tAGE")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%d\t%t\t%s\t%s\n",
			truncate(r.Name, 40), r.Action, r.Rules, r.Background, r.Ready, r.Age)
	}
	return w.Flush()
}

func nestedBool(obj map[string]interface{}, fields ...string) (bool, bool, error) {
	cur := obj
	for i, k := range fields {
		if i == len(fields)-1 {
			v, ok := cur[k].(bool)
			return v, ok, nil
		}
		next, ok := cur[k].(map[string]interface{})
		if !ok {
			return false, false, nil
		}
		cur = next
	}
	return false, false, nil
}

func age(t time.Time) string {
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
