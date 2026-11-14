package batch

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Run naming v1 (design 0025 §8.3, decision D1), computed here INDEPENDENTLY
// of cmd/batch: the suite checks the names the CLI gives runs on a cluster
// against its own oracle, so a CLI that drifted from the vectors would be
// caught here even if its unit tests were edited to agree with it.
// testdata/naming-v1-vectors.json is the vector file every repository vendors.

// maxRestarts is the highest restart a run may have (`-r1` … `-r99`).
const maxRestarts = 99

// Canonical is the JSON object of the identifying parameters, keys sorted by
// code point (a bytewise sort of UTF-8 is exactly that), with D1's minimal
// escapes: `"`, `\`, the five short escapes and `\u00xx` for every other
// control character. `/`, `<`, `>`, `&`, U+007F, U+2028 and U+2029 stay raw.
func Canonical(identifying map[string]string) (string, error) {
	keys := make([]string, 0, len(identifying))
	for k := range identifying {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if !utf8.ValidString(k) || !utf8.ValidString(identifying[k]) {
			return "", fmt.Errorf("parameter %q is not valid text and is never hashed", k)
		}
		if i > 0 {
			b.WriteByte(',')
		}
		quote(&b, k)
		b.WriteByte(':')
		quote(&b, identifying[k])
	}
	b.WriteByte('}')
	return b.String(), nil
}

func quote(b *strings.Builder, s string) {
	short := map[rune]string{'"': `\"`, '\\': `\\`, '\b': `\b`, '\f': `\f`, '\n': `\n`, '\r': `\r`, '\t': `\t`}
	b.WriteByte('"')
	for _, r := range s {
		switch esc, ok := short[r]; {
		case ok:
			b.WriteString(esc)
		case r < 0x20:
			fmt.Fprintf(b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

// Hash8 is the first 8 lowercase hex characters of SHA-256(canonical).
func Hash8(identifying map[string]string) (string, error) {
	c, err := Canonical(identifying)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(c))
	return hex.EncodeToString(sum[:])[:8], nil
}

// RunName is `<batchjob>-<hash8>`: a manual run's name, and so its duplicate guard.
func RunName(batchJob string, identifying map[string]string) (string, error) {
	h, err := Hash8(identifying)
	if err != nil {
		return "", err
	}
	return batchJob + "-" + h, nil
}

// RestartName is `<base>-r<n>`, the nth restart of a run.
func RestartName(base string, n int) (string, error) {
	if n < 1 || n > maxRestarts {
		return "", fmt.Errorf("a run restarts at most %d times (asked for r%d)", maxRestarts, n)
	}
	return fmt.Sprintf("%s-r%d", base, n), nil
}
