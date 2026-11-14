package main

import (
	"testing"
)

// `adhar batch` is a Develop command beside `function` (design 0025 §8.2):
// registered on the root and placed in that group, with the Phase-1 verbs, the
// Phase-2 `queues` (core/volcano ships; the command is discovery-guarded, so a
// cluster without Volcano is told so), the Addendum-Q `schedules` (CLI-Q.1: the
// in-service schedule reports, read-only) and the Phase-3 day-2 verbs (CLI-3.1),
// which shipped with the batch observer and the adhar.batch events they read.
func TestBatchIsRegisteredUnderDevelopWithItsVerbs(t *testing.T) {
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
		for _, want := range []string{"list", "describe", "runs", "run", "logs", "stop", "resume", "restart", "hold", "release", "queues", "schedules",
			"backfill", "abandon", "recover", "dlq", "history"} {
			if !have[want] {
				t.Errorf("adhar batch lacks the verb %q", want)
			}
		}
	}
	if !found {
		t.Fatal("adhar batch is not registered on the root command")
	}
}
