/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package compliance

// `adhar compliance` — the platform's own control posture, as evidence.
//
// Reads the PolicyReports and ClusterPolicyReports that Kyverno, Kubescape and
// anything else implementing the wgpolicyk8s.io standard already write, and
// reduces them to: which controls exist, which pass, which fail, and on what
// date. Sources are read through the STANDARD API rather than each tool's own,
// so a scanner added later appears in the report without code changes here.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	outputPath   string
	outputFormat string
	maxEvidence  int
	failuresOnly bool
)

// ComplianceCmd is the command group.
var ComplianceCmd = &cobra.Command{
	Use:     "compliance",
	Aliases: []string{"posture", "audit"},
	Short:   "Control posture from the platform's policy reports",
	Long: `📋 **Adhar Compliance**

Reduces the cluster's PolicyReports (Kyverno, Kubescape, any wgpolicyk8s.io
producer) to a control posture: what is enforced, what passes, what fails.

  adhar compliance                      the posture, on screen
  adhar compliance --failures-only      just what needs fixing
  adhar compliance export -o report.md  an auditor-ready artifact

Compliance is the share of *applicable* evaluations that passed. Skipped checks
are excluded from the denominator: a control that did not apply to a resource is
not evidence that the resource complies, and counting it as a pass inflates the
score — which is the first number an auditor will probe.`,
	RunE: runReport,
}

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Write the posture as a Markdown or JSON evidence artifact",
	Long: `Writes the posture to a file you can attach to an audit, commit as a
point-in-time record, or diff against last quarter's.

Markdown by default because it is diffable and renders everywhere; a binary
report cannot be compared with the previous one, which is the question auditors
most often ask.`,
	RunE: runExport,
}

func init() {
	ComplianceCmd.PersistentFlags().IntVar(&maxEvidence, "max-evidence", 5,
		"Failing resources to list per control (0 = none)")
	ComplianceCmd.Flags().BoolVar(&failuresOnly, "failures-only", false, "Show only controls with failures")
	exportCmd.Flags().StringVarP(&outputPath, "output", "o", "compliance-report.md", "File to write")
	exportCmd.Flags().StringVar(&outputFormat, "format", "", "markdown|json (default: inferred from the extension)")
	ComplianceCmd.AddCommand(exportCmd)
}

// gather reads both report kinds through the policy-report standard API.
func gather(ctx context.Context) (Posture, error) {
	cfg, err := helpers.GetKubeConfig()
	if err != nil {
		return Posture{}, fmt.Errorf("no reachable cluster: %w", err)
	}
	c, err := client.New(cfg, client.Options{})
	if err != nil {
		return Posture{}, err
	}

	var results []Result
	for _, kind := range []string{"PolicyReportList", "ClusterPolicyReportList"} {
		list := &unstructured.UnstructuredList{}
		list.SetAPIVersion("wgpolicyk8s.io/v1alpha2")
		list.SetKind(kind)
		if err := c.List(ctx, list); err != nil {
			// A cluster with only one of the two kinds is normal; a cluster with
			// neither means no policy engine, which the caller reports honestly.
			continue
		}
		for i := range list.Items {
			results = append(results, resultsOf(&list.Items[i])...)
		}
	}
	if len(results) == 0 {
		return Posture{}, fmt.Errorf("no policy reports found — is a policy engine (kyverno, kubescape) installed and has it run?")
	}
	return Aggregate(results, clusterName(cfg.Host), time.Now(), maxEvidence), nil
}

func resultsOf(u *unstructured.Unstructured) []Result {
	raw, found, _ := unstructured.NestedSlice(u.Object, "results")
	if !found {
		return nil
	}
	out := make([]Result, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		str := func(k string) string {
			s, _, _ := unstructured.NestedString(m, k)
			return s
		}
		r := Result{
			Policy:   str("policy"),
			Rule:     str("rule"),
			Outcome:  str("result"),
			Severity: str("severity"),
			Category: str("category"),
			Source:   str("source"),
		}
		// The subject lives in resources[0]; absent for cluster-scoped checks.
		if res, found, _ := unstructured.NestedSlice(m, "resources"); found && len(res) > 0 {
			if rm, ok := res[0].(map[string]interface{}); ok {
				r.Namespace, _, _ = unstructured.NestedString(rm, "namespace")
				r.Kind, _, _ = unstructured.NestedString(rm, "kind")
				r.Name, _, _ = unstructured.NestedString(rm, "name")
			}
		}
		if r.Policy == "" {
			continue
		}
		out = append(out, r)
	}
	return out
}

// clusterName makes the artifact self-describing; an evidence file that does not
// say which cluster it came from is not evidence.
func clusterName(host string) string {
	h := strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	if i := strings.IndexAny(h, ":/"); i > 0 {
		h = h[:i]
	}
	return h
}

func runReport(cmd *cobra.Command, args []string) error {
	p, err := gather(cmd.Context())
	if err != nil {
		return err
	}

	fmt.Println(helpers.SectionHeading("📋", fmt.Sprintf("Compliance posture · %s", p.Cluster)))
	fmt.Printf("  %s overall · %d control(s), %d with failures · %d pass / %d fail",
		pct(p.OverallCompliance()), len(p.Controls), p.FailingControls(), p.Totals.Pass, p.Totals.Fail)
	if p.Totals.Skip > 0 {
		fmt.Printf(" · %d skipped (excluded)", p.Totals.Skip)
	}
	fmt.Print("\n\n")

	ct := helpers.NewTable("CATEGORY", "COMPLIANCE", "CONTROLS", "WITH FAILURES")
	for _, c := range p.Categories {
		ct.Row(c.Category, pct(c.Compliance()), fmt.Sprint(c.Controls), failCell(c.FailingControls))
	}
	fmt.Println(ct.Render())

	t := helpers.NewTable("CONTROL", "SEVERITY", "CATEGORY", "PASS", "FAIL", "STATE")
	shown := 0
	for _, c := range p.Controls {
		if failuresOnly && c.Fail == 0 {
			continue
		}
		shown++
		t.Row(c.Policy, orUnset(c.Severity), c.Category, fmt.Sprint(c.Pass), fmt.Sprint(c.Fail), controlState(c))
	}
	if shown == 0 {
		fmt.Printf("  %s\n\n", helpers.StateReady("no control is failing"))
		return nil
	}
	fmt.Println(t.Render())
	fmt.Printf("  %s\n\n", helpers.SubtitleStyle.Render(
		"compliance counts applicable evaluations only · adhar compliance export -o report.md"))
	return nil
}

func runExport(cmd *cobra.Command, args []string) error {
	p, err := gather(cmd.Context())
	if err != nil {
		return err
	}
	format := outputFormat
	if format == "" {
		if strings.HasSuffix(strings.ToLower(outputPath), ".json") {
			format = "json"
		} else {
			format = "markdown"
		}
	}

	var data []byte
	switch format {
	case "json":
		// The full structure, not a rendering of it: a machine-readable artifact
		// is what lets a GRC system ingest the posture without scraping prose.
		data, err = json.MarshalIndent(p, "", "  ")
		if err != nil {
			return err
		}
	case "markdown", "md":
		data = []byte(p.Markdown())
	default:
		return fmt.Errorf("unknown --format %q (markdown|json)", format)
	}

	if err := os.WriteFile(outputPath, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", outputPath, err)
	}
	fmt.Println(helpers.SectionHeading("📋", "Compliance evidence"))
	fmt.Printf("  %s %s\n", helpers.StateReady("written"), outputPath)
	fmt.Printf("  %s\n\n", helpers.SubtitleStyle.Render(fmt.Sprintf(
		"%s overall · %d control(s), %d failing · generated %s",
		pct(p.OverallCompliance()), len(p.Controls), p.FailingControls(),
		p.GeneratedAt.UTC().Format(time.RFC3339))))
	return nil
}

func controlState(c Control) string {
	switch {
	case c.Fail > 0:
		return helpers.StateDegraded("failing")
	case c.Evaluated() == 0:
		return helpers.StatePending("not applicable")
	default:
		return helpers.StateReady("passing")
	}
}

func failCell(n int) string {
	if n == 0 {
		return helpers.StateReady("0")
	}
	return helpers.StateDegraded(fmt.Sprint(n))
}

func orUnset(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
