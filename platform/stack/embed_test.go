package stack

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// withCache points extraction at a throw-away directory.
func withCache(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := cacheRoot
	cacheRoot = func() (string, error) { return dir, nil }
	t.Cleanup(func() { cacheRoot = prev })
	return dir
}

// trackedStackFiles is what Git holds under platform/stack, minus Go sources:
// exactly what a source checkout seeds into the GitOps repositories.
func trackedStackFiles(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard", "--", "packages", "environments").Output()
	if err != nil {
		t.Skipf("git not available: %v", err)
	}
	var files []string
	for _, f := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if f == "" {
			continue
		}
		if _, err := os.Stat(f); err != nil {
			continue // deleted in the working tree
		}
		files = append(files, f)
	}
	sort.Strings(files)
	return files
}

// The binary must carry the WHOLE stack. A pattern that drops dot-files, or a
// new top-level directory nobody added to the embed directive, produces a
// platform that differs from the one a checkout seeds — and only on machines
// without a clone, which is to say never on a developer's.
func TestTheEmbeddedStackIsTheWholeStack(t *testing.T) {
	embedded, err := Files()
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, f := range embedded {
		have[f] = true
	}
	var missing []string
	for _, f := range trackedStackFiles(t) {
		if !have[f] {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%d stack file(s) are not embedded in the binary, e.g. %v", len(missing), missing[:min(5, len(missing))])
	}

	// Every top-level entry is either embedded or deliberately source-only.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".go") {
			continue
		}
		if name == "generated" {
			continue // build products, embedded separately
		}
		if e.IsDir() {
			if _, err := fs.Stat(FS, name); err != nil {
				t.Errorf("platform/stack/%s is not covered by the go:embed directive in embed.go", name)
			}
			continue
		}
		if !have[name] {
			t.Errorf("platform/stack/%s is not covered by the go:embed directive in embed.go", name)
		}
	}
}

// An extracted stack must be byte-identical to the tree, executable bits
// included: embed.FS has no modes, and a script seeded as 0644 shows up as a
// mode change in every GitOps repository first seeded from a checkout.
func TestAnExtractedStackMatchesTheTree(t *testing.T) {
	withCache(t)
	dir, err := Materialize()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range trackedStackFiles(t) {
		want, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if string(want) != string(got) {
			t.Fatalf("%s differs after extraction", f)
		}
		wi, _ := os.Stat(f)
		gi, _ := os.Stat(filepath.Join(dir, f))
		if (wi.Mode()&0o100 != 0) != (gi.Mode()&0o100 != 0) {
			t.Errorf("%s: executable bit is %v in the tree and %v after extraction", f, wi.Mode()&0o100 != 0, gi.Mode()&0o100 != 0)
		}
	}
}

func TestExtractionIsReusedAndRepairsATamperedCopy(t *testing.T) {
	root := withCache(t)
	first, err := Materialize()
	if err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(first, "adhar-appset-production.yaml")
	if err := os.WriteFile(victim, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A stale extraction from another version must not survive either.
	stale := filepath.Join(root, "0000000000000000")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	second, err := Materialize()
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("the same binary extracted to two places: %s, %s", first, second)
	}
	want, _ := FS.ReadFile("adhar-appset-production.yaml")
	got, _ := os.ReadFile(victim)
	if string(got) != string(want) {
		t.Fatal("a modified cache was reused: what is in it is pushed to a cluster")
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("the extraction of a replaced binary was left behind")
	}
}

func TestResolvePrecedence(t *testing.T) {
	withCache(t)
	t.Setenv(EnvStackDir, "")

	// Beside a folder called platform/stack — even at the root of an adhar source
	// checkout — the CLI uses what it was built with. `adhar upgrade` run from the
	// repository root once pushed the working tree instead of the binary's stack.
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "platform", "stack", "packages"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte("module adhar-io/adhar\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)
	src, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if !src.Embedded() {
		t.Fatalf("a local ./platform/stack was used instead of the stack built into the binary: %+v", src)
	}

	// Only a directory the operator names replaces it: the environment variable, or a flag.
	exported := filepath.Join(t.TempDir(), "mine")
	if err := Export(exported); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvStackDir, exported)
	if src, err = Resolve(""); err != nil || src.Origin != OriginExplicit || src.Dir != exported {
		t.Fatalf("ADHAR_STACK_DIR ignored: %+v, %v", src, err)
	}

	// A named directory that is not a stack is an error, never a silent fallback.
	if _, err := Resolve(filepath.Join(work, "nope")); err == nil {
		t.Fatal("a missing --stack-dir fell back to another stack")
	}
}

func TestExportRefusesANonEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Export(dir); err == nil {
		t.Fatal("export wrote into a directory that already held files")
	}
}

// A directory with a go.mod is invisible to go:embed, so those trees ride in an
// archive `go generate` builds. It is compiled in, so a stale one ships the old
// skeleton from every installed binary while a checkout seeds the new one.
// `make test` regenerates it first; this catches the build that did not.
func TestTheNestedModuleBundleIsCurrent(t *testing.T) {
	want, err := BuildNestedModuleBundle(".")
	if err != nil {
		t.Fatal(err)
	}
	got, err := generated.ReadFile(NestedModuleBundle)
	if err != nil {
		t.Fatalf("%s was not built: run `go generate ./platform/stack/` (make build and make test do)", NestedModuleBundle)
	}
	if string(got) != string(want) {
		t.Fatalf("%s is stale: run `go generate ./platform/stack/`", NestedModuleBundle)
	}
}
