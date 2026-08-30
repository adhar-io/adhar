/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

// This file gives `adhar --help`, `adhar <cmd> --help`, and the bare `adhar`
// invocation one polished, consistent layout. It renders straight from the
// registered command tree and its persona groups, so the help never drifts from
// what is actually wired up.

// Help palette (kept local so help styling is self-contained and easy to tune).
var (
	hSection = lipgloss.NewStyle().Foreground(helpers.AccentColor).Bold(true)
	hCommand = lipgloss.NewStyle().Foreground(helpers.PrimaryColor).Bold(true)
	hDesc    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#57606a", Dark: "#8b949e"})
	hLabel   = lipgloss.NewStyle().Foreground(helpers.HighlightColor).Bold(true)
	hAccent  = lipgloss.NewStyle().Foreground(helpers.SecondaryColor)
	hFaint   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#8c959f", Dark: "#484f58"})
	hTagline = lipgloss.NewStyle().Foreground(helpers.InfoColor).Italic(true)
	hTag     = lipgloss.NewStyle().Foreground(helpers.SecondaryColor).Italic(true)
)

// rule returns a faint horizontal divider of n cells.
func rule(n int) string {
	return strings.Repeat("─", n)
}

// personaGroup describes one top-level help section on the root command.
type personaGroup struct {
	id       string
	icon     string
	name     string
	subtitle string
}

// rootPersonaGroups is the display order + presentation of the root help groups.
// The ids match the cobra group ids assigned in main.go's init().
var rootPersonaGroups = []personaGroup{
	{GroupDevelop, "🧑‍💻", "Develop", "build, ship & self-serve resources"},
	{GroupObserve, "🔭", "Observe", "health, logs, metrics & traces"},
	{GroupOperate, "⚙️", "Operate", "day-2 operations"},
	{GroupAdminister, "🛡️", "Administer", "platform lifecycle & governance"},
	{GroupUtilities, "🧰", "Utilities", "tooling"},
}

// colorDisabled reports whether help should render without ANSI styling.
func colorDisabled(cmd *cobra.Command) bool {
	if v, err := cmd.Flags().GetBool("no-color"); err == nil && v {
		return true
	}
	if os.Getenv("NO_COLOR") != "" {
		return true
	}
	return false
}

// paint applies a style unless color is disabled.
func paint(off bool, style lipgloss.Style, s string) string {
	if off {
		return s
	}
	return style.Render(s)
}

// visibleCommands returns the runnable/visible subcommands of cmd.
func visibleCommands(cmd *cobra.Command) []*cobra.Command {
	var out []*cobra.Command
	for _, c := range cmd.Commands() {
		if c.IsAvailableCommand() && !c.IsAdditionalHelpTopicCommand() {
			out = append(out, c)
		}
	}
	return out
}

// nameWidth is the padded width of the longest command name in the set.
func nameWidth(cmds []*cobra.Command) int {
	w := 0
	for _, c := range cmds {
		if n := utf8.RuneCountInString(c.Name()); n > w {
			w = n
		}
	}
	return w
}

// renderCommandRow renders one aligned "name    description" line.
func renderCommandRow(b *strings.Builder, off bool, c *cobra.Command, width int) {
	name := c.Name()
	pad := strings.Repeat(" ", width-utf8.RuneCountInString(name))
	short := firstLine(c.Short)
	fmt.Fprintf(b, "      %s%s   %s\n", paint(off, hCommand, name), pad, paint(off, hDesc, short))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// bannerBlock returns the brand banner: full art on an interactive terminal,
// a compact one-liner otherwise (piped/redirected/CI), so help stays tidy.
func bannerBlock(full bool) string {
	fi, err := os.Stdout.Stat()
	interactive := err == nil && (fi.Mode()&os.ModeCharDevice) != 0
	if full && interactive {
		return "\n" + helpers.RenderBanner() + "\n"
	}
	return helpers.RenderBannerLine(globals.Version) + "\n"
}

// styledHelp is the SetHelpFunc handler for the whole command tree; it always
// prints the brand banner (used for `--help`, where PersistentPreRun is skipped).
func styledHelp(cmd *cobra.Command, args []string) {
	renderHelp(cmd, args, true)
}

// renderHelp draws the help layout. withBanner is false on the bare `adhar`
// invocation, where PersistentPreRun has already printed the header.
func renderHelp(cmd *cobra.Command, _ []string, withBanner bool) {
	off := colorDisabled(cmd)
	var b strings.Builder

	isRoot := !cmd.HasParent()
	if withBanner {
		b.WriteString(bannerBlock(isRoot))
		b.WriteString("\n") // one blank line between the banner and the content
	}

	// Tagline / command summary.
	if isRoot {
		fmt.Fprintf(&b, "  %s\n", paint(off, hTagline, cmd.Short))
	} else {
		fmt.Fprintf(&b, "  %s %s\n", paint(off, hCommand, cmd.CommandPath()), paint(off, hDesc, "· "+firstLine(cmd.Short)))
	}
	fmt.Fprintf(&b, "  %s\n\n", paint(off, hFaint, rule(44)))

	// USAGE
	fmt.Fprintf(&b, "  %s\n", paint(off, hLabel, "USAGE"))
	fmt.Fprintf(&b, "    %s\n", paint(off, hAccent, usageLine(cmd)))
	b.WriteString("\n")

	// COMMANDS — persona groups on the root, flat elsewhere.
	cmds := visibleCommands(cmd)
	if len(cmds) > 0 {
		width := nameWidth(cmds)
		if isRoot {
			renderPersonaGroups(&b, off, cmd, width)
		} else {
			fmt.Fprintf(&b, "  %s\n", paint(off, hLabel, "COMMANDS"))
			for _, c := range cmds {
				renderCommandRow(&b, off, c, width)
			}
			b.WriteString("\n")
		}
	}

	// FLAGS (local) and GLOBAL FLAGS (inherited).
	if s := strings.TrimRight(cmd.LocalFlags().FlagUsages(), "\n"); s != "" {
		label := "FLAGS"
		if isRoot {
			label = "GLOBAL FLAGS"
		}
		fmt.Fprintf(&b, "  %s\n%s\n\n", paint(off, hLabel, label), dimBlock(off, s))
	}
	if !isRoot {
		if s := strings.TrimRight(cmd.InheritedFlags().FlagUsages(), "\n"); s != "" {
			fmt.Fprintf(&b, "  %s\n%s\n\n", paint(off, hLabel, "GLOBAL FLAGS"), dimBlock(off, s))
		}
	}

	// EXAMPLES
	if cmd.Example != "" {
		fmt.Fprintf(&b, "  %s\n%s\n\n", paint(off, hLabel, "EXAMPLES"), exampleBlock(off, cmd.Example))
	}

	// Footer: hint lines, then (when this renderer owns the chrome, i.e. the
	// --help path where PersistentPostRun is skipped) the brand sign-off.
	fmt.Fprintf(&b, "  %s\n", paint(off, hFaint, rule(44)))
	b.WriteString(footerHint(off, cmd))
	if withBanner {
		fmt.Fprintf(&b, "\n  %s\n", paint(off, hTag, "Adhar • Built with ❤️  for developers!"))
	}

	fmt.Fprint(cmd.OutOrStdout(), b.String())
}

// usageLine builds a friendly usage string.
func usageLine(cmd *cobra.Command) string {
	if cmd.HasAvailableSubCommands() {
		return cmd.CommandPath() + " <command> [flags]"
	}
	return cmd.UseLine()
}

// renderPersonaGroups renders the root help's persona sections in order, then
// any commands that were not assigned to a persona group.
func renderPersonaGroups(b *strings.Builder, off bool, root *cobra.Command, width int) {
	seen := map[string]bool{}
	for _, g := range rootPersonaGroups {
		var in []*cobra.Command
		for _, c := range visibleCommands(root) {
			if c.GroupID == g.id {
				in = append(in, c)
				seen[c.Name()] = true
			}
		}
		if len(in) == 0 {
			continue
		}
		fmt.Fprintf(b, "  %s  %s   %s\n", g.icon, paint(off, hSection, strings.ToUpper(g.name)), paint(off, hDesc, g.subtitle))
		for _, c := range in {
			renderCommandRow(b, off, c, width)
		}
		b.WriteString("\n")
	}
	// Anything without a persona group (e.g. completion).
	var rest []*cobra.Command
	for _, c := range visibleCommands(root) {
		if !seen[c.Name()] {
			rest = append(rest, c)
		}
	}
	if len(rest) > 0 {
		fmt.Fprintf(b, "  %s  %s\n", "•", paint(off, hSection, "MORE"))
		for _, c := range rest {
			renderCommandRow(b, off, c, width)
		}
		b.WriteString("\n")
	}
}

// dimBlock re-indents and dims a pre-aligned block (e.g. cobra FlagUsages).
func dimBlock(off bool, s string) string {
	var out strings.Builder
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, " ")
		if line == "" {
			out.WriteString("\n")
			continue
		}
		// FlagUsages already indents by 2 spaces; add 2 more for the section body.
		fmt.Fprintf(&out, "  %s\n", paint(off, hDesc, line))
	}
	return strings.TrimRight(out.String(), "\n")
}

// exampleBlock renders the command's Example text, highlighting comment lines.
func exampleBlock(off bool, s string) string {
	var out strings.Builder
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			out.WriteString("\n")
		case strings.HasPrefix(trimmed, "#"):
			fmt.Fprintf(&out, "    %s\n", paint(off, hFaint, trimmed))
		default:
			fmt.Fprintf(&out, "    %s\n", paint(off, hAccent, trimmed))
		}
	}
	return strings.TrimRight(out.String(), "\n")
}

// footerHint renders a "Next Steps" block in the style of `adhar down` —
// arrowed, highlighted runnable commands.
func footerHint(off bool, cmd *cobra.Command) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  %s\n", paint(off, hLabel, "NEXT STEPS"))
	step := func(command, desc string) {
		fmt.Fprintf(&b, "    %s Run %s %s\n",
			paint(off, hAccent, "→"),
			paint(off, hCommand, command),
			paint(off, hDesc, desc))
	}
	switch {
	case !cmd.HasParent():
		step("adhar up", "to launch a local platform")
		step("adhar <command> --help", "for details on any command")
		step("adhar version", "for build information")
	case cmd.HasAvailableSubCommands():
		step(cmd.CommandPath()+" <command> --help", "for details on a subcommand")
		step("adhar --help", "to see all commands")
	default:
		step("adhar --help", "to see all commands")
	}
	return b.String()
}
