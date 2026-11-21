package batch

// The run-summary archive under the conditions it has to survive (design 0025
// Phase 4, A4.20 and A4.22): a store that hangs and then refuses must not hold
// up a reconcile and must be counted; a finished run observed again by a
// restarted observer, or deleted by its TTL afterwards, is not archived again;
// and what reaches the object store masks sensitive parameters exactly as the
// bus does (D8) — the archive keeps a summary for years, so a value that
// slipped through here would outlive every rotation of it.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"adhar-io/adhar/platform/batcharchive"
	"adhar-io/adhar/platform/batcharchive/batcharchivetest"
	"adhar-io/adhar/platform/events"
)

func rootCreds() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: batcharchive.CredentialSecret, Namespace: "adhar-system"},
		Data:       map[string][]byte{batcharchive.CredentialAccessKey: []byte("rustfsadmin"), batcharchive.CredentialSecretKey: []byte("s3cret")},
	}
}

// outcomes collects a writer's results, so a test can wait for the n-th.
type outcomes struct {
	mu  sync.Mutex
	got []batcharchive.Result
}

func (o *outcomes) record(r batcharchive.Result) {
	CountArchive(r)
	o.mu.Lock()
	o.got = append(o.got, r)
	o.mu.Unlock()
}

func (o *outcomes) wait(t *testing.T, n int, within time.Duration) []batcharchive.Result {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		o.mu.Lock()
		out := append([]batcharchive.Result(nil), o.got...)
		o.mu.Unlock()
		if len(out) >= n || time.Now().After(deadline) {
			return out
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// finishRun drives one run from running to succeeded across two reconciles
// (the first is the job's baseline), returning the finished copy.
func finishRun(t *testing.T, c client.Client, r *Reconciler, run *unstructured.Unstructured) {
	t.Helper()
	reconcileOnce(t, r)
	done := runPod("p1", run, t0, podState{phase: "Succeeded", exitCode: code(0), message: okSummary})
	finished := run.DeepCopy()
	setStatus(finished, "active", int64(0))
	setCond(finished, "Complete", "CompletionsReached", "2026-11-03T17:43:40Z")
	update(t, c, &done, finished)
}

// A4.22: RustFS hangs, then refuses. The reconcile that ends the run returns
// at once with the run published and recorded; the write is counted as failed
// under the run's environment once the store answers.
func TestAStoreThatIsDownNeverHoldsUpTheReconcile(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`<Error><Code>ServiceUnavailable</Code></Error>`))
	}))
	defer store.Close()
	defer unblock()
	t.Setenv(batcharchive.EnvURL, store.URL)

	run := runJob("importer-09932cb3", "uid-1", "manual", map[string]string{"businessDate": "2026-11-03"})
	setStatus(run, "active", int64(1))
	running := runPod("p1", run, t0, podState{phase: "Running", running: true})
	c := fakeCluster(t, run.DeepCopy(), running.DeepCopy(), rootCreds())
	w := NewArchive(c, "adhar-system")
	if w == nil {
		t.Fatal("no archive with ADHAR_BATCH_ARCHIVE_URL set")
	}
	defer w.Close(context.Background())
	got := &outcomes{}
	w.OnResult = got.record
	pub := &recorded{}
	r := &Reconciler{Client: c, Runs: c, APIReader: c, Events: pub, Archive: w, Now: func() time.Time { return t0.Add(time.Hour) }}
	finishRun(t, c, r, run)

	failedBefore := testutil.ToFloat64(archiveWrites.WithLabelValues("", string(batcharchive.Failed)))
	pub.reset()
	start := time.Now()
	reconcileOnce(t, r)
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("the reconcile waited %s on a store that does not answer; the archive is advisory", took)
	}
	want(t, "finished", pub.types(), events.TypeBatchRunSucceeded)
	if res := got.wait(t, 1, 50*time.Millisecond); len(res) != 0 {
		t.Fatalf("the write reported before the store answered: %+v", res)
	}

	unblock()
	res := got.wait(t, 1, 5*time.Second)
	if len(res) != 1 || res[0].Outcome != batcharchive.Failed || res[0].Key != "prod/importer/2026-11-03/importer-09932cb3.json" {
		t.Fatalf("results = %+v; want one failed write of the run's key", res)
	}
	if after := testutil.ToFloat64(archiveWrites.WithLabelValues("", string(batcharchive.Failed))); after != failedBefore+1 {
		t.Errorf("adhar_batch_archive_writes_total{result=failed} went %v -> %v; a refused write must be counted", failedBefore, after)
	}

	// The record still says the run ended: the next reconcile neither
	// republishes nor re-archives it because the store refused.
	pub.reset()
	reconcileOnce(t, r)
	if len(pub.types()) != 0 {
		t.Errorf("a refused archive write made the observer republish: %v", pub.types())
	}
	if res := got.wait(t, 2, 100*time.Millisecond); len(res) != 1 {
		t.Errorf("a refused archive write was retried by the next reconcile: %+v", res)
	}
}

// A4.20: the record, not the observer's memory, says a run was archived. A
// restarted observer (a fresh Reconciler over the same cluster) looking at a
// finished run, and the run's Job later deleted by its TTL, archive nothing.
func TestARunObservedAgainAfterARestartIsNotArchivedTwice(t *testing.T) {
	run := runJob("importer-09932cb3", "uid-1", "manual", map[string]string{"businessDate": "2026-11-03"})
	setStatus(run, "active", int64(1))
	running := runPod("p1", run, t0, podState{phase: "Running", running: true})
	c := fakeCluster(t, run.DeepCopy(), running.DeepCopy())
	clock := func() time.Time { return t0.Add(time.Hour) }
	arc := &recordedArchive{}
	r := &Reconciler{Client: c, Runs: c, APIReader: c, Events: &recorded{}, Archive: arc, Now: clock}
	finishRun(t, c, r, run)
	reconcileOnce(t, r)
	if got := arc.take(); len(got) != 1 {
		t.Fatalf("archived %d entries for the finished run, want 1", len(got))
	}

	restarted := &Reconciler{Client: c, Runs: c, APIReader: c, Events: &recorded{}, Archive: arc, Now: clock}
	reconcileOnce(t, restarted)
	if got := arc.take(); len(got) != 0 {
		t.Fatalf("a restarted observer archived a run that had already ended: %+v", got)
	}

	gone := run.DeepCopy()
	if err := c.Delete(context.Background(), gone); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, restarted)
	reconcileOnce(t, restarted)
	if got := arc.take(); len(got) != 0 {
		t.Fatalf("a finished run deleted by its TTL was archived again (as lost?): %+v", got)
	}
}

// D8 at rest: the object written to the store carries the business date, and
// neither a value the template marks sensitive nor an undeclared one whose
// name reads as a credential. Asserted on the BYTES in the store, because
// that is what outlives the topic.
func TestAnArchivedSummaryMasksSensitiveParameters(t *testing.T) {
	store := batcharchivetest.New("rustfsadmin", "s3cret", batcharchive.DefaultBucket)
	defer store.Close()
	t.Setenv(batcharchive.EnvURL, store.URL())

	params := map[string]string{"businessDate": "2026-11-03", "account": "acme-42-confidential", "apiToken": "tok-do-not-archive", "region": "ap-southeast-1"}
	run := runJob("importer-09932cb3", "uid-1", "manual", params)
	setStatus(run, "active", int64(1))
	running := runPod("p1", run, t0, podState{phase: "Running", running: true})
	template := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "importer-run", Namespace: "prod"},
		Data: map[string]string{"parameters.json": `[{"name":"businessDate","type":"date"},` +
			`{"name":"account","type":"string","sensitive":true},{"name":"region","type":"string"}]`},
	}
	c := fakeCluster(t, run.DeepCopy(), running.DeepCopy(), template, rootCreds())
	w := NewArchive(c, "adhar-system")
	if w == nil {
		t.Fatal("no archive with ADHAR_BATCH_ARCHIVE_URL set")
	}
	defer w.Close(context.Background())
	got := &outcomes{}
	w.OnResult = got.record
	r := &Reconciler{Client: c, Runs: c, APIReader: c, Events: &recorded{}, Archive: w, Now: func() time.Time { return t0.Add(time.Hour) }}
	finishRun(t, c, r, run)
	reconcileOnce(t, r)
	if res := got.wait(t, 1, 5*time.Second); len(res) != 1 || res[0].Outcome != batcharchive.Written {
		t.Fatalf("results = %+v; want the summary written", res)
	}

	body := string(store.Object(batcharchive.DefaultBucket, "prod/importer/2026-11-03/importer-09932cb3.json"))
	if body == "" {
		t.Fatal("the run's summary is not in the store")
	}
	for _, secret := range []string{"acme-42-confidential", "tok-do-not-archive"} {
		if strings.Contains(body, secret) {
			t.Errorf("the archived summary carries the sensitive value %q (D8):\n%s", secret, body)
		}
	}
	for _, kept := range []string{`"businessDate":"2026-11-03"`, `"region":"ap-southeast-1"`, `"account":"••••••"`, `"apiToken":"••••••"`} {
		if !strings.Contains(body, kept) {
			t.Errorf("the archived summary lacks %s:\n%s", kept, body)
		}
	}
}
