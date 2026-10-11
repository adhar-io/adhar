package events

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// waitFor polls until cond holds or the deadline passes. The publisher is
// asynchronous by design, so a test either waits on a condition or sleeps — and
// a sleep long enough to be reliable is long enough to be slow.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The happy path, end to end: what the bridge actually receives.
func TestAnEventReachesTheBridgeAsACloudEvent(t *testing.T) {
	type received struct {
		path        string
		contentType string
		body        []byte
	}
	var mu sync.Mutex
	var got []received
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		mu.Lock()
		got = append(got, received{r.URL.Path, r.Header.Get("Content-Type"), b})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := NewPublisher(Options{BridgeURL: srv.URL, Source: "/adhar/prod/test"})
	defer p.Close(context.Background())

	data, _ := json.Marshal(map[string]string{"node": "adhar-worker-3"})
	p.Publish(context.Background(), Event{
		Type:            TypeNodeAdded,
		Subject:         "adhar/adhar-worker-3",
		DataContentType: "application/json",
		Data:            data,
		Organisation:    "adhar",
		Team:            "platform",
		Environment:     "prod",
	})
	waitFor(t, "the event to be delivered", func() bool { return p.Stats().Published == 1 })

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("bridge received %d request(s), want 1", len(got))
	}
	// Routed by DOMAIN: a cluster event goes to the cluster topic.
	if want := "/topics/" + TopicCluster; got[0].path != want {
		t.Errorf("posted to %q, want %q", got[0].path, want)
	}
	var env bridgeRecords
	if err := json.Unmarshal(got[0].body, &env); err != nil {
		t.Fatalf("bridge body is not a records envelope: %v (%s)", err, got[0].body)
	}
	if len(env.Records) != 1 {
		t.Fatalf("envelope carries %d record(s), want 1", len(env.Records))
	}
	ev := env.Records[0].Value
	// The partition key is the subject, so events about one object stay ordered.
	if env.Records[0].Key != "adhar/adhar-worker-3" {
		t.Errorf("record key is %q, want the event subject", env.Records[0].Key)
	}
	// Everything the spec requires was filled in by the publisher, not the caller.
	if ev.SpecVersion != SpecVersion || ev.ID == "" || ev.Time == "" {
		t.Errorf("publisher did not complete the envelope: %+v", ev)
	}
	if ev.Source != "/adhar/prod/test" {
		t.Errorf("source is %q, want the publisher's", ev.Source)
	}
	if err := ev.Validate(); err != nil {
		t.Errorf("the delivered event is not a valid CloudEvent: %v", err)
	}
}

// The rule the whole package exists to keep: a broken bus must not slow a
// reconcile down, and must never report an error into it.
func TestPublishNeverBlocksOnAHungBridge(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // hang until the test lets go
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// A short per-attempt timeout, so draining on Close does not spend the
	// default five seconds per stuck request. The point under test is that
	// PUBLISH does not wait, not how patient delivery is.
	p := NewPublisher(Options{
		BridgeURL: srv.URL, Source: "/adhar/test", QueueSize: 4,
		Timeout: 50 * time.Millisecond,
	})
	// Order matters: let the handler go BEFORE Close drains, or every drained
	// event waits out its own timeout against a server nobody will answer.
	defer func() {
		close(release)
		p.Close(context.Background())
	}()

	// Far more events than the queue holds, against a bridge that answers none.
	start := time.Now()
	for i := 0; i < 500; i++ {
		p.Publish(context.Background(), Event{Type: TypeNodeAdded, Subject: "n"})
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("500 publishes against a hung bridge took %s; Publish must not wait for Kafka", elapsed)
	}
	// Memory is bounded: the queue cannot have grown past its ceiling.
	if n := len(p.queue); n > 4 {
		t.Errorf("queue holds %d events, over its ceiling of 4", n)
	}
	// And the overflow was COUNTED, not silently forgotten.
	if p.Stats().Dropped == 0 {
		t.Error("events were discarded without being counted as dropped")
	}
}

// No bus configured is the normal state on a local `adhar up`. It must be
// indistinguishable from a working one, from the caller's side.
func TestWithNoBusConfiguredPublishIsANoOp(t *testing.T) {
	p := NewPublisher(Options{Source: "/adhar/local/test"})
	defer p.Close(context.Background())
	if p.Enabled() {
		t.Error("a publisher with no bridge URL reports itself enabled")
	}
	p.Publish(context.Background(), Event{Type: TypeNodeAdded, Subject: "n"})
	if s := p.Stats(); s.Dropped != 1 || s.Published != 0 || s.Failed != 0 {
		t.Errorf("stats %+v; want exactly one drop and nothing sent", s)
	}
}

// A malformed event is a bug at the call site. It must not be sent, because a
// consumer validating strictly drops the whole batch it arrives in — so one bad
// event would cost unrelated good ones.
func TestAMalformedEventIsRejectedBeforeItIsSent(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := NewPublisher(Options{BridgeURL: srv.URL, Source: "/adhar/test"})
	defer p.Close(context.Background())

	// A type outside the platform's namespace, and data with no content type.
	p.Publish(context.Background(), Event{Type: "com.example.thing.happened"})
	p.Publish(context.Background(), Event{Type: TypeNodeAdded, Data: json.RawMessage(`{"a":1}`)})

	waitFor(t, "both events to be rejected", func() bool { return p.Stats().Failed == 2 })
	if n := hits.Load(); n != 0 {
		t.Errorf("the bridge was called %d time(s) for malformed events; it must not be called at all", n)
	}
}

// A bridge that answers with an error is a bus problem, not a platform problem:
// counted, and nothing else.
func TestABridgeErrorIsCountedAndSwallowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	p := NewPublisher(Options{BridgeURL: srv.URL, Source: "/adhar/test"})
	defer p.Close(context.Background())
	p.Publish(context.Background(), Event{Type: TypePackageDegraded, Subject: "adhar-system/keycloak"})
	waitFor(t, "the failure to be counted", func() bool { return p.Stats().Failed == 1 })
	if p.Stats().Published != 0 {
		t.Error("a 500 from the bridge was counted as published")
	}
}

// Close must flush: the last events of a run are usually the interesting ones.
func TestCloseDrainsWhatIsAlreadyQueued(t *testing.T) {
	var seen atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	p := NewPublisher(Options{BridgeURL: srv.URL, Source: "/adhar/test", QueueSize: 64})
	for i := 0; i < 20; i++ {
		p.Publish(context.Background(), Event{Type: TypeTeamCreated, Subject: "adhar/platform"})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p.Close(ctx)
	if n := seen.Load(); n != 20 {
		t.Errorf("Close delivered %d of 20 queued events", n)
	}
}

// The correlation id is what makes the stream answerable: every event from one
// reconcile carries the same one, taken from the context so no call site has to
// pass it explicitly.
func TestEveryEventFromOneUnitOfWorkSharesACorrelationID(t *testing.T) {
	var mu sync.Mutex
	var ids []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env bridgeRecords
		_ = json.NewDecoder(r.Body).Decode(&env)
		mu.Lock()
		for _, rec := range env.Records {
			ids = append(ids, rec.Value.Correlation)
		}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := NewPublisher(Options{BridgeURL: srv.URL, Source: "/adhar/test"})
	defer p.Close(context.Background())

	ctx := WithCorrelation(context.Background(), "reconcile-42")
	p.Publish(ctx, Event{Type: TypeComponentReady, Subject: "adhar-system/cilium"})
	p.Publish(ctx, Event{Type: TypeComponentReady, Subject: "adhar-system/argocd"})
	waitFor(t, "both events", func() bool { return p.Stats().Published == 2 })

	mu.Lock()
	defer mu.Unlock()
	for i, id := range ids {
		if id != "reconcile-42" {
			t.Errorf("event %d carries correlation %q, want the context's", i, id)
		}
	}
}

// Every type in the taxonomy must route to a topic the manifests declare. A
// type whose domain has no topic is accepted by the bridge and then
// auto-created with broker defaults — one partition and the wrong retention.
func TestEveryEventTypeRoutesToADeclaredTopic(t *testing.T) {
	all := []string{
		TypePlatformReconciled, TypePlatformFailed, TypeComponentReady, TypeComponentFailed,
		TypeNodeAdded, TypeNodeRemoved, TypeScaleRefused, TypeDataPlaneRegistered, TypeDataPlaneRemoved,
		TypeOrganisationCreated, TypeTeamCreated, TypeProjectCreated,
		TypeRepositoryPushed, TypePackageSynced, TypePackageDegraded, TypeCustomPackageReady,
		TypePipelineSucceeded, TypePipelineFailed,
		TypeAgentRunStarted, TypeAgentRunFinished, TypeAgentRunRefused,
	}
	for _, typ := range all {
		topic, ok := TopicFor(typ)
		if !ok {
			t.Errorf("event type %q routes to no topic", typ)
			continue
		}
		var declared bool
		for _, known := range Topics {
			if known == topic {
				declared = true
			}
		}
		if !declared {
			t.Errorf("event type %q routes to %q, which is not in Topics", typ, topic)
		}
	}
	if _, ok := TopicFor("io.adhar.nosuchdomain.thing.happened"); ok {
		t.Error("an unknown domain was routed instead of reported")
	}
	if _, ok := TopicFor("com.example.thing.happened"); ok {
		t.Error("a foreign event type was routed")
	}
}
