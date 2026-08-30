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

var vulnerabilitiesCmd = &cobra.Command{
	Use:     "vulnerabilities",
	Aliases: []string{"vulns"},
	Short:   "List individual vulnerabilities from scan reports",
	Long: `List the individual CVEs found across trivy-operator VulnerabilityReports
(aquasecurity.github.io/v1alpha1). Filter with the shared --severity flag to show
only findings at or above a level. Read-only.

Examples:
  adhar security vulnerabilities
  adhar security vulnerabilities --severity=high
  adhar security vulnerabilities --namespace=prod --output=json`,
	RunE: runVulnerabilities,
}

var vulnLimit int

func init() {
	vulnerabilitiesCmd.Flags().IntVar(&vulnLimit, "limit", 100, "Maximum number of vulnerabilities to display")
}

type vulnRow struct {
	Namespace string `json:"namespace"`
	Workload  string `json:"workload"`
	CVE       string `json:"cve"`
	Severity  string `json:"severity"`
	Resource  string `json:"resource"`
	Installed string `json:"installedVersion"`
	Fixed     string `json:"fixedVersion"`
	Title     string `json:"title"`
	rank      int
}

func runVulnerabilities(cmd *cobra.Command, args []string) error {
	fmt.Println(helpers.TitleStyle.Render("🐞 Vulnerabilities (trivy-operator)"))

	dyn, err := getDynamicClient()
	if err != nil {
		return unreachable(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	list, err := dyn.Resource(vulnerabilityReportGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		if crdMissing(err) {
			fmt.Println(helpers.CreateMuted("   VulnerabilityReport CRD not installed (trivy-operator not present)."))
			return nil
		}
		return fmt.Errorf("list VulnerabilityReports: %w", err)
	}

	minRank := severityRank(severity)

	var rows []vulnRow
	for _, item := range list.Items {
		workload := workloadName(item.GetName(), item.GetLabels())
		for _, v := range nestedSlice(item.Object, "report", "vulnerabilities") {
			vm, ok := v.(map[string]interface{})
			if !ok {
				continue
			}
			sev := fmt.Sprintf("%v", vm["severity"])
			rank := severityRank(sev)
			if minRank > 0 && rank < minRank {
				continue
			}
			rows = append(rows, vulnRow{
				Namespace: item.GetNamespace(),
				Workload:  workload,
				CVE:       fmt.Sprintf("%v", vm["vulnerabilityID"]),
				Severity:  normalizeSeverityDisplay(sev),
				Resource:  fmt.Sprintf("%v", vm["resource"]),
				Installed: fmt.Sprintf("%v", vm["installedVersion"]),
				Fixed:     fmt.Sprintf("%v", vm["fixedVersion"]),
				Title:     fmt.Sprintf("%v", vm["title"]),
				rank:      rank,
			})
		}
	}

	// Highest severity first.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].rank != rows[j].rank {
			return rows[i].rank > rows[j].rank
		}
		return rows[i].CVE < rows[j].CVE
	})

	total := len(rows)
	if vulnLimit > 0 && total > vulnLimit {
		rows = rows[:vulnLimit]
	}

	if output == "json" {
		return helpers.PrintJSON(rows)
	}
	if output == "yaml" {
		return helpers.PrintYAML(rows)
	}

	if total == 0 {
		fmt.Println(helpers.CreateMuted("   No vulnerabilities found matching the filter."))
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SEVERITY\tCVE\tRESOURCE\tINSTALLED\tFIXED\tWORKLOAD")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			r.Severity, r.CVE, truncate(r.Resource, 28),
			valueOrDash(r.Installed), valueOrDash(r.Fixed), truncate(r.Workload, 28))
	}
	if err := w.Flush(); err != nil {
		return err
	}

	shown := len(rows)
	if shown < total {
		fmt.Println(helpers.CreateMuted(fmt.Sprintf("   Showing %d of %d (raise --limit to see more).", shown, total)))
	} else {
		fmt.Println(helpers.CreateMuted(fmt.Sprintf("   %d vulnerabilities.", total)))
	}
	return nil
}

func normalizeSeverityDisplay(sev string) string {
	if n := normalizeSeverity(sev); n != "" {
		return n
	}
	if sev == "" || sev == "<nil>" {
		return "unknown"
	}
	return sev
}
