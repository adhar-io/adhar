package secrets

import (
	"fmt"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/k8s"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// getClientset returns a typed Kubernetes clientset via the shared platform helper.
func getClientset() (*kubernetes.Clientset, error) {
	return k8s.GetClientset()
}

// getDynamicClient returns a dynamic client (for CRDs like ExternalSecret),
// built from the same kubeconfig as the typed clientset.
func getDynamicClient() (dynamic.Interface, error) {
	return k8s.GetDynamicClient()
}

// resolveNamespace returns the user-selected namespace, defaulting to the Adhar
// system namespace when none is provided.
func resolveNamespace() string {
	if namespace != "" {
		return namespace
	}
	return globals.AdharSystemNamespace
}

// parseTimeout parses the --timeout flag, defaulting to 30s on error.
func parseTimeout(s string) time.Duration {
	if s == "" {
		return 30 * time.Second
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 30 * time.Second
	}
	return d
}

// unreachable wraps a client-construction error with a friendly message.
func unreachable(err error) error {
	fmt.Println(helpers.ErrorStyle.Render("❌ Could not connect to the cluster"))
	fmt.Println(helpers.CreateMuted("   " + err.Error()))
	fmt.Println(helpers.CreateMuted("   Is the cluster running? Try `adhar up` or check your kubeconfig context."))
	return fmt.Errorf("failed to get Kubernetes client: %w", err)
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

func formatAge(t time.Time) string {
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

// nestedString safely reads a nested string from an unstructured object map.
func nestedString(obj map[string]interface{}, fields ...string) string {
	s, _, _ := unstructured.NestedString(obj, fields...)
	return s
}

// nestedSlice safely reads a nested slice from an unstructured object map.
func nestedSlice(obj map[string]interface{}, fields ...string) ([]interface{}, bool, error) {
	return unstructured.NestedSlice(obj, fields...)
}
