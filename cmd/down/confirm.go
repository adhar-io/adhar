package down

import (
	"fmt"
	"sort"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/config"
	"github.com/charmbracelet/lipgloss"
)

// confirm.go renders the prompt shown before a cloud teardown.
//
// It is a panel rather than a few printed lines because of what it guards: the
// command destroys machines, volumes and load balancers that cost money and
// cannot be recovered. "Are you sure?" is worth nothing unless it says sure about
// WHAT, WHERE, and under WHICH NAME — the same config file can point at different
// accounts, and since `adhar up --name` exists the cluster being hunted for is no
// longer implied by the environment.

// teardownPlan is everything the confirmation needs to describe. Built from the
// same inputs the teardown itself uses, so the prompt cannot promise something
// different from what runs.
type teardownPlan struct {
	ConfigFile string
	// Environments is what will actually be torn down, in the order the teardown
	// walks them. Empty means the file could not be read.
	Environments []string
	// AllEnvironments is true when no --env was given, so the run covers the whole
	// file rather than one named environment.
	AllEnvironments bool
	// Clusters are the names that will be searched, primary first.
	Clusters []string
	Target   string
	Deletes  string
	// CloudResources is false for a purely local (Kind) teardown, which destroys
	// containers rather than volumes anybody paid for. The data-loss warning is
	// suppressed there: shouting about unrecoverable volumes over a Kind cluster
	// teaches the reader to ignore the warning when it is real.
	CloudResources bool
}

// buildTeardownPlan resolves the plan. Best effort: an unreadable config still
// yields a prompt (with less in it) rather than blocking the teardown, which is
// the same rule the rest of this command follows.
func buildTeardownPlan(configFile, envName, clusterOverride string) teardownPlan {
	plan := teardownPlan{
		ConfigFile:      configFile,
		AllEnvironments: envName == "",
		Target:          teardownTargetDescription(configFile, envName),
		Deletes:         teardownResourceSummary(configFile, envName),
	}

	cfg, err := config.LoadConfig(configFile)
	if err != nil || cfg == nil {
		// No environment list and no cluster names; the caller renders what it has.
		return plan
	}
	// LoadConfig PARSES the file; it does not expand templates into
	// ResolvedEnvironments. Without this the map is empty, so the panel fell back
	// to "every environment in the file" and showed no cluster names at all — the
	// two things it exists to tell you.
	if err := cfg.ResolveEnvironments(); err != nil {
		return plan
	}
	if envName != "" {
		plan.Environments = []string{envName}
	} else {
		for name := range cfg.ResolvedEnvironments {
			plan.Environments = append(plan.Environments, name)
		}
		sort.Strings(plan.Environments)
	}

	// Cloud unless every environment in scope is local.
	for _, name := range plan.Environments {
		if env, ok := cfg.ResolvedEnvironments[name]; ok && env.ResolvedProvider != globals.CloudProviderKind {
			plan.CloudResources = true
			break
		}
	}

	// Every cluster name the teardown will look for, across the environments in
	// scope, de-duplicated and stable. With one environment this is the usual
	// "adhar, and the legacy dev" pair.
	seen := map[string]bool{}
	for _, name := range plan.Environments {
		env := cfg.ResolvedEnvironments[name]
		for _, candidate := range helpers.EnvironmentClusterNames(clusterOverride, env) {
			if candidate != "" && !seen[candidate] {
				seen[candidate] = true
				plan.Clusters = append(plan.Clusters, candidate)
			}
		}
	}
	return plan
}

// scopeLine describes the breadth of the teardown, naming the environments rather
// than only counting them: "EVERY environment in <file>" told the operator the one
// thing they already knew and withheld the thing they needed.
func (p teardownPlan) scopeLine() string {
	switch {
	case len(p.Environments) == 0:
		if p.AllEnvironments {
			return "every environment in the file"
		}
		return "one environment"
	case !p.AllEnvironments:
		return p.Environments[0]
	case len(p.Environments) == 1:
		return fmt.Sprintf("every environment — %s", p.Environments[0])
	default:
		return fmt.Sprintf("every environment (%d) — %s", len(p.Environments), strings.Join(p.Environments, ", "))
	}
}

func (p teardownPlan) clusterLine() string {
	if len(p.Clusters) == 0 {
		return ""
	}
	if len(p.Clusters) == 1 {
		return p.Clusters[0]
	}
	// The extras are legacy names still searched so a cluster built before the
	// rename is not left running. Say so, or the list reads like a mistake.
	return fmt.Sprintf("%s  (also searching %s)", p.Clusters[0], strings.Join(p.Clusters[1:], ", "))
}

const (
	// labelWidth aligns the value column; valueWidth is what is left for wrapping.
	labelWidth = 10
	valueWidth = 58
	// panelWidth keeps the box a constant shape across providers. Wide enough for
	// the longest resource summary at valueWidth, narrow enough for an 80-column
	// terminal.
	panelWidth = 74
)

func row(label, value string) string {
	if value == "" {
		return ""
	}
	return fmt.Sprintf("  %-*s%s\n", labelWidth, label,
		helpers.WrapValue(value, valueWidth, labelWidth+2))
}

// RenderTeardownConfirmation is the panel shown before anything is deleted.
func RenderTeardownConfirmation(plan teardownPlan) string {
	var b strings.Builder
	// The headline has to match the Deletes line below it. A purely local teardown
	// was announced as deleting "cloud infrastructure" that "cannot be undone"
	// while its own summary said "no cloud resources" — and a Kind cluster is one
	// `adhar up` away from being back. Overstating the small case is how a reader
	// learns to skim the warning that matters.
	headline := "This deletes cloud infrastructure and cannot be undone."
	if !plan.CloudResources {
		headline = "This deletes your local cluster and everything running on it."
	}
	b.WriteString(helpers.WarningStyle.Render(helpers.IconDegraded) + "  " +
		lipgloss.NewStyle().Foreground(helpers.WarningColor).Bold(true).
			Render(headline) + "\n\n")

	b.WriteString(row("Config", plan.ConfigFile))
	b.WriteString(row("Scope", plan.scopeLine()))
	b.WriteString(row("Cluster", plan.clusterLine()))
	b.WriteString(row("Target", plan.Target))
	b.WriteString(row("Deletes", strings.TrimSuffix(plan.Deletes, ".")))

	if plan.CloudResources {
		b.WriteString("\n" + helpers.SubtitleStyle.UnsetMarginLeft().Render(
			"  Volume data is destroyed with the volumes. Anything not backed up\n"+
				"  elsewhere is gone.") + "\n")
	}

	// Fixed width so the panel is the same shape whichever provider it describes;
	// sized to content it shifted by a few columns per cloud.
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(helpers.WarningColor).
		Padding(1, 2).
		MarginTop(1).
		Width(panelWidth).
		Render(strings.TrimRight(b.String(), "\n"))
}
