package autoscaler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/meta/testrestmapper"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"adhar-io/adhar/api/v1alpha1"
)

// How a tick learns about Volcano gangs (design 0025 §4.7): a discovery check,
// then a plain LIST of PodGroups and Queues. A cluster without Volcano is never
// asked for either, and a Volcano that cannot be read costs the tick its gangs,
// not its decision.

// volcanoReconciler is newReconciler with a client that knows Volcano's kinds.
func volcanoReconciler(t *testing.T, f *fakeScaler, served bool, objs ...client.Object) *Reconciler {
	t.Helper()
	s := testScheme(t)
	volcano := meta.NewDefaultRESTMapper(nil)
	volcano.Add(volcanoSchedulingGV.WithKind("PodGroup"), meta.RESTScopeNamespace)
	volcano.Add(volcanoSchedulingGV.WithKind("Queue"), meta.RESTScopeRoot)
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithRESTMapper(meta.MultiRESTMapper{testrestmapper.TestOnlyStaticRESTMapper(s), volcano}).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.AdharPlatform{}).
		Build()
	r := newReconciler(t, f)
	r.Client = c
	r.VolcanoServed = func(context.Context) bool { return served }
	return r
}

// podGroup is what Volcano's job controller writes for a vcjob: the job's
// labels, a controller reference to it, and the gang's totals.
func podGroup(name string, labels map[string]string, minMember int64, cpu, memory, phase string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{
			"minMember":    minMember,
			"queue":        "acme-payments-prod",
			"minResources": map[string]interface{}{"cpu": cpu, "memory": memory},
		},
		"status": map[string]interface{}{
			"phase": phase,
			"conditions": []interface{}{map[string]interface{}{
				"type":    "Unschedulable",
				"status":  "True",
				"reason":  "NotEnoughResources",
				"message": "2/2 tasks in gang unschedulable: pod group is not ready, 2 Pending, 2 minAvailable; Pending: 2 Unschedulable. Node: settlement-worker-0: 0/1 nodes are unavailable: 1 Insufficient cpu.",
			}},
		},
	}}
	u.SetGroupVersionKind(volcanoSchedulingGV.WithKind("PodGroup"))
	u.SetNamespace("acme-payments-prod")
	u.SetName(name + "-0d7c2c8e-uid")
	u.SetLabels(labels)
	controller := true
	u.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: "batch.volcano.sh/v1alpha1", Kind: "Job", Name: name, UID: "0d7c2c8e-uid", Controller: &controller,
	}})
	return u
}

func volcanoQueue(name, parent, cpu, memory string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{
			"parent":     parent,
			"capability": map[string]interface{}{"cpu": cpu, "memory": memory},
		},
		"status": map[string]interface{}{"allocated": map[string]interface{}{"cpu": "0", "memory": "0"}},
	}}
	u.SetGroupVersionKind(volcanoSchedulingGV.WithKind("Queue"))
	u.SetName(name)
	return u
}

var optedInProd = map[string]string{workloadLabel: "batch", scaleUpLabel: "true", environmentLabel: "prod"}

func volcanoCluster(gang *unstructured.Unstructured) []client.Object {
	cp, w1 := controlPlane(), worker("w1")
	return []client.Object{
		platformWith(&v1alpha1.AutoscalingSpec{Enabled: true, MinWorkers: 1, MaxWorkers: 4}),
		clusterSpecConfigMap(), &cp, &w1, gang,
		volcanoQueue("adhar-batch", "root", "12", "24Gi"),
		volcanoQueue("acme-payments-prod", "adhar-batch", "8", "16Gi"),
	}
}

func TestATickBuysTheWorkersAPendingGangNeeds(t *testing.T) {
	f := &fakeScaler{}
	r := volcanoReconciler(t, f, true, volcanoCluster(podGroup("settlement", optedInProd, 2, "3", "4Gi", "Pending"))...)
	tick(t, r)
	if len(f.upCalls) != 1 || f.lastDesired != 2 {
		t.Fatalf("got %d scale-up(s) to %d workers, want one to 2", len(f.upCalls), f.lastDesired)
	}

	// The same gang from a dev run is read the same way and buys nothing.
	f = &fakeScaler{}
	dev := map[string]string{workloadLabel: "batch", environmentLabel: "dev"}
	r = volcanoReconciler(t, f, true, volcanoCluster(podGroup("settlement", dev, 2, "3", "4Gi", "Pending"))...)
	tick(t, r)
	if len(f.upCalls) != 0 {
		t.Fatalf("a dev gang bought a node: %v", f.upCalls)
	}
}

// listRecorder fails the test on any Volcano read.
type listRecorder struct {
	client.Reader
	t *testing.T
}

func (l listRecorder) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if u, ok := list.(*unstructured.UnstructuredList); ok && u.GroupVersionKind().Group == volcanoSchedulingGV.Group {
		l.t.Fatalf("a cluster that does not serve Volcano was asked for %s", u.GroupVersionKind().Kind)
	}
	return l.Reader.List(ctx, list, opts...)
}

func TestAClusterWithoutVolcanoIsNeverAskedForGangs(t *testing.T) {
	f := &fakeScaler{}
	r := volcanoReconciler(t, f, false, volcanoCluster(podGroup("settlement", optedInProd, 2, "3", "4Gi", "Pending"))...)
	r.APIReader = listRecorder{Reader: r.Client, t: t}
	tick(t, r)
	if len(f.upCalls) != 0 {
		t.Fatalf("gangs counted without Volcano being served: %v", f.upCalls)
	}

	// Discovery off by default: a reconciler with no RESTConfig (every test above
	// this lane, and a manager built without one) decides as it always has.
	plain := newReconciler(t, &fakeScaler{})
	if plain.volcanoServed(context.Background()) {
		t.Fatal("no RESTConfig and no override must read as no Volcano")
	}
}

// failingReader fails every Volcano read.
type failingReader struct{ client.Reader }

func (f failingReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if u, ok := list.(*unstructured.UnstructuredList); ok && u.GroupVersionKind().Group == volcanoSchedulingGV.Group {
		return errors.New("the server was unable to return a response in the time allotted")
	}
	return f.Reader.List(ctx, list, opts...)
}

func TestAVolcanoThatCannotBeReadDoesNotStopTheTick(t *testing.T) {
	f := &fakeScaler{}
	api := pending("api", "0/2 nodes are available: 2 Insufficient cpu.")
	objs := append(volcanoCluster(podGroup("settlement", optedInProd, 2, "3", "4Gi", "Pending")), &api)
	r := volcanoReconciler(t, f, true, objs...)
	r.APIReader = failingReader{Reader: r.Client}
	tick(t, r)
	if len(f.upCalls) != 1 || f.lastDesired != 2 {
		t.Fatalf("the service's scale-up was lost to a Volcano read: %d call(s) to %d", len(f.upCalls), f.lastDesired)
	}
}

func TestPodGroupsAreReadAsVolcanoWritesThem(t *testing.T) {
	g := gangFromPodGroup(podGroup("settlement", optedInProd, 2, "3", "4Gi", "Inqueue"))
	if !g.OwnedByVolcanoJob || !g.Unschedulable || g.Phase != gangPhaseInqueue || g.MinMember != 2 || g.Queue != "acme-payments-prod" {
		t.Fatalf("read %+v", g)
	}
	if cpu := g.MinResources.Cpu().MilliValue(); cpu != 3000 {
		t.Fatalf("minResources cpu = %dm", cpu)
	}
	// The condition's message is what says the gang waits for nodes rather than
	// for its queue or a label no worker has (gangShortOfCapacity).
	if !mentionsCapacityShortage(g.Message) {
		t.Fatalf("the Unschedulable condition's message was not read: %q", g.Message)
	}
	// A PodGroup whose owner is not Volcano's Job (a hand-written one, or one a
	// native Job's pods name) cannot carry an opt-in on its owner's behalf.
	u := podGroup("settlement", optedInProd, 2, "3", "4Gi", "Pending")
	refs := u.GetOwnerReferences()
	refs[0].APIVersion = "batch/v1"
	u.SetOwnerReferences(refs)
	if gangFromPodGroup(u).OwnedByVolcanoJob {
		t.Fatal("a batch/v1 Job was taken for a Volcano Job")
	}
	u.SetOwnerReferences(nil)
	if gangFromPodGroup(u).OwnedByVolcanoJob {
		t.Fatal("an unowned PodGroup was taken for a Volcano Job's")
	}

	q := queueFromUnstructured(volcanoQueue("acme-payments-prod", "adhar-batch", "8", "16Gi"))
	if q.Parent != "adhar-batch" || q.Capability.Cpu().MilliValue() != 8000 || q.Allocated.Cpu().MilliValue() != 0 {
		t.Fatalf("read %+v", q)
	}
}

func TestDiscoveryMustServeBothKinds(t *testing.T) {
	list := &metav1.APIResourceList{APIResources: []metav1.APIResource{{Name: "podgroups"}, {Name: "podgroups/status"}}}
	if servesPodGroupsAndQueues(list) {
		t.Fatal("PodGroups without Queues cannot show a queue has room")
	}
	list.APIResources = append(list.APIResources, metav1.APIResource{Name: "queues"})
	if !servesPodGroupsAndQueues(list) || servesPodGroupsAndQueues(nil) {
		t.Fatal("discovery check misread")
	}
}

// The discovery check is what a cluster without Volcano pays every tick, so it
// must be the one small GET of Volcano's group version: not the aggregated
// discovery of every group (hundreds of CRDs on this platform's control plane),
// and a group the server does not serve reads as no Volcano.
func TestVolcanoDiscoveryIsOneSmallRequest(t *testing.T) {
	var (
		mu     sync.Mutex
		paths  []string
		served bool
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		paths = append(paths, req.URL.Path)
		ok := served
		mu.Unlock()
		if ok && req.URL.Path == "/apis/scheduling.volcano.sh/v1beta1" {
			_ = json.NewEncoder(w).Encode(metav1.APIResourceList{
				GroupVersion: volcanoSchedulingGV.String(),
				APIResources: []metav1.APIResource{
					{Name: "podgroups", Namespaced: true, Kind: "PodGroup"},
					{Name: "queues", Kind: "Queue"},
				},
			})
			return
		}
		http.NotFound(w, req)
	}))
	defer srv.Close()

	r := newReconciler(t, &fakeScaler{})
	r.RESTConfig = &rest.Config{Host: srv.URL}
	if r.volcanoServed(context.Background()) {
		t.Fatal("a server without Volcano read as serving it")
	}
	mu.Lock()
	served = true
	mu.Unlock()
	if !r.volcanoServed(context.Background()) {
		t.Fatal("a server serving PodGroups and Queues read as without Volcano")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, p := range paths {
		if p != "/apis/scheduling.volcano.sh/v1beta1" {
			t.Fatalf("discovery asked for %s; only Volcano's group version may be read: %v", p, paths)
		}
	}
	if len(paths) != 2 {
		t.Fatalf("want one request per tick, got %v", paths)
	}
}
