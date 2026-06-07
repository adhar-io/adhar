package restore

import (
	"fmt"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/k8s"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// veleroNamespace is where Velero CRs live.
const veleroNamespace = "velero"

// restoreGVR is the GVR for Velero Restore resources.
var restoreGVR = schema.GroupVersionResource{
	Group: "velero.io", Version: "v1", Resource: "restores",
}

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

func crdMissing(err error) bool {
	return err != nil && strings.Contains(err.Error(), "could not find")
}

func nestedString(obj map[string]interface{}, fields ...string) string {
	s, _, _ := unstructured.NestedString(obj, fields...)
	return s
}

func countNested(obj map[string]interface{}, fields ...string) int64 {
	v, found, _ := unstructured.NestedInt64(obj, fields...)
	if !found {
		return 0
	}
	return v
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

func restoreAge(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func phaseIcon(phase string) string {
	switch phase {
	case "Completed":
		return "✅ Completed"
	case "InProgress", "New":
		return "⏳ " + phase
	case "Failed", "PartiallyFailed", "FailedValidation":
		return "❌ " + phase
	case "":
		return "❓ Unknown"
	default:
		return "⚠️  " + phase
	}
}
