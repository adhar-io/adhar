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
	"sort"
	"strings"
	"unicode/utf8"
)

// Run naming v1 (design 0025 §8.3, D1), the scheme the CLI (`cmd/batch/naming.go`),
// the Console (`batch-naming.ts`) and the kit's launcher compute independently.
// The observer is a fourth writer of run names — a `dependsOn` run is
// `<batchjob>-<hash8>` for its business date — and the name is the duplicate
// guard, so it must agree byte for byte: an observer that hashed differently from
// the CLI would start a second run for a date a person already ran.
// TestObserverRunNamesAgreeWithTheSharedNamingVectors reads the same vector file
// the CLI vendors (cmd/batch/testdata/naming-v1-vectors.json).
//
//	canonical = the JSON object of the identifying parameters, keys sorted by
//	            code point, every value a string, no whitespace, UTF-8
//	hash8     = the first 8 lowercase hex characters of SHA-256(canonical)
//	run       = <batchjob>-<hash8>
const namingVersion = "v1"

// canonicalParams is the canonical text of the identifying parameters. Go's
// bytewise string order over UTF-8 is code-point order, which is what D1 asks
// for; text that is not valid UTF-8 is never hashed.
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

// writeJSONString writes the minimal escapes of D1 — what ECMAScript's
// JSON.stringify writes, which encoding/json does not (it escapes U+2028/9).
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

// runName is `<batchjob>-<hash8>` for the identifying parameters, after the
// declared defaults are applied.
func runName(batchJob string, identifying map[string]string) (string, error) {
	h, err := paramsHash(identifying)
	if err != nil {
		return "", err
	}
	return batchJob + "-" + h, nil
}
