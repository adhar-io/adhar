package restore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/k8s"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// backupStorageLocationGVR is the GVR for Velero BackupStorageLocation resources.
var backupStorageLocationGVR = schema.GroupVersionResource{
	Group: "velero.io", Version: "v1", Resource: "backupstoragelocations",
}

// defaultRestoreName derives a restore name from the source backup when the
// caller did not supply one.
func defaultRestoreName(backup string) string {
	return fmt.Sprintf("%s-restore-%s", backup, time.Now().Format("20060102-150405"))
}

// createVeleroRestore applies a velero.io/v1 Restore built from the given spec.
// It is the single code path all restore subcommands (full/database/selective/
// create) funnel through, so the CLI always produces a real, control-plane
// reconciled Velero object rather than touching the local filesystem.
func createVeleroRestore(name string, spec map[string]interface{}) error {
	dyn, err := getDynamicClient()
	if err != nil {
		return unreachable(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "velero.io/v1",
		"kind":       "Restore",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": veleroNamespace,
			"labels":    map[string]interface{}{"adhar.io/managed-by": "adhar-cli"},
		},
		"spec": spec,
	}}

	if _, err := dyn.Resource(restoreGVR).Namespace(veleroNamespace).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		if crdMissing(err) {
			return fmt.Errorf("Velero Restore CRD not installed (velero not present in the cluster)")
		}
		return fmt.Errorf("failed to create restore %q: %w", name, err)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Restore %q created from backup %q", name, spec["backupName"])))
	fmt.Println(helpers.CreateMuted("   Track progress with: adhar restore status " + name))
	return nil
}

// parseSelector parses a "key=value,key2=value2" string into a matchLabels map.
func parseSelector(s string) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	if strings.TrimSpace(s) == "" {
		return out, nil
	}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 || kv[0] == "" {
			return nil, fmt.Errorf("invalid selector %q, expected key=value[,key=value]", s)
		}
		out[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
	}
	return out, nil
}

// toIfaceSlice converts a []string to []interface{} for unstructured specs.
func toIfaceSlice(in []string) []interface{} {
	out := make([]interface{}, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
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

func unstructuredBool(obj map[string]interface{}, fields ...string) (bool, bool, error) {
	return unstructured.NestedBool(obj, fields...)
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
