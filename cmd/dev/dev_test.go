package dev

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The stack is chosen from a marker file, because that is the one thing a
// developer cannot forget to set. Getting this wrong means running the source in
// the wrong runtime, which fails in ways that look like the app being broken.
func TestDetectStackFromMarkerFile(t *testing.T) {
	cases := map[string]string{
		"go.mod":           "Go",
		"package.json":     "Node",
		"pyproject.toml":   "Python",
		"requirements.txt": "Python",
		"pom.xml":          "Java",
	}
	for marker, want := range cases {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, marker), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := detectStack(dir)
		if err != nil {
			t.Errorf("%s: %v", marker, err)
			continue
		}
		if got.name != want {
			t.Errorf("%s gave %q, want %q", marker, got.name, want)
		}
		if got.image == "" || got.command == "" {
			t.Errorf("%s: a stack needs both an image and a start command", marker)
		}
	}
}

// An unrecognised project must say so and name the escape hatch, rather than
// silently guessing a runtime.
func TestDetectStackNamesTheEscapeHatch(t *testing.T) {
	_, err := detectStack(t.TempDir())
	if err == nil {
		t.Fatal("an empty directory must not resolve to a stack")
	}
	for _, want := range []string{"--image", "--command", "go.mod"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should mention %q, got: %v", want, err)
		}
	}
}

// The sync excludes the directories that make the difference between a one-second
// loop and a thirty-second one. node_modules alone can be hundreds of megabytes.
func TestIgnoredPathsCoverTheExpensiveDirectories(t *testing.T) {
	sep := string(os.PathSeparator)
	for _, p := range []string{
		"/repo" + sep + "node_modules" + sep + "react" + sep + "index.js",
		"/repo" + sep + ".git" + sep + "HEAD",
		"/repo" + sep + "target" + sep + "app.jar",
		"/repo" + sep + "__pycache__" + sep + "m.pyc",
		"/repo" + sep + "main.go~",
		"/repo" + sep + ".#main.go",
		"/repo" + sep + "x.swp",
	} {
		if !ignored(p) {
			t.Errorf("%q should be ignored by the watcher and the sync", p)
		}
	}
	// Real source must not be skipped, or saves appear to do nothing.
	for _, p := range []string{
		"/repo" + sep + "main.go",
		"/repo" + sep + "src" + sep + "app.ts",
		"/repo" + sep + "go.mod",
	} {
		if ignored(p) {
			t.Errorf("%q must be watched", p)
		}
	}
}

// A dependency manifest change needs an install, not just a restart: syncing a
// lockfile without reinstalling leaves the app running against the old tree, which
// looks like the sync having silently failed.
func TestDependencyChangeTriggersInstall(t *testing.T) {
	st := stack{deps: []string{"go.mod", "go.sum"}}
	dir := "/repo"
	sep := string(os.PathSeparator)

	if !touchesDeps([]string{dir + sep + "go.mod"}, dir, st) {
		t.Error("a go.mod change must trigger a dependency install")
	}
	if touchesDeps([]string{dir + sep + "main.go"}, dir, st) {
		t.Error("an ordinary source change must not trigger a dependency install")
	}
	// A file with the same name deeper in the tree is not the root manifest.
	if touchesDeps([]string{dir + sep + "vendor" + sep + "go.mod"}, dir, st) {
		t.Error("only the root manifest counts, not a nested file of the same name")
	}
}

// Kubernetes names bound what is usable as a namespace, so a bad name must be
// rejected before anything is created rather than failing halfway through.
func TestValidateName(t *testing.T) {
	for _, ok := range []string{"api", "web-frontend", "svc2"} {
		if err := validateName(ok); err != nil {
			t.Errorf("%q should be accepted: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "My App", "under_score", "UPPER", "dots.here"} {
		if err := validateName(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestShellQuoteHandlesQuotes(t *testing.T) {
	if got := shellQuote(`go run ./...`); got != `'go run ./...'` {
		t.Errorf("got %s", got)
	}
	// An embedded single quote must not break out of the quoting.
	if got := shellQuote(`echo 'hi'`); !strings.HasPrefix(got, "'") || strings.Contains(got, `''hi''`) {
		t.Errorf("embedded quotes not escaped safely: %s", got)
	}
}
