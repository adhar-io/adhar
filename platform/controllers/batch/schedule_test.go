package batch

import (
	"testing"
	"time"

	"adhar-io/adhar/platform/events"
)

func sgt() *time.Location { return zone("Asia/Singapore") }

func at(hhmm string, day int) time.Time {
	t, _ := time.ParseInLocation("2006-01-02 15:04", "2026-11-"+twoDigits(day)+" "+hhmm, sgt())
	return t
}

func twoDigits(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

func nightly() jobSpec {
	return jobSpec{
		Name: "importer", Namespace: "prod", UID: "xr-1", Application: "importer",
		Created: at("09:00", 3), Cron: "30 1 * * *", TimeZone: "Asia/Singapore",
		StartingDeadline: time.Hour, ScheduleRunning: true, FinishBy: "02:00",
	}
}

func withDurations(minutes ...int) *jobState {
	s := &jobState{V: stateVersion}
	for i, m := range minutes {
		s.Runs = append(s.Runs, runRecord{UID: "h" + twoDigits(i), End: endSucceeded, Gone: true,
			Seconds: float64(m * 60), Finished: stamp(at("01:00", 1).Add(time.Duration(i) * time.Hour))})
	}
	return s
}

func typesOf(ts []jobTransition) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Type)
	}
	return out
}

// A3.4: a job whose expected end crosses finishBy publishes
// deadline.endangered BEFORE the deadline — here, the moment its run starts
// at 01:30 with a p90 of 45 minutes against a 02:00 deadline — and only once.
func TestAnExpectedEndPastFinishByIsEndangeredBeforeTheDeadline(t *testing.T) {
	spec := nightly()
	state := withDurations(20, 25, 30, 45, 45, 40, 30, 25, 20, 45)
	run := observation{UID: "r1", Name: "importer-x", Trigger: TriggerSchedule, Phase: phaseRunning,
		ScheduledAt: stamp(at("01:30", 4)), Created: at("01:30", 4), Started: at("01:31", 4)}
	now := at("01:32", 4)
	out := evaluateSchedule(spec, []observation{run}, state, false, now)
	if got := typesOf(out.Transitions); len(got) != 1 || got[0] != events.TypeBatchDeadlineEndangered {
		t.Fatalf("published %v, want deadline.endangered", got)
	}
	e := out.Transitions[0]
	if !e.Deadline.Equal(at("02:00", 4)) || !e.ExpectedEnd.Equal(at("02:16", 4)) || e.P90 != 45*60 {
		t.Errorf("deadline %s, expected end %s, p90 %v", e.Deadline, e.ExpectedEnd, e.P90)
	}
	if !now.Before(e.Deadline) {
		t.Error("endangered must be published before the deadline")
	}
	if len(out.Deadlines) != 1 || out.Deadlines[0].Slack == nil || *out.Deadlines[0].Slack != -16*60 || out.Deadlines[0].Remaining != 28*60 {
		t.Errorf("deadline gauges = %+v, want projected slack -960 and 1680 s remaining", out.Deadlines)
	}
	if again := evaluateSchedule(spec, []observation{run}, state, false, now.Add(time.Minute)); len(again.Transitions) != 0 {
		t.Errorf("published again: %v", typesOf(again.Transitions))
	}

	// The deadline passes without a success: deadline.missed, once.
	late := evaluateSchedule(spec, []observation{run}, state, false, at("02:00", 4).Add(time.Second))
	if got := typesOf(late.Transitions); len(got) != 1 || got[0] != events.TypeBatchDeadlineMissed {
		t.Errorf("after the deadline: %v, want deadline.missed", got)
	}
	if again := evaluateSchedule(spec, []observation{run}, state, false, at("02:05", 4)); len(again.Transitions) != 0 {
		t.Errorf("missed published again: %v", typesOf(again.Transitions))
	}
}

// A run that is on track publishes nothing; one that succeeds in time never
// becomes missed.
func TestAnOnTrackRunPublishesNoDeadlineEvent(t *testing.T) {
	spec := nightly()
	state := withDurations(10, 12, 15)
	run := observation{UID: "r1", Trigger: TriggerSchedule, Phase: phaseRunning, ScheduledAt: stamp(at("01:30", 4)),
		Created: at("01:30", 4), Started: at("01:30", 4)}
	if out := evaluateSchedule(spec, []observation{run}, state, false, at("01:35", 4)); len(out.Transitions) != 0 {
		t.Errorf("on track: published %v", typesOf(out.Transitions))
	}
	run.Phase, run.Finished = phaseSucceeded, at("01:44", 4)
	if out := evaluateSchedule(spec, []observation{run}, state, false, at("02:30", 4)); len(out.Transitions) != 0 {
		t.Errorf("succeeded in time: published %v", typesOf(out.Transitions))
	}
}

// The SLA warns ahead of the slot too: a job whose p90 alone is longer than
// the time between its slot and its deadline is endangered before it starts,
// which is the Console's Tonight view (§7.2) arriving as an event.
func TestAJobThatCannotFitItsWindowIsEndangeredBeforeItStarts(t *testing.T) {
	spec := nightly()
	state := withDurations(40, 45, 50)
	tonight := observation{UID: "r1", Trigger: TriggerSchedule, Phase: phaseSucceeded, ScheduledAt: stamp(at("01:30", 4)),
		Created: at("01:30", 4), Started: at("01:30", 4), Finished: at("01:58", 4)}
	out := evaluateSchedule(spec, []observation{tonight}, state, false, at("20:00", 4))
	got := typesOf(out.Transitions)
	if len(got) != 1 || got[0] != events.TypeBatchDeadlineEndangered || !out.Transitions[0].Deadline.Equal(at("02:00", 5)) {
		t.Errorf("published %v for %v, want deadline.endangered for 02:00 tomorrow", got, out.Transitions)
	}
}

// The deadline is a wall-clock time in the job's zone, so it moves with
// daylight saving rather than by an hour of arithmetic (§11.4).
func TestTheDeadlineFollowsTheWallClockAcrossDaylightSaving(t *testing.T) {
	ny := zone("America/New_York")
	// 2026-11-01 is the US fall-back day.
	before := time.Date(2026, 10, 31, 23, 0, 0, 0, ny)
	d, ok := deadlineAfter(before, "06:00", ny)
	if !ok || d.In(ny).Hour() != 6 || d.In(ny).Day() != 1 {
		t.Errorf("deadline = %s, want 06:00 local on Nov 1", d.In(ny))
	}
	if got := d.Sub(before); got != 8*time.Hour {
		t.Errorf("23:00 to 06:00 across the fall-back is 8h of elapsed time, got %s", got)
	}
}

// A slot that passed with no run is published as schedule.missed, once; a
// slot whose run exists — or existed and was cleaned up — is not.
func TestAScheduleSlotWithNoRunIsMissedOnce(t *testing.T) {
	spec := nightly()
	spec.FinishBy = ""
	state := &jobState{V: stateVersion}
	now := at("01:41", 4)
	out := evaluateSchedule(spec, nil, state, false, now)
	if got := typesOf(out.Transitions); len(got) != 1 || got[0] != events.TypeBatchScheduleMissed || !out.Transitions[0].Slot.Equal(at("01:30", 4)) {
		t.Fatalf("published %v, want schedule.missed for 01:30", got)
	}
	if again := evaluateSchedule(spec, nil, state, false, now.Add(time.Minute)); len(again.Transitions) != 0 {
		t.Errorf("published again: %v", typesOf(again.Transitions))
	}
	// Within the grace the CronJob may still start it.
	if early := evaluateSchedule(spec, nil, &jobState{V: stateVersion}, false, at("01:35", 4)); len(early.Transitions) != 0 {
		t.Errorf("within the grace: %v", typesOf(early.Transitions))
	}
	ran := observation{UID: "r1", Trigger: TriggerSchedule, ScheduledAt: stamp(at("01:30", 4)), Created: at("01:30", 4)}
	if out := evaluateSchedule(spec, []observation{ran}, &jobState{V: stateVersion}, false, now); len(out.Transitions) != 0 {
		t.Errorf("a slot with a run: %v", typesOf(out.Transitions))
	}
	recorded := &jobState{V: stateVersion, Runs: []runRecord{{UID: "r1", Scheduled: stamp(at("01:30", 4)), End: endSucceeded, Gone: true}}}
	if out := evaluateSchedule(spec, nil, recorded, false, now); len(out.Transitions) != 0 {
		t.Errorf("a slot whose run was cleaned up: %v", typesOf(out.Transitions))
	}
	// A held or suspended schedule misses nothing.
	held := nightly()
	held.FinishBy, held.Held, held.HoldReason = "", true, "month end"
	out = evaluateSchedule(held, nil, &jobState{V: stateVersion, Held: &heldState{}}, false, now)
	if len(out.Transitions) != 0 {
		t.Errorf("held: %v", typesOf(out.Transitions))
	}
}

// The hold annotation is published as it appears and as it goes.
func TestTheHoldIsPublishedWhenItIsSetAndWhenItIsLifted(t *testing.T) {
	spec := nightly()
	spec.FinishBy, spec.Held, spec.HoldReason, spec.HeldSince = "", true, "month end", "2026-11-04T00:00:00Z"
	state := &jobState{V: stateVersion}
	now := at("01:00", 4)
	out := evaluateSchedule(spec, nil, state, false, now)
	if got := typesOf(out.Transitions); len(got) != 1 || got[0] != events.TypeBatchScheduleHeld || out.Transitions[0].HoldReason != "month end" {
		t.Fatalf("held: %v", got)
	}
	if again := evaluateSchedule(spec, nil, state, false, now); len(again.Transitions) != 0 {
		t.Errorf("held again: %v", typesOf(again.Transitions))
	}
	spec.Held = false
	out = evaluateSchedule(spec, nil, state, false, now)
	if got := typesOf(out.Transitions); len(got) != 1 || got[0] != events.TypeBatchScheduleReleased {
		t.Errorf("released: %v", got)
	}
}

// The first look at a job publishes nothing about the past.
func TestTheFirstLookAtAJobPublishesNoPastMisses(t *testing.T) {
	spec := nightly()
	state := &jobState{V: stateVersion}
	if out := evaluateSchedule(spec, nil, state, true, at("02:30", 4)); len(out.Transitions) != 0 {
		t.Errorf("baseline published %v", typesOf(out.Transitions))
	}
	if !state.missed(stamp(at("01:30", 4))) || state.Deadlines[stamp(at("02:00", 4))] != "missed" {
		t.Errorf("baseline must still record what it did not publish: %+v", state)
	}
}

func TestCronPrevAndNextAgree(t *testing.T) {
	c, ok := parseCron("30 1 * * *", sgt())
	if !ok {
		t.Fatal("parse")
	}
	if p, _ := c.prev(at("01:30", 4)); !p.Equal(at("01:30", 4)) {
		t.Errorf("prev at the slot = %s", p)
	}
	if p, _ := c.prev(at("01:29", 4)); !p.Equal(at("01:30", 3)) {
		t.Errorf("prev before the slot = %s", p)
	}
	if n, _ := c.next(at("01:30", 4)); !n.Equal(at("01:30", 5)) {
		t.Errorf("next = %s", n)
	}
	m, _ := parseCron("*/5 * * * *", time.UTC)
	if p, _ := m.prev(time.Date(2026, 11, 4, 10, 7, 0, 0, time.UTC)); p.Minute() != 5 {
		t.Errorf("minutely prev = %s", p)
	}
}

func TestP90IsNearestRank(t *testing.T) {
	if v, _ := p90Of([]float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}); v != 9 {
		t.Errorf("p90 of 1..10 = %v, want 9", v)
	}
	if v, _ := p90Of([]float64{7}); v != 7 {
		t.Errorf("p90 of one = %v", v)
	}
	if _, ok := p90Of(nil); ok {
		t.Error("no history has no p90")
	}
}
