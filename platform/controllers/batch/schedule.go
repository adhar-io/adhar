/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"fmt"
	"math"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"adhar-io/adhar/platform/events"
)

// jobSpec is what the observer reads from a BatchJob XR. Every field is
// optional: the XRD gains `sla.finishBy` and `dependsOn` in Phase 3 (D3), and
// an older control plane simply never has them set.
type jobSpec struct {
	Name, Namespace, UID string
	Application          string
	Created              time.Time
	Cron, TimeZone       string
	Suspend              bool
	StartingDeadline     time.Duration
	// ScheduleRunning is false while the composition holds the schedule
	// suspended for a reason other than the declared one (prerequisites, a
	// scheduler switch): no slot can be "missed" then.
	ScheduleRunning bool
	Held            bool
	HoldReason      string
	HeldSince       string
	FinishBy        string
	SLATimeZone     string
	DependsOn       []dependency
	Obj             *unstructured.Unstructured
}

type dependency struct {
	BatchJob         string
	Namespace        string
	SameBusinessDate bool
}

func specOf(xr *unstructured.Unstructured) jobSpec {
	p, _, _ := unstructured.NestedMap(xr.Object, "spec", "parameters")
	str := func(m map[string]interface{}, keys ...string) string {
		v, _, _ := unstructured.NestedString(m, keys...)
		return v
	}
	s := jobSpec{
		Name: xr.GetName(), Namespace: xr.GetNamespace(), UID: string(xr.GetUID()),
		Application:      firstNonEmpty(str(p, "application"), xr.GetName()),
		Created:          xr.GetCreationTimestamp().Time,
		Cron:             str(p, "schedule", "cron"),
		TimeZone:         firstNonEmpty(str(p, "schedule", "timeZone"), "UTC"),
		StartingDeadline: time.Hour,
		ScheduleRunning:  true,
		FinishBy:         str(p, "sla", "finishBy"),
		SLATimeZone:      str(p, "sla", "timeZone"),
		Obj:              xr,
	}
	s.Suspend, _, _ = unstructured.NestedBool(p, "schedule", "suspend")
	if n, ok, _ := unstructured.NestedInt64(p, "schedule", "startingDeadlineSeconds"); ok && n > 0 {
		s.StartingDeadline = time.Duration(n) * time.Second
	}
	reason, held := xr.GetAnnotations()[annotationHold]
	s.Held, s.HoldReason = held, reason
	s.HeldSince, _, _ = unstructured.NestedString(xr.Object, "status", "held", "since")
	if ok, condReason, _, _ := condition(xr, "ScheduleReady"); !ok {
		conds, _, _ := unstructured.NestedSlice(xr.Object, "status", "conditions")
		for _, c := range conds {
			if m, isMap := c.(map[string]interface{}); isMap && m["type"] == "ScheduleReady" {
				s.ScheduleRunning = false
			}
		}
	} else if condReason == "Held" {
		s.ScheduleRunning = false
	}
	deps, _, _ := unstructured.NestedSlice(p, "dependsOn")
	for _, d := range deps {
		m, ok := d.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := m["batchJob"].(string)
		if name == "" {
			continue
		}
		ns, _ := m["namespace"].(string)
		same := true
		if v, present := m["sameBusinessDate"].(bool); present {
			same = v
		}
		s.DependsOn = append(s.DependsOn, dependency{BatchJob: name, Namespace: firstNonEmpty(ns, xr.GetNamespace()), SameBusinessDate: same})
	}
	return s
}

func (s jobSpec) slaZone() *time.Location { return zone(firstNonEmpty(s.SLATimeZone, s.TimeZone)) }

/* ─────────────────────────── the SLA (§11.4) ─────────────────────────── */

// missedScheduleGrace is how late a slot's run may appear before the slot is
// published as missed: the same ten minutes BatchScheduleMissed waits (§11.3),
// so the event and the alert agree about when a slot is lost.
const missedScheduleGrace = 10 * time.Minute

// recentWindow bounds how far back a missed slot or deadline is still news: a
// manager down for a weekend reports the latest of them, not each one.
const recentWindow = 26 * time.Hour

// p90Of is the nearest-rank 90th percentile, in seconds; false with no history.
func p90Of(durations []float64) (float64, bool) {
	if len(durations) == 0 {
		return 0, false
	}
	sorted := append([]float64(nil), durations...)
	sort.Float64s(sorted)
	rank := int(math.Ceil(0.9*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	return sorted[rank], true
}

// deadlineAfter is the first instant at or after `anchor` whose wall clock in
// loc reads finishBy (HH:MM). Built with time.Date per calendar day, so a
// daylight-saving change moves the deadline with the wall clock, never by an
// hour of arithmetic.
func deadlineAfter(anchor time.Time, finishBy string, loc *time.Location) (time.Time, bool) {
	var hh, mm int
	if _, err := fmt.Sscanf(finishBy, "%d:%d", &hh, &mm); err != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return time.Time{}, false
	}
	local := anchor.In(loc)
	for day := 0; day < 3; day++ {
		d := time.Date(local.Year(), local.Month(), local.Day()+day, hh, mm, 0, 0, loc)
		if !d.Before(anchor) {
			return d, true
		}
	}
	return time.Time{}, false
}

// window is one deadline and what is known about the work due by it.
type window struct {
	Deadline   time.Time
	Slot       time.Time // the schedule slot it belongs to, when there is one
	Succeeded  bool
	SuccessEnd time.Time
	Active     *observation
	HasRun     bool
	// BusinessDate is the date the window's work is for: its run's, else the
	// slot's date in the job's zone — the runner's own rule for a scheduled
	// run (§5.5).
	BusinessDate string
}

// scheduleOutcome is what one evaluation of a job's schedule and SLA says.
type scheduleOutcome struct {
	Transitions []jobTransition
	// Deadlines are the deadlines in play, for the two SLA gauges the Mimir
	// ruler reads (observability/mimir/manifests/batch-app-alerts.yaml): every
	// recent deadline that passed with no success for its business date, and
	// the one being forecast. Empty when the job has no deadline in play.
	Deadlines []deadlineGauge
	// RequeueAfter is when the evaluation can next change by the clock alone.
	RequeueAfter time.Duration
}

// deadlineGauge is one business date's deadline, as the gauges publish it.
type deadlineGauge struct {
	BusinessDate string
	Deadline     time.Time
	// Remaining is adhar_batch_deadline_seconds_remaining: the deadline less
	// now, negative once it has passed.
	Remaining float64
	// Slack is adhar_batch_deadline_projected_slack_seconds: the deadline less
	// the expected end, negative when the run is not expected to make it. Nil
	// when nothing is expected — no history to take a p90 from, or a deadline
	// that has already passed, where Remaining says it all.
	Slack *float64
}

type jobTransition struct {
	Type        string
	Slot        time.Time
	Deadline    time.Time
	ExpectedEnd time.Time
	P90         float64
	HasP90      bool
	Run         *observation
	HoldReason  string
	HeldSince   string
}

// slotOf is the schedule slot a run was started for: the CronJob controller's
// own annotation, else (a Volcano CronJob writes none) a scheduled run created
// within the starting deadline of a slot.
func slotOf(o observation, cron *cronSchedule, startingDeadline time.Duration) time.Time {
	if at := parseTime(o.ScheduledAt); !at.IsZero() {
		return at
	}
	if cron == nil || o.Trigger != TriggerSchedule {
		return time.Time{}
	}
	if p, ok := cron.prev(o.Created); ok && o.Created.Sub(p) <= startingDeadline {
		return p
	}
	return time.Time{}
}

// evaluateSchedule is the job-level half of the observer: the hold, missed
// slots and the `finishBy` SLA. It is pure — the clock, the runs and the record
// are inputs — so A3.4 is a table test, not a wait.
func evaluateSchedule(spec jobSpec, runs []observation, state *jobState, baseline bool, now time.Time) scheduleOutcome {
	var out scheduleOutcome
	requeue := 5 * time.Minute
	soon := func(at time.Time) {
		if d := at.Sub(now); d > 0 && d < requeue {
			requeue = d
		}
	}

	// The hold (§3.4): one annotation, published as it appears and as it goes.
	switch {
	case spec.Held && state.Held == nil:
		state.Held = &heldState{Reason: spec.HoldReason, Since: firstNonEmpty(spec.HeldSince, stamp(now))}
		if !baseline {
			out.Transitions = append(out.Transitions, jobTransition{Type: events.TypeBatchScheduleHeld, HoldReason: spec.HoldReason, HeldSince: state.Held.Since})
		}
	case !spec.Held && state.Held != nil:
		since := state.Held.Since
		state.Held = nil
		out.Transitions = append(out.Transitions, jobTransition{Type: events.TypeBatchScheduleReleased, HeldSince: since})
	}

	var cron *cronSchedule
	if spec.Cron != "" {
		cron, _ = parseCron(spec.Cron, zone(spec.TimeZone))
	}

	// Missed slots: the latest slot that should have produced a run by now.
	if cron != nil && !spec.Suspend && !spec.Held && spec.ScheduleRunning {
		if slot, ok := cron.prev(now.Add(-missedScheduleGrace)); ok && slot.After(spec.Created) && now.Sub(slot) <= recentWindow {
			key := stamp(slot)
			if !state.missed(key) && !slotHasRun(slot, runs, state, cron, spec.StartingDeadline) {
				state.Missed = append(state.Missed, key)
				out.Transitions = append(out.Transitions, jobTransition{Type: events.TypeBatchScheduleMissed, Slot: slot})
			}
		}
		if n, ok := cron.next(now); ok {
			soon(n.Add(missedScheduleGrace + time.Second))
		}
	}

	if spec.FinishBy == "" {
		if baseline {
			out.Transitions = nil
		}
		out.RequeueAfter = requeue
		return out
	}
	loc := spec.slaZone()
	p90, hasP90 := p90Of(state.durations(20))

	// The windows in play: every recent run's deadline, plus the slot that
	// just passed and the next one, which may have no run yet.
	windows := map[string]*window{}
	add := func(anchor, slot time.Time) *window {
		d, ok := deadlineAfter(anchor, spec.FinishBy, loc)
		if !ok {
			return nil
		}
		key := stamp(d)
		w := windows[key]
		if w == nil {
			w = &window{Deadline: d, Slot: slot}
			windows[key] = w
		}
		if w.Slot.IsZero() {
			w.Slot = slot
		}
		return w
	}
	for i := range runs {
		o := &runs[i]
		slot := slotOf(*o, cron, spec.StartingDeadline)
		anchor := o.Created
		if !slot.IsZero() {
			anchor = slot
		}
		if now.Sub(anchor) > recentWindow {
			continue
		}
		w := add(anchor, slot)
		if w == nil {
			continue
		}
		w.HasRun = true
		if w.BusinessDate == "" || !o.Phase.terminal() {
			w.BusinessDate = firstNonEmpty(o.BusinessDate, w.BusinessDate)
		}
		switch {
		case o.Phase == phaseSucceeded:
			w.Succeeded = true
			if o.Finished.After(w.SuccessEnd) {
				w.SuccessEnd = o.Finished
			}
		case !o.Phase.terminal():
			if w.Active == nil || o.Created.After(w.Active.Created) {
				w.Active = o
			}
		}
	}
	if cron != nil && !spec.Suspend && !spec.Held {
		if p, ok := cron.prev(now); ok && p.After(spec.Created) {
			add(p, p)
		}
		if n, ok := cron.next(now); ok && n.Sub(now) <= 24*time.Hour {
			add(n, n)
		}
	}

	keys := make([]string, 0, len(windows))
	for k := range windows {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if state.Deadlines == nil {
		state.Deadlines = map[string]string{}
	}
	forecast := false
	for _, k := range keys {
		w := windows[k]
		if w.Succeeded {
			continue
		}
		bd := w.BusinessDate
		switch {
		case bd != "":
		case !w.Slot.IsZero():
			bd = dateInZone(w.Slot, spec.TimeZone)
		default:
			bd = dateInZone(w.Deadline, firstNonEmpty(spec.SLATimeZone, spec.TimeZone))
		}
		said := state.Deadlines[k]
		if !now.Before(w.Deadline) {
			recent := w.Deadline.After(spec.Created) && now.Sub(w.Deadline) <= recentWindow
			if said != "missed" && recent {
				state.Deadlines[k] = "missed"
				out.Transitions = append(out.Transitions, jobTransition{Type: events.TypeBatchDeadlineMissed, Slot: w.Slot, Deadline: w.Deadline, P90: p90, HasP90: hasP90, Run: w.Active})
			}
			// The gauge is state, not a transition: a deadline the first look
			// records without publishing is still missed, and still alerts.
			if recent {
				out.Deadlines = append(out.Deadlines, deadlineGauge{BusinessDate: bd, Deadline: w.Deadline, Remaining: w.Deadline.Sub(now).Seconds()})
			}
			// A run still going past its deadline keeps the next window from
			// being forecast: it is still tonight's work.
			if w.Active != nil {
				forecast = true
			}
			continue
		}
		soon(w.Deadline.Add(time.Second))
		// Only the earliest unfinished deadline is forecast: tomorrow's
		// window is not news while tonight's is still open, and a job that is
		// chronically too long would otherwise warn about every night at once.
		if forecast {
			continue
		}
		forecast = true
		// Expected end = start (or the slot, for work not started) + p90 of
		// the last 20 durations (§11.4). Work that has not started cannot
		// start before now.
		start := now
		switch {
		case w.Active != nil && !w.Active.Started.IsZero():
			start = w.Active.Started
		case w.Active == nil && !w.HasRun && w.Slot.After(now):
			start = w.Slot
		}
		g := deadlineGauge{BusinessDate: bd, Deadline: w.Deadline, Remaining: w.Deadline.Sub(now).Seconds()}
		if hasP90 {
			expected := start.Add(time.Duration(p90 * float64(time.Second)))
			slack := w.Deadline.Sub(expected).Seconds()
			g.Slack = &slack
			if slack < 0 && said == "" {
				state.Deadlines[k] = "endangered"
				out.Transitions = append(out.Transitions, jobTransition{Type: events.TypeBatchDeadlineEndangered, Slot: w.Slot, Deadline: w.Deadline, ExpectedEnd: expected, P90: p90, HasP90: true, Run: w.Active})
			}
			if start.Equal(now) && slack >= 0 {
				// Waiting work becomes endangered when the clock alone
				// pushes its expected end past the deadline.
				soon(now.Add(time.Duration(slack*float64(time.Second)) + time.Second))
			}
		}
		out.Deadlines = append(out.Deadlines, g)
	}
	if baseline {
		// The first look at a job records what is already past without
		// publishing it: a slot or deadline missed before the observer knew the
		// job is history, and its runs may be gone without a record.
		kept := out.Transitions[:0]
		for _, t := range out.Transitions {
			if t.Type != events.TypeBatchScheduleMissed && t.Type != events.TypeBatchDeadlineMissed {
				kept = append(kept, t)
			}
		}
		out.Transitions = kept
	}
	if requeue < 10*time.Second {
		requeue = 10 * time.Second
	}
	out.RequeueAfter = requeue
	return out
}

// slotHasRun says the slot produced a run: one observed now, or one recorded
// before it was deleted.
func slotHasRun(slot time.Time, runs []observation, state *jobState, cron *cronSchedule, startingDeadline time.Duration) bool {
	for _, o := range runs {
		if s := slotOf(o, cron, startingDeadline); !s.IsZero() && s.Equal(slot) {
			return true
		}
	}
	key := stamp(slot)
	for _, r := range state.Runs {
		if r.Scheduled == key {
			return true
		}
	}
	return false
}
