package main

import (
	"testing"
)

// `adhar batch` is a Develop command beside `function` (design 0025 §8.2):
// registered on the root and placed in that group, with the Phase-1 verbs and
// none of the Phase 2/3 ones, which arrive with the packages they need.
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
		for _, want := range []string{"list", "describe", "runs", "run", "logs", "stop", "resume", "restart", "hold", "release"} {
			if !have[want] {
				t.Errorf("adhar batch lacks the Phase-1 verb %q", want)
			}
		}
		for _, later := range []string{"queues", "backfill", "abandon", "recover", "dlq", "history"} {
			if have[later] {
				t.Errorf("adhar batch %s is Phase 2/3 and must not ship before its package", later)
			}
		}
	}
	if !found {
		t.Fatal("adhar batch is not registered on the root command")
	}
}
