/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

// Package push provides `adhar push`, which starts the platform's `new-service`
// pipeline: it creates the Deployment and Service for a service whose image the
// app-ci pipeline builds (buildpacks → scan → sign → Harbor). It does not build.
package push

import (
	"fmt"

	"adhar-io/adhar/cmd/service"

	"github.com/spf13/cobra"
)

var (
	gitURL  string
	subpath string
	wait    bool
)

// PushCmd is the top-level `adhar push` command (cf push-style).
var PushCmd = &cobra.Command{
	Use:   "push <name>",
	Short: "Create the Deployment and Service for a service built by app-ci",
	Long: `Create the runtime objects for a service whose image the platform builds.

'adhar push' starts the platform's 'new-service' Tekton pipeline. It creates a
Deployment and a Service named <name> in adhar-system that run
harbor-core.adhar-system.svc.cluster.local/library/<name>:latest.

It does NOT build the image. Builds are done by the app-ci pipeline, which runs
on every push to the service's repository in the platform's Gitea (buildpacks →
Trivy scan → Cosign signature → Harbor). Until app-ci has pushed the image, the
pod cannot pull it. --git-url and --subpath are recorded in the run's log only.

For a new service, the end-to-end path is a golden-path template, which creates
the Gitea repository (so app-ci builds every push) and the Application:
  adhar application deploy <name> --template <t> --path manifests

Examples:
  adhar push web --git-url https://gitea.adhar.localtest.me:8443/adhar/web.git --wait
  kubectl -n adhar-system get deploy,pods -l app=web    # check the pod started

Related:
  adhar application deploy <name> --template <t>   scaffold a repo + Application (recommended)
  adhar service new ...                            the same pipeline under 'service'`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if gitURL == "" {
			return fmt.Errorf("--git-url is required (the service's source repository)")
		}
		// Route through the shared supply-chain orchestration — identical to
		// `adhar service new` and the Console.
		return service.TriggerNewService(cmd.Context(), name, gitURL, subpath, wait)
	},
}

func init() {
	PushCmd.Flags().StringVar(&gitURL, "git-url", "", "Git URL of the service's source repository; recorded in the run log (required)")
	PushCmd.Flags().StringVar(&subpath, "subpath", "", "Sub-path within the repository; recorded in the run log")
	PushCmd.Flags().BoolVar(&wait, "wait", false, "Wait for the new-service pipeline run to finish (not for the pod to be healthy)")
}
