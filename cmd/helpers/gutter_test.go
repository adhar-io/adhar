package helpers

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// Everything the CLI prints under the banner shares ONE left margin. These pin
// the three ways it was lost: a style with its own margin, a renderer that
// indents nothing, and a command printing a line with bare fmt.
func TestEverySharedRendererPrintsBehindTheGutter(t *testing.T) {
	if BannerGutter < 1 {
		t.Fatalf("BannerGutter = %d; there is no gutter to share", BannerGutter)
	}
	// The section heading. It carried MarginLeft(1) while the banner used 2, so
	// every heading sat one column left of the logo above it.
	if got := TitleStyle.GetMarginLeft(); got != BannerGutter {
		t.Errorf("TitleStyle margin is %d, banner gutter is %d", got, BannerGutter)
	}
	// Panels. Thirty-odd commands render their output inside BorderStyle, so its
	// margin is what puts all of them behind the gutter at once.
	if got := BorderStyle.GetMarginLeft(); got != BannerGutter {
		t.Errorf("BorderStyle margin is %d, banner gutter is %d", got, BannerGutter)
	}
	// The shared table.
	tb := NewTable("NAME", "STATE").WithBudget(60)
	tb.Row("adhar-worker-1", "Ready")
	for i, line := range strings.Split(tb.Render(), "\n") {
		if !strings.HasPrefix(line, Gutter) {
			t.Errorf("table line %d does not start behind the gutter: %q", i, line)
		}
	}
	// A Create* line.
	if !strings.HasPrefix(CreateMuted("3 items"), Gutter) {
		t.Errorf("CreateMuted does not start behind the gutter: %q", CreateMuted("3 items"))
	}
}

// Indent must not pad a blank line. Trailing whitespace is invisible on screen
// but shows up in every diff, copied transcript and golden file.
func TestIndentLeavesBlankLinesEmpty(t *testing.T) {
	got := Indent("first\n\nthird")
	want := Gutter + "first\n\n" + Gutter + "third"
	if got != want {
		t.Errorf("Indent(%q) = %q, want %q", "first\n\nthird", got, want)
	}
	if Indent("") != "" {
		t.Errorf("Indent(\"\") = %q, want empty", Indent(""))
	}
}

// A table must stay inside its budget AFTER being indented: sized to the full
// budget, a table that exactly fitted would wrap the moment it moved right.
//
// 60 is TerminalBudget's own floor. Below it the per-column minimum wins over
// the budget by design — three columns cannot go under 12 cells each and still
// be readable — so a narrower budget tests that floor, not this.
func TestTableBudgetPaysForTheGutter(t *testing.T) {
	const budget = 60
	tb := NewTable("A", "B", "C").WithBudget(budget)
	tb.Row(strings.Repeat("a", 30), strings.Repeat("b", 30), strings.Repeat("c", 30))
	for i, line := range strings.Split(tb.Render(), "\n") {
		if w := lipgloss.Width(line); w > budget {
			t.Errorf("rendered line %d is %d cells, over the %d budget: %q", i, w, budget, line)
		}
	}
}

// No command may print a complete line with bare `fmt`. There are ~290 such
// lines across the CLI and they ALL started at column 0 while the banner, the
// help body, the headings, the tables and the panels sat at two — so a command's
// own answer was the one thing not aligned with the rest of its output. Use
// helpers.Printf / helpers.Println, which indent.
//
// `fmt.Println("")` is allowed: a blank line needs no gutter, and padding one
// would only add trailing whitespace.
func TestCommandsDoNotPrintCompleteLinesWithBareFmt(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	// The live checklist, the spinner and the progress bars own their stream and
	// position themselves by counting the lines they drew; a gutter inserted
	// under them would shift what they redraw.
	exempt := map[string]bool{
		"helpers/progress.go": true, "helpers/stagetracker.go": true, "helpers/spinner.go": true,
	}
	// A literal that does not begin with a space or an escape, and (for Printf)
	// ends in a newline, is a whole line of output.
	bare := regexp.MustCompile(`fmt\.Printf\("(?:\\n)*[^ \\"][^"]*?\\n"|fmt\.Println\("(?:\\n)*[^ \\"][^"]*?"`)
	// A format that carries NO text of its own — only newlines, spaces and
	// verbs — is a passthrough for a value that already carries the gutter
	// (TitleStyle and SubtitleStyle set a margin, SectionHeading is TitleStyle,
	// Table.Render and the Create* helpers indent themselves). Routing those
	// through helpers.Printf indented them twice, which is what put every
	// section heading at column 4. So a passthrough is CORRECT with plain fmt.
	passthrough := regexp.MustCompile(`^(?:\\n|\s|%[svqd])*$`)
	literal := regexp.MustCompile(`fmt\.Print(?:f|ln)\("((?:[^"\\]|\\.)*)"`)
	var found []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if exempt[filepath.ToSlash(rel)] {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for _, line := range strings.Split(string(b), "\n") {
			if m := bare.FindString(line); m != "" {
				if lm := literal.FindStringSubmatch(line); lm != nil && passthrough.MatchString(lm[1]) {
					continue
				}
				found = append(found, rel+": "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		t.Errorf("%d command line(s) print with bare fmt instead of helpers.Printf/Println, "+
			"so they start at column 0 while everything around them starts at %d:\n  %s",
			len(found), BannerGutter, strings.Join(found, "\n  "))
	}
}

// Every bordered PANEL must carry the gutter.
//
// `BorderStyle` has it, so the thirty-odd commands that render into that style
// are fine. The ones that bite are the panels built with their own
// `lipgloss.NewStyle()`: the teardown confirmation, the provisioning summary,
// the ready panel and four boxes in `adhar get`. Each had no left margin, so
// they drew at column 0 while the banner, the heading and the prompt around
// them sat at two — most visibly on `adhar down`, where the panel carrying the
// words "cannot be undone" was the one thing out of line.
//
// Several of them also set `Margin(1, 0)`, whose horizontal 0 OVERWRITES an
// earlier MarginLeft, so the gutter has to come after it in the chain. That is
// why this checks the whole chain rather than its first few lines.
//
// Rounded borders only: `lipgloss.NormalBorder()` is used by `CodeStyle`, which
// is composed INLINE inside a sentence and must not be indented.
func TestEveryBorderedPanelCarriesTheGutter(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	var offenders []string
	checked := 0
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		lines := strings.Split(string(b), "\n")
		for i, line := range lines {
			if !strings.Contains(line, "Border(lipgloss.RoundedBorder())") {
				continue
			}
			checked++
			// Scan to the end of the chain: the first line carrying `Render(`.
			var chain []string
			for j := i; j < len(lines) && j < i+20; j++ {
				chain = append(chain, lines[j])
				if strings.Contains(lines[j], "Render(") {
					break
				}
			}
			if !strings.Contains(strings.Join(chain, "\n"), "MarginLeft") {
				rel, _ := filepath.Rel(root, path)
				offenders = append(offenders, fmt.Sprintf("%s:%d", rel, i+1))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("found no bordered panels; this guard is not looking at anything")
	}
	if len(offenders) > 0 {
		t.Errorf("%d bordered panel(s) set no MarginLeft, so they draw at column 0 while "+
			"everything around them starts at %d:\n  %s\n"+
			"Add `MarginLeft(helpers.BannerGutter)` LAST in the chain — after any Margin(…), "+
			"whose horizontal value would otherwise overwrite it.",
			len(offenders), BannerGutter, strings.Join(offenders, "\n  "))
	}
	t.Logf("checked %d bordered panel(s)", checked)
}

// An Unbounded table never shortens a cell.
//
// `adhar get secrets` sized its own budget wide enough to avoid truncation,
// which meant the caller had to know the table's internal arithmetic — and when
// the table started charging the gutter to that budget, every password lost its
// last characters to an ellipsis. A truncated credential is not a credential,
// so the intent is now stated rather than recomputed.
func TestAnUnboundedTableNeverTruncates(t *testing.T) {
	secret := "Hc-5NuJsYBlkgafY8h-LFbyI-7/7Nr1jpJu-"
	tb := NewTable("SERVICE", "USERNAME", "PASSWORD").Nested().Unbounded().WithBudget(40)
	tb.Row("Keycloak (admin)", "adhar-admin", secret)
	out := tb.Render()
	if !strings.Contains(out, secret) {
		t.Errorf("the credential was shortened despite Unbounded():\n%s", out)
	}
	if strings.Contains(out, "...") {
		t.Errorf("an ellipsis appears in an unbounded table:\n%s", out)
	}
}

// A table rendered inside a bordered panel must not indent itself: the panel
// already supplies the margin, and doing both put every boxed table three
// columns in instead of one — and made it two cells narrower than the caller
// sized it for, which is what truncated the passwords.
func TestANestedTableAddsNoGutterOfItsOwn(t *testing.T) {
	tb := NewTable("A", "B").Nested().WithBudget(40)
	tb.Row("one", "two")
	for i, line := range strings.Split(tb.Render(), "\n") {
		if strings.HasPrefix(line, " ") {
			t.Errorf("nested table line %d is indented: %q", i, line)
		}
	}
	// And its budget is not reduced: a nested table given 40 gets 40.
	wide := NewTable("A", "B").Nested().WithBudget(40)
	wide.Row(strings.Repeat("a", 19), strings.Repeat("b", 19))
	for _, line := range strings.Split(wide.Render(), "\n") {
		if lipgloss.Width(line) > 40 {
			t.Errorf("nested table exceeded its budget: %d cells", lipgloss.Width(line))
		}
	}
	if strings.Contains(wide.Render(), "...") {
		t.Error("a nested table truncated content that fits its stated budget")
	}
}
