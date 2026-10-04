package up

import (
	"errors"
	"strings"
	"testing"
)

// The live checklist line must not reprint a whole cloud SDK dump.
//
// Three places render the same failure within one screen: this line (next to the
// checklist, the moment the environment stops), the closing panel (cause
// condensed, remedy attached) and the returned error (full, for logs). While
// this one printed %v of the error, an operator saw the same 40-line azidentity
// body and the same multi-line remedy three times — observed live on the Azure
// auth failure of 2026-10-04.
//
// Its job is to say which environment stopped and why, in one line.
func TestLiveFailureLineIsASingleLine(t *testing.T) {
	err := errors.New("authentication failed for azure provider: failed to authenticate with Azure: " +
		"ClientSecretCredential authentication failed.\n" +
		"POST https://login.microsoftonline.com/c95747c3/oauth2/v2.0/token\n" +
		strings.Repeat("-", 80) + "\n" +
		"RESPONSE 401: 401 Unauthorized\n" +
		`{"error":"invalid_client","error_description":"AADSTS7000215: ..."}`)

	got := liveFailureLine(err)
	if strings.Contains(got, "\n") {
		t.Errorf("the live line must be one line, got:\n%s", got)
	}
	// The first line is the part that names what failed.
	if !strings.Contains(got, "authentication failed for azure provider") {
		t.Errorf("the live line must still say what failed, got: %s", got)
	}
	// It must be visibly incomplete, so nobody reads it as the whole error.
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a truncated line must be marked as truncated, got: %s", got)
	}
	if strings.Contains(got, "RESPONSE 401") {
		t.Errorf("HTTP framing belongs in the panel and the log, not the live line, got: %s", got)
	}
}

// A single-line error is already what we want and must pass through unchanged —
// no ellipsis on something that was never cut.
func TestLiveFailureLineLeavesAShortErrorAlone(t *testing.T) {
	const msg = "quota exceeded in centralindia"
	if got := liveFailureLine(errors.New(msg)); got != msg {
		t.Errorf("a one-line error must be unchanged: got %q, want %q", got, msg)
	}
}

func TestLiveFailureLineHandlesNil(t *testing.T) {
	if got := liveFailureLine(nil); got != "" {
		t.Errorf("nil must render as empty, got %q", got)
	}
}
