package down

import (
	"os"
	"strings"
	"testing"

	"adhar-io/adhar/cmd/helpers"
	pfactory "adhar-io/adhar/platform/providers"
)

// absentJob is the job the teardown loop queues for a cluster it did NOT find:
// it deleted nothing, so any record still marked running describes a cluster
// nobody saw deleted.
func absentJob() volumeJob {
	return volumeJob{Env: "dev", ProviderName: "aws", Region: "ap-southeast-1", ClusterNames: []string{"adhar", "dev"},
		ClusterMode: pfactory.ClusterModeCompute}
}

// "Not found" is one listing call, and several providers' listings answer
// without an error when half of them failed (DigitalOcean, Civo, AWS, Azure;
// see unconfirmedGone). The install id proves whose the volumes are — not that
// their cluster is gone. So a record this machine still marks as RUNNING, met
// by a teardown that deleted nothing, is never acted on by --yes, never
// offered while one of its volumes is attached, and left exactly as it was
// unless a person at a terminal says the cluster is gone.
func TestAClusterNotSeenDeletedIsNeverReapedUnderYes(t *testing.T) {
	stillRunning := func(t *testing.T, inst pfactory.ClusterInstall) {
		t.Helper()
		recs, err := pfactory.LoadClusterInstalls("aws", "ap-southeast-1", "adhar")
		if err != nil || len(recs) != 1 || recs[0].ID != inst.ID || !recs[0].Active() || len(recs[0].RetainedVolumes) != 0 || recs[0].KeptByChoice {
			t.Fatalf("record = %+v (%v); a cluster nobody saw deleted must keep its record exactly as it was", recs, err)
		}
	}

	t.Run("--yes deletes nothing and leaves the record alone", func(t *testing.T) {
		inst := recordInstall(t)
		cloud := newFakeCloud()
		cloud.add("vol-1", inst.ID, false)
		step, out := testStep(cloud, stepOpts{yes: true})
		step.run([]volumeJob{absentJob()})
		if len(cloud.deleted) != 0 {
			t.Fatalf("--yes deleted %v:\n%s", cloud.deleted, out)
		}
		for _, want := range []string{"still records as RUNNING", "confirm at a terminal", "vol-1", inst.ID} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
		stillRunning(t, inst)
	})

	t.Run("an attached volume means nothing is offered, even at a terminal", func(t *testing.T) {
		inst := recordInstall(t)
		cloud := newFakeCloud()
		cloud.add("vol-attached", inst.ID, false)
		cloud.vols["vol-attached"].AttachedTo = []string{"i-0running"}
		cloud.add("vol-detached", inst.ID, false)
		step, out := testStep(cloud, stepOpts{interactive: true, input: "yes\nyes\n"})
		step.run([]volumeJob{absentJob()})
		if len(cloud.deleted) != 0 {
			t.Fatalf("deleted %v while a volume of the same install was attached:\n%s", cloud.deleted, out)
		}
		if strings.Contains(out.String(), "Delete these") || !strings.Contains(out.String(), "may well be running") {
			t.Errorf("an attached install must not be offered at all:\n%s", out)
		}
		stillRunning(t, inst)
	})

	t.Run("a typed no keeps them and the record", func(t *testing.T) {
		inst := recordInstall(t)
		cloud := newFakeCloud()
		cloud.add("vol-1", inst.ID, false)
		step, out := testStep(cloud, stepOpts{interactive: true, input: "no\n"})
		step.run([]volumeJob{absentJob()})
		if len(cloud.deleted) != 0 {
			t.Fatalf("a \"no\" deleted %v:\n%s", cloud.deleted, out)
		}
		stillRunning(t, inst)
	})

	t.Run("a typed yes at a terminal deletes them and forgets the record", func(t *testing.T) {
		inst := recordInstall(t)
		cloud := newFakeCloud()
		cloud.add("vol-1", inst.ID, false)
		cloud.add("vol-2", inst.ID, false)
		step, out := testStep(cloud, stepOpts{interactive: true, input: "yes\n"})
		if !step.run([]volumeJob{absentJob()}) || strings.Join(sortedCopy(cloud.deleted), ",") != "vol-1,vol-2" {
			t.Fatalf("deleted %v:\n%s", cloud.deleted, out)
		}
		if recs, _ := pfactory.LoadClusterInstalls("aws", "ap-southeast-1", "adhar"); len(recs) != 0 {
			t.Fatalf("the record outlived its last volume: %+v", recs)
		}
	})

	t.Run("a cluster the CLI saw deleted is finished under --yes", func(t *testing.T) {
		inst := recordInstall(t)
		if n, err := pfactory.MarkClusterInstallsDeleted("aws", "ap-southeast-1", "adhar", "deleted by `adhar down`"); err != nil || n != 1 {
			t.Fatalf("marked %d (%v)", n, err)
		}
		cloud := newFakeCloud()
		cloud.add("vol-1", inst.ID, false)
		step, out := testStep(cloud, stepOpts{yes: true})
		if !step.run([]volumeJob{absentJob()}) || strings.Join(cloud.deleted, ",") != "vol-1" {
			t.Fatalf("a re-run after the cluster was seen deleted did not finish the job: %v\n%s", cloud.deleted, out)
		}
	})

	t.Run("the cluster this run deleted is not held back", func(t *testing.T) {
		inst := recordInstall(t) // marking failed: still active
		cloud := newFakeCloud()
		cloud.add("vol-1", inst.ID, false)
		step, out := testStep(cloud, stepOpts{yes: true})
		if !step.run([]volumeJob{deletedJob()}) || strings.Join(cloud.deleted, ",") != "vol-1" {
			t.Fatalf("deleted %v:\n%s", cloud.deleted, out)
		}
		// …but only THAT cluster: a deleted job does not vouch for another name.
		job := deletedJob()
		job.DeletedName = "other"
		inst2, _ := pfactory.RecordClusterInstall("aws", "ap-southeast-1", "adhar")
		cloud.add("vol-2", inst2.ID, false)
		step, out = testStep(cloud, stepOpts{yes: true})
		step.run([]volumeJob{job})
		if strings.Join(cloud.deleted, ",") != "vol-1" {
			t.Fatalf("a deleted job vouched for a cluster it did not delete: %v\n%s", cloud.deleted, out)
		}
	})
}

// The teardown writes down that the cluster is gone the moment DeleteCluster
// succeeds, BEFORE the volume step — so a teardown cut short in between (a
// crash, a Ctrl+C) is finished by a re-run under --yes, while a cluster the CLI
// never saw deleted is not.
func TestADeletedClusterIsRecordedGoneBeforeTheVolumeStep(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	inst, err := pfactory.RecordClusterInstall("digitalocean", "blr1", "adhar")
	if err != nil {
		t.Fatal(err)
	}
	cloud := newFakeCloud()
	cloud.add("vol-1", inst.ID, false)
	p := &fakeCloudProvider{fakeCloud: cloud, region: "blr1"}
	r := teardownRun{file: writeConfig(t, "compute", false), env: "dev", clusters: foundCluster(p, "adhar-cluster-adhar"), installID: inst.ID}.run(t)
	if r.err != nil || len(p.deletedIDs) != 1 {
		t.Fatalf("err=%v deletes=%v", r.err, p.deletedIDs)
	}
	recs, _ := pfactory.LoadClusterInstalls("digitalocean", "blr1", "adhar")
	if len(recs) != 1 || recs[0].Active() {
		t.Fatalf("after DeleteCluster the record is %+v; it must say the cluster was seen deleted", recs)
	}
	if len(r.outcome.VolumeJobs) != 1 || r.outcome.VolumeJobs[0].DeletedName != "adhar" {
		t.Fatalf("jobs = %+v", r.outcome.VolumeJobs)
	}

	// The process dies here, before the volume step. The re-run finds no
	// cluster, and finishes the job under --yes.
	rerun := teardownRun{file: writeConfig(t, "compute", false), env: "dev", clusters: map[string]*helpers.ResolvedCluster{}}.run(t)
	if rerun.err != nil || len(rerun.outcome.VolumeJobs) != 1 || rerun.outcome.VolumeJobs[0].ClusterDeleted {
		t.Fatalf("re-run: err=%v jobs=%+v", rerun.err, rerun.outcome.VolumeJobs)
	}
	if ok, out := volumeStepFor(cloud, rerun.outcome.VolumeJobs); !ok || strings.Join(cloud.deleted, ",") != "vol-1" {
		t.Fatalf("the re-run did not finish the job: %v\n%s", cloud.deleted, out)
	}
}

// A teardown that found no cluster still runs the volume step for it, which may
// delete volumes. Its summary must not say "Nothing was deleted" above a list
// of deleted volumes.
func TestAGoneClustersReportDoesNotClaimNothingWasDeleted(t *testing.T) {
	withJobs := teardownOutcome{Absent: []string{"dev"}, VolumeJobs: []volumeJob{absentJob()}}
	if got := absentHeadline(withJobs); strings.Contains(got, "Nothing was deleted") || !strings.Contains(got, "No cluster was deleted") {
		t.Fatalf("headline = %q; a volume step follows, so it may not claim nothing was deleted", got)
	}
	if got := absentHeadline(teardownOutcome{}); got != "Nothing was deleted" {
		t.Fatalf("headline with nothing to follow = %q", got)
	}
}

// A piped "yes" is not a person. The step reads "interactive" from stdin being
// a terminal, and every confirmation --yes does not cover (legacy volumes,
// volumes kept on purpose, a cluster nobody saw deleted) depends on it: a pipe
// must never pass for one.
func TestAPipeOnStdinIsNeverATerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = prev
		_ = r.Close()
		_ = w.Close()
	})
	if s := newVolumeStep(nil, nil); s.interactive {
		t.Fatal("a pipe on stdin was taken for a terminal")
	}
}
