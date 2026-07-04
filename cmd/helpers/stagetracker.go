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

// stagetracker.go renders `adhar up` provisioning as a single, live checklist of
// the real stages (Kind → CRDs → Networking → Cilium/Gateway → ArgoCD → Gitea →
// Crossplane → GitOps). The whole block redraws in place: each stage moves from
// ○ pending → ⠋ active (animated) → ✓ done (with elapsed). On a non-interactive
// writer it degrades to one plain line per stage transition so logs stay clean.

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

type stageState int

const (
	stagePending stageState = iota
	stageActive
	stageDone
	stageFailed
)

type trackedStage struct {
	label  string
	detail string
	state  stageState
	start  time.Time
	end    time.Time
}

// StageTracker renders and animates the provisioning checklist.
type StageTracker struct {
	w         io.Writer
	isTTY     bool
	title     string
	overall   time.Time
	stages    []*trackedStage
	mu        sync.Mutex
	frame     int
	running   bool
	stopCh    chan struct{}
	doneCh    chan struct{}
	lastLines int
}

var (
	stDoneStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#10B981")).Bold(true)
	stFailStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#F85149")).Bold(true)
	stActiveStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color(brandPurple.hex())).Bold(true)
	stPendGlyph    = lipgloss.NewStyle().Foreground(taglineGray)
	stLabelDone    = lipgloss.NewStyle().Foreground(lipgloss.Color(brandBlue.hex()))
	stLabelActive  = lipgloss.NewStyle().Foreground(lipgloss.Color(brandBlue.hex())).Bold(true)
	stLabelPending = lipgloss.NewStyle().Foreground(taglineGray)
	stDetailStyle  = lipgloss.NewStyle().Foreground(taglineGray)
	stTitleStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color(brandBlue.hex())).Bold(true)
)

// Stage is a [label, detail] pair used to build a StageTracker.
type StageDef struct{ Label, Detail string }

// NewStageTracker builds a tracker for the given stages (writing to w, e.g.
// os.Stderr). When animate is false (e.g. --verbose, where controller logs
// stream) the tracker degrades to one plain line per transition even on a TTY,
// so the in-place redraw never fights with the log stream.
func NewStageTracker(w io.Writer, title string, defs []StageDef, animate bool) *StageTracker {
	tty := false
	if f, ok := w.(*os.File); ok && animate {
		tty = term.IsTerminal(int(f.Fd()))
	}
	st := make([]*trackedStage, len(defs))
	for i, d := range defs {
		st[i] = &trackedStage{label: d.Label, detail: d.Detail, state: stagePending}
	}
	return &StageTracker{w: w, isTTY: tty, title: title, overall: time.Now(), stages: st}
}

// Start prints the initial checklist and, on a TTY, begins the redraw loop.
func (t *StageTracker) Start() {
	t.mu.Lock()
	t.running = true
	t.mu.Unlock()
	if !t.isTTY {
		fmt.Fprintf(t.w, "\n  %s\n", stTitleStyle.Render(t.title))
		return
	}
	t.stopCh = make(chan struct{})
	t.doneCh = make(chan struct{})
	fmt.Fprintln(t.w) // blank line before the block
	t.render(true)
	go t.loop()
}

// Activate marks stage i as in-progress (and finalises any earlier active one).
func (t *StageTracker) Activate(i int) {
	t.mu.Lock()
	if i >= 0 && i < len(t.stages) && t.stages[i].state == stagePending {
		t.stages[i].state = stageActive
		t.stages[i].start = time.Now()
		if !t.isTTY {
			fmt.Fprintf(t.w, "  %s %s\n", stActiveStyle.Render("•"), t.stages[i].label)
		}
	}
	t.mu.Unlock()
}

// Done marks stage i complete.
func (t *StageTracker) Done(i int) { t.setFinal(i, stageDone) }

// Fail marks stage i failed.
func (t *StageTracker) Fail(i int) { t.setFinal(i, stageFailed) }

func (t *StageTracker) setFinal(i int, s stageState) {
	t.mu.Lock()
	if i >= 0 && i < len(t.stages) && t.stages[i].state != stageDone {
		if t.stages[i].start.IsZero() {
			t.stages[i].start = time.Now()
		}
		t.stages[i].state = s
		t.stages[i].end = time.Now()
		if !t.isTTY {
			icon := stDoneStyle.Render("✓")
			if s == stageFailed {
				icon = stFailStyle.Render("✗")
			}
			fmt.Fprintf(t.w, "  %s %s  %s\n", icon, t.stages[i].label,
				stDetailStyle.Render(fmtElapsed(t.stages[i].end.Sub(t.stages[i].start))))
		}
	}
	t.mu.Unlock()
}

// Stop finalises the render and stops the redraw loop.
func (t *StageTracker) Stop() {
	t.mu.Lock()
	t.running = false
	t.mu.Unlock()
	if t.isTTY && t.stopCh != nil {
		close(t.stopCh)
		<-t.doneCh
	}
	t.render(false)
	fmt.Fprintln(t.w) // blank line after the block
}

func (t *StageTracker) loop() {
	defer close(t.doneCh)
	defer func() { _ = recover() }()
	tk := time.NewTicker(100 * time.Millisecond)
	defer tk.Stop()
	for {
		select {
		case <-t.stopCh:
			return
		case <-tk.C:
			t.render(false)
		}
	}
}

func (t *StageTracker) render(first bool) {
	if !t.isTTY {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	var b strings.Builder
	// Title with overall elapsed.
	b.WriteString(fmt.Sprintf("  %s  %s\n", stTitleStyle.Render(t.title),
		stDetailStyle.Render(fmtElapsed(time.Since(t.overall)))))
	for _, s := range t.stages {
		var glyph, label, extra string
		switch s.state {
		case stageDone:
			glyph = stDoneStyle.Render("✓")
			label = stLabelDone.Render(s.label)
			extra = stDetailStyle.Render(fmtElapsed(s.end.Sub(s.start)))
		case stageFailed:
			glyph = stFailStyle.Render("✗")
			label = stLabelActive.Render(s.label)
		case stageActive:
			glyph = stActiveStyle.Render(spinnerFrames[t.frame%len(spinnerFrames)])
			label = stLabelActive.Render(s.label)
			extra = stDetailStyle.Render(s.detail)
		default:
			glyph = stPendGlyph.Render("○")
			label = stLabelPending.Render(s.label)
		}
		line := fmt.Sprintf("  %s  %s", glyph, label)
		if extra != "" {
			line += "  " + extra
		}
		b.WriteString(line + "\n")
	}
	t.frame++

	out := b.String()
	n := strings.Count(out, "\n")
	if !first && t.lastLines > 0 {
		// Move up to the top of the previous block and clear to end of screen.
		fmt.Fprintf(t.w, "\x1b[%dA\r\x1b[J", t.lastLines)
	}
	fmt.Fprint(t.w, out)
	t.lastLines = n
}
