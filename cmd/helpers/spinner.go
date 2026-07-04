/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package helpers

// spinner.go is a small, dependency-light activity spinner for `adhar up`. It
// animates a single status line with the brand-gradient braille frames and a
// live elapsed timer, then resolves to a ✓/✗ line. On a non-interactive writer
// (pipes, CI, NO_COLOR) it degrades to plain "• …" / "✓ …" lines with no
// escape codes, so logs stay clean.

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

var (
	spinFrameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(brandPurple.hex())).Bold(true)
	spinLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(brandBlue.hex()))
	spinTimeStyle  = lipgloss.NewStyle().Foreground(taglineGray)
	spinOKStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#10B981")).Bold(true)
	spinFailStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#F85149")).Bold(true)
)

// Spinner animates one status line. It is safe to update the label concurrently
// while it animates (SetLabel), and it is a no-op-safe if the writer is not a TTY.
type Spinner struct {
	w      io.Writer
	isTTY  bool
	mu     sync.Mutex
	label  string
	start  time.Time
	frame  int
	active bool
	stopCh chan struct{}
	doneCh chan struct{}
}

// NewSpinner returns a spinner writing to w (typically os.Stderr).
func NewSpinner(w io.Writer) *Spinner {
	tty := false
	if f, ok := w.(*os.File); ok {
		tty = term.IsTerminal(int(f.Fd()))
	}
	return &Spinner{w: w, isTTY: tty}
}

// Start begins animating with the given label. On a non-TTY it prints a single
// plain "• label" line and does not animate.
func (s *Spinner) Start(label string) {
	s.mu.Lock()
	s.label = label
	s.start = time.Now()
	s.active = true
	s.mu.Unlock()

	if !s.isTTY {
		fmt.Fprintf(s.w, "   • %s\n", label)
		return
	}
	s.stopCh = make(chan struct{})
	s.doneCh = make(chan struct{})
	go s.run()
}

// SetLabel updates the animated line's text in place.
func (s *Spinner) SetLabel(label string) {
	s.mu.Lock()
	s.label = label
	s.mu.Unlock()
}

func (s *Spinner) run() {
	defer close(s.doneCh)
	// Guard the render loop so a terminal quirk never takes down provisioning.
	defer func() { _ = recover() }()
	t := time.NewTicker(90 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-t.C:
			s.render()
		}
	}
}

func (s *Spinner) render() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return
	}
	frame := spinFrameStyle.Render(spinnerFrames[s.frame%len(spinnerFrames)])
	s.frame++
	el := spinTimeStyle.Render(fmtElapsed(time.Since(s.start)))
	fmt.Fprintf(s.w, "\r\x1b[2K   %s %s  %s", frame, spinLabelStyle.Render(s.label), el)
}

// Success stops the animation and resolves the line to a green ✓ with elapsed time.
func (s *Spinner) Success(msg string) { s.finish(spinOKStyle.Render("✓"), msg) }

// Fail stops the animation and resolves the line to a red ✗.
func (s *Spinner) Fail(msg string) { s.finish(spinFailStyle.Render("✗"), msg) }

func (s *Spinner) finish(icon, msg string) {
	s.mu.Lock()
	s.active = false
	el := fmtElapsed(time.Since(s.start))
	s.mu.Unlock()

	if s.isTTY && s.stopCh != nil {
		close(s.stopCh)
		<-s.doneCh
	}
	line := fmt.Sprintf("   %s %s  %s\n", icon, spinLabelStyle.Render(msg), spinTimeStyle.Render(el))
	if s.isTTY {
		fmt.Fprintf(s.w, "\r\x1b[2K%s", line)
	} else {
		fmt.Fprint(s.w, line)
	}
}

func fmtElapsed(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}
