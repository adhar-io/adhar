/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package service

import (
	"context"
	"fmt"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var (
	newName    string
	newGitURL  string
	newSubpath string
	newWait    bool
)

// pipelineRunGVR is the Tekton PipelineRun resource.
var pipelineRunGVR = schema.GroupVersionResource{Group: "tekton.dev", Version: "v1", Resource: "pipelineruns"}

// newServiceCmd triggers the platform supply-chain "new-service" orchestration:
// scaffold → build (buildpacks → Harbor, signed) → deploy (kyverno-gated). One
// command drives the whole build+deploy leg through the platform's Tekton
// supply chain, so a developer goes from source to a running, signed service
// without touching kpack/Tekton/Harbor directly. The Console triggers the same
// pipeline, so both surfaces share one orchestration.
var newServiceCmd = &cobra.Command{
	Use:   "new",
	Short: "Create a new service from source — scaffold, build (signed), and deploy via the supply chain",
	Long: `Create a new service end to end through the Adhar supply chain.

Given a name and a Git source, this triggers the platform's 'new-service' pipeline
which builds the source with buildpacks, pushes a SIGNED image to Harbor, and
deploys it (the Deployment only starts once a signed image exists — kyverno-gated).
This is the same orchestration the Adhar Console uses.

Examples:
  adhar service new --name api --git-url https://github.com/paketo-buildpacks/samples --subpath go/mod
  adhar service new --name web --git-url https://gitea.adhar.localtest.me:8443/adhar/web.git --wait`,
	RunE: runNewService,
}

func init() {
	newServiceCmd.Flags().StringVar(&newName, "name", "", "Service name (required)")
	newServiceCmd.Flags().StringVar(&newGitURL, "git-url", "", "Git URL of the service source to build (required)")
	newServiceCmd.Flags().StringVar(&newSubpath, "subpath", "", "Sub-path within the repo that holds the buildable source")
	newServiceCmd.Flags().BoolVar(&newWait, "wait", false, "Wait for the build+deploy pipeline to complete")
	_ = newServiceCmd.MarkFlagRequired("name")
	_ = newServiceCmd.MarkFlagRequired("git-url")
}

func runNewService(cmd *cobra.Command, args []string) error {
	return TriggerNewService(cmd.Context(), newName, newGitURL, newSubpath, newWait)
}

// TriggerNewService drives the platform supply-chain "new-service" orchestration
// (scaffold → buildpacks build → signed image to Harbor → kyverno-gated deploy)
// by creating a Tekton PipelineRun. Shared by `adhar service new` and the
// top-level `adhar push`, so both take the identical path.
func TriggerNewService(ctx context.Context, name, gitURL, subpath string, wait bool) error {
	dc, err := helpers.DynamicClient()
	if err != nil {
		fmt.Println(helpers.ErrorStyle.Render("❌ Could not connect to the cluster"))
		fmt.Println(helpers.CreateMuted("   Is the platform up? Try `adhar up`."))
		return err
	}

	pr := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "tekton.dev/v1",
		"kind":       "PipelineRun",
		"metadata": map[string]interface{}{
			"generateName": name + "-",
			"namespace":    globals.AdharSystemNamespace,
			"labels": map[string]interface{}{
				"adhar.io/service": name,
			},
		},
		"spec": map[string]interface{}{
			"pipelineRef": map[string]interface{}{"name": "new-service"},
			"params": []interface{}{
				map[string]interface{}{"name": "name", "value": name},
				map[string]interface{}{"name": "git-url", "value": gitURL},
				map[string]interface{}{"name": "subpath", "value": subpath},
			},
		},
	}}

	created, err := dc.Resource(pipelineRunGVR).Namespace(globals.AdharSystemNamespace).Create(ctx, pr, metav1.CreateOptions{})
	if err != nil {
		fmt.Println(helpers.ErrorStyle.Render("❌ Failed to start the new-service pipeline"))
		fmt.Println(helpers.CreateMuted("   Is the supply-chain package enabled? (Tekton + kpack + Harbor)"))
		return fmt.Errorf("creating new-service PipelineRun: %w", err)
	}

	runName := created.GetName()
	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("Started build+deploy of %q (PipelineRun %s)", name, runName)))
	fmt.Println(helpers.CreateMuted("   buildpacks → Harbor (signed) → kyverno-gated Deployment + Service"))

	if !wait {
		fmt.Println(helpers.CreateMuted(fmt.Sprintf("   Track it: kubectl -n %s get pipelinerun %s -w", globals.AdharSystemNamespace, runName)))
		return nil
	}

	return waitForPipelineRun(ctx, dc, runName)
}

// waitForPipelineRun polls the PipelineRun until it succeeds or fails.
func waitForPipelineRun(ctx context.Context, dc dynamic.Interface, runName string) error {
	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		pr, err := dc.Resource(pipelineRunGVR).Namespace(globals.AdharSystemNamespace).Get(ctx, runName, metav1.GetOptions{})
		if err == nil {
			conds, _, _ := unstructured.NestedSlice(pr.Object, "status", "conditions")
			for _, c := range conds {
				cond, ok := c.(map[string]interface{})
				if !ok {
					continue
				}
				if cond["type"] == "Succeeded" {
					switch cond["status"] {
					case "True":
						fmt.Println(helpers.SuccessStyle.Render("Build+deploy pipeline succeeded"))
						return nil
					case "False":
						return fmt.Errorf("pipeline failed: %v", cond["message"])
					}
				}
			}
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("timed out waiting for pipeline %s", runName)
}
