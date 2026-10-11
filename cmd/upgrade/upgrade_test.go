package upgrade

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitDiffNames(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()

	write := func(dir, name, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// same file, modified file, added file (only in b), deleted file (only in a)
	write(a, "same.yaml", "identical\n")
	write(b, "same.yaml", "identical\n")
	write(a, "pkg/modified.yaml", "old\n")
	write(b, "pkg/modified.yaml", "new\n")
	write(b, "pkg/added.yaml", "added\n")
	write(a, "pkg/deleted.yaml", "deleted\n")

	names, err := gitDiffNames(t.Context(), a, b)
	if err != nil {
		t.Fatalf("gitDiffNames: %v", err)
	}
	if len(names) != 3 {
		t.Fatalf("expected 3 differing files, got %d: %v", len(names), names)
	}
	joined := strings.Join(names, "\n")
	for _, want := range []string{"pkg/modified.yaml", "pkg/added.yaml", "pkg/deleted.yaml"} {
		if !strings.Contains(joined, want) {
			t.Errorf("diff output missing %s: %v", want, names)
		}
	}
	if strings.Contains(joined, "same.yaml") {
		t.Errorf("identical file reported as differing: %v", names)
	}
}

func TestGitDiffNamesIdentical(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	for _, d := range []string{a, b} {
		if err := os.WriteFile(filepath.Join(d, "f.yaml"), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	names, err := gitDiffNames(t.Context(), a, b)
	if err != nil {
		t.Fatalf("gitDiffNames: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("expected no differences, got %v", names)
	}
}

func TestSanitize(t *testing.T) {
	if got := sanitize("error at https://user:hunter2@host/repo", "hunter2"); strings.Contains(got, "hunter2") {
		t.Errorf("credential leaked: %s", got)
	}
	if got := sanitize("plain", ""); got != "plain" {
		t.Errorf("empty secret must be a no-op, got %s", got)
	}
}

// The fleet-churn warning must be printed ONLY when something was pushed.
//
// `adhar upgrade` on an in-sync cluster printed "packages: no changes", then
// "Pushing stack…", then "ArgoCD is re-comparing all 78 applications … expect
// ~10 min of churn". None of that happens: a force push of identical content
// creates no commit, so ArgoCD never sees a new revision. Verified live on
// 2026-10-11, where a no-op run left the packages repo HEAD untouched.
//
// The warning is load-bearing — it is what stops an operator reading
// post-upgrade churn as breakage — so spending it on an event that cannot occur
// is worse than saying nothing. Both the announcement and the closing report
// must therefore branch on hasDiff.
//
// Asserted against the source with comments stripped, so the prose above (which
// quotes the old strings) cannot satisfy the check.
func TestUpgradeOnlyWarnsAboutChurnWhenItPushed(t *testing.T) {
	src := upgradeSourceWithoutComments(t)

	// Anchor on the region BETWEEN the apply and the churn sentence.
	//
	// A bare search for "if !hasDiff {" is not enough: phase 2 already has one
	// (the "Stack already in sync" notice), so the first match sits before the
	// warning and the check passes even with the closing gate deleted. That
	// version of this test passed against the bug it claimed to catch.
	apply := strings.Index(src, "ApplyPlatformStack(ctx")
	churn := strings.Index(src, "flips every app OutOfSync")
	if apply < 0 {
		t.Fatal("cannot find the ApplyPlatformStack call; this guard is no longer checking anything")
	}
	if churn < 0 {
		t.Fatal("the churn warning is gone entirely; it is needed on a real push, where ~10 min of " +
			"re-comparison is expected and an operator who does not know that reads it as breakage")
	}
	if churn < apply {
		t.Fatalf("the churn warning (at %d) precedes the apply (at %d)", churn, apply)
	}
	between := src[apply:churn]
	if !strings.Contains(between, "if !hasDiff {") || !strings.Contains(between, "return nil") {
		t.Error("nothing returns early for a no-op between applying the stack and the churn warning; " +
			"a run that pushed nothing therefore still warns about ten minutes of fleet-wide " +
			"re-comparison that cannot happen")
	}
	// And the "Pushing stack" announcement must branch too, so it does not
	// claim a push after reporting no changes.
	if !strings.Contains(src, `helpers.Println("◌ Re-applying the platform ApplicationSet…")`) {
		t.Error("the phase-3 announcement always says it is pushing the stack; after \"no changes\" " +
			"that is not what happens, and the operator has no way to tell a real push from a no-op")
	}
}

// upgradeSourceWithoutComments returns upgrade.go with // comments removed, so
// an assertion about the CODE is not satisfied by prose describing the bug.
func upgradeSourceWithoutComments(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("upgrade.go")
	if err != nil {
		t.Fatalf("reading upgrade.go: %v", err)
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
