package azure

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"adhar-io/adhar/platform/types"
)

// stateProvider returns a provider whose state lives under a temporary HOME.
// getStateFilePath goes through os.UserHomeDir, which honours $HOME.
func stateProvider(t *testing.T) (*Provider, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	p := &Provider{
		config:           &Config{SubscriptionID: "sub", ResourceGroup: "adhar-rg"},
		clusters:         map[string]*types.Cluster{},
		resourceTrackers: map[string]*ResourceTracker{},
	}
	return p, filepath.Join(home, ".adhar", "state", "azure", "clusters.json")
}

// The state file must survive a write, intact and complete.
//
// It is the only record of what exists in the cloud, so a partial write is a
// lost cluster: os.WriteFile truncates and then writes, and anything between the
// two — a crash, a full disk, a kill — leaves an empty or half-written file that
// the next run reads as "no clusters". The write now goes through a temporary
// file and a rename, which is atomic within a directory.
func TestSaveStateRoundTripsAndLeavesNoDebris(t *testing.T) {
	p, stateFile := stateProvider(t)
	p.clusters["azure-adhar"] = &types.Cluster{Name: "adhar", Status: "running"}
	p.resourceTrackers["azure-adhar"] = &ResourceTracker{
		ResourceGroup:   "adhar-rg",
		Location:        "centralindia",
		VirtualMachines: []string{"adhar-master-0", "adhar-worker-workers-0"},
	}

	if err := p.saveState(); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	// Valid, complete JSON — not a truncated document.
	b, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("reading state: %v", err)
	}
	var round StateData
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatalf("state file is not valid JSON (a truncated write looks exactly like this): %v", err)
	}
	if len(round.Clusters) != 1 || round.ResourceTrackers["azure-adhar"] == nil {
		t.Errorf("state did not round-trip: %s", b)
	}
	if got := round.ResourceTrackers["azure-adhar"].VirtualMachines; len(got) != 2 {
		t.Errorf("tracker lost its VMs: %v", got)
	}

	// The temporary file must not be left behind: a directory filling with
	// .clusters-*.json is how a "harmless" atomic write becomes a disk problem.
	entries, err := os.ReadDir(filepath.Dir(stateFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "clusters.json" {
			t.Errorf("unexpected leftover in the state directory: %s", e.Name())
		}
	}

	// And loading it back must see the cluster.
	fresh := &Provider{config: p.config, clusters: map[string]*types.Cluster{}, resourceTrackers: map[string]*ResourceTracker{}}
	if err := fresh.loadState(); err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if len(fresh.clusters) != 1 {
		t.Errorf("loadState saw %d clusters, want 1 — this is the read that decides whether "+
			"`adhar down` can find anything", len(fresh.clusters))
	}
}

// Overwriting an existing state file must replace it wholly, with no remnant of
// the longer previous document.
func TestSaveStateReplacesRatherThanOverlays(t *testing.T) {
	p, stateFile := stateProvider(t)
	p.clusters["azure-adhar"] = &types.Cluster{Name: "adhar"}
	p.resourceTrackers["azure-adhar"] = &ResourceTracker{
		ResourceGroup:   "adhar-rg",
		VirtualMachines: []string{"a", "b", "c", "d", "e", "f", "g", "h"},
	}
	if err := p.saveState(); err != nil {
		t.Fatal(err)
	}

	// A legitimately empty state — the last cluster really was deleted — must be
	// writable, and must not leave the old content behind it.
	p.clusters = map[string]*types.Cluster{}
	p.resourceTrackers = map[string]*ResourceTracker{}
	if err := p.saveState(); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	var round StateData
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatalf("state file is not valid JSON after a shrinking write: %v\n%s", err, b)
	}
	if len(round.Clusters) != 0 || len(round.ResourceTrackers) != 0 {
		t.Errorf("expected an empty state, got: %s", b)
	}
}

// loadState must not wipe what is already in memory when the file is absent.
// A missing state file means "nothing recorded yet", not "forget what you know".
func TestLoadStateLeavesMemoryAloneWhenNoFileExists(t *testing.T) {
	p, _ := stateProvider(t)
	p.clusters["azure-adhar"] = &types.Cluster{Name: "adhar"}
	if err := p.loadState(); err != nil {
		t.Fatalf("loadState with no file must not error: %v", err)
	}
	if len(p.clusters) != 1 {
		t.Error("a missing state file must not clear clusters already held in memory")
	}
}
