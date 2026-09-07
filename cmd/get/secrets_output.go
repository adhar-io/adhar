package get

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"

	"adhar-io/adhar/cmd/helpers"
)

// Output rendering, machine-readable formats, clipboard and the interactive
// picker for `adhar get secrets`. Credentials are never truncated: the table
// grows to what the values need and, when the terminal is narrower than that,
// each credential is printed as a card instead so nothing is cut off.

const (
	outputTable = "table"
	outputJSON  = "json"
	outputEnv   = "env"
)

// terminalWidth returns the current terminal width, or 0 when stdout is not a
// terminal (piped output: no wrapping decisions, no interactivity).
func terminalWidth() int {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return 0
	}
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 {
		return 0
	}
	return w
}

func padRight(s string, w int) string {
	if n := w - lipgloss.Width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// renderSecretsTable renders the credentials as a table sized to its content
// (no truncation), or as cards when the terminal cannot fit the table.
func renderSecretsTable(entries []SecretEntry, width int) string {
	svcW, userW, passW := lipgloss.Width("SERVICE"), lipgloss.Width("USERNAME"), lipgloss.Width("PASSWORD")
	for _, e := range entries {
		svcW = max(svcW, lipgloss.Width(e.Icon+" "+e.Service))
		userW = max(userW, lipgloss.Width(orDash(e.Username)))
		passW = max(passW, lipgloss.Width(orDash(e.Password)))
	}
	// 2 leading spaces + 2 gaps of 2 + border (4)
	totalW := svcW + userW + passW + 6
	if width > 0 && totalW+4 > width {
		return renderSecretsCards(entries)
	}

	var tb strings.Builder
	tb.WriteString(helpers.CreateHighlight("  " + padRight("SERVICE", svcW) + "  " + padRight("USERNAME", userW) + "  " + padRight("PASSWORD", passW)))
	tb.WriteString("\n" + strings.Repeat("─", totalW) + "\n")
	for _, e := range entries {
		tb.WriteString("  " + padRight(e.Icon+" "+e.Service, svcW) + "  " + padRight(orDash(e.Username), userW) + "  " + orDash(e.Password) + "\n")
	}
	return helpers.BorderStyle.Width(totalW + 2).Render(strings.TrimRight(tb.String(), "\n"))
}

// renderSecretsCards prints one block per credential; used for narrow
// terminals and as the non-tabular layout in the interactive picker.
func renderSecretsCards(entries []SecretEntry) string {
	var b strings.Builder
	for i, e := range entries {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(helpers.CreateHighlight(e.Icon+" "+e.Service) + "\n")
		b.WriteString("   username  " + orDash(e.Username) + "\n")
		b.WriteString("   password  " + orDash(e.Password) + "\n")
	}
	return b.String()
}

// secretEntryJSON is the stable machine-readable shape (`-o json`).
type secretEntryJSON struct {
	Service  string `json:"service"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func writeSecretsJSON(w io.Writer, entries []SecretEntry) error {
	out := make([]secretEntryJSON, 0, len(entries))
	for _, e := range entries {
		out = append(out, secretEntryJSON{Service: e.Service, Username: e.Username, Password: e.Password})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// envKey turns "Keycloak user1 (admin)" into KEYCLOAK_USER1_ADMIN.
func envKey(service string) string {
	var b strings.Builder
	lastUnderscore := true
	for _, r := range strings.ToUpper(service) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastUnderscore = false
		default:
			if !lastUnderscore {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

// writeSecretsEnv prints `export`-able lines (`-o env`): eval "$(adhar get secrets -o env)".
func writeSecretsEnv(w io.Writer, entries []SecretEntry) error {
	for _, e := range entries {
		k := envKey(e.Service)
		if _, err := fmt.Fprintf(w, "export %s_USERNAME=%q\nexport %s_PASSWORD=%q\n", k, e.Username, k, e.Password); err != nil {
			return err
		}
	}
	return nil
}

// findEntry matches a credential by service name (case-insensitive substring,
// e.g. "argocd", "gitea", "keycloak user1").
func findEntry(entries []SecretEntry, service string) (SecretEntry, bool) {
	needle := strings.ToLower(strings.TrimSpace(service))
	for _, e := range entries {
		if strings.ToLower(e.Service) == needle {
			return e, true
		}
	}
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Service), needle) {
			return e, true
		}
	}
	return SecretEntry{}, false
}

// copyToClipboard puts text on the system clipboard: the platform's clipboard
// tool when available, otherwise the OSC 52 escape sequence, which most modern
// terminals (iTerm2, kitty, WezTerm, Windows Terminal, tmux with
// allow-passthrough) honour — including over SSH. Returns how it was copied.
func copyToClipboard(text string) (string, error) {
	var candidates [][]string
	switch runtime.GOOS {
	case "darwin":
		candidates = [][]string{{"pbcopy"}}
	case "windows":
		candidates = [][]string{{"clip"}}
	default:
		candidates = [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard", "-in"}, {"xsel", "--clipboard", "--input"}}
	}
	for _, c := range candidates {
		if _, err := exec.LookPath(c[0]); err != nil {
			continue
		}
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			return c[0], nil
		}
	}
	// OSC 52 (wrapped for tmux so it reaches the outer terminal).
	seq := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07"
	if os.Getenv("TMUX") != "" {
		seq = "\x1bPtmux;\x1b" + seq + "\x1b\\"
	}
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return "", errors.New("no clipboard tool found (pbcopy/wl-copy/xclip/xsel) and no terminal for OSC 52")
	}
	defer tty.Close()
	if _, err := tty.WriteString(seq); err != nil {
		return "", err
	}
	return "terminal (OSC 52)", nil
}

// ---------------------------------------------------------------------------
// Interactive picker: ↑/↓ choose a credential, `u` copies its username,
// `p`/Enter copies its password, `q` leaves. Rendered inline (no alt screen)
// so the table above stays on screen.
// ---------------------------------------------------------------------------

type secretsPicker struct {
	entries []SecretEntry
	cursor  int
	status  string
	done    bool
}

func (m secretsPicker) Init() tea.Cmd { return nil }

func (m secretsPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "q", "esc", "ctrl+c":
		m.done = true
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.entries)-1 {
			m.cursor++
		}
	case "u":
		m.status = m.copy("username", m.entries[m.cursor].Username)
	case "p", "enter", " ":
		m.status = m.copy("password", m.entries[m.cursor].Password)
	}
	return m, nil
}

func (m secretsPicker) copy(what, value string) string {
	e := m.entries[m.cursor]
	if value == "" {
		return helpers.WarningStyle.Render(fmt.Sprintf("✗ %s has no %s", e.Service, what))
	}
	via, err := copyToClipboard(value)
	if err != nil {
		return helpers.ErrorStyle.Render("✗ copy failed: " + err.Error())
	}
	return helpers.SuccessStyle.Render(fmt.Sprintf("✓ %s for %s copied to clipboard (%s)", what, e.Service, via))
}

func (m secretsPicker) View() string {
	if m.done {
		return ""
	}
	var b strings.Builder
	for i, e := range m.entries {
		marker := "  "
		line := e.Icon + " " + e.Service + "  " + helpers.SubtitleStyle.Render(orDash(e.Username))
		if i == m.cursor {
			marker = helpers.HighlightStyle.Render("▶ ")
			line = helpers.HighlightStyle.Render(e.Icon+" "+e.Service) + "  " + orDash(e.Username)
		}
		b.WriteString(marker + line + "\n")
	}
	b.WriteString("\n" + helpers.SubtitleStyle.Render("↑/↓ select · u copy username · p/enter copy password · q quit") + "\n")
	if m.status != "" {
		b.WriteString(m.status + "\n")
	}
	return b.String()
}

// runSecretsPicker runs the inline picker; it returns immediately when stdin
// is not a terminal.
func runSecretsPicker(entries []SecretEntry) error {
	if len(entries) == 0 || !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil
	}
	_, err := tea.NewProgram(secretsPicker{entries: entries}).Run()
	return err
}
