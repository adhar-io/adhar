package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// A provider must not write to stdout while a cluster is being built or torn
// down, because something else owns the cursor.
//
// `adhar up` draws a checklist that redraws in place: it moves the cursor up
// `lastLines` and clears to the end of the screen (cmd/helpers/stagetracker.go).
// It redirects the platform logger AND the standard `log` package into itself
// (startCloudProgress), so anything a provider logs becomes stage detail. A bare
// `fmt.Printf` goes straight past that to the terminal, scrolls the block
// without the tracker knowing, and the next redraw lands in the wrong place —
// leaving orphaned half-copies of the checklist on screen:
//
//	Provisioning prod  2s
//	  ✓  Preflight  2s
//	  ⠹  Cloud cluster  adhar · aws · ap-southeast-1 — creating machines…
//	Provisioning prod  2s
//	  ✓  Preflight  2s
//	Provisioning prod  4s
//	…
//
// The AWS provider had 133 of them while azure, gcp, civo and custom had none,
// which is why only AWS printed like that (2026-10-08). `adhar down` has the
// same shape: its Bubble Tea view captures the logger and a direct print tears
// the frame.
//
// A REPORT command is the exception: `InvestigateCluster` exists to print to
// stdout and runs outside any tracker. It is allowed, and named here so the
// exception is deliberate rather than discovered.
func TestProvidersDoNotWriteProgressToStdout(t *testing.T) {
	// Functions whose whole purpose is to print a report for a human.
	allowed := map[string]bool{
		"InvestigateCluster": true,
	}

	root := providersRoot(t)
	dirs, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading %s: %v", root, err)
	}

	funcStart := regexp.MustCompile(`^func (?:\([^)]*\) )?(\w+)`)
	var offenders []string
	checked := 0

	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		files, err := filepath.Glob(filepath.Join(root, d.Name(), "*.go"))
		if err != nil {
			t.Fatalf("globbing %s: %v", d.Name(), err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("reading %s: %v", file, err)
			}
			checked++
			fn := ""
			for i, line := range strings.Split(string(raw), "\n") {
				if m := funcStart.FindStringSubmatch(line); m != nil {
					fn = m[1]
				}
				if !strings.Contains(line, "fmt.Print") {
					continue
				}
				if allowed[fn] {
					continue
				}
				offenders = append(offenders, fmt.Sprintf("%s:%d (%s)", shortPath(file), i+1, fn))
			}
		}
	}

	if checked == 0 {
		t.Fatal("no provider sources were read; this guard is not looking at the providers")
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Errorf("%d provider line(s) write to stdout while the CLI owns the cursor — use log.Printf, which the "+
			"progress tracker captures as stage detail:\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// providersRoot is the directory holding the per-cloud packages.
func providersRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return dir
}

func shortPath(p string) string {
	if i := strings.Index(p, "platform/providers/"); i >= 0 {
		return p[i+len("platform/providers/"):]
	}
	return filepath.Base(filepath.Dir(p)) + "/" + filepath.Base(p)
}
