package controllers

import (
	"context"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/platform/controllers/adharplatform"
	"adhar-io/adhar/platform/controllers/custompackage"
	"adhar-io/adhar/platform/utils"

	"adhar-io/adhar/platform/controllers/gitrepository"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

func RunControllers(
	ctx context.Context,
	mgr manager.Manager,
	exitCh chan error,
	ctxCancel context.CancelFunc,
	exitOnSync bool,
	cfg v1alpha1.BuildCustomizationSpec,
	tmpDir string,
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
	// Start our manager in another goroutine
	logger.V(1).Info("starting manager")

	go func() {
		exitCh <- mgr.Start(ctx)
		close(exitCh)
	}()

	return nil
}
