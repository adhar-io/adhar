package events

import (
	"os"
	"strings"
	"testing"
	"time"

	platformevents "adhar-io/adhar/platform/events"
)

func reset() {
	flagDomains, flagTypes, flagSubject = nil, nil, ""
}

// --domain picks topics; an unknown one must be reported with the list of real
// ones rather than silently reading nothing.
func TestDomainSelectionMapsToTopics(t *testing.T) {
	reset()
	defer reset()

	flagDomains = nil
	all, err := topicsToRead()
	if err != nil || len(all) != len(platformevents.Topics) {
		t.Fatalf("no --domain should read every topic, got %v (%v)", all, err)
	}

	flagDomains = []string{"cluster", "adhar.tenancy"}
	got, err := topicsToRead()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Accepts both the bare domain and the full topic name: a user who read the
	// topic out of `adhar events topics` should not have to strip the prefix.
	if len(got) != 2 || got[0] != platformevents.TopicCluster || got[1] != platformevents.TopicTenancy {
		t.Errorf("topicsToRead() = %v, want the cluster and tenancy topics", got)
	}

	flagDomains = []string{"nosuch"}
	if _, err := topicsToRead(); err == nil {
		t.Error("an unknown domain was accepted")
	}
}

// Asking for a TYPE should read only the topic that carries it. Reading five
// topics to display one type is five consumer group members for nothing.
func TestTypeSelectionNarrowsTheTopics(t *testing.T) {
	reset()
	defer reset()
	flagTypes = []string{platformevents.TypeScaleRefused, platformevents.TypeNodeAdded}
	got, err := topicsToRead()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != platformevents.TopicCluster {
		t.Errorf("two cluster types should read one topic, got %v", got)
	}
	flagTypes = []string{"com.example.not.ours"}
	if _, err := topicsToRead(); err == nil {
		t.Error("a foreign event type was accepted")
	}
}

func TestFiltersSelectTheRightEvents(t *testing.T) {
	reset()
	defer reset()
	now := time.Now()
	e := platformevents.Event{
		Type:    platformevents.TypeComponentReady,
		Subject: "prod/argocd",
		Time:    platformevents.Timestamp(now),
	}

	if !matches(e, time.Time{}) {
		t.Error("an event was rejected with no filters set")
	}

	flagSubject = "argocd"
	if !matches(e, time.Time{}) {
		t.Error("--subject should match a substring of the subject")
	}
	flagSubject = "gitea"
	if matches(e, time.Time{}) {
		t.Error("--subject matched an unrelated subject")
	}
	flagSubject = ""

	flagTypes = []string{platformevents.TypePackageDegraded}
	if matches(e, time.Time{}) {
		t.Error("--type matched an event of a different type")
	}
	flagTypes = nil

	// --since: older events out, newer in.
	if matches(e, now.Add(time.Hour)) {
		t.Error("an event older than the cutoff was kept")
	}
	if !matches(e, now.Add(-time.Hour)) {
		t.Error("an event newer than the cutoff was dropped")
	}

	// An unparseable timestamp must be SHOWN, not hidden: a filter that
	// silently drops malformed events hides exactly the ones worth seeing.
	broken := e
	broken.Time = "not-a-time"
	if !matches(broken, now.Add(time.Hour)) {
		t.Error("an event with an unparseable time was hidden by --since")
	}
}

// The summary column reads the data generically, so a type added after this was
// written still shows something useful.
func TestSummaryPrefersTheFieldAnOperatorReadsFirst(t *testing.T) {
	cases := []struct{ data, want string }{
		{`{"error":"tracker lacks network resources","succeeded":false}`, "tracker lacks network resources"},
		{`{"succeeded":false}`, "failed"},
		{`{"component":"argocd","ready":true}`, "component=argocd ready=true"},
		{`{}`, ""},
		{`not json`, ""},
	}
	for _, c := range cases {
		got := summarise(platformevents.Event{Data: []byte(c.data)})
		if got != c.want {
			t.Errorf("summarise(%s) = %q, want %q", c.data, got, c.want)
		}
	}
}

// The bridge serves /healthy and /ready on a SEPARATE management port that the
// Service does not expose; on the REST port they return 501. A probe written
// against /healthy therefore reports a broken bridge on a healthy one — which
// is exactly what happened on 2026-10-11, where the live bridge answered
// `GET /healthy -> 501` while being perfectly able to serve.
func TestTheBridgeProbeDoesNotUseTheManagementPortPaths(t *testing.T) {
	b, err := os.ReadFile("reach.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	// Only the request path matters; the comment explains the trap and may name it.
	if strings.Contains(s, `http.MethodGet, base+"/healthy"`) {
		t.Error("the probe requests /healthy, which is on the bridge's management port and " +
			"returns 501 on the REST port the Service exposes")
	}
	if !strings.Contains(s, `http.MethodGet, base+"/topics"`) {
		t.Error("the probe should ask /topics, which is on the REST port and also fails when " +
			"the bridge cannot reach the broker")
	}
}
