/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

// Package push provides the CloudFoundry-style `adhar push` — a single command
// that takes a developer's service from source to a running, signed deployment
// through the Adhar supply chain (buildpacks → Harbor → GitOps deploy).
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
	Short: "Build and deploy a service from source in one command (cf push-style)",
	Long: `Push a service from source to a running, signed deployment in one command.

'adhar push' triggers the platform supply chain: it builds your source with
buildpacks (no Dockerfile required), pushes a SIGNED image to Harbor, and deploys
it — the Deployment only starts once a signed image exists (kyverno-gated). The
Console drives the same pipeline, so developers and operators share one path.

Examples:
  adhar push api --git-url https://github.com/paketo-buildpacks/samples --subpath go/mod
  adhar push web --git-url https://gitea.adhar.localtest.me:8443/adhar/web.git --wait

Related:
  adhar apps deploy <name> --template <t>   deploy from a Gitea template (no build)
  adhar service new ...                      the same build+deploy flow under 'service'`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if gitURL == "" {
			return fmt.Errorf("--git-url is required (the source repo to build)")
		}
		// Route through the shared supply-chain orchestration — identical to
		// `adhar service new` and the Console.
		return service.TriggerNewService(cmd.Context(), name, gitURL, subpath, wait)
	},
}

func init() {
	PushCmd.Flags().StringVar(&gitURL, "git-url", "", "Git URL of the service source to build (required)")
	PushCmd.Flags().StringVar(&subpath, "subpath", "", "Sub-path within the repo that holds the buildable source")
	PushCmd.Flags().BoolVar(&wait, "wait", false, "Wait for the build+deploy pipeline to complete")
}
