/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"context"
	"encoding/json"
	"strings"

	"adhar-io/adhar/platform/events"
)

// Publisher is the one call the observer makes to the bus. *events.Publisher
// satisfies it; tests record instead. It has no error return on purpose — the
// bus is advisory (platform/events/CONTRACT.md), so nothing the observer does
// can depend on whether an event was delivered.
type Publisher interface {
	Publish(ctx context.Context, e events.Event)
}

// hierarchy is the ADR-0026 hierarchy of a namespace, from its labels — never
// from the run or its pod, which a tenant writes (§5.14: tenant pods cannot
// forge where their events belong).
type hierarchy struct {
	Organisation, Team, Project, Environment, Plane string
}

func hierarchyOf(labels map[string]string) hierarchy {
	return hierarchy{
		Organisation: labels["adhar.io/organisation"],
		Team:         labels["adhar.io/team"],
		Project:      labels["adhar.io/project"],
		Environment:  labels["adhar.io/environment"],
		Plane:        labels["adhar.io/plane"],
	}
}

// The `data` of an adhar.batch event (§12). Every field is optional for a
// consumer, as the termination summary's are (D13).
type eventData struct {
	BatchJob        string           `json:"batchJob"`
	Namespace       string           `json:"namespace"`
	Application     string           `json:"application,omitempty"`
	Run             *runData         `json:"run,omitempty"`
	SpringBatch     *springBatchData `json:"springBatch,omitempty"`
	Counts          *countsData      `json:"counts,omitempty"`
	Start           string           `json:"start,omitempty"`
	End             string           `json:"end,omitempty"`
	DurationSeconds *float64         `json:"durationSeconds,omitempty"`
	Outcome         *outcomeData     `json:"outcome,omitempty"`
	// Pod is the attempt a `run.lost` is about.
	Pod      string        `json:"pod,omitempty"`
	Schedule *scheduleData `json:"schedule,omitempty"`
	Deadline *deadlineData `json:"deadline,omitempty"`
	// Dependency is set on `run.requested` for a run the observer started.
	Dependency *dependencyData `json:"dependency,omitempty"`
}

type runData struct {
	Name      string `json:"name"`
	Kind      string `json:"kind,omitempty"`
	UID       string `json:"uid,omitempty"`
	Trigger   string `json:"trigger,omitempty"`
	Actor     string `json:"actor,omitempty"`
	Scheduler string `json:"scheduler,omitempty"`
	Queue     string `json:"queue,omitempty"`
}

type springBatchData struct {
	Job          string            `json:"job,omitempty"`
	InstanceID   *int64            `json:"instanceId,omitempty"`
	ExecutionID  *int64            `json:"executionId,omitempty"`
	Status       string            `json:"status,omitempty"`
	BusinessDate string            `json:"businessDate,omitempty"`
	Identifying  map[string]string `json:"identifying,omitempty"`
}

type countsData struct {
	Read         *int64 `json:"read,omitempty"`
	Written      *int64 `json:"written,omitempty"`
	Skipped      *int64 `json:"skipped,omitempty"`
	DeadLettered *int64 `json:"deadLettered,omitempty"`
}

type outcomeData struct {
	Reason   string `json:"reason,omitempty"`
	ExitCode *int   `json:"exitCode,omitempty"`
	Message  string `json:"message,omitempty"`
}

type scheduleData struct {
	Cron       string `json:"cron,omitempty"`
	TimeZone   string `json:"timeZone,omitempty"`
	Slot       string `json:"slot,omitempty"`
	HoldReason string `json:"holdReason,omitempty"`
	HeldSince  string `json:"heldSince,omitempty"`
}

type deadlineData struct {
	FinishBy     string   `json:"finishBy"`
	TimeZone     string   `json:"timeZone,omitempty"`
	Deadline     string   `json:"deadline"`
	Slot         string   `json:"slot,omitempty"`
	ExpectedEnd  string   `json:"expectedEnd,omitempty"`
	P90Seconds   *float64 `json:"p90Seconds,omitempty"`
	SlackSeconds *float64 `json:"slackSeconds,omitempty"`
}

type dependencyData struct {
	BusinessDate string   `json:"businessDate"`
	After        []string `json:"after"`
}

func newEvent(typ, namespace, batchJob, correlation string, h hierarchy, data eventData) events.Event {
	body, _ := json.Marshal(data)
	return events.Event{
		Type:            typ,
		Subject:         namespace + "/" + batchJob,
		DataContentType: "application/json",
		Data:            body,
		Organisation:    h.Organisation,
		Team:            h.Team,
		Project:         h.Project,
		Environment:     h.Environment,
		Plane:           h.Plane,
		Correlation:     correlation,
	}
}

// runEvent is the event for one run transition.
func runEvent(t transition, o observation, fields []paramField, h hierarchy) events.Event {
	d := eventData{
		BatchJob:    o.BatchJob,
		Namespace:   o.Namespace,
		Application: o.Application,
		Run: &runData{
			Name: o.Name, Kind: o.Kind, UID: o.UID, Trigger: o.Trigger, Actor: o.Actor,
			Scheduler: o.Scheduler, Queue: o.Queue,
		},
		Start: stamp(o.Started),
	}
	sb := &springBatchData{BusinessDate: o.BusinessDate, Identifying: maskedIdentifying(fields, o.Params)}
	if s := o.Summary; s != nil {
		sb.Job, sb.InstanceID, sb.ExecutionID, sb.Status = s.Job, s.InstanceID, s.ExecutionID, s.Status
	}
	if sb.Job != "" || sb.BusinessDate != "" || sb.Identifying != nil || sb.ExecutionID != nil {
		d.SpringBatch = sb
	}
	if t.Type == events.TypeBatchRunRequested && o.DependsOn != "" {
		d.Dependency = &dependencyData{BusinessDate: o.BusinessDate, After: strings.Split(o.DependsOn, ",")}
	}
	switch t.Type {
	case events.TypeBatchRunLost:
		d.Pod = t.Lost.Pod
		d.Outcome = &outcomeData{Reason: t.Lost.Reason, ExitCode: t.Lost.ExitCode, Message: t.Lost.Message}
	case events.TypeBatchRunSucceeded, events.TypeBatchRunFailed, events.TypeBatchRunSkipped:
		d.End = stamp(o.Finished)
		if !o.Started.IsZero() && o.Finished.After(o.Started) {
			secs := o.Finished.Sub(o.Started).Seconds()
			d.DurationSeconds = &secs
		}
		if s := o.Summary; s != nil {
			d.Counts = &countsData{Read: s.Read, Written: s.Written, Skipped: s.Skipped, DeadLettered: s.DeadLettered}
			if d.Counts.Read == nil && d.Counts.Written == nil && d.Counts.Skipped == nil && d.Counts.DeadLettered == nil {
				d.Counts = nil
			}
		}
		// The outcome is IN the event (CONTRACT.md): exit code, the kit's
		// reason or the Job's (BackoffLimitExceeded, DeadlineExceeded,
		// PodFailurePolicy), and the message.
		d.Outcome = &outcomeData{Reason: o.Reason, ExitCode: o.ExitCode, Message: o.Message}
		if t.Type == events.TypeBatchRunSkipped {
			d.Outcome.Reason = skippedReason(o)
		}
	case events.TypeBatchRunStopped:
		d.Outcome = &outcomeData{Reason: o.Reason, Message: o.Message}
	}
	return newEvent(t.Type, o.Namespace, o.BatchJob, o.UID, h, d)
}

// jobEvent is the event for a hold, a missed slot or a deadline.
func jobEvent(t jobTransition, spec jobSpec, h hierarchy) events.Event {
	d := eventData{BatchJob: spec.Name, Namespace: spec.Namespace, Application: spec.Application}
	correlation := spec.UID
	if t.Run != nil {
		correlation = t.Run.UID
		d.Run = &runData{Name: t.Run.Name, Kind: t.Run.Kind, UID: t.Run.UID, Trigger: t.Run.Trigger, Scheduler: t.Run.Scheduler, Queue: t.Run.Queue}
		d.Start = stamp(t.Run.Started)
	}
	switch t.Type {
	case events.TypeBatchScheduleHeld, events.TypeBatchScheduleReleased:
		d.Schedule = &scheduleData{Cron: spec.Cron, TimeZone: spec.TimeZone, HoldReason: t.HoldReason, HeldSince: t.HeldSince}
	case events.TypeBatchScheduleMissed:
		d.Schedule = &scheduleData{Cron: spec.Cron, TimeZone: spec.TimeZone, Slot: stamp(t.Slot)}
		d.Outcome = &outcomeData{Reason: "NoRun", Message: "the slot passed and no run was started for it"}
	case events.TypeBatchDeadlineEndangered, events.TypeBatchDeadlineMissed:
		dd := &deadlineData{
			FinishBy: spec.FinishBy, TimeZone: firstNonEmpty(spec.SLATimeZone, spec.TimeZone),
			Deadline: stamp(t.Deadline), Slot: stamp(t.Slot), ExpectedEnd: stamp(t.ExpectedEnd),
		}
		if t.HasP90 {
			p := t.P90
			dd.P90Seconds = &p
		}
		if !t.ExpectedEnd.IsZero() {
			slack := t.Deadline.Sub(t.ExpectedEnd).Seconds()
			dd.SlackSeconds = &slack
		}
		d.Deadline = dd
	}
	return newEvent(t.Type, spec.Namespace, spec.Name, correlation, h, d)
}
