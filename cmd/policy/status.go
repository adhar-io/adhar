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

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Summarize policy compliance from PolicyReports",
	Long: `Summarize Kyverno/wgpolicyk8s.io PolicyReports and ClusterPolicyReports,
aggregating pass/fail/warn/error/skip result counts per report and overall.

Examples:
  adhar policy status                   # Summary across all namespaces
  adhar policy status --namespace=prod  # Summary for a single namespace`,
	RunE: runPolicyStatus,
}

func init() {
	PolicyCmd.AddCommand(statusCmd)
}

type reportSummary struct {
	Name      string
	Namespace string
	Pass      int
	Fail      int
	Warn      int
	Error     int
	Skip      int
}

func (r reportSummary) total() int { return r.Pass + r.Fail + r.Warn + r.Error + r.Skip }

func runPolicyStatus(cmd *cobra.Command, args []string) error {
	fmt.Println(helpers.TitleStyle.Render("🛡️  Policy Compliance (PolicyReports)"))

	dyn, err := getDynamicClient()
	if err != nil {
		return unreachable(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var summaries []reportSummary
	var notes []string

	// Namespaced PolicyReports.
	prs, err := dyn.Resource(policyReportGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "could not find") {
			notes = append(notes, "PolicyReport CRD not installed")
		} else {
			return fmt.Errorf("failed to list PolicyReports: %w", err)
		}
	} else {
		for _, pr := range prs.Items {
			summaries = append(summaries, summarizeReport(pr.GetName(), pr.GetNamespace(), pr.Object))
		}
	}

	// Cluster-scoped reports (only when no namespace filter is set).
	if namespace == "" {
		cprs, err := dyn.Resource(clusterPolicyReportGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			if strings.Contains(err.Error(), "could not find") {
				notes = append(notes, "ClusterPolicyReport CRD not installed")
			} else {
				return fmt.Errorf("failed to list ClusterPolicyReports: %w", err)
			}
		} else {
			for _, cpr := range cprs.Items {
				summaries = append(summaries, summarizeReport(cpr.GetName(), "<cluster>", cpr.Object))
			}
		}
	}

	sort.Slice(summaries, func(i, j int) bool { return summaries[i].Name < summaries[j].Name })

	if output == "json" {
		return helpers.PrintJSON(summaries)
	}
	if output == "yaml" {
		return helpers.PrintYAML(summaries)
	}

	var total reportSummary
	if len(summaries) == 0 {
		fmt.Println(helpers.CreateMuted("   No PolicyReports found"))
	} else {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("%-40s %-14s %-6s %-6s %-6s %-6s %-6s\n",
			"REPORT", "NAMESPACE", "PASS", "FAIL", "WARN", "ERROR", "SKIP"))
		b.WriteString(strings.Repeat("─", 92) + "\n")
		for _, s := range summaries {
			b.WriteString(fmt.Sprintf("%-40s %-14s %-6d %-6d %-6d %-6d %-6d\n",
				truncate(s.Name, 38), truncate(s.Namespace, 12), s.Pass, s.Fail, s.Warn, s.Error, s.Skip))
			total.Pass += s.Pass
			total.Fail += s.Fail
			total.Warn += s.Warn
			total.Error += s.Error
			total.Skip += s.Skip
		}
		fmt.Print(b.String())
	}

	overall := "✅ Compliant"
	if total.Fail > 0 || total.Error > 0 {
		overall = "❌ Violations present"
	} else if total.Warn > 0 {
		overall = "⚠️  Warnings present"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Overall: %s\n", overall))
	sb.WriteString(fmt.Sprintf("Pass: %d  Fail: %d  Warn: %d  Error: %d  Skip: %d  (total results: %d)",
		total.Pass, total.Fail, total.Warn, total.Error, total.Skip, total.total()))
	fmt.Println(helpers.BorderStyle.Width(70).Render(sb.String()))

	for _, n := range notes {
		fmt.Println(helpers.CreateMuted("   " + n))
	}
	return nil
}

// summarizeReport prefers the report's `summary` block when present, otherwise
// tallies the `results[].result` entries.
func summarizeReport(name, ns string, obj map[string]interface{}) reportSummary {
	s := reportSummary{Name: name, Namespace: ns}

	if sum, found, _ := nestedMap(obj, "summary"); found {
		s.Pass = intOf(sum["pass"])
		s.Fail = intOf(sum["fail"])
		s.Warn = intOf(sum["warn"])
		s.Error = intOf(sum["error"])
		s.Skip = intOf(sum["skip"])
		if s.total() > 0 {
			return s
		}
	}

	for _, r := range nestedSlice(obj, "results") {
		rm, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		switch fmt.Sprintf("%v", rm["result"]) {
		case "pass":
			s.Pass++
		case "fail":
			s.Fail++
		case "warn":
			s.Warn++
		case "error":
			s.Error++
		case "skip":
			s.Skip++
		}
	}
	return s
}
