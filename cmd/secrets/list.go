package secrets

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var listExternal bool

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all secrets",
	Long: `List Kubernetes secrets (and optionally ExternalSecrets) with their
metadata. Secret values are never printed — only the keys and metadata are
shown. Use ` + "`adhar secrets get --name <name> --reveal`" + ` to reveal values.

Examples:
  adhar secrets list                    # List secrets in adhar-system
  adhar secrets list --namespace=prod   # List secrets in a namespace
  adhar secrets list --external         # Also list ExternalSecrets + sync status`,
	RunE: runList,
}

func init() {
	listCmd.Flags().BoolVar(&listExternal, "external", false, "Also list external-secrets ExternalSecrets and their sync status")
}

// externalSecretsGVR is the GVR for external-secrets.io ExternalSecret resources.
var externalSecretsGVR = schema.GroupVersionResource{
	Group:    "external-secrets.io",
	Version:  "v1",
	Resource: "externalsecrets",
}

func runList(cmd *cobra.Command, args []string) error {
	logger.Info("📋 Listing secrets...")

	ns := resolveNamespace()
	clientset, err := getClientset()
	if err != nil {
		return unreachable(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout(timeout))
	defer cancel()

	secrets, err := clientset.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list secrets in namespace %q: %w", ns, err)
	}

	type row struct {
		Name string
		Type string
		Keys []string
		Age  string
	}
	rows := make([]row, 0, len(secrets.Items))
	for _, s := range secrets.Items {
		keys := make([]string, 0, len(s.Data))
		for k := range s.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		rows = append(rows, row{
			Name: s.Name,
			Type: string(s.Type),
			Keys: keys,
			Age:  formatAge(s.CreationTimestamp.Time),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

	fmt.Printf("\n%s\n", helpers.TitleStyle.Render(fmt.Sprintf("🔐 Secrets in namespace %q (%d)", ns, len(rows))))
	if len(rows) == 0 {
		fmt.Println(helpers.CreateMuted("   No secrets found"))
	} else {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("%-40s %-30s %-8s %s\n", "NAME", "TYPE", "AGE", "KEYS"))
		b.WriteString(strings.Repeat("─", 100) + "\n")
		for _, r := range rows {
			b.WriteString(fmt.Sprintf("%-40s %-30s %-8s %s\n",
				truncate(r.Name, 38), truncate(r.Type, 28), r.Age, strings.Join(r.Keys, ", ")))
		}
		fmt.Print(b.String())
	}

	if listExternal {
		if err := listExternalSecrets(ctx, ns); err != nil {
			fmt.Println(helpers.CreateMuted("   external-secrets: " + err.Error()))
		}
	}

	logger.Info("✅ Secrets listed")
	return nil
}

// listExternalSecrets queries ExternalSecret CRs via the dynamic client and
// prints their sync status. A missing CRD is reported but non-fatal.
func listExternalSecrets(ctx context.Context, ns string) error {
	dyn, err := getDynamicClient()
	if err != nil {
		return err
	}

	list, err := dyn.Resource(externalSecretsGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "could not find") {
			return fmt.Errorf("ExternalSecret CRD not installed (external-secrets operator not present)")
		}
		return fmt.Errorf("failed to list ExternalSecrets: %w", err)
	}

	fmt.Printf("\n%s\n", helpers.TitleStyle.Render(fmt.Sprintf("🔁 ExternalSecrets in namespace %q (%d)", ns, len(list.Items))))
	if len(list.Items) == 0 {
		fmt.Println(helpers.CreateMuted("   No ExternalSecrets found"))
		return nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%-40s %-12s %-14s %s\n", "NAME", "READY", "STORE", "STATUS"))
	b.WriteString(strings.Repeat("─", 100) + "\n")
	for _, es := range list.Items {
		ready, status := externalSecretReady(es.Object)
		store := nestedString(es.Object, "spec", "secretStoreRef", "name")
		b.WriteString(fmt.Sprintf("%-40s %-12s %-14s %s\n",
			truncate(es.GetName(), 38), ready, truncate(store, 12), status))
	}
	fmt.Print(b.String())
	return nil
}

// externalSecretReady inspects status.conditions for a Ready condition and
// returns a friendly indicator plus the condition message/reason.
func externalSecretReady(obj map[string]interface{}) (string, string) {
	conds, found, _ := nestedSlice(obj, "status", "conditions")
	if !found {
		return "❓ Unknown", "no status yet"
	}
	for _, c := range conds {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if fmt.Sprintf("%v", cm["type"]) == "Ready" {
			st := fmt.Sprintf("%v", cm["status"])
			detail := fmt.Sprintf("%v", cm["reason"])
			if msg, ok := cm["message"].(string); ok && msg != "" {
				detail = msg
			}
			if st == "True" {
				return "✅ True", detail
			}
			return "❌ False", detail
		}
	}
	return "❓ Unknown", "no Ready condition"
}
