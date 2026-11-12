/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"fmt"
	"strings"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var describeCmd = &cobra.Command{
	Use:   "describe <job>",
	Short: "Show a job's definition, parameters, schedule, conditions and last runs",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cl, err := connect(cmd)
		if err != nil {
			return err
		}
		ns, err := resolveNamespace(ctx(cmd), cl.dc, cmd)
		if err != nil {
			return err
		}
		ns, name := parseRef(args[0], ns)
		job, err := getBatchJob(ctx(cmd), cl.dc, ns, name)
		if err != nil {
			return err
		}
		runs, podsUnknown, err := runsOfJob(ctx(cmd), cl.dc, ns, name)
		if err != nil {
			return err
		}
		view := jobViewOf(job, runs, now())
		last, _ := filterRuns(runs, "", 10)
		if machineReadable() {
			views := make([]runView, 0, len(last))
			for _, r := range last {
				views = append(views, runViewOf(r, now()))
			}
			return printMachine(struct {
				jobView    `json:",inline" yaml:",inline"`
				Parameters []paramField `json:"parameters" yaml:"parameters"`
				Runs       []runView    `json:"runs" yaml:"runs"`
			}{view, declaredFields(job), views})
		}
		printDescription(job, view, last, podsUnknown)
		return nil
	},
}

// declaredFields reads the XR's own parameter declarations (the drawer needs
// no template): the same shape parameters.json carries.
func declaredFields(job *unstructured.Unstructured) []paramField {
	raw, _, _ := unstructured.NestedSlice(job.Object, "spec", "parameters", "parameters")
	var out []paramField
	for _, item := range raw {
		o, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		// Reuse the parameters.json reader, which already tolerates every
		// optional field: one normalisation for both sources.
		fields, err := parseParameterFields(toJSON([]interface{}{o}))
		if err == nil {
			out = append(out, fields...)
		}
	}
	return out
}

func printDescription(job *unstructured.Unstructured, v jobView, last []runRow, podsUnknown bool) {
	p := func(path ...string) string {
		s, _, _ := unstructured.NestedString(job.Object, append([]string{"spec", "parameters"}, path...)...)
		return s
	}
	title := v.Name
	if v.DisplayName != "" && v.DisplayName != v.Name {
		title = fmt.Sprintf("%s (%s)", v.DisplayName, v.Name)
	}
	helpers.Printf("%s\n\n", helpers.SectionHeading(helpers.IconApp, title))
	helpers.Printf("namespace      %s\n", v.Namespace)
	helpers.Printf("application    %s\n", v.Application)
	helpers.Printf("environment    %s\n", dash(v.Environment))
	helpers.Printf("job bean       %s\n", dash(v.Job))
	helpers.Printf("image          %s\n", dash(v.Image))
	helpers.Printf("schedule       %s\n", scheduleCell(v))
	helpers.Printf("next run       %s\n", dash(v.NextRun))
	scheduler := dash(v.Scheduler)
	if v.Queue != "" {
		scheduler += " (queue " + v.Queue + ")"
	}
	helpers.Printf("scheduler      %s\n", scheduler)
	helpers.Printf("resources      cpu %s, memory %s\n", dash(p("resources", "cpu")), dash(p("resources", "memory")))
	deadline, _, _ := unstructured.NestedInt64(job.Object, "spec", "parameters", "timeouts", "activeDeadlineSeconds")
	grace, _, _ := unstructured.NestedInt64(job.Object, "spec", "parameters", "timeouts", "terminationGracePeriodSeconds")
	helpers.Printf("timeouts       deadline %ds, grace %ds\n", deadline, grace)
	helpers.Printf("database       %s (schema %s)\n", dash(p("database", "secretRef")), dash(p("database", "schema")))
	helpers.Printf("last success   %s\n", dash(v.LastSuccess))
	if v.Held != "" {
		since := ""
		if v.HeldSince != "" {
			since = " since " + v.HeldSince
		}
		helpers.Printf("held           %s%s\n", helpers.StateDisabled(v.Held), since)
	}
	helpers.Printf("status         %s\n", jobStatusCell(v.Status))
	if v.Message != "" {
		helpers.Printf("message        %s\n", v.Message)
	}

	fields := declaredFields(job)
	fmt.Println()
	helpers.Printf("%s\n\n", helpers.SectionHeading(helpers.IconBullet, "Parameters"))
	if len(fields) == 0 {
		helpers.Printf("%s\n", helpers.CreateMuted("none declared: every manual run has the same identity"))
	} else {
		t := helpers.NewTable("NAME", "TYPE", "IDENTIFYING", "REQUIRED", "DEFAULT", "SOURCE", "ALLOWED", "DESCRIPTION")
		for _, f := range fields {
			def := "—"
			if f.Default != nil {
				def = *f.Default
				if def == "" {
					def = `""`
				}
			}
			t.Row(f.Name, f.Type, yesNo(f.Identifying), yesNo(f.Required), def, f.Source, dash(strings.Join(f.Allowed, ", ")), dash(f.Description))
		}
		fmt.Println(t.Render())
	}

	if conds := conditionsOf(job); len(conds) > 0 {
		fmt.Println()
		helpers.Printf("%s\n\n", helpers.SectionHeading(helpers.IconBullet, "Conditions"))
		t := helpers.NewTable("TYPE", "STATUS", "REASON", "MESSAGE")
		for _, c := range conds {
			t.Row(c.Type, conditionCell(c.Status), dash(c.Reason), dash(c.Message))
		}
		fmt.Println(t.Render())
	}

	fmt.Println()
	helpers.Printf("%s\n\n", helpers.SectionHeading(helpers.IconBullet, "Last runs"))
	if len(last) == 0 {
		helpers.Printf("%s\n", helpers.CreateMuted(fmt.Sprintf("no runs yet — `adhar batch run %s` starts one", v.Name)))
		return
	}
	views := make([]runView, 0, len(last))
	for _, r := range last {
		views = append(views, runViewOf(r, now()))
	}
	fmt.Println(runsTable(views).Render())
	if podsUnknown {
		helpers.Printf("%s\n", helpers.CreateMuted("Pods could not be read, so exit codes and outcomes are unknown rather than absent."))
	}
}

func conditionCell(status string) string {
	switch status {
	case "True":
		return helpers.StateReady(status)
	case "False":
		return helpers.StateFailed(status)
	default:
		return helpers.StateUnknown(dash(status))
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
