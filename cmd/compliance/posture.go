/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package compliance

// Turning policy reports into compliance evidence.
//
// The platform already produced the raw material — Kyverno writes ~1000
// PolicyReports, Kubescape adds its own — but raw reports are not evidence. An
// auditor does not want 11,715 individual results; they want to know which
// controls exist, which are passing, which are not, and on what date. Nobody was
// doing that reduction, so answering a compliance question meant someone writing
// jq against a thousand objects.
//
// The aggregation lives here, separate from any cluster access, because the
// arithmetic is the part that has to be right: a posture report that quietly
// drops a failing control is worse than no report, since it is believed.

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Result is one policy evaluation, flattened from a PolicyReport entry.
type Result struct {
	Policy   string
	Rule     string
	Outcome  string // pass | fail | warn | error | skip
	Severity string // critical | high | medium | low | ""
	Category string
	Source   string // kyverno | kubescape | …
	// Subject identifies what was evaluated, for the evidence trail.
	Namespace string
	Kind      string
	Name      string
}

// Control is one policy rolled up across every resource it was evaluated on.
type Control struct {
	Policy   string
	Category string
	Severity string
	Source   string
	Pass     int
	Fail     int
	Warn     int
	Error    int
	Skip     int
	// Failing lists the resources that failed, capped by the caller. An auditor
	// asking "which ones" should not have to run a second query.
	Failing []string
}

// Evaluated is how many results contributed a pass/fail judgement. Skips are
// excluded: a control that did not apply to a resource says nothing about
// compliance, and counting it as a pass inflates every score.
func (c Control) Evaluated() int { return c.Pass + c.Fail }

// Compliance is the share of applicable evaluations that passed, 0–100.
// Returns -1 when nothing applicable was evaluated, which is NOT the same as 0%
// and must not be rendered as a failing score.
func (c Control) Compliance() float64 {
	n := c.Evaluated()
	if n == 0 {
		return -1
	}
	return float64(c.Pass) / float64(n) * 100
}

// Posture is the whole estate at a point in time.
type Posture struct {
	GeneratedAt time.Time
	Cluster     string
	Totals      struct{ Pass, Fail, Warn, Error, Skip int }
	Controls    []Control
	// Categories rolls controls up to the grouping an auditor reads by.
	Categories []CategoryRoll
}

type CategoryRoll struct {
	Category string
	Pass     int
	Fail     int
	Controls int
	// Failing is how many controls have at least one failure — the number that
	// matters, because one control failing on 60 pods is one thing to fix, not 60.
	FailingControls int
}

// Compliance for a category, on the same applicable-only basis as a control.
func (c CategoryRoll) Compliance() float64 {
	n := c.Pass + c.Fail
	if n == 0 {
		return -1
	}
	return float64(c.Pass) / float64(n) * 100
}

// severityRank orders findings the way someone triaging reads them.
func severityRank(s string) int {
	switch strings.ToLower(s) {
	case "critical":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	case "low":
		return 3
	default:
		return 4
	}
}

// Aggregate reduces raw results to a posture.
//
// `maxFailing` caps the evidence list per control. Unbounded, one permissive
// policy across a large cluster produces a report megabytes long that nobody
// opens; zero means no examples, which makes a finding unactionable.
func Aggregate(results []Result, cluster string, now time.Time, maxFailing int) Posture {
	if maxFailing < 0 {
		maxFailing = 0
	}
	byPolicy := map[string]*Control{}
	p := Posture{GeneratedAt: now, Cluster: cluster}

	for _, r := range results {
		c := byPolicy[r.Policy]
		if c == nil {
			c = &Control{
				Policy:   r.Policy,
				Category: firstNonEmpty(r.Category, "Uncategorised"),
				Severity: r.Severity,
				Source:   r.Source,
			}
			byPolicy[r.Policy] = c
		}
		// Keep the HIGHEST severity seen for a policy: rules within one policy
		// can differ, and reporting the lowest would understate the finding.
		if severityRank(r.Severity) < severityRank(c.Severity) {
			c.Severity = r.Severity
		}
		switch strings.ToLower(r.Outcome) {
		case "pass":
			c.Pass++
			p.Totals.Pass++
		case "fail":
			c.Fail++
			p.Totals.Fail++
			if len(c.Failing) < maxFailing {
				c.Failing = append(c.Failing, subject(r))
			}
		case "warn":
			c.Warn++
			p.Totals.Warn++
		case "error":
			c.Error++
			p.Totals.Error++
		case "skip":
			c.Skip++
			p.Totals.Skip++
		}
	}

	for _, c := range byPolicy {
		p.Controls = append(p.Controls, *c)
	}
	// Failing first, then by severity, then by how much is failing, then name —
	// so the top of the report is what to fix first and the order is stable.
	sort.SliceStable(p.Controls, func(i, j int) bool {
		a, b := p.Controls[i], p.Controls[j]
		if (a.Fail > 0) != (b.Fail > 0) {
			return a.Fail > 0
		}
		if ra, rb := severityRank(a.Severity), severityRank(b.Severity); ra != rb {
			return ra < rb
		}
		if a.Fail != b.Fail {
			return a.Fail > b.Fail
		}
		return a.Policy < b.Policy
	})

	cats := map[string]*CategoryRoll{}
	for _, c := range p.Controls {
		cr := cats[c.Category]
		if cr == nil {
			cr = &CategoryRoll{Category: c.Category}
			cats[c.Category] = cr
		}
		cr.Pass += c.Pass
		cr.Fail += c.Fail
		cr.Controls++
		if c.Fail > 0 {
			cr.FailingControls++
		}
	}
	for _, cr := range cats {
		p.Categories = append(p.Categories, *cr)
	}
	sort.SliceStable(p.Categories, func(i, j int) bool {
		if p.Categories[i].FailingControls != p.Categories[j].FailingControls {
			return p.Categories[i].FailingControls > p.Categories[j].FailingControls
		}
		return p.Categories[i].Category < p.Categories[j].Category
	})
	return p
}

// OverallCompliance is the estate-wide score on the applicable-only basis.
func (p Posture) OverallCompliance() float64 {
	n := p.Totals.Pass + p.Totals.Fail
	if n == 0 {
		return -1
	}
	return float64(p.Totals.Pass) / float64(n) * 100
}

// FailingControls is the count an auditor leads with: distinct controls with at
// least one failure, not the raw number of failing resources.
func (p Posture) FailingControls() int {
	n := 0
	for _, c := range p.Controls {
		if c.Fail > 0 {
			n++
		}
	}
	return n
}

func subject(r Result) string {
	parts := []string{}
	if r.Namespace != "" {
		parts = append(parts, r.Namespace)
	}
	id := r.Kind
	if r.Name != "" {
		if id != "" {
			id += "/"
		}
		id += r.Name
	}
	if id != "" {
		parts = append(parts, id)
	}
	if len(parts) == 0 {
		return "(cluster-scoped)"
	}
	return strings.Join(parts, "/")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

/* ─────────────── evidence rendering ─────────────── */

// Markdown renders the posture as an auditor-ready document.
//
// Markdown rather than a PDF or a bespoke format: it is diffable, reviewable in a
// pull request, renders in every ticketing system, and can be committed as the
// evidence artifact for a point in time. A binary report cannot be diffed against
// last quarter's, which is the question auditors actually ask.
func (p Posture) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Compliance posture — %s\n\n", firstNonEmpty(p.Cluster, "cluster"))
	fmt.Fprintf(&b, "Generated %s\n\n", p.GeneratedAt.UTC().Format(time.RFC3339))

	fmt.Fprintf(&b, "## Summary\n\n")
	fmt.Fprintf(&b, "| Metric | Value |\n|---|---|\n")
	fmt.Fprintf(&b, "| Overall compliance | %s |\n", pct(p.OverallCompliance()))
	fmt.Fprintf(&b, "| Controls evaluated | %d |\n", len(p.Controls))
	fmt.Fprintf(&b, "| Controls with failures | %d |\n", p.FailingControls())
	fmt.Fprintf(&b, "| Passing evaluations | %d |\n", p.Totals.Pass)
	fmt.Fprintf(&b, "| Failing evaluations | %d |\n", p.Totals.Fail)
	if p.Totals.Skip > 0 {
		fmt.Fprintf(&b, "| Skipped (not applicable) | %d |\n", p.Totals.Skip)
	}
	// State the basis explicitly: a score whose denominator is unstated is not
	// evidence, and "skips excluded" is the assumption most likely to be queried.
	fmt.Fprintf(&b, "\nCompliance is the share of *applicable* evaluations that passed; "+
		"skipped checks are excluded from the denominator because a control that did not "+
		"apply to a resource is not evidence that the resource complies.\n\n")

	fmt.Fprintf(&b, "## By category\n\n")
	fmt.Fprintf(&b, "| Category | Compliance | Controls | With failures |\n|---|---|---|---|\n")
	for _, c := range p.Categories {
		fmt.Fprintf(&b, "| %s | %s | %d | %d |\n", c.Category, pct(c.Compliance()), c.Controls, c.FailingControls)
	}

	fmt.Fprintf(&b, "\n## Findings\n\n")
	any := false
	for _, c := range p.Controls {
		if c.Fail == 0 {
			continue
		}
		any = true
		fmt.Fprintf(&b, "### %s\n\n", c.Policy)
		fmt.Fprintf(&b, "- Category: %s\n- Severity: %s\n- Source: %s\n- Failing: %d of %d applicable\n",
			c.Category, firstNonEmpty(c.Severity, "unspecified"), firstNonEmpty(c.Source, "unknown"),
			c.Fail, c.Evaluated())
		if len(c.Failing) > 0 {
			fmt.Fprintf(&b, "- Examples:\n")
			for _, f := range c.Failing {
				fmt.Fprintf(&b, "  - `%s`\n", f)
			}
			if c.Fail > len(c.Failing) {
				fmt.Fprintf(&b, "  - …and %d more\n", c.Fail-len(c.Failing))
			}
		}
		b.WriteString("\n")
	}
	if !any {
		b.WriteString("No control reported a failure at the time of generation.\n")
	}

	fmt.Fprintf(&b, "\n## Controls passing\n\n")
	passing := 0
	for _, c := range p.Controls {
		if c.Fail == 0 && c.Evaluated() > 0 {
			passing++
			fmt.Fprintf(&b, "- %s (%s, %d evaluations)\n", c.Policy, c.Category, c.Evaluated())
		}
	}
	if passing == 0 {
		b.WriteString("None.\n")
	}
	return b.String()
}

func pct(v float64) string {
	if v < 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", v)
}
