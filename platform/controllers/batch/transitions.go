/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"adhar-io/adhar/platform/events"
)

const (
	endSucceeded = "succeeded"
	endFailed    = "failed"
	endSkipped   = "skipped"
	endLost      = "lost"
	// endUnknown is a run deleted while no observer was watching: it may have
	// succeeded, failed or been lost, and nothing left says which.
	endUnknown = "unknown"
)

// transition is one thing that happened to a run since the observer last
// looked. Lost carries the attempt for `run.lost`.
type transition struct {
	Type string
	Lost *lostAttempt
}

// transitionsOf compares what a run IS with what was already said about it and
// returns what to say now, updating the record to match. It is the whole of
// A3.1: every transition once, in the order it happened, and nothing for a run
// that has not changed.
//
// `baseline` is the first look at a job the observer has no record of — it was
// just deployed onto a cluster with history, or the job is new. A run that
// already ended then is recorded (its duration and business date are history
// the SLA and dependsOn need) and NOT published: it is not a transition anyone
// can still act on, and a newly deployed observer that replayed a week of runs
// onto the bus would look exactly like a week of runs.
func transitionsOf(o observation, rec *runRecord, baseline bool) []transition {
	if baseline && o.Phase.terminal() {
		rec.Requested, rec.Started = true, o.Ran || o.Phase == phaseSucceeded
		for _, l := range o.Lost {
			if !rec.lostPod(l.Pod) {
				rec.Lost = append(rec.Lost, l.Pod)
			}
		}
		finish(o, rec)
		return nil
	}

	var out []transition
	if !rec.Requested {
		rec.Requested = true
		// A scheduled run is not requested by anyone: the clock starts it, so
		// its first event is `run.started`.
		if o.Trigger != TriggerSchedule {
			out = append(out, transition{Type: events.TypeBatchRunRequested})
		}
	}
	if !rec.Started && (o.Ran || o.Phase == phaseSucceeded) {
		rec.Started = true
		out = append(out, transition{Type: events.TypeBatchRunStarted})
	}
	for i := range o.Lost {
		l := o.Lost[i]
		if rec.lostPod(l.Pod) {
			continue
		}
		rec.Lost = append(rec.Lost, l.Pod)
		out = append(out, transition{Type: events.TypeBatchRunLost, Lost: &l})
	}
	// A native pod last seen Running that has gone without an end — its node
	// was removed and the pod force-deleted. Volcano deletes and recreates a
	// gang's pods itself on RestartJob, so its pods are judged by their state
	// alone; a run that has ended or is being stopped deletes its pods on
	// purpose.
	if o.Kind == kindJob && !o.Phase.terminal() && o.Phase != phaseStopping && o.Phase != phaseStopped {
		for _, pod := range rec.Running {
			if o.PodNames[pod] || rec.lostPod(pod) {
				continue
			}
			rec.Lost = append(rec.Lost, pod)
			out = append(out, transition{Type: events.TypeBatchRunLost, Lost: &lostAttempt{
				Pod: pod, Reason: "PodVanished", Message: "the pod was deleted while running and reported no end",
			}})
		}
	}
	rec.Running = append([]string(nil), o.RunningPods...)

	// A native backfill creates every date's run suspended and releases them a
	// few at a time. A run still held back — the backfill's mark, and nothing
	// ever ran — is queued, not stopped: publishing run.stopped at its creation
	// and run.resumed at its release would put two things nobody did on the
	// bus for every date of every backfill. Once it has run, a stop is a stop,
	// mark or not.
	heldBack := o.Queued && !rec.Started

	switch {
	case o.Phase == phaseStopped && rec.Stops == rec.Resumes && !heldBack:
		rec.Stops++
		out = append(out, transition{Type: events.TypeBatchRunStopped})
	case rec.Stops > rec.Resumes && (o.Phase == phasePending || o.Phase == phaseRunning):
		rec.Resumes++
		out = append(out, transition{Type: events.TypeBatchRunResumed})
	}

	if rec.End == "" && o.Phase.terminal() {
		finish(o, rec)
		switch rec.End {
		case endSkipped:
			out = append(out, transition{Type: events.TypeBatchRunSkipped})
		case endSucceeded:
			out = append(out, transition{Type: events.TypeBatchRunSucceeded})
		default:
			out = append(out, transition{Type: events.TypeBatchRunFailed})
		}
	}
	return out
}

// finish records how a terminal run ended.
func finish(o observation, rec *runRecord) {
	switch {
	case o.Phase == phaseSucceeded && skippedReason(o) != "":
		rec.End = endSkipped
	case o.Phase == phaseSucceeded:
		rec.End = endSucceeded
	default:
		rec.End = endFailed
	}
	rec.BusinessDate = o.BusinessDate
	rec.Satisfies = satisfiesDependency(o)
	rec.Finished = stamp(o.Finished)
	if !o.Started.IsZero() && !o.Finished.IsZero() && o.Finished.After(o.Started) {
		rec.Seconds = o.Finished.Sub(o.Started).Seconds()
	}
}

// vanished is the transition of a run that was deleted before it ended (§12:
// "an OOM-killed/vanished run"). Its record is the only trace left of it.
func vanished(rec *runRecord) transition {
	rec.End, rec.Gone = endLost, true
	return transition{Type: events.TypeBatchRunLost, Lost: &lostAttempt{
		Reason: "Vanished", Message: "the run was deleted before it ended",
	}}
}

// unobserved is the transition of a recorded run that disappeared while the
// observer was not running — a run that ended AND was deleted during a
// manager restart. Tombstones live in memory, so its final state is gone, and
// calling it Vanished ("deleted before it ended") would claim to know what the
// observer did not see. It is still published as run.lost, because a run
// with no terminal event reads as running forever in every history built from
// the bus; the reason says the outcome is unknown, and it satisfies no
// dependsOn.
func unobserved(rec *runRecord) transition {
	rec.End, rec.Gone = endUnknown, true
	return transition{Type: events.TypeBatchRunLost, Lost: &lostAttempt{
		Reason: "Unobserved", Message: "the run was deleted while the batch observer was not running; how it ended is not known",
	}}
}
