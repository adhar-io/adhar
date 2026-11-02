package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeVolumes is a cloud with block volumes and nothing else.
type fakeVolumes struct {
	vols map[string]*fakeVol
}

type fakeVol struct {
	v BlockVolume
	// detachAfter reads, the volume reports detached.
	detachAfter int
	reads       int
	// stuck: the delete is accepted and the volume stays.
	stuck bool
	// refuse: the delete is refused.
	refuse error
	// readErr: every read fails.
	readErr error
	deleted bool
}

func (f *fakeVolumes) ClusterVolumes(context.Context, VolumeOwner) ([]BlockVolume, error) {
	return nil, nil
}

func (f *fakeVolumes) LegacyVolumes(context.Context, VolumeOwner) (LegacyVolumes, error) {
	return LegacyVolumes{}, nil
}

func (f *fakeVolumes) GetVolume(_ context.Context, id string) (BlockVolume, bool, error) {
	fv, ok := f.vols[id]
	if !ok || (fv.deleted && !fv.stuck) {
		return BlockVolume{}, false, nil
	}
	if fv.readErr != nil {
		return BlockVolume{}, false, fv.readErr
	}
	fv.reads++
	v := fv.v
	if fv.detachAfter >= 0 && fv.reads > fv.detachAfter {
		v.AttachedTo = nil
		fv.v.AttachedTo = nil
	}
	return v, true, nil
}

func (f *fakeVolumes) DeleteVolume(_ context.Context, v BlockVolume) error {
	fv := f.vols[v.ID]
	if fv.refuse != nil {
		return fv.refuse
	}
	if len(fv.v.AttachedTo) > 0 {
		return errors.New("in use")
	}
	fv.deleted = true
	return nil
}

func fast() ReapOptions {
	return ReapOptions{DetachTimeout: 30 * time.Millisecond, VerifyTimeout: 30 * time.Millisecond, PollInterval: time.Millisecond}
}

func ids(vs []BlockVolume) string {
	var out []string
	for _, v := range vs {
		out = append(out, v.ID)
	}
	return strings.Join(out, ",")
}

func TestReapVolumesReportsExactlyWhatHappenedToEachVolume(t *testing.T) {
	f := &fakeVolumes{vols: map[string]*fakeVol{
		"free":       {v: BlockVolume{ID: "free"}, detachAfter: -1},
		"detaches":   {v: BlockVolume{ID: "detaches", AttachedTo: []string{"i-1"}}, detachAfter: 2},
		"held":       {v: BlockVolume{ID: "held", AttachedTo: []string{"i-not-ours"}}, detachAfter: 1 << 30},
		"stuck":      {v: BlockVolume{ID: "stuck"}, detachAfter: -1, stuck: true},
		"refused":    {v: BlockVolume{ID: "refused"}, detachAfter: -1, refuse: errors.New("AccessDenied")},
		"unreadable": {v: BlockVolume{ID: "unreadable"}, readErr: errors.New("throttled")},
	}}
	var vols []BlockVolume
	for _, id := range []string{"free", "detaches", "held", "stuck", "refused", "unreadable", "already-gone"} {
		vols = append(vols, BlockVolume{ID: id})
	}
	res := ReapVolumes(context.Background(), f, vols, fast())

	if got := ids(res.Deleted); !strings.Contains(got, "free") || !strings.Contains(got, "detaches") || !strings.Contains(got, "already-gone") || len(res.Deleted) != 3 {
		t.Errorf("deleted = %q, want free, detaches and already-gone", got)
	}
	if got := ids(res.StillAttached); got != "held" {
		t.Errorf("still attached = %q, want held — a volume something else still uses is never deleted", got)
	}
	reasons := map[string]string{}
	for _, fl := range res.Failed {
		reasons[fl.Volume.ID] = fl.Reason
	}
	if !strings.Contains(reasons["stuck"], "still exists") {
		t.Errorf("an accepted deletion that did not happen was reported as %q", reasons["stuck"])
	}
	if !strings.Contains(reasons["refused"], "AccessDenied") {
		t.Errorf("a refused deletion was reported as %q", reasons["refused"])
	}
	if !strings.Contains(reasons["unreadable"], "could not be read") {
		t.Errorf("an unreadable volume was reported as %q", reasons["unreadable"])
	}
	if f.vols["held"].deleted {
		t.Fatal("an attached volume was deleted")
	}
	if len(res.Remaining()) != 4 {
		t.Errorf("remaining = %d, want held, stuck, refused, unreadable", len(res.Remaining()))
	}
}

func TestInstallIDsAreUniqueAndSafeEverywhereTheyAreWritten(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id, err := NewClusterInstallID()
		if err != nil {
			t.Fatal(err)
		}
		if !ValidClusterInstallID(id) {
			t.Fatalf("generated id %q fails its own shape", id)
		}
		if id[0] < 'a' || id[0] > 'z' {
			t.Fatalf("id %q does not start with a letter; helm --set would read a numeric one as a number", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
	for _, bad := range []string{"", "1234567890123456", "ABCDEFGHIJKLMNOP", "abc", "abcdefghijklmnop/", "abcdefghijklmnopq"} {
		if ValidClusterInstallID(bad) {
			t.Errorf("%q accepted as an install id", bad)
		}
	}
}

func TestOnlyACloudIssuedUUIDIsAClusterIdentity(t *testing.T) {
	if !IsCloudClusterUUID("5c1d6b7e-8f9a-4b2c-9d3e-0f1a2b3c4d5e") {
		t.Error("a UUID was not recognised")
	}
	for _, s := range []string{"", "adhar", "adhar-cluster-adhar", "aws-adhar"} {
		if IsCloudClusterUUID(s) {
			t.Errorf("%q was accepted as a cluster UUID; a name is not an identity", s)
		}
	}
}

func withHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// A new cluster of the same name never overwrites the record of an older one:
// the older one's volumes may have been kept on purpose.
func TestRecordingANewClusterKeepsTheOldOnesRecord(t *testing.T) {
	withHome(t)
	first, err := RecordClusterInstall("aws", "ap-southeast-1", "adhar")
	if err != nil {
		t.Fatal(err)
	}
	second, err := RecordClusterInstall("AWS", "ap-southeast-1", "adhar")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("two clusters got the same install id")
	}
	all, err := LoadClusterInstalls("aws", "ap-southeast-1", "adhar")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].ID != first.ID || all[0].Active() || !all[1].Active() {
		t.Fatalf("records = %+v; want the first marked deleted and kept, the second active", all)
	}
	active, err := ActiveClusterInstall("aws", "ap-southeast-1", "adhar")
	if err != nil || active == nil || active.ID != second.ID {
		t.Fatalf("active = %+v, %v; want the second", active, err)
	}
	// Different region, different cluster.
	if other, _ := ActiveClusterInstall("aws", "us-east-1", "adhar"); other != nil {
		t.Fatalf("a cluster in another region inherited the record: %+v", other)
	}
}

func TestTheRunningClustersIDWinsOverALocalRecord(t *testing.T) {
	withHome(t)
	local, err := RecordClusterInstall("gcp", "us-central1", "adhar")
	if err != nil {
		t.Fatal(err)
	}
	changed, err := AdoptClusterInstall("gcp", "us-central1", "adhar", "zzzzzzzzzzzzzzz1")
	if err != nil || !changed {
		t.Fatalf("adopt = %v, %v", changed, err)
	}
	active, _ := ActiveClusterInstall("gcp", "us-central1", "adhar")
	if active == nil || active.ID != "zzzzzzzzzzzzzzz1" {
		t.Fatalf("active = %+v; the cluster's own id must win", active)
	}
	all, _ := LoadClusterInstalls("gcp", "us-central1", "adhar")
	if len(all) != 2 || all[0].ID != local.ID || all[0].Active() {
		t.Fatalf("the superseded local record was dropped instead of kept: %+v", all)
	}
	if changed, _ := AdoptClusterInstall("gcp", "us-central1", "adhar", "zzzzzzzzzzzzzzz1"); changed {
		t.Error("adopting the same id twice reported a change")
	}
	if _, err := AdoptClusterInstall("gcp", "us-central1", "adhar", "not an id"); err == nil {
		t.Error("a malformed id was adopted")
	}
}

func TestARecordIsForgottenOnlyWhenAskedAndTheFileGoesWithTheLast(t *testing.T) {
	home := withHome(t)
	a, _ := RecordClusterInstall("azure", "centralindia", "adhar")
	b := ClusterInstall{ProviderClusterID: "5c1d6b7e-8f9a-4b2c-9d3e-0f1a2b3c4d5e", Provider: "azure", Region: "centralindia", ClusterName: "adhar"}
	if err := SaveClusterInstall(b); err != nil {
		t.Fatal(err)
	}
	if err := ForgetClusterInstall(a); err != nil {
		t.Fatal(err)
	}
	all, _ := LoadClusterInstalls("azure", "centralindia", "adhar")
	if len(all) != 1 || all[0].ProviderClusterID != b.ProviderClusterID {
		t.Fatalf("records = %+v; want only the cloud-id record", all)
	}
	if err := ForgetClusterInstall(b); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".adhar", "state", "installs", "azure", "centralindia", "adhar.json")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the empty record file was left behind (%v)", err)
	}
}

// A record file that cannot be parsed is an error, never "no records": treating
// it as empty would forget the volumes it points at, and the next `adhar up`
// would overwrite it.
func TestAnUnreadableRecordIsNeverTreatedAsEmpty(t *testing.T) {
	withHome(t)
	path, err := ClusterInstallPath("aws", "ap-southeast-1", "adhar")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadClusterInstalls("aws", "ap-southeast-1", "adhar"); err == nil {
		t.Fatal("a corrupt record read as empty")
	}
	if _, err := RecordClusterInstall("aws", "ap-southeast-1", "adhar"); err == nil {
		t.Fatal("a corrupt record was overwritten by a new cluster's")
	}
	if b, _ := os.ReadFile(path); string(b) != "{not json" {
		t.Fatal("the corrupt record was modified")
	}
}

func TestRecordPathsCannotEscapeTheStateDirectory(t *testing.T) {
	home := withHome(t)
	path, err := ClusterInstallPath("aws", "../../etc", "../passwd")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, ".adhar", "state", "installs") + string(filepath.Separator)
	if filepath.Clean(path) != path || !strings.HasPrefix(path, root) {
		t.Fatalf("path %q escapes the state directory", path)
	}
	for _, seg := range strings.Split(strings.TrimPrefix(path, root), string(filepath.Separator)) {
		if seg == ".." || seg == "." {
			t.Fatalf("path %q has a traversal segment", path)
		}
	}
	if p, _ := ClusterInstallPath("aws", "..", ".."); strings.Contains(p, string(filepath.Separator)+".."+string(filepath.Separator)) {
		t.Fatalf("path %q has a traversal segment", p)
	}
}
