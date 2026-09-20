/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package stack

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var enableCmd = &cobra.Command{
	Use:   "enable <package>...",
	Short: "Turn a platform package on (edits the stack; you review and push)",
	Long: `Turn one or more platform packages on.

This edits the profile's ApplicationSet file and its mirrored environment config —
both, because a parity test requires them to agree — and then stops. Nothing is
committed, nothing is pushed and nothing reaches the cluster until you run
` + "`adhar upgrade`" + `. That is deliberate: which change lands in Git is yours to decide.

Two invariants are checked before anything is written, because both produce a
cluster that looks broken for no visible reason:

  • Mutual exclusion. Some packages claim the same cluster-scoped objects in the
    shared adhar-system namespace; enabling one offers to disable the other.
  • Dependencies. agentgateway serves adhar-ai's endpoints, so enabling it alone
    gives you a data plane with nothing behind it.

Enabling in the local profile also enables in production, since the local core must
stay a subset of production — the parity test enforces that, and discovering it
from a failing test later is worse than doing it now.`,
	Example: `  adhar stack enable harbor
  adhar stack enable adhar-ai agentgateway
  adhar stack enable openbao --yes`,
	Args:         cobra.MinimumNArgs(1),
	RunE:         func(cmd *cobra.Command, args []string) error { return runToggle(cmd, args, true) },
	SilenceUsage: true,
}

var disableCmd = &cobra.Command{
	Use:   "disable <package>...",
	Short: "Turn a platform package off (edits the stack; you review and push)",
	Long: `Turn one or more platform packages off.

Like ` + "`enable`" + `, this edits the profile's files and stops; ` + "`adhar upgrade`" + ` converges the
cluster. With prune enabled — which is the platform's default — Argo CD then
UNINSTALLS the package's workloads, so this deletes running things. PersistentVolumes
follow their reclaim policy: data can go with them.

Packages that depend on the one being disabled are named before anything is written.`,
	Example: `  adhar stack disable posthog
  adhar stack disable vllm-cpu --yes`,
	Args:         cobra.MinimumNArgs(1),
	RunE:         func(cmd *cobra.Command, args []string) error { return runToggle(cmd, args, false) },
	SilenceUsage: true,
}

func runToggle(cmd *cobra.Command, names []string, on bool) error {
	ctx := cmd.Context()
	p, source, err := detectProfile(ctx, flagStackDir)
	if err != nil {
		return err
	}
	elements, err := readElements(p.appsetFile)
	if err != nil {
		return err
	}
	known := map[string]element{}
	for _, e := range elements {
		known[e.Name] = e
	}

	// Validate every name before writing anything: a half-applied multi-package
	// edit is worse than a refusal, because the files must stay in step.
	for _, n := range names {
		if _, ok := known[n]; !ok {
			return fmt.Errorf("no package named %q in the %s profile — see `adhar stack list`", n, p.name)
		}
	}

	verb := "enable"
	if !on {
		verb = "disable"
	}

	// Which files. Enabling locally implies enabling in production, since the local
	// core must remain a subset; disabling in production implies disabling locally
	// for the same reason.
	profiles := []profile{p}
	if on && p.name == "local" {
		other, err := profileFor("production", flagStackDir)
		if err != nil {
			return err
		}
		profiles = append(profiles, other)
	}
	if !on && p.name == "production" {
		other, err := profileFor("local", flagStackDir)
		if err != nil {
			return err
		}
		if _, err := readElements(other.appsetFile); err == nil {
			profiles = append(profiles, other)
		}
	}

	fmt.Println()
	fmt.Printf("  %s %s in the %s profile (detected from %s)\n\n",
		strings.ToUpper(verb[:1])+verb[1:], strings.Join(names, ", "), p.name, source)

	// Report the consequences before asking.
	for _, n := range names {
		if on {
			if c := conflictsWith(n, elements); len(c) > 0 {
				fmt.Printf("  %s %s conflicts with the enabled package(s) %s — disable those first:\n",
					helpers.IconFailed, n, strings.Join(c, ", "))
				for _, g := range exclusiveGroups {
					if contains(g.members, n) {
						fmt.Printf("      %s\n", g.why)
					}
				}
				return fmt.Errorf("refusing to enable %s while %s is enabled: they would fight over the same objects and both report Degraded", n, strings.Join(c, ", "))
			}
			if adv := advisoriesFor(n, elements); len(adv) > 0 {
				fmt.Printf("  %s %s overlaps with the enabled %s; both will run, but the platform's consumers are wired to one of them.\n",
					helpers.IconDegraded, n, strings.Join(adv, ", "))
			}
			if missing := missingDependencies(n, elements); len(missing) > 0 {
				fmt.Printf("  %s %s needs %s, which %s not enabled — add them to this command:\n",
					helpers.IconDegraded, n, strings.Join(missing, ", "), plural(len(missing)))
				fmt.Printf("      adhar stack enable %s %s\n\n", n, strings.Join(missing, " "))
			}
		} else {
			if deps := dependentsOf(n, elements); len(deps) > 0 {
				fmt.Printf("  %s %s is still enabled and depends on %s; it will stop working.\n",
					helpers.IconDegraded, strings.Join(deps, ", "), n)
			}
			if known[n].on() {
				fmt.Printf("  %s Argo CD prunes what it removes: disabling %s UNINSTALLS its workloads.\n", helpers.IconDegraded, n)
			}
		}
	}

	if !flagYes && !confirm(fmt.Sprintf("Edit %d file(s) to %s %s?", len(profiles)*2, verb, strings.Join(names, ", "))) {
		fmt.Println("\n  nothing changed")
		fmt.Println()
		return nil
	}

	var edited []string
	for _, prof := range profiles {
		for _, file := range []string{prof.appsetFile, prof.envFile} {
			for _, n := range names {
				changed, err := setEnabled(file, n, on)
				if err != nil {
					// A package wired in the appset but absent from the environment
					// config is a pre-existing inconsistency, not this command's
					// doing — say so and carry on rather than leaving a half edit.
					fmt.Printf("  %s %s: %v\n", helpers.IconDegraded, filepath.Base(file), err)
					continue
				}
				if changed {
					edited = append(edited, fmt.Sprintf("%s (%s)", file, n))
				}
			}
		}
	}

	fmt.Println()
	if len(edited) == 0 {
		fmt.Printf("  already %sd — no file changed\n\n", verb)
		return nil
	}
	fmt.Println(helpers.SectionHeading("✏️", fmt.Sprintf("%d edit(s)", len(edited))))
	for _, e := range edited {
		fmt.Printf("    %s\n", e)
	}
	fmt.Println()
	fmt.Println("  → Review:   git diff -- " + flagStackDir)
	fmt.Println("  → Converge: adhar upgrade --diff-only   then   adhar upgrade")
	if !on {
		fmt.Println("  → Argo CD will prune the package's resources on the next sync.")
	}
	fmt.Println()
	return nil
}

func plural(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

func confirm(question string) bool {
	fi, err := os.Stdin.Stat()
	if err != nil || (fi.Mode()&os.ModeCharDevice) == 0 {
		// Non-interactive: refuse rather than assume. A CI job that meant to edit
		// the stack can say --yes.
		fmt.Printf("  %s not a terminal; pass --yes to proceed\n", helpers.IconDegraded)
		return false
	}
	fmt.Printf("  %s [y/N] ", question)
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(sc.Text()))
	return answer == "y" || answer == "yes"
}

// ---------------------------------------------------------------------------
// sync
// ---------------------------------------------------------------------------

var syncCmd = &cobra.Command{
	Use:     "sync [package]...",
	Aliases: []string{"refresh"},
	Short:   "Ask Argo CD to re-compare a package now instead of at the next resync",
	Long: `Request a hard refresh of one or more packages' Argo CD Applications.

Argo CD re-compares on its own interval; this annotates the Application so it
happens now. It is the right tool after pushing a change to the stack repo, or when
an Application is parked at Unknown after a first-comparison failure.

It does NOT force a sync of a stuck operation: an Application waiting on a health
check replays the revision it started with, and clearing that needs its operation
terminated (see TROUBLESHOOTING). With no arguments, every package that is not
Synced and Healthy is refreshed.`,
	RunE:         runSync,
	SilenceUsage: true,
}

func runSync(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	cfg, err := helpers.GetKubeConfig()
	if err != nil {
		return fmt.Errorf("no cluster to talk to: %w", err)
	}
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}

	targets := args
	if len(targets) == 0 {
		rows, _, _, err := gather(ctx)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.Enabled && !(r.Sync == "Synced" && r.Health == "Healthy") {
				targets = append(targets, r.Name)
			}
		}
		if len(targets) == 0 {
			fmt.Println("\n  every enabled package is already Synced and Healthy")
			fmt.Println()
			return nil
		}
	}

	fmt.Println()
	for _, name := range targets {
		patch := []byte(`{"metadata":{"annotations":{"argocd.argoproj.io/refresh":"hard"}}}`)
		if _, err := dc.Resource(gvr).Namespace(flagNamespace).Patch(ctx, name, "application/merge-patch+json", patch, metav1.PatchOptions{}); err != nil {
			fmt.Printf("  %s %s: %v\n", helpers.IconFailed, name, err)
			continue
		}
		fmt.Printf("  %s %s refresh requested\n", helpers.IconReady, name)
	}
	fmt.Printf("\n  Watch it:  adhar stack status\n\n")
	return nil
}

// ---------------------------------------------------------------------------
// conflicts
// ---------------------------------------------------------------------------

var conflictsCmd = &cobra.Command{
	Use:   "conflicts",
	Short: "Show which packages must not be enabled together, and whether any are",
	Long: `Show the catalogue's mutual-exclusion rules and dependencies, and check the current
profile against them.

Every package installs into the one shared adhar-system namespace (ADR-0011), so
some pairs claim the same cluster-scoped object — the same ClusterSecretStore, the
same Service, the same Deployment name. Two Applications fighting over one object
both report Degraded and neither says why, which is a genuinely hard failure to
read; this is the list that explains it.`,
	RunE:         runConflicts,
	SilenceUsage: true,
}

func runConflicts(cmd *cobra.Command, _ []string) error {
	p, _, err := detectProfileWrapper(cmd.Context())
	if err != nil {
		return err
	}
	elements, err := readElements(p.appsetFile)
	if err != nil {
		return err
	}
	on := map[string]bool{}
	for _, e := range elements {
		on[e.Name] = e.on()
	}

	fmt.Println()
	fmt.Println(helpers.SectionHeading("⚠️", fmt.Sprintf("Exclusion rules · %s profile", p.name)))
	fmt.Println()
	t := helpers.NewTable("GROUP", "STATE", "WHY THEY COLLIDE")
	violations := 0
	for _, g := range exclusiveGroups {
		var enabled []string
		for _, m := range g.members {
			if on[m] {
				enabled = append(enabled, m)
			}
		}
		state := helpers.StateReady(fmt.Sprintf("ok (%s)", describeEnabled(enabled)))
		if len(enabled) > 1 {
			state = helpers.StateFailed("both enabled: " + strings.Join(enabled, " + "))
			violations++
		}
		t.Row(strings.Join(g.members, " / "), state, g.why)
	}
	fmt.Println(t.Render())

	fmt.Println()
	fmt.Println(helpers.SectionHeading("↔", "Overlaps (advisory — both may run)"))
	fmt.Println()
	a := helpers.NewTable("GROUP", "STATE", "WHY IT IS ADVISORY")
	for _, g := range advisoryGroups {
		var enabled []string
		for _, m := range g.members {
			if on[m] {
				enabled = append(enabled, m)
			}
		}
		state := helpers.StateReady(fmt.Sprintf("ok (%s)", describeEnabled(enabled)))
		if len(enabled) > 1 {
			state = helpers.StateDegraded("both enabled: " + strings.Join(enabled, " + "))
		}
		a.Row(strings.Join(g.members, " / "), state, g.why)
	}
	fmt.Println(a.Render())

	fmt.Println()
	fmt.Println(helpers.SectionHeading("🔗", "Dependencies"))
	fmt.Println()
	d := helpers.NewTable("PACKAGE", "NEEDS", "STATE")
	for pkg, deps := range dependencies {
		if !on[pkg] {
			continue
		}
		missing := missingDependencies(pkg, elements)
		state := helpers.StateReady("satisfied")
		if len(missing) > 0 {
			state = helpers.StateDegraded("missing: " + strings.Join(missing, ", "))
			violations++
		}
		d.Row(pkg, strings.Join(deps, ", "), state)
	}
	if d.Rows() == 0 {
		fmt.Println("  (no packages with dependencies are enabled)")
	} else {
		fmt.Println(d.Render())
	}

	fmt.Println()
	if violations == 0 {
		fmt.Printf("  %s no violations in this profile\n\n", helpers.IconReady)
		return nil
	}
	fmt.Printf("  %d problem(s) — fix with `adhar stack disable <package>`, then `adhar upgrade`\n\n", violations)
	return nil
}

func describeEnabled(enabled []string) string {
	if len(enabled) == 0 {
		return "none enabled"
	}
	return enabled[0]
}

// detectProfileWrapper keeps the signature small for callers that do not need the
// detection source.
func detectProfileWrapper(ctx context.Context) (profile, string, error) {
	p, source, err := detectProfile(ctx, flagStackDir)
	return p, source, err
}
