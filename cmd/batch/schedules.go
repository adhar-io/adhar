/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/client-go/dynamic"
)

// In-service schedules (design 0025 §5.20.8, Addendum Q). A service that runs
// its batch jobs on the kit's Quartz engine keeps its triggers in its OWN
// database, which neither the Console nor this command may read (INV-6). The
// kit therefore publishes what it schedules as a report: `ConfigMap
// <scheduler-name>-batch-schedules` in the service's namespace, labelled
// adhar.io/batch-schedules=true, key schedules.json, version 1. This command
// lists those ConfigMaps and prints them; it never writes one, and an
// in-service run has no Kubernetes object to stop, restart or hold.
//
// The report is written by a different program, at a different release, than
// this reader, so EVERY field is optional here: a field that is absent or of
// another type is shown as missing, never as an error and never as a zero that
// reads like a fact. A report of a version this reader does not know is shown
// as written rather than parsed: guessing at a newer shape is how a reader
// shows the wrong next fire with confidence.

const (
	// labelSchedules marks a schedule report (§5.20.8).
	labelSchedules = "adhar.io/batch-schedules"
	// schedulesKey is the report's one data key.
	schedulesKey = "schedules.json"
	// schedulesSuffix is the name the kit gives the report, after the scheduler.
	schedulesSuffix = "-batch-schedules"
	// scheduleReportVersion is the one version this reader parses.
	scheduleReportVersion = 1
	// stalledAfter is how far in the past a next fire may lie before the
	// scheduler or its report has stalled — the 600 s of the template's
	// BatchInServiceScheduleStalled alert.
	stalledAfter = 10 * time.Minute
	// rawShown caps a raw report in the table; -o json carries all of it.
	rawShown = 4096
)

var schedulesAll bool

var schedulesCmd = &cobra.Command{
	Use:   "schedules",
	Short: "List the in-service schedules services report (Quartz engine)",
	Long: `List the batch jobs services schedule INSIDE themselves with the kit's Quartz
engine: for each service, its jobs, their triggers (kind, expression, time zone,
calendar, misfire policy, state), the next fire, the last run with its status,
and the replicas checked in to its scheduler.

They come from the schedule reports the services write — ConfigMap
<scheduler>-batch-schedules in the service's namespace, labelled
adhar.io/batch-schedules=true — so reading them needs list on configmaps in that
namespace, and nothing in the service's database is read. A report says when it
was last updated; one cut to fit its size limit says so. The view is read-only:
an in-service run is not a Kubernetes object, so it has no run, stop, restart or
hold here. A job that needs those belongs in a BatchJob.`,
	Example: `  adhar batch schedules --env prod      # every in-service schedule in the prod environment
  adhar batch schedules -n team-a-dev   # one namespace
  adhar batch schedules -A -o json      # every namespace, as the reports were written`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cl, err := connect(cmd)
		if err != nil {
			return err
		}
		namespace, err := schedulesNamespace(ctx(cmd), cl.dc, cmd)
		if err != nil {
			return err
		}
		reports, err := readScheduleReports(ctx(cmd), cl.dc, namespace)
		if err != nil {
			return err
		}
		if machineReadable() {
			return printMachine(reports)
		}
		fmt.Print(renderSchedules(reports, namespace, now()))
		return nil
	},
}

func init() {
	schedulesCmd.Flags().BoolVarP(&schedulesAll, "all-namespaces", "A", false, "List the schedule reports of every namespace")
}

// schedulesNamespace is where `schedules` looks: every namespace with -A,
// otherwise the scope every other verb resolves. -A beside a namespace is
// ambiguous, not a namespace with extra steps.
func schedulesNamespace(c context.Context, dc dynamic.Interface, cmd *cobra.Command) (string, error) {
	if !schedulesAll {
		return resolveNamespace(c, dc, cmd)
	}
	if (cmd.Flags().Changed("namespace") && namespaceFlag != "") || (cmd.Flags().Changed("env") && envFlag != "") {
		return "", fmt.Errorf("-A reads every namespace; drop it, or drop -n/--env")
	}
	return "", nil
}

// scheduleReport is one service's report as `schedules` shows it, and its -o
// json. Raw is set, and nothing below Version is, when the report could not be
// read as version 1; Problem says why.
type scheduleReport struct {
	Namespace   string `json:"namespace" yaml:"namespace"`
	ConfigMap   string `json:"configMap" yaml:"configMap"`
	Application string `json:"application,omitempty" yaml:"application,omitempty"`
	// Version is the report's `v` as written (a JSON token, so a string "1"
	// reads `"1"`); empty when the report carries none.
	Version   string `json:"version,omitempty" yaml:"version,omitempty"`
	Engine    string `json:"engine,omitempty" yaml:"engine,omitempty"`
	Scheduler string `json:"scheduler,omitempty" yaml:"scheduler,omitempty"`
	Updated   string `json:"updated,omitempty" yaml:"updated,omitempty"`
	// Truncated is set on every report read as v1, and absent from one shown
	// raw: whether an unread report was cut is not something this reader knows.
	Truncated *bool `json:"truncated,omitempty" yaml:"truncated,omitempty"`
	// Instances is nil when the report carries no instances array, and an
	// empty list when it carries an empty one: "none checked in" is a scheduler
	// with no live replica, not a report that left the field out.
	Instances *[]scheduleInstance `json:"instances,omitempty" yaml:"instances,omitempty"`
	Jobs      []scheduledJob      `json:"jobs,omitempty" yaml:"jobs,omitempty"`
	Raw       string              `json:"raw,omitempty" yaml:"raw,omitempty"`
	Problem   string              `json:"problem,omitempty" yaml:"problem,omitempty"`
	env       string              // the ConfigMap's adhar.io/environment, for the order
}

// scheduleInstance is one replica checked in to the scheduler's cluster.
type scheduleInstance struct {
	ID      string `json:"id,omitempty" yaml:"id,omitempty"`
	Checkin string `json:"checkin,omitempty" yaml:"checkin,omitempty"`
}

// scheduledJob is one Spring Batch job the service schedules.
type scheduledJob struct {
	Job      string            `json:"job,omitempty" yaml:"job,omitempty"`
	Name     string            `json:"name,omitempty" yaml:"name,omitempty"`
	Triggers []scheduleTrigger `json:"triggers,omitempty" yaml:"triggers,omitempty"`
	LastRun  *scheduleLastRun  `json:"lastRun,omitempty" yaml:"lastRun,omitempty"`
}

// scheduleTrigger is one Quartz trigger of a job.
type scheduleTrigger struct {
	Key        string `json:"key,omitempty" yaml:"key,omitempty"`
	Kind       string `json:"kind,omitempty" yaml:"kind,omitempty"`
	Expression string `json:"expression,omitempty" yaml:"expression,omitempty"`
	TimeZone   string `json:"timeZone,omitempty" yaml:"timeZone,omitempty"`
	Calendar   string `json:"calendar,omitempty" yaml:"calendar,omitempty"`
	Misfire    string `json:"misfire,omitempty" yaml:"misfire,omitempty"`
	State      string `json:"state,omitempty" yaml:"state,omitempty"`
	Previous   string `json:"previous,omitempty" yaml:"previous,omitempty"`
	Next       string `json:"next,omitempty" yaml:"next,omitempty"`
}

// scheduleLastRun is a job's newest execution in the JobRepository.
type scheduleLastRun struct {
	Run         string `json:"run,omitempty" yaml:"run,omitempty"`
	ExecutionID *int64 `json:"executionId,omitempty" yaml:"executionId,omitempty"`
	Status      string `json:"status,omitempty" yaml:"status,omitempty"`
	Outcome     string `json:"outcome,omitempty" yaml:"outcome,omitempty"`
	Trigger     string `json:"trigger,omitempty" yaml:"trigger,omitempty"`
	Start       string `json:"start,omitempty" yaml:"start,omitempty"`
	End         string `json:"end,omitempty" yaml:"end,omitempty"`
}

// readScheduleReports lists the report ConfigMaps of a namespace ("" for all)
// by their label and reads each. A refusal is the reader's RBAC, said as such:
// AQ.8 expects the user who cannot list the namespace's ConfigMaps to be told
// that, not shown an empty list that reads like "no schedules".
func readScheduleReports(c context.Context, dc dynamic.Interface, namespace string) ([]scheduleReport, error) {
	list, err := dc.Resource(configMapGVR).Namespace(namespace).List(c, metav1.ListOptions{LabelSelector: labelSchedules + "=true"})
	if err != nil {
		if apierrors.IsForbidden(err) {
			if namespace == "" {
				return nil, fmt.Errorf("reading the schedule reports of every namespace needs list on configmaps cluster-wide; name one namespace with -n or --env")
			}
			return nil, fmt.Errorf("reading the schedule reports in %s needs list on configmaps there", namespace)
		}
		return nil, fmt.Errorf("listing the schedule reports: %w", err)
	}
	reports := make([]scheduleReport, 0, len(list.Items))
	for i := range list.Items {
		reports = append(reports, scheduleReportOf(&list.Items[i]))
	}
	sort.SliceStable(reports, func(i, j int) bool {
		a, b := reports[i], reports[j]
		if a.Application != b.Application {
			return a.Application < b.Application
		}
		if ra, rb := envRank(a.env), envRank(b.env); ra != rb {
			return ra < rb
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.ConfigMap < b.ConfigMap
	})
	return reports, nil
}

// scheduleReportOf reads one report ConfigMap. It never fails: what it cannot
// read is kept raw with the reason, so the service still appears.
func scheduleReportOf(cm *unstructured.Unstructured) scheduleReport {
	labels := cm.GetLabels()
	r := scheduleReport{
		Namespace:   cm.GetNamespace(),
		ConfigMap:   cm.GetName(),
		Application: labels["adhar.io/application"],
		env:         labels["adhar.io/environment"],
	}
	if r.env == "" {
		r.env = r.Namespace
	}
	data, _, _ := unstructured.NestedStringMap(cm.Object, "data")
	text, ok := data[schedulesKey]
	if !ok {
		r.Problem = "the ConfigMap has no " + schedulesKey + " key"
		r.fallbackApplication("")
		return r
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var top interface{}
	if err := dec.Decode(&top); err != nil {
		r.Raw, r.Problem = text, fmt.Sprintf("%s is not JSON: %v", schedulesKey, err)
		r.fallbackApplication("")
		return r
	}
	doc, isObject := top.(map[string]interface{})
	if !isObject {
		r.Raw, r.Problem = text, schedulesKey+" is not a JSON object"
		r.fallbackApplication("")
		return r
	}
	// The version is a JSON number, and JSON has one kind of number: 1.0 is 1.
	// A null is a `v` that is not 1, not a missing one. Both as the Console
	// reads it (CON-Q.1), so the two never disagree about the same report.
	if v, present := doc["v"]; present {
		token, _ := json.Marshal(v)
		r.Version = string(token)
		n, isNumber := v.(json.Number)
		f, err := n.Float64()
		if !isNumber || err != nil || f != scheduleReportVersion {
			r.Raw = text
			r.Problem = fmt.Sprintf("report version %s is not one this CLI reads (it reads v%d); shown as written", r.Version, scheduleReportVersion)
			r.fallbackApplication(str(doc, "scheduler"))
			return r
		}
	}
	// No `v` at all is read as v1, the only version there is: every field is
	// optional to a reader, the version included.
	r.Engine = str(doc, "engine")
	r.Scheduler = str(doc, "scheduler")
	r.Updated = str(doc, "updated")
	truncated, _ := doc["truncated"].(bool)
	r.Truncated = &truncated
	if items, isArray := doc["instances"].([]interface{}); isArray {
		instances := []scheduleInstance{}
		for _, item := range items {
			if o, isObj := item.(map[string]interface{}); isObj {
				instances = append(instances, scheduleInstance{ID: str(o, "id"), Checkin: str(o, "checkin")})
			}
		}
		r.Instances = &instances
	}
	jobs, _ := doc["jobs"].([]interface{})
	for _, item := range jobs {
		o, isObj := item.(map[string]interface{})
		if !isObj {
			continue
		}
		j := scheduledJob{Job: str(o, "job"), Name: str(o, "name")}
		triggers, _ := o["triggers"].([]interface{})
		for _, ti := range triggers {
			t, isObj := ti.(map[string]interface{})
			if !isObj {
				continue
			}
			j.Triggers = append(j.Triggers, scheduleTrigger{
				Key: str(t, "key"), Kind: str(t, "kind"), Expression: str(t, "expression"),
				TimeZone: str(t, "timeZone"), Calendar: str(t, "calendar"), Misfire: str(t, "misfire"),
				State: str(t, "state"), Previous: str(t, "previous"), Next: str(t, "next"),
			})
		}
		if lr, isObj := o["lastRun"].(map[string]interface{}); isObj {
			run := &scheduleLastRun{
				Run: str(lr, "run"), Status: str(lr, "status"), Outcome: str(lr, "outcome"),
				Trigger: str(lr, "trigger"), Start: str(lr, "start"), End: str(lr, "end"),
			}
			if n, isNumber := lr["executionId"].(json.Number); isNumber {
				if id, err := n.Int64(); err == nil {
					run.ExecutionID = &id
				}
			}
			j.LastRun = run
		}
		r.Jobs = append(r.Jobs, j)
	}
	r.fallbackApplication(r.Scheduler)
	return r
}

// fallbackApplication names a report that carries no adhar.io/application
// label: after its scheduler, then after its ConfigMap.
func (r *scheduleReport) fallbackApplication(scheduler string) {
	if r.Application != "" {
		return
	}
	if scheduler != "" {
		r.Application = scheduler
		return
	}
	r.Application = strings.TrimSuffix(r.ConfigMap, schedulesSuffix)
}

// str reads a string field; anything else is absent.
func str(o map[string]interface{}, key string) string {
	s, _ := o[key].(string)
	return s
}

// renderSchedules is the table view: one section per service, with a table of
// its triggers, one row each, and a table of its jobs' last runs. They are two
// tables because a last run belongs to the JOB, not to whichever of its
// triggers would be printed beside it, and because one grid of both does not
// fit a terminal. The absolute fire times are in -o json.
func renderSchedules(reports []scheduleReport, namespace string, at time.Time) string {
	var b strings.Builder
	if len(reports) == 0 {
		where := "in " + namespace
		if namespace == "" {
			where = "in any namespace"
		}
		fmt.Fprintf(&b, "%s\n", helpers.CreateMuted("No in-service schedule reports "+where+". A service on the kit's Quartz engine writes ConfigMap <scheduler>-batch-schedules; BatchJobs are listed by `adhar batch list`."))
		return b.String()
	}
	stalled := false
	for i, r := range reports {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s\n", helpers.Indent(helpers.SectionHeading(helpers.IconApp, r.Application)))
		fmt.Fprintf(&b, "%s\n\n", helpers.CreateMuted(reportSummary(r, at)))
		if r.Raw != "" || r.Problem != "" {
			fmt.Fprintf(&b, "%s\n", helpers.CreateWarning(r.Problem))
			if r.Raw != "" {
				fmt.Fprintf(&b, "%s\n", helpers.Indent(rawCell(r.Raw)))
			}
			continue
		}
		if r.Truncated != nil && *r.Truncated {
			fmt.Fprintf(&b, "%s\n", helpers.CreateWarning("This report is truncated: it outgrew its 256 KiB limit, so it may leave out last runs and, after them, the triggers beyond 50 per job.")+"\n")
		}
		if len(r.Jobs) == 0 {
			fmt.Fprintf(&b, "%s\n", helpers.CreateMuted("The report lists no jobs."))
			continue
		}
		triggers := helpers.NewTable("JOB", "TRIGGER", "SCHEDULE", "CALENDAR", "MISFIRE", "STATE", "NEXT FIRE")
		runs := helpers.NewTable("JOB", "LAST RUN", "STATUS", "OUTCOME", "TRIGGER", "ENDED")
		for _, j := range r.Jobs {
			job := dash(j.Name)
			if j.Name == "" {
				job = dash(j.Job)
			}
			if len(j.Triggers) == 0 {
				triggers.Row(job, "—", "—", "—", "—", "—", "—")
			}
			for k, tr := range j.Triggers {
				name := job
				if k > 0 {
					name = ""
				}
				next, late := nextFireCell(tr.Next, triggerFires(tr.State), at)
				stalled = stalled || late
				triggers.Row(name, dash(triggerName(tr.Key)), scheduleCellOf(tr), dash(tr.Calendar), dash(tr.Misfire), triggerStateCell(tr.State), next)
			}
			if lr := j.LastRun; lr != nil {
				runs.Row(job, dash(lr.Run), executionStatusCell(lr.Status), dash(lr.Outcome), dash(triggerName(lr.Trigger)), endedCell(lr, at))
			}
		}
		b.WriteString(triggers.Render())
		b.WriteString("\n")
		if runs.Rows() > 0 {
			b.WriteString("\n")
			b.WriteString(runs.Render())
			b.WriteString("\n")
		}
	}
	if stalled {
		fmt.Fprintf(&b, "\n%s\n", helpers.CreateWarning("A next fire more than 10 minutes in the past means the scheduler, or its report, has stalled (the BatchInServiceScheduleStalled alert)."))
	}
	return b.String()
}

// reportSummary is the line under a service's heading: where its report is,
// the engine, the replicas checked in and how old it is.
func reportSummary(r scheduleReport, at time.Time) string {
	parts := []string{r.Namespace + "/" + r.ConfigMap}
	if r.Engine != "" {
		parts = append(parts, r.Engine)
	}
	if r.Raw == "" && r.Problem == "" {
		if r.Instances != nil {
			parts = append(parts, plural(len(*r.Instances), "replica")+" checked in")
		} else {
			parts = append(parts, "replicas not reported")
		}
	}
	if r.Updated != "" {
		parts = append(parts, "updated "+ago(r.Updated, at))
	}
	return strings.Join(parts, " · ")
}

// triggerName is the trigger's own name: the last segment of its key
// (`adhar-batch-config.import-job.nightly` → nightly); a key of another shape
// is shown whole.
func triggerName(key string) string {
	if i := strings.LastIndex(key, "."); i >= 0 && i < len(key)-1 {
		return key[i+1:]
	}
	return key
}

func scheduleCellOf(t scheduleTrigger) string {
	var parts []string
	for _, p := range []string{t.Kind, t.Expression, t.TimeZone} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return dash(strings.Join(parts, " "))
}

// triggerStateCell renders Quartz's trigger state. BLOCKED is a trigger whose
// job is executing now; COMPLETE is one that will not fire again.
func triggerStateCell(state string) string {
	switch strings.ToUpper(state) {
	case "":
		return "—"
	case "NORMAL":
		return helpers.StateReady(state)
	case "BLOCKED":
		return helpers.StatePending(state)
	case "PAUSED", "PAUSED_BLOCKED", "COMPLETE":
		return helpers.StateDisabled(state)
	case "ERROR":
		return helpers.StateFailed(state)
	default:
		return helpers.StateUnknown(state)
	}
}

// triggerFires says a trigger in this Quartz state will still fire: no state
// at all (every field is optional), NORMAL, BLOCKED (its job is running now) and
// the transient ones a store passes through. PAUSED, PAUSED_BLOCKED, COMPLETE,
// ERROR and NONE do not, so a next fire long past is what they look like and not
// a stall — Quartz keeps a paused trigger's NEXT_FIRE_TIME where it was. The
// same set as the Console's FIRING_STATES (batch-schedules-model.ts).
func triggerFires(state string) bool {
	switch strings.ToUpper(state) {
	case "", "NORMAL", "BLOCKED", "WAITING", "ACQUIRED", "EXECUTING":
		return true
	}
	return false
}

// nextFireCell is how far off the next fire is; late says it lies more than
// stalledAfter in the past on a trigger that fires. A value that is not RFC 3339
// is shown as written.
func nextFireCell(next string, fires bool, at time.Time) (string, bool) {
	if next == "" {
		return "—", false
	}
	ts, err := time.Parse(time.RFC3339, next)
	if err != nil {
		return next, false
	}
	if ts.After(at) {
		return "in " + duration.HumanDuration(ts.Sub(at)), false
	}
	late := fires && at.Sub(ts) > stalledAfter
	cell := duration.HumanDuration(at.Sub(ts)) + " ago"
	if late {
		return helpers.StateDegraded(cell), true
	}
	return cell, false
}

// endedCell is when a last run ended, or that it is still going since its
// start; a time that is not RFC 3339 is shown as written.
func endedCell(lr *scheduleLastRun, at time.Time) string {
	switch {
	case lr.End != "":
		return ago(lr.End, at)
	case lr.Start != "":
		return "started " + ago(lr.Start, at)
	default:
		return "—"
	}
}

// executionStatusCell renders a Spring Batch BatchStatus.
func executionStatusCell(status string) string {
	switch strings.ToUpper(status) {
	case "":
		return "—"
	case "COMPLETED":
		return helpers.StateReady(status)
	case "FAILED":
		return helpers.StateFailed(status)
	case "STARTING", "STARTED", "STOPPING":
		return helpers.StatePending(status)
	case "STOPPED", "ABANDONED":
		return helpers.StateDisabled(status)
	default:
		return helpers.StateUnknown(status)
	}
}

// ago is "4m ago" for an RFC 3339 time, or the value as written.
func ago(ts string, at time.Time) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	if t.After(at) {
		return ts
	}
	return duration.HumanDuration(at.Sub(t)) + " ago"
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// rawCell is a raw report for the table: pretty-printed when it is JSON, and
// cut at rawShown bytes with a note, since -o json carries all of it.
func rawCell(raw string) string {
	var pretty bytes.Buffer
	if json.Indent(&pretty, []byte(raw), "", "  ") == nil {
		raw = pretty.String()
	}
	if len(raw) > rawShown {
		cut := rawShown
		for cut > 0 && !utf8.RuneStart(raw[cut]) {
			cut--
		}
		return raw[:cut] + fmt.Sprintf("\n… (%d bytes in all; -o json shows the whole report)", len(raw))
	}
	return raw
}
