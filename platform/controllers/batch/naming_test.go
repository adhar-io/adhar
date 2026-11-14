package batch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The observer names a dependsOn run itself, and the name is the duplicate
// guard: it must be byte for byte the name the CLI, the Console and the kit
// compute for the same identity, or a person's `adhar batch run -p
// businessDate=D` and the observer's dependency run are two runs of one date.
// The vector file is the CLI's, which every repository vendors.
func TestObserverRunNamesAgreeWithTheSharedNamingVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "cmd", "batch", "testdata", "naming-v1-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Version string `json:"version"`
		Vectors []struct {
			ID        string            `json:"id"`
			Params    map[string]string `json:"params"`
			Canonical string            `json:"canonical"`
			Hash8     string            `json:"hash8"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if file.Version != namingVersion || len(file.Vectors) < 7 {
		t.Fatalf("vectors are naming %s with %d vectors; the observer implements %s", file.Version, len(file.Vectors), namingVersion)
	}
	for _, v := range file.Vectors {
		canonical, err := canonicalParams(v.Params)
		if err != nil {
			t.Fatalf("%s: %v", v.ID, err)
		}
		if canonical != v.Canonical {
			t.Errorf("%s: canonical = %q, want %q", v.ID, canonical, v.Canonical)
		}
		name, err := runName("importer", v.Params)
		if err != nil || name != "importer-"+v.Hash8 {
			t.Errorf("%s: run name = %q (%v), want importer-%s", v.ID, name, err, v.Hash8)
		}
	}
}
