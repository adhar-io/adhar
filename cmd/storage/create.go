package storage

import (
	"context"
	"fmt"

	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a PersistentVolumeClaim",
	Long: `Create a PersistentVolumeClaim from flags.

Examples:
  adhar storage create --name=data --size=10Gi
  adhar storage create --name=cache --size=5Gi --class=standard --namespace=prod`,
	RunE: runCreate,
}

func runCreate(cmd *cobra.Command, args []string) error {
	if volumeName == "" {
		return fmt.Errorf("--name is required for volume creation")
	}
	if size == "" {
		return fmt.Errorf("--size is required for volume creation")
	}

	qty, err := resource.ParseQuantity(size)
	if err != nil {
		return fmt.Errorf("invalid --size %q: %w", size, err)
	}

	ns := resolveNamespace()
	logger.Info(fmt.Sprintf("💾 Creating PersistentVolumeClaim %s/%s (size: %s)", ns, volumeName, size))

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      volumeName,
			Namespace: ns,
			Labels:    map[string]string{"adhar.io/managed-by": "adhar-storage"},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: qty,
				},
			},
		},
	}
	if storageClass != "" {
		sc := storageClass
		pvc.Spec.StorageClassName = &sc
	}

	clientset, err := getClientset()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout())
	defer cancel()

	created, err := clientset.CoreV1().PersistentVolumeClaims(ns).Create(ctx, pvc, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("creating PVC %s/%s: %w", ns, volumeName, err)
	}

	logger.Info(fmt.Sprintf("✅ PVC %s/%s created (phase: %s)", ns, created.Name, created.Status.Phase))
	return nil
}
