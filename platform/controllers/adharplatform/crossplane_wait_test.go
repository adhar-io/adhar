package adharplatform

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func xrd(conditions ...map[string]interface{}) unstructured.Unstructured {
	u := unstructured.Unstructured{Object: map[string]interface{}{"status": map[string]interface{}{}}}
	list := make([]interface{}, 0, len(conditions))
	for _, c := range conditions {
		list = append(list, c)
	}
	_ = unstructured.SetNestedSlice(u.Object, list, "status", "conditions")
	return u
}

func TestAllXRDsEstablishedRequiresEveryXRD(t *testing.T) {
	established := map[string]interface{}{"type": "Established", "status": "True"}
	pending := map[string]interface{}{"type": "Established", "status": "False"}
	offered := map[string]interface{}{"type": "Offered", "status": "True"}

	if !allXRDsEstablished([]unstructured.Unstructured{xrd(established), xrd(offered, established)}) {
		t.Error("all Established → true")
	}
	if allXRDsEstablished([]unstructured.Unstructured{xrd(established), xrd(pending)}) {
		t.Error("one XRD still establishing → false")
	}
	if allXRDsEstablished([]unstructured.Unstructured{xrd(offered)}) {
		t.Error("a different condition does not count as Established")
	}
	if allXRDsEstablished([]unstructured.Unstructured{xrd()}) {
		t.Error("no conditions at all → false")
	}
	if !allXRDsEstablished(nil) {
		t.Error("vacuously true for an empty list (the caller requires a non-empty list before trusting it)")
	}
}
