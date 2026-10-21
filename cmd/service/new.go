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

// newServiceCmd triggers the platform's "new-service" Tekton pipeline, which
// creates the Deployment and Service for a service. It does not build: the image
// is built, scanned and signed by the app-ci pipeline on pushes to the service's
// Gitea repository. The Console triggers the same pipeline.
var newServiceCmd = &cobra.Command{
	Use:   "new",
	Short: "Create the Deployment and Service for a service built by app-ci",
	Long: `Create the runtime objects for a service via the platform's 'new-service' pipeline.

It creates a Deployment and a Service named <name> in adhar-system running
harbor-core.adhar-system.svc.cluster.local/library/<name>:latest. It does NOT
build the image: the app-ci pipeline builds, scans and signs it on pushes to the
service's repository in the platform's Gitea. --git-url and --subpath are
recorded in the run's log only. This is the same pipeline the Adhar Console uses.

Examples:
  adhar service new --name api --git-url https://github.com/paketo-buildpacks/samples --subpath go/mod
  adhar service new --name web --git-url https://gitea.adhar.localtest.me:8443/adhar/web.git --wait`,
	RunE: runNewService,
}

func init() {
	newServiceCmd.Flags().StringVar(&newName, "name", "", "Service name (required)")
	newServiceCmd.Flags().StringVar(&newGitURL, "git-url", "", "Git URL of the service's source repository; recorded in the run log (required)")
	newServiceCmd.Flags().StringVar(&newSubpath, "subpath", "", "Sub-path within the repository; recorded in the run log")
	newServiceCmd.Flags().BoolVar(&newWait, "wait", false, "Wait for the new-service pipeline run to finish (not for the pod to be healthy)")
	_ = newServiceCmd.MarkFlagRequired("name")
	_ = newServiceCmd.MarkFlagRequired("git-url")
}

func runNewService(cmd *cobra.Command, args []string) error {
	return TriggerNewService(cmd.Context(), newName, newGitURL, newSubpath, newWait)
}

// TriggerNewService starts the platform's "new-service" Tekton pipeline, which
// creates the service's Deployment and Service (it does not build the image). Shared by `adhar service new` and the
// top-level `adhar push`, so both take the identical path.
func TriggerNewService(ctx context.Context, name, gitURL, subpath string, wait bool) error {
	dc, err := helpers.DynamicClient()
	if err != nil {
		fmt.Println(helpers.ErrorStyle.Render("✖ Could not connect to the cluster"))
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
		fmt.Println(helpers.ErrorStyle.Render("✖ Failed to start the new-service pipeline"))
		fmt.Println(helpers.CreateMuted("   Is the adhar-supply-chain package enabled? (Tekton + Harbor)"))
		return fmt.Errorf("creating new-service PipelineRun: %w", err)
	}

	runName := created.GetName()
	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("Started new-service for %q (PipelineRun %s)", name, runName)))
	fmt.Println(helpers.CreateMuted(fmt.Sprintf("   Creates Deployment + Service %q in %s running library/%s:latest.", name, globals.AdharSystemNamespace, name)))
	fmt.Println(helpers.CreateMuted("   The image is built by app-ci on pushes to the service's Gitea repository; until then the pod cannot pull it."))

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
						fmt.Println(helpers.SuccessStyle.Render("new-service pipeline succeeded"))
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
