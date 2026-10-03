package helpers

import (
	"io"
	"testing"
)

// Log must leave the tracker ready for the next animated redraw: a block on
// screen and lastLines describing it.
//
// render() repositions by moving the cursor up lastLines and clearing to the
// end of the screen, and that move is guarded by `lastLines > 0`. Log clears
// the block and zeroes lastLines before writing its line, so if it returned
// without redrawing, the very next 100ms tick would see 0, skip the move, and
// APPEND a second checklist below the first.
//
// Log also holds the mutex across the whole clear → print → redraw sequence.
// Releasing it in the middle let the animation goroutine land in that window
// and render with lastLines == 0, which is the same append. That window is real
// but was NOT what produced the duplicate block seen on 2026-10-03: a test
// hammering Log against the ticker passes against the racy version too, so it
// proves nothing and is not kept here. The actual cause was code printing to
// stdout from inside the provisioning loop while the tracker animated stderr —
// guarded by TestNothingPrintsWhileTheStageTrackerIsLive in cmd/up.
func TestTrackerLogLeavesLastLinesConsistent(t *testing.T) {
	tr := NewStageTracker(io.Discard, "Provisioning prod",
		[]StageDef{{Label: "Preflight"}, {Label: "Cloud cluster"}}, true)
	// NewStageTracker only enables rendering for an *os.File; force it so the
	// redraw path runs against a writer the test can throw away.
	tr.isTTY = true

	// Not started, so no animation goroutine competes: this isolates Log's own
	// bookkeeping. Nothing is drawn, so nothing may be claimed to be on screen.
	tr.Log("a line")
	tr.mu.Lock()
	last := tr.lastLines
	tr.mu.Unlock()
	if last != 0 {
		t.Errorf("before Start, Log must not claim a block is on screen; lastLines=%d", last)
	}

	tr.Start()
	tr.Log("another line")
	tr.mu.Lock()
	last = tr.lastLines
	tr.mu.Unlock()
	tr.Stop()

	// title + 2 stages = 3 lines.
	if last != 3 {
		t.Errorf("after Log while running, lastLines must describe the redrawn block (want 3, got %d) — "+
			"a stale 0 makes the next tick append a duplicate instead of repositioning", last)
	}
}
