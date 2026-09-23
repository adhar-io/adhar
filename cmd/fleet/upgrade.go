/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package fleet

// `adhar fleet upgrade` — move every workload cluster to a target Kubernetes
// version in canary waves, gated on health, with optional rollback.
//
// The mechanism is deliberately GitOps-shaped: the upgrade edits
// `spec.infrastructure.version` on each DataPlane and lets the DataPlane
// controller do the work. This CLI never talks to a cloud API, never touches a
// node, and never applies anything to a workload cluster directly — so a fleet
// upgrade is a sequence of small, auditable spec changes that can be inspected,
// reverted, and replayed. A CLI that drove cloud upgrades itself would be a
// second control plane competing with the controller.
//
// What this adds over editing the specs by hand is the discipline: an order that
// puts the least-loaded plane first, a pause and a health check between batches,
// and a stop (with rollback) the moment a healthy plane stops being healthy.

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	targetVersion   string
	canaryCount     int
	batchSize       int
	includeNotReady bool
	dryRun          bool
	assumeYes       bool
	waveTimeout     time.Duration
	settleDelay     time.Duration
	rollbackOnFail  bool
)

var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Roll a Kubernetes version across the fleet in canary waves",
	Long: `Upgrades every registered workload cluster to --to-version, in waves.

  wave 1        the canary: one plane, the least loaded
  wave 2..n     progressively, --batch-size at a time
  between each  wait for the planes to report Ready again, then decide

The upgrade edits spec.infrastructure.version on each DataPlane; the DataPlane
controller performs the actual upgrade. Nothing here touches a cloud API or a
node directly, so every step is an auditable spec change you can inspect or
revert yourself.

Stops as soon as a plane that WAS Ready stops being Ready. A plane that was
already unhealthy before the wave does not stop the rollout — otherwise one
long-broken cluster blocks the fleet forever, which is how gates end up disabled.

Examples:
  adhar fleet upgrade --to-version v1.32.0 --dry-run
  adhar fleet upgrade --to-version v1.32.0 --canary 1 --batch-size 3
  adhar fleet upgrade --to-version v1.32.0 --selector region=eu --rollback-on-failure`,
	RunE: runUpgrade,
}

func init() {
	f := upgradeCmd.Flags()
	f.StringVar(&targetVersion, "to-version", "", "Target Kubernetes version (required)")
	f.IntVar(&canaryCount, "canary", 1, "Planes in the first wave")
	f.IntVar(&batchSize, "batch-size", 0, "Planes per wave after the canary (default: a quarter of the fleet)")
	f.BoolVar(&includeNotReady, "include-not-ready", false, "Also upgrade planes that are not currently Ready")
	f.BoolVar(&dryRun, "dry-run", false, "Print the wave plan and exit without changing anything")
	f.BoolVarP(&assumeYes, "yes", "y", false, "Skip the confirmation prompt")
	f.DurationVar(&waveTimeout, "wave-timeout", 20*time.Minute, "How long to wait for a wave to become Ready")
	f.DurationVar(&settleDelay, "settle", 30*time.Second, "Grace period before polling a wave, so the controller can mark planes NotReady")
	f.BoolVar(&rollbackOnFail, "rollback-on-failure", false, "Restore the previous version on planes that regressed")
}

func runUpgrade(cmd *cobra.Command, args []string) error {
	if targetVersion == "" {
		return fmt.Errorf("--to-version is required (e.g. --to-version v1.32.0)")
	}
	ctx := cmd.Context()
	c, err := newClient()
	if err != nil {
		return err
	}
	sel, err := parseSelector(selectorFlag)
	if err != nil {
		return err
	}

	planes, err := snapshot(ctx, c)
	if err != nil {
		return err
	}
	waves, skipped, err := Plan(planes, PlanOptions{
		Canary:          canaryCount,
		BatchSize:       batchSize,
		Selector:        sel,
		IncludeNotReady: includeNotReady,
	})
	if err != nil {
		return err
	}
	if len(waves) == 0 {
		fmt.Println(helpers.SectionHeading("🌍", "Fleet upgrade"))
		fmt.Println("  Nothing to upgrade — no plane matched, or none is Ready.")
		if len(skipped) > 0 {
			fmt.Print(Describe(nil, skipped))
		}
		return nil
	}

	fmt.Println(helpers.SectionHeading("🌍", fmt.Sprintf("Fleet upgrade → %s", targetVersion)))
	fmt.Print(Describe(waves, skipped))
	fmt.Println()

	if dryRun {
		fmt.Printf("  %s\n\n", helpers.SubtitleStyle.Render("--dry-run: nothing was changed"))
		return nil
	}
	if !assumeYes {
		// Same prompt shape as `adhar upgrade`: a fleet upgrade should not be a
		// different interaction from a platform upgrade.
		fmt.Printf("Upgrade %d plane(s) to %s? [y/N]: ", countPlanes(waves), targetVersion)
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			fmt.Println("Aborted; no plane was changed.")
			return nil
		}
	}

	// Remember each plane's version so a rollback has something to restore.
	previous := map[string]string{}
	for _, w := range waves {
		for _, p := range w.Planes {
			var dp v1alpha1.DataPlane
			if err := c.Get(ctx, client.ObjectKey{Name: p.Name}, &dp); err == nil {
				previous[p.Name] = dp.Spec.Infrastructure.Version
			}
		}
	}

	for _, w := range waves {
		label := fmt.Sprintf("wave %d/%d", w.Index, len(waves))
		if w.Index == 1 {
			label += " (canary)"
		}
		fmt.Printf("\n  ▸ %s — %d plane(s)\n", label, len(w.Planes))

		before, err := snapshot(ctx, c)
		if err != nil {
			return err
		}
		for _, p := range w.Planes {
			if err := setVersion(ctx, c, p.Name, targetVersion); err != nil {
				return fmt.Errorf("wave %d: %s: %w", w.Index, p.Name, err)
			}
			fmt.Printf("      %s %s → %s\n", helpers.StatePending("upgrading"), p.Name, targetVersion)
		}

		gate, err := awaitWave(ctx, c, before, w)
		if err != nil {
			return err
		}
		if gate.Healthy {
			fmt.Printf("      %s\n", helpers.StateReady(fmt.Sprintf("wave %d healthy", w.Index)))
			continue
		}

		// Stop. Report precisely which planes regressed, because that is what
		// someone has to look at, and it is not the same set as "not Ready".
		fmt.Printf("      %s planes regressed: %v\n", helpers.StateDegraded("STOP"), gate.Regressed)
		if len(gate.StillConverging) > 0 {
			fmt.Printf("      %s (already unhealthy before this wave, not counted): %v\n",
				helpers.SubtitleStyle.Render("note"), gate.StillConverging)
		}
		if rollbackOnFail {
			rollback(ctx, c, gate.Regressed, previous)
		} else {
			fmt.Printf("      %s\n", helpers.SubtitleStyle.Render(
				"pass --rollback-on-failure to restore the previous version automatically"))
		}
		return fmt.Errorf("fleet upgrade stopped at wave %d: %d plane(s) regressed", w.Index, len(gate.Regressed))
	}

	fmt.Printf("\n  %s\n\n", helpers.StateReady(fmt.Sprintf("all %d plane(s) upgraded to %s", countPlanes(waves), targetVersion)))
	return nil
}

func countPlanes(waves []Wave) int {
	n := 0
	for _, w := range waves {
		n += len(w.Planes)
	}
	return n
}

// setVersion patches one DataPlane's requested Kubernetes version.
//
// A patch, not an update: a fleet upgrade races the controller, which is writing
// status on the same objects, and a read-modify-write of the whole object loses
// whatever it wrote in between.
func setVersion(ctx context.Context, c client.Client, name, version string) error {
	var dp v1alpha1.DataPlane
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &dp); err != nil {
		return err
	}
	if dp.Spec.Infrastructure.Version == version {
		return nil // already there; re-patching would bump generation for nothing
	}
	patch := client.MergeFrom(dp.DeepCopy())
	dp.Spec.Infrastructure.Version = version
	return c.Patch(ctx, &dp, patch)
}

// awaitWave waits for the planes in a wave to settle, then judges them.
//
// The settle delay exists because of a race that makes a gate useless: right
// after the spec changes, the controller has not yet observed it, so every plane
// still reports the Ready it had a second ago. Polling immediately therefore
// passes the gate before the upgrade has begun. Waiting first — then requiring
// Ready — is what makes the check mean something.
func awaitWave(ctx context.Context, c client.Client, before []Plane, w Wave) (Gate, error) {
	select {
	case <-ctx.Done():
		return Gate{}, ctx.Err()
	case <-time.After(settleDelay):
	}

	deadline := time.Now().Add(waveTimeout)
	var last Gate
	for {
		after, err := snapshot(ctx, c)
		if err != nil {
			return Gate{}, err
		}
		last = Evaluate(before, after, w)
		// Healthy means every plane in the wave is Ready — done.
		if last.Healthy && len(last.StillConverging) == 0 {
			return last, nil
		}
		// A regression is decided immediately; waiting cannot improve it and
		// every extra minute is more of the fleet sitting on a bad version.
		if len(last.Regressed) > 0 {
			return last, nil
		}
		if time.Now().After(deadline) {
			// Timed out with planes still converging and none regressed. Report
			// it as a failure — an upgrade that never finished is not a success —
			// but say which planes, so it is distinguishable from a crash.
			fmt.Printf("      %s wave did not become Ready within %s: %v\n",
				helpers.StateDegraded("timeout"), waveTimeout, last.StillConverging)
			last.Healthy = false
			last.Regressed = append(last.Regressed, last.StillConverging...)
			return last, nil
		}
		select {
		case <-ctx.Done():
			return Gate{}, ctx.Err()
		case <-time.After(15 * time.Second):
		}
	}
}

// rollback restores the recorded previous version on the planes that regressed.
//
// Only those planes. Rolling the whole fleet back would undo waves that are
// healthy, which is a larger change than the one that failed and turns a
// contained problem into a fleet-wide event.
func rollback(ctx context.Context, c client.Client, names []string, previous map[string]string) {
	fmt.Printf("      %s\n", helpers.SubtitleStyle.Render("rolling back the planes that regressed"))
	for _, n := range names {
		prev, ok := previous[n]
		if !ok || prev == "" {
			fmt.Printf("      %s %s had no previous version recorded — left as is\n",
				helpers.StateDegraded("skip"), n)
			continue
		}
		if err := setVersion(ctx, c, n, prev); err != nil {
			fmt.Printf("      %s %s → %s failed: %v\n", helpers.StateDegraded("rollback"), n, prev, err)
			continue
		}
		fmt.Printf("      %s %s → %s\n", helpers.StateReady("rolled back"), n, prev)
	}
}
