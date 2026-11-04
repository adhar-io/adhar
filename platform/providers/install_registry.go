package provider

// The CLI's record of which install id belongs to which cluster.
//
// The id has to be readable when the cluster is NOT: `adhar down` deletes a
// cluster's volumes after its instances are gone, and a teardown that keeps the
// volumes (--keep-volumes, a refusal, a volume still attached) has to be able to
// finish the job days later, from nothing but a config file. So the id lives
// here, in ~/.adhar/state/installs/<provider>/<region>/<cluster>.json, as well as
// in the cluster's adhar-cluster-spec ConfigMap.
//
// One file per (provider, region, cluster name) holding a LIST, because a name
// is reused: tear "adhar" down keeping its volumes, run `adhar up` again, and the
// same name now has a running cluster with a new id AND a deleted one whose
// volumes are still there. Each is its own record, and a record is dropped only
// once a lookup by its id finds no volume left — never on the strength of the
// cluster having been deleted, which is how the 2026-10-04 Azure teardown lost
// the only list of what it had left behind (teardown-forgets-what-it-did-not-
// delete).
//
// Deliberately NOT under ~/.adhar/clusters/<name>/: every provider's
// DeleteCluster removes that directory (RemoveClusterState) whether or not the
// cluster's volumes survive it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ClusterInstall is one cluster the CLI built, remembered for as long as
// anything it created may still exist.
type ClusterInstall struct {
	// ID is the install id handed to the CSI driver (see ClusterIDTag).
	ID string `json:"id,omitempty"`
	// ProviderClusterID is a managed cluster's cloud-issued UUID, for clouds
	// whose own CSI driver stamps it on every volume (Civo, DOKS). A record may
	// carry this alone: those clusters' volumes are attributed by the cloud.
	ProviderClusterID string `json:"providerClusterId,omitempty"`
	Provider          string `json:"provider"`
	Region            string `json:"region,omitempty"`
	ClusterName       string `json:"clusterName"`
	// CreatedAt is when the record was made, which is just before the cluster.
	CreatedAt time.Time `json:"createdAt"`
	// ClusterDeletedAt is set once the cluster itself is gone (or was replaced
	// by a new cluster of the same name). A record that has it survives ONLY
	// because volumes carrying its id may remain.
	ClusterDeletedAt *time.Time `json:"clusterDeletedAt,omitempty"`
	// RetainedVolumes are the volumes the last teardown left in place.
	RetainedVolumes []string `json:"retainedVolumes,omitempty"`
	// KeptByChoice is true when some of those were kept because somebody said
	// so (`adhar down --keep-volumes`, or a "no" at the prompt) rather than
	// because a deletion failed. A later `adhar down --yes` does not delete
	// them; only a confirmation typed at a terminal does.
	KeptByChoice bool `json:"keptByChoice,omitempty"`
	// Note says why the record is still here, in words.
	Note string `json:"note,omitempty"`
}

// Active reports whether the record describes a cluster that still exists, as
// far as the CLI knows.
func (c ClusterInstall) Active() bool { return c.ClusterDeletedAt == nil }

// Owner is the record as a volume lookup key.
func (c ClusterInstall) Owner() VolumeOwner {
	return VolumeOwner{ClusterName: c.ClusterName, InstallID: c.ID, ProviderClusterID: c.ProviderClusterID}
}

// key identifies a record within its file.
func (c ClusterInstall) key() string {
	if c.ID != "" {
		return "id:" + c.ID
	}
	return "cloud:" + c.ProviderClusterID
}

type clusterInstallFile struct {
	Installs []ClusterInstall `json:"installs"`
}

// ClusterInstallsDir is where the records live.
func ClusterInstallsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving the home directory: %w", err)
	}
	return filepath.Join(home, ".adhar", "state", "installs"), nil
}

// installPathComponent makes one path element safe: a region or a cluster name
// is configuration, not a path.
func installPathComponent(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "_"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "." || out == ".." {
		return "_"
	}
	return out
}

func normalizeInstallProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

// ClusterInstallPath is the record file for one (provider, region, cluster).
func ClusterInstallPath(provider, region, cluster string) (string, error) {
	dir, err := ClusterInstallsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir,
		installPathComponent(normalizeInstallProvider(provider)),
		installPathComponent(region),
		installPathComponent(cluster)+".json"), nil
}

// LoadClusterInstalls returns every record for a cluster name, oldest first. A
// missing file is no records; an unreadable one is an error, never "none" —
// treating a corrupt record as empty would forget volumes it points at.
func LoadClusterInstalls(provider, region, cluster string) ([]ClusterInstall, error) {
	path, err := ClusterInstallPath(provider, region, cluster)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var f clusterInstallFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON (%v); fix or move it aside — it records which volumes belong "+
			"to which cluster, so it is never discarded automatically", path, err)
	}
	return f.Installs, nil
}

func writeClusterInstalls(provider, region, cluster string, installs []ClusterInstall) error {
	path, err := ClusterInstallPath(provider, region, cluster)
	if err != nil {
		return err
	}
	if len(installs) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("removing %s: %w", path, err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(clusterInstallFile{Installs: installs}, "", "  ")
	if err != nil {
		return err
	}
	// Atomic: os.WriteFile truncates first, so a kill or a full disk in that
	// window would leave an empty record — the lost-cluster outcome by another
	// route.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".install-*.json")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return os.Rename(tmpName, path)
}

// RecordClusterInstall generates the install id for a cluster about to be
// created and records it BEFORE anything exists, so no volume can be tagged
// with an id the CLI does not know.
//
// A record still marked active for the same name describes a cluster that is
// being replaced (`--recreate`, or one deleted outside the CLI): it is marked
// deleted, not dropped, because its volumes may still be there.
func RecordClusterInstall(provider, region, cluster string) (ClusterInstall, error) {
	installs, err := LoadClusterInstalls(provider, region, cluster)
	if err != nil {
		return ClusterInstall{}, err
	}
	id, err := NewClusterInstallID()
	if err != nil {
		return ClusterInstall{}, err
	}
	now := time.Now().UTC()
	for i := range installs {
		if installs[i].Active() {
			t := now
			installs[i].ClusterDeletedAt = &t
			installs[i].Note = "replaced by a new cluster of the same name; its volumes may remain"
		}
	}
	inst := ClusterInstall{
		ID:          id,
		Provider:    normalizeInstallProvider(provider),
		Region:      strings.TrimSpace(region),
		ClusterName: cluster,
		CreatedAt:   now,
	}
	installs = append(installs, inst)
	if err := writeClusterInstalls(provider, region, cluster, installs); err != nil {
		return ClusterInstall{}, err
	}
	return inst, nil
}

// ActiveClusterInstall is the record for the running cluster of this name, or
// nil when the CLI has none (a cluster from before attribution, or one created
// on another machine).
func ActiveClusterInstall(provider, region, cluster string) (*ClusterInstall, error) {
	installs, err := LoadClusterInstalls(provider, region, cluster)
	if err != nil {
		return nil, err
	}
	for i := len(installs) - 1; i >= 0; i-- {
		if installs[i].Active() {
			inst := installs[i]
			return &inst, nil
		}
	}
	return nil, nil
}

// AdoptClusterInstall records an install id read from the running cluster
// itself (its adhar-cluster-spec ConfigMap). The cluster is the authority on
// its own id: a cluster created from another machine, or recreated elsewhere,
// tags its volumes with the id it was given, whatever this machine remembers.
// Reports whether anything changed.
func AdoptClusterInstall(provider, region, cluster, id string) (bool, error) {
	if !ValidClusterInstallID(id) {
		return false, fmt.Errorf("refusing to record %q as an install id: not the shape adhar generates", id)
	}
	installs, err := LoadClusterInstalls(provider, region, cluster)
	if err != nil {
		return false, err
	}
	now := time.Now().UTC()
	changed := false
	found := false
	for i := range installs {
		switch {
		case installs[i].ID == id:
			found = true
			if !installs[i].Active() {
				installs[i].ClusterDeletedAt = nil
				installs[i].Note = ""
				changed = true
			}
		case installs[i].Active():
			t := now
			installs[i].ClusterDeletedAt = &t
			installs[i].Note = "the running cluster reports install id " + id + "; this one's volumes may remain"
			changed = true
		}
	}
	if !found {
		installs = append(installs, ClusterInstall{
			ID:          id,
			Provider:    normalizeInstallProvider(provider),
			Region:      strings.TrimSpace(region),
			ClusterName: cluster,
			CreatedAt:   now,
			Note:        "adopted from the cluster's adhar-cluster-spec",
		})
		changed = true
	}
	if !changed {
		return false, nil
	}
	return true, writeClusterInstalls(provider, region, cluster, installs)
}

// RecordProviderClusterID remembers a managed cluster's cloud-issued UUID — the
// identity Civo's and DOKS's own CSI drivers stamp on every volume — in a
// record of its own, unless a record already carries it. Reports whether
// anything was written.
//
// Called before the cluster is deleted (and by `adhar up` right after it is
// created): the volume step runs only AFTER the deletion, and a UUID held only
// in memory would be lost to a crash or a Ctrl+C in between, leaving volumes
// nothing can attribute. Anything that is not a UUID is refused — a compute
// cluster's "id" is its name, and a name attributes nothing.
func RecordProviderClusterID(provider, region, cluster, cloudID string) (bool, error) {
	cloudID = strings.TrimSpace(cloudID)
	if !IsCloudClusterUUID(cloudID) {
		return false, fmt.Errorf("refusing to record %q as a cloud cluster id: not a UUID", cloudID)
	}
	installs, err := LoadClusterInstalls(provider, region, cluster)
	if err != nil {
		return false, err
	}
	for _, i := range installs {
		if i.ProviderClusterID == cloudID {
			return false, nil
		}
	}
	installs = append(installs, ClusterInstall{
		ProviderClusterID: cloudID,
		Provider:          normalizeInstallProvider(provider),
		Region:            strings.TrimSpace(region),
		ClusterName:       cluster,
		CreatedAt:         time.Now().UTC(),
		Note:              "the cloud's own id for a managed cluster, whose CSI driver stamps it on every volume",
	})
	return true, writeClusterInstalls(provider, region, cluster, installs)
}

// MarkClusterInstallsDeleted records that the cluster of this name is gone:
// every record still marked active for it (its install id, and a managed
// cluster's UUID record) gets ClusterDeletedAt. Called by `adhar down` the
// moment DeleteCluster succeeds, BEFORE the volume step: a teardown cut short
// in between (a crash, a Ctrl+C) leaves records that say the cluster was seen
// deleted, so a re-run can finish its volumes under --yes. A record still
// marked active for a cluster the provider merely failed to list is treated
// far more carefully (cmd/down/volumes.go), because one listing call is all the
// evidence there is that it is gone. Reports how many records changed.
func MarkClusterInstallsDeleted(provider, region, cluster, note string) (int, error) {
	installs, err := LoadClusterInstalls(provider, region, cluster)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	changed := 0
	for i := range installs {
		if !installs[i].Active() {
			continue
		}
		t := now
		installs[i].ClusterDeletedAt = &t
		installs[i].Note = note
		changed++
	}
	if changed == 0 {
		return 0, nil
	}
	return changed, writeClusterInstalls(provider, region, cluster, installs)
}

// SaveClusterInstall inserts or replaces one record (matched by its id, or by
// the cloud's cluster UUID when it has no install id).
func SaveClusterInstall(inst ClusterInstall) error {
	if inst.ID == "" && inst.ProviderClusterID == "" {
		return fmt.Errorf("a cluster install record needs an install id or a cloud cluster id")
	}
	installs, err := LoadClusterInstalls(inst.Provider, inst.Region, inst.ClusterName)
	if err != nil {
		return err
	}
	inst.Provider = normalizeInstallProvider(inst.Provider)
	replaced := false
	for i := range installs {
		if installs[i].key() == inst.key() {
			installs[i] = inst
			replaced = true
		}
	}
	if !replaced {
		installs = append(installs, inst)
	}
	return writeClusterInstalls(inst.Provider, inst.Region, inst.ClusterName, installs)
}

// ForgetClusterInstall drops one record. Callers do this only after a lookup by
// the record's id found NO volume left: the record is the only way back to
// them.
func ForgetClusterInstall(inst ClusterInstall) error {
	installs, err := LoadClusterInstalls(inst.Provider, inst.Region, inst.ClusterName)
	if err != nil {
		return err
	}
	kept := installs[:0]
	for _, i := range installs {
		if i.key() != inst.key() {
			kept = append(kept, i)
		}
	}
	return writeClusterInstalls(inst.Provider, inst.Region, inst.ClusterName, kept)
}
