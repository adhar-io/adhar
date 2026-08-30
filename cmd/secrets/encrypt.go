package secrets

import (
	"context"
	"fmt"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var encryptCmd = &cobra.Command{
	Use:   "encrypt",
	Short: "Store sensitive data as a managed secret",
	Long: `Wrap sensitive values as a CompositeSecret (platform.adhar.io) through the
control plane instead of creating a raw Kubernetes Secret. Crossplane materializes
the backing Secret locally; on a cloud platform the same XR maps to the provider's
encrypted secret store (KMS/Key Vault), so the request is portable.

Examples:
  adhar secrets encrypt --name=api --key=token --value=abc123
  adhar secrets encrypt --name=db --from-literal=user=admin --from-literal=pass=s3cr3t
  adhar secrets encrypt --name=db --namespace=prod --from-literal=key=val`,
	RunE: runEncrypt,
}

var (
	encryptLiterals []string
	encryptProvider string
)

func init() {
	encryptCmd.Flags().StringArrayVar(&encryptLiterals, "from-literal", nil, "key=value pair to store (repeatable)")
	encryptCmd.Flags().StringVar(&encryptProvider, "encryption-provider", "", "Encryption provider (vault, aws-kms, azure-keyvault, gcp-kms, sealed-secrets)")
}

func runEncrypt(cmd *cobra.Command, args []string) error {
	if secretName == "" {
		return fmt.Errorf("--name is required")
	}

	stringData := map[string]interface{}{}
	for _, lit := range encryptLiterals {
		k, v, ok := strings.Cut(lit, "=")
		if !ok || k == "" {
			return fmt.Errorf("invalid --from-literal %q, expected key=value", lit)
		}
		stringData[k] = v
	}
	if key != "" {
		stringData[key] = value
	}
	if len(stringData) == 0 {
		return fmt.Errorf("no data provided: use --from-literal=key=value or --key/--value")
	}

	ns := resolveNamespace()
	logger.Info(fmt.Sprintf("🔒 Storing secret %q via the control plane (provider: %s)", secretName, helpers.ActiveProvider()))

	spec := map[string]interface{}{
		"name":       secretName,
		"type":       "generic",
		"namespace":  ns,
		"stringData": stringData,
		"encryption": map[string]interface{}{
			"enabled": true,
		},
	}
	if encryptProvider != "" {
		spec["encryption"].(map[string]interface{})["provider"] = encryptProvider
	}

	// Same feature label the compositesecret-local composition carries.
	obj := helpers.NewXR("CompositeSecret", secretName, ns, "secret", nil, spec)

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	dyn, err := getDynamicClient()
	if err != nil {
		return unreachable(err)
	}

	if _, err := dyn.Resource(compositeSecretGVR).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			return fmt.Errorf("secret %q already exists in namespace %q", secretName, ns)
		}
		return fmt.Errorf("create managed secret: %w", err)
	}

	keys := make([]string, 0, len(stringData))
	for k := range stringData {
		keys = append(keys, k)
	}
	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ CompositeSecret %q created with keys: %s", secretName, strings.Join(keys, ", "))))
	fmt.Println(helpers.CreateMuted(fmt.Sprintf("   Backing secret materializes as: %s (namespace %s)", secretName, ns)))
	return nil
}
