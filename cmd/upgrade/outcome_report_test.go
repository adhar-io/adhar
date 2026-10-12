package upgrade

import (
	"strings"
	"testing"

	"adhar-io/adhar/cmd/helpers"
	"github.com/charmbracelet/x/ansi"
)

const churn = "flips every app OutOfSync"

// indentOf returns the column the first glyph of a line sits at.
func indentOf(line string) int {
	plain := ansi.Strip(line)
	return ansi.StringWidth(plain) - ansi.StringWidth(strings.TrimLeft(plain, " "))
}

// The churn warning belongs to the PACKAGES repo alone.
//
// Every Application tracks that monorepo, so a push there flips all of them
// OutOfSync and roughly ten minutes of re-comparison follows. Nothing tracks the
// environments repo except the Kargo projects and AppProjects built from it, so
// a change there churns nothing — yet an environments-only push still promised
// the fleet-wide wait (live 2026-10-12, pushing only `test/config.yaml`). That
// warning is what stops an operator reading post-upgrade churn as breakage, so
// spending it on a push that cannot cause any is worse than saying nothing.
func TestTheChurnWarningFollowsThePackagesRepo(t *testing.T) {
	for _, tc := range []struct {
		name      string
		diff      stackDiff
		wantChurn bool
		wantText  string
	}{
		{"nothing changed", stackDiff{}, false, "already in sync"},
		{"environments only", stackDiff{"environments": 1}, false, "only the environments repo changed"},
		{"packages only", stackDiff{"packages": 1}, true, "re-comparing all 77 applications"},
		{"both", stackDiff{"packages": 3, "environments": 1}, true, "re-comparing all 77 applications"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := upgradeOutcomeReport(tc.diff, 77, true)
			if strings.Contains(got, churn) != tc.wantChurn {
				verb := "must not"
				if tc.wantChurn {
					verb = "must"
				}
				t.Errorf("the report %s warn about fleet churn for %s:\n%s", verb, tc.name, got)
			}
			if !strings.Contains(got, tc.wantText) {
				t.Errorf("the report does not say %q for %s:\n%s", tc.wantText, tc.name, got)
			}
			// Wave-stuck recovery advice belongs to the churn case only.
			// Telling an operator that an app "needs its operation terminated"
			// right after telling them nothing is re-comparing is two
			// contradictory sentences, and advice for a situation they are not in.
			const advice = "wave-stuck app needs its operation"
			if strings.Contains(got, advice) != tc.wantChurn {
				verb := "must not"
				if tc.wantChurn {
					verb = "must"
				}
				t.Errorf("the report %s give wave-stuck recovery advice for %s:\n%s", verb, tc.name, got)
			}
		})
	}
}

// Every wrapped sentence must start at the same column as the sentence it
// continues.
//
// helpers.Indent adds the two-column gutter and a bullet line carries "● "
// after it, so the text begins at column 4. The continuations were hand-indented
// three spaces through bare fmt.Println and landed at column 3 — one short, so
// each one stepped left of its own first line.
func TestEveryReportLineStartsAtTheBulletColumn(t *testing.T) {
	// The bullet GLYPH sits at the gutter; its text begins two cells later, and
	// that is the column a continuation must match. Measured in display cells,
	// not bytes: "● " is four bytes and two columns, and using len() here is
	// what made the first version of this test demand column 6.
	bulletCol := helpers.BannerGutter
	textCol := bulletCol + ansi.StringWidth("● ")

	for _, diff := range []stackDiff{
		{},
		{"environments": 1},
		{"packages": 1},
	} {
		for _, skipFoundation := range []bool{true, false} {
			report := upgradeOutcomeReport(diff, 77, skipFoundation)
			lines := strings.Split(strings.TrimRight(report, "\n"), "\n")
			if len(lines) < 2 {
				t.Fatalf("report for %v has %d line(s); nothing to align", diff, len(lines))
			}
			for i, l := range lines {
				plain := ansi.Strip(l)
				if strings.TrimSpace(plain) == "" {
					continue
				}
				want, kind := textCol, "continuation"
				if strings.HasPrefix(strings.TrimLeft(plain, " "), "●") {
					want, kind = bulletCol, "bullet"
				}
				if got := indentOf(l); got != want {
					t.Errorf("diff=%v skipFoundation=%v line %d is a %s at column %d, want %d:\n%q",
						diff, skipFoundation, i, kind, got, want, plain)
				}
			}
		}
	}
}
