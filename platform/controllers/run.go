package controllers

import (
	"context"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/platform/controllers/adharplatform"
	"adhar-io/adhar/platform/controllers/custompackage"
	"adhar-io/adhar/platform/controllers/dataplane"
	"adhar-io/adhar/platform/utils"

	"adhar-io/adhar/platform/controllers/gitrepository"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// compositeClusterCRDPresent reports whether the CompositeCluster CRD (a
// Crossplane XRD the DataPlane controller watches) is registered on the cluster.
// The DataPlane controller must not be wired onto the manager before it exists,
// or the manager fails its cache-sync startup (see RunControllers).
func compositeClusterCRDPresent(mgr manager.Manager) bool {
	_, err := mgr.GetRESTMapper().RESTMapping(
		schema.GroupKind{Group: "platform.adhar.io", Kind: "CompositeCluster"},
		"v1alpha1",
	)
	return err == nil
}

func RunControllers(
	ctx context.Context,
	mgr manager.Manager,
	exitCh chan error,
	ctxCancel context.CancelFunc,
	exitOnSync bool,
	cfg v1alpha1.BuildCustomizationSpec,
	tmpDir string,
	stackDir string,
) error {
	logger := log.FromContext(ctx)

	repoMap := utils.NewRepoLock()

	// Run AdharPlatform controller
	if err := (&adharplatform.AdharPlatformReconciler{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		ExitOnSync: exitOnSync,
		CancelFunc: ctxCancel,
		Config:     cfg,
		TempDir:    tmpDir,
		StackDir:   stackDir,
		RepoMap:    repoMap,
	}).SetupWithManager(mgr); err != nil {
		logger.Error(err, "unable to create adharplatform controller")
		return err
	}

	err := (&gitrepository.GitRepositoryReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		Recorder:        mgr.GetEventRecorderFor("gitrepository-controller"),
		Config:          cfg,
		GitProviderFunc: gitrepository.GetGitProvider,
		TempDir:         tmpDir,
		RepoMap:         repoMap,
	}).SetupWithManager(mgr, nil)
	if err != nil {
		logger.Error(err, "unable to create repo controller")
	}

	err = (&custompackage.CustomPackageReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("custompackage-controller"),
		TempDir:  tmpDir,
		RepoMap:  repoMap,
	}).SetupWithManager(mgr)
	if err != nil {
		logger.Error(err, "unable to create custom package controller")
	}

	// The DataPlane controller Owns() CompositeCluster — a Crossplane XRD whose
	// CRD does not exist until the control plane is installed (late in, or after,
	// bootstrap). controller-runtime blocks manager startup on every watched
	// informer's cache sync and, if one never syncs, fails mgr.Start() at its
	// WaitForCacheSyncTimeout (2m by default). During `adhar up` that killed the
	// whole manager two minutes in — before Gitea was ready and the platform
	// ApplicationSet was applied — leaving ArgoCD empty while the CLI still
	// reported success. So register the DataPlane controller only once its CRD is
	// present. A fresh bootstrap has no DataPlanes to reconcile; the persistent
	// in-cluster manager (started after Crossplane is installed) picks it up.
	if compositeClusterCRDPresent(mgr) {
		if err := (&dataplane.DataPlaneReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
		}).SetupWithManager(mgr); err != nil {
			logger.Error(err, "unable to create dataplane controller")
		}
	} else {
		logger.Info("CompositeCluster CRD not present; skipping DataPlane controller until Crossplane is installed")
	}
	// Start our manager in another goroutine
	logger.V(1).Info("starting manager")

	go func() {
		exitCh <- mgr.Start(ctx)
		close(exitCh)
	}()

	return nil
}
