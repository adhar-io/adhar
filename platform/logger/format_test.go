package logger

import (
	"encoding/json"
	"strings"
	"testing"
)

// Human-readable log lines share the CLI's left margin. Before this, every
// `[timestamp] INFO: …` line started at column 0 while the banner, the
// headings, the tables and the panels around it started at two — so the only
// thing out of line was the running commentary.
func TestTextLogLinesStartAtTheGutter(t *testing.T) {
	f := &DefaultFormatter{}
	got := f.Format(INFO, "Retrieving platform secrets...", nil)
	if !strings.HasPrefix(got, ConsoleGutter+"[") {
		t.Errorf("a text log line does not start at the gutter: %q", got)
	}
}

// A multi-line message must be indented on EVERY line; a wrapped diagnostic
// that starts in one column and continues in another is harder to read than one
// that is not indented at all.
func TestEveryLineOfAMultiLineMessageIsIndented(t *testing.T) {
	f := &DefaultFormatter{}
	got := f.Format(WARN, "first line\nsecond line", nil)
	for i, line := range strings.Split(got, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, ConsoleGutter) {
			t.Errorf("line %d is not indented: %q", i, line)
		}
	}
}

// Several call sites end their message with a newline to space the output.
// Padding that line would put invisible trailing whitespace into every
// transcript, diff and golden file.
func TestABlankLineIsLeftEmpty(t *testing.T) {
	f := &DefaultFormatter{}
	got := f.Format(INFO, "Found 5 credential(s)\n", nil)
	for i, line := range strings.Split(got, "\n") {
		if line != strings.TrimRight(line, " \t") {
			t.Errorf("line %d carries trailing whitespace: %q", i, line)
		}
	}
}

// JSON output must NOT be indented: it is parsed, and a leading space makes it
// a different document to anything reading the stream with `jq`.
func TestJSONLogLinesAreNotIndented(t *testing.T) {
	f := &JSONFormatter{}
	got := f.Format(ERROR, "boom", map[string]interface{}{"k": "v"})
	if strings.HasPrefix(got, " ") {
		t.Errorf("a JSON log line is indented: %q", got)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Errorf("JSON log line does not parse: %v (%s)", err, got)
	}
}

// The logger's gutter and the CLI banner's must agree. They are declared
// separately — `cmd/helpers` imports this package, so this one cannot import
// it back — and two constants that must match are two constants that drift.
func TestTheLogGutterMatchesTheBanner(t *testing.T) {
	// cmd/helpers.BannerGutter is 2; asserted by width rather than by import.
	if len(ConsoleGutter) != 2 || strings.TrimSpace(ConsoleGutter) != "" {
		t.Errorf("ConsoleGutter is %q; it must be exactly the banner's two spaces", ConsoleGutter)
	}
}
