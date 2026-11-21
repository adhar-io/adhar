package batch

// The run-summary archive (design 0025 Phase 4, A4.20–A4.23): each finished
// run's final event, the SAME event the bus carries, at a key computed from
// the run; attempts that are not the end of a run are not archived; a
// re-observed run is not archived twice; the credential is read live.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"adhar-io/adhar/platform/batcharchive"
	"adhar-io/adhar/platform/batcharchive/batcharchivetest"
	"adhar-io/adhar/platform/events"
)

type archivedEntry struct {
	key string
	e   events.Event
}

type recordedArchive struct {
	mu  sync.Mutex
	got []archivedEntry
}

func (a *recordedArchive) Archive(_ context.Context, key string, e events.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.got = append(a.got, archivedEntry{key, e})
}

func (a *recordedArchive) take() []archivedEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.got
	a.got = nil
	return out
}

// update replaces objects whole, status included: the fake client keeps a
// Job's and a pod's status behind its status subresource on Update.
func update(t *testing.T, c client.Client, objs ...client.Object) {
	t.Helper()
	for _, o := range objs {
		_ = c.Delete(context.Background(), o.DeepCopyObject().(client.Object))
		o.SetResourceVersion("")
		if err := c.Create(context.Background(), o); err != nil {
			t.Fatal(err)
		}
	}
}

// A4.20: a run's end is archived once, as the event published for it, at
// <namespace>/<batchJob>/<businessDate>/<run>.json. An OOM-killed attempt the
// Job retries is published as run.lost and NOT archived: it is not the end.
func TestAFinishedRunIsArchivedOnceAsItsPublishedEvent(t *testing.T) {
	run := runJob("importer-09932cb3", "uid-1", "manual", map[string]string{"businessDate": "2026-11-03"})
	setStatus(run, "active", int64(1))
	first := runPod("p1", run, t0, podState{phase: "Running", running: true})
	c := fakeCluster(t, run.DeepCopy(), first.DeepCopy())
	pub, arc := &recorded{}, &recordedArchive{}
	r := &Reconciler{Client: c, Runs: c, APIReader: c, Events: pub, Archive: arc, Source: "/adhar/test/batch-observer",
		Now: func() time.Time { return t0.Add(time.Hour) }}

	reconcileOnce(t, r)
	if got := arc.take(); len(got) != 0 {
		t.Fatalf("a running run was archived: %+v", got)
	}

	killed := runPod("p1", run, t0, podState{phase: "Failed", exitCode: code(137), reason: "OOMKilled"})
	replacement := runPod("p2", run, t0.Add(time.Minute), podState{phase: "Pending"})
	update(t, c, killed.DeepCopy(), replacement.DeepCopy())
	pub.reset()
	reconcileOnce(t, r)
	want(t, "oom", pub.types(), events.TypeBatchRunLost)
	if got := arc.take(); len(got) != 0 {
		t.Fatalf("an attempt the Job retries was archived as the run's end: %+v", got)
	}

	done := runPod("p2", run, t0.Add(time.Minute), podState{phase: "Succeeded", exitCode: code(0), message: okSummary})
	finished := run.DeepCopy()
	setStatus(finished, "active", int64(0))
	setCond(finished, "Complete", "CompletionsReached", "2026-11-03T17:43:40Z")
	update(t, c, done.DeepCopy(), finished)
	pub.reset()
	reconcileOnce(t, r)
	want(t, "finished", pub.types(), events.TypeBatchRunSucceeded)
	got := arc.take()
	if len(got) != 1 {
		t.Fatalf("archived %d entries for one finished run, want 1", len(got))
	}
	if got[0].key != "prod/importer/2026-11-03/importer-09932cb3.json" {
		t.Errorf("key = %q", got[0].key)
	}
	sent := pub.got[0]
	if got[0].e.ID == "" || got[0].e.ID != sent.ID || got[0].e.Time != sent.Time || got[0].e.Source != "/adhar/test/batch-observer" ||
		string(got[0].e.Data) != string(sent.Data) {
		t.Errorf("the archived event is not the published one:\narchived %+v\npublished %+v", got[0].e, sent)
	}
	if err := got[0].e.Validate(); err != nil {
		t.Errorf("the archived event is not a valid CloudEvent: %v", err)
	}
	var d eventData
	_ = json.Unmarshal(got[0].e.Data, &d)
	if d.Counts == nil || d.Counts.Read == nil || *d.Counts.Read != 120000 || d.SpringBatch == nil || d.SpringBatch.ExecutionID == nil {
		t.Errorf("the archived summary lost the run's counts or execution: %s", got[0].e.Data)
	}

	pub.reset()
	reconcileOnce(t, r)
	if got := arc.take(); len(got) != 0 {
		t.Fatalf("looking again at a finished run archived it again: %+v", got)
	}
}

// A4.20: a run deleted before it ended has no other end; its run.lost is
// archived. The record holds no business date for a run that never finished,
// so the key's date segment is `undated`.
func TestAVanishedRunIsArchivedAsLost(t *testing.T) {
	active := runJob("importer-09932cb3", "uid-1", "manual", map[string]string{"businessDate": "2026-11-03"})
	setStatus(active, "active", int64(1))
	c := fakeCluster(t, active.DeepCopy())
	arc := &recordedArchive{}
	r := &Reconciler{Client: c, Runs: c, APIReader: c, Events: &recorded{}, Archive: arc, Now: func() time.Time { return t0.Add(time.Hour) }}
	reconcileOnce(t, r)
	if err := c.Delete(context.Background(), active.DeepCopy()); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r)
	got := arc.take()
	if len(got) != 1 || got[0].e.Type != events.TypeBatchRunLost || got[0].key != "prod/importer/undated/importer-09932cb3.json" {
		t.Fatalf("archived %+v; want the run.lost at the undated key", got)
	}
}

// A4.22: the manager's archive is off without ADHAR_BATCH_ARCHIVE_URL, and
// with it reads the credential LIVE — a Secret published after the manager
// started (RustFS comes up after the bootstrap) is used without a restart.
func TestTheArchiveReadsItsCredentialLive(t *testing.T) {
	t.Setenv(batcharchive.EnvURL, "")
	c := fakeCluster(t)
	if NewArchive(c, "adhar-system") != nil {
		t.Fatal("the archive is on with no ADHAR_BATCH_ARCHIVE_URL; it must be opt-in")
	}

	store := batcharchivetest.New("rustfsadmin", "s3cret", batcharchive.DefaultBucket)
	defer store.Close()
	t.Setenv(batcharchive.EnvURL, store.URL())
	w := NewArchive(c, "adhar-system")
	if w == nil {
		t.Fatal("ADHAR_BATCH_ARCHIVE_URL is set and there is no archive")
	}
	defer w.Close(context.Background())
	var mu sync.Mutex
	var outcomes []batcharchive.Outcome
	w.OnResult = func(r batcharchive.Result) {
		CountArchive(r)
		mu.Lock()
		outcomes = append(outcomes, r.Outcome)
		mu.Unlock()
	}
	seen := func(n int) []batcharchive.Outcome {
		deadline := time.Now().Add(5 * time.Second)
		for {
			mu.Lock()
			out := append([]batcharchive.Outcome(nil), outcomes...)
			mu.Unlock()
			if len(out) >= n || time.Now().After(deadline) {
				return out
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	e := events.Event{SpecVersion: "1.0", ID: "x", Source: "/s", Type: events.TypeBatchRunSucceeded, Time: "2026-11-03T17:43:40Z", Subject: "prod/importer"}

	w.Archive(context.Background(), "prod/importer/2026-11-03/a.json", e)
	if got := seen(1); len(got) != 1 || got[0] != batcharchive.Failed {
		t.Fatalf("with no Secret yet: %v, want one failed write", got)
	}
	update(t, c, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "root-creds", Namespace: "adhar-system"},
		Data:       map[string][]byte{"rootUser": []byte("rustfsadmin"), "rootPassword": []byte("s3cret")},
	})
	w.Archive(context.Background(), "prod/importer/2026-11-03/a.json", e)
	if got := seen(2); len(got) != 2 || got[1] != batcharchive.Written {
		t.Fatalf("after the Secret appeared: %v, want the write to land", got)
	}
	if store.Object(batcharchive.DefaultBucket, "prod/importer/2026-11-03/a.json") == nil {
		t.Fatal("the object is not in the store")
	}
}

// A4.22: the ONE place that switches the archive on names the store, and the
// store's bucket Job makes the bucket the observer writes to. A typo in
// either is an archive that is silently off, or one whose first write of
// every run pays a bucket creation.
func TestTheManagerArchivesToTheBucketRustFSMakes(t *testing.T) {
	manager, err := os.ReadFile(filepath.Join("..", "resources", "manager", "manager.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)- name: ` + batcharchive.EnvURL + `\s*\n\s*value: (\S+)`).FindStringSubmatch(string(manager))
	if m == nil || m[1] != batcharchive.DefaultEndpoint {
		t.Errorf("the manager Deployment does not set %s to %s (got %v)", batcharchive.EnvURL, batcharchive.DefaultEndpoint, m)
	}
	if strings.Contains(string(manager), batcharchive.EnvCredentialSecret) || strings.Contains(string(manager), "name: root-creds") {
		t.Error("the manager Deployment wires the archive credential into its env; it is read live, because the manager starts before RustFS publishes it")
	}
	buckets, err := os.ReadFile(filepath.Join("..", "..", "stack", "packages", "data", "rustfs", "manifests", "buckets.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	b := regexp.MustCompile(`(?m)- name: BUCKETS\s*\n\s*value: "([^"]*)"`).FindStringSubmatch(string(buckets))
	if b == nil || !slices.Contains(strings.Fields(b[1]), batcharchive.DefaultBucket) {
		t.Errorf("rustfs-buckets does not make %s (BUCKETS = %v)", batcharchive.DefaultBucket, b)
	}
}
