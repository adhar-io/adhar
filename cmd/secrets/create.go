package secrets

import (
	"context"
	"fmt"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var createLiterals []string

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Create new secret",
	Long: `Create a new Kubernetes secret in the cluster.

Examples:
  adhar secrets create --name=db-creds --from-literal=user=admin --from-literal=pass=s3cr3t
  adhar secrets create --name=token --key=token --value=abc123
  adhar secrets create --name=app --namespace=prod --from-literal=key=val`,
	RunE: runCreate,
}

func init() {
	createCmd.Flags().StringArrayVar(&createLiterals, "from-literal", nil, "key=value pair to store (repeatable)")
}

func runCreate(cmd *cobra.Command, args []string) error {
	if secretName == "" {
		return fmt.Errorf("--name is required for secret creation")
	}

	data := map[string][]byte{}
	for _, lit := range createLiterals {
		k, v, ok := strings.Cut(lit, "=")
		if !ok || k == "" {
			return fmt.Errorf("invalid --from-literal %q, expected key=value", lit)
		}
		data[k] = []byte(v)
	}
	// Support the package-wide --key/--value flags as a convenience.
	if key != "" {
		data[key] = []byte(value)
	}
	if len(data) == 0 {
		return fmt.Errorf("no data provided: use --from-literal=key=value or --key/--value")
	}

	stype := corev1.SecretTypeOpaque
	if secretType != "" {
		stype = corev1.SecretType(secretType)
	}

	ns := resolveNamespace()
	logger.Info(fmt.Sprintf("🔐 Creating secret: %s/%s (type: %s)", ns, secretName, stype))

	clientset, err := getClientset()
	if err != nil {
		return unreachable(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout(timeout))
	defer cancel()

	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: ns,
			Labels:    map[string]string{"adhar.io/managed-by": "adhar-cli"},
		},
		Type: stype,
		Data: data,
	}

	if _, err := clientset.CoreV1().Secrets(ns).Create(ctx, sec, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("failed to create secret %q in namespace %q: %w", secretName, ns, err)
	}

	// Never echo values; only confirm the keys that were stored.
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Secret %q created with keys: %s", secretName, strings.Join(keys, ", "))))
	return nil
}
