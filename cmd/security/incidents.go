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

var incidentsCmd = &cobra.Command{
	Use:   "incidents",
	Short: "Surface active security incidents",
	Long: `Surface active security incidents from cluster reports:
  • failing policy results (wgpolicyk8s.io PolicyReports / ClusterPolicyReports, result=fail)
  • exposed secrets detected by trivy-operator (ExposedSecretReports)
Read-only.

Examples:
  adhar security incidents
  adhar security incidents --namespace=prod --output=json`,
	RunE: runIncidents,
}

type incident struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Severity  string `json:"severity"`
	Policy    string `json:"policy"`
	Resource  string `json:"resource"`
	Message   string `json:"message"`
	rank      int
}

func runIncidents(cmd *cobra.Command, args []string) error {
	fmt.Println(helpers.TitleStyle.Render("🚨 Security Incidents"))

	dyn, err := getDynamicClient()
	if err != nil {
		return unreachable(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var incidents []incident
	var notes []string

	// 1) Failing policy results (namespaced PolicyReports).
	prs, err := dyn.Resource(policyReportGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		if crdMissing(err) {
			notes = append(notes, "PolicyReport CRD not installed")
		} else {
			return fmt.Errorf("list PolicyReports: %w", err)
		}
	} else {
		for _, pr := range prs.Items {
			incidents = append(incidents, failingResults(pr.GetNamespace(), pr.Object)...)
		}
	}

	// Cluster-scoped policy reports (only without a namespace filter).
	if namespace == "" {
		cprs, err := dyn.Resource(clusterPolicyReportGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			if crdMissing(err) {
				notes = append(notes, "ClusterPolicyReport CRD not installed")
			} else {
				return fmt.Errorf("list ClusterPolicyReports: %w", err)
			}
		} else {
			for _, cpr := range cprs.Items {
				incidents = append(incidents, failingResults("<cluster>", cpr.Object)...)
			}
		}
	}

	// 2) Exposed secrets (trivy-operator ExposedSecretReports).
	esrs, err := dyn.Resource(exposedSecretReportGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		if crdMissing(err) {
			notes = append(notes, "ExposedSecretReport CRD not installed (trivy-operator not present)")
		} else {
			return fmt.Errorf("list ExposedSecretReports: %w", err)
		}
	} else {
		for _, esr := range esrs.Items {
			workload := workloadName(esr.GetName(), esr.GetLabels())
			for _, s := range nestedSlice(esr.Object, "report", "secrets") {
				sm, ok := s.(map[string]interface{})
				if !ok {
					continue
				}
				sev := fmt.Sprintf("%v", sm["severity"])
				incidents = append(incidents, incident{
					Kind:      "ExposedSecret",
					Namespace: esr.GetNamespace(),
					Severity:  normalizeSeverityDisplay(sev),
					Policy:    fmt.Sprintf("%v", sm["ruleID"]),
					Resource:  workload,
					Message:   fmt.Sprintf("%v", sm["title"]),
					rank:      severityRank(sev),
				})
			}
		}
	}

	sort.Slice(incidents, func(i, j int) bool {
		if incidents[i].rank != incidents[j].rank {
			return incidents[i].rank > incidents[j].rank
		}
		return incidents[i].Kind < incidents[j].Kind
	})

	if output == "json" {
		return helpers.PrintJSON(incidents)
	}
	if output == "yaml" {
		return helpers.PrintYAML(incidents)
	}

	if len(incidents) == 0 {
		fmt.Println(helpers.CreateSuccess("No active security incidents. ✅"))
	} else {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "TYPE\tSEVERITY\tNAMESPACE\tPOLICY/RULE\tRESOURCE\tMESSAGE")
		for _, in := range incidents {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				in.Kind, valueOrDash(in.Severity), valueOrDash(in.Namespace),
				truncate(in.Policy, 26), truncate(in.Resource, 26), truncate(in.Message, 40))
		}
		if err := w.Flush(); err != nil {
			return err
		}
		fmt.Println(helpers.CreateMuted(fmt.Sprintf("   %d active incident(s).", len(incidents))))
	}

	for _, n := range notes {
		fmt.Println(helpers.CreateMuted("   " + n))
	}
	return nil
}

// failingResults extracts result=fail entries from a (Cluster)PolicyReport.
func failingResults(ns string, obj map[string]interface{}) []incident {
	var out []incident
	for _, r := range nestedSlice(obj, "results") {
		rm, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		if fmt.Sprintf("%v", rm["result"]) != "fail" {
			continue
		}
		sev := fmt.Sprintf("%v", rm["severity"])
		resource := ""
		if resList, ok := rm["resources"].([]interface{}); ok && len(resList) > 0 {
			if rmap, ok := resList[0].(map[string]interface{}); ok {
				resource = fmt.Sprintf("%v/%v", rmap["kind"], rmap["name"])
			}
		}
		out = append(out, incident{
			Kind:      "PolicyViolation",
			Namespace: ns,
			Severity:  normalizeSeverityDisplay(sev),
			Policy:    fmt.Sprintf("%v", rm["policy"]),
			Resource:  resource,
			Message:   fmt.Sprintf("%v", rm["message"]),
			rank:      severityRank(sev),
		})
	}
	return out
}
