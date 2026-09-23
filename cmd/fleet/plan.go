/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package fleet

// Wave planning and health gating for a fleet-wide upgrade.
//
// This file is deliberately pure: it takes a snapshot of the fleet and returns
// what to touch next, with no Kubernetes client anywhere. A fleet upgrade is the
// single most destructive thing this CLI can do — it changes every workload
// cluster an organisation runs — so the decisions about ORDER, how far to go
// before checking, and when to stop have to be testable without a cluster.
//
// The model is the one Argo Rollouts uses for a Deployment, applied to clusters:
// a canary first, then progressively larger waves, with a health gate between
// each. What differs is the blast radius. A bad canary pod affects a fraction of
// one service's traffic; a bad canary CLUSTER affects everything running on it.
// So the gate here fails closed and the default first wave is exactly one plane.

import (
	"fmt"
	"sort"
	"strings"
)

// Plane is one workload cluster as the planner sees it.
type Plane struct {
	Name string
	Mode string
	// Ready is the DataPlane's Ready condition at snapshot time.
	Ready bool
	// Version is the Kubernetes version the plane reports.
	Version string
	// Apps is how many Argo CD Applications are placed on it.
	Apps int
	// Labels come from spec.placement.labels — used by --selector.
	Labels map[string]string
}

// Wave is one batch of planes upgraded together, then gated.
type Wave struct {
	// Index is 1-based; wave 1 is the canary.
	Index  int
	Planes []Plane
}

// PlanOptions controls how the fleet is divided.
type PlanOptions struct {
	// Canary is how many planes go first. 0 means the default of 1.
	Canary int
	// BatchSize is how many planes per wave after the canary. 0 means the
	// default of 25% of the fleet, at least 1.
	BatchSize int
	// Selector restricts the fleet to planes carrying every one of these
	// labels. An empty selector means the whole fleet.
	Selector map[string]string
	// IncludeNotReady upgrades planes that are already unhealthy. Off by
	// default: rolling a change onto a broken cluster makes the failure
	// harder to attribute and can turn a recoverable plane into a lost one.
	IncludeNotReady bool
}

// Plan divides the fleet into ordered waves.
//
// Ordering is by ASCENDING app count, so the canary is the least-loaded plane
// that still exercises the change. Alphabetical order was the obvious
// alternative and is worse: it makes the canary an accident of naming, and on a
// fleet where one cluster carries production it is a coin flip whether that
// cluster goes first.
func Plan(planes []Plane, opts PlanOptions) ([]Wave, []Plane, error) {
	eligible, skipped := partition(planes, opts)
	if len(eligible) == 0 {
		return nil, skipped, nil
	}

	// Least loaded first; ties broken by name so a plan is reproducible.
	sort.SliceStable(eligible, func(i, j int) bool {
		if eligible[i].Apps != eligible[j].Apps {
			return eligible[i].Apps < eligible[j].Apps
		}
		return eligible[i].Name < eligible[j].Name
	})

	canary := opts.Canary
	if canary <= 0 {
		canary = 1
	}
	if canary > len(eligible) {
		canary = len(eligible)
	}

	batch := opts.BatchSize
	if batch <= 0 {
		// A quarter of the fleet, so a fleet of four upgrades in canary + 3
		// waves and a fleet of forty in canary + 4. Never zero.
		batch = (len(eligible) + 3) / 4
		if batch < 1 {
			batch = 1
		}
	}

	waves := []Wave{{Index: 1, Planes: eligible[:canary]}}
	for i := canary; i < len(eligible); i += batch {
		end := i + batch
		if end > len(eligible) {
			end = len(eligible)
		}
		waves = append(waves, Wave{Index: len(waves) + 1, Planes: eligible[i:end]})
	}
	return waves, skipped, nil
}

func partition(planes []Plane, opts PlanOptions) (eligible, skipped []Plane) {
	for _, p := range planes {
		if !matches(p.Labels, opts.Selector) {
			continue // not in scope at all; not "skipped", just not selected
		}
		if !p.Ready && !opts.IncludeNotReady {
			skipped = append(skipped, p)
			continue
		}
		eligible = append(eligible, p)
	}
	return eligible, skipped
}

func matches(labels, selector map[string]string) bool {
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}

/* ─────────────── health gating ─────────────── */

// Gate is the verdict after a wave.
type Gate struct {
	// Healthy is true when every plane in the wave came back Ready.
	Healthy bool
	// Regressed names planes that were Ready before the wave and are not now.
	// These are what a rollback has to undo.
	Regressed []string
	// StillConverging names planes that are not Ready but were not Ready
	// before either — they were already broken, so the wave did not break them.
	StillConverging []string
}

// Evaluate compares the fleet before and after a wave.
//
// A REGRESSION is the only thing that stops the rollout: a plane that was Ready
// and is not any more. A plane that was already unhealthy staying unhealthy is
// not evidence about this change, and treating it as such would mean one
// long-broken cluster permanently blocks every future fleet upgrade — which is
// how teams end up running upgrades with the gate disabled.
func Evaluate(before, after []Plane, wave Wave) Gate {
	prev := map[string]bool{}
	for _, p := range before {
		prev[p.Name] = p.Ready
	}
	now := map[string]bool{}
	for _, p := range after {
		now[p.Name] = p.Ready
	}

	g := Gate{Healthy: true}
	for _, p := range wave.Planes {
		ready, present := now[p.Name]
		switch {
		case !present:
			// The plane vanished mid-upgrade. That is worse than unhealthy.
			g.Regressed = append(g.Regressed, p.Name)
			g.Healthy = false
		case ready:
			// fine
		case prev[p.Name]:
			g.Regressed = append(g.Regressed, p.Name)
			g.Healthy = false
		default:
			g.StillConverging = append(g.StillConverging, p.Name)
		}
	}
	sort.Strings(g.Regressed)
	sort.Strings(g.StillConverging)
	return g
}

/* ─────────────── rendering ─────────────── */

// Describe renders a plan as the confirmation text shown before anything runs.
// A fleet upgrade should be readable in full before it is agreed to.
func Describe(waves []Wave, skipped []Plane) string {
	var b strings.Builder
	total := 0
	for _, w := range waves {
		total += len(w.Planes)
	}
	fmt.Fprintf(&b, "%d plane(s) in %d wave(s):\n", total, len(waves))
	for _, w := range waves {
		label := fmt.Sprintf("wave %d", w.Index)
		if w.Index == 1 {
			label += " (canary)"
		}
		names := make([]string, 0, len(w.Planes))
		for _, p := range w.Planes {
			names = append(names, fmt.Sprintf("%s[%d apps]", p.Name, p.Apps))
		}
		fmt.Fprintf(&b, "  %-16s %s\n", label, strings.Join(names, ", "))
	}
	if len(skipped) > 0 {
		names := make([]string, 0, len(skipped))
		for _, p := range skipped {
			names = append(names, p.Name)
		}
		sort.Strings(names)
		fmt.Fprintf(&b, "  skipped (not Ready): %s\n", strings.Join(names, ", "))
		b.WriteString("  pass --include-not-ready to upgrade them anyway\n")
	}
	return b.String()
}

// SkewedPlanes reports planes whose Kubernetes version differs from the hub's.
//
// Drift is not an error — a fleet mid-upgrade is skewed by definition — but it
// is the thing an operator most often wants to see, because a plane left behind
// by a failed wave looks identical to one nobody has upgraded yet.
func SkewedPlanes(planes []Plane, hubVersion string) []Plane {
	if hubVersion == "" {
		return nil
	}
	want := minorOf(hubVersion)
	if want == "" {
		return nil
	}
	var out []Plane
	for _, p := range planes {
		if m := minorOf(p.Version); m != "" && m != want {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// minorOf reduces "v1.31.4+do.2" to "1.31" — the granularity Kubernetes's own
// skew policy is written in. Comparing full versions would report a fleet as
// drifted over a patch release that is explicitly supported.
func minorOf(v string) string {
	s := strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.SplitN(s, ".", 3)
	if len(parts) < 2 {
		return ""
	}
	major, minor := parts[0], parts[1]
	if major == "" || minor == "" {
		return ""
	}
	// Guard against "1.x" style slugs by requiring digits.
	for _, p := range []string{major, minor} {
		for _, c := range p {
			if c < '0' || c > '9' {
				return ""
			}
		}
	}
	return major + "." + minor
}
