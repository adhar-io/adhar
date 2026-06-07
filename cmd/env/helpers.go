package env

import (
	"fmt"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/k8s"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// envLabel marks a namespace as an Adhar-managed environment.
const envLabel = "adhar.io/environment"

// compositeEnvironmentGVR is the GVR for the CompositeEnvironment XR. Creation
// is best-effort: when the XRD is absent we fall back to a plain namespace.
var compositeEnvironmentGVR = schema.GroupVersionResource{
	Group: "platform.adhar.io", Version: "v1alpha1", Resource: "compositeenvironments",
}

func getClientset() (*kubernetes.Clientset, error) {
	return k8s.GetClientset()
}

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
	if err == nil {
		return false
	}
	s := err.Error()
	for _, sub := range []string{"could not find", "no matches for kind", "the server could not find"} {
		if contains(s, sub) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func envAge(t time.Time) string {
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
