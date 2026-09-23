package compliance

import (
	"strings"
	"testing"
	"time"
)

func res(policy, outcome, sev, cat, ns, kind, name string) Result {
	return Result{Policy: policy, Outcome: outcome, Severity: sev, Category: cat,
		Source: "kyverno", Namespace: ns, Kind: kind, Name: name}
}

var when = time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)

func TestSkipsAreExcludedFromTheDenominator(t *testing.T) {
	// A control that did not APPLY to a resource is not evidence that the
	// resource complies. Counting skips as passes inflates every score, which is
	// precisely the number an auditor will probe.
	p := Aggregate([]Result{
		res("a", "pass", "high", "Pod Security", "ns", "Pod", "p1"),
		res("a", "skip", "high", "Pod Security", "ns", "Pod", "p2"),
		res("a", "fail", "high", "Pod Security", "ns", "Pod", "p3"),
	}, "c", when, 5)

	c := p.Controls[0]
	if c.Evaluated() != 2 {
		t.Fatalf("Evaluated = %d, want 2 (skip excluded)", c.Evaluated())
	}
	if got := c.Compliance(); got != 50 {
		t.Fatalf("Compliance = %v, want 50", got)
	}
	if p.Totals.Skip != 1 {
		t.Fatalf("skips must still be counted for transparency, got %d", p.Totals.Skip)
	}
}

func TestNothingApplicableIsNotZeroPercent(t *testing.T) {
	// -1 (n/a) vs 0 matters: rendering "0%" for a control that never applied
	// reads as total failure and sends people chasing a non-problem.
	p := Aggregate([]Result{res("a", "skip", "low", "X", "ns", "Pod", "p")}, "c", when, 5)
	if got := p.Controls[0].Compliance(); got != -1 {
		t.Fatalf("Compliance = %v, want -1 for 'not applicable'", got)
	}
	if got := p.OverallCompliance(); got != -1 {
		t.Fatalf("OverallCompliance = %v, want -1", got)
	}
	if !strings.Contains(p.Markdown(), "n/a") {
		t.Fatal("markdown should render n/a, not 0.0%")
	}
}

func TestFailingControlsCountsControlsNotResources(t *testing.T) {
	// One policy failing on 60 pods is ONE thing to fix. Reporting 60 findings
	// makes the report look 60x worse than the work required.
	var rs []Result
	for i := 0; i < 60; i++ {
		rs = append(rs, res("disallow-host-path", "fail", "high", "Pod Security", "ns", "Pod", "p"))
	}
	p := Aggregate(rs, "c", when, 3)
	if p.FailingControls() != 1 {
		t.Fatalf("FailingControls = %d, want 1", p.FailingControls())
	}
	if p.Totals.Fail != 60 {
		t.Fatalf("raw failures should still total 60, got %d", p.Totals.Fail)
	}
}

func TestEvidenceIsCappedButTheTrueCountIsKept(t *testing.T) {
	var rs []Result
	for i := 0; i < 10; i++ {
		rs = append(rs, res("p", "fail", "high", "C", "ns", "Pod", "pod"))
	}
	p := Aggregate(rs, "c", when, 3)
	c := p.Controls[0]
	if len(c.Failing) != 3 {
		t.Fatalf("evidence list = %d, want capped at 3", len(c.Failing))
	}
	if c.Fail != 10 {
		t.Fatalf("Fail = %d, want the true 10", c.Fail)
	}
	if !strings.Contains(p.Markdown(), "and 7 more") {
		t.Fatalf("markdown must disclose the truncation:\n%s", p.Markdown())
	}
}

func TestFindingsAreOrderedFailingThenBySeverity(t *testing.T) {
	p := Aggregate([]Result{
		res("clean", "pass", "critical", "C", "ns", "Pod", "a"),
		res("low-fail", "fail", "low", "C", "ns", "Pod", "b"),
		res("crit-fail", "fail", "critical", "C", "ns", "Pod", "c"),
		res("med-fail", "fail", "medium", "C", "ns", "Pod", "d"),
	}, "c", when, 5)

	got := []string{p.Controls[0].Policy, p.Controls[1].Policy, p.Controls[2].Policy, p.Controls[3].Policy}
	want := []string{"crit-fail", "med-fail", "low-fail", "clean"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v (failing first, then severity)", got, want)
		}
	}
}

func TestControlTakesTheHighestSeverityAcrossItsRules(t *testing.T) {
	// Rules within one policy can differ; reporting the lowest understates it.
	p := Aggregate([]Result{
		res("p", "fail", "low", "C", "ns", "Pod", "a"),
		res("p", "fail", "critical", "C", "ns", "Pod", "b"),
	}, "c", when, 5)
	if p.Controls[0].Severity != "critical" {
		t.Fatalf("severity = %q, want critical", p.Controls[0].Severity)
	}
}

func TestCategoryRollupCountsControlsWithFailures(t *testing.T) {
	p := Aggregate([]Result{
		res("a", "fail", "high", "Pod Security", "ns", "Pod", "x"),
		res("a", "fail", "high", "Pod Security", "ns", "Pod", "y"),
		res("b", "pass", "high", "Pod Security", "ns", "Pod", "z"),
		res("c", "pass", "high", "Supply Chain", "ns", "Pod", "w"),
	}, "c", when, 5)

	var ps *CategoryRoll
	for i := range p.Categories {
		if p.Categories[i].Category == "Pod Security" {
			ps = &p.Categories[i]
		}
	}
	if ps == nil {
		t.Fatal("Pod Security category missing")
	}
	if ps.Controls != 2 || ps.FailingControls != 1 {
		t.Fatalf("controls=%d failing=%d, want 2 and 1", ps.Controls, ps.FailingControls)
	}
	// Tolerance, not equality: Go folds the untyped constant 1.0/3.0*100 at
	// arbitrary precision and rounds once, while the runtime rounds twice, so an
	// exact comparison fails on a value that is correct.
	if got := ps.Compliance(); got < 33.3 || got > 33.4 {
		t.Fatalf("category compliance = %v, want ~33.3 (1 pass of 3 applicable)", got)
	}
}

func TestUncategorisedResultsAreNotDropped(t *testing.T) {
	// A policy with no category must still appear; silently dropping it would
	// hide a real finding from the report.
	p := Aggregate([]Result{res("p", "fail", "", "", "ns", "Pod", "a")}, "c", when, 5)
	if len(p.Controls) != 1 || p.Controls[0].Category != "Uncategorised" {
		t.Fatalf("controls = %+v", p.Controls)
	}
	if !strings.Contains(p.Markdown(), "Uncategorised") {
		t.Fatal("markdown should surface uncategorised controls")
	}
}

func TestClusterScopedSubjectIsLabelled(t *testing.T) {
	p := Aggregate([]Result{res("p", "fail", "high", "C", "", "", "")}, "c", when, 5)
	if got := p.Controls[0].Failing[0]; got != "(cluster-scoped)" {
		t.Fatalf("subject = %q", got)
	}
}

func TestMarkdownStatesTheBasisOfTheScore(t *testing.T) {
	// A score whose denominator is unstated is not evidence.
	p := Aggregate([]Result{
		res("a", "pass", "high", "C", "ns", "Pod", "x"),
		res("a", "skip", "high", "C", "ns", "Pod", "y"),
	}, "prod", when, 5)
	md := p.Markdown()
	for _, want := range []string{"prod", "2026-09-22T10:00:00Z", "applicable", "skipped"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
}

func TestEmptyInputProducesAnHonestReport(t *testing.T) {
	p := Aggregate(nil, "c", when, 5)
	md := p.Markdown()
	if !strings.Contains(md, "No control reported a failure") {
		t.Fatalf("empty posture should say so plainly:\n%s", md)
	}
	if p.FailingControls() != 0 {
		t.Fatal("no controls, no failures")
	}
}

func TestAggregateIsDeterministic(t *testing.T) {
	// An evidence artifact committed each quarter must diff cleanly; unstable
	// ordering would make every regeneration look like a change.
	in := []Result{
		res("b", "fail", "high", "C", "ns", "Pod", "1"),
		res("a", "fail", "high", "C", "ns", "Pod", "2"),
		res("c", "pass", "high", "C", "ns", "Pod", "3"),
	}
	first := Aggregate(in, "c", when, 5).Markdown()
	for i := 0; i < 5; i++ {
		if got := Aggregate(in, "c", when, 5).Markdown(); got != first {
			t.Fatal("markdown is not deterministic across runs")
		}
	}
}
