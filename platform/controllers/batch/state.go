/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"encoding/json"
	"sort"
	"time"
)

// What the observer has already said, per BatchJob, in `ConfigMap
// <batchjob>-observer` beside the job (owned by the BatchJob, so it goes when
// the job goes).
//
// It is what makes "transitions only" survive a restart. The bus contract says
// an event is published when something CHANGES, and the only way to know a run
// changed after the manager restarted is to remember what was said before it.
// Without it a restart republishes every run's history (or, the other way
// round, a run that finished while the manager was down is never published).
//
// It lives in a ConfigMap of the observer's own, never on the run objects:
// the run belongs to the Job controller, Volcano and the person who started it
// (§3.5, no field has two writers), and a Volcano Job UPDATE passes through
// adhar-batch-queue-binding, which fails CLOSED — a Kyverno outage must not make
// the observer's bookkeeping fail, let alone a run's.
//
// It also holds the two things the cluster forgets too soon: run durations (the
// SLA's p90 of the last 20, §11.4, where the CronJob keeps three successful
// Jobs) and which business dates succeeded (dependsOn, §12).
const (
	stateKey      = "state.json"
	stateSuffix   = "-observer"
	stateVersion  = 1
	historyLimit  = 40
	deadlineLimit = 16
	missedLimit   = 8
	pendingLimit  = 16
)

type runRecord struct {
	UID  string `json:"uid"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Scheduled is the slot a scheduled run was started for, so a slot whose
	// run has since been deleted is still known to have had one.
	Scheduled string   `json:"scheduled,omitempty"`
	Created   string   `json:"created,omitempty"`
	Requested bool     `json:"requested,omitempty"`
	Started   bool     `json:"started,omitempty"`
	Stops     int      `json:"stops,omitempty"`
	Resumes   int      `json:"resumes,omitempty"`
	Lost      []string `json:"lost,omitempty"`
	// Running are the pods last seen Running, so a pod that disappears without
	// ever reporting an end (its node was force-deleted) is still a lost attempt.
	Running []string `json:"running,omitempty"`
	// End is succeeded | failed | skipped | lost, once published.
	End          string  `json:"end,omitempty"`
	BusinessDate string  `json:"businessDate,omitempty"`
	Satisfies    bool    `json:"satisfies,omitempty"`
	Seconds      float64 `json:"seconds,omitempty"`
	Finished     string  `json:"finished,omitempty"`
	Gone         bool    `json:"gone,omitempty"`
}

func (r *runRecord) lostPod(pod string) bool {
	for _, p := range r.Lost {
		if p == pod {
			return true
		}
	}
	return false
}

type heldState struct {
	Reason string `json:"reason,omitempty"`
	Since  string `json:"since,omitempty"`
}

type jobState struct {
	V    int         `json:"v"`
	Runs []runRecord `json:"runs,omitempty"`
	// Held is set while a published `schedule.held` has no `released` yet.
	Held *heldState `json:"held,omitempty"`
	// Missed are the slots published as `schedule.missed`.
	Missed []string `json:"missed,omitempty"`
	// Deadlines maps a deadline instant to what was published for it
	// (`endangered`, `missed`), so each is said once.
	Deadlines map[string]string `json:"deadlines,omitempty"`
	// Pending are dependency runs this job's success called for and the API
	// server refused for a reason that passes by itself (a count/jobs.batch
	// ResourceQuota). They are retried from here, because the success that
	// called for them is recorded and will never call for them again.
	Pending []pendingRun `json:"pending,omitempty"`
}

// pendingRun is one dependency run still to be started.
type pendingRun struct {
	Namespace    string `json:"namespace"`
	BatchJob     string `json:"batchJob"`
	BusinessDate string `json:"businessDate"`
	Since        string `json:"since"`
	Next         string `json:"next"`
	Attempts     int    `json:"attempts,omitempty"`
}

// pend records a refused dependency run, once per job and date.
func (s *jobState) pend(namespace, batchJob, date string, now time.Time) {
	for i := range s.Pending {
		p := &s.Pending[i]
		if p.Namespace == namespace && p.BatchJob == batchJob && p.BusinessDate == date {
			return
		}
	}
	s.Pending = append(s.Pending, pendingRun{Namespace: namespace, BatchJob: batchJob, BusinessDate: date,
		Since: stamp(now), Next: stamp(now.Add(pendingBackoff(0)))})
}

// pendingBackoff is the wait before retry n+1: 30 s doubling to 10 minutes. A
// quota frees when one of the namespace's runs ends, which is minutes, not
// seconds, and each attempt is a create the API server refuses.
func pendingBackoff(attempts int) time.Duration {
	d := 30 * time.Second
	for i := 0; i < attempts && d < 10*time.Minute; i++ {
		d *= 2
	}
	if d > 10*time.Minute {
		d = 10 * time.Minute
	}
	return d
}

func decodeState(raw string) *jobState {
	s := &jobState{V: stateVersion}
	if raw == "" {
		return s
	}
	if err := json.Unmarshal([]byte(raw), s); err != nil || s.V != stateVersion {
		// An unreadable or future record is started over rather than trusted:
		// the cost is a re-published transition, never a wrong one.
		return &jobState{V: stateVersion}
	}
	return s
}

func (s *jobState) encode() string {
	b, _ := json.Marshal(s)
	return string(b)
}

func (s *jobState) record(uid string) *runRecord {
	for i := range s.Runs {
		if s.Runs[i].UID == uid {
			return &s.Runs[i]
		}
	}
	return nil
}

// compact keeps every run that still exists and the newest finished ones that
// do not, so the record stays a few kilobytes however long the job lives.
func (s *jobState) compact() {
	var live, gone []runRecord
	for _, r := range s.Runs {
		if r.Gone {
			gone = append(gone, r)
		} else {
			live = append(live, r)
		}
	}
	sort.SliceStable(gone, func(i, j int) bool { return gone[i].Finished > gone[j].Finished })
	if keep := historyLimit - len(live); keep < len(gone) {
		if keep < 0 {
			keep = 0
		}
		gone = gone[:keep]
	}
	s.Runs = append(live, gone...)
	if len(s.Missed) > missedLimit {
		sort.Strings(s.Missed)
		s.Missed = s.Missed[len(s.Missed)-missedLimit:]
	}
	if len(s.Pending) > pendingLimit {
		s.Pending = s.Pending[len(s.Pending)-pendingLimit:]
	}
	if len(s.Deadlines) > deadlineLimit {
		keys := make([]string, 0, len(s.Deadlines))
		for k := range s.Deadlines {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys[:len(keys)-deadlineLimit] {
			delete(s.Deadlines, k)
		}
	}
}

// succeededFor says a run of this job counts as succeeded for business date D.
func (s *jobState) succeededFor(date string) bool {
	for _, r := range s.Runs {
		if r.Satisfies && r.BusinessDate == date {
			return true
		}
	}
	return false
}

// latestSuccess is the newest business date that succeeded, for a dependency
// that does not ask for the same date.
func (s *jobState) latestSuccess() string {
	var best string
	for _, r := range s.Runs {
		if r.Satisfies && r.BusinessDate > best {
			best = r.BusinessDate
		}
	}
	return best
}

// durations are the last n successful durations, newest first.
func (s *jobState) durations(n int) []float64 {
	var done []runRecord
	for _, r := range s.Runs {
		if r.End == endSucceeded && r.Seconds > 0 {
			done = append(done, r)
		}
	}
	sort.SliceStable(done, func(i, j int) bool { return done[i].Finished > done[j].Finished })
	var out []float64
	for i := 0; i < len(done) && i < n; i++ {
		out = append(out, done[i].Seconds)
	}
	return out
}

func (s *jobState) missed(slot string) bool {
	for _, m := range s.Missed {
		if m == slot {
			return true
		}
	}
	return false
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
