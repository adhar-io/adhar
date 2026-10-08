package up

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A cloud saying "ready" is bookkeeping, not a health check.
//
// On 2026-10-07 the Civo API reported a managed cluster ACTIVE with ready=true
// while nothing was listening on its endpoint: the master node answered ping and
// SSH, port 6443 was dark, and `adhar up` sat for twenty minutes in a later
// stage without ever naming the cause. The provider's wait returns on Civo's own
// flag (`waitForCivoClusterReady`), so the bootstrap measures the one thing that
// matters instead — whether the API answers.

func TestWaitForClusterAPIReturnsAsSoonAsItAnswers(t *testing.T) {
	calls := 0
	probe := func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("connection refused")
		}
		return nil
	}

	start := time.Now()
	err := waitForClusterAPIReachable(context.Background(), "https://10.0.0.1:6443", probe,
		time.Second, time.Millisecond, nil)
	if err != nil {
		t.Fatalf("waitForClusterAPIReachable: %v", err)
	}
	if calls != 3 {
		t.Errorf("probed %d times, want 3 (it must keep trying while the API is still starting)", calls)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Error("the wait did not return promptly once the API answered")
	}
}

// The failure is the point: a dark endpoint has to be reported in a bounded
// time, with the endpoint named and the diagnosis that fits.
func TestWaitForClusterAPIFailsWithTheEndpointAndADiagnosis(t *testing.T) {
	probe := func(context.Context) error { return errors.New("i/o timeout") }

	err := waitForClusterAPIReachable(context.Background(), "https://212.2.248.136:6443", probe,
		30*time.Millisecond, 5*time.Millisecond, nil)
	if err == nil {
		t.Fatal("a dark API endpoint was reported as reachable")
	}
	for _, want := range []string{
		"212.2.248.136:6443", // which cluster
		"did not answer",     // what happened
		"i/o timeout",        // the underlying error, kept
		"ready",              // the cloud's flag is not a health check
		"--recreate",         // what to do about it
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q:\n%v", want, err)
		}
	}
}

// It must not outlive its budget: the whole reason this exists is that the
// previous behaviour was an unbounded wait.
func TestWaitForClusterAPIRespectsItsBudget(t *testing.T) {
	probe := func(context.Context) error { return errors.New("connection refused") }

	start := time.Now()
	if err := waitForClusterAPIReachable(context.Background(), "https://10.0.0.1:6443", probe,
		40*time.Millisecond, 5*time.Millisecond, nil); err == nil {
		t.Fatal("expected a failure")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("the wait took %s against a 40ms budget", elapsed)
	}
}

// A cancelled run stops immediately and says so, rather than finishing its
// retry budget after the operator pressed Ctrl-C.
func TestWaitForClusterAPIStopsWhenTheRunIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := waitForClusterAPIReachable(ctx, "https://10.0.0.1:6443",
		func(context.Context) error { return errors.New("connection refused") },
		time.Minute, time.Millisecond, nil)
	if err == nil {
		t.Fatal("expected a failure")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error does not wrap context.Canceled: %v", err)
	}
}

// The "still waiting" line is said once. A cluster that needs a few seconds is
// normal and must not produce a wall of text in the checklist.
func TestWaitForClusterAPIAnnouncesOnlyOnce(t *testing.T) {
	var logged []string
	calls := 0
	probe := func(context.Context) error {
		calls++
		if calls < 5 {
			return errors.New("connection refused")
		}
		return nil
	}

	if err := waitForClusterAPIReachable(context.Background(), "https://10.0.0.1:6443", probe,
		time.Second, time.Millisecond, func(m string) { logged = append(logged, m) }); err != nil {
		t.Fatalf("waitForClusterAPIReachable: %v", err)
	}
	if len(logged) != 1 {
		t.Errorf("logged %d lines, want exactly 1: %v", len(logged), logged)
	}
	if len(logged) == 1 && !strings.Contains(logged[0], "10.0.0.1:6443") {
		t.Errorf("the line does not name the endpoint: %q", logged[0])
	}
}
