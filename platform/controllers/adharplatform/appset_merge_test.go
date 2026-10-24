package adharplatform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func productionAppSet(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "stack", "adhar-appset-production.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// marketplaceFlip edits the file the way the console's flipElementEnabled
// does: one `enabled:` line inside the named element, nothing else.
func marketplaceFlip(t *testing.T, data []byte, name string, on bool) []byte {
	t.Helper()
	before := appSetEnabledFlags(data)[name]
	out := setAppSetFlags(data, map[string]bool{name: on})
	if appSetEnabledFlags(out)[name] != on || before == on {
		t.Fatalf("test setup: could not flip %s to %t", name, on)
	}
	return out
}

// pick returns one package enabled and one disabled in the profile.
func pick(t *testing.T, data []byte) (on, off string) {
	t.Helper()
	for name, v := range appSetEnabledFlags(data) {
		if v && on == "" {
			on = name
		}
		if !v && off == "" {
			off = name
		}
	}
	if on == "" || off == "" {
		t.Fatal("profile needs both an enabled and a disabled package")
	}
	return on, off
}

// A package the Marketplace enabled must survive every later push of an
// unchanged local file — the first one (repo pushed before the annotation, so
// the base is the last non-Marketplace commit) AND the second one, where the
// last CLI commit already carries the kept flag and only the annotation still
// says what the local file wanted.
func TestPushesKeepMarketplaceToggles(t *testing.T) {
	local := productionAppSet(t)
	_, off := pick(t, local)

	legacyHead := marketplaceFlip(t, local, off, true)
	first, kept := MergeMarketplaceToggles(local, legacyHead, local)
	if !appSetEnabledFlags(first)[off] || len(kept) != 1 || kept[0] != off {
		t.Fatalf("first push reverted the Marketplace's %s (kept=%v)", off, kept)
	}

	second, _ := MergeMarketplaceToggles(local, first, first) // the last non-Marketplace commit IS the first push
	if !appSetEnabledFlags(second)[off] {
		t.Fatalf("second push reverted the Marketplace's %s", off)
	}

	// The Marketplace turns it off again; the next push must keep THAT.
	third, _ := MergeMarketplaceToggles(local, marketplaceFlip(t, second, off, false), nil)
	if appSetEnabledFlags(third)[off] {
		t.Fatalf("a later Marketplace disable of %s was reverted", off)
	}
}

// `adhar stack enable/disable` edits the local file; that explicit change wins,
// including over a Marketplace flip of the same package.
func TestLocalEditsStillReachGitea(t *testing.T) {
	seeded := productionAppSet(t)
	on, off := pick(t, seeded)
	head, _ := MergeMarketplaceToggles(seeded, nil, nil)
	head = marketplaceFlip(t, head, off, true)

	local := setAppSetFlags(seeded, map[string]bool{on: false, off: false})
	// `off` is unchanged locally, so the Marketplace's enable is kept; `on`
	// was disabled locally, so it goes out disabled.
	merged, _ := MergeMarketplaceToggles(local, head, nil)
	if f := appSetEnabledFlags(merged); f[on] || !f[off] {
		t.Fatalf("got %s=%t %s=%t, want false/true", on, f[on], off, f[off])
	}

	local = setAppSetFlags(seeded, map[string]bool{off: true})
	head = marketplaceFlip(t, head, off, false)
	merged, _ = MergeMarketplaceToggles(local, head, nil)
	if !appSetEnabledFlags(merged)[off] {
		t.Fatalf("a local enable of %s lost to an older Marketplace disable", off)
	}
}

// A fresh repo has nothing to merge: the local file goes out as it is, plus
// the annotation, and the result is still a valid ApplicationSet.
func TestFirstSeedIsTheLocalFile(t *testing.T) {
	local := productionAppSet(t)
	merged, kept := MergeMarketplaceToggles(local, nil, nil)
	if len(kept) != 0 {
		t.Fatalf("kept %v on a fresh repo", kept)
	}
	if string(merged) == string(local) {
		t.Fatal("annotation was not added")
	}
	var obj struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name        string            `json:"name"`
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	if err := yaml.Unmarshal(merged, &obj); err != nil {
		t.Fatalf("merged file is not YAML: %v", err)
	}
	if obj.Kind != "ApplicationSet" || obj.Metadata.Name != "helm-charts-production" {
		t.Fatalf("merged file lost its identity: %+v", obj)
	}
	set, ok := seededEnabled(merged)
	if !ok || obj.Metadata.Annotations[SeededEnabledAnnotation] == "" {
		t.Fatal("annotation missing")
	}
	for name, v := range appSetEnabledFlags(local) {
		if set[name] != v {
			t.Fatalf("annotation says %s=%t, local file says %t", name, set[name], v)
		}
	}
	// Re-stamping replaces the value instead of adding a second line.
	again, _ := MergeMarketplaceToggles(local, merged, nil)
	if strings.Count(string(again), SeededEnabledAnnotation) != 1 {
		t.Fatal("annotation duplicated on a second push")
	}
}
