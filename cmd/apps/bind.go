/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package apps

import (
	"context"
	"encoding/json"
	"fmt"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

var bindSecretFlag string

// bindCmd wires a backing service's connection Secret into an application, the
// way `cf bind-service` connects an app to a service instance. Adhar's
// CompositeDatabase/CompositeStorage compositions emit a `<service>-app` Secret
// with ready connection details (uri/host/port/...); binding injects it into the
// app's container as envFrom, then the app rolls to pick it up.
var bindCmd = &cobra.Command{
	Use:   "bind [app-name] [service-name]",
	Short: "Bind a backing service's connection Secret into an application",
	Long: `Inject a backing service's connection Secret into an application as
environment variables (envFrom), then roll the app so it picks them up.

By convention the Secret is '<service>-app' (produced by CompositeDatabase /
CompositeStorage); override with --secret.

Examples:
  adhar apps bind my-api my-postgres
  adhar apps bind my-api cache --secret cache-app --namespace platform-apps`,
	Args: cobra.ExactArgs(2),
	RunE: runBind,
}

func init() {
	bindCmd.Flags().StringVar(&bindSecretFlag, "secret", "", "Connection Secret name (default: <service>-app)")
}

func runBind(cmd *cobra.Command, args []string) error {
	appName, serviceName := args[0], args[1]
	ns := namespace
	if ns == "" {
		ns = "default"
	}
	secretName := bindSecretFlag
	if secretName == "" {
		secretName = serviceName + "-app"
	}

	kubeconfigPath, err := cmd.Root().PersistentFlags().GetString("kubeconfig")
	if err != nil {
		return fmt.Errorf("read kubeconfig flag: %w", err)
	}
	if kubeconfigPath == "" {
		kubeconfigPath = helpers.GetKubeConfigPath()
	}
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return fmt.Errorf("build kubeconfig: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("create kubernetes client: %w", err)
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	// Verify both the app Deployment and the connection Secret exist.
	dep, err := clientset.AppsV1().Deployments(ns).Get(ctx, appName, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return fmt.Errorf("deployment %q not found in namespace %q", appName, ns)
		}
		return fmt.Errorf("get deployment: %w", err)
	}
	if _, err := clientset.CoreV1().Secrets(ns).Get(ctx, secretName, metav1.GetOptions{}); err != nil {
		if k8serrors.IsNotFound(err) {
			return fmt.Errorf("connection Secret %q not found in namespace %q (is service %q provisioned?)", secretName, ns, serviceName)
		}
		return fmt.Errorf("get secret: %w", err)
	}

	containers := dep.Spec.Template.Spec.Containers
	if len(containers) == 0 {
		return fmt.Errorf("deployment %q has no containers to bind", appName)
	}
	// Idempotent: skip if already bound to this Secret.
	for _, ef := range containers[0].EnvFrom {
		if ef.SecretRef != nil && ef.SecretRef.Name == secretName {
			fmt.Println(helpers.CreateSuccess(fmt.Sprintf("%s is already bound to %s", appName, secretName)))
			return nil
		}
	}

	// Append an envFrom secretRef to the first container via strategic-merge patch.
	newEnvFrom := append(containers[0].EnvFrom, corev1.EnvFromSource{
		SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: secretName}},
	})
	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"template": map[string]interface{}{
				"spec": map[string]interface{}{
					"containers": []map[string]interface{}{
						{"name": containers[0].Name, "envFrom": newEnvFrom},
					},
				},
			},
		},
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("build patch: %w", err)
	}
	if _, err := clientset.AppsV1().Deployments(ns).Patch(ctx, appName, types.StrategicMergePatchType, body, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("bind service: %w", err)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("Bound %s → %s (secret %s) in namespace %s", appName, serviceName, secretName, ns)))
	fmt.Println(helpers.CreateMuted("   The app is rolling to pick up the connection details."))
	return nil
}
