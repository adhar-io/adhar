package helpers

import (
	"bytes"
	"strings"
	"testing"
)

func TestStageTrackerSetDetailPrintsProgressOnceOnPlainOutput(t *testing.T) {
	var buf bytes.Buffer
	tr := NewStageTracker(&buf, "Provisioning", []StageDef{{Label: "GitOps sync", Detail: "apps"}}, false)
	tr.Start()
	tr.Activate(0)
	tr.SetDetail(0, "3/28 apps Synced + Healthy")
	tr.SetDetail(0, "3/28 apps Synced + Healthy") // unchanged: not printed again
	tr.SetDetail(0, "28/28 apps Synced + Healthy")
	tr.Done(0)
	tr.Stop()
	out := buf.String()
	if strings.Count(out, "3/28 apps") != 1 || strings.Count(out, "28/28 apps") != 1 {
		t.Errorf("each distinct detail is printed exactly once:\n%s", out)
	}
	// Assert against the vocabulary constant, not a hardcoded glyph. This line
	// spelled "●" literally and so broke the moment success became a check mark,
	// reporting "stage completion still rendered" for a purely cosmetic change.
	if !strings.Contains(out, "GitOps sync") || !strings.Contains(out, IconReady) {
		t.Errorf("stage completion should be rendered with %q:\n%s", IconReady, out)
	}
	tr.SetDetail(5, "out of range must not panic")
}

// The duplicated title was a cursor-arithmetic failure. render() repositions with
// "\x1b[<lastLines>A\r\x1b[J" — move up by however many lines it last drew, then
// clear — so anything else writing to the terminal in between makes that count
// wrong, the next redraw starts too low, and the previous block is orphaned:
//
//	Provisioning Adhar platform  0s      <- never overwritten
//	Provisioning Adhar platform  2m56s   <- the live block
//
// Log() exists so progress output cannot cause that: it erases the block, writes
// the line where it will scroll up and stay, and redraws beneath.
func TestTrackerLogErasesTheBlockBeforeWritingAndRedrawsAfter(t *testing.T) {
	var buf bytes.Buffer
	tr := NewStageTracker(&buf, "Provisioning Adhar platform",
		[]StageDef{{Label: "Kind cluster"}, {Label: "Platform CRDs"}}, false)
	tr.isTTY = true // exercise the repositioning path without a terminal
	tr.Start()
	tr.Log("created cluster adhar")
	tr.Stop()

	out := buf.String()
	if !strings.Contains(out, "created cluster adhar") {
		t.Fatalf("the log line was dropped:\n%q", out)
	}
	// Exactly one title survives: the orphan is what the bug looked like.
	if n := strings.Count(out, "Provisioning Adhar platform"); n < 1 {
		t.Fatalf("title missing entirely:\n%q", out)
	}
	// The erase sequence must appear before the log line, or the line lands
	// inside the block instead of above it.
	erase, line := strings.Index(out, "\x1b[J"), strings.Index(out, "created cluster adhar")
	if erase < 0 {
		t.Error("no erase sequence was emitted, so the block was not cleared first")
	} else if erase > line {
		t.Error("the block was cleared AFTER the log line, which leaves the line inside it")
	}
}

// A non-interactive run has no block to protect, so lines print plainly and in
// order — CI logs must stay readable.
func TestTrackerLogIsPlainWhenNotATerminal(t *testing.T) {
	var buf bytes.Buffer
	tr := NewStageTracker(&buf, "Provisioning", []StageDef{{Label: "Kind cluster"}}, false)
	tr.Start()
	tr.Log("first")
	tr.Log("second")
	tr.Stop()
	out := buf.String()
	if strings.Contains(out, "\x1b[") {
		t.Errorf("no escape sequences belong in non-TTY output: %q", out)
	}
	if i, j := strings.Index(out, "first"), strings.Index(out, "second"); i < 0 || j < 0 || i > j {
		t.Errorf("lines out of order or missing: %q", out)
	}
}

// A logger may emit several lines in one Write, and each has to be placed
// individually; a partial line is held until its newline so a message is never
// split across two repositionings.
func TestTrackerWriterSplitsLinesAndHoldsPartials(t *testing.T) {
	var buf bytes.Buffer
	tr := NewStageTracker(&buf, "Provisioning", []StageDef{{Label: "Kind cluster"}}, false)
	tr.Start()
	w := NewTrackerWriter(tr)

	if _, err := w.Write([]byte("alpha\nbeta\n")); err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("partial without newline"))
	out := buf.String()
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Errorf("complete lines were not emitted: %q", out)
	}
	if strings.Contains(out, "partial") {
		t.Error("a partial line must be held until its newline arrives")
	}
	w.Flush()
	if !strings.Contains(buf.String(), "partial without newline") {
		t.Error("Flush must emit the trailing partial line")
	}
	tr.Stop()
}

// Writing through a nil tracker must not panic — the wiring is conditional on
// --verbose, so a nil is reachable.
func TestTrackerWriterToleratesNoTracker(t *testing.T) {
	var w *TrackerWriter
	if n, err := w.Write([]byte("x\n")); err != nil || n != 2 {
		t.Errorf("nil writer should accept and discard, got n=%d err=%v", n, err)
	}
	w.Flush()
}
