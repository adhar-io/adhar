/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package apps

import (
	"context"
	"fmt"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// restartCmd performs a rolling restart of an application's Deployment.
var restartCmd = &cobra.Command{
	Use:   "restart [app-name]",
	Short: "Rolling-restart an application",
	Long: `Roll an application's Deployment (equivalent to kubectl rollout restart).
Pods are recreated one batch at a time so the app stays available.

Examples:
  adhar apps restart my-app
  adhar apps restart my-app --namespace=platform-apps`,
	Args: cobra.ExactArgs(1),
	RunE: runRestart,
}

func runRestart(cmd *cobra.Command, args []string) error {
	appName := args[0]
	ns := namespace
	if ns == "" {
		ns = "default"
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

	deployments := clientset.AppsV1().Deployments(ns)
	if _, err := deployments.Get(ctx, appName, metav1.GetOptions{}); err != nil {
		if k8serrors.IsNotFound(err) {
			return fmt.Errorf("deployment %q not found in namespace %q", appName, ns)
		}
		return fmt.Errorf("get deployment: %w", err)
	}

	// The canonical rolling-restart trigger: stamp the pod template with a
	// restart timestamp so the Deployment controller rolls new pods.
	patch := fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}}`,
		time.Now().Format(time.RFC3339))
	if _, err := deployments.Patch(ctx, appName, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("restart deployment: %w", err)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("Restarting %s in namespace %s", appName, ns)))
	fmt.Println(helpers.CreateMuted(fmt.Sprintf("   Track it: kubectl -n %s rollout status deployment/%s", ns, appName)))
	return nil
}
