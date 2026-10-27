package function

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestDash(t *testing.T) {
	t.Parallel()

	if got := dash(""); got != "—" {
		t.Fatalf("dash(\"\") = %q, want em dash", got)
	}
	if got := dash("   "); got != "—" {
		t.Fatalf("dash(whitespace) = %q, want em dash", got)
	}
	if got := dash("http://fn"); got != "http://fn" {
		t.Fatalf("dash(non-empty) = %q, want value", got)
	}
}

func TestFromObjectExtractsFunctionStatus(t *testing.T) {
	t.Parallel()

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "platform.adhar.io/v1alpha1",
			"kind":       "Function",
			"metadata": map[string]interface{}{
				"name":              "orders-fn",
				"namespace":         "adhar-system",
				"creationTimestamp": time.Now().Add(-2 * time.Minute).Format(time.RFC3339),
			},
			"spec": map[string]interface{}{
				"parameters": map[string]interface{}{
					"runtime":  "node20",
					"image":    "harbor.example/orders-fn:latest",
					"minScale": int64(0),
					"maxScale": int64(25),
				},
			},
			"status": map[string]interface{}{
				"url": "https://orders-fn.dev.example.com",
				"conditions": []interface{}{
					map[string]interface{}{"type": "Ready", "status": "True"},
					map[string]interface{}{"type": "Synced", "status": "True"},
				},
			},
		},
	}
	obj.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-2 * time.Minute)))

	got := fromObject(obj)

	if got.Name != "orders-fn" {
		t.Fatalf("name = %q, want orders-fn", got.Name)
	}
	if got.Namespace != "adhar-system" {
		t.Fatalf("namespace = %q, want adhar-system", got.Namespace)
	}
	if got.Runtime != "node20" {
		t.Fatalf("runtime = %q, want node20", got.Runtime)
	}
	if got.Image != "harbor.example/orders-fn:latest" {
		t.Fatalf("image = %q, want harbor.example/orders-fn:latest", got.Image)
	}
	if got.URL != "https://orders-fn.dev.example.com" {
		t.Fatalf("url = %q, want function url", got.URL)
	}
	if got.MinScale != 0 || got.MaxScale != 25 {
		t.Fatalf("scale = %d..%d, want 0..25", got.MinScale, got.MaxScale)
	}
	if !got.Ready {
		t.Fatalf("ready = false, want true")
	}
	if len(got.Conditions) != 2 {
		t.Fatalf("conditions len = %d, want 2", len(got.Conditions))
	}
	if got.Age == "" {
		t.Fatalf("age must not be empty")
	}
}

func TestFromObjectDoesNotMarkReadyWhenConditionFalse(t *testing.T) {
	t.Parallel()

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{
				"name":              "billing-fn",
				"namespace":         "adhar-system",
				"creationTimestamp": time.Now().Add(-1 * time.Minute).Format(time.RFC3339),
			},
			"status": map[string]interface{}{
				"conditions": []interface{}{
					map[string]interface{}{"type": "Ready", "status": "False"},
				},
			},
		},
	}
	obj.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-1 * time.Minute)))

	got := fromObject(obj)
	if got.Ready {
		t.Fatalf("ready = true, want false")
	}
	if len(got.Conditions) != 1 || got.Conditions[0] != "Ready=False" {
		t.Fatalf("conditions = %#v, want [Ready=False]", got.Conditions)
	}
}
