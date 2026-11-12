/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"context"
	"errors"
	"fmt"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	"k8s.io/client-go/dynamic"
)

var restartCmd = &cobra.Command{
	Use:   "restart <run>",
	Short: "Restart a failed run as <run>-r<n> with the same identifying parameters",
	Long: `Restart a failed run. The new run carries the SAME identifying parameters, so
Spring Batch finds the same job instance and re-executes only the steps that
did not complete, and is named <job>-<hash8>-r<n>, the next free restart. A
scheduled run took its business date from the clock; the restart states it
explicitly so it continues that date rather than starting a new instance.`,
	Args: cobra.ExactArgs(1),
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
		created, plan, err := restartRun(ctx(cmd), cl.dc, ns, name)
		if err != nil {
			return err
		}
		if machineReadable() {
			return printMachine(map[string]interface{}{
				"run": plan.Name, "namespace": ns, "restartOf": name, "created": created,
				"params": plan.Params, "identifying": plan.Identifying,
			})
		}
		if !created {
			helpers.Printf("%s\n", helpers.CreateWarning(fmt.Sprintf("%s already exists; this restart was started before", plan.Name)))
			return nil
		}
		helpers.Printf("%s\n", helpers.CreateSuccess(fmt.Sprintf("Restarted %s as %s (`adhar batch logs %s -f` follows it)", name, plan.Name, plan.Name)))
		return nil
	},
}

// restartRun is `adhar batch restart` with the kube access behind the dynamic
// client. It reads the run, refuses anything but a failed one (the Console's
// gate), reads the job's run template, plans the restart and creates it.
func restartRun(c context.Context, dc dynamic.Interface, ns, name string) (created bool, plan *restartPlan, err error) {
	run, err := getRun(c, dc, ns, name)
	if err != nil {
		return false, nil, err
	}
	batchJob := run.GetLabels()[labelBatchJob]
	rows, _, err := runsOfJob(c, dc, ns, batchJob)
	if err != nil {
		return false, nil, err
	}
	var row *runRow
	var names []string
	for i := range rows {
		names = append(names, rows[i].Name)
		if rows[i].Name == name {
			row = &rows[i]
		}
	}
	if row == nil {
		return false, nil, fmt.Errorf("run %s is not listed among the runs of %s", name, batchJob)
	}
	if !runActionsOf(row.Phase).Restart {
		return false, nil, fmt.Errorf("%s is %s; only a failed run can be restarted (a stopped one is resumed)", name, row.Phase)
	}
	job, err := getBatchJob(c, dc, ns, batchJob)
	if err != nil {
		return false, nil, err
	}
	t, err := readRunTemplate(c, dc, job)
	if err != nil {
		return false, nil, err
	}
	plan, err = planRestart(batchJob, *row, t, names, now())
	if err != nil {
		return false, nil, err
	}
	if !machineReadable() {
		if w := restartWarning(row.ExitCode); w != "" {
			helpers.Printf("%s\n", helpers.CreateWarning(w))
		}
		if plan.ImageFrom != "" {
			helpers.Printf("%s\n", helpers.CreateMuted(fmt.Sprintf("the template's image moved: the run used %s, the restart uses %s", plan.ImageFrom, plan.ImageTo)))
		}
		if len(plan.Params) > 0 {
			helpers.Printf("%s\n", helpers.CreateMuted("parameters "+paramsLine(t.Fields, plan.Params)))
		}
	}
	_, err = createRun(c, dc, buildRun(t, plan.Name, ns, "restart", plan.Params, name))
	if errors.Is(err, errRunExists) {
		return false, plan, nil
	}
	if err != nil {
		return false, nil, err
	}
	return true, plan, nil
}
