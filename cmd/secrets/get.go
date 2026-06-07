package secrets

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var revealValues bool

var getCmd = &cobra.Command{
	Use:   "get",
	Short: "Get a secret's metadata and keys",
	Long: `Get a single Kubernetes secret. By default only metadata and the list of
keys are shown — values are NEVER printed unless you pass --reveal.

Examples:
  adhar secrets get --name=db-creds                 # Show keys + metadata only
  adhar secrets get --name=db-creds --reveal        # Reveal decoded values (sensitive!)
  adhar secrets get --name=db-creds --namespace=prod`,
	RunE: runGet,
}

func init() {
	getCmd.Flags().BoolVar(&revealValues, "reveal", false, "Reveal decoded secret values (sensitive, off by default)")
}

func runGet(cmd *cobra.Command, args []string) error {
	if secretName == "" {
		return fmt.Errorf("--name is required")
	}

	ns := resolveNamespace()
	clientset, err := getClientset()
	if err != nil {
		return unreachable(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout(timeout))
	defer cancel()

	sec, err := clientset.CoreV1().Secrets(ns).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get secret %q in namespace %q: %w", secretName, ns, err)
	}

	keys := make([]string, 0, len(sec.Data))
	for k := range sec.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(fmt.Sprintf("🔐 Name:      %s\n", sec.Name))
	b.WriteString(fmt.Sprintf("📦 Namespace: %s\n", sec.Namespace))
	b.WriteString(fmt.Sprintf("🏷️  Type:      %s\n", sec.Type))
	b.WriteString(fmt.Sprintf("🕐 Age:       %s\n", formatAge(sec.CreationTimestamp.Time)))
	b.WriteString(fmt.Sprintf("🔑 Keys:      %s", strings.Join(keys, ", ")))
	fmt.Println(helpers.BorderStyle.Width(80).Render(b.String()))

	if !revealValues {
		fmt.Println(helpers.CreateMuted("   Values hidden. Pass --reveal to display decoded values (sensitive)."))
		return nil
	}

	fmt.Println(helpers.WarningStyle.Render("⚠️  Revealing decoded secret values:"))
	for _, k := range keys {
		fmt.Printf("   %s = %s\n", k, string(sec.Data[k]))
	}
	return nil
}
