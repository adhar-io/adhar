package adharplatform

import (
	"context"
	"embed"
	"fmt"
	"time"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"

	appsv1 "k8s.io/api/apps/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// NOTE: resources/cnpg/install.yaml is the pre-rendered CloudNativePG operator
// (same rendering as the data/cnpg stack package); gitea-db.yaml is the
// Gitea database Cluster used in HA mode.
//
//go:embed resources/cnpg
var cnpgFS embed.FS

const (
	cnpgOperatorDeployment = "cnpg-cloudnative-pg"
	cnpgClusterCRDName     = "clusters.postgresql.cnpg.io"
	// cnpgReadyTimeout bounds the in-reconcile wait for the operator; on
	// timeout the reconcile requeues and retries, so bootstrap converges even
	// on slow nodes.
	cnpgReadyTimeout = 3 * time.Minute
)

// ReconcileCNPG installs the CloudNativePG operator and the Gitea database
// cluster. It only runs in HA mode (wired in installCorePackagesSync), where
// Gitea's database moves from the chart-bundled PostgreSQL to a CNPG-managed
// cluster (roadmap P1.2b). In GitOps phase the data/cnpg package adopts the
// same operator resources via ArgoCD (both apply identical rendered content
// with server-side apply).
func (r *AdharPlatformReconciler) ReconcileCNPG(ctx context.Context, req ctrl.Request, resource *v1alpha1.AdharPlatform) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling CloudNativePG operator (HA mode)")

	installBytes, err := cnpgFS.ReadFile("resources/cnpg/install.yaml")
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reading cnpg install manifest: %w", err)
	}
	if err := r.applyManifest(ctx, installBytes, resource, "CloudNativePG install"); err != nil {
		return ctrl.Result{}, err
	}

	// The gitea-db Cluster CR needs the operator's CRD established and its
	// webhook serving; wait bounded, then requeue on timeout.
	if err := r.waitForCNPGReady(ctx); err != nil {
		return ctrl.Result{}, fmt.Errorf("waiting for CloudNativePG operator: %w", err)
	}

	dbBytes, err := cnpgFS.ReadFile("resources/cnpg/gitea-db.yaml")
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reading gitea-db manifest: %w", err)
	}
	if err := r.applyManifest(ctx, dbBytes, resource, "Gitea database cluster"); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("CloudNativePG reconciliation completed successfully")
	return ctrl.Result{}, nil
}

// waitForCNPGReady blocks until the CNPG Cluster CRD is established and the
// operator deployment has ready replicas, or the timeout elapses.
func (r *AdharPlatformReconciler) waitForCNPGReady(ctx context.Context) error {
	logger := log.FromContext(ctx)
	deadline := time.Now().Add(cnpgReadyTimeout)

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		crdEstablished := false
		var crd apiextensionsv1.CustomResourceDefinition
		if err := r.Get(ctx, types.NamespacedName{Name: cnpgClusterCRDName}, &crd); err == nil {
			for _, cond := range crd.Status.Conditions {
				if cond.Type == apiextensionsv1.Established && cond.Status == apiextensionsv1.ConditionTrue {
					crdEstablished = true
				}
			}
		}

		operatorReady := false
		var deploy appsv1.Deployment
		if err := r.Get(ctx, types.NamespacedName{Name: cnpgOperatorDeployment, Namespace: globals.AdharSystemNamespace}, &deploy); err == nil {
			operatorReady = deploy.Status.ReadyReplicas > 0
		}

		if crdEstablished && operatorReady {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("operator not ready after %s (CRD established: %t, operator ready: %t)", cnpgReadyTimeout, crdEstablished, operatorReady)
		}
		logger.V(1).Info("waiting for CloudNativePG operator", "crdEstablished", crdEstablished, "operatorReady", operatorReady)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}
