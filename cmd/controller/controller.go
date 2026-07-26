/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package controller implements the `adhar controller` command: the long-running
// controller-manager mode used when the platform controllers run in-cluster as a
// Deployment (installed by `adhar up --in-cluster`) instead of inside the CLI
// process. It reconciles AdharPlatform, GitRepository and CustomPackage
// continuously and never exits on sync.
package controller

import (
	"context"
	"fmt"
	stdlog "log"
	"os"
	"time"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/controllers"
	"adhar-io/adhar/platform/k8s"

	"github.com/go-logr/stdr"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var (
	metricsBindAddress string
	probeBindAddress   string
	leaderElect        bool
	stackDir           string
	platformName       string
	namespace          string
)

// ControllerCmd represents the controller command
var ControllerCmd = &cobra.Command{
	Use:   "controller",
	Short: "🎛️ Run the platform controller manager (in-cluster mode)",
	Long: `🎛️ **Adhar Platform Controller Manager**

Runs the AdharPlatform, GitRepository and CustomPackage controllers as a
long-lived manager process. This is the entrypoint used by the in-cluster
controller Deployment (installed with 'adhar up --in-cluster') so the platform
keeps reconciling continuously after the CLI exits.

The manager reads its build configuration (host, port, TLS settings) from the
existing AdharPlatform resource. GitOps repository seeding is a bootstrap
(CLI) concern: this mode reconciles an already-bootstrapped platform.`,
	RunE:         runController,
	SilenceUsage: true,
}

func init() {
	ControllerCmd.Flags().StringVar(&metricsBindAddress, "metrics-bind-address", "0", "Address for the metrics endpoint ('0' disables it)")
	ControllerCmd.Flags().StringVar(&probeBindAddress, "health-probe-bind-address", ":8081", "Address for the /healthz and /readyz probe endpoints")
	ControllerCmd.Flags().BoolVar(&leaderElect, "leader-elect", true, "Enable leader election so only one manager reconciles at a time")
	ControllerCmd.Flags().StringVar(&stackDir, "stack-dir", "", "Optional path to a platform stack directory for GitOps repo seeding (bootstrap is normally CLI-owned)")
	ControllerCmd.Flags().StringVar(&platformName, "platform-name", globals.DefaultClusterName, "Name of the AdharPlatform resource to reconcile")
	ControllerCmd.Flags().StringVar(&namespace, "namespace", globals.AdharSystemNamespace, "Namespace of the AdharPlatform resource and leader-election lease")
}

func runController(cmd *cobra.Command, args []string) error {
	// In-cluster mode logs to stderr unconditionally: logs are the only UI.
	stdr.SetVerbosity(1)
	ctrllog.SetLogger(stdr.New(stdlog.New(os.Stderr, "", stdlog.LstdFlags)))

	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("loading kubernetes config: %w", err)
	}
	scheme := k8s.GetScheme()

	mgr, err := manager.New(cfg, manager.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsBindAddress,
		},
		HealthProbeBindAddress:  probeBindAddress,
		LeaderElection:          leaderElect,
		LeaderElectionID:        "adhar-platform-controller.adhar.io",
		LeaderElectionNamespace: namespace,
		GracefulShutdownTimeout: func() *time.Duration {
			d := 30 * time.Second
			return &d
		}(),
	})
	if err != nil {
		return fmt.Errorf("creating controller manager: %w", err)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("adding healthz check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("adding readyz check: %w", err)
	}

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	// The reconcilers take the build customization (host/port/TLS) as static
	// config; in-cluster it comes from the AdharPlatform resource the CLI
	// created at bootstrap. A missing resource is tolerated: reconciliation
	// starts once one is created.
	buildCfg, err := loadBuildCustomization(ctx, cfg, scheme)
	if err != nil {
		return err
	}

	tmpDir, err := os.MkdirTemp("", "adhar-controller-")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	exitCh := make(chan error)
	// exitOnSync is always false in-cluster: this manager's purpose is
	// continuous reconciliation.
	if err := controllers.RunControllers(ctx, mgr, exitCh, cancel, false, buildCfg, tmpDir, stackDir); err != nil {
		return fmt.Errorf("starting controllers: %w", err)
	}
	return <-exitCh
}

// loadBuildCustomization reads the build configuration from the existing
// AdharPlatform resource, if present.
func loadBuildCustomization(ctx context.Context, cfg *rest.Config, scheme *runtime.Scheme) (v1alpha1.BuildCustomizationSpec, error) {
	directClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return v1alpha1.BuildCustomizationSpec{}, fmt.Errorf("creating kubernetes client: %w", err)
	}
	var platform v1alpha1.AdharPlatform
	err = directClient.Get(ctx, types.NamespacedName{Name: platformName, Namespace: namespace}, &platform)
	switch {
	case errors.IsNotFound(err):
		ctrllog.Log.Info("AdharPlatform resource not found yet; starting with empty build configuration",
			"name", platformName, "namespace", namespace)
		return v1alpha1.BuildCustomizationSpec{}, nil
	case err != nil:
		return v1alpha1.BuildCustomizationSpec{}, fmt.Errorf("reading AdharPlatform %s/%s: %w", namespace, platformName, err)
	default:
		return platform.Spec.BuildCustomization, nil
	}
}
