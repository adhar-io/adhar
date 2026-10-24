package upgrade

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The environments repo is edited adhar-controlplane by Kargo, so a promotion shows up
// as a "differing" file and a push reverts it — twice on live clusters with
// nothing on screen to say the change was Kargo's. The diff must name it.
func TestKargoWrittenFilesAreRecognised(t *testing.T) {
	dir := t.TempDir()
	git := func(env []string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), append([]string{
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_COMMITTER_NAME=x", "GIT_COMMITTER_EMAIL=x@x",
		}, env...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	platform := []string{"GIT_AUTHOR_NAME=Adhar Platform", "GIT_AUTHOR_EMAIL=p@x"}
	kargo := []string{"GIT_AUTHOR_NAME=Kargo", "GIT_AUTHOR_EMAIL=k@x"}

	git(nil, "init", "-q")
	write("test/config.yaml", "environment: test\n")
	write("dev/config.yaml", "environment: dev\n")
	git(platform, "add", ".")
	git(platform, "commit", "-qm", "Update: Add environments content")
	write("test/config.yaml", "environment: test\nimage: v2\n")
	git(kargo, "commit", "-qam", "promote: dev -> test")

	gitDir := filepath.Join(dir, ".git")
	if got := inClusterChange(t.Context(), gitDir, "test/config.yaml"); got != `Kargo ("promote: dev -> test")` {
		t.Errorf("Kargo's promotion of test/config.yaml was not recognised: %q", got)
	}
	if got := inClusterChange(t.Context(), gitDir, "dev/config.yaml"); got != "" {
		t.Errorf("a platform-written file was labelled as adhar-controlplane: %q", got)
	}
	// The console's Marketplace commits as the platform's own Gitea account,
	// so it is recognised by its message, not its author.
	write("applicationset.yaml", "enabled: \"true\"\n")
	git(platform, "add", ".")
	git(platform, "commit", "-qm", "chore(marketplace): enable trino via Adhar Console")
	if got := inClusterChange(t.Context(), gitDir, "applicationset.yaml"); got != `the Marketplace ("chore(marketplace): enable trino via Adhar Console")` {
		t.Errorf("a Marketplace toggle was not recognised: %q", got)
	}
	got := classifyChanges(t.Context(), gitDir, []string{"M  test/config.yaml", "M  dev/config.yaml", "A  test/config.yaml"})
	if got[0].Owner == "" || got[1].Owner != "" || got[2].Owner != "" {
		t.Errorf("only the modified Kargo-written file may carry an owner: %+v", got)
	}
}

func TestStackChangesRenderAtTheGutterWithAWarningForPromotions(t *testing.T) {
	c := stackChanges{
		{Repo: "packages", Files: []fileChange{
			{Status: "M", Path: "security/rotate-job.yaml"},
			{Status: "A", Path: "new.yaml"},
			{Status: "D", Path: "old.yaml"},
		}},
		{Repo: "environments", Files: []fileChange{
			{Status: "M", Path: "test/config.yaml", Owner: `Kargo ("promote: dev -> test")`},
		}},
	}
	out := ansi.Strip(renderStackChanges(c))
	for _, want := range []string{
		"  packages  3 files\n",
		"    ~ security/rotate-job.yaml\n",
		"    + new.yaml\n",
		"    − old.yaml\n",
		"  environments  1 file\n",
		`changed adhar-controlplane by Kargo ("promote: dev -> test") — pushing reverts it`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered changes lack %q:\n%s", want, out)
		}
	}
	if p := ansi.Strip(confirmPrompt(c)); !strings.Contains(p, "changed adhar-controlplane") || !strings.Contains(p, "Push 4 files to Gitea anyway?") {
		t.Errorf("the prompt does not warn that the push reverts a promotion:\n%s", p)
	}
	if p := ansi.Strip(confirmPrompt(c[:1])); p != "Push 3 files to Gitea and sync? [y/N]: " {
		t.Errorf("plain prompt = %q", p)
	}
	if renderStackChanges(stackChanges{{Repo: "packages"}}) != "" {
		t.Error("nothing changed, yet a file list was rendered")
	}
}

func TestParseNameStatus(t *testing.T) {
	for in, want := range map[string]fileChange{
		"M  a/b.yaml":        {Status: "M", Path: "a/b.yaml"},
		"R100  old.y  new.y": {Status: "R100", Path: "old.y → new.y"},
	} {
		if got := parseNameStatus(in); got != want {
			t.Errorf("parseNameStatus(%q) = %+v, want %+v", in, got, want)
		}
	}
}
