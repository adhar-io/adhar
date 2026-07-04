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

	stdlog "log"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/controllers"
	"adhar-io/adhar/platform/k8s"
	"adhar-io/adhar/platform/logger"

	"adhar-io/adhar/platform/providers/kind"

	"github.com/go-logr/logr"
	"github.com/go-logr/stdr"
	"github.com/spf13/cobra"
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
	StackDir                  string
	Scheme                    *runtime.Scheme
	CancelFunc                context.CancelFunc
}

// LocalProvisioner handles local development environment creation
type LocalProvisioner struct {
	options *LocalOptions
}

// NewLocalProvisioner creates a new LocalProvisioner
func NewLocalProvisioner(options *LocalOptions) *LocalProvisioner {
	return &LocalProvisioner{options: options}
}

// Provision creates the local development environment end to end: it builds the
// Kind cluster, installs CRDs, configures networking and TLS, starts the
// platform controllers, and creates the AdharPlatform resource that drives
// GitOps reconciliation. It blocks until the manager exits or the context is
// cancelled.
func (lp *LocalProvisioner) Provision(ctx context.Context, args []string) error {
	// Set up the controller-runtime and klog loggers FIRST — before any code
	// path that emits a controller-runtime log (e.g. the Kind cluster reconcile
	// below). Otherwise controller-runtime prints a one-time
	// "log.SetLogger(...) was never called" stack-trace warning.
	// Verbose mode: show all messages. Normal mode: completely silent.
	if lp.options.Verbose {
		stdr.SetVerbosity(1)
		ctrl.SetLogger(stdr.New(stdlog.New(os.Stderr, "", stdlog.LstdFlags)))
	} else {
		ctrl.SetLogger(logr.Discard())
		// Silence klog (k8s.io/client-go) warnings that bypass the cr logger.
		klog.SetOutput(io.Discard)
	}

	// A single live checklist tracks the real provisioning stages. Steps 1–3 run
	// here on the CLI and are marked directly; stages 4–8 are owned by the
	// AdharPlatform controller and advanced by polling its status. In --verbose
	// mode the tracker degrades to plain lines so streaming logs stay readable.
	stages := []helpers.StageDef{
		{Label: "Kind cluster", Detail: "local node · Cilium CNI · ports 8443/8080"},
		{Label: "Platform CRDs", Detail: "AdharPlatform · GitRepository · CustomPackage"},
		{Label: "Networking", Detail: "CoreDNS rewrite · self-signed TLS"},
		{Label: "Cilium & Gateway", Detail: "eBPF CNI + Cilium Gateway API"},
		{Label: "ArgoCD", Detail: "GitOps engine"},
		{Label: "Gitea", Detail: "in-cluster Git server"},
		{Label: "Crossplane", Detail: "control plane + providers"},
		{Label: "GitOps sync - platform stack", Detail: "curated platform apps via ArgoCD"},
	}
	tracker := helpers.NewStageTracker(os.Stderr, "Provisioning Adhar platform", stages, !lp.options.Verbose)
	tracker.Start()

	// Stage 1: Create Kind cluster
	tracker.Activate(0)
	if err := lp.ReconcileKindCluster(ctx, recreateCluster); err != nil {
		tracker.Fail(0)
		tracker.Stop()
		return err
	}
	tracker.Done(0)

	kubeConfig, err := lp.GetKubeConfig()
	if err != nil {
		return err
	}
	kubeClient, err := lp.GetKubeClient(kubeConfig)
	if err != nil {
		return err
	}

	// Stage 2: Install CRDs
	tracker.Activate(1)
	if err := lp.ReconcileCRDs(ctx, kubeClient); err != nil {
		tracker.Fail(1)
		tracker.Stop()
		return err
	}
	tracker.Done(1)

	mgr, err := manager.New(kubeConfig, manager.Options{
		Scheme: lp.options.Scheme,
		Metrics: server.Options{
			BindAddress: "0",
		},
		GracefulShutdownTimeout: func() *time.Duration {
			d := 30 * time.Second
			return &d
		}(),
	})
	if err != nil {
		return fmt.Errorf("creating controller manager: %w", err)
	}

	dir, err := os.MkdirTemp("", fmt.Sprintf("%s-%s-", globals.ProjectName, lp.options.Name))
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	// Stage 3: Setup networking (CoreDNS + self-signed TLS)
	tracker.Activate(2)
	err = kind.SetupCoreDNS(ctx, kubeClient, lp.options.Scheme, lp.options.TemplateData)
	if err != nil {
		tracker.Fail(2)
		tracker.Stop()
		return err
	}
	cert, err := kind.SetupSelfSignedCertificate(ctx, kubeClient, lp.options.TemplateData)
	if err != nil {
		tracker.Fail(2)
		tracker.Stop()
		return err
	}
	lp.options.TemplateData.SelfSignedCert = string(cert)
	tracker.Done(2)

	// Stages 4–8: the AdharPlatform controller installs the core components
	// (Cilium → Gateway → ArgoCD → Gitea → Crossplane) and drives the GitOps
	// sync. Activate the first controller-owned stage; the poller advances the
	// rest from the CR status as each component reports Available.
	tracker.Activate(3)
	managerExit := make(chan error)
	if err := lp.RunControllers(ctx, mgr, managerExit, dir); err != nil {
		tracker.Fail(3)
		tracker.Stop()
		return fmt.Errorf("starting controllers: %w", err)
	}

	localBuild := v1alpha1.AdharPlatform{
		ObjectMeta: metav1.ObjectMeta{
			Name:      lp.options.Name,
			Namespace: globals.AdharSystemNamespace,
		},
	}

	cliStartTime := time.Now().Format(time.RFC3339Nano)

	_, err = controllerutil.CreateOrUpdate(ctx, kubeClient, &localBuild, func() error {
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
		return fmt.Errorf("creating AdharPlatform resource: %w", err)
	}

	// Poll the AdharPlatform status to advance the controller-owned stages as
	// each core component reports Available.
	stopPoll := make(chan struct{})
	go pollPlatformStages(ctx, kubeClient, lp.options.Name, tracker, stopPoll)

	finish := func(failed bool) {
		close(stopPoll)
		if failed {
			tracker.Stop()
			return
		}
		// Force-complete any stage the poller hasn't caught yet — the controller
		// shuts down the instant the platform is deployed, which can race the
		// final status read.
		for i := 3; i < len(stages); i++ {
			tracker.Done(i)
		}
		tracker.Stop()
	}

	select {
	case mgrErr := <-managerExit:
		// Manager exited on its own — check if it's a real error
		if mgrErr != nil && !isShutdownError(mgrErr) {
			finish(true)
			return mgrErr
		}
	case <-ctx.Done():
		// Context cancelled — controller signalled successful shutdown
		if mgrErr := <-managerExit; mgrErr != nil && !isShutdownError(mgrErr) {
			finish(true)
			return mgrErr
		}
	}
	finish(false)
	return nil
}

// pollPlatformStages advances the controller-owned stages of the tracker as each
// core component reports Available in the AdharPlatform status. It runs until the
// stop channel closes or the context is cancelled. Marks are idempotent, so it is
// safe for several components to become Available between ticks.
func pollPlatformStages(ctx context.Context, c client.Client, name string, tracker *helpers.StageTracker, stop <-chan struct{}) {
	defer func() { _ = recover() }()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			var pl v1alpha1.AdharPlatform
			if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: globals.AdharSystemNamespace}, &pl); err != nil {
				continue
			}
			st := pl.Status
			// Stage 3 Cilium & Gateway → 4 ArgoCD → 5 Gitea → 6 Crossplane →
			// 7 GitOps sync (completed after the controller shuts down).
			if st.Gateway.Available {
				tracker.Done(3)
				tracker.Activate(4)
			}
			if st.ArgoCD.Available {
				tracker.Done(4)
				tracker.Activate(5)
			}
			if st.Gitea.Available {
				tracker.Done(5)
				tracker.Activate(6)
			}
			if st.Crossplane.Available {
				tracker.Done(6)
				tracker.Activate(7)
			}
		}
	}
}

// isShutdownError returns true for errors that are expected during graceful shutdown
func isShutdownError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return err == context.Canceled ||
		strings.Contains(msg, "context canceled") ||
		strings.Contains(msg, "grace period") ||
		strings.Contains(msg, "context deadline exceeded")
}

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

	// Resolve the stack directory (first package dir or default)
	stackDir := "platform/stack"
	if len(localDirs) > 0 {
		stackDir = localDirs[0]
	}
	absStackDir, err := filepath.Abs(stackDir)
	if err != nil {
		return fmt.Errorf("resolving stack directory path: %w", err)
	}

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
		NoExit:                    exitOnSync,
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
		StackDir:                  absStackDir,
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
	fmt.Printf("  Gateway:     true\n")
	fmt.Printf("  Cilium:      true\n")

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

// printSuccessMsg prints the polished end-of-run panel for the local cluster:
// a "ready" header, a bordered table of access URLs, and the key command hints.
func printSuccessMsg() {
	baseURL := fmt.Sprintf("%s://%s:%s", protocol, host, port)
	if behindProxy() {
		baseURL = fmt.Sprintf("https://%s", host)
	}

	access := [][2]string{
		{"Console", baseURL + "/console"},
		{"ArgoCD", baseURL + "/argocd"},
		{"Gitea", baseURL + "/gitea"},
		{"Grafana", baseURL + "/grafana"},
		{"Keycloak", baseURL + "/keycloak"},
	}
	hints := [][2]string{
		{"Credentials", "adhar get secrets"},
		{"Status", "adhar get status"},
		{"Teardown", "adhar down"},
	}

	fmt.Println()
	fmt.Println(helpers.RenderReadyPanel(access, hints))
	fmt.Println()
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

	corePkgs := map[string]struct{}{v1alpha1.ArgoCDPackageName: {}, v1alpha1.GiteaPackageName: {}, v1alpha1.GatewayPackageName: {}}
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

// ReconcileKindCluster creates (or recreates) the Kind cluster for local
// development and exports its kubeconfig.
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

// GetKubeConfig builds a REST config from the provisioner's kubeconfig path.
func (b *LocalProvisioner) GetKubeConfig() (*rest.Config, error) {
	kubeConfig, err := clientcmd.BuildConfigFromFlags("", b.options.KubeConfigPath)
	if err != nil {
		logger.Error("Error building kubeconfig from kind cluster", err, map[string]interface{}{})
		return nil, err
	}
	return kubeConfig, nil
}

// GetKubeClient creates a controller-runtime client from the given REST config
// using the provisioner's scheme.
func (b *LocalProvisioner) GetKubeClient(kubeConfig *rest.Config) (client.Client, error) {
	kubeClient, err := client.New(kubeConfig, client.Options{Scheme: b.options.Scheme})
	if err != nil {
		logger.Error("Error creating kubernetes client", err, map[string]interface{}{})
		return nil, err
	}
	return kubeClient, nil
}

// ReconcileCRDs installs the platform CRDs (AdharPlatform, GitRepository,
// CustomPackage) into the cluster.
func (b *LocalProvisioner) ReconcileCRDs(ctx context.Context, kubeClient client.Client) error {
	// Ensure idpbuilder CRDs
	if err := controllers.EnsureCRDs(ctx, b.options.Scheme, kubeClient, b.options.TemplateData); err != nil {
		logger.Error("Error creating idpbuilder CRDs", err, map[string]interface{}{})
		return err
	}
	return nil
}

// RunControllers starts the platform controllers on the given manager, signalling
// completion or errors over exitCh.
func (b *LocalProvisioner) RunControllers(ctx context.Context, mgr manager.Manager, exitCh chan error, tmpDir string) error {
	return controllers.RunControllers(ctx, mgr, exitCh, b.options.CancelFunc, b.options.ExitOnSync, b.options.TemplateData, tmpDir, b.options.StackDir)
}

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
