package secrets

import (
	"context"
	"fmt"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var rotateCmd = &cobra.Command{
	Use:   "rotate",
	Short: "Rotate an existing secret",
	Long: `Rotate a secret through the control plane by creating a CompositeSecretRotation
XR (platform.adhar.io). Crossplane reconciles it — locally by running a Job that
generates a fresh value and patches the target Secret's key; on a cloud platform
the same XR drives the cloud secret-manager's rotation (external-secrets/KMS).

Examples:
  adhar secrets rotate --name=db-creds
  adhar secrets rotate --name=api-key --secret-type=api-key --key=token
  adhar secrets rotate --name=db-creds --schedule="0 0 * * 0" --period-days=7`,
	RunE: runRotate,
}

var (
	rotateProvider   string
	rotateSecretType string
	rotateSchedule   string
	rotatePeriod     int
	rotateKey        string
)

func init() {
	rotateCmd.Flags().StringVar(&rotateProvider, "provider", "external-secrets",
		"Secret provider (aws-secrets-manager, azure-keyvault, gcp-secret-manager, vault, external-secrets)")
	rotateCmd.Flags().StringVar(&rotateSecretType, "secret-type", "generic",
		"Secret type (database, api-key, certificate, ssh-key, generic)")
	rotateCmd.Flags().StringVar(&rotateSchedule, "schedule", "", "Cron schedule for rotation (optional)")
	rotateCmd.Flags().IntVar(&rotatePeriod, "period-days", 0, "Rotation period in days (optional)")
	rotateCmd.Flags().StringVar(&rotateKey, "key", "password", "Secret key whose value is rotated (local provider)")
}

func runRotate(cmd *cobra.Command, args []string) error {
	if secretName == "" {
		return fmt.Errorf("--name is required for secret rotation")
	}

	ns := resolveNamespace()
	rotationName := secretName + "-rotation"
	logger.Info(fmt.Sprintf("🔄 Requesting rotation for secret: %s/%s (provider: %s)", ns, secretName, helpers.ActiveProvider()))

	parameters := map[string]interface{}{
		"provider":    rotateProvider,
		"secretType":  rotateSecretType,
		"rotationKey": rotateKey,
		"secretReferences": []interface{}{
			map[string]interface{}{
				"name":      secretName,
				"namespace": ns,
			},
		},
	}
	if rotateSchedule != "" {
		parameters["rotationSchedule"] = rotateSchedule
	}
	if rotatePeriod > 0 {
		parameters["rotationPeriodDays"] = int64(rotatePeriod)
	}

	// Provider-aware selection — identical contract to the Console. Locally this
	// resolves to compositesecretrotation-local; on a cloud platform (ADHAR_PROVIDER)
	// it resolves to that cloud's secret-manager rotation composition.
	obj := helpers.NewXR("CompositeSecretRotation", rotationName, ns, "secretrotation", nil,
		map[string]interface{}{"parameters": parameters})

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	dyn, err := getDynamicClient()
	if err != nil {
		return unreachable(err)
	}

	if _, err := dyn.Resource(compositeSecretRotationGVR).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			fmt.Println(helpers.WarningStyle.Render(fmt.Sprintf("⚠️  Rotation policy %q already exists; re-run rotation by deleting and recreating it", rotationName)))
			return nil
		}
		return fmt.Errorf("create secret rotation: %w", err)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Rotation requested for secret %q (XR %s in namespace %s)", secretName, rotationName, ns)))
	fmt.Println(helpers.CreateMuted(fmt.Sprintf("   Track it: kubectl -n %s get compositesecretrotation %s", ns, rotationName)))
	return nil
}
