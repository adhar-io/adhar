/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

// Package stack is the platform half of the application split.
//
// `adhar application` manages what YOU deploy — Application XRs, the
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
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/stack"
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
	Short:   "▣ Manage the platform's own packages — list, enable, disable, sync",
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

  # An editable copy of the stack built into this binary
  adhar stack export ./stack

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
	p.StringVar(&flagStackDir, "stack-dir", "", "Platform stack directory (default: the stack built into adhar, or ./platform/stack inside a source checkout)")
	p.StringVarP(&flagNamespace, "namespace", "n", "adhar-system", "Namespace the platform runs in")
	p.BoolVar(&flagJSON, "json", false, "Machine-readable output")

	enableCmd.Flags().BoolVarP(&flagYes, "yes", "y", false, "Do not ask for confirmation")
	disableCmd.Flags().BoolVarP(&flagYes, "yes", "y", false, "Do not ask for confirmation")

	StackCmd.AddCommand(listCmd, statusCmd, describeCmd, enableCmd, disableCmd, syncCmd, conflictsCmd, exportCmd)
}

// stackSource is where this invocation's stack came from; set by useStack.
var stackSource stack.Source

// useStack resolves the stack once per invocation and points flagStackDir at it:
// --stack-dir or $ADHAR_STACK_DIR when given, the working tree inside a source
// checkout, otherwise the stack built into the binary. Every subcommand reads
// files through flagStackDir, so none of them needs a clone of the repository.
func useStack() error {
	if stackSource.Dir != "" {
		return nil
	}
	src, err := stack.Resolve(flagStackDir)
	if err != nil {
		return err
	}
	stackSource, flagStackDir = src, src.Dir
	return nil
}

// requireEditableStack refuses to edit the binary's own stack. The extraction
// in the user cache is rewritten whenever it differs from what was built in, so
// an edit there would be undone before `adhar upgrade` could push it — a toggle
// that reports success and changes nothing.
func requireEditableStack(verb string) error {
	if !stackSource.Embedded() {
		return nil
	}
	return fmt.Errorf(`the stack built into adhar is read-only, so there is nothing to %s here.

  Write an editable copy and work on that:

    adhar stack export ./stack
    export %s=$PWD/stack        # every adhar command now uses it
    adhar stack %s <package>
    adhar upgrade

  Or toggle the package in the Console's Marketplace, which writes to the
  platform's GitOps repository directly`, verb, stack.EnvStackDir, verb)
}

var exportCmd = &cobra.Command{
	Use:   "export <directory>",
	Short: "Write the stack built into this binary to a directory you can edit",
	Long: `Write the platform stack built into this adhar binary — every package, every
environment and the ApplicationSets — into a new directory.

The CLI needs no copy of the stack on disk: ` + "`adhar up`" + ` and ` + "`adhar upgrade`" + ` use the
one they were built with. Export it when you want to CHANGE it: enable or
disable packages, edit a package's manifests, or keep the stack in your own Git
repository. Point adhar at the copy with --stack-dir or $` + stack.EnvStackDir + `.`,
	Example: `  adhar stack export ./stack
  export ` + stack.EnvStackDir + `=$PWD/stack
  adhar stack enable harbor
  adhar upgrade --diff-only`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := filepath.Abs(args[0])
		if err != nil {
			return err
		}
		if err := stack.Export(dir); err != nil {
			return err
		}
		files, _ := stack.Files()
		helpers.Printf("%s Wrote the platform stack (%d files) to %s\n", helpers.IconReady, len(files), dir)
		helpers.Printf("  → Use it:  export %s=%s\n", stack.EnvStackDir, dir)
		return nil
	},
}
