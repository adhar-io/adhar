package backup

import (
	"fmt"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/k8s"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// veleroNamespace is where Velero CRs (Backups/Schedules/Restores) live.
const veleroNamespace = "velero"

// Velero GVRs used by the dynamic client.
var (
	backupGVR = schema.GroupVersionResource{
		Group: "velero.io", Version: "v1", Resource: "backups",
	}
	scheduleGVR = schema.GroupVersionResource{
		Group: "velero.io", Version: "v1", Resource: "schedules",
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

// crdMissing reports whether an error indicates the CRD/resource is absent.
func crdMissing(err error) bool {
	return err != nil && (containsAny(err.Error(), "could not find", "the server could not find"))
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
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

func unstructuredNestedInt(obj map[string]interface{}, fields ...string) (int64, bool, error) {
	return unstructured.NestedInt64(obj, fields...)
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

func backupAge(t time.Time) string {
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

// phaseIcon maps a Velero phase to a friendly indicator.
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

// adharNamespaceHint is kept for parity with other commands that default to the
// Adhar system namespace; Velero objects use the velero namespace by default.
var _ = globals.AdharSystemNamespace
