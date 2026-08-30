package security

import (
	"fmt"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/k8s"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// GVRs for the security data sources the CLI reads. trivy-operator publishes the
// aquasecurity.github.io reports; Kyverno publishes clusterpolicies and the
// wgpolicyk8s.io PolicyReports. All read-only; enforcement flows through the
// control plane (see `adhar policy apply`).
var (
	vulnerabilityReportGVR = schema.GroupVersionResource{
		Group: "aquasecurity.github.io", Version: "v1alpha1", Resource: "vulnerabilityreports",
	}
	exposedSecretReportGVR = schema.GroupVersionResource{
		Group: "aquasecurity.github.io", Version: "v1alpha1", Resource: "exposedsecretreports",
	}
	configAuditReportGVR = schema.GroupVersionResource{
		Group: "aquasecurity.github.io", Version: "v1alpha1", Resource: "configauditreports",
	}
	clusterPolicyGVR = schema.GroupVersionResource{
		Group: "kyverno.io", Version: "v1", Resource: "clusterpolicies",
	}
	policyReportGVR = schema.GroupVersionResource{
		Group: "wgpolicyk8s.io", Version: "v1alpha2", Resource: "policyreports",
	}
	clusterPolicyReportGVR = schema.GroupVersionResource{
		Group: "wgpolicyk8s.io", Version: "v1alpha2", Resource: "clusterpolicyreports",
	}
)

// getDynamicClient returns a dynamic client from the shared kubeconfig.
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

// crdMissing reports whether a List error is just an absent CRD (operator not
// installed) rather than a real API failure.
func crdMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return contains(msg, "could not find the requested resource") ||
		contains(msg, "the server could not find") ||
		contains(msg, "no matches for kind") ||
		contains(msg, "could not find")
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func nestedString(obj map[string]interface{}, fields ...string) string {
	s, _, _ := unstructured.NestedString(obj, fields...)
	return s
}

func nestedSlice(obj map[string]interface{}, fields ...string) []interface{} {
	s, _, _ := unstructured.NestedSlice(obj, fields...)
	return s
}

func nestedMap(obj map[string]interface{}, fields ...string) (map[string]interface{}, bool) {
	m, found, _ := unstructured.NestedMap(obj, fields...)
	return m, found
}

// intOf coerces an unstructured numeric value to int.
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

// severityRank maps a severity name to an ordinal for --severity filtering.
func severityRank(sev string) int {
	switch normalizeSeverity(sev) {
	case "critical":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

func normalizeSeverity(sev string) string {
	switch sev {
	case "CRITICAL", "Critical", "critical":
		return "critical"
	case "HIGH", "High", "high":
		return "high"
	case "MEDIUM", "Medium", "medium":
		return "medium"
	case "LOW", "Low", "low":
		return "low"
	default:
		return ""
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

func valueOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
