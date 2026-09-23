/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package fleet

// `adhar fleet` — operate every workload cluster registered with this control
// plane as one thing.
//
// The platform could already CREATE data planes (the DataPlane controller,
// ADR-0023) but there was no way to act across them: no answer to "what is out
// there", "which ones drifted", or "upgrade them all without taking the estate
// down". Those are the operations that decide whether a second cluster is an
// asset or a liability, so they belong in the CLI rather than in a runbook.
//
// Every subcommand is read-only except `upgrade`, and `upgrade` confirms with a
// full printed plan before it touches anything.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/k8s"

	"github.com/spf13/cobra"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	selectorFlag  string
	namespaceFlag string
)

// FleetCmd is the command group.
var FleetCmd = &cobra.Command{
	Use:     "fleet",
	Aliases: []string{"planes"},
	Short:   "Operate every registered workload cluster as one fleet",
	Long: `🌍 **Adhar Fleet**

Acts across every workload cluster registered with this control plane
(DataPlane resources — see ADR-0023), rather than one cluster at a time.

  adhar fleet list                     what is registered, and is it healthy
  adhar fleet drift                    which planes differ from the hub
  adhar fleet upgrade --dry-run        the wave plan, without touching anything
  adhar fleet upgrade                  roll it out in canary waves, gated on health

An upgrade goes out in waves: one canary first, then progressively larger
batches, with a health check between each. If a plane that was healthy stops
being healthy, the rollout stops — and with --rollback-on-failure it puts the
affected planes back.`,
	RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
}

func init() {
	FleetCmd.PersistentFlags().StringVar(&selectorFlag, "selector", "",
		"Restrict to planes whose placement labels match (k=v,k=v)")
	FleetCmd.PersistentFlags().StringVarP(&namespaceFlag, "namespace", "n", globals.AdharSystemNamespace,
		"Namespace holding the AdharPlatform resource")

	FleetCmd.AddCommand(listCmd, driftCmd, upgradeCmd)
}

/* ─────────────── reading the fleet ─────────────── */

// snapshot reads every DataPlane and converts it to the planner's view.
//
// DataPlane is the source of truth rather than AdharPlatform.status.fleet: the
// roll-up is a summary maintained for display and can lag, while the DataPlane
// objects are what the controller reconciles. Reading the summary would mean
// planning an upgrade from a cache.
func snapshot(ctx context.Context, c client.Client) ([]Plane, error) {
	var list v1alpha1.DataPlaneList
	if err := c.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("listing data planes: %w", err)
	}
	out := make([]Plane, 0, len(list.Items))
	for i := range list.Items {
		dp := &list.Items[i]
		out = append(out, Plane{
			Name:    dp.Name,
			Mode:    string(dp.Spec.Infrastructure.Mode),
			Ready:   readyCondition(dp),
			Version: dp.Status.KubernetesVersion,
			Apps:    dp.Status.AppCount,
			Labels:  dp.Spec.Placement.Labels,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func readyCondition(dp *v1alpha1.DataPlane) bool {
	for _, c := range dp.Status.Conditions {
		if c.Type == "Ready" {
			return c.Status == "True"
		}
	}
	return false
}

// hubVersion is the control plane's own Kubernetes version — what a plane's
// version is compared against.
//
// Read from the API server's /version rather than from AdharPlatform: the spec
// records what the platform was asked to build, and after a control-plane
// upgrade those two disagree. Drift has to be measured against what is actually
// running, or the report is about intentions.
func hubVersion(ctx context.Context) string {
	cfg, err := helpers.GetKubeConfig()
	if err != nil {
		return ""
	}
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return ""
	}
	v, err := dc.ServerVersion()
	if err != nil || v == nil {
		return ""
	}
	return v.GitVersion
}

func parseSelector(s string) (map[string]string, error) {
	out := map[string]string{}
	if strings.TrimSpace(s) == "" {
		return out, nil
	}
	for _, pair := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("--selector %q is not a comma-separated list of k=v", s)
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out, nil
}

func newClient() (client.Client, error) {
	cfg, err := helpers.GetKubeConfig()
	if err != nil {
		return nil, fmt.Errorf("no reachable cluster: %w", err)
	}
	return client.New(cfg, client.Options{Scheme: k8s.GetScheme()})
}

/* ─────────────── list ─────────────── */

var listCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls", "status"},
	Short:   "List every registered workload cluster",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newClient()
		if err != nil {
			return err
		}
		sel, err := parseSelector(selectorFlag)
		if err != nil {
			return err
		}
		planes, err := snapshot(cmd.Context(), c)
		if err != nil {
			return err
		}
		if len(planes) == 0 {
			fmt.Println(helpers.SectionHeading("🌍", "Fleet"))
			fmt.Println("  No workload clusters are registered.")
			fmt.Printf("  %s\n", helpers.SubtitleStyle.Render("register one with a DataPlane resource (mode: vcluster | adopt | composite)"))
			return nil
		}

		hub := hubVersion(cmd.Context())
		fmt.Println(helpers.SectionHeading("🌍", fmt.Sprintf("Fleet · %d plane(s)", len(planes))))
		t := helpers.NewTable("PLANE", "MODE", "STATE", "KUBERNETES", "APPS", "LABELS")
		shown, ready := 0, 0
		for _, p := range planes {
			if !matches(p.Labels, sel) {
				continue
			}
			shown++
			if p.Ready {
				ready++
			}
			t.Row(p.Name, p.Mode, planeState(p), versionCell(p.Version, hub), fmt.Sprint(p.Apps), labelCell(p.Labels))
		}
		fmt.Println(t.Render())
		fmt.Printf("  %s\n\n", helpers.SubtitleStyle.Render(fmt.Sprintf("%d/%d Ready · hub runs %s", ready, shown, orDash(hub))))
		return nil
	},
}

func planeState(p Plane) string {
	if p.Ready {
		return helpers.StateReady("Ready")
	}
	return helpers.StateDegraded("not Ready")
}

// versionCell marks a plane whose MINOR differs from the hub's. Patch-level
// differences are supported and flagging them trains people to ignore the column.
func versionCell(v, hub string) string {
	if v == "" {
		return helpers.StatePending("unknown")
	}
	if hub != "" && minorOf(v) != "" && minorOf(hub) != "" && minorOf(v) != minorOf(hub) {
		return helpers.StateDegraded(v + " (skew)")
	}
	return v
}

func labelCell(l map[string]string) string {
	if len(l) == 0 {
		return "—"
	}
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+l[k])
	}
	return strings.Join(parts, " ")
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

/* ─────────────── drift ─────────────── */

var driftCmd = &cobra.Command{
	Use:   "drift",
	Short: "Show planes whose Kubernetes version differs from the hub",
	Long: `Reports planes running a different Kubernetes MINOR version than the control
plane. Patch differences are not drift — Kubernetes's own skew policy is written
in minors, and flagging patches makes the report noise.

Drift is not necessarily a fault: a fleet part-way through an upgrade is skewed
by definition. It matters because a plane left behind by a FAILED wave looks
exactly like one nobody has upgraded yet.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newClient()
		if err != nil {
			return err
		}
		planes, err := snapshot(cmd.Context(), c)
		if err != nil {
			return err
		}
		hub := hubVersion(cmd.Context())
		if hub == "" {
			return fmt.Errorf("cannot determine the control plane's Kubernetes version; is there an AdharPlatform in %s?", namespaceFlag)
		}
		skewed := SkewedPlanes(planes, hub)
		fmt.Println(helpers.SectionHeading("🧭", fmt.Sprintf("Fleet drift · hub %s", hub)))
		if len(skewed) == 0 {
			fmt.Printf("  %s\n\n", helpers.StateReady(fmt.Sprintf("all %d plane(s) match the hub's minor version", len(planes))))
			return nil
		}
		t := helpers.NewTable("PLANE", "KUBERNETES", "HUB", "STATE")
		for _, p := range skewed {
			t.Row(p.Name, p.Version, hub, planeState(p))
		}
		fmt.Println(t.Render())
		fmt.Printf("  %s\n\n", helpers.SubtitleStyle.Render("bring them in line with: adhar fleet upgrade"))
		return nil
	},
}
