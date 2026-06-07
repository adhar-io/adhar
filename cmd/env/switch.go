package env

import (
	"context"
	"fmt"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	switchContext string
)

var switchCmd = &cobra.Command{
	Use:     "switch [environment-name]",
	Aliases: []string{"use"},
	Short:   "Switch to environment",
	Long: `Switch the active environment by updating the current kubeconfig context's
default namespace to the environment's namespace. Optionally switch the
kube-context as well with --context.

Examples:
  adhar env switch dev                 # Set current context namespace to "dev"
  adhar env use staging                # "use" is an alias for "switch"
  adhar env switch prod --context=eks  # Also switch kube-context to "eks"`,
	Args: cobra.ExactArgs(1),
	RunE: runSwitch,
}

func init() {
	switchCmd.Flags().StringVar(&switchContext, "context", "", "Also switch to this kube-context")
}

func runSwitch(cmd *cobra.Command, args []string) error {
	envName := args[0]
	ns := envName // environments are namespaces

	// Verify the namespace exists before mutating kubeconfig.
	if clientset, err := getClientset(); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := clientset.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{}); err != nil {
			fmt.Println(helpers.WarningStyle.Render(fmt.Sprintf("⚠️  Namespace %q not found; switching anyway", ns)))
		}
	}

	// Load and modify the kubeconfig in place.
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	cfg, err := rules.Load()
	if err != nil {
		return fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	if switchContext != "" {
		if _, ok := cfg.Contexts[switchContext]; !ok {
			return fmt.Errorf("kube-context %q not found in kubeconfig", switchContext)
		}
		cfg.CurrentContext = switchContext
	}

	curName := cfg.CurrentContext
	curCtx, ok := cfg.Contexts[curName]
	if !ok || curCtx == nil {
		return fmt.Errorf("current kube-context %q not found in kubeconfig", curName)
	}
	curCtx.Namespace = ns

	if err := clientcmd.ModifyConfig(rules, *cfg, true); err != nil {
		return fmt.Errorf("failed to write kubeconfig: %w", err)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Switched to environment %q (context %q, namespace %q)", envName, curName, ns)))
	return nil
}
