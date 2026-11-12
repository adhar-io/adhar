/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"context"
	"fmt"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// Stop and resume are the Job API's own suspend (design 0025 §3.4): a stopped
// run's pods get SIGTERM and their grace period, the kit's graceful-stop
// lifecycle ends the step at the next chunk boundary and records STOPPED, and
// resuming starts a new pod that finds STOPPED and restarts from the last
// commit. A Volcano run (Phase 2) gets the bus.volcano.sh Command `vcctl job
// suspend` / `resume` would create instead. Exactly what the Console's Stop
// and Resume buttons send.

var stopCmd = &cobra.Command{
	Use:   "stop <run>",
	Short: "Stop a run at its next chunk boundary (suspend the Job)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return suspendOrResume(cmd, args[0], true)
	},
}

var resumeCmd = &cobra.Command{
	Use:   "resume <run>",
	Short: "Resume a stopped run from its last commit (unsuspend the Job)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return suspendOrResume(cmd, args[0], false)
	},
}

func suspendOrResume(cmd *cobra.Command, ref string, stop bool) error {
	cl, err := connect(cmd)
	if err != nil {
		return err
	}
	ns, err := resolveNamespace(ctx(cmd), cl.dc, cmd)
	if err != nil {
		return err
	}
	ns, name := parseRef(ref, ns)
	run, err := getRun(ctx(cmd), cl.dc, ns, name)
	if err != nil {
		return err
	}
	req, err := suspendRequest(run, stop)
	if err != nil {
		return err
	}
	if err := perform(ctx(cmd), cl.dc, req); err != nil {
		return err
	}
	if stop {
		helpers.Printf("%s\n", helpers.CreateSuccess(fmt.Sprintf("Stopping %s: its pod gets SIGTERM and finishes the current chunk; `adhar batch resume %s` continues it", name, name)))
	} else {
		helpers.Printf("%s\n", helpers.CreateSuccess(fmt.Sprintf("Resuming %s from its last commit (`adhar batch logs %s -f` follows it)", name, name)))
	}
	return nil
}

// suspendRequest is the write a stop or a resume makes, gated the way the
// Console gates its buttons: stop while it can still stop, resume what was
// stopped. Anything else is reported, not patched.
func suspendRequest(run *unstructured.Unstructured, stop bool) (runRequest, error) {
	phase := runPhaseOf(run)
	actions := runActionsOf(phase)
	if stop {
		if !actions.Stop {
			return runRequest{}, fmt.Errorf("%s is %s; only a pending or running run can be stopped", run.GetName(), phase)
		}
		return stopRequest(run), nil
	}
	if !actions.Resume {
		return runRequest{}, fmt.Errorf("%s is %s; only a stopped run can be resumed", run.GetName(), phase)
	}
	return resumeRequest(run), nil
}

// perform makes one runRequest against the cluster.
func perform(c context.Context, dc dynamic.Interface, r runRequest) error {
	switch r.Verb {
	case "patch":
		_, err := dc.Resource(r.GVR).Namespace(r.Namespace).Patch(c, r.Name, types.MergePatchType, []byte(toJSON(r.Body)), metav1.PatchOptions{})
		if err != nil {
			return fmt.Errorf("patching %s/%s: %w", r.Namespace, r.Name, err)
		}
	case "create":
		_, err := dc.Resource(r.GVR).Namespace(r.Namespace).Create(c, &unstructured.Unstructured{Object: r.Body}, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("creating the %s: %w", r.GVR.Resource, err)
		}
	default:
		return fmt.Errorf("unknown request verb %q", r.Verb)
	}
	return nil
}
