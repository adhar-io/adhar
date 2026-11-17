package events

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"adhar-io/adhar/cmd/helpers"
	platformevents "adhar-io/adhar/platform/events"

	"k8s.io/client-go/rest"
)

// fakeBridge is the Strimzi bridge's consumer protocol, as much of it as a
// reader uses: create a consumer, subscribe, poll records, delete. Every poll
// after the records are handed out returns an empty batch, which is how a
// drained partition answers.
type fakeBridge struct {
	mu         sync.Mutex
	records    [][]record
	subscribed []string
	offset     string
	group      string
	consumer   string // the created consumer's path, the only one that exists
	deleted    bool
}

func (f *fakeBridge) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/consumers/") && strings.Count(r.URL.Path, "/") == 2:
			var req createConsumerRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			f.offset, f.group = req.AutoOffsetReset, strings.TrimPrefix(r.URL.Path, "/consumers/")
			f.consumer = r.URL.Path + "/instances/" + req.Name
			// As the bridge builds it: the Host header it saw and the path it
			// was asked, which behind a proxy is not the path the client used.
			_ = json.NewEncoder(w).Encode(createConsumerResponse{InstanceID: req.Name, BaseURI: "http://" + r.Host + r.URL.Path + "/instances/" + req.Name})
		case f.consumer == "" || !strings.HasPrefix(r.URL.Path, f.consumer):
			t.Errorf("%s %s is not the consumer the bridge created (%s)", r.Method, r.URL.Path, f.consumer)
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/subscription"):
			var req map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&req)
			f.subscribed = req["topics"]
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/records"):
			batch := []record{}
			if len(f.records) > 0 {
				batch, f.records = f.records[0], f.records[1:]
			}
			_ = json.NewEncoder(w).Encode(batch)
		case r.Method == http.MethodDelete:
			f.deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected bridge call %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func batchRecord(t *testing.T, offset int64, typ, subject, at string) record {
	t.Helper()
	e := platformevents.Event{SpecVersion: "1.0", ID: typ + at, Source: "batch-observer", Type: typ, Subject: subject, Time: at}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return record{Topic: platformevents.TopicBatch, Offset: offset, Key: subject, Value: b}
}

// ReadHistory reads the RETAINED stream (earliest offset), keeps what the
// caller's filter and --since keep, returns the newest Limit oldest first,
// reports a record that is not a CloudEvent, and always deletes its consumer.
// An empty batch in the middle of the stream (the first poll after a
// subscribe routinely returns nothing) does not end the read; two do.
func TestReadHistoryReadsTheRetainedStreamAndCleansUp(t *testing.T) {
	prev := historyPollTimeout
	historyPollTimeout = time.Millisecond
	t.Cleanup(func() { historyPollTimeout = prev })

	stream := func() [][]record {
		return [][]record{
			{batchRecord(t, 0, platformevents.TypeBatchRunStarted, "prod/importer", "2026-10-01T01:00:00Z")},
			{},
			{
				batchRecord(t, 1, platformevents.TypeBatchRunSucceeded, "prod/importer", "2026-11-01T01:10:00Z"),
				batchRecord(t, 2, platformevents.TypeBatchRunSucceeded, "prod/importer-v2", "2026-11-01T01:10:00Z"),
				{Topic: platformevents.TopicBatch, Offset: 3, Value: json.RawMessage(`"not an event"`)},
			},
			{
				batchRecord(t, 4, platformevents.TypeBatchRunFailed, "prod/importer", "2026-11-02T01:10:00Z"),
				batchRecord(t, 5, platformevents.TypeBatchRunStarted, "prod/importer", "2026-11-03T01:00:00Z"),
			},
		}
	}
	read := func(limit int) ([]string, []error, *fakeBridge) {
		f := &fakeBridge{records: stream()}
		srv := httptest.NewServer(f.handler(t))
		defer srv.Close()
		got, problems, err := ReadHistory(context.Background(), HistoryQuery{
			Bridge: srv.URL,
			Topics: []string{platformevents.TopicBatch},
			Since:  time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC),
			Keep:   func(e platformevents.Event) bool { return e.Subject == "prod/importer" },
			Limit:  limit,
		})
		if err != nil {
			t.Fatal(err)
		}
		var types []string
		for _, e := range got {
			types = append(types, e.Type)
		}
		return types, problems, f
	}

	all, _, _ := read(0)
	wantAll := []string{platformevents.TypeBatchRunSucceeded, platformevents.TypeBatchRunFailed, platformevents.TypeBatchRunStarted}
	if strings.Join(all, ",") != strings.Join(wantAll, ",") {
		t.Errorf("events = %v, want every prod/importer event since the cut-off, oldest first: %v", all, wantAll)
	}
	types, problems, f := read(2)
	want := wantAll[1:]
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Errorf("events = %v, want the newest two of prod/importer since the cut-off, oldest first: %v", types, want)
	}
	if len(problems) != 1 {
		t.Errorf("a record that is not a CloudEvent is reported, once: %v", problems)
	}
	if f.offset != "earliest" || len(f.subscribed) != 1 || f.subscribed[0] != platformevents.TopicBatch {
		t.Errorf("history reads from the earliest offset of the asked topics: offset %q topics %v", f.offset, f.subscribed)
	}
	if !f.deleted {
		t.Error("the bridge consumer was left behind")
	}
	// Two reads are two consumer groups. In one group Kafka would hand each
	// reader only some of the partitions, and neither would know.
	_, _, other := read(0)
	if f.group == other.group || !strings.HasPrefix(f.group, "adhar-cli-") || !strings.HasPrefix(other.group, "adhar-cli-") {
		t.Errorf("every reader needs a consumer group of its own: %q and %q", f.group, other.group)
	}
}

// The consumer is reached at the path the bridge names for it, at the address
// the bridge was reached by — whatever host, scheme or prefix base_uri carries.
func TestConsumerURIKeepsTheBridgesPathAndTheClientsAddress(t *testing.T) {
	const proxy = "https://api.example:6443" + bridgeProxy
	for _, c := range []struct{ base, baseURI, want string }{
		// A bridge reached directly (--bridge): base_uri is already right.
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080/consumers/g-1/instances/g-1", "http://127.0.0.1:8080/consumers/g-1/instances/g-1"},
		// Behind the API server's proxy: the bridge names the API server's host, its own scheme and no proxy path.
		{proxy, "http://api.example:6443/consumers/g-1/instances/g-1/", proxy + "/consumers/g-1/instances/g-1"},
		// A proxy that does send X-Forwarded-Path: the prefix is not doubled.
		{proxy, proxy + "/consumers/g-1/instances/g-1", proxy + "/consumers/g-1/instances/g-1"},
	} {
		got, err := (&bridgeClient{base: c.base}).consumerURI(c.baseURI)
		if err != nil || got != c.want {
			t.Errorf("consumerURI(%q) from %s = %q %v, want %q", c.baseURI, c.base, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "http://127.0.0.1:8080/", "://"} {
		if got, err := (&bridgeClient{base: "http://127.0.0.1:8080"}).consumerURI(bad); err == nil {
			t.Errorf("consumerURI(%q) = %q, want a refusal", bad, got)
		}
	}
}

const bridgeProxy = "/api/v1/namespaces/adhar-system/services/adhar-events-bridge-service:8080/proxy"

func kubeconfigFor(t *testing.T, server string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	body := "apiVersion: v1\nkind: Config\nclusters:\n- name: c\n  cluster: {server: \"" + server + "\"}\nusers:\n- name: u\n  user: {token: t}\n" +
		"contexts:\n- name: c\n  context: {cluster: c, user: u}\ncurrent-context: c\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A caller that connected to a cluster reads THAT cluster's bridge, through
// its API server's service proxy, and the default kubeconfig's cluster is
// never asked. Behind the proxy the bridge's base_uri has lost the proxy's
// path (the fake answers as the real one does), and the consumer is still
// subscribed, polled and deleted where it lives. A cluster with no bridge says
// so in one sentence instead of failing inside the consumer protocol.
func TestReadHistoryReadsTheBridgeOfTheClusterItIsGiven(t *testing.T) {
	prev, prevPath, prevTransport := historyPollTimeout, helpers.KubeConfigPath, sharedTransport
	historyPollTimeout = time.Millisecond
	t.Cleanup(func() { historyPollTimeout, helpers.KubeConfigPath, sharedTransport = prev, prevPath, prevTransport })

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the default kubeconfig's cluster was asked %s %s; the caller named another", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer other.Close()
	helpers.KubeConfigPath = kubeconfigFor(t, other.URL)

	f := &fakeBridge{records: [][]record{{batchRecord(t, 0, platformevents.TypeBatchRunFailed, "prod/importer", "2026-11-02T01:10:00Z")}}}
	bridge := http.StripPrefix(bridgeProxy, f.handler(t))
	var outside []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case !strings.HasPrefix(r.URL.Path, bridgeProxy+"/"):
			outside = append(outside, r.Method+" "+r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodGet && r.URL.Path == bridgeProxy+"/topics":
			_, _ = w.Write([]byte(`["adhar.batch"]`))
		default:
			bridge.ServeHTTP(w, r)
		}
	}))
	defer api.Close()

	got, _, err := ReadHistory(context.Background(), HistoryQuery{Cluster: &rest.Config{Host: api.URL}, Topics: []string{platformevents.TopicBatch}})
	if err != nil || len(got) != 1 || got[0].Type != platformevents.TypeBatchRunFailed {
		t.Fatalf("the named cluster's events = %v %v", got, err)
	}
	if len(outside) != 0 {
		t.Errorf("requests left the service proxy, where no bridge is: %v", outside)
	}
	if !f.deleted {
		t.Error("the bridge consumer was left behind")
	}

	absent := httptest.NewServer(http.NotFoundHandler())
	defer absent.Close()
	_, _, err = ReadHistory(context.Background(), HistoryQuery{Cluster: &rest.Config{Host: absent.URL}, Topics: []string{platformevents.TopicBatch}})
	if err == nil || !strings.Contains(err.Error(), "no event bridge on this cluster") || !strings.Contains(err.Error(), "opt-in") {
		t.Errorf("a cluster without the bus: %v", err)
	}
}
