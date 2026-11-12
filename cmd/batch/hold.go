/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"context"
	"fmt"
	"strings"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// Hold and release are the ONE change an operator makes on a BatchJob (design
// 0025 §3.5): the adhar.io/batch-hold annotation, which the composition turns
// into a suspended CronJob. It is audited, it needs no commit, Argo CD does not
// revert it (the annotation is not in Git), and adhar-batch-hold-only refuses
// any other change by a person. Exactly the patch the Console's Hold sends.

var holdReason string

var holdCmd = &cobra.Command{
	Use:   "hold <job> --reason <why>",
	Short: "Hold a job's schedule without a commit",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(holdReason) == "" {
			return fmt.Errorf("--reason is required: the hold is audited, and the reason is what the next person reads")
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
		previous, err := setHold(ctx(cmd), cl.dc, ns, name, &holdReason)
		if err != nil {
			return err
		}
		if previous != nil {
			helpers.Printf("%s\n", helpers.CreateSuccess(fmt.Sprintf("%s stays on hold; the reason is now %q (was %q)", name, strings.TrimSpace(holdReason), *previous)))
			return nil
		}
		helpers.Printf("%s\n", helpers.CreateSuccess(fmt.Sprintf("%s is on hold: %s. Its schedule is suspended until `adhar batch release %s`; runs can still be started by hand", name, strings.TrimSpace(holdReason), name)))
		return nil
	},
}

var releaseCmd = &cobra.Command{
	Use:   "release <job>",
	Short: "Release a held job's schedule",
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
		previous, err := setHold(ctx(cmd), cl.dc, ns, name, nil)
		if err != nil {
			return err
		}
		if previous == nil {
			helpers.Printf("%s\n", helpers.CreateMuted(fmt.Sprintf("%s was not on hold; nothing to release", name)))
			return nil
		}
		helpers.Printf("%s\n", helpers.CreateSuccess(fmt.Sprintf("%s released (was held: %s); its schedule resumes", name, *previous)))
		return nil
	},
}

func init() {
	holdCmd.Flags().StringVar(&holdReason, "reason", "", "Why the schedule is held (required; shown on the job and in the Console)")
}

// setHold reads the job so the answer can say what the hold was, then sends
// the merge patch: the reason, or null to remove the annotation.
func setHold(c context.Context, dc dynamic.Interface, ns, name string, reason *string) (previous *string, err error) {
	job, err := getBatchJob(c, dc, ns, name)
	if err != nil {
		return nil, err
	}
	if r, held := job.GetAnnotations()[annotationHold]; held {
		previous = &r
	}
	if reason == nil && previous == nil {
		return nil, nil
	}
	patch := toJSON(holdPatch(reason))
	if _, err := dc.Resource(batchJobGVR).Namespace(ns).Patch(c, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return nil, fmt.Errorf("patching BatchJob %s/%s: %w (a hold needs patch on batchjobs; adhar-batch-hold-only admits only this annotation)", ns, name, err)
	}
	return previous, nil
}
