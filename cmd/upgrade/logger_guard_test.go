package upgrade

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Any CLI command that reaches controller code must set controller-runtime's
// logger first.
//
// `adhar upgrade` calls the AdharPlatform reconciler's ApplyPlatformStack, which
// calls log.FromContext. With no logger configured, controller-runtime prints a
// 30-line goroutine dump:
//
//	[controller-runtime] log.SetLogger(...) was never called; logs will not be displayed.
//	Detected at:
//	  >  goroutine 1 [running]:
//	  …
//
// It landed directly after "Pushing stack and re-applying the platform
// ApplicationSet…", so a SUCCESSFUL upgrade looked like a crash — the command
// then printed "● Upgrade applied" underneath the trace. `adhar up` had always
// wired the logger (cmd/up/bootstrap.go, cmd/up/local.go); this command never
// did, and nothing caught the difference.
//
// Asserted against the source because that is where the mistake is made: the
// omission is invisible at runtime until a cluster is reachable AND the push
// path is exercised, which is the most expensive moment to discover it.
func TestUpgradeWiresTheControllerRuntimeLogger(t *testing.T) {
	src := readPackageSource(t)

	// Both branches must exist: discard keeps a normal run clean, and the stderr
	// logger is what makes `-v` useful when a push stalls mid-reconcile.
	for _, want := range []string{
		"ctrl.SetLogger(logr.Discard())",
		"ctrl.SetLogger(stdr.New(",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("cmd/upgrade must configure controller-runtime's logger (%q missing): "+
				"without it, calling the reconciler prints a goroutine dump into the middle "+
				"of a successful upgrade", want)
		}
	}

	// Ordering matters as much as presence. The logger has to be set before the
	// reconciler is invoked, or the dump is printed anyway.
	setAt := strings.Index(src, "ctrl.SetLogger(")
	useAt := strings.Index(src, "ApplyPlatformStack(")
	switch {
	case setAt < 0 || useAt < 0:
		t.Fatalf("expected both ctrl.SetLogger and ApplyPlatformStack in cmd/upgrade (set=%d use=%d)", setAt, useAt)
	case setAt > useAt:
		t.Error("ctrl.SetLogger must be called BEFORE ApplyPlatformStack — setting it afterwards " +
			"still lets controller-runtime print its 'was never called' trace")
	}
}

// readPackageSource concatenates this package's non-test Go files.
func readPackageSource(t *testing.T) string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("globbing package sources: %v", err)
	}
	var b strings.Builder
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		b.Write(data)
	}
	if b.Len() == 0 {
		t.Fatal("no non-test sources found in cmd/upgrade")
	}
	return b.String()
}
