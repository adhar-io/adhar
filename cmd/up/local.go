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

package up

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/controllers"
	"adhar-io/adhar/platform/k8s"
	"adhar-io/adhar/platform/logger"

	"adhar-io/adhar/platform/providers/kind"

	"github.com/go-logr/logr"
	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	ctrl "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// LocalOptions holds configuration for local provisioning
type LocalOptions struct {
	Name                      string
	TemplateData              v1alpha1.BuildCustomizationSpec
	RecreateCluster           bool
	DevPassword               bool
	KubeVersion               string
	ExtraPortsMapping         string
	KindConfigPath            string
	KubeConfigPath            string
	ExtraPackages             []string
	RegistryConfig            []string
	PackageCustomizationFiles []string
	NoExit                    bool
	Protocol                  string
	Host                      string
	IngressHost               string
	Port                      string
	PathRouting               bool
	Verbose                   bool
	ProgressUI                bool
	CustomPackageFiles        []string
	CustomPackageDirs         []string
	CustomPackageUrls         []string
	PackageCustomization      map[string]v1alpha1.PackageCustomization
	ExitOnSync                bool
	Scheme                    *runtime.Scheme
	CancelFunc                context.CancelFunc
	Progress                  *helpers.ProgressTracker
}

// LocalProvisioner handles local development environment creation
type LocalProvisioner struct {
	options *LocalOptions
}

func isLocalPlatformReady(ctx context.Context, kubeClient client.Client) (bool, error) {
	requiredDeployments := []types.NamespacedName{
		{Name: "gitea", Namespace: globals.AdharSystemNamespace},
		{Name: "argo-cd-argocd-server", Namespace: globals.AdharSystemNamespace},
		{Name: "cilium-operator", Namespace: globals.AdharSystemNamespace},
	}

	for _, nn := range requiredDeployments {
		var dep appsv1.Deployment
		if err := kubeClient.Get(ctx, nn, &dep); err != nil {
			return false, nil
		}
		if dep.Status.ReadyReplicas < 1 || dep.Status.AvailableReplicas < 1 {
			return false, nil
		}
	}

	// Gateway service presence is required for UI access.
	var gwSvc corev1.Service
	if err := kubeClient.Get(ctx, types.NamespacedName{Name: "cilium-gateway-adhar-gateway", Namespace: globals.AdharSystemNamespace}, &gwSvc); err != nil {
		return false, nil
	}

	// Ensure GitOps bootstrap finished so ArgoCD can sync from Gitea
	var platform v1alpha1.AdharPlatform
	if err := kubeClient.Get(ctx, types.NamespacedName{Name: globals.DefaultClusterName, Namespace: globals.AdharSystemNamespace}, &platform); err != nil {
		return false, nil
	}

	return platform.Status.Gitea.RepositoriesCreated, nil
}

// NewLocalProvisioner creates a new LocalProvisioner
func NewLocalProvisioner(options *LocalOptions) *LocalProvisioner {
	return &LocalProvisioner{options: options}
}

// LocalProvisioner handles local development environment creation
func (lp *LocalProvisioner) Provision(ctx context.Context, args []string) error {

	progress := lp.options.Progress
	startStep := func(idx int, desc string) {
		if progress != nil {
			if desc != "" {
				progress.StartStep(idx, desc)
			} else {
				progress.StartStep(idx, progress.Steps[idx].Description)
			}
		}
	}
	completeStep := func(idx int) {
		if progress != nil {
			progress.CompleteStep(idx)
		}
	}
	failStep := func(idx int, err error) {
		if progress != nil {
			progress.FailStep(idx, err)
		}
	}

	startStep(0, "Creating Kind cluster")
	logger.Info("Creating kind cluster")
	if err := lp.ReconcileKindCluster(ctx, recreateCluster); err != nil {
		failStep(0, err)
		return err
	}
	completeStep(0)

	logger.Info("Getting Kube config")
	kubeConfig, err := lp.GetKubeConfig()
	if err != nil {
		failStep(1, err)
		return err
	}

	logger.Info("Getting Kube client")
	kubeClient, err := lp.GetKubeClient(kubeConfig)
	if err != nil {
		failStep(1, err)
		return err
	}

	startStep(1, "Installing CRDs")
	logger.Info("Adding CRDs to the cluster")
	if err := lp.ReconcileCRDs(ctx, kubeClient); err != nil {
		failStep(1, err)
		return err
	}
	completeStep(1)

	logger.Info("Creating controller manager")

	// Set up controller-runtime logger
	ctrl.SetLogger(logr.Discard())
	// Silence client-go reflector warnings during planned shutdown (ExitOnSync cancels context)
	klog.SetOutput(io.Discard)
	klog.SetLogger(logr.Discard())
	klog.LogToStderr(false)

	// Create controller manager with graceful shutdown timeout
	mgr, err := manager.New(kubeConfig, manager.Options{
		Scheme: lp.options.Scheme,
		Metrics: server.Options{
			BindAddress: "0",
		},
		GracefulShutdownTimeout: func() *time.Duration {
			// ExitOnSync cancels the manager context once the platform is usable.
			// Some caches/watches can take a bit longer than 5s to shut down cleanly,
			// so keep this reasonably high and treat shutdown timeout as non-fatal.
			d := 30 * time.Second
			return &d
		}(),
	})
	if err != nil {
		logger.Error("Error creating controller manager", err, map[string]interface{}{})
		return err
	}

	dir, err := os.MkdirTemp("", fmt.Sprintf("%s-%s-", globals.ProjectName, lp.options.Name))
	if err != nil {
		logger.Error("creating temp dir", err, map[string]interface{}{})
		return err
	}
	defer func() {
		_ = os.RemoveAll(dir)
	}()
	logger.Info("Created temp directory for cloning repositories")

	startStep(2, "Installing CoreDNS and certificates")
	logger.Info("Setting up CoreDNS")
	err = kind.SetupCoreDNS(ctx, kubeClient, lp.options.Scheme, lp.options.TemplateData)
	if err != nil {
		failStep(2, err)
		return err
	}

	logger.Info("Setting up TLS certificate")
	cert, err := kind.SetupSelfSignedCertificate(ctx, kubeClient, lp.options.TemplateData)
	if err != nil {
		failStep(2, err)
		return err
	}
	lp.options.TemplateData.SelfSignedCert = string(cert)
	completeStep(2)

	managerExit := make(chan error)

	startStep(3, "Starting controllers")
	logger.Info("Running controllers")
	if err := lp.RunControllers(ctx, mgr, managerExit, dir); err != nil {
		logger.Error("Error running controllers", err, map[string]interface{}{})
		failStep(3, err)
		return err
	}
	completeStep(3)

	// Wait a moment for the controller manager to start and be ready
	logger.Info("Waiting for controller manager to start")
	time.Sleep(1 * time.Second)

	startStep(4, "Creating AdharPlatform resource")
	localBuild := v1alpha1.AdharPlatform{
		ObjectMeta: metav1.ObjectMeta{
			Name:      lp.options.Name,
			Namespace: globals.AdharSystemNamespace,
		},
	}

	cliStartTime := time.Now().Format(time.RFC3339Nano)

	logger.Info("Creating adharplatform resource")
	created, err := controllerutil.CreateOrUpdate(ctx, kubeClient, &localBuild, func() error {
		if localBuild.ObjectMeta.Annotations == nil {
			localBuild.ObjectMeta.Annotations = map[string]string{}
		}
		localBuild.ObjectMeta.Annotations[v1alpha1.CliStartTimeAnnotation] = cliStartTime
		localBuild.Spec = v1alpha1.AdharPlatformSpec{
			BuildCustomization: lp.options.TemplateData,
			PackageConfigs: v1alpha1.PackageConfigsSpec{
				Argo: v1alpha1.ArgoPackageConfigSpec{
					Enabled: true,
				},
				EmbeddedArgoApplications: v1alpha1.EmbeddedArgoApplicationsPackageConfigSpec{
					Enabled: true,
				},
				CustomPackageDirs:        lp.options.CustomPackageDirs,
				CustomPackageUrls:        lp.options.CustomPackageUrls,
				CorePackageCustomization: lp.options.PackageCustomization,
			},
		}

		return nil
	})
	if err != nil {
		failStep(4, err)
		return fmt.Errorf("creating AdharPlatform resource: %w", err)
	}

	if created == controllerutil.OperationResultCreated {
		logger.Info("AdharPlatform resource created - controller will reconcile automatically")
	} else if created == controllerutil.OperationResultUpdated {
		logger.Info("AdharPlatform resource updated - controller will reconcile automatically")
	} else {
		logger.Info("AdharPlatform resource unchanged")
	}
	completeStep(4)

	// The controller will automatically reconcile when it sees the resource
	// Wait a moment to ensure the controller manager has started and the watch is active
	// This ensures the resource change is picked up and triggers reconciliation
	logger.Info("Waiting for controller to start watching and trigger reconciliation")
	time.Sleep(3 * time.Second)

	// GitOps repositories will be set up by the AdharPlatform controller

	// Wait for the controller manager to either exit (error) or cancel the context (ExitOnSync).
	// IMPORTANT: never block indefinitely waiting for manager shutdown; it can hang on cache shutdown
	// depending on cluster conditions. `adhar up` should complete once the controller signals done.
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	shutdownTimeout := 20 * time.Second
	startStep(5, "Waiting for platform readiness and GitOps sync")

	for {
		select {
		case mgrErr := <-managerExit:
			// If we cancelled the context intentionally (ExitOnSync), manager shutdown errors are non-fatal.
			// controller-runtime may return "failed waiting for all runnables to end within grace period"
			// if shutdown exceeds GracefulShutdownTimeout.
			if ctx.Err() != nil && lp.options.ExitOnSync {
				completeStep(5)
				return nil
			}
			if mgrErr != nil && mgrErr != context.Canceled {
				failStep(5, mgrErr)
				return mgrErr
			}
			completeStep(5)
			return nil
		case <-ctx.Done():
			// Context was cancelled - this is expected when ExitOnSync is enabled.
			// Give the manager a bounded window to exit cleanly, then return success.
			select {
			case mgrErr := <-managerExit:
				if lp.options.ExitOnSync {
					// Non-fatal in ExitOnSync mode (see comment above).
					completeStep(5)
					return nil
				}
				if mgrErr != nil && mgrErr != context.Canceled {
					failStep(5, mgrErr)
					return mgrErr
				}
				completeStep(5)
				return nil
			case <-time.After(shutdownTimeout):
				logger.Info("Controller shutdown timed out (non-fatal) - exiting CLI")
				completeStep(5)
				return nil
			}
		case <-ticker.C:
			logger.Info("Waiting for platform controller to finish initial sync...")
			// Root fix: don't rely solely on controller calling ctxCancel (it can fail to do so if reconcile stalls).
			// If core services + gateway are ready, we can exit successfully.
			ready, _ := isLocalPlatformReady(ctx, kubeClient)
			if ready {
				logger.Info("Platform is ready (detected by CLI); exiting")
				lp.options.CancelFunc()
				completeStep(5)
			}
		}
	}
}

// runPreFlightChecks validates system requirements

// createLocalDevelopmentCluster creates a local Kind cluster using the LocalProvisioner
func createLocalDevelopmentCluster(ctx context.Context, cmd *cobra.Command, args []string, ctxCancel context.CancelFunc) error {
	// Get kubeconfig path
	kubeConfigPath := filepath.Join(homedir.HomeDir(), ".kube", "config")

	protocol = strings.ToLower(protocol)
	host = strings.ToLower(host)
	if ingressHost == "" {
		ingressHost = host
	}

	// Validate arguments and set up build configuration
	if err := validate(); err != nil {
		return err
	}

	// Suppress noisy info logs while progress UI is active to avoid multiple renders
	prevLogLevel := logger.CLILogLevel
	if !verbose {
		_ = logger.SetLogLevel("error")
	}
	defer func() {
		if !verbose && prevLogLevel != "" {
			_ = logger.SetLogLevel(prevLogLevel)
		}
	}()

	var localFiles []string
	var localDirs []string
	var remotePaths []string

	if len(extraPackages) > 0 {
		r, f, d, pErr := helpers.ParsePackageStrings(extraPackages)
		if pErr != nil {
			return pErr
		}
		localFiles = f
		localDirs = d
		remotePaths = r
	}

	o := make(map[string]v1alpha1.PackageCustomization)
	for i := range packageCustomizationFiles {
		c, pErr := getPackageCustomFile(packageCustomizationFiles[i])
		if pErr != nil {
			return pErr
		}
		o[c.Name] = c
	}

	// Check if no-exit flag is set (defined in up.go)
	noExit, _ := cmd.Flags().GetBool("no-exit")
	exitOnSync := true // Exit after ApplicationSet is applied, GitOps will continue via ArgoCD
	if cmd.Flags().Changed("no-exit") {
		exitOnSync = !noExit
	}

	// If registry-config is unset we pass nil
	// If registry-config is change (--registry-config=foo) we pass the new value
	// If registry-config is set but unchanged (--registry-confg) we pass ""
	maybeRegistryConfig := []string{}
	if cmd.Flags().Changed("registry-config") {
		maybeRegistryConfig = registryConfig
	}

	progressTracker := helpers.NewStyledProgressTracker(
		helpers.TitleStyle.Render("Provisioning Adhar platform"),
		[]string{
			"Create Kind cluster",
			"Install CRDs",
			"Configure DNS & TLS",
			"Start controllers",
			"Create AdharPlatform",
			"Waiting for readiness",
		},
		[]string{
			"Prepare local cluster and ports",
			"Install platform CRDs",
			"Install CoreDNS and certificates",
			"Launch controller manager",
			"Bootstrap GitOps resources",
			"Wait for services and GitOps sync",
		},
	)
	progressTracker.ShowSpinner = true

	// Create LocalProvisioner with options
	options := &LocalOptions{
		Name:                      globals.DefaultClusterName,
		RecreateCluster:           recreateCluster,
		KubeConfigPath:            kubeConfigPath,
		DevPassword:               devPassword,
		KubeVersion:               kubeVersion,
		ExtraPortsMapping:         extraPortsMapping,
		KindConfigPath:            kindConfigPath,
		ExtraPackages:             extraPackages,
		RegistryConfig:            maybeRegistryConfig,
		PackageCustomizationFiles: packageCustomizationFiles,
		NoExit:                    noExit,
		Protocol:                  protocol,
		Host:                      host,
		IngressHost:               ingressHost,
		Port:                      port,
		PathRouting:               pathRouting,
		Verbose:                   verbose,
		ProgressUI:                true,
		CustomPackageFiles:        localFiles,
		CustomPackageDirs:         localDirs,
		CustomPackageUrls:         remotePaths,
		ExitOnSync:                exitOnSync,
		PackageCustomization:      o,
		Scheme:                    k8s.GetScheme(),
		CancelFunc:                ctxCancel,
		TemplateData: v1alpha1.BuildCustomizationSpec{
			Protocol:       protocol,
			Host:           host,
			IngressHost:    ingressHost,
			Port:           port,
			UsePathRouting: pathRouting,
			StaticPassword: devPassword,
		},
		Progress: progressTracker,
	}

	provisioner := NewLocalProvisioner(options)

	// If dry run, show what would be provisioned
	if dryRun {
		// Create a simple env config for dry run display
		envConfig := &config.ResolvedEnvironmentConfig{
			Name:             globals.DefaultClusterName,
			ResolvedProvider: "kind",
			ResolvedRegion:   "local",
			ResolvedType:     config.EnvironmentTypeNonProduction,
			ResolvedClusterConfig: []config.KeyValueConfig{
				{Key: "kubeVersion", Value: kubeVersion},
				{Key: "controlPlaneReplicas", Value: "1"},
				{Key: "workerReplicas", Value: "0"},
			},
			GlobalSettings: &config.GlobalSettings{
				AdharContext: "provider-mode",
				DefaultHost:  globals.DefaultHostName,
				EnableHAMode: false,
				Email:        "admin@" + globals.DefaultHostName,
			},
		}
		return showLocalDryRunInfo(envConfig)
	}

	// Start the provisioning process
	logger.GetLogger().StartOperation("Local Development Cluster", "Creating Kind cluster with platform services")

	// Use the LocalProvisioner to create the complete environment
	if err := provisioner.Provision(ctx, args); err != nil {
		logger.Error("Local cluster provisioning failed", err, map[string]interface{}{
			"cluster":  globals.DefaultClusterName,
			"provider": "kind",
		})
		return fmt.Errorf("failed to provision local development cluster: %w", err)
	}

	logger.GetLogger().FinishOperation("Local Development Cluster", "Platform ready for development")

	// Check if the context has been cancelled
	if cmd.Context().Err() != nil {
		// Context was cancelled - this is expected when ExitOnSync is enabled
		// and the controller has finished provisioning. Return success.
		logger.Info("Context cancelled - platform provisioning completed successfully")
		printSuccessMsg()
		return nil
	}

	// Print success message
	printSuccessMsg()

	return nil
}

func showLocalDryRunInfo(envConfig *config.ResolvedEnvironmentConfig) error {
	fmt.Printf("\n%s\n", helpers.BoldStyle.Render("🔍 Dry Run - Local Development Preview"))
	fmt.Printf("┌─────────────────────────────────────────────┐\n")
	fmt.Printf("│ Environment: %-30s │\n", envConfig.Name)
	fmt.Printf("│ Provider:    %-30s │\n", envConfig.ResolvedProvider)
	fmt.Printf("│ Region:      %-30s │\n", envConfig.ResolvedRegion)
	fmt.Printf("│ Type:        %-30s │\n", envConfig.ResolvedType)
	fmt.Printf("└─────────────────────────────────────────────┘\n")

	fmt.Printf("\nPlatform Configuration:\n")
	fmt.Printf("  Host:        %s\n", envConfig.GlobalSettings.DefaultHost)
	// Protocol/Port/PathRouting are not in envConfig directly for local dry-run; show sensible defaults
	fmt.Printf("  Protocol:    https\n")
	fmt.Printf("  Port:        8443\n")
	fmt.Printf("  Path Routing: %v\n", true)

	if len(envConfig.ResolvedClusterConfig) > 0 {
		fmt.Printf("\nKind Cluster Configuration:\n")
		for _, cfg := range envConfig.ResolvedClusterConfig {
			switch cfg.Key {
			case "kubeVersion":
				fmt.Printf("  Kubernetes Version: %s\n", cfg.Value)
			case "extraPorts":
				fmt.Printf("  Extra Ports: %s\n", cfg.Value)
			case "configPath":
				fmt.Printf("  Config Path: %s\n", cfg.Value)
			default:
				fmt.Printf("  %s: %s\n", cfg.Key, cfg.Value)
			}
		}
	}

	fmt.Printf("\nCore Services:\n")
	fmt.Printf("  ArgoCD:      true\n")
	fmt.Printf("  Gitea:       true\n")
	fmt.Printf("  Cilium:      true\n")
	fmt.Printf("  Gateway API: true\n")

	if len(envConfig.ResolvedClusterConfig) > 0 {
		fmt.Printf("\nKind Cluster Configuration:\n")
		for _, cfg := range envConfig.ResolvedClusterConfig {
			switch cfg.Key {
			case "kubeVersion":
				fmt.Printf("  Kubernetes Version: %s\n", cfg.Value)
			case "extraPorts":
				fmt.Printf("  Extra Ports: %s\n", cfg.Value)
			case "configPath":
				fmt.Printf("  Config Path: %s\n", cfg.Value)
			default:
				fmt.Printf("  %s: %s\n", cfg.Key, cfg.Value)
			}
		}
	}

	fmt.Printf("\n%s\n", helpers.CodeStyle.Render("No changes will be made in dry-run mode"))
	return nil
}

// printSuccessMsg prints success message for local development cluster
func printSuccessMsg() {
	var argoURL string

	// For local development (Kind clusters), use clean URLs without ports
	proxy := behindProxy()
	if proxy {
		argoURL = fmt.Sprintf("https://%s/argocd", host)
	} else if host == globals.DefaultHostName { // adhar.localtest.me (Kind cluster)
		// Kind clusters use direct port mapping, show clean URLs
		argoURL = fmt.Sprintf("https://%s/argocd", host)
	} else {
		// Production clusters or custom domains may need port specification
		if pathRouting {
			argoURL = fmt.Sprintf("%s://%s:%s/argocd", protocol, host, port)
		} else {
			argoURL = fmt.Sprintf("%s://argocd.%s:%s", protocol, host, port)
		}
	}

	fmt.Print("\n\n########################### Finished Creating Adhar IDP Successfully! ############################\n\n")
	fmt.Printf("🎉 %s\n\n", helpers.BoldStyle.Render("Local Development Platform Ready!"))
	fmt.Printf("Your Adhar platform includes:\n")
	fmt.Printf("  ✅ Kind Kubernetes cluster\n")
	fmt.Printf("  ✅ Cilium CNI for secure networking\n")
	fmt.Printf("  ✅ Cilium Gateway API for traffic routing\n")
	fmt.Printf("  ✅ ArgoCD for GitOps deployments\n")
	fmt.Printf("  ✅ Gitea for Git repository hosting\n")
	fmt.Printf("  ✅ Platform observability stack\n\n")
	fmt.Printf("%s\n", helpers.TitleStyle.Render("Quick Access:"))
	fmt.Printf("  • %s %s\n", helpers.HighlightStyle.Render("ArgoCD Dashboard:"), helpers.HighlightStyle.Render(argoURL))
	fmt.Printf("  • %s %s\n", helpers.HighlightStyle.Render("Username:"), helpers.InfoStyle.Render("admin"))
	fmt.Printf("  • %s %s\n\n", helpers.HighlightStyle.Render("Password:"), helpers.InfoStyle.Render("Run `adhar get secrets -p argocd`"))

	fmt.Printf("%s\n", helpers.TitleStyle.Render("Next Steps:"))
	fmt.Printf("  1) %s\n", helpers.InfoStyle.Render("Deploy your first application via ArgoCD"))
	fmt.Printf("  2) %s\n", helpers.InfoStyle.Render("Push code to the integrated Gitea instance"))
	fmt.Printf("  3) %s\n", helpers.InfoStyle.Render("Use `adhar get secrets` to retrieve service credentials"))
	fmt.Printf("  4) %s\n", helpers.InfoStyle.Render("Run `adhar get status` to monitor platform health"))
	fmt.Printf("  5) %s\n\n", helpers.InfoStyle.Render("Destroy cluster: adhar down"))
}

// behindProxy checks if we are in codespaces
func behindProxy() bool {
	// check if we are in codespaces: https://docs.github.com/en/codespaces/developing-in-a-codespace
	_, ok := os.LookupEnv("CODESPACES")
	return ok
}

// validate validates the up command arguments
func validate() error {

	// Add check for host
	if host == "" {
		return fmt.Errorf("host cannot be empty")
	}

	_, err := url.Parse(fmt.Sprintf("%s://%s:%s", protocol, host, port))
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}

	for i := range packageCustomizationFiles {
		_, pErr := getPackageCustomFile(packageCustomizationFiles[i])
		if pErr != nil {
			return pErr
		}
	}

	_, _, _, err = helpers.ParsePackageStrings(extraPackages)
	return err
}

func getPackageCustomFile(input string) (v1alpha1.PackageCustomization, error) {
	// the format should be `<package-name>:<path-to-file>`
	s := strings.Split(input, ":")
	if len(s) != 2 {
		return v1alpha1.PackageCustomization{}, fmt.Errorf("ensure %s is formatted as <package-name>:<path-to-file>", input)
	}

	paths, err := helpers.GetAbsFilePaths([]string{s[1]}, false)
	if err != nil {
		return v1alpha1.PackageCustomization{}, err
	}

	err = helpers.ValidateKubernetesYamlFile(paths[0])
	if err != nil {
		return v1alpha1.PackageCustomization{}, err
	}

	corePkgs := map[string]struct{}{v1alpha1.ArgoCDPackageName: {}, v1alpha1.GiteaPackageName: {}, v1alpha1.CiliumPackageName: {}}
	name := s[0]
	_, ok := corePkgs[name]
	if !ok {
		return v1alpha1.PackageCustomization{}, fmt.Errorf("customization for %s not supported", name)
	}
	return v1alpha1.PackageCustomization{
		Name:     name,
		FilePath: paths[0],
	}, nil
}

func (b *LocalProvisioner) ReconcileKindCluster(ctx context.Context, recreateCluster bool) error {
	// Initialize Kind Cluster
	cluster, err := kind.NewCluster(b.options.Name, b.options.KubeVersion, b.options.KubeConfigPath, b.options.KindConfigPath, b.options.ExtraPortsMapping, b.options.RegistryConfig, b.options.TemplateData)
	if err != nil {
		logger.Error("Error Creating kind cluster", err, map[string]interface{}{})
		return err
	}

	// Build Kind cluster
	if err := cluster.Reconcile(ctx, recreateCluster); err != nil {
		logger.Error("Error starting kind cluster", err, map[string]interface{}{})
		return err
	}

	// Create Kube Config for Kind cluster
	if err := cluster.ExportKubeConfig(b.options.Name, false); err != nil {
		logger.Error("Error exporting kubeconfig from kind cluster", err, map[string]interface{}{})
		return err
	}
	return nil
}

func (b *LocalProvisioner) GetKubeConfig() (*rest.Config, error) {
	kubeConfig, err := clientcmd.BuildConfigFromFlags("", b.options.KubeConfigPath)
	if err != nil {
		logger.Error("Error building kubeconfig from kind cluster", err, map[string]interface{}{})
		return nil, err
	}
	return kubeConfig, nil
}

func (b *LocalProvisioner) GetKubeClient(kubeConfig *rest.Config) (client.Client, error) {
	kubeClient, err := client.New(kubeConfig, client.Options{Scheme: b.options.Scheme})
	if err != nil {
		logger.Error("Error creating kubernetes client", err, map[string]interface{}{})
		return nil, err
	}
	return kubeClient, nil
}

func (b *LocalProvisioner) ReconcileCRDs(ctx context.Context, kubeClient client.Client) error {
	// Ensure idpbuilder CRDs
	if err := controllers.EnsureCRDs(ctx, b.options.Scheme, kubeClient, b.options.TemplateData); err != nil {
		logger.Error("Error creating idpbuilder CRDs", err, map[string]interface{}{})
		return err
	}
	return nil
}

func (b *LocalProvisioner) RunControllers(ctx context.Context, mgr manager.Manager, exitCh chan error, tmpDir string) error {
	return controllers.RunControllers(ctx, mgr, exitCh, b.options.CancelFunc, b.options.ExitOnSync, b.options.TemplateData, tmpDir)
}

//lint:ignore U1000 Compatibility checks are part of planned UX improvements.
func (b *LocalProvisioner) isCompatible(ctx context.Context, kubeClient client.Client) (bool, error) {
	localBuild := v1alpha1.AdharPlatform{
		ObjectMeta: metav1.ObjectMeta{
			Name:      b.options.Name,
			Namespace: globals.AdharSystemNamespace,
		},
	}

	err := kubeClient.Get(ctx, client.ObjectKeyFromObject(&localBuild), &localBuild)
	if err != nil {
		if errors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}

	ok := isBuildCustomizationSpecEqual(b.options.TemplateData, localBuild.Spec.BuildCustomization)

	if ok {
		return ok, nil
	}

	existing, given := localBuild.Spec.BuildCustomization, b.options.TemplateData
	existing.SelfSignedCert = ""
	given.SelfSignedCert = ""

	return false, fmt.Errorf("provided command flags and existing configurations are incompatible. please recreate the cluster. "+
		"existing: %+v, given: %+v",
		existing, given)
}

//lint:ignore U1000 Helper retained for future configuration comparisons.
func isBuildCustomizationSpecEqual(s1, s2 v1alpha1.BuildCustomizationSpec) bool {
	// probably ok to use cmp.Equal but keeping it simple for now
	return s1.Protocol == s2.Protocol &&
		s1.Host == s2.Host &&
		s1.IngressHost == s2.IngressHost &&
		s1.Port == s2.Port &&
		s1.UsePathRouting == s2.UsePathRouting &&
		s1.SelfSignedCert == s2.SelfSignedCert &&
		s1.StaticPassword == s2.StaticPassword
}
