/*
Copyright 2026 The Adhar Authors.

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

package knowledge

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// CLIDir is where the command reference lives inside the bundle.
const CLIDir = "cli"

// CommandReference renders the CLI as Markdown: `cli/<root>.md` for the root
// command (its global flags and the list of commands) and one page per
// top-level command, `cli/<root>-<name>.md`, holding that command and every
// subcommand under it. One `##` heading per command, because the indexer
// chunks on headings and a subcommand with its help should be one chunk.
//
// Hidden commands and Cobra's own `help`/`completion` are left out: they are
// either not meant to be used or say nothing a person would ask about.
func CommandReference(root *cobra.Command) []File {
	if root == nil {
		return nil
	}
	name := root.Name()
	var out []File

	var top strings.Builder
	fmt.Fprintf(&top, "# `%s` command reference\n\n", name)
	if s := strings.TrimSpace(root.Long); s != "" {
		top.WriteString(s + "\n\n")
	} else if root.Short != "" {
		top.WriteString(root.Short + "\n\n")
	}
	top.WriteString("## Commands\n\n")
	for _, c := range documented(root) {
		fmt.Fprintf(&top, "- `%s` — %s\n", c.CommandPath(), c.Short)
	}
	if flags := strings.TrimRight(root.PersistentFlags().FlagUsages(), "\n "); flags != "" {
		fmt.Fprintf(&top, "\n## Global flags\n\nAccepted by every `%s` command.\n\n```\n%s\n```\n", name, flags)
	}
	out = append(out, File{Path: CLIDir + "/" + name + ".md", Data: []byte(top.String())})

	for _, group := range documented(root) {
		var b strings.Builder
		// The group's own help is its first `##` section; repeating its Short
		// under the `#` title would only index the same sentence twice.
		fmt.Fprintf(&b, "# `%s`\n\n", group.CommandPath())
		walk(group, func(c *cobra.Command) { section(&b, c) })
		out = append(out, File{
			Path: fmt.Sprintf("%s/%s-%s.md", CLIDir, name, group.Name()),
			Data: []byte(b.String()),
		})
	}
	return out
}

func documented(c *cobra.Command) []*cobra.Command {
	var out []*cobra.Command
	for _, sub := range c.Commands() {
		if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
			continue
		}
		out = append(out, sub)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

func walk(c *cobra.Command, fn func(*cobra.Command)) {
	fn(c)
	for _, sub := range documented(c) {
		walk(sub, fn)
	}
}

func section(b *strings.Builder, c *cobra.Command) {
	fmt.Fprintf(b, "## `%s`\n\n", c.CommandPath())
	if c.Short != "" {
		b.WriteString(c.Short + "\n\n")
	}
	if c.Deprecated != "" {
		fmt.Fprintf(b, "Deprecated: %s\n\n", c.Deprecated)
	}
	if len(c.Aliases) > 0 {
		quoted := make([]string, len(c.Aliases))
		for i, a := range c.Aliases {
			quoted[i] = "`" + a + "`"
		}
		fmt.Fprintf(b, "Aliases: %s.\n\n", strings.Join(quoted, ", "))
	}
	if s := strings.TrimSpace(c.Long); s != "" {
		b.WriteString(s + "\n\n")
	}
	fmt.Fprintf(b, "Usage: `%s`\n\n", c.UseLine())
	if subs := documented(c); len(subs) > 0 {
		b.WriteString("Subcommands:\n\n")
		for _, s := range subs {
			fmt.Fprintf(b, "- `%s` — %s\n", s.CommandPath(), s.Short)
		}
		b.WriteString("\n")
	}
	if flags := strings.TrimRight(c.NonInheritedFlags().FlagUsages(), "\n "); flags != "" {
		fmt.Fprintf(b, "Flags:\n\n```\n%s\n```\n\n", flags)
	}
	if s := strings.TrimSpace(c.Example); s != "" {
		fmt.Fprintf(b, "Examples:\n\n```\n%s\n```\n\n", s)
	}
}
