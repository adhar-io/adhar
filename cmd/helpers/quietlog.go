package helpers

import (
	"io"
	"regexp"
	"strings"
	"sync"
)

// QuietLog is where the structured logger writes while the live checklist owns
// the terminal. It keeps the screen to steps and progress: INFO and DEBUG lines
// ("● Reusing the adhar-letsencrypt-dns certificate …", "Wrote kubeconfig …")
// are HELD, while WARN, ERROR and FATAL still scroll above the checklist,
// because those are things the operator has to know now. If the run fails,
// Held returns what was kept back, which is exactly the context a failure
// needs. `--verbose` bypasses this and prints everything as it happens.
type QuietLog struct {
	show io.Writer
	mu   sync.Mutex
	held strings.Builder
}

// NewQuietLog returns a QuietLog that passes warnings and errors to show.
func NewQuietLog(show io.Writer) *QuietLog { return &QuietLog{show: show} }

// chatter matches one structured-logger write at INFO or DEBUG, in either of
// the logger's formats: "  [2026-10-21T17:59:12.586+08:00] INFO: …" or JSON
// with "level":"INFO".
var chatter = regexp.MustCompile(`^\s*\[[^\]]*\] (?:INFO|DEBUG):|"level"\s*:\s*"(?:INFO|DEBUG)"`)

// Write receives one logger message per call (the logger Println's each one,
// multi-line messages included), so a message is classified as a whole.
func (q *QuietLog) Write(p []byte) (int, error) {
	if chatter.Match(p) {
		q.hold(p)
		return len(p), nil
	}
	if q.show == nil {
		return len(p), nil
	}
	return q.show.Write(p)
}

// Hidden is a writer whose output is always held — for a stream that is detail
// in its entirety, such as each provider's standard-library `log` chatter.
func (q *QuietLog) Hidden() io.Writer { return hiddenWriter{q} }

// Held returns everything held back so far, in the order it was written.
func (q *QuietLog) Held() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.held.String()
}

func (q *QuietLog) hold(p []byte) {
	q.mu.Lock()
	q.held.Write(p)
	q.mu.Unlock()
}

type hiddenWriter struct{ q *QuietLog }

func (h hiddenWriter) Write(p []byte) (int, error) {
	h.q.hold(p)
	return len(p), nil
}

// RenderHeld formats what a QuietLog held back for printing under a failure:
// indented and dimmed, because it is evidence attached to the error, not a
// second stream of output competing with it. Empty input renders nothing.
func RenderHeld(held string) string {
	held = strings.TrimRight(held, "\n")
	if strings.TrimSpace(held) == "" {
		return ""
	}
	dim := SubtitleStyle.UnsetMarginLeft()
	var out strings.Builder
	out.WriteString("\n" + dim.Render("  detail") + "\n")
	for _, line := range strings.Split(held, "\n") {
		out.WriteString(dim.Render("    "+strings.TrimLeft(line, " ")) + "\n")
	}
	return out.String()
}
