package down

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	pfactory "adhar-io/adhar/platform/providers"
)

// fakeCloud is a provider's block storage: volumes by id, each tagged with an
// install id (or none), plus whatever the legacy discovery would offer.
type fakeCloud struct {
	vols       map[string]*pfactory.BlockVolume
	installOf  map[string]string // volume id -> install id
	legacy     []string          // volume ids offered as legacy
	stuck      map[string]bool   // delete accepted, volume stays
	deleted    []string
	listCalls  int
	finalized  []pfactory.VolumeOwner
	listErr    error
	unprovable string // when set, the legacy discovery cannot prove ownership
}

func newFakeCloud() *fakeCloud {
	return &fakeCloud{vols: map[string]*pfactory.BlockVolume{}, installOf: map[string]string{}, stuck: map[string]bool{}}
}

func (f *fakeCloud) add(id, installID string, legacy bool) {
	f.vols[id] = &pfactory.BlockVolume{ID: id, Name: "pvc-" + id, SizeGiB: 4, PVCNamespace: "adhar-system", PVCName: "data-" + id,
		Created: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC), DeleteCommand: "cloud volume delete " + id}
	if installID != "" {
		f.installOf[id] = installID
	}
	if legacy {
		f.legacy = append(f.legacy, id)
	}
}

func (f *fakeCloud) ClusterVolumes(_ context.Context, owner pfactory.VolumeOwner) ([]pfactory.BlockVolume, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []pfactory.BlockVolume
	for id, v := range f.vols {
		if owner.InstallID != "" && f.installOf[id] == owner.InstallID {
			out = append(out, *v)
		}
	}
	pfactory.SortVolumes(out)
	return out, nil
}

func (f *fakeCloud) LegacyVolumes(context.Context, pfactory.VolumeOwner) (pfactory.LegacyVolumes, error) {
	if f.unprovable != "" {
		return pfactory.LegacyVolumes{Reason: "not provable: " + f.unprovable}, nil
	}
	res := pfactory.LegacyVolumes{Provable: len(f.legacy) > 0, Reason: "nothing that could own them is left"}
	for _, id := range f.legacy {
		if v, ok := f.vols[id]; ok {
			res.Volumes = append(res.Volumes, *v)
		}
	}
	return res, nil
}

func (f *fakeCloud) GetVolume(_ context.Context, id string) (pfactory.BlockVolume, bool, error) {
	v, ok := f.vols[id]
	if !ok {
		return pfactory.BlockVolume{}, false, nil
	}
	return *v, true, nil
}

func (f *fakeCloud) DeleteVolume(_ context.Context, v pfactory.BlockVolume) error {
	if _, ok := f.vols[v.ID]; !ok {
		return errors.New("not found")
	}
	f.deleted = append(f.deleted, v.ID)
	if !f.stuck[v.ID] {
		delete(f.vols, v.ID)
	}
	return nil
}

func (f *fakeCloud) AfterClusterVolumes(_ context.Context, owner pfactory.VolumeOwner, _ []pfactory.BlockVolume) error {
	f.finalized = append(f.finalized, owner)
	return nil
}

type stepOpts struct {
	interactive, yes, keep bool
	input                  string
}

func testStep(cloud *fakeCloud, o stepOpts) (*volumeStep, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return &volumeStep{
		ctx:         context.Background(),
		out:         out,
		in:          bufio.NewReader(strings.NewReader(o.input)),
		interactive: o.interactive,
		assumeYes:   o.yes,
		keep:        o.keep,
		finish:      "adhar down -f config.aws.yaml --env dev",
		managerFor: func(volumeJob) (pfactory.ClusterVolumeManager, error) {
			return cloud, nil
		},
		reap: pfactory.ReapOptions{DetachTimeout: 20 * time.Millisecond, VerifyTimeout: 20 * time.Millisecond, PollInterval: time.Millisecond},
		now:  time.Now,
	}, out
}

func deletedJob() volumeJob {
	return volumeJob{Env: "dev", ProviderName: "aws", Region: "ap-southeast-1", ClusterNames: []string{"adhar"},
		ClusterMode: pfactory.ClusterModeCompute, ClusterDeleted: true}
}

// recordInstall writes the CLI's record of a cluster, as `adhar up` does.
func recordInstall(t *testing.T) pfactory.ClusterInstall {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	inst, err := pfactory.RecordClusterInstall("aws", "ap-southeast-1", "adhar")
	if err != nil {
		t.Fatal(err)
	}
	return inst
}

func TestTheVolumeStepDeletesOnlyThisClustersVolumesAndForgetsItsRecord(t *testing.T) {
	inst := recordInstall(t)
	cloud := newFakeCloud()
	cloud.add("vol-mine-1", inst.ID, false)
	cloud.add("vol-mine-2", inst.ID, false)
	cloud.add("vol-same-name-other-install", "zzzzzzzzzzzzzzzz", false)
	cloud.add("vol-untagged", "", false)

	step, out := testStep(cloud, stepOpts{yes: true})
	if !step.run([]volumeJob{deletedJob()}) {
		t.Fatalf("the step reported failure:\n%s", out)
	}
	if got := strings.Join(sortedCopy(cloud.deleted), ","); got != "vol-mine-1,vol-mine-2" {
		t.Fatalf("deleted %s; only this install's volumes may go", got)
	}
	for _, want := range []string{"vol-mine-1", "4 GiB", "adhar-system/data-vol-mine-1", "2026-10-30", "read back as gone"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	recs, _ := pfactory.LoadClusterInstalls("aws", "ap-southeast-1", "adhar")
	if len(recs) != 0 {
		t.Fatalf("the record outlived its last volume: %+v", recs)
	}
	if len(cloud.finalized) != 1 || cloud.finalized[0].InstallID != inst.ID {
		t.Errorf("the provider's finalizer ran for %+v, want once for install %s", cloud.finalized, inst.ID)
	}
}

// "None offered" must not read as "none exist" when the provider could not
// prove whose the untagged volumes were.
func TestAnUnprovableLegacyCheckIsReportedNotSilent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cloud := newFakeCloud()
	cloud.unprovable = "1 instance(s) in ap-southeast-1 could own an untagged EBS CSI volume"
	step, out := testStep(cloud, stepOpts{yes: true})
	step.run([]volumeJob{deletedJob()})
	if !strings.Contains(out.String(), "were not offered — not provable: 1 instance(s)") {
		t.Errorf("the unprovable legacy check was not reported:\n%s", out)
	}
}

func TestKeepVolumesDeletesNothingAndKeepsTheRecordPointingAtThem(t *testing.T) {
	inst := recordInstall(t)
	cloud := newFakeCloud()
	cloud.add("vol-1", inst.ID, false)
	cloud.add("vol-legacy", "", true)

	step, out := testStep(cloud, stepOpts{keep: true, yes: true})
	if !step.run([]volumeJob{deletedJob()}) {
		t.Fatalf("keeping volumes on request is not a failure:\n%s", out)
	}
	if len(cloud.deleted) != 0 {
		t.Fatalf("--keep-volumes deleted %v", cloud.deleted)
	}
	recs, _ := pfactory.LoadClusterInstalls("aws", "ap-southeast-1", "adhar")
	if len(recs) != 1 || recs[0].ClusterDeletedAt == nil || strings.Join(recs[0].RetainedVolumes, ",") != "vol-1" {
		t.Fatalf("record = %+v; want it kept, marked deleted, pointing at vol-1", recs)
	}
	for _, want := range []string{"--keep-volumes", "adhar down -f config.aws.yaml --env dev", "cloud volume delete vol-1", "cloud volume delete vol-legacy"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestADeclinedConfirmationDeletesNothing(t *testing.T) {
	inst := recordInstall(t)
	cloud := newFakeCloud()
	cloud.add("vol-1", inst.ID, false)
	step, out := testStep(cloud, stepOpts{interactive: true, input: "no\n"})
	step.run([]volumeJob{deletedJob()})
	if len(cloud.deleted) != 0 {
		t.Fatalf("a declined confirmation deleted %v", cloud.deleted)
	}
	if !strings.Contains(out.String(), "Delete these 1 volume(s)?") || !strings.Contains(out.String(), "kept (not confirmed)") {
		t.Errorf("output:\n%s", out)
	}
	if recs, _ := pfactory.LoadClusterInstalls("aws", "ap-southeast-1", "adhar"); len(recs) != 1 {
		t.Fatalf("the record of kept volumes was dropped: %+v", recs)
	}
}

// Legacy volumes are found by evidence, not by a tag: only a confirmation typed
// at a terminal deletes them. --yes/--force does not, and neither does a piped
// "yes".
func TestLegacyVolumesNeedAConfirmationTypedAtATerminal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	legacyJob := volumeJob{Env: "dev", ProviderName: "aws", Region: "ap-southeast-1", ClusterNames: []string{"adhar"},
		ClusterMode: pfactory.ClusterModeCompute}

	for name, o := range map[string]stepOpts{
		"--yes at a terminal":         {interactive: true, yes: true},
		"--yes in a script":           {yes: true},
		"a piped yes, not a terminal": {input: "yes\n"},
	} {
		t.Run(name, func(t *testing.T) {
			cloud := newFakeCloud()
			cloud.add("vol-0c2f4e863c19f8550", "", true)
			step, out := testStep(cloud, o)
			step.run([]volumeJob{legacyJob})
			if len(cloud.deleted) != 0 {
				t.Fatalf("%s deleted a legacy volume", name)
			}
			if !strings.Contains(out.String(), "needs an interactive confirmation") {
				t.Errorf("output does not say why it was kept:\n%s", out)
			}
		})
	}

	cloud := newFakeCloud()
	cloud.add("vol-0c2f4e863c19f8550", "", true)
	step, out := testStep(cloud, stepOpts{interactive: true, input: "yes\n"})
	if !step.run([]volumeJob{legacyJob}) || strings.Join(cloud.deleted, ",") != "vol-0c2f4e863c19f8550" {
		t.Fatalf("a typed yes at a terminal did not delete the legacy volume: %v\n%s", cloud.deleted, out)
	}
	if !strings.Contains(out.String(), "nothing that could own them is left") {
		t.Errorf("the evidence was not shown before asking:\n%s", out)
	}
}

// A deletion that cannot be verified keeps the record and fails the run, with
// the volume named and the command that finishes the job.
func TestARecordSurvivesUntilItsVolumesAreVerifiedGone(t *testing.T) {
	inst := recordInstall(t)
	cloud := newFakeCloud()
	cloud.add("vol-gone", inst.ID, false)
	cloud.add("vol-stuck", inst.ID, false)
	cloud.stuck["vol-stuck"] = true
	step, out := testStep(cloud, stepOpts{yes: true})
	if step.run([]volumeJob{deletedJob()}) {
		t.Fatalf("an unverified deletion was reported as success:\n%s", out)
	}
	recs, _ := pfactory.LoadClusterInstalls("aws", "ap-southeast-1", "adhar")
	if len(recs) != 1 || strings.Join(recs[0].RetainedVolumes, ",") != "vol-stuck" {
		t.Fatalf("record = %+v; want it kept, pointing at vol-stuck only", recs)
	}
	for _, want := range []string{"vol-stuck", "still exists", "cloud volume delete vol-stuck", "adhar down -f config.aws.yaml --env dev"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// "Could not list" is never "nothing there": nothing is deleted, the record is
// kept, and the run fails.
func TestAVolumeListingFailureDeletesNothingAndKeepsTheRecord(t *testing.T) {
	inst := recordInstall(t)
	cloud := newFakeCloud()
	cloud.add("vol-1", inst.ID, false)
	cloud.listErr = errors.New("UnauthorizedOperation")
	step, out := testStep(cloud, stepOpts{yes: true})
	if step.run([]volumeJob{deletedJob()}) {
		t.Fatalf("a listing failure was reported as success:\n%s", out)
	}
	if len(cloud.deleted) != 0 {
		t.Fatal("something was deleted although nothing could be listed")
	}
	if recs, _ := pfactory.LoadClusterInstalls("aws", "ap-southeast-1", "adhar"); len(recs) != 1 {
		t.Fatalf("the record was dropped on a listing failure: %+v", recs)
	}
}

// A provided cluster's volumes are never the platform's, even if a job for one
// somehow reached the step.
func TestTheVolumeStepRefusesAProvidedCluster(t *testing.T) {
	inst := recordInstall(t)
	cloud := newFakeCloud()
	cloud.add("vol-1", inst.ID, false)
	job := deletedJob()
	job.ClusterMode = pfactory.ClusterModeProvided
	step, _ := testStep(cloud, stepOpts{yes: true})
	step.run([]volumeJob{job})
	if len(cloud.deleted) != 0 || cloud.listCalls != 0 {
		t.Fatalf("a provided cluster's volumes were touched: deleted=%v lists=%d", cloud.deleted, cloud.listCalls)
	}
	if clusterModeOf(nil) == pfactory.ClusterModeProvided {
		t.Fatal("precondition: an environment with no provider block is compute")
	}
}

// And the teardown loop never queues one: the provided gate `continue`s before
// either place a volume job is created.
func TestProvidedEnvironmentsNeverQueueAVolumeJob(t *testing.T) {
	src, err := os.ReadFile("down.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	gate := strings.Index(body, "case !pfactory.ClusterLifecycleIsOurs(mode):")
	gateEnd := strings.Index(body[gate:], "continue")
	if gate < 0 || gateEnd < 0 {
		t.Fatal("the provided-cluster gate is gone from the teardown loop")
	}
	for i, at := 0, 0; ; i++ {
		j := strings.Index(body[at:], "outcome.VolumeJobs = append(")
		if j < 0 {
			if i != 2 {
				t.Fatalf("found %d places that queue a volume job, want 2 (deleted and already gone)", i)
			}
			break
		}
		if at+j < gate+gateEnd {
			t.Fatal("a volume job is queued before the provided-cluster gate")
		}
		at += j + 1
	}
}

func TestEveryProviderThisTeardownBuildsLeavesTheVolumesToTheStep(t *testing.T) {
	prev := purgeVolumes
	t.Cleanup(func() { purgeVolumes = prev })
	purgeVolumes = false
	if !pfactory.KeepClusterVolumesRequested(teardownProviderOptions()) {
		t.Fatal("DeleteCluster would delete the cluster's volumes before anyone is asked, and --keep-volumes could not be honoured")
	}
}

func TestTheConfirmationSaysWhatHappensToTheVolumes(t *testing.T) {
	plan := teardownPlan{ConfigFile: "config.aws.yaml", Environments: []string{"dev"}, CloudResources: true}
	if out := RenderTeardownConfirmation(plan); !strings.Contains(out, "you confirm before any") {
		t.Errorf("the panel does not say volumes are confirmed separately:\n%s", out)
	}
	plan.KeepVolumes = true
	if out := RenderTeardownConfirmation(plan); !strings.Contains(out, "Kept (--keep-volumes)") || strings.Contains(out, "destroyed with the volumes") {
		t.Errorf("with --keep-volumes the panel still promises to destroy volume data:\n%s", out)
	}
}

func sortedCopy(in []string) []string {
	out := append([]string{}, in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// One namespace-isolated cluster serves dev, test and prod; tearing the whole
// file down must ask about its volumes once, not three times.
func TestASharedClusterIsAskedAboutOnce(t *testing.T) {
	inst := recordInstall(t)
	cloud := newFakeCloud()
	cloud.add("vol-1", inst.ID, false)
	var jobs []volumeJob
	for _, env := range []string{"dev", "prod", "test"} {
		jobs = append(jobs, volumeJob{Env: env, ProviderName: "aws", Region: "ap-southeast-1",
			ClusterNames: []string{"adhar", env}, ClusterMode: pfactory.ClusterModeCompute})
	}
	step, out := testStep(cloud, stepOpts{interactive: true, input: "yes\n"})
	if !step.run(jobs) {
		t.Fatalf("step failed:\n%s", out)
	}
	if n := strings.Count(out.String(), "Delete these"); n != 1 {
		t.Fatalf("asked %d times about one cluster's volumes:\n%s", n, out)
	}
	if strings.Join(cloud.deleted, ",") != "vol-1" {
		t.Fatalf("deleted %v", cloud.deleted)
	}
	merged := mergeVolumeJobs(append(jobs, volumeJob{ProviderName: "aws", Region: "ap-southeast-1",
		ClusterNames: []string{"adhar"}, ClusterMode: pfactory.ClusterModeProvided}))
	if len(merged) != 1 || merged[0].ClusterMode != pfactory.ClusterModeProvided {
		t.Fatalf("merged = %+v; a provided mode among the merged must win", merged)
	}
}
