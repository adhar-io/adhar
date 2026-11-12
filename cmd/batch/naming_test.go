package batch

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

type namingVector struct {
	ID               string            `json:"id"`
	Params           map[string]string `json:"params"`
	Canonical        string            `json:"canonical"`
	CanonicalUTF8Hex string            `json:"canonicalUtf8Hex"`
	Hash8            string            `json:"hash8"`
}

func loadVectors(t *testing.T) []namingVector {
	t.Helper()
	raw, err := os.ReadFile("testdata/naming-v1-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Version string         `json:"version"`
		Vectors []namingVector `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if file.Version != namingVersion {
		t.Fatalf("the vendored vectors are naming %s; this client implements %s", file.Version, namingVersion)
	}
	if len(file.Vectors) < 7 {
		t.Fatalf("expected the seven D1 vectors, found %d", len(file.Vectors))
	}
	return file.Vectors
}

// The CLI, the Console and the kit each compute a run's name on their own, and
// the name is the duplicate guard. The vector file is the one the Console
// vendors; every byte of the canonical text and the hash must agree.
func TestRunNamesAgreeWithTheSharedNamingVectors(t *testing.T) {
	for _, v := range loadVectors(t) {
		t.Run(v.ID, func(t *testing.T) {
			canonical, err := canonicalParams(v.Params)
			if err != nil {
				t.Fatal(err)
			}
			if canonical != v.Canonical {
				t.Errorf("canonical = %q, want %q", canonical, v.Canonical)
			}
			if got := hex.EncodeToString([]byte(canonical)); got != v.CanonicalUTF8Hex {
				t.Errorf("canonical bytes = %s, want %s", got, v.CanonicalUTF8Hex)
			}
			h, err := paramsHash(v.Params)
			if err != nil {
				t.Fatal(err)
			}
			if h != v.Hash8 {
				t.Errorf("hash8 = %s, want %s", h, v.Hash8)
			}
		})
	}
	name, err := runName("importer", map[string]string{"businessDate": "2026-11-03"})
	if err != nil || name != "importer-09932cb3" {
		t.Fatalf("runName = %q, %v; §8.3 says importer-09932cb3", name, err)
	}
}

// A value that is not valid text (Go's shape of a lone surrogate) is an
// invalid parameter and is never hashed, rather than hashed to something the
// Console would never produce.
func TestInvalidTextIsNeverHashed(t *testing.T) {
	if _, err := runName("importer", map[string]string{"note": "\xff"}); err == nil {
		t.Fatal("an invalid UTF-8 value must be refused, not hashed")
	}
	if _, err := runName("importer", map[string]string{"\xed\xa0\x80": "x"}); err == nil {
		t.Fatal("an invalid UTF-8 key must be refused, not hashed")
	}
}

func TestRestartNamesKeepTheirOrder(t *testing.T) {
	base := "importer-09932cb3"
	if got, ok := nextRestartName(base, nil); !ok || got != base+"-r1" {
		t.Fatalf("first restart = %q %v", got, ok)
	}
	// r1 was cleaned up by its TTL; r2 is still followed by r3, never by a refilled r1.
	if got, ok := nextRestartName(base, []string{base, base + "-r2", "other-09932cb3-r7"}); !ok || got != base+"-r3" {
		t.Fatalf("after r2 = %q %v", got, ok)
	}
	if got, ok := nextRestartName(base, []string{base + "-r99"}); ok {
		t.Fatalf("r99 must be the last: got %q", got)
	}
	if _, err := restartName(base, 100); err == nil {
		t.Fatal("r100 must be refused")
	}
	if n, ok := restartIndex(base, base+"-r12"); !ok || n != 12 {
		t.Fatalf("restartIndex r12 = %d %v", n, ok)
	}
	for _, bad := range []string{base + "-r0", base + "-r100", base + "-rx", base + "x", "importer-09932cb4"} {
		if _, ok := restartIndex(base, bad); ok {
			t.Errorf("%q must not be in the chain of %s", bad, base)
		}
	}
}
