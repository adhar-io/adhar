package up

import (
	"strings"
	"testing"
)

// A MULTI-LINE cloud error must stay inside the frame.
//
// TestSummaryWrapsALongFailureInsideTheFrame already covers a long error, but it
// passes a single line — and length was never the thing that broke the panel.
// WrapValue cannot rescue a value that already contains newlines: each one
// restarts the line at the frame's own indent instead of the value column. Nor
// can it break a token wider than the column, such as a token endpoint URL or an
// 80-dash divider. A live Azure auth failure on 2026-10-04 had all three and
// printed this:
//
//	│  9889-34c2ffd38528/oauth2/v2.0/token                                     │
//	│                  ------------------------------------------------------  │
//	│  --------------------------                                              │
func TestSummaryKeepsAMultiLineCloudErrorInsideTheFrame(t *testing.T) {
	azure := "prod — authentication failed for azure provider: failed to authenticate with Azure: " +
		"ClientSecretCredential authentication failed.\n" +
		"POST https://login.microsoftonline.com/c95747c3-18ed-485f-9889-34c2ffd38528/oauth2/v2.0/token\n" +
		strings.Repeat("-", 80) + "\n" +
		"RESPONSE 401: 401 Unauthorized\n" +
		strings.Repeat("-", 80) + "\n" +
		`{"error":"invalid_client","error_description":"AADSTS7000215: Invalid client secret provided. ` +
		`Ensure the secret being sent in the request is the client secret value, not the client secret ID, ` +
		`for a secret added to app '66483720-42ae-4f05-82a6-e3d75db21412'. Trace ID: 1ef53886"}` + "\n" +
		strings.Repeat("-", 80) + "\n" +
		"To troubleshoot, visit https://aka.ms/azsdk/go/identity/troubleshoot#client-secret"

	out := renderProvisionSummary(0, 1, []string{azure})
	for _, line := range strings.Split(out, "\n") {
		if n := len([]rune(line)); n > summaryPanelWidth+4 {
			t.Errorf("line escapes the frame (%d cells): %q", n, line)
		}
	}
	// The environment that failed is the one thing that must never be cut.
	if !strings.Contains(out, "prod") {
		t.Error("the failing environment must be named")
	}
	if strings.Contains(out, strings.Repeat("-", 20)) {
		t.Error("HTTP framing dividers carry no information and must be dropped")
	}
}

// The remedy is the actionable half, so it survives while the cause is shortened.
//
// ExplainAccessError appends its advice AFTER the raw error, so shortening by
// truncation alone would discard precisely the sentence worth reading.
func TestSummaryKeepsTheRemedyAndShortensTheCause(t *testing.T) {
	cause := "prod — authentication failed for azure provider: " + strings.Repeat("noise ", 60)
	remedy := "The value in AZURE_CLIENT_SECRET is the secret's ID, not the secret's VALUE. " +
		"Issue a fresh value: az ad sp credential reset --id 66483720 --query password -o tsv"
	out := renderProvisionSummary(0, 1, []string{cause + remedyMarker + remedy})

	// The panel wraps and is framed, so compare against the de-framed, de-wrapped
	// text: the border glyphs would otherwise land between the words.
	flat := strings.Join(strings.Fields(strings.Map(dropBoxDrawing, out)), " ")
	for _, want := range []string{"az ad sp credential reset", "not the secret's VALUE"} {
		if !strings.Contains(flat, want) {
			t.Errorf("the remedy must survive; missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "…") {
		t.Error("an over-long cause should be visibly elided, so nobody reads it as the whole error")
	}
	for _, line := range strings.Split(out, "\n") {
		if n := len([]rune(line)); n > summaryPanelWidth+4 {
			t.Errorf("line escapes the frame (%d cells): %q", n, line)
		}
	}
}

// A short, single-line failure must pass through untouched — the condensing is
// for SDK dumps, not for the common case.
func TestSummaryLeavesAShortFailureAlone(t *testing.T) {
	const f = "staging — quota exceeded"
	if got := condenseFailure(f); got != f {
		t.Errorf("a short failure must not be altered: got %q, want %q", got, f)
	}
}

// dropBoxDrawing removes the panel's frame so wrapped prose can be compared.
func dropBoxDrawing(r rune) rune {
	if r >= 0x2500 && r <= 0x257F {
		return ' '
	}
	return r
}
