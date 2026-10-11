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

package helpers

// gutter.go is the ONE left margin of the CLI.
//
// The banner prints behind a two-column gutter and so does the help body, but
// every command's own output started wherever its author's format string
// happened to begin: the section heading at column 1 (TitleStyle carried
// MarginLeft(1)), tables and bespoke `%-30s` header rows at column 0, panels at
// 0. On screen the logo and the help text share a left edge that the command's
// actual answer does not, so each command looks like it belongs to a different
// program.
//
// Everything printed under the banner is indented from here. Keep BannerGutter
// the single source of truth — it is the number the help body in
// cmd/helptext.go is written against, so a change there moves the whole CLI
// together rather than splitting it again.

import (
	"fmt"
	"strings"
)

// Gutter is the CLI's left margin as a string, for prefixing a line directly.
var Gutter = strings.Repeat(" ", BannerGutter)

// Indent puts every line of s behind the gutter.
//
// Blank lines are left EMPTY rather than padded: trailing whitespace on an
// otherwise empty line is invisible on screen but shows up in a diff, in a
// copied transcript and in a test fixture, and it is the first thing that makes
// a golden file disagree with what a person sees.
func Indent(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			lines[i] = ""
			continue
		}
		lines[i] = Gutter + ln
	}
	return strings.Join(lines, "\n")
}

// Printf and Println are the gutter-aware replacements for the fmt pair, for the
// hundreds of one-off lines commands print that are not a table, a panel or a
// Create* line. They indent every line of the formatted result.
//
// They write to STDOUT only. Diagnostics on stderr are left alone on purpose:
// the live checklist (StageTracker) and the spinner own that stream and
// position themselves by counting the lines they drew, so a gutter inserted
// underneath them would shift what they redraw. The structured logger is left
// alone for the same kind of reason — its lines carry a timestamp prefix and
// are grepped and parsed, and `grep '^\['` should keep working.
func Printf(format string, args ...interface{}) {
	fmt.Print(Indent(fmt.Sprintf(format, args...)))
}

// Println indents each line of the space-joined arguments.
func Println(args ...interface{}) {
	fmt.Println(Indent(strings.TrimSuffix(fmt.Sprintln(args...), "\n")))
}
