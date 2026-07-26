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
