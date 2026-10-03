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

// A cloud bring-up closes with the SAME panel as a local one, via the same
// renderer, so the two cannot drift apart. The cloud path used to print a row of
// '#' characters, a bulleted list restating the checklist, and four numbered steps
// with URLs run together — for the more expensive cluster of the two.
func TestCloudReadyPanelMatchesTheLocalShape(t *testing.T) {
	// A SERVING platform: the gate application is Synced + Healthy.
	out := renderCloudReadyPanel("platform.adhar.io", "adhar",
		&convergenceSnapshot{Known: true, GateReady: true, GateApp: "adhar-console", Healthy: 75, Total: 75})

	// Same header the local panel uses.
	if !strings.Contains(out, "Platform ready") {
		t.Errorf("cloud panel is missing the shared header:\n%s", out)
	}
	// Every app, one per row, on the configured host.
	for _, app := range cloudPlatformApps {
		want := "https://" + strings.ToLower(app) + ".platform.adhar.io"
		if !strings.Contains(out, want) {
			t.Errorf("cloud panel is missing %s", want)
		}
	}
	// No port suffix: a cloud platform is behind the Gateway's load balancer on 443,
	// unlike the local Kind flow's high port.
	if strings.Contains(out, ".platform.adhar.io:") {
		t.Errorf("cloud URLs must not carry a port:\n%s", out)
	}
	// The kube-context hint is cloud-specific, and must not stutter.
	if !strings.Contains(out, "use-context adhar") {
		t.Errorf("cloud panel should name the kube-context:\n%s", out)
	}
	if strings.Contains(out, "adhar-adhar") {
		t.Errorf("the context name stutters:\n%s", out)
	}
	// The replaced banner must not come back.
	if strings.Contains(out, "####") {
		t.Errorf("the '#' banner is gone for good:\n%s", out)
	}
}


// The panel must NOT claim readiness when the console is not serving.
//
// This is the regression that shipped: on the first GCP bring-up `adhar up`
// printed the green "Platform ready" box with seven URLs while Keycloak sat in a
// startup-probe restart loop, so the console Deployment had never been created
// and every one of those URLs refused connections. The counts and the log line
// above the box said otherwise; nobody reads those once a box of links appears.
func TestCloudPanelDoesNotClaimReadyWhileTheConsoleIsDown(t *testing.T) {
	notServing := []struct {
		name string
		conv *convergenceSnapshot
	}{
		{"gate app degraded", &convergenceSnapshot{Known: true, GateReady: false, GateApp: "adhar-console", Healthy: 39, Total: 75}},
		// An unreadable status is NOT a working console. Defaulting the other way
		// is how a tool ends up asserting readiness it never established.
		{"status unreadable", &convergenceSnapshot{}},
		{"no snapshot at all", nil},
	}
	for _, tc := range notServing {
		t.Run(tc.name, func(t *testing.T) {
			out := renderCloudReadyPanel("platform.adhar.io", "adhar", tc.conv)
			if strings.Contains(out, "Platform ready") {
				t.Errorf("panel claims readiness with a console that is not serving:\n%s", out)
			}
			if !strings.Contains(out, "Platform converging") {
				t.Errorf("panel does not say the platform is still converging:\n%s", out)
			}
			// The URLs still belong here — they are correct, and the operator wants
			// them shortly. What must not survive is the claim that they work.
			if !strings.Contains(out, "https://console.platform.adhar.io") {
				t.Errorf("panel dropped the URLs it should still hand over:\n%s", out)
			}
		})
	}
}

// The headline has to be checkable, not reassuring: it names the gate and the
// counts so the reader can confirm them with `adhar get status`.
func TestConvergingHeadlineNamesTheGateAndTheCounts(t *testing.T) {
	got := convergingHeadline(&convergenceSnapshot{Known: true, GateApp: "adhar-console", Healthy: 39, Total: 75})
	for _, want := range []string{"adhar-console", "39/75", "NOT serving"} {
		if !strings.Contains(got, want) {
			t.Errorf("headline %q is missing %q", got, want)
		}
	}
	// An unknown state must not be reported as a specific count.
	if h := convergingHeadline(&convergenceSnapshot{}); strings.Contains(h, "0/0") {
		t.Errorf("unreadable status reported as a count: %q", h)
	}
}
