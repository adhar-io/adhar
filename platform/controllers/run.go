package controllers

import (
	"context"
	"os"
	"time"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/batcharchive"
	"adhar-io/adhar/platform/controllers/adharplatform"
	"adhar-io/adhar/platform/controllers/autoscaler"
	batchobserver "adhar-io/adhar/platform/controllers/batch"
	"adhar-io/adhar/platform/controllers/custompackage"
	"adhar-io/adhar/platform/controllers/dataplane"
	"adhar-io/adhar/platform/events"
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
	appsTimeout time.Duration,
	cfg v1alpha1.BuildCustomizationSpec,
	tmpDir string,
	stackDir string,
) error {
	logger := log.FromContext(ctx)

	repoMap := utils.NewRepoLock()

	// The event bus. Opt-in: with ADHAR_EVENTS_BRIDGE_URL unset this publisher
	// is inert, which is the normal state locally where Kafka is not enabled.
	// Closed when the manager's context ends so the last events of a run — the
	// ones that say how it finished — are flushed rather than lost.
	// The HOST identifies this platform instance — there is no cluster name in
	// the build spec, and the host is what makes two Adhar platforms publishing
	// into one aggregated stream distinguishable.
	bus := events.NewFromEnv(cfg.Host, "adharplatform-controller")
	if bus.Enabled() {
		logger.Info("platform events will be published", "bridge", os.Getenv(events.EnvBridgeURL))
	}
	go func() {
		<-ctx.Done()
		// A bounded drain: shutdown must not wait on an unhealthy broker.
		flush, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		bus.Close(flush)
	}()

	// Run AdharPlatform controller
	if err := (&adharplatform.AdharPlatformReconciler{
		Client:              mgr.GetClient(),
		Scheme:              mgr.GetScheme(),
		ExitOnSync:          exitOnSync,
		AppsConvergeTimeout: appsTimeout,
		CancelFunc:          ctxCancel,
		Config:              cfg,
		TempDir:             tmpDir,
		StackDir:            stackDir,
		RepoMap:             repoMap,
		Events:              bus,
		Recorder:            mgr.GetEventRecorder("adharplatform-controller"),
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

	// The DataPlane controller watches only core types (its managed infra is
	// polled), so it is safe to register before Crossplane/ArgoCD exist.
	if err := (&dataplane.DataPlaneReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		logger.Error(err, "unable to create dataplane controller")
	}

	// The node autoscaler is registered unconditionally: it watches only core
	// types (Pods, Nodes) and no-ops unless the AdharPlatform enables
	// autoscaling, so it is safe before any CRD or cloud credential exists.
	if err := (&autoscaler.Reconciler{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		Recorder:   mgr.GetEventRecorderFor("node-autoscaler"),
		Namespace:  globals.AdharSystemNamespace,
		Events:     bus,
		NodeUsage:  autoscaler.KubeletUsageReader(mgr.GetConfig()),
		RESTConfig: mgr.GetConfig(),
	}).SetupWithManager(mgr); err != nil {
		logger.Error(err, "unable to create node autoscaler controller")
	}

	// The batch observer (design 0025 §12): adhar.batch events, dependsOn and
	// the finishBy SLA. Only in the long-running manager — a bootstrap process
	// that exits on sync must not own a duty that starts runs, and two
	// observers would both act on one transition. It watches Jobs and pods
	// from the start and adds its BatchJob and Volcano watches only once
	// discovery shows those CRDs, so it is safe on a cluster that has neither.
	// Its own publisher, so its events carry the observer as their source.
	if !exitOnSync {
		batchBus := events.NewFromEnv(cfg.Host, "batch-observer")
		go func() {
			<-ctx.Done()
			flush, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			batchBus.Close(flush)
		}()
		// The run-summary archive (Phase 4): opt-in by ADHAR_BATCH_ARCHIVE_URL,
		// nil (nothing archived) otherwise. Its credential is read live through
		// the uncached reader, never a Secret informer.
		var archive batchobserver.Archiver
		if w := batchobserver.NewArchive(mgr.GetAPIReader(), globals.AdharSystemNamespace); w != nil {
			logger.Info("batch run summaries will be archived", "store", os.Getenv(batcharchive.EnvURL))
			archive = w
			go func() {
				<-ctx.Done()
				flush, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancel()
				w.Close(flush)
			}()
		}
		observer := &batchobserver.Reconciler{
			Events: batchBus, Archive: archive,
			Source: events.FromEnv(cfg.Host, "batch-observer").Source,
		}
		if err := observer.SetupWithManager(mgr); err != nil {
			logger.Error(err, "unable to create batch observer")
		}
	}

	// Start our manager in another goroutine
	logger.V(1).Info("starting manager")

	go func() {
		exitCh <- mgr.Start(ctx)
		close(exitCh)
	}()

	return nil
}
