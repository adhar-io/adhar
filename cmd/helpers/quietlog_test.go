package helpers

import (
	"bytes"
	"strings"
	"testing"

	"adhar-io/adhar/platform/logger"
)

// The line that prompted this (2026-10-21) scrolled above the `adhar up`
// checklist on every cloud run: INFO is held, warnings and errors are not.
func TestQuietLogHoldsInfoAndShowsWarnings(t *testing.T) {
	var shown bytes.Buffer
	q := NewQuietLog(&shown)

	l := logger.NewLogger(&logger.LoggerConfig{Level: logger.DEBUG, Output: "stderr", Format: "text"})
	l.SetOutput(q)
	l.Infof("● Reusing the %s certificate for *.%s issued earlier", "adhar-letsencrypt-dns", "cloud.adhar.io")
	l.Debug("dialing gitea")
	l.Warn("Could not reuse the cached edge certificate")
	l.Error("bootstrap failed")

	out := shown.String()
	if strings.Contains(out, "Reusing the") || strings.Contains(out, "dialing gitea") {
		t.Errorf("INFO/DEBUG reached the terminal:\n%s", out)
	}
	if !strings.Contains(out, "Could not reuse") || !strings.Contains(out, "bootstrap failed") {
		t.Errorf("a warning or error was hidden:\n%s", out)
	}
	held := q.Held()
	if !strings.Contains(held, "Reusing the") || !strings.Contains(held, "dialing gitea") {
		t.Errorf("held lines were lost; a failure would have no context:\n%s", held)
	}
	if strings.Contains(held, "bootstrap failed") {
		t.Error("an error shown on screen is also repeated in the held detail")
	}
}

func TestQuietLogUnderstandsTheJSONFormat(t *testing.T) {
	var shown bytes.Buffer
	q := NewQuietLog(&shown)
	_, _ = q.Write([]byte(`{"level":"INFO","message":"x"}` + "\n"))
	_, _ = q.Write([]byte(`{"level":"WARN","message":"y"}` + "\n"))
	if strings.Contains(shown.String(), `"INFO"`) || !strings.Contains(shown.String(), `"WARN"`) {
		t.Errorf("JSON levels misclassified: %q", shown.String())
	}
}

func TestProviderChatterIsAlwaysHeld(t *testing.T) {
	var shown bytes.Buffer
	q := NewQuietLog(&shown)
	_, _ = q.Hidden().Write([]byte("2026/10/21 17:48:18 Deleting 5 instance(s)\n"))
	if shown.Len() != 0 || !strings.Contains(q.Held(), "Deleting 5 instance(s)") {
		t.Errorf("provider log reached the terminal or was lost: shown=%q held=%q", shown.String(), q.Held())
	}
	if RenderHeld("") != "" || RenderHeld("\n\n") != "" {
		t.Error("an empty hold renders a lone heading under the error")
	}
}
