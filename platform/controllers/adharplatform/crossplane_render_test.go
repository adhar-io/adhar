package adharplatform

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// renderAttempts bounds runRender's retries of the CLI's own start-up race.
const renderAttempts = 3

// runRender runs a `crossplane render` command whose Stderr is a *bytes.Buffer
// (Stdout may be the same buffer or another). It re-runs the command only when
// the CLI failed to START a function container — "cannot start function
// runtimes: … has no published binding for port" — which is the CLI inspecting
// a Docker container before Docker has published its port. That happens when
// the host is loaded (the full suite failed TestTheSmokeGateRendersWithCrossplane
// that way on 2026-11-18 and passed alone), and says nothing about the
// composition under test. Every other failure, including every rendering error,
// is returned from the first attempt, unchanged.
func runRender(cmd *exec.Cmd) error {
	stderr, ok := cmd.Stderr.(*bytes.Buffer)
	for attempt := 1; ; attempt++ {
		err := cmd.Run()
		if err == nil || !ok || attempt == renderAttempts || !functionStartRace(stderr.String()) {
			return err
		}
		next := exec.Command(cmd.Path, cmd.Args[1:]...)
		next.Env, next.Dir = cmd.Env, cmd.Dir
		if out, isBuffer := cmd.Stdout.(*bytes.Buffer); isBuffer {
			out.Reset()
		}
		stderr.Reset()
		next.Stdout, next.Stderr = cmd.Stdout, cmd.Stderr
		*cmd = *next
		time.Sleep(time.Duration(attempt) * 2 * time.Second)
	}
}

func functionStartRace(stderr string) bool {
	return strings.Contains(stderr, "cannot start function runtimes") && strings.Contains(stderr, "no published binding for port")
}

// Only the start race is retried: a real rendering error must fail the test on
// its first, and only, run.
func TestRunRenderRetriesOnlyTheFunctionStartRace(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	run := func(script string) (attempts int, stdout string, err error) {
		dir := t.TempDir()
		var out, errBuf bytes.Buffer
		cmd := exec.Command(sh, "-c", script)
		cmd.Dir = dir
		cmd.Stdout, cmd.Stderr = &out, &errBuf
		err = runRender(cmd)
		count, _ := os.ReadFile(filepath.Join(dir, "count"))
		return strings.Count(string(count), "\n"), out.String(), err
	}

	// Fails with the start race once, then renders.
	n, out, err := run(`echo x >> count; if [ "$(wc -l < count)" -lt 2 ]; then ` +
		`echo 'crossplane: error: cannot render composite resource: cannot start function runtimes: cannot start Function "f": ` +
		`container "" has no published binding for port 9443' >&2; exit 1; fi; echo rendered`)
	if err != nil || n != 2 || strings.TrimSpace(out) != "rendered" {
		t.Fatalf("the start race was not retried to success: attempts=%d out=%q err=%v", n, out, err)
	}

	// A rendering error is never retried.
	n, _, err = run(`echo x >> count; echo 'crossplane: error: cannot render composite resource: template: bad' >&2; exit 1`)
	if err == nil || n != 1 {
		t.Fatalf("a rendering error was retried or swallowed: attempts=%d err=%v", n, err)
	}

	// The race that never clears ends after renderAttempts, still failing.
	n, _, err = run(`echo x >> count; echo 'cannot start function runtimes: container "" has no published binding for port 9443' >&2; exit 1`)
	if err == nil || n != renderAttempts {
		t.Fatalf("a persistent start failure ran %d times (want %d) or passed: %v", n, renderAttempts, err)
	}
}
