package events

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	platformevents "adhar-io/adhar/platform/events"
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
	deleted    bool
}

func (f *fakeBridge) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/consumers/adhar-cli":
			var req createConsumerRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			f.offset = req.AutoOffsetReset
			_ = json.NewEncoder(w).Encode(createConsumerResponse{InstanceID: req.Name, BaseURI: "http://" + r.Host + "/consumers/adhar-cli/instances/" + req.Name})
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
}
