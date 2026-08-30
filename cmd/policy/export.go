package policy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

var (
	exportCmd = &cobra.Command{
		Use:   "export [policy-name]",
		Short: "Export Kyverno ClusterPolicies to YAML",
		Long: `Export existing Kyverno ClusterPolicies (kyverno.io/v1) from the cluster to
YAML. Server-managed fields (status, managedFields, resourceVersion, uid, …) are
stripped so the output is re-appliable.

With --dir=- (or --stdout) the YAML is printed to stdout; otherwise one file per
policy is written to --dir.

Examples:
  adhar policy export --all
  adhar policy export baseline-require-limits
  adhar policy export --all --dir=./policies
  adhar policy export --all --stdout`,
		Args: cobra.MaximumNArgs(1),
		RunE: runExportPolicy,
	}

	exportDir    string
	exportAll    bool
	exportStdout bool
)

func init() {
	exportCmd.Flags().StringVarP(&exportDir, "dir", "d", "./policies", "Export directory")
	exportCmd.Flags().BoolVarP(&exportAll, "all", "a", false, "Export all policies")
	exportCmd.Flags().BoolVar(&exportStdout, "stdout", false, "Print to stdout instead of writing files")
}

func runExportPolicy(cmd *cobra.Command, args []string) error {
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	if name == "" && !exportAll {
		return fmt.Errorf("policy name is required, or use --all")
	}

	dyn, err := getDynamicClient()
	if err != nil {
		return unreachable(err)
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	if ctx == nil {
		ctx = context.Background()
	}

	var items []unstructured.Unstructured
	if name != "" {
		obj, err := dyn.Resource(clusterPolicyGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("get ClusterPolicy %s: %w", name, err)
		}
		items = append(items, *obj)
	} else {
		list, err := dyn.Resource(clusterPolicyGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("list ClusterPolicies: %w", err)
		}
		items = list.Items
	}

	if len(items) == 0 {
		fmt.Println(helpers.CreateMuted("   No ClusterPolicies to export."))
		return nil
	}

	toStdout := exportStdout || exportDir == "-"
	if !toStdout {
		if err := os.MkdirAll(exportDir, 0o755); err != nil {
			return fmt.Errorf("create export dir: %w", err)
		}
	}

	for i := range items {
		cleanForExport(&items[i])
		out, err := yaml.Marshal(items[i].Object)
		if err != nil {
			return fmt.Errorf("marshal %s: %w", items[i].GetName(), err)
		}
		if toStdout {
			fmt.Println("---")
			fmt.Print(string(out))
			continue
		}
		path := filepath.Join(exportDir, items[i].GetName()+".yaml")
		if err := os.WriteFile(path, out, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		fmt.Printf("   ✅ exported %s -> %s\n", items[i].GetName(), path)
	}

	if !toStdout {
		fmt.Println(helpers.CreateSuccess(fmt.Sprintf("Exported %d policy/policies to %s", len(items), exportDir)))
	}
	return nil
}

// cleanForExport strips server-managed metadata and status so the manifest is
// re-appliable.
func cleanForExport(obj *unstructured.Unstructured) {
	unstructured.RemoveNestedField(obj.Object, "status")
	unstructured.RemoveNestedField(obj.Object, "metadata", "managedFields")
	unstructured.RemoveNestedField(obj.Object, "metadata", "resourceVersion")
	unstructured.RemoveNestedField(obj.Object, "metadata", "uid")
	unstructured.RemoveNestedField(obj.Object, "metadata", "generation")
	unstructured.RemoveNestedField(obj.Object, "metadata", "creationTimestamp")
	unstructured.RemoveNestedField(obj.Object, "metadata", "selfLink")
	unstructured.RemoveNestedField(obj.Object, "metadata", "annotations", "kubectl.kubernetes.io/last-applied-configuration")
}
