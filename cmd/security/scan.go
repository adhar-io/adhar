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

var scanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Show image vulnerability scan results",
	Long: `Summarise the trivy-operator VulnerabilityReports produced in the cluster
(aquasecurity.github.io/v1alpha1). Reports are generated continuously by the
operator; this reads them and rolls up Critical/High/Medium/Low counts per
workload. Read-only.

Examples:
  adhar security scan                    # All namespaces
  adhar security scan --namespace=prod   # A single namespace
  adhar security scan --output=json`,
	RunE: runScan,
}

type scanRow struct {
	Namespace string `json:"namespace"`
	Workload  string `json:"workload"`
	Image     string `json:"image"`
	Critical  int    `json:"critical"`
	High      int    `json:"high"`
	Medium    int    `json:"medium"`
	Low       int    `json:"low"`
	Unknown   int    `json:"unknown"`
}

func runScan(cmd *cobra.Command, args []string) error {
	fmt.Println(helpers.TitleStyle.Render("🔍 Image Vulnerability Scan (trivy-operator)"))

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

	var rows []scanRow
	var totals scanRow
	for _, item := range list.Items {
		obj := item.Object
		sum, _ := nestedMap(obj, "report", "summary")
		row := scanRow{
			Namespace: item.GetNamespace(),
			Workload:  workloadName(item.GetName(), item.GetLabels()),
			Image:     imageRef(obj),
			Critical:  intOf(sum["criticalCount"]),
			High:      intOf(sum["highCount"]),
			Medium:    intOf(sum["mediumCount"]),
			Low:       intOf(sum["lowCount"]),
			Unknown:   intOf(sum["unknownCount"]),
		}
		rows = append(rows, row)
		totals.Critical += row.Critical
		totals.High += row.High
		totals.Medium += row.Medium
		totals.Low += row.Low
		totals.Unknown += row.Unknown
	}

	// Most severe first.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Critical != rows[j].Critical {
			return rows[i].Critical > rows[j].Critical
		}
		if rows[i].High != rows[j].High {
			return rows[i].High > rows[j].High
		}
		return rows[i].Workload < rows[j].Workload
	})

	if output == "json" {
		return helpers.PrintJSON(rows)
	}
	if output == "yaml" {
		return helpers.PrintYAML(rows)
	}

	if len(rows) == 0 {
		fmt.Println(helpers.CreateMuted("   No VulnerabilityReports found (the operator may still be scanning)."))
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAMESPACE\tWORKLOAD\tIMAGE\tCRIT\tHIGH\tMED\tLOW")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%d\n",
			r.Namespace, truncate(r.Workload, 32), truncate(r.Image, 40),
			r.Critical, r.High, r.Medium, r.Low)
	}
	if err := w.Flush(); err != nil {
		return err
	}

	summary := fmt.Sprintf("Reports: %d   Critical: %d  High: %d  Medium: %d  Low: %d",
		len(rows), totals.Critical, totals.High, totals.Medium, totals.Low)
	fmt.Println(helpers.BorderStyle.Width(72).Render(summary))
	return nil
}

// workloadName prefers the trivy resource labels (kind/name) over the mangled
// report object name (e.g. "replicaset-foo-abc123-...").
func workloadName(reportName string, labels map[string]string) string {
	kind := labels["trivy-operator.resource.kind"]
	name := labels["trivy-operator.resource.name"]
	if kind != "" && name != "" {
		return kind + "/" + name
	}
	if name != "" {
		return name
	}
	return reportName
}

func imageRef(obj map[string]interface{}) string {
	repo := nestedString(obj, "report", "artifact", "repository")
	tag := nestedString(obj, "report", "artifact", "tag")
	if repo == "" {
		return "-"
	}
	if tag != "" {
		return repo + ":" + tag
	}
	return repo
}
