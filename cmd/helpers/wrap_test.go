package helpers

import (
	"strings"
	"testing"
)

// Long values wrap under the value column so the label grid survives.
func TestWrapValueKeepsTheGrid(t *testing.T) {
	got := WrapValue("one two three four five six seven eight nine ten", 20, 12)
	for i, line := range strings.Split(got, "\n") {
		if i == 0 {
			continue
		}
		if !strings.HasPrefix(line, strings.Repeat(" ", 12)) {
			t.Errorf("continuation line %d is not indented to the value column: %q", i, line)
		}
	}
	if strings.Contains(got, "\n") == false {
		t.Error("a value longer than the width should wrap")
	}
}
