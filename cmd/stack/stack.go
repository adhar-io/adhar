/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

// Package stack is the platform half of the application split.
//
// `adhar application` manages what YOU deploy — CompositeApplication XRs, the
// golden paths, your services. `adhar stack` manages what the PLATFORM is made of:
// the ~90 curated packages (Argo CD, Keycloak, Harbor, Tekton, Grafana, the data
// and AI stacks) that arrive with `adhar up`. They are different things with
// different lifecycles and different blast radii, and one command listing both was
// the reason "which of these can I turn off?" had no answer.
//
// ENABLING A PACKAGE IS A GIT CHANGE, NOT A CLUSTER CALL. The platform
// ApplicationSet is applied by the controller from the stack on disk and re-applied
// every reconcile, so a `kubectl edit` of it is reverted within the minute —
// there is no live switch to flip. So `adhar stack enable/disable` edits the
// SOURCE: the profile's ApplicationSet file and its mirrored environment config,
// both of which a parity test requires to agree. Then `adhar upgrade` converges the
// cluster. That is the platform's own workflow (CUSTOMIZATION §1), which this
// command performs correctly rather than describing.
package stack

import (
	"github.com/spf13/cobra"
)

var (
	flagProfile   string
	flagStackDir  string
	flagNamespace string
	flagJSON      bool
	flagYes       bool
)

// StackCmd is `adhar stack`.
var StackCmd = &cobra.Command{
	Use:     "stack",
	Aliases: []string{"platform", "packages"},
	Short:   "📦 Manage the platform's own packages — list, enable, disable, sync",
	Long: `Manage the packages the platform itself is made of.

Adhar ships a curated catalogue of ~90 CNCF and open-source packages, delivered by
Argo CD from the platform stack. This command is how you see what is in it, what
state each package is in, and how you turn one on or off.

Enabling and disabling are Git changes. The platform ApplicationSet is applied by
the controller from the stack on disk and re-applied on every reconcile, so there
is no live switch: ` + "`adhar stack enable`" + ` edits the profile's ApplicationSet file and
its mirrored environment config — both, because a parity test requires them to
agree — and ` + "`adhar upgrade`" + ` then converges the cluster. Nothing is pushed for you;
the diff is yours to review.

For your own applications, use ` + "`adhar application`" + `.`,
	Example: `  # What is in the platform, and what state is it in?
  adhar stack list
  adhar stack list --category data --enabled

  # One package, in detail: its contract, its live state, its resources
  adhar stack describe keycloak

  # Turn something on (then review the diff and run adhar upgrade)
  adhar stack enable harbor
  adhar stack disable posthog

  # Packages that must not both be on
  adhar stack conflicts

  # Nudge Argo CD at one package
  adhar stack sync metabase`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// A bare `adhar stack` is "what is in my platform?".
		return runList(cmd, args)
	},
	SilenceUsage: true,
}

func init() {
	p := StackCmd.PersistentFlags()
	p.StringVar(&flagProfile, "profile", "", "Stack profile to edit: local or production (default: detected from the cluster)")
	p.StringVar(&flagStackDir, "stack-dir", "platform/stack", "Path to the platform stack directory")
	p.StringVarP(&flagNamespace, "namespace", "n", "adhar-system", "Namespace the platform runs in")
	p.BoolVar(&flagJSON, "json", false, "Machine-readable output")

	enableCmd.Flags().BoolVarP(&flagYes, "yes", "y", false, "Do not ask for confirmation")
	disableCmd.Flags().BoolVarP(&flagYes, "yes", "y", false, "Do not ask for confirmation")

	StackCmd.AddCommand(listCmd, statusCmd, describeCmd, enableCmd, disableCmd, syncCmd, conflictsCmd)
}
