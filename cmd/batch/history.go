/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"adhar-io/adhar/cmd/events"
	"adhar-io/adhar/cmd/helpers"
	platformevents "adhar-io/adhar/platform/events"

	"github.com/spf13/cobra"
	"k8s.io/client-go/rest"
)

// History (design 0025 §8.1, §12, D12): what happened to a job beyond the
// runs the cluster still holds. A run Job is deleted by its TTL; the batch
// observer published each of its transitions onto adhar.batch, which keeps 90
// days. The CLI reads that topic through the platform's event bridge exactly as
// `adhar events list` does — the API server's service proxy, the kubeconfig's
// own credentials — keeps the events whose subject is <namespace>/<job>, and
// never reads the observer's own record ConfigMap, which is internal.

var (
	historySince  string
	historyLimit  int
	historyBridge string
)

var historyCmd = &cobra.Command{
	Use:   "history <job>",
	Short: "A job's run history from the adhar.batch event stream (90 days) and the run-summary archive",
	Long: `Show what happened to a job, oldest first, from the adhar.batch events the
batch observer published: runs requested, started, succeeded, failed, stopped,
resumed, skipped and lost; holds, releases and missed schedule slots; deadlines
endangered and missed. Unlike ` + "`runs`" + `, which lists the run Jobs the cluster still
holds, this reaches back as far as the topic retains (90 days), and past that
through the run-summary archive: the final event of every finished run, which
the batch observer keeps in the platform object store (rows marked "archive").

It reads the stream through the platform's event bridge (opt-in: the
kafka-operator package), as ` + "`adhar events list`" + ` does, and the archive with
the platform's object-store credential (Secret adhar-system/root-creds) through
a port-forward to RustFS — so the archive is read only by an identity that may
read that Secret; anyone else gets the stream and a line saying so.`,
	Example: `  adhar batch history importer --env prod
  adhar batch history importer --since 7d -o json | jq -r '.[].type'`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		since, err := parseSince(historySince)
		if err != nil {
			return err
		}
		cl, err := connect(cmd)
		if err != nil {
			return err
		}
		ns, err := resolveNamespace(ctx(cmd), cl.dc, cmd)
		if err != nil {
			return err
		}
		ns, name := parseRef(args[0], ns)
		var cutoff time.Time
		if since > 0 {
			cutoff = now().Add(-since)
		}
		evs, problems, busErr := readBatchHistory(ctx(cmd), eventBus{cluster: cl.cfg, bridge: historyBridge}, ns+"/"+name, cutoff, historyLimit)
		// The run-summary archive (Phase 4): each finished run's final event,
		// kept past the topic's retention. A second source, never a reason to
		// fail: what it cannot read is said in one line.
		var archived []platformevents.Event
		consulted := false
		if historyArchive {
			a, aProblems, aErr := readBatchArchive(ctx(cmd), cl, ns, name, cutoff, historyLimit)
			if aErr != nil {
				problems = append(problems, fmt.Errorf("the run-summary archive was not consulted: %w", aErr))
			} else {
				archived, consulted = a, true
				problems = append(problems, aProblems...)
			}
		}
		if busErr != nil {
			if !consulted {
				// The bus is opt-in and, without the archive, the history
				// lives nowhere else; what the cluster itself still holds is
				// one command away.
				return fmt.Errorf("%w\n  `adhar batch runs %s` lists the runs the cluster still holds", busErr, name)
			}
			problems = append([]error{fmt.Errorf("the adhar.batch stream could not be read, so this is the archive alone — finished runs only: %w", busErr)}, problems...)
		}
		merged := mergeHistory(evs, archived, historyLimit)
		if machineReadable() {
			return printMachine(merged)
		}
		for _, p := range problems {
			helpers.Printf("%s\n", helpers.CreateWarning(p.Error()))
		}
		printHistory(ns, name, mergedRowsOf(merged))
		return nil
	},
}

func init() {
	historyCmd.Flags().StringVar(&historySince, "since", "", "Only events newer than this (30d, 12h, 90m); default: everything retained")
	historyCmd.Flags().IntVar(&historyLimit, "limit", 200, "Show at most the newest this many events (0: all)")
	historyCmd.Flags().StringVar(&historyBridge, "bridge", "", "Kafka bridge base URL (default: the platform's, through the API server)")
}

// eventBus is where a command reads adhar.batch: the platform's bridge in the
// cluster the command connected to, or a bridge URL the person reaches
// themselves (--bridge). The cluster travels with it because the job was read
// there: `adhar batch` follows --kubeconfig and $KUBECONFIG, and events read
// from the default kubeconfig instead would be another cluster's — a history
// that is not this job's, and for `dlq --replay` execution ids to replay that
// this job never had.
type eventBus struct {
	cluster *rest.Config
	bridge  string
}

// historyQuery is one job's events on adhar.batch, as the bus is asked.
func (b eventBus) historyQuery(subject string, since time.Time, limit int) events.HistoryQuery {
	return events.HistoryQuery{
		Bridge:  b.bridge,
		Cluster: b.cluster,
		Topics:  []string{platformevents.TopicBatch},
		Since:   since,
		Limit:   limit,
		Keep:    subjectIs(subject),
	}
}

// readBatchHistory reads one job's events from adhar.batch. It is a variable so
// the tests can stand in for the bridge.
var readBatchHistory = func(c context.Context, bus eventBus, subject string, since time.Time, limit int) ([]platformevents.Event, []error, error) {
	return events.ReadHistory(c, bus.historyQuery(subject, since, limit))
}

// subjectIs keeps one job's events. Exactly the subject, unlike `adhar events
// --subject`: a substring match would give `importer` the history of
// `importer-eu` too, and `prod/importer` that of `preprod/importer`.
func subjectIs(subject string) func(platformevents.Event) bool {
	return func(e platformevents.Event) bool { return e.Subject == subject }
}

var sinceDays = regexp.MustCompile(`^(\d+)([dw])$`)

// parseSince reads --since: days and weeks (30d, 2w) as a person writes a
// retention window, else anything time.ParseDuration reads.
func parseSince(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if m := sinceDays.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		day := 24 * time.Hour
		if m[2] == "w" {
			day *= 7
		}
		return time.Duration(n) * day, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("--since %q is not a duration (30d, 2w, 12h, 90m)", s)
	}
	return d, nil
}

// batchEventData is the `data` of an adhar.batch event as a consumer reads it
// (platform/events/CONTRACT.md): every field optional.
type batchEventData struct {
	BatchJob string `json:"batchJob"`
	Run      *struct {
		Name    string `json:"name"`
		Kind    string `json:"kind"`
		Trigger string `json:"trigger"`
		Actor   string `json:"actor"`
		Queue   string `json:"queue"`
	} `json:"run"`
	SpringBatch *struct {
		ExecutionID  *int64 `json:"executionId"`
		Status       string `json:"status"`
		BusinessDate string `json:"businessDate"`
	} `json:"springBatch"`
	Counts *struct {
		Read         *int64 `json:"read"`
		Written      *int64 `json:"written"`
		Skipped      *int64 `json:"skipped"`
		DeadLettered *int64 `json:"deadLettered"`
	} `json:"counts"`
	End             string   `json:"end"`
	DurationSeconds *float64 `json:"durationSeconds"`
	Outcome         *struct {
		Reason   string `json:"reason"`
		ExitCode *int   `json:"exitCode"`
		Message  string `json:"message"`
	} `json:"outcome"`
	Pod      string `json:"pod"`
	Schedule *struct {
		Slot       string `json:"slot"`
		HoldReason string `json:"holdReason"`
	} `json:"schedule"`
	Deadline *struct {
		Deadline     string   `json:"deadline"`
		ExpectedEnd  string   `json:"expectedEnd"`
		SlackSeconds *float64 `json:"slackSeconds"`
	} `json:"deadline"`
	Dependency *struct {
		BusinessDate string   `json:"businessDate"`
		After        []string `json:"after"`
	} `json:"dependency"`
}

// eventDataOf decodes an event's data; a body that is not the batch shape
// reads as empty rather than failing the history around it.
func eventDataOf(e platformevents.Event) batchEventData {
	var d batchEventData
	if len(e.Data) > 0 {
		_ = json.Unmarshal(e.Data, &d)
	}
	return d
}

// historyRow is one line of `adhar batch history`. From says where the event
// was read: the bus, or the run-summary archive.
type historyRow struct {
	Time, Event, Run, BusinessDate, Detail, From string
}

// mergedRowsOf is historyRowsOf over a merged history, each row labelled with
// its source.
func mergedRowsOf(h []historyEvent) []historyRow {
	evs := make([]platformevents.Event, len(h))
	for i := range h {
		evs[i] = h[i].Event
	}
	rows := historyRowsOf(evs)
	for i := range rows {
		rows[i].From = "bus"
		if h[i].Archived {
			rows[i].From = "archive"
		}
	}
	return rows
}

func historyRowsOf(evs []platformevents.Event) []historyRow {
	rows := make([]historyRow, 0, len(evs))
	for _, e := range evs {
		d := eventDataOf(e)
		row := historyRow{Time: e.Time, Event: strings.TrimPrefix(e.Type, platformevents.TypePrefix+"batch.")}
		if t, err := time.Parse(time.RFC3339Nano, e.Time); err == nil {
			row.Time = t.Local().Format("2006-01-02 15:04:05")
		}
		if d.Run != nil {
			row.Run = d.Run.Name
		}
		switch {
		case d.SpringBatch != nil && d.SpringBatch.BusinessDate != "":
			row.BusinessDate = d.SpringBatch.BusinessDate
		case d.Dependency != nil:
			row.BusinessDate = d.Dependency.BusinessDate
		}
		row.Detail = eventDetail(d)
		rows = append(rows, row)
	}
	return rows
}

// eventDetail is the phrase worth a column: the outcome first, then what the
// event type is about.
func eventDetail(d batchEventData) string {
	var parts []string
	if d.Outcome != nil {
		if d.Outcome.Reason != "" {
			parts = append(parts, d.Outcome.Reason)
		}
		if d.Outcome.ExitCode != nil {
			parts = append(parts, fmt.Sprintf("exit %d", *d.Outcome.ExitCode))
		}
	}
	if d.Run != nil && d.Run.Trigger != "" {
		parts = append(parts, "trigger "+d.Run.Trigger)
	}
	if d.Run != nil && d.Run.Actor != "" {
		parts = append(parts, "by "+d.Run.Actor)
	}
	if d.Counts != nil && d.Counts.Read != nil {
		parts = append(parts, fmt.Sprintf("read %d written %d", *d.Counts.Read, n(d.Counts.Written)))
	}
	if d.Counts != nil && d.Counts.DeadLettered != nil && *d.Counts.DeadLettered > 0 {
		parts = append(parts, fmt.Sprintf("%d dead-lettered", *d.Counts.DeadLettered))
	}
	if d.DurationSeconds != nil {
		parts = append(parts, (time.Duration(*d.DurationSeconds) * time.Second).String())
	}
	if d.Pod != "" {
		parts = append(parts, "pod "+d.Pod)
	}
	if d.Schedule != nil {
		if d.Schedule.HoldReason != "" {
			parts = append(parts, "held: "+d.Schedule.HoldReason)
		}
		if d.Schedule.Slot != "" {
			parts = append(parts, "slot "+d.Schedule.Slot)
		}
	}
	if d.Deadline != nil && d.Deadline.Deadline != "" {
		parts = append(parts, "deadline "+d.Deadline.Deadline)
		if d.Deadline.ExpectedEnd != "" {
			parts = append(parts, "expected "+d.Deadline.ExpectedEnd)
		}
	}
	if d.Dependency != nil && len(d.Dependency.After) > 0 {
		parts = append(parts, "after "+strings.Join(d.Dependency.After, ", "))
	}
	if d.Outcome != nil && d.Outcome.Message != "" {
		parts = append(parts, d.Outcome.Message)
	}
	return strings.Join(parts, " · ")
}

func printHistory(ns, name string, rows []historyRow) {
	if len(rows) == 0 {
		helpers.Printf("%s\n", helpers.CreateMuted(fmt.Sprintf("No adhar.batch events for %s/%s on the bus or in the archive.", ns, name)))
		return
	}
	tb := helpers.NewTable("TIME", "EVENT", "RUN", "DATE", "FROM", "DETAIL")
	for _, r := range rows {
		tb.Row(r.Time, r.Event, dash(r.Run), dash(r.BusinessDate), dash(r.From), dash(r.Detail))
	}
	fmt.Println(helpers.SectionHeading(helpers.IconInfo, fmt.Sprintf("History of %s/%s", ns, name)))
	fmt.Println(tb.Render())
}
