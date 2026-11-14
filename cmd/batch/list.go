/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"context"
	"fmt"
	"sort"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/client-go/dynamic"
)

var allNamespaces bool

var listCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List BatchJobs with their schedule, next run, last run and hold",
	RunE: func(cmd *cobra.Command, args []string) error {
		cl, err := connect(cmd)
		if err != nil {
			return err
		}
		namespace := ""
		if !allNamespaces {
			if namespace, err = resolveNamespace(ctx(cmd), cl.dc, cmd); err != nil {
				return err
			}
		}
		views, err := listJobs(ctx(cmd), cl.dc, namespace)
		if err != nil {
			return err
		}
		if machineReadable() {
			return printMachine(views)
		}
		if len(views) == 0 {
			where := "in " + namespace
			if allNamespaces {
				where = "anywhere"
			}
			helpers.Printf("%s\n", helpers.CreateMuted("No BatchJobs "+where+". A job is declared in Git (deploy/envs/<env>) and reaches the cluster through Argo CD."))
			return nil
		}
		headers := []string{"NAME", "APPLICATION", "SCHEDULE", "NEXT RUN", "LAST RUN", "LAST SUCCESS", "HELD", "STATUS"}
		if allNamespaces {
			headers = append([]string{"NAMESPACE"}, headers...)
		}
		t := helpers.NewTable(headers...)
		for _, v := range views {
			cells := []string{v.Name, v.Application, scheduleCell(v), dash(v.NextRun), lastRunCell(v), dash(v.LastSuccess), dash(v.Held), jobStatusCell(v.Status)}
			if allNamespaces {
				cells = append([]string{v.Namespace}, cells...)
			}
			t.Row(cells...)
		}
		fmt.Println(t.Render())
		return nil
	},
}

func init() {
	listCmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "List BatchJobs across every namespace")
}

// jobView is one BatchJob as the list reports it.
type jobView struct {
	Name        string    `json:"name" yaml:"name"`
	Namespace   string    `json:"namespace" yaml:"namespace"`
	DisplayName string    `json:"displayName,omitempty" yaml:"displayName,omitempty"`
	Application string    `json:"application" yaml:"application"`
	Environment string    `json:"environment,omitempty" yaml:"environment,omitempty"`
	Job         string    `json:"job,omitempty" yaml:"job,omitempty"`
	Image       string    `json:"image,omitempty" yaml:"image,omitempty"`
	Schedule    string    `json:"schedule,omitempty" yaml:"schedule,omitempty"`
	TimeZone    string    `json:"timeZone,omitempty" yaml:"timeZone,omitempty"`
	Suspended   bool      `json:"suspended" yaml:"suspended"`
	NextRun     string    `json:"nextRun,omitempty" yaml:"nextRun,omitempty"`
	Scheduler   string    `json:"scheduler,omitempty" yaml:"scheduler,omitempty"`
	Queue       string    `json:"queue,omitempty" yaml:"queue,omitempty"`
	Held        string    `json:"held,omitempty" yaml:"held,omitempty"`
	HeldSince   string    `json:"heldSince,omitempty" yaml:"heldSince,omitempty"`
	Status      jobStatus `json:"status" yaml:"status"`
	Problem     string    `json:"problem,omitempty" yaml:"problem,omitempty"`
	Message     string    `json:"message,omitempty" yaml:"message,omitempty"`
	LastRun     *runView  `json:"lastRun,omitempty" yaml:"lastRun,omitempty"`
	LastSuccess string    `json:"lastSuccess,omitempty" yaml:"lastSuccess,omitempty"`
	ActiveRuns  []string  `json:"activeRuns,omitempty" yaml:"activeRuns,omitempty"`
	Conditions  []string  `json:"conditions,omitempty" yaml:"conditions,omitempty"`
	Age         string    `json:"age,omitempty" yaml:"age,omitempty"`
	job         *unstructured.Unstructured
	runs        []runRow
}

// listJobs reads every BatchJob of a namespace ("" for all) and the runs that
// belong to them, and builds the views: application, then environment in
// promotion order, then name — the grouping the Console's page uses.
func listJobs(c context.Context, dc dynamic.Interface, namespace string) ([]jobView, error) {
	jobs, err := dc.Resource(batchJobGVR).Namespace(namespace).List(c, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing BatchJobs: %w", err)
	}
	runs, pods, _, err := listRuns(c, dc, namespace, labelBatchJob)
	if err != nil {
		return nil, err
	}
	rows := runRowsOf(runs, pods, now())
	attachGangs(c, dc, namespace, rows)
	views := make([]jobView, 0, len(jobs.Items))
	for i := range jobs.Items {
		views = append(views, jobViewOf(&jobs.Items[i], rows, now()))
	}
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].Application != views[j].Application {
			return views[i].Application < views[j].Application
		}
		if ri, rj := envRank(views[i].Environment), envRank(views[j].Environment); ri != rj {
			return ri < rj
		}
		return views[i].Name < views[j].Name
	})
	return views, nil
}

func envRank(env string) int {
	for i, e := range []string{"dev", "test", "prod"} {
		if e == env {
			return i
		}
	}
	return 3
}

func jobViewOf(job *unstructured.Unstructured, allRuns []runRow, at time.Time) jobView {
	p := func(path ...string) string {
		s, _, _ := unstructured.NestedString(job.Object, append([]string{"spec", "parameters"}, path...)...)
		return s
	}
	labels := job.GetLabels()
	v := jobView{
		Name:        job.GetName(),
		Namespace:   job.GetNamespace(),
		DisplayName: p("displayName"),
		Application: labels["adhar.io/application"],
		Environment: labels["adhar.io/environment"],
		Job:         p("job"),
		Image:       p("image"),
		Age:         duration.HumanDuration(at.Sub(job.GetCreationTimestamp().Time)),
		job:         job,
	}
	if v.Application == "" {
		v.Application = p("application")
	}
	if v.Application == "" {
		v.Application = job.GetName()
	}
	if v.Environment == "" {
		v.Environment = p("environment")
	}
	v.Schedule, v.TimeZone, v.Suspended = scheduleOf(job)
	if next, ok := nextRunOf(job, at); ok {
		v.NextRun = next.Format(time.RFC3339)
	}
	v.Scheduler, _, _ = unstructured.NestedString(job.Object, "status", "scheduler")
	v.Queue, _, _ = unstructured.NestedString(job.Object, "status", "queue")
	v.Message, _, _ = unstructured.NestedString(job.Object, "status", "message")
	v.LastSuccess, _, _ = unstructured.NestedString(job.Object, "status", "lastSuccessfulTime")
	v.ActiveRuns, _, _ = unstructured.NestedStringSlice(job.Object, "status", "activeRuns")
	if reason, held := heldReason(job); held {
		v.Held = reason
		if v.Held == "" {
			v.Held = "no reason given"
		}
		v.HeldSince, _, _ = unstructured.NestedString(job.Object, "status", "held", "since")
	}
	for _, r := range allRuns {
		if r.Namespace == job.GetNamespace() && r.BatchJob == job.GetName() {
			v.runs = append(v.runs, r)
		}
	}
	if len(v.runs) > 0 {
		rv := runViewOf(v.runs[0], at)
		v.LastRun = &rv
		if v.LastSuccess == "" {
			for _, r := range v.runs {
				if r.Phase == phaseSucceeded {
					v.LastSuccess = r.FinishedAt
					break
				}
			}
		}
	}
	v.Status = jobStatusOf(job, v.runs)
	v.Problem = jobProblem(job)
	for _, c := range conditionsOf(job) {
		v.Conditions = append(v.Conditions, c.Type+"="+c.Status)
	}
	return v
}

func scheduleCell(v jobView) string {
	if v.Schedule == "" {
		return "on demand"
	}
	s := v.Schedule
	if v.TimeZone != "UTC" {
		s += " " + v.TimeZone
	}
	if v.Suspended {
		s += " (suspended in Git)"
	}
	return s
}

func lastRunCell(v jobView) string {
	if v.LastRun == nil {
		return "—"
	}
	return fmt.Sprintf("%s %s", phaseCell(v.LastRun.phase), v.LastRun.Age)
}

func jobStatusCell(s jobStatus) string {
	switch s {
	case jobRunning:
		return helpers.StatePending(string(s))
	case jobFailed:
		return helpers.StateFailed(string(s))
	case jobNotReady:
		return helpers.StateDegraded(string(s))
	case jobStopped:
		return helpers.StateDisabled(string(s))
	case jobSucceeded:
		return helpers.StateReady(string(s))
	case jobDeleting:
		return helpers.StatePending(string(s))
	default:
		return helpers.StateUnknown(string(s))
	}
}
