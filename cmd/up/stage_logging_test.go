package up

import "testing"

// The status poll ticks every two seconds, so a line logged from inside it would
// repeat dozens of times over a bring-up. logOnce keys on the message's subject so
// each fact is recorded once.
//
// This matters because the stage tracker is a LIVE view: it repaints in place and
// leaves nothing behind. A piped or CI run has only these log lines to show what
// actually came up and in what order.
func TestLogOnceRecordsEachFactOnce(t *testing.T) {
	seen := map[string]bool{}
	count := 0
	// logOnce calls logger.Info, which we cannot easily capture here; the
	// behaviour under test is the gate, so observe the map it maintains.
	for i := 0; i < 5; i++ {
		before := len(seen)
		logOnce(seen, "argocd", "ArgoCD is serving")
		if len(seen) != before {
			count++
		}
	}
	if count != 1 {
		t.Errorf("the same key should be admitted once, got %d", count)
	}
	if !seen["argocd"] {
		t.Error("the key was not recorded")
	}
}

// A changing detail (the sync count) is a different fact each time, so it must be
// keyed by value — otherwise only the first count is ever reported and the sync
// looks stalled in the log.
func TestLogOnceAdmitsEachDistinctProgressValue(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range []string{"3/75", "10/75", "75/75", "10/75"} {
		logOnce(seen, "gitops:"+p, "GitOps sync: "+p)
	}
	if len(seen) != 3 {
		t.Errorf("want 3 distinct progress lines, got %d: %v", len(seen), seen)
	}
}

// A nil map must not panic: the poll's recover would swallow it and the remaining
// stages would silently stop advancing.
func TestLogOnceToleratesANilMap(t *testing.T) {
	logOnce(nil, "k", "message") // must not panic
}
