package batch

import (
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// discoveryWith is the API server's discovery: Volcano's scheduling group
// served, or not.
func discoveryWith(volcano bool) *fakediscovery.FakeDiscovery {
	disc := kubefake.NewSimpleClientset().Discovery().(*fakediscovery.FakeDiscovery)
	if volcano {
		disc.Resources = []*metav1.APIResourceList{{
			GroupVersion: "scheduling.volcano.sh/v1beta1",
			APIResources: []metav1.APIResource{{Name: "queues"}, {Name: "podgroups", Namespaced: true}},
		}}
	}
	return disc
}

// queueObject is a Volcano Queue as v1.16 reports it.
func queueObject(name, parent string, spec, status map[string]interface{}) *unstructured.Unstructured {
	if spec == nil {
		spec = map[string]interface{}{}
	}
	if parent != "" {
		spec["parent"] = parent
	}
	if status == nil {
		status = map[string]interface{}{"state": "Open"}
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "scheduling.volcano.sh/v1beta1", "kind": "Queue",
		"metadata": map[string]interface{}{"name": name},
		"spec":     spec, "status": status,
	}}
}

func queueFixtures() []runtime.Object {
	return []runtime.Object{
		queueObject("root", "", nil, nil),
		queueObject("default", "root", nil, nil),
		queueObject("adhar-batch", "root",
			map[string]interface{}{"capability": map[string]interface{}{"cpu": "12", "memory": "24Gi"}, "deserved": map[string]interface{}{"cpu": "12", "memory": "24Gi"}},
			map[string]interface{}{"state": "Open", "allocated": map[string]interface{}{"cpu": "6", "memory": "10Gi"}, "running": int64(3)}),
		queueObject("other-team-prod", "adhar-batch", nil, nil),
		queueObject("acme-payments-prod", "adhar-batch",
			map[string]interface{}{"capability": map[string]interface{}{"cpu": "6", "memory": "12Gi"}, "deserved": map[string]interface{}{"cpu": "2", "memory": "4Gi"}, "reclaimable": false, "priority": int64(300)},
			map[string]interface{}{"state": "Open", "allocated": map[string]interface{}{"cpu": "1500m", "memory": "3Gi"}, "running": int64(1)}),
		queueObject("acme-payments-dev", "adhar-batch",
			map[string]interface{}{"capability": map[string]interface{}{"cpu": "2", "memory": "4Gi"}, "reclaimable": true, "priority": int64(100)},
			map[string]interface{}{"state": "Open", "allocated": map[string]interface{}{"cpu": "2", "memory": "2Gi"}, "running": int64(1), "pending": int64(2)}),
		queueObject("acme-payments-test", "adhar-batch",
			map[string]interface{}{"capability": map[string]interface{}{"cpu": "2", "memory": "4Gi"}, "reclaimable": true, "priority": int64(200)},
			map[string]interface{}{"state": "Open", "allocated": map[string]interface{}{"cpu": "2"}}),
	}
}

func queueNames(qs []queueView) []string {
	var out []string
	for _, q := range qs {
		out = append(out, q.Name)
	}
	return out
}

// Without Volcano there are no queues, and the command says so without asking
// the API server for any: the native path is the Phase-1 product, not an error.
func TestQueuesWithoutVolcanoSayTheNativePathAndReadNothing(t *testing.T) {
	dc := fakeCluster()
	dc.PrependReactor("*", "*", func(a k8stesting.Action) (bool, runtime.Object, error) {
		t.Errorf("a cluster without Volcano was asked for %s %s", a.GetVerb(), a.GetResource().Resource)
		return false, nil, nil
	})
	report, err := readQueues(context.Background(), dc, discoveryWith(false), "")
	if err != nil || report.Volcano || len(report.Queues) != 0 || report.Root != nil {
		t.Fatalf("report = %+v %v", report, err)
	}
	if got := toJSON(report); got != `{"volcano":false,"queues":[]}` {
		t.Errorf("-o json = %s", got)
	}
	printQueues(report)
}

// The operator's view: the root and every leaf under it, team then tier in
// promotion order; Volcano's own root and default queues are not batch queues.
// A leaf at its capability with gangs waiting is saturated; one at capability
// with nothing waiting is merely full.
func TestQueuesListTheRootAndItsLeaves(t *testing.T) {
	dc := fakeCluster(queueFixtures()...)
	report, err := readQueues(context.Background(), dc, discoveryWith(true), "")
	if err != nil || !report.Volcano {
		t.Fatalf("report = %+v %v", report, err)
	}
	if report.Root == nil || report.Root.Name != "adhar-batch" || report.Root.Capability["cpu"] != "12" || report.Root.Allocated["cpu"] != "6" {
		t.Errorf("root = %+v", report.Root)
	}
	want := []string{"acme-payments-dev", "acme-payments-test", "acme-payments-prod", "other-team-prod"}
	if got := queueNames(report.Queues); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("leaves = %v, want %v", got, want)
	}
	dev, test, prod := report.Queues[0], report.Queues[1], report.Queues[2]
	if dev.Team != "acme-payments" || dev.Tier != "dev" || !dev.Saturated || dev.Pending != 2 || dev.Reclaimable == nil || !*dev.Reclaimable {
		t.Errorf("dev leaf = %+v", dev)
	}
	if test.Saturated {
		t.Errorf("a full leaf with nothing waiting is not saturated: %+v", test)
	}
	if prod.Saturated || prod.Deserved["cpu"] != "2" || *prod.Priority != 300 || *prod.Reclaimable {
		t.Errorf("prod leaf = %+v", prod)
	}
	if report.Descriptor != "absent" {
		t.Errorf("no descriptor ConfigMap = %q", report.Descriptor)
	}
	if got := usageCell(prod, "cpu"); got != "1500m/2/6" {
		t.Errorf("usage = %q", got)
	}
	printQueues(report)

	// A descriptor that names another root is the root.
	cm := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]interface{}{"name": schedulerDescriptor, "namespace": "adhar-system"},
		"data":     map[string]interface{}{"scheduler": "volcano", "rootQueue": "other-root", "gang": "true"},
	}}
	objs := append(queueFixtures(), cm, queueObject("other-root", "root", nil, nil), queueObject("x-y-dev", "other-root", nil, nil))
	report, err = readQueues(context.Background(), fakeCluster(objs...), discoveryWith(true), "")
	if err != nil || report.Descriptor != "present" || report.Scheduler != "volcano" || report.Root == nil || report.Root.Name != "other-root" || strings.Join(queueNames(report.Queues), ",") != "x-y-dev" {
		t.Errorf("descriptor root = %+v %v", report, err)
	}
}

// A team member may get its own three leaves by name and list nothing (§10.2):
// --team never lists, a missing leaf is named with where to look, and a
// forbidden one is named as forbidden. Without --team a forbidden list points
// at --team.
func TestQueuesForATeamReadTheirThreeLeavesByName(t *testing.T) {
	objs := queueFixtures()
	var trimmed []runtime.Object
	for _, o := range objs {
		if o.(*unstructured.Unstructured).GetName() != "acme-payments-prod" {
			trimmed = append(trimmed, o)
		}
	}
	dc := fakeCluster(trimmed...)
	dc.PrependReactor("list", "queues", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "scheduling.volcano.sh", Resource: "queues"}, "", nil)
	})
	dc.PrependReactor("get", "queues", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.(k8stesting.GetAction).GetName() == "acme-payments-test" {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "scheduling.volcano.sh", Resource: "queues"}, "acme-payments-test", nil)
		}
		return false, nil, nil
	})
	report, err := readQueues(context.Background(), dc, discoveryWith(true), "acme-payments")
	if err != nil {
		t.Fatal(err)
	}
	if got := queueNames(report.Queues); len(got) != 1 || got[0] != "acme-payments-dev" {
		t.Errorf("leaves = %v", got)
	}
	reasons := map[string]string{}
	for _, m := range report.Missing {
		reasons[m.Name] = m.Reason
	}
	if !strings.HasPrefix(reasons["acme-payments-prod"], "not found") || !strings.HasPrefix(reasons["acme-payments-test"], "forbidden") {
		t.Errorf("missing = %v", report.Missing)
	}
	printQueues(report)

	if _, err := readQueues(context.Background(), dc, discoveryWith(true), ""); err == nil || !strings.Contains(err.Error(), "--team") {
		t.Errorf("a forbidden list points at --team: %v", err)
	}
}

// --team shows only the team's leaves UNDER the batch root: a namesake queue
// elsewhere (under Volcano's default, made by hand) is not where its runs are
// bound (§4.3), and reading it as the team's leaf would report another
// queue's usage. It is named as missing, with its parent.
func TestQueuesForATeamIgnoreANamesakeOutsideTheBatchRoot(t *testing.T) {
	dc := fakeCluster(append(queueFixtures(), queueObject("x-team-dev", "default", nil, nil))...)
	report, err := readQueues(context.Background(), dc, discoveryWith(true), "x-team")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Queues) != 0 {
		t.Errorf("a queue outside adhar-batch was shown as the team's leaf: %v", queueNames(report.Queues))
	}
	found := false
	for _, m := range report.Missing {
		if m.Name == "x-team-dev" && strings.Contains(m.Reason, `"default"`) {
			found = true
		}
	}
	if !found {
		t.Errorf("missing = %+v", report.Missing)
	}
}
