package main

import (
	"testing"
)

// `adhar batch` is a Develop command beside `function` (design 0025 §8.2):
// registered on the root and placed in that group, with the Phase-1 verbs, the
// Phase-2 `queues` (core/volcano ships; the command is discovery-guarded, so a
// cluster without Volcano is told so), the Addendum-Q `schedules` (CLI-Q.1: the
// in-service schedule reports, read-only) and none of the Phase-3 ones, which
// arrive with the observer and the events they need.
func TestBatchIsRegisteredUnderDevelopWithThePhaseOneVerbs(t *testing.T) {
	var found bool
	for _, c := range rootCmd.Commands() {
		if c.Name() != "batch" {
			continue
		}
		found = true
		if c.GroupID != GroupDevelop {
			t.Errorf("adhar batch is in group %q, want %q", c.GroupID, GroupDevelop)
		}
		have := map[string]bool{}
		for _, sub := range c.Commands() {
			have[sub.Name()] = true
		}
		for _, want := range []string{"list", "describe", "runs", "run", "logs", "stop", "resume", "restart", "hold", "release", "queues", "schedules"} {
			if !have[want] {
				t.Errorf("adhar batch lacks the Phase-1/2/Q verb %q", want)
			}
		}
		for _, later := range []string{"backfill", "abandon", "recover", "dlq", "history"} {
			if have[later] {
				t.Errorf("adhar batch %s is Phase 3 and must not ship before its package", later)
			}
		}
	}
	if !found {
		t.Fatal("adhar batch is not registered on the root command")
	}
}
