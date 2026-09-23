package fleet

import (
	"strings"
	"testing"
)

func p(name string, ready bool, apps int, version string, labels ...string) Plane {
	l := map[string]string{}
	for i := 0; i+1 < len(labels); i += 2 {
		l[labels[i]] = labels[i+1]
	}
	return Plane{Name: name, Ready: ready, Apps: apps, Version: version, Labels: l}
}

func waveNames(w Wave) []string {
	out := make([]string, 0, len(w.Planes))
	for _, x := range w.Planes {
		out = append(out, x.Name)
	}
	return out
}

func TestCanaryIsTheLeastLoadedPlane(t *testing.T) {
	// Ordering by app count, not by name: on a fleet where one cluster carries
	// production, alphabetical order makes it a coin flip whether that cluster
	// is the canary.
	waves, _, err := Plan([]Plane{
		p("alpha", true, 40, "v1.31.0"),
		p("bravo", true, 2, "v1.31.0"),
		p("charlie", true, 9, "v1.31.0"),
	}, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := waveNames(waves[0]); len(got) != 1 || got[0] != "bravo" {
		t.Fatalf("canary = %v, want [bravo] (fewest apps)", got)
	}
}

func TestPlanIsReproducibleWhenAppCountsTie(t *testing.T) {
	for i := 0; i < 5; i++ {
		waves, _, _ := Plan([]Plane{
			p("z", true, 3, "v1.31.0"), p("a", true, 3, "v1.31.0"), p("m", true, 3, "v1.31.0"),
		}, PlanOptions{})
		if got := waveNames(waves[0]); got[0] != "a" {
			t.Fatalf("run %d: canary = %v, want a (name breaks the tie)", i, got)
		}
	}
}

func TestNotReadyPlanesAreSkippedByDefault(t *testing.T) {
	// Rolling a change onto an already-broken cluster makes the failure
	// impossible to attribute.
	waves, skipped, _ := Plan([]Plane{
		p("ok", true, 1, "v1.31.0"),
		p("broken", false, 1, "v1.31.0"),
	}, PlanOptions{})
	if len(skipped) != 1 || skipped[0].Name != "broken" {
		t.Fatalf("skipped = %v, want [broken]", skipped)
	}
	for _, w := range waves {
		for _, pl := range w.Planes {
			if pl.Name == "broken" {
				t.Fatal("a not-Ready plane was scheduled without --include-not-ready")
			}
		}
	}

	waves2, skipped2, _ := Plan([]Plane{
		p("ok", true, 1, "v1.31.0"),
		p("broken", false, 1, "v1.31.0"),
	}, PlanOptions{IncludeNotReady: true})
	if len(skipped2) != 0 {
		t.Fatalf("with --include-not-ready nothing should be skipped, got %v", skipped2)
	}
	total := 0
	for _, w := range waves2 {
		total += len(w.Planes)
	}
	if total != 2 {
		t.Fatalf("expected both planes scheduled, got %d", total)
	}
}

func TestSelectorRestrictsScopeWithoutReportingSkips(t *testing.T) {
	// A plane outside the selector is not "skipped" — it was never in scope, and
	// reporting it would make every selected run look like it had failures.
	waves, skipped, _ := Plan([]Plane{
		p("eu-1", true, 1, "v1.31.0", "region", "eu"),
		p("us-1", true, 1, "v1.31.0", "region", "us"),
	}, PlanOptions{Selector: map[string]string{"region": "eu"}})
	if len(skipped) != 0 {
		t.Fatalf("out-of-scope planes must not be reported as skipped, got %v", skipped)
	}
	total := 0
	for _, w := range waves {
		total += len(w.Planes)
		for _, pl := range w.Planes {
			if pl.Name != "eu-1" {
				t.Fatalf("selector leaked %s", pl.Name)
			}
		}
	}
	if total != 1 {
		t.Fatalf("expected 1 selected plane, got %d", total)
	}
}

func TestDefaultBatchIsAQuarterOfTheFleet(t *testing.T) {
	planes := make([]Plane, 0, 9)
	for i := 0; i < 9; i++ {
		planes = append(planes, p(string(rune('a'+i)), true, i, "v1.31.0"))
	}
	waves, _, _ := Plan(planes, PlanOptions{})
	// 9 planes: canary 1, then batches of ceil(9/4)=3 → 1,3,3,2
	got := []int{}
	for _, w := range waves {
		got = append(got, len(w.Planes))
	}
	want := []int{1, 3, 3, 2}
	if len(got) != len(want) {
		t.Fatalf("wave sizes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("wave sizes = %v, want %v", got, want)
		}
	}
}

func TestEveryPlaneIsScheduledExactlyOnce(t *testing.T) {
	planes := make([]Plane, 0, 17)
	for i := 0; i < 17; i++ {
		planes = append(planes, p("p"+string(rune('a'+i)), true, i%5, "v1.31.0"))
	}
	waves, _, _ := Plan(planes, PlanOptions{Canary: 2, BatchSize: 4})
	seen := map[string]int{}
	for _, w := range waves {
		for _, pl := range w.Planes {
			seen[pl.Name]++
		}
	}
	if len(seen) != 17 {
		t.Fatalf("scheduled %d distinct planes, want 17", len(seen))
	}
	for n, c := range seen {
		if c != 1 {
			t.Fatalf("%s scheduled %d times", n, c)
		}
	}
}

func TestCanaryLargerThanFleetDoesNotPanic(t *testing.T) {
	waves, _, _ := Plan([]Plane{p("only", true, 1, "v1.31.0")}, PlanOptions{Canary: 10})
	if len(waves) != 1 || len(waves[0].Planes) != 1 {
		t.Fatalf("waves = %v", waves)
	}
}

func TestEmptyFleetPlansNothing(t *testing.T) {
	waves, skipped, err := Plan(nil, PlanOptions{})
	if err != nil || len(waves) != 0 || len(skipped) != 0 {
		t.Fatalf("waves=%v skipped=%v err=%v", waves, skipped, err)
	}
}

/* ─────────── gating ─────────── */

func TestGateStopsOnlyOnARegression(t *testing.T) {
	before := []Plane{p("a", true, 1, "v1"), p("b", false, 1, "v1")}
	wave := Wave{Index: 1, Planes: []Plane{p("a", true, 1, "v1"), p("b", false, 1, "v1")}}

	// `a` broke → regression. `b` was already broken → not this change's fault.
	after := []Plane{p("a", false, 1, "v1"), p("b", false, 1, "v1")}
	g := Evaluate(before, after, wave)
	if g.Healthy {
		t.Fatal("a plane that was Ready and is not should fail the gate")
	}
	if len(g.Regressed) != 1 || g.Regressed[0] != "a" {
		t.Fatalf("Regressed = %v, want [a]", g.Regressed)
	}
	if len(g.StillConverging) != 1 || g.StillConverging[0] != "b" {
		t.Fatalf("StillConverging = %v, want [b]", g.StillConverging)
	}
}

func TestAlreadyBrokenPlaneDoesNotBlockTheFleetForever(t *testing.T) {
	// The regression this guards: gating on "all Ready" means one long-broken
	// cluster blocks every future upgrade, and teams respond by disabling the gate.
	before := []Plane{p("healthy", true, 1, "v1"), p("longBroken", false, 1, "v1")}
	after := []Plane{p("healthy", true, 1, "v1"), p("longBroken", false, 1, "v1")}
	g := Evaluate(before, after, Wave{Planes: before})
	if !g.Healthy {
		t.Fatalf("gate should pass; Regressed=%v", g.Regressed)
	}
}

func TestVanishedPlaneFailsTheGate(t *testing.T) {
	before := []Plane{p("gone", true, 1, "v1")}
	g := Evaluate(before, nil, Wave{Planes: before})
	if g.Healthy || len(g.Regressed) != 1 || g.Regressed[0] != "gone" {
		t.Fatalf("a plane disappearing mid-upgrade must fail the gate; got %+v", g)
	}
}

/* ─────────── drift ─────────── */

func TestSkewComparesMinorNotPatch(t *testing.T) {
	planes := []Plane{
		p("patch-behind", true, 1, "v1.31.2"),   // same minor → not drift
		p("minor-behind", true, 1, "v1.30.9"),   // drift
		p("cloud-slug", true, 1, "1.31.4+do.2"), // same minor, vendor suffix → not drift
		p("unknown", true, 1, ""),               // unparseable → ignored, not guessed
	}
	got := SkewedPlanes(planes, "v1.31.0")
	if len(got) != 1 || got[0].Name != "minor-behind" {
		names := []string{}
		for _, x := range got {
			names = append(names, x.Name)
		}
		t.Fatalf("skewed = %v, want [minor-behind]", names)
	}
}

func TestSkewWithNoHubVersionReportsNothing(t *testing.T) {
	if got := SkewedPlanes([]Plane{p("a", true, 1, "v1.30.0")}, ""); got != nil {
		t.Fatalf("without a hub version there is nothing to compare; got %v", got)
	}
}

func TestDescribeNamesTheCanaryAndTheSkips(t *testing.T) {
	waves, skipped, _ := Plan([]Plane{
		p("small", true, 1, "v1"), p("big", true, 50, "v1"), p("down", false, 3, "v1"),
	}, PlanOptions{})
	out := Describe(waves, skipped)
	for _, want := range []string{"canary", "small", "big", "down", "--include-not-ready"} {
		if !strings.Contains(out, want) {
			t.Fatalf("Describe() missing %q:\n%s", want, out)
		}
	}
}
