package batch

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type namingVector struct {
	ID               string            `json:"id"`
	Params           map[string]string `json:"params"`
	Canonical        string            `json:"canonical"`
	CanonicalUtf8Hex string            `json:"canonicalUtf8Hex"`
	Hash8            string            `json:"hash8"`
}

func loadNamingVectors(t *testing.T) []namingVector {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "naming-v1-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version string         `json:"version"`
		Vectors []namingVector `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != "v1" || len(doc.Vectors) == 0 {
		t.Fatalf("vector file: version %q with %d vectors, want v1 with some", doc.Version, len(doc.Vectors))
	}
	return doc.Vectors
}

// The suite names runs with its own encoder; it must reproduce every shared
// vector to the byte, the UTF-8 of the canonical text included.
func TestBatchRunNamingMatchesTheSharedV1Vectors(t *testing.T) {
	for _, v := range loadNamingVectors(t) {
		t.Run(v.ID, func(t *testing.T) {
			canonical, err := Canonical(v.Params)
			if err != nil {
				t.Fatal(err)
			}
			if canonical != v.Canonical {
				t.Errorf("canonical\n got %q\nwant %q", canonical, v.Canonical)
			}
			if got := hex.EncodeToString([]byte(canonical)); got != v.CanonicalUtf8Hex {
				t.Errorf("canonical UTF-8\n got %s\nwant %s", got, v.CanonicalUtf8Hex)
			}
			h, _ := Hash8(v.Params)
			if h != v.Hash8 {
				t.Errorf("hash8 = %s, want %s", h, v.Hash8)
			}
			if name, _ := RunName("importer", v.Params); name != "importer-"+v.Hash8 {
				t.Errorf("run name = %s, want importer-%s", name, v.Hash8)
			}
		})
	}
}

// D1: "each repository vendors the same vector file". The suite's copy is the
// CLI's, byte for byte, so the oracle and the client are held to one file.
func TestBatchVendoredVectorsAreByteForByteTheCLIs(t *testing.T) {
	ours, err := os.ReadFile(filepath.Join("testdata", "naming-v1-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	cli, err := os.ReadFile(filepath.Join("..", "..", "..", "cmd", "batch", "testdata", "naming-v1-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ours, cli) {
		t.Fatal("tests/e2e/batch/testdata/naming-v1-vectors.json differs from cmd/batch/testdata/naming-v1-vectors.json; vendor the CLI's file unchanged")
	}
}

func TestBatchRunNamingRefusesALoneSurrogate(t *testing.T) {
	if _, err := RunName("importer", map[string]string{"note": "a\xed\xa0\x80b"}); err == nil {
		t.Fatal("a value that is not valid UTF-8 (a lone surrogate) was hashed")
	}
}

func TestBatchRestartNamesRunFromR1ToR99(t *testing.T) {
	if got, _ := RestartName("importer-09932cb3", 1); got != "importer-09932cb3-r1" {
		t.Errorf("first restart = %s", got)
	}
	if got, _ := RestartName("importer-09932cb3", 99); got != "importer-09932cb3-r99" {
		t.Errorf("last restart = %s", got)
	}
	for _, n := range []int{0, 100} {
		if _, err := RestartName("importer-09932cb3", n); err == nil {
			t.Errorf("r%d was accepted", n)
		}
	}
}
