package helpers

import "strings"

// WrapValue soft-wraps a value into a label/value grid, indenting continuation
// lines under the value column so the grid survives a long sentence.
//
// Shared because two commands lay out the same kind of panel — the teardown
// confirmation and the provisioning summary — and a second copy is how the two
// drift apart.
func WrapValue(s string, width, indent int) string {
	if width <= 0 {
		return s
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return s
	}
	var b strings.Builder
	line := 0
	for i, w := range words {
		switch {
		case i == 0:
			b.WriteString(w)
			line = len(w)
		case line+1+len(w) <= width:
			b.WriteString(" " + w)
			line += 1 + len(w)
		default:
			b.WriteString("\n" + strings.Repeat(" ", indent) + w)
			line = len(w)
		}
	}
	return b.String()
}
