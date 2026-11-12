/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes"
)

var (
	logsRole   string
	logsIndex  int
	logsFollow bool
)

var logsCmd = &cobra.Command{
	Use:   "logs <run>",
	Short: "Print the pod logs of a run",
	Long: `Print the logs of a run's pods, oldest attempt first, from the batch container.
A partitioned run (Phase 2) has a manager and workers: --role picks one kind,
--index one worker. With -f the logs are followed until the run ends.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if logsRole != "" && logsRole != "manager" && logsRole != "worker" && logsRole != "single" {
			return fmt.Errorf("--role %q: expected manager or worker", logsRole)
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
		run, err := getRun(ctx(cmd), cl.dc, ns, name)
		if err != nil {
			return err
		}
		src := &podLogSource{cs: cl.cs}
		printed := map[string]bool{}
		for {
			podList, err := cl.dc.Resource(podGVR).Namespace(ns).List(ctx(cmd), metav1.ListOptions{LabelSelector: podSelectorOf(run)})
			if err != nil {
				return fmt.Errorf("listing the pods of %s: %w", name, err)
			}
			pods := selectPods(podList.Items, logsRole, cmd.Flags().Changed("index"), logsIndex)
			for _, p := range pods {
				phase, _, _ := unstructured.NestedString(p.Object, "status", "phase")
				if printed[p.GetName()] || phase == "Pending" || phase == "" {
					continue
				}
				printed[p.GetName()] = true
				if len(pods) > 1 || logsFollow {
					helpers.Printf("%s\n", helpers.CreateMuted(fmt.Sprintf("── %s ──", p.GetName())))
				}
				src.stream(ctx(cmd), ns, p.GetName(), logsFollow, os.Stdout)
			}
			if !logsFollow {
				if len(printed) == 0 {
					helpers.Printf("%s\n", helpers.CreateMuted(fmt.Sprintf("No pod of %s has started yet (-f waits for one).", name)))
				}
				return nil
			}
			// Following: the run may still be scheduling its first pod, or
			// replacing a failed one. Re-read it and look again until it ends.
			if run, err = getRun(ctx(cmd), cl.dc, ns, name); err != nil {
				return err
			}
			if runPhaseOf(run).terminal() {
				return nil
			}
			select {
			case <-ctx(cmd).Done():
				return nil
			case <-time.After(pollInterval):
			}
		}
	},
}

func init() {
	logsCmd.Flags().StringVar(&logsRole, "role", "", "Only pods of this role (manager, worker)")
	logsCmd.Flags().IntVar(&logsIndex, "index", 0, "Only the worker with this completion index")
	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "Follow the logs until the run ends")
}

// podSelectorOf is the label that ties a run's pods to it.
func podSelectorOf(run *unstructured.Unstructured) string {
	if runKindOf(run) == "VolcanoJob" {
		return labelVolcanoJob + "=" + run.GetName()
	}
	return labelNativeJob + "=" + run.GetName()
}

// selectPods keeps the pods of the asked role and index, oldest first, so the
// attempts read in the order they happened.
func selectPods(pods []unstructured.Unstructured, role string, indexSet bool, index int) []*unstructured.Unstructured {
	var out []*unstructured.Unstructured
	for i := range pods {
		p := &pods[i]
		if role != "" && podLabel(p, labelRole) != role {
			continue
		}
		if indexSet {
			idx := podLabel(p, labelNativeIndex)
			if idx == "" {
				idx = podLabel(p, labelVolcanoIndex)
			}
			if idx != fmt.Sprint(index) {
				continue
			}
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].GetCreationTimestamp().Time.Before(out[j].GetCreationTimestamp().Time)
	})
	return out
}

// podLogSource reads logs with the typed clientset.
type podLogSource struct{ cs kubernetes.Interface }

func (s *podLogSource) stream(c context.Context, ns, pod string, follow bool, out io.Writer) {
	opts := &corev1.PodLogOptions{Container: batchContainer, Follow: follow}
	stream, err := s.cs.CoreV1().Pods(ns).GetLogs(pod, opts).Stream(c)
	if err != nil {
		// A template whose container is not named `batch` (a template from a
		// future composition): let the API server pick when there is one.
		opts.Container = ""
		if stream, err = s.cs.CoreV1().Pods(ns).GetLogs(pod, opts).Stream(c); err != nil {
			fmt.Fprintln(out, helpers.CreateMuted(fmt.Sprintf("  (cannot read the logs of %s: %v)", pod, err)))
			return
		}
	}
	defer func() { _ = stream.Close() }()
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		fmt.Fprintln(out, scanner.Text())
	}
}
