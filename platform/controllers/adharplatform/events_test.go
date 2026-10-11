package adharplatform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/platform/events"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// collectEvents stands in for the Kafka bridge and returns what was delivered.
func collectEvents(t *testing.T) (*events.Publisher, func(want int) []events.Event) {
	t.Helper()
	var mu sync.Mutex
	var got []events.Event
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Records []struct {
				Value events.Event `json:"value"`
			} `json:"records"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		for _, rec := range body.Records {
			got = append(got, rec.Value)
		}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	p := events.NewPublisher(events.Options{BridgeURL: srv.URL, Source: "/adhar/test/controller"})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		p.Close(ctx)
	})
	// Waits for an EXPECTED COUNT, not for "something". Delivery is
	// asynchronous, so a helper that returns as soon as one event lands reads a
	// partial batch and the assertion that follows is a coin toss.
	return p, func(want int) []events.Event {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			n := len(got)
			mu.Unlock()
			if n >= want {
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		out := make([]events.Event, len(got))
		copy(out, got)
		return out
	}
}

// A reconcile publishes one event per condition that CHANGED, and nothing on a
// pass where nothing changed. The second half is the point: the reconcile runs
// every 30 seconds for the life of the cluster, so republishing the condition
// set each pass would put tens of thousands of identical events a day onto a
// topic whose only value is that reading it tells you what happened.
func TestOnlyChangedConditionsArePublished(t *testing.T) {
	bus, delivered := collectEvents(t)
	r := &AdharPlatformReconciler{Events: bus}

	resource := &v1alpha1.AdharPlatform{}
	resource.SetName("prod")
	resource.Status.ArgoCD.Available = true

	first := syncConditions(resource, "", "")
	if len(first) == 0 {
		t.Fatal("the first pass reported no transitions")
	}
	r.publishTransitions(context.Background(), resource.GetName(), first)

	// Nothing changed on the second pass.
	second := syncConditions(resource, "", "")
	if len(second) != 0 {
		var types []string
		for _, tr := range second {
			types = append(types, tr.Type)
		}
		t.Errorf("an unchanged pass reported transitions: %v", types)
	}

	got := delivered(len(first))
	if len(got) != len(first) {
		t.Fatalf("published %d event(s) for %d transition(s)", len(got), len(first))
	}
	var sawArgoReady, sawPlatform bool
	for _, e := range got {
		if err := e.Validate(); err != nil {
			t.Errorf("published an invalid CloudEvent: %v", err)
		}
		if e.Type == events.TypeComponentReady && e.Subject == "prod/argocd" {
			sawArgoReady = true
		}
		if e.Type == events.TypePlatformFailed && e.Subject == "prod" {
			sawPlatform = true
		}
	}
	if !sawArgoReady {
		t.Error("no component.ready event for argocd, whose condition went True")
	}
	// The aggregate is False (only one component is up), so the platform event
	// must be the FAILED one — a platform that is not ready must not be
	// announced as reconciled.
	if !sawPlatform {
		t.Error("no platform.failed event for the aggregate Ready condition")
	}
}

// The aggregate and the components answer different questions, so they must be
// different event types: an operator watching for "is the platform up" should
// not have to filter five component events to find it.
func TestTheAggregateConditionBecomesAPlatformEvent(t *testing.T) {
	bus, delivered := collectEvents(t)
	r := &AdharPlatformReconciler{Events: bus}

	resource := &v1alpha1.AdharPlatform{}
	resource.SetName("prod")
	resource.Status.ArgoCD.Available = true
	resource.Status.Gateway.Available = true
	resource.Status.Gitea.Available = true
	resource.Status.Gitea.RepositoriesCreated = true
	resource.Status.Crossplane.Available = true
	resource.Status.Crossplane.ControlPlaneApplied = true

	// Wait for EVERY event this publishes, not just one.
	//
	// A ready platform transitions the aggregate AND each component, and
	// publishTransitions emits one event per transition — so delivered(1)
	// returned as soon as the first of them landed, which is usually a
	// COMPONENT event. The platform event was then simply not in the slice yet
	// and the count below read 0. Flaky roughly one run in three under load,
	// and the helper's own comment warns about exactly this: "a helper that
	// returns as soon as one event lands reads a partial batch and the
	// assertion that follows is a coin toss."
	//
	// Counting the transitions rather than hardcoding a number keeps this
	// correct when a condition is added, and matches the test above.
	transitions := syncConditions(resource, "", "")
	r.publishTransitions(context.Background(), "prod", transitions)

	var platformEvents int
	for _, e := range delivered(len(transitions)) {
		switch e.Type {
		case events.TypePlatformReconciled:
			platformEvents++
			if e.Subject != "prod" {
				t.Errorf("platform event subject is %q, want the platform name", e.Subject)
			}
		case events.TypePlatformFailed:
			t.Error("a fully ready platform published platform.failed")
		}
	}
	if platformEvents != 1 {
		t.Errorf("published %d platform.reconciled event(s), want exactly 1", platformEvents)
	}
}

// Publishing is advisory: a controller with no bus must reconcile exactly as
// before. This is the normal state on a local `adhar up`.
func TestAControllerWithNoBusStillReconciles(t *testing.T) {
	r := &AdharPlatformReconciler{} // Events is nil
	resource := &v1alpha1.AdharPlatform{}
	resource.SetName("local")
	transitions := syncConditions(resource, "boom", "it broke")
	// Must not panic, and must leave the conditions written.
	r.publishTransitions(context.Background(), "local", transitions)
	if len(resource.Status.Conditions) == 0 {
		t.Fatal("conditions were not written")
	}
	var ready *metav1.Condition
	for i := range resource.Status.Conditions {
		if resource.Status.Conditions[i].Type == ConditionReady {
			ready = &resource.Status.Conditions[i]
		}
	}
	if ready == nil || ready.Message != "it broke" {
		t.Errorf("the failure message did not reach the Ready condition: %+v", ready)
	}
}
