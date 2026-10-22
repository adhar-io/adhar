package autoscaler

import (
	"os"
	"strings"
	"testing"
	"time"
)

// DigitalOcean, 2026-10-22: "Scaled cluster adhar workers to 5" every three
// minutes for hours, four nodes throughout, nothing said.
func TestAScaleUpThatNeverBecomesANodeIsReported(t *testing.T) {
	t0 := time.Date(2026, 10, 22, 10, 48, 0, 0, time.UTC)
	var p scaleUpProgress
	p.recordSuccess(5, t0)
	if msg, _ := p.check(4, t0.Add(5*time.Minute)); msg != "" {
		t.Fatalf("a join takes minutes; nothing to report yet, got %q", msg)
	}
	// Retries every cooldown keep the ORIGINAL clock.
	p.recordSuccess(5, t0.Add(3*time.Minute))
	p.recordSuccess(5, t0.Add(6*time.Minute))
	msg, first := p.check(4, t0.Add(16*time.Minute))
	if !first || !strings.Contains(msg, "scaling to 5 workers 16m0s ago") || !strings.Contains(msg, "still has 4") {
		t.Fatalf("want the stall reported once with its numbers, got first=%t %q", first, msg)
	}
	if msg, first := p.check(4, t0.Add(19*time.Minute)); msg == "" || first {
		t.Errorf("the stall stays in the reason but is evented only once; got first=%t %q", first, msg)
	}
	// The node arriving clears it.
	if msg, _ := p.check(5, t0.Add(20*time.Minute)); msg != "" {
		t.Errorf("reaching the target must clear the stall, got %q", msg)
	}
	if msg, _ := p.check(4, t0.Add(60*time.Minute)); msg != "" {
		t.Errorf("with no scale-up outstanding there is nothing to report, got %q", msg)
	}
}

func TestTheControllerRecordsAndReportsProgress(t *testing.T) {
	b, err := readOwnSource("controller.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"r.progress.recordSuccess(decision.Workers+decision.ScaleUpBy, now)",
		"r.progress.check(decision.Workers, now)",
		`"ScaleUpMadeNoProgress"`,
	} {
		if !strings.Contains(b, want) {
			t.Errorf("controller.go lacks %s", want)
		}
	}
}

func readOwnSource(name string) (string, error) {
	b, err := os.ReadFile(name)
	return string(b), err
}
