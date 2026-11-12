/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Run naming v1 — the contract the CLI, the Console (`batch-naming.ts`) and the
// kit's launcher all compute independently (design 0025 §8.3, decision D1).
// They must agree byte for byte: a run's NAME is its duplicate guard, so a CLI
// that hashed one value differently from the Console would start a second run
// for a business date that already ran.
//
//	canonical = the JSON object of the identifying parameters, keys sorted by
//	            code point, every value a string, no whitespace, UTF-8
//	hash8     = the first 8 lowercase hex characters of SHA-256(canonical)
//	run       = <batchjob>-<hash8>
//	restart   = <batchjob>-<hash8>-r<n>   (n = 1..99)
//
// The encoder is hand-rolled rather than encoding/json: Go escapes U+2028 and
// U+2029 even with SetEscapeHTML(false), and the normative escaping is what
// ECMAScript's JSON.stringify writes. testdata/naming-v1-vectors.json is the
// same vector file every repository vendors.

// namingVersion is the scheme this client implements (`naming` in the run template).
const namingVersion = "v1"

// maxRestarts is the highest restart a run may have.
const maxRestarts = 99

// canonicalParams is the canonical text of the identifying parameters.
//
// Go compares strings bytewise, and UTF-8 preserves code-point order under a
// bytewise comparison, so sort.Strings IS the code-point order D1 asks for
// (JavaScript's default order is by UTF-16 unit, which is why the Console
// carries its own comparator). A string that is not valid UTF-8 is the Go
// shape of a lone surrogate: not text, and never hashed.
func canonicalParams(params map[string]string) (string, error) {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		v := params[k]
		if !utf8.ValidString(k) || !utf8.ValidString(v) {
			return "", fmt.Errorf("parameter %q is not valid text", k)
		}
		if i > 0 {
			b.WriteByte(',')
		}
		writeJSONString(&b, k)
		b.WriteByte(':')
		writeJSONString(&b, v)
	}
	b.WriteByte('}')
	return b.String(), nil
}

// writeJSONString writes s with the minimal escapes of D1: `"` and `\`, the
// five short escapes, `\u00xx` for every other control character, and nothing
// else — `/`, `<`, `>`, `&`, U+007F, U+2028, U+2029 and all non-ASCII text go
// out as raw UTF-8.
func writeJSONString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

// paramsHash is the first 8 hex characters of SHA-256 over the canonical text.
func paramsHash(identifying map[string]string) (string, error) {
	canonical, err := canonicalParams(identifying)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])[:8], nil
}

// runName is `<batchjob>-<hash8>` for the identifying parameters, AFTER the
// declared defaults are applied.
func runName(batchJob string, identifying map[string]string) (string, error) {
	h, err := paramsHash(identifying)
	if err != nil {
		return "", err
	}
	return batchJob + "-" + h, nil
}

// restartName is `<base>-r<n>`, the nth restart of a run.
func restartName(base string, n int) (string, error) {
	if n < 1 || n > maxRestarts {
		return "", fmt.Errorf("a run restarts at most %d times (asked for r%d)", maxRestarts, n)
	}
	return fmt.Sprintf("%s-r%d", base, n), nil
}

var restartTail = regexp.MustCompile(`^[1-9][0-9]?$`)

// restartIndex places name in the restart chain of base: 0 for base itself,
// n for `<base>-r<n>`, and false for a name that is not in the chain at all.
func restartIndex(base, name string) (int, bool) {
	if name == base {
		return 0, true
	}
	tail := strings.TrimPrefix(name, base+"-r")
	if tail == name || !restartTail.MatchString(tail) {
		return 0, false
	}
	n, _ := strconv.Atoi(tail)
	return n, true
}

// nextRestartName is the next free restart of base, given every run name that
// exists. Holes are not refilled: `r2` existing with `r1` gone (a TTL clean-up)
// is still followed by `r3`, so the numbers keep their order. False when all
// 99 are used.
func nextRestartName(base string, existing []string) (string, bool) {
	highest := 0
	for _, name := range existing {
		if n, ok := restartIndex(base, name); ok && n > highest {
			highest = n
		}
	}
	if highest >= maxRestarts {
		return "", false
	}
	name, _ := restartName(base, highest+1)
	return name, true
}
