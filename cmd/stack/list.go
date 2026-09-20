/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package stack

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"
)

var (
	filterCategory string
	onlyEnabled    bool
	onlyProblems   bool
)

var listCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List the platform's packages with their enablement and live state",
	Long: `List the platform catalogue.

Each row pairs what the profile DECLARES (enabled or not) with what the cluster is
actually doing (Argo CD sync and health), because the interesting cases are where
those two disagree: a package enabled but Missing has not synced yet; one disabled
but still present has not been pruned.`,
	RunE:         runList,
	SilenceUsage: true,
}

var statusCmd = &cobra.Command{
	Use:   "status [package]",
	Short: "Summarise the platform's convergence, or one package's state",
	Long: `Summarise how converged the platform is.

With no argument it counts the catalogue by state and lists what is not yet Synced
and Healthy — the same question ` + "`adhar up`" + ` answers while it waits. With a package
name it shows that one package's state.`,
	RunE:         runStatus,
	SilenceUsage: true,
}

func init() {
	for _, c := range []*cobra.Command{listCmd, statusCmd} {
		c.Flags().StringVar(&filterCategory, "category", "", "Only packages in one category (ai, application, core, data, infrastructure, observability, security)")
		c.Flags().BoolVar(&onlyEnabled, "enabled", false, "Only packages this profile enables")
		c.Flags().BoolVar(&onlyProblems, "problems", false, "Only packages that are not Synced and Healthy")
	}
}

// row is one package's declared and live state.
type row struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	Enabled  bool   `json:"enabled"`
	Sync     string `json:"sync,omitempty"`
	Health   string `json:"health,omitempty"`
	Message  string `json:"message,omitempty"`
	Present  bool   `json:"present"`
}

func gather(ctx context.Context) ([]row, profile, string, error) {
	p, source, err := detectProfile(ctx, flagStackDir)
	if err != nil {
		return nil, profile{}, "", err
	}
	elements, err := readElements(p.appsetFile)
	if err != nil {
		return nil, p, source, err
	}
	live := liveApplications(ctx)

	rows := make([]row, 0, len(elements))
	for _, e := range elements {
		r := row{Name: e.Name, Category: e.Category, Enabled: e.on()}
		if app, ok := live[e.Name]; ok {
			r.Present, r.Sync, r.Health, r.Message = true, app.sync, app.health, app.message
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Category != rows[j].Category {
			return rows[i].Category < rows[j].Category
		}
		return rows[i].Name < rows[j].Name
	})
	return rows, p, source, nil
}

type appState struct {
	sync, health, message string
}

// liveApplications reads Argo CD's view. A missing or unreachable cluster is not an
// error: the declared half of this command is useful on its own, and `adhar stack
// list` in a checkout with no cluster should still print the catalogue.
func liveApplications(ctx context.Context) map[string]appState {
	out := map[string]appState{}
	cfg, err := helpers.GetKubeConfig()
	if err != nil {
		return out
	}
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return out
	}
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	list, err := dc.Resource(gvr).Namespace(flagNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return out
	}
	for _, item := range list.Items {
		obj := item.Object
		name := item.GetName()
		if pkg, ok := item.GetLabels()["adhar.io/package-name"]; ok && pkg != "" {
			name = pkg
		}
		sync, _ := nested(obj, "status", "sync", "status")
		health, _ := nested(obj, "status", "health", "status")
		msg, _ := nested(obj, "status", "health", "message")
		out[name] = appState{sync: sync, health: health, message: msg}
	}
	return out
}

func nested(o map[string]interface{}, path ...string) (string, bool) {
	cur := interface{}(o)
	for _, p := range path {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return "", false
		}
		cur, ok = m[p]
		if !ok {
			return "", false
		}
	}
	s, ok := cur.(string)
	return s, ok
}

func keep(r row) bool {
	if filterCategory != "" && !strings.EqualFold(r.Category, filterCategory) {
		return false
	}
	if onlyEnabled && !r.Enabled {
		return false
	}
	if onlyProblems && (r.Sync == "Synced" && r.Health == "Healthy" || !r.Enabled) {
		return false
	}
	return true
}

func runList(cmd *cobra.Command, _ []string) error {
	rows, p, source, err := gather(cmd.Context())
	if err != nil {
		return err
	}
	var shown []row
	for _, r := range rows {
		if keep(r) {
			shown = append(shown, r)
		}
	}
	if flagJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(shown)
	}

	fmt.Println()
	fmt.Println(helpers.SectionHeading("📦", fmt.Sprintf("Platform stack · %s profile", p.name)))
	fmt.Println()
	t := helpers.NewTable("PACKAGE", "CATEGORY", "DECLARED", "LIVE STATE")
	for _, r := range shown {
		t.Row(r.Name, r.Category, declared(r), liveCell(r))
	}
	fmt.Println(t.Render())
	fmt.Printf("\n  %d of %d packages shown · profile from %s\n", len(shown), len(rows), source)
	fmt.Printf("  %s\n\n", helpers.SubtitleStyle.Render("enable/disable edits "+filepath.Base(p.appsetFile)+" and the environment config, then run `adhar upgrade`"))
	return nil
}

func declared(r row) string {
	if r.Enabled {
		return helpers.StateReady("enabled")
	}
	return helpers.StateDisabled("disabled")
}

// liveCell renders the disagreements as well as the agreements: "enabled but
// Missing" and "disabled but still present" are the two states worth noticing, and
// a plain sync/health pair hides both.
func liveCell(r row) string {
	switch {
	case !r.Present && r.Enabled:
		return helpers.StatePending("not created yet")
	case !r.Present:
		return "—"
	case !r.Enabled:
		return helpers.StateDegraded("still present (not pruned)")
	case r.Sync == "Synced" && r.Health == "Healthy":
		return helpers.StateReady("Synced + Healthy")
	case r.Health == "Progressing":
		return helpers.StatePending(r.Sync + " · Progressing")
	case r.Health == "Missing":
		return helpers.StatePending(r.Sync + " · Missing")
	default:
		return helpers.StateDegraded(r.Sync + " · " + orDash(r.Health))
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func runStatus(cmd *cobra.Command, args []string) error {
	rows, p, _, err := gather(cmd.Context())
	if err != nil {
		return err
	}
	if len(args) > 0 {
		for _, r := range rows {
			if r.Name == args[0] {
				if flagJSON {
					enc := json.NewEncoder(os.Stdout)
					enc.SetIndent("", "  ")
					return enc.Encode(r)
				}
				fmt.Printf("\n  %s (%s) · %s · %s\n", r.Name, r.Category, strings.TrimSpace(declared(r)), strings.TrimSpace(liveCell(r)))
				if r.Message != "" {
					fmt.Printf("  %s\n", r.Message)
				}
				fmt.Println()
				return nil
			}
		}
		return fmt.Errorf("no package named %q in the %s profile", args[0], p.name)
	}

	var enabled, healthy, degraded, pending, orphaned int
	var notReady []row
	for _, r := range rows {
		if !r.Enabled {
			if r.Present {
				orphaned++
			}
			continue
		}
		enabled++
		switch {
		case r.Sync == "Synced" && r.Health == "Healthy":
			healthy++
		case !r.Present || r.Health == "Progressing" || r.Health == "Missing":
			pending++
			notReady = append(notReady, r)
		default:
			degraded++
			notReady = append(notReady, r)
		}
	}

	if flagJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]interface{}{
			"profile": p.name, "wired": len(rows), "enabled": enabled,
			"healthy": healthy, "degraded": degraded, "pending": pending, "notPruned": orphaned,
			"notReady": notReady,
		})
	}

	fmt.Println()
	fmt.Println(helpers.SectionHeading("📦", fmt.Sprintf("Platform stack · %s profile", p.name)))
	fmt.Println()
	t := helpers.NewTable("STATE", "COUNT")
	t.Row("Wired in this profile", fmt.Sprintf("%d", len(rows)))
	t.Row("Enabled", fmt.Sprintf("%d", enabled))
	t.Row(helpers.StateReady("Synced + Healthy"), fmt.Sprintf("%d", healthy))
	t.Row(helpers.StatePending("Still converging"), fmt.Sprintf("%d", pending))
	t.Row(helpers.StateDegraded("Degraded"), fmt.Sprintf("%d", degraded))
	if orphaned > 0 {
		t.Row(helpers.StateDegraded("Disabled but still present"), fmt.Sprintf("%d", orphaned))
	}
	fmt.Println(t.Render())

	if len(notReady) > 0 {
		fmt.Println()
		fmt.Println("  Not ready:")
		for _, r := range notReady {
			line := fmt.Sprintf("    %-28s %s", r.Name, strings.TrimSpace(liveCell(r)))
			if r.Message != "" {
				line += " · " + helpers.TruncateDisplay(r.Message, 60)
			}
			fmt.Println(line)
		}
		fmt.Println()
		fmt.Println("  → Investigate one:  adhar ai diagnose <package>   (or: adhar stack describe <package>)")
	}
	fmt.Println()
	return nil
}

// ---------------------------------------------------------------------------
// describe
// ---------------------------------------------------------------------------

var describeCmd = &cobra.Command{
	Use:     "describe <package>",
	Aliases: []string{"info", "show"},
	Short:   "Show a package's marketplace contract and current state",
	Long: `Show everything the platform knows about one package: the marketplace contract
it ships (category, version, maintainer, licence, stability, provenance,
dependencies), whether this profile enables it, what Argo CD makes of it, and the
packages it must not be enabled alongside.`,
	Args:         cobra.ExactArgs(1),
	RunE:         runDescribe,
	SilenceUsage: true,
}

func runDescribe(cmd *cobra.Command, args []string) error {
	name := args[0]
	rows, p, _, err := gather(cmd.Context())
	if err != nil {
		return err
	}
	var r *row
	for i := range rows {
		if rows[i].Name == name {
			r = &rows[i]
			break
		}
	}
	if r == nil {
		return fmt.Errorf("no package named %q in the %s profile", name, p.name)
	}
	elements, err := readElements(p.appsetFile)
	if err != nil {
		return err
	}

	contract := readContract(flagStackDir, elements, name)

	if flagJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]interface{}{"package": r, "contract": contract})
	}

	fmt.Println()
	fmt.Println(helpers.SectionHeading("📦", name))
	fmt.Println()
	t := helpers.NewTable("FIELD", "VALUE")
	t.Row("Category", r.Category)
	t.Row("Declared", strings.TrimSpace(declared(*r)))
	t.Row("Live state", strings.TrimSpace(liveCell(*r)))
	if contract != nil {
		t.Row("Version", orDash(contract.Version))
		t.Row("Stability", orDash(contract.Stability))
		t.Row("Licence", orDash(contract.License))
		t.Row("Homepage", orDash(contract.Homepage))
		if contract.Maintainer.Name != "" {
			t.Row("Maintainer", contract.Maintainer.Name)
		}
		if len(contract.Dependencies) > 0 {
			t.Row("Dependencies", strings.Join(contract.Dependencies, ", "))
		}
		if contract.Provenance.UpstreamChart != "" {
			t.Row("Upstream", contract.Provenance.UpstreamChart)
		}
	}
	fmt.Println(t.Render())

	if contract != nil && contract.Description != "" {
		fmt.Printf("\n  %s\n", contract.Description)
	}
	if r.Message != "" {
		fmt.Printf("\n  Argo CD says: %s\n", r.Message)
	}
	if c := conflictsWith(name, elements); len(c) > 0 {
		fmt.Printf("\n  %s must not be enabled alongside: %s\n", name, strings.Join(c, ", "))
	}
	if d := dependencies[name]; len(d) > 0 {
		fmt.Printf("  needs: %s\n", strings.Join(d, ", "))
	}
	fmt.Println()
	return nil
}

// contract is the subset of adhar-package.yaml worth showing in a terminal.
type contract struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	License     string `json:"license"`
	Homepage    string `json:"homepage"`
	Stability   string `json:"stability"`
	Maintainer  struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"maintainer"`
	Dependencies []string `json:"dependencies"`
	Provenance   struct {
		UpstreamChart string `json:"upstreamChart"`
		SourceRepo    string `json:"sourceRepo"`
	} `json:"provenance"`
}

// readContract loads a package's marketplace contract from the stack on disk. The
// manifest path is the only reliable route to it: a package's directory name is
// its identity, but the ApplicationSet entry name is not always the directory
// (supply-chain-policies-enforce and vllm-cpu both live elsewhere).
func readContract(stackDir string, elements []element, name string) *contract {
	dir := ""
	for _, e := range elements {
		if e.Name == name {
			dir = filepath.Dir(filepath.Join(stackDir, "packages", e.ManifestPath))
			break
		}
	}
	if dir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(dir, "adhar-package.yaml")) // #nosec G304 -- resolved from the stack's own ApplicationSet
	if err != nil {
		return nil
	}
	var c contract
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil
	}
	return &c
}
