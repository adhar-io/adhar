package policy

import (
	"fmt"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/k8s"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// Kyverno + policy-report GVRs used by the dynamic client.
var (
	clusterPolicyGVR = schema.GroupVersionResource{
		Group: "kyverno.io", Version: "v1", Resource: "clusterpolicies",
	}
	policyGVR = schema.GroupVersionResource{
		Group: "kyverno.io", Version: "v1", Resource: "policies",
	}
	policyReportGVR = schema.GroupVersionResource{
		Group: "wgpolicyk8s.io", Version: "v1alpha2", Resource: "policyreports",
	}
	clusterPolicyReportGVR = schema.GroupVersionResource{
		Group: "wgpolicyk8s.io", Version: "v1alpha2", Resource: "clusterpolicyreports",
	}
)

// getDynamicClient returns a dynamic client built from the shared kubeconfig.
func getDynamicClient() (dynamic.Interface, error) {
	return k8s.GetDynamicClient()
}

// unreachable wraps a client-construction error with a friendly message.
func unreachable(err error) error {
	fmt.Println(helpers.ErrorStyle.Render("❌ Could not connect to the cluster"))
	fmt.Println(helpers.CreateMuted("   " + err.Error()))
	fmt.Println(helpers.CreateMuted("   Is the cluster running? Try `adhar up` or check your kubeconfig context."))
	return fmt.Errorf("failed to get Kubernetes client: %w", err)
}

func nestedString(obj map[string]interface{}, fields ...string) string {
	s, _, _ := unstructured.NestedString(obj, fields...)
	return s
}

func nestedSlice(obj map[string]interface{}, fields ...string) []interface{} {
	s, _, _ := unstructured.NestedSlice(obj, fields...)
	return s
}

func nestedMap(obj map[string]interface{}, fields ...string) (map[string]interface{}, bool, error) {
	return unstructured.NestedMap(obj, fields...)
}

// intOf coerces an unstructured numeric value (int64/float64/json.Number) to int.
func intOf(v interface{}) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case int:
		return n
	case float64:
		return int(n)
	default:
		return 0
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}
