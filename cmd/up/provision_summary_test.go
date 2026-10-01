package up

import (
	"strings"
	"testing"
)

// The summary must never contradict its own result. It announced
// "● Platform Provisioning Complete!" whatever happened — printed directly above
// "Error: failed to provision 1 out of 1 environments" — and a summary that
// disagrees with the exit code teaches people not to read it.
func TestSummaryHeadlineFollowsTheOutcome(t *testing.T) {
	tests := []struct {
		name             string
		succeeded, total int
		wantContains     string
		wantNotContains  string
	}{
		// Whole phrases, not words: "incomplete" contains "complete", so a
		// substring check on the word alone fails the partial case for the wrong
		// reason.
		{"total failure", 0, 1, "provisioning failed", "provisioning complete"},
		{"partial", 1, 3, "provisioning incomplete", "provisioning complete"},
		{"all good", 2, 2, "provisioning complete", "provisioning failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.ToLower(renderProvisionSummary(tc.succeeded, tc.total, nil))
			if !strings.Contains(got, tc.wantContains) {
				t.Errorf("summary should say %q:\n%s", tc.wantContains, got)
			}
			if strings.Contains(got, tc.wantNotContains) {
				t.Errorf("summary must not say %q for %d/%d:\n%s", tc.wantNotContains, tc.succeeded, tc.total, got)
			}
		})
	}
}

// Nothing to provision is not a result worth framing.
func TestSummaryIsEmptyWhenThereIsNothingToReport(t *testing.T) {
	if got := renderProvisionSummary(0, 0, nil); got != "" {
		t.Errorf("no environments should render nothing, got:\n%s", got)
	}
}

// The count and the failures share one label column. They were formatted
// independently and their values started two columns apart.
func TestSummaryRowsShareOneLabelColumn(t *testing.T) {
	out := renderProvisionSummary(1, 3, []string{"staging — quota exceeded"})
	var envCol, failCol int
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, "0 of 3"); i >= 0 {
			envCol = i
		}
		if i := strings.Index(line, "1 of 3"); i >= 0 {
			envCol = i
		}
		if i := strings.Index(line, "staging"); i >= 0 {
			failCol = i
		}
	}
	if envCol == 0 || failCol == 0 {
		t.Fatalf("could not locate both value columns in:\n%s", out)
	}
	if envCol != failCol {
		t.Errorf("value columns differ: count at %d, failure at %d\n%s", envCol, failCol, out)
	}
}

// A long cloud error has to stay inside the frame, wrapped under its column.
func TestSummaryWrapsALongFailureInsideTheFrame(t *testing.T) {
	long := `dev — failed to create droplet: POST https://api.digitalocean.com/v2/droplets: 422 (request "a3dee5f9") 59723548 are invalid key identifiers for Droplet creation.`
	out := renderProvisionSummary(0, 1, []string{long})
	for _, line := range strings.Split(out, "\n") {
		if len([]rune(line)) > summaryPanelWidth+4 {
			t.Errorf("line escapes the frame (%d cells): %q", len([]rune(line)), line)
		}
	}
	if !strings.Contains(out, "invalid key identifiers") {
		t.Error("the actual cause must survive wrapping")
	}
}
