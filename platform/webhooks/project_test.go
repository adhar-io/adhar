package webhooks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const testNS = "adhar-system"

func projectObj(name, team, cpu, mem string, pods int64) *unstructured.Unstructured {
	params := map[string]interface{}{"name": name, "team": team}
	if cpu != "" {
		params["cpuQuota"] = cpu
	}
	if mem != "" {
		params["memoryQuota"] = mem
	}
	if pods > 0 {
		params["podQuota"] = pods
	}
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "platform.adhar.io/v1alpha1",
		"kind":       "CompositeProject",
		"metadata":   map[string]interface{}{"name": name, "namespace": testNS},
		"spec":       map[string]interface{}{"parameters": params},
	}}
	return u
}

// scheme that knows the unstructured CompositeProject list, so the fake client
// can serve List() for it.
func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	gv := schema.GroupVersion{Group: "platform.adhar.io", Version: "v1alpha1"}
	s.AddKnownTypeWithName(gv.WithKind("CompositeProject"), &unstructured.Unstructured{})
	s.AddKnownTypeWithName(gv.WithKind("CompositeProjectList"), &unstructured.UnstructuredList{})
	return s
}

func quotaCM(data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: TenantQuotaConfigMap, Namespace: testNS},
		Data:       data,
	}
}

func handle(t *testing.T, v *ProjectValidator, obj *unstructured.Unstructured) admission.Response {
	t.Helper()
	raw, err := json.Marshal(obj.Object)
	if err != nil {
		t.Fatal(err)
	}
	return v.Handle(context.Background(), admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{Object: runtime.RawExtension{Raw: raw}},
	})
}

func TestValidatorAdmitsWhenNoAllowanceIsDeclared(t *testing.T) {
	// Fails OPEN: shipping this must not reject projects on clusters that never
	// opted in by creating the ConfigMap.
	c := fake.NewClientBuilder().WithScheme(testScheme()).Build()
	v := &ProjectValidator{Client: c, Namespace: testNS}
	r := handle(t, v, projectObj("p1", "payments", "999", "999Gi", 9999))
	if !r.Allowed {
		t.Fatalf("expected admit, got denied: %s", r.Result.Message)
	}
}

func TestValidatorEnforcesTheTeamCeilingAcrossProjects(t *testing.T) {
	existing := projectObj("a", "payments", "6", "", 0)
	c := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(quotaCM(map[string]string{"payments": "cpu: \"10\"\n"}), existing).Build()
	v := &ProjectValidator{Client: c, Namespace: testNS}

	// 6 committed + 5 requested > 10 → denied, and the message must be actionable.
	r := handle(t, v, projectObj("b", "payments", "5", "", 0))
	if r.Allowed {
		t.Fatal("6+5 > 10 must be denied")
	}
	msg := r.Result.Message
	for _, want := range []string{"payments", "cpu", TenantQuotaConfigMap} {
		if !strings.Contains(msg, want) {
			t.Fatalf("deny message missing %q: %s", want, msg)
		}
	}

	// 6 + 4 == 10 fits.
	if r2 := handle(t, v, projectObj("b", "payments", "4", "", 0)); !r2.Allowed {
		t.Fatalf("6+4 == 10 must be allowed: %s", r2.Result.Message)
	}
}

func TestValidatorDoesNotChargeATeamForAnotherTeamsProjects(t *testing.T) {
	// The bug this guards: summing every project in the cluster instead of the
	// team's would make one busy team exhaust everyone else's ceiling.
	other := projectObj("theirs", "search", "9", "", 0)
	c := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(quotaCM(map[string]string{"default": "cpu: \"10\"\n"}), other).Build()
	v := &ProjectValidator{Client: c, Namespace: testNS}
	if r := handle(t, v, projectObj("mine", "payments", "9", "", 0)); !r.Allowed {
		t.Fatalf("another team's usage must not count: %s", r.Result.Message)
	}
}

func TestValidatorUsesDefaultWhenTheTeamHasNoEntry(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(quotaCM(map[string]string{"default": "cpu: \"2\"\n"})).Build()
	v := &ProjectValidator{Client: c, Namespace: testNS}
	if r := handle(t, v, projectObj("p", "anyteam", "3", "", 0)); r.Allowed {
		t.Fatal("the default allowance should apply to a team with no entry of its own")
	}
}

func TestValidatorPrefersTheTeamsOwnEntryOverDefault(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(quotaCM(map[string]string{
			"default":  "cpu: \"2\"\n",
			"payments": "cpu: \"50\"\n",
		})).Build()
	v := &ProjectValidator{Client: c, Namespace: testNS}
	if r := handle(t, v, projectObj("p", "payments", "40", "", 0)); !r.Allowed {
		t.Fatalf("the team's own larger allowance should win: %s", r.Result.Message)
	}
}

func TestUpdatingAProjectInPlaceIsNotDoubleCounted(t *testing.T) {
	// Re-applying an unchanged project must not trip quota — otherwise a team
	// cannot edit a description without being rejected.
	existing := projectObj("a", "payments", "8", "", 0)
	c := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(quotaCM(map[string]string{"payments": "cpu: \"10\"\n"}), existing).Build()
	v := &ProjectValidator{Client: c, Namespace: testNS}
	if r := handle(t, v, projectObj("a", "payments", "8", "", 0)); !r.Allowed {
		t.Fatalf("re-applying the same project must be allowed: %s", r.Result.Message)
	}
}

func TestValidatorAdmitsWhenTheAllowanceEntryIsMalformed(t *testing.T) {
	// A typo in ONE team's ceiling must not deny every project in the cluster.
	c := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(quotaCM(map[string]string{"payments": "cpu: lots\n"})).Build()
	v := &ProjectValidator{Client: c, Namespace: testNS}
	if r := handle(t, v, projectObj("p", "payments", "5", "", 0)); !r.Allowed {
		t.Fatalf("a malformed allowance should fail open: %s", r.Result.Message)
	}
}

func TestValidatorDeniesAProjectWhoseOwnQuotaIsUnparseable(t *testing.T) {
	// Fails CLOSED on the caller's own input: accepting "1 cpu" as zero would let
	// a typo buy unlimited capacity.
	c := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(quotaCM(map[string]string{"payments": "cpu: \"10\"\n"})).Build()
	v := &ProjectValidator{Client: c, Namespace: testNS}
	if r := handle(t, v, projectObj("p", "payments", "1 cpu", "", 0)); r.Allowed {
		t.Fatal("an unparseable request quota must be denied, not treated as zero")
	}
}

func TestValidatorAdmitsAProjectWithNoTeam(t *testing.T) {
	// Required-field validation belongs to the composition, not to quota.
	c := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(quotaCM(map[string]string{"default": "cpu: \"1\"\n"})).Build()
	v := &ProjectValidator{Client: c, Namespace: testNS}
	if r := handle(t, v, projectObj("p", "", "99", "", 0)); !r.Allowed {
		t.Fatalf("a team-less project is not this validator's complaint: %s", r.Result.Message)
	}
}

func TestValidatorReportsEveryBreachedAxisAtOnce(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(quotaCM(map[string]string{"payments": "cpu: \"1\"\nmemory: 1Gi\npods: 5\n"})).Build()
	v := &ProjectValidator{Client: c, Namespace: testNS}
	r := handle(t, v, projectObj("p", "payments", "2", "2Gi", 10))
	if r.Allowed {
		t.Fatal("should be denied")
	}
	for _, axis := range []string{"cpu", "memory", "pods"} {
		if !strings.Contains(r.Result.Message, axis) {
			t.Fatalf("%s missing from: %s", axis, r.Result.Message)
		}
	}
}
