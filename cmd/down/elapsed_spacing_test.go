package down

import (
	"regexp"
	"strings"
	"testing"
)

var ansiAndBorder = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|[│┃|╎┆┊]`)

// The elapsed time is separated from the spinner line by one blank line.
//
// A single "\n" only ENDS the spinner's line, so the two rendered flush
// together while the comment beside the code claimed there was a blank line
// between them. Reported live against `adhar down`:
//
//	⠼   Tearing down dev · deleting 'adhar' on gcp
//	Elapsed time: 17s
//
// Asserted on the RENDERED panel, because the separator has to survive
// lipgloss's border and padding — a check on the format string would pass
// while the box still drew them on adjacent lines.
func TestTheElapsedTimeIsSeparatedFromTheSpinnerLine(t *testing.T) {
	m := downModel{step: "Tearing down dev", elapsedTime: "17s"}
	lines := strings.Split(m.View(), "\n")

	at := -1
	for i, l := range lines {
		if strings.Contains(l, "Elapsed time:") {
			at = i
			break
		}
	}
	if at < 1 {
		t.Fatalf("no elapsed time line in the rendered panel:\n%s", m.View())
	}
	prev := strings.TrimSpace(ansiAndBorder.ReplaceAllString(lines[at-1], ""))
	if prev != "" {
		t.Errorf("the elapsed time sits directly under %q; it needs one blank line above it", prev)
	}
}
