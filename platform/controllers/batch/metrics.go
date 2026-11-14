/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/types"
)

// The SLA gauges (§11.4). Their reader is the Mimir ruler, not Prometheus:
// observability/mimir/manifests/batch-app-alerts.yaml records and alerts on
// them, Alloy scrapes them from the manager pod (its prometheus.io/scrape
// annotation) and remote-writes them to Mimir. Two gauges, because the two
// alerts ask two questions: BatchDeadlineMissed "has finishBy passed with no
// success for the date?" (remaining <= 0) and BatchDeadlineEndangered "will the
// run, at its usual pace, end after finishBy?" (slack < 0 while remaining > 0).
// One gauge answering both made every endangered job read as missed.
//
// The labels are the OTLP resource labels the kit's own batch series carry,
// because the rules join on them: a scraped `namespace` would be the MANAGER's
// (adhar-system), and the rules group by these names and no others
// (TestTheDeadlineGaugesAreWhatTheMimirRulesRead pins both halves).
var deadlineLabels = []string{
	"k8s_namespace_name", "adhar_batch_job", "adhar_application",
	"adhar_environment", "adhar_team", "business_date",
}

var (
	deadlineRemaining = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "adhar_batch_deadline_seconds_remaining",
		Help: "Seconds until a BatchJob's sla.finishBy for a business date that has not succeeded yet; " +
			"negative once the deadline has passed. Gone once the date succeeds, or 26 hours after the deadline.",
	}, deadlineLabels)
	deadlineSlack = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "adhar_batch_deadline_projected_slack_seconds",
		Help: "A BatchJob's sla.finishBy less the expected end of its unfinished work (start, or the slot for work " +
			"not started, plus the p90 of the last 20 durations); negative means it is not expected to make it. " +
			"Only for the deadline being forecast, and only once the job has a duration history.",
	}, deadlineLabels)
)

var registerMetrics sync.Once

// gauges is package-wide, like the gauges it keeps: one process, one registry.
var gauges deadlineSeries

// deadlineSeries remembers the label sets each job last published, so a
// reconcile can drop exactly the series that stopped being true (a date that
// succeeded, a team label that changed) AFTER setting the current ones: a
// scrape between "delete everything" and "set again" would read a missed
// deadline as resolved.
type deadlineSeries struct {
	mu   sync.Mutex
	last map[types.NamespacedName][]seriesKey
}

type seriesKey struct {
	labels prometheus.Labels
	slack  bool
}

func (k seriesKey) id() string {
	s := ""
	if k.slack {
		s = "slack"
	}
	for _, l := range deadlineLabels {
		s += "\x00" + k.labels[l]
	}
	return s
}

// publish sets the job's gauges to the deadlines in play and removes the rest.
// Nil deadlines (the BatchJob is gone, or declares no finishBy) remove all.
func (d *deadlineSeries) publish(job types.NamespacedName, application string, h hierarchy, deadlines []deadlineGauge) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var now []seriesKey
	for _, g := range deadlines {
		l := prometheus.Labels{
			"k8s_namespace_name": job.Namespace, "adhar_batch_job": job.Name, "adhar_application": application,
			"adhar_environment": h.Environment, "adhar_team": h.Team, "business_date": g.BusinessDate,
		}
		deadlineRemaining.With(l).Set(g.Remaining)
		now = append(now, seriesKey{labels: l})
		if g.Slack != nil {
			deadlineSlack.With(l).Set(*g.Slack)
			now = append(now, seriesKey{labels: l, slack: true})
		}
	}
	keep := map[string]bool{}
	for _, k := range now {
		keep[k.id()] = true
	}
	for _, k := range d.last[job] {
		if keep[k.id()] {
			continue
		}
		if k.slack {
			deadlineSlack.Delete(k.labels)
		} else {
			deadlineRemaining.Delete(k.labels)
		}
	}
	if d.last == nil {
		d.last = map[types.NamespacedName][]seriesKey{}
	}
	if len(now) == 0 {
		delete(d.last, job)
		return
	}
	d.last[job] = now
}
