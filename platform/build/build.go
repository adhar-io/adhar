package build

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/controllers"
	"adhar-io/adhar/platform/kind"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var (
	setupLog = ctrl.Log.WithName("setup")
)

type Build struct {
	name                 string
	cfg                  v1alpha1.BuildCustomizationSpec
	kindConfigPath       string
	kubeConfigPath       string
	kubeVersion          string
	extraPortsMapping    string
	registryConfig       []string
	customPackageDirs    []string
	customPackageUrls    []string
	packageCustomization map[string]v1alpha1.PackageCustomization
	exitOnSync           bool
	scheme               *runtime.Scheme
	CancelFunc           context.CancelFunc
}

type NewBuildOptions struct {
	Name                 string
	TemplateData         v1alpha1.BuildCustomizationSpec
	KindConfigPath       string
	KubeConfigPath       string
	KubeVersion          string
	ExtraPortsMapping    string
	RegistryConfig       []string
	CustomPackageDirs    []string
	CustomPackageUrls    []string
	PackageCustomization map[string]v1alpha1.PackageCustomization
	ExitOnSync           bool
	Scheme               *runtime.Scheme
	CancelFunc           context.CancelFunc
}

func NewBuild(opts NewBuildOptions) *Build {
	return &Build{
		name:                 opts.Name,
		kindConfigPath:       opts.KindConfigPath,
		kubeConfigPath:       opts.KubeConfigPath,
		kubeVersion:          opts.KubeVersion,
		extraPortsMapping:    opts.ExtraPortsMapping,
		registryConfig:       opts.RegistryConfig,
		customPackageDirs:    opts.CustomPackageDirs,
		customPackageUrls:    opts.CustomPackageUrls,
		packageCustomization: opts.PackageCustomization,
		exitOnSync:           opts.ExitOnSync,
		scheme:               opts.Scheme,
		cfg:                  opts.TemplateData,
		CancelFunc:           opts.CancelFunc,
	}
}

func (b *Build) ReconcileKindCluster(ctx context.Context, recreateCluster bool) error {
	// Initialize Kind Cluster
	cluster, err := kind.NewCluster(b.name, b.kubeVersion, b.kubeConfigPath, b.kindConfigPath, b.extraPortsMapping, b.registryConfig, b.cfg, setupLog)
	if err != nil {
		setupLog.Error(err, "Error Creating kind cluster")
		return err
	}

	// Build Kind cluster
	if err := cluster.Reconcile(ctx, recreateCluster); err != nil {
		setupLog.Error(err, "Error starting kind cluster")
		return err
	}

	// Create Kube Config for Kind cluster
	if err := cluster.ExportKubeConfig(b.name, false); err != nil {
		setupLog.Error(err, "Error exporting kubeconfig from kind cluster")
		return err
	}
	return nil
}

func (b *Build) GetKubeConfig() (*rest.Config, error) {
	kubeConfig, err := clientcmd.BuildConfigFromFlags("", b.kubeConfigPath)
	if err != nil {
		setupLog.Error(err, "Error building kubeconfig from kind cluster")
		return nil, err
	}
	return kubeConfig, nil
}

func (b *Build) GetKubeClient(kubeConfig *rest.Config) (client.Client, error) {
	kubeClient, err := client.New(kubeConfig, client.Options{Scheme: b.scheme})
	if err != nil {
		setupLog.Error(err, "Error creating kubernetes client")
		return nil, err
	}
	return kubeClient, nil
}

func (b *Build) ReconcileCRDs(ctx context.Context, kubeClient client.Client) error {
	// Ensure adhar CRDs
	if err := controllers.EnsureCRDs(ctx, b.scheme, kubeClient, b.cfg); err != nil {
		setupLog.Error(err, "Error creating adhar CRDs")
		return err
	}
	return nil
}

func (b *Build) RunControllers(ctx context.Context, mgr manager.Manager, exitCh chan error, tmpDir string) error {
	return controllers.RunControllers(ctx, mgr, exitCh, b.CancelFunc, b.exitOnSync, b.cfg, tmpDir)
}

func (b *Build) isCompatible(ctx context.Context, kubeClient client.Client) (bool, error) {
	localBuild := v1alpha1.AdharPlatform{
		ObjectMeta: metav1.ObjectMeta{
			Name:      b.name,
			Namespace: globals.AdharSystemNamespace,
		},
	}

	err := kubeClient.Get(ctx, client.ObjectKeyFromObject(&localBuild), &localBuild)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}

	ok := isBuildCustomizationSpecEqual(b.cfg, localBuild.Spec.BuildCustomization)

	if ok {
		return ok, nil
	}

	existing, given := localBuild.Spec.BuildCustomization, b.cfg
	existing.SelfSignedCert = ""
	given.SelfSignedCert = ""

	return false, fmt.Errorf("provided command flags and existing configurations are incompatible. please recreate the cluster. "+
		"existing: %+v, given: %+v",
		existing, given)
}

func (b *Build) Run(ctx context.Context, recreateCluster bool) error {
	setupLog.V(1).Info("Creating kind cluster")
	if err := b.ReconcileKindCluster(ctx, recreateCluster); err != nil {
		return err
	}

	setupLog.V(1).Info("Getting Kube config")
	kubeConfig, err := b.GetKubeConfig()
	if err != nil {
		return err
	}

	setupLog.V(1).Info("Getting Kube client")
	kubeClient, err := b.GetKubeClient(kubeConfig)
	if err != nil {
		return err
	}

	createNSCmdStr := fmt.Sprintf("kubectl create namespace %s --kubeconfig %s", globals.AdharSystemNamespace, b.kubeConfigPath)
	cmdNS := exec.CommandContext(ctx, "sh", "-c", createNSCmdStr)
	var nsStdOutBuf, nsStdErrBuf strings.Builder
	cmdNS.Stdout = &nsStdOutBuf // Capture stdout for logging if needed
	cmdNS.Stderr = &nsStdErrBuf // Capture stderr to check for specific errors

	setupLog.Info("Creating adhar-system namespace...")
	setupLog.V(1).Info("Executing command", "command", createNSCmdStr)
	if err := cmdNS.Run(); err != nil {
		stderrOutput := nsStdErrBuf.String()
		// Check if the error is because the namespace already exists
		if strings.Contains(stderrOutput, "AlreadyExists") || strings.Contains(stderrOutput, "already exists") {
			setupLog.Info("Namespace adhar-system already exists, proceeding.")
			setupLog.V(1).Info("Namespace exists details", "stderr", stderrOutput)
		} else {
			// If it's a different error, log and return it
			setupLog.Error(err, "Failed to create adhar-system namespace", "command", createNSCmdStr, "stdout", nsStdOutBuf.String(), "stderr", stderrOutput)
			return fmt.Errorf("failed to create namespace '%s': %w. Stdout: '%s', Stderr: '%s'", globals.AdharSystemNamespace, err, nsStdOutBuf.String(), stderrOutput)
		}
	} else {
		setupLog.Info("Namespace adhar-system created successfully.")
		setupLog.V(1).Info("Namespace creation output", "stdout", nsStdOutBuf.String())
	}

	setupLog.Info("Installing Cilium CNI...")
	if err := b.InstallCilium(ctx, b.kubeConfigPath); err != nil {
		setupLog.Error(err, "Failed to install Cilium")
		return err
	}

	setupLog.Info("Validating essential cluster components")
	if err := b.ValidateClusterComponents(ctx, b.kubeConfigPath); err != nil {
		setupLog.Error(err, "Failed to validate cluster components")
		return err
	}

	setupLog.Info("Adding CRDs to the cluster")
	if err := b.ReconcileCRDs(ctx, kubeClient); err != nil {
		return err
	}

	setupLog.V(1).Info("Creating controller manager")
	// Create controller manager with improved logging
	mgr, err := ctrl.NewManager(kubeConfig, ctrl.Options{
		Scheme: b.scheme,
		Metrics: server.Options{
			BindAddress: "0",
		},
		Logger: setupLog, // Use our cleaner logger instead of default structured logger
	})
	if err != nil {
		setupLog.Error(err, "Error creating controller manager")
		return err
	}

	dir, err := os.MkdirTemp("", fmt.Sprintf("%s-%s-", globals.ProjectName, b.name))
	if err != nil {
		setupLog.Error(err, "creating temp dir")
		return err
	}
	defer os.RemoveAll(dir)
	setupLog.V(1).Info("Created temp directory for cloning repositories", "dir", dir)

	setupLog.Info("Setting up CoreDNS")
	err = setupCoreDNS(ctx, kubeClient, b.scheme, b.cfg)
	if err != nil {
		return err
	}

	setupLog.Info("Setting up TLS certificate")
	cert, err := setupSelfSignedCertificate(ctx, setupLog, kubeClient, b.cfg)
	if err != nil {
		return err
	}
	b.cfg.SelfSignedCert = string(cert)

	setupLog.V(1).Info("Checking for incompatible options from a previous run")
	ok, err := b.isCompatible(ctx, kubeClient)
	if err != nil {
		setupLog.Error(err, "Error while checking incompatible flags")
		return err
	}
	if !ok {
		return err
	}

	managerExit := make(chan error)

	setupLog.V(1).Info("Running controllers")
	if err := b.RunControllers(ctx, mgr, managerExit, dir); err != nil {
		setupLog.Error(err, "Error running controllers")
		return err
	}

	localBuild := v1alpha1.AdharPlatform{
		ObjectMeta: metav1.ObjectMeta{
			Name:      b.name,
			Namespace: globals.AdharSystemNamespace,
		},
	}

	cliStartTime := time.Now().Format(time.RFC3339Nano)

	setupLog.Info("Creating adharplatform resource")
	_, err = controllerutil.CreateOrUpdate(ctx, kubeClient, &localBuild, func() error {
		if localBuild.ObjectMeta.Annotations == nil {
			localBuild.ObjectMeta.Annotations = map[string]string{}
		}
		localBuild.ObjectMeta.Annotations[v1alpha1.CliStartTimeAnnotation] = cliStartTime
		localBuild.Spec = v1alpha1.AdharPlatformSpec{
			BuildCustomization: b.cfg,
			PackageConfigs: v1alpha1.PackageConfigsSpec{
				Argo: v1alpha1.ArgoPackageConfigSpec{
					Enabled: true,
				},
				EmbeddedArgoApplications: v1alpha1.EmbeddedArgoApplicationsPackageConfigSpec{
					Enabled: true,
				},
				CustomPackageDirs:        b.customPackageDirs,
				CustomPackageUrls:        b.customPackageUrls,
				CorePackageCustomization: b.packageCustomization,
			},
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("creating adharplatform resource: %w", err)
	}

	select {
	case mgrErr := <-managerExit:
		if mgrErr != nil {
			return mgrErr
		}
	case <-ctx.Done():
		return nil
	}
	return nil
}

func (b *Build) InstallCilium(ctx context.Context, kubeConfigPath string) error {
	// Define the path to the Cilium manifest
	ciliumManifestPath := "platform/controllers/adharplatform/resources/cilium/install.yaml"
	if _, err := os.Stat(ciliumManifestPath); os.IsNotExist(err) {
		ciliumManifestPath = "../controllers/adharplatform/resources/cilium/install.yaml"
		if _, err := os.Stat(ciliumManifestPath); os.IsNotExist(err) {
			return fmt.Errorf("cilium manifest file not found at %s or %s: %w", "platform/controllers/adharplatform/resources/cilium/install.yaml", ciliumManifestPath, err)
		}
	}

	applyCmdStr := fmt.Sprintf("kubectl apply -f %s --kubeconfig %s", ciliumManifestPath, kubeConfigPath)
	applyCmd := exec.CommandContext(ctx, "sh", "-c", applyCmdStr)
	var applyOutBuf, applyErrBuf strings.Builder
	applyCmd.Stdout = &applyOutBuf
	applyCmd.Stderr = &applyErrBuf

	setupLog.V(1).Info("Executing kubectl apply for Cilium installation", "command", applyCmdStr)
	if err := applyCmd.Run(); err != nil {
		setupLog.Error(err, "Failed to apply cilium manifest", "command", applyCmdStr, "stdout", applyOutBuf.String(), "stderr", applyErrBuf.String())
		return fmt.Errorf("failed to apply cilium manifest: %w. Stdout: '%s', Stderr: '%s'", err, applyOutBuf.String(), applyErrBuf.String())
	}

	setupLog.Info("Cilium manifest applied successfully.")
	setupLog.V(1).Info("Cilium apply output", "stdout", applyOutBuf.String())

	// Wait for nodes to become ready
	setupLog.Info("Waiting for all nodes to become ready (timeout: 10 minutes)...")
	nodeReadyCmd := fmt.Sprintf("kubectl --kubeconfig %s wait --for=condition=Ready nodes --all --timeout=600s", kubeConfigPath)
	nodeReadyExec := exec.CommandContext(ctx, "sh", "-c", nodeReadyCmd)
	if err := nodeReadyExec.Run(); err != nil {
		setupLog.Error(err, "Nodes did not become ready within 10-minute timeout - this often indicates slow image pulls")
		return fmt.Errorf("nodes not ready: %w", err)
	}

	setupLog.Info("All nodes are ready!")
	setupLog.Info("Cilium installation complete.")
	return nil
}

// ValidateClusterComponents validates that essential cluster components are ready
func (b *Build) ValidateClusterComponents(ctx context.Context, kubeConfigPath string) error {
	setupLog.Info("Validating essential cluster components...")

	// List of essential components to validate
	essentialComponents := []struct {
		name      string
		namespace string
		resource  string
	}{
		{"CoreDNS", "kube-system", "deployment/coredns"},
		{"Local Path Provisioner", "local-path-storage", "deployment/local-path-provisioner"},
	}

	for _, component := range essentialComponents {
		setupLog.Info("Waiting for component to be ready (timeout: 10 minutes)", "component", component.name)
		setupLog.V(1).Info("Component details", "component", component.name, "namespace", component.namespace, "resource", component.resource)

		// Use kubectl wait with a reasonable timeout
		waitCmd := fmt.Sprintf("kubectl --kubeconfig %s -n %s wait --for=condition=Available %s --timeout=600s",
			kubeConfigPath, component.namespace, component.resource)

		setupLog.V(1).Info("Executing wait command", "command", waitCmd)
		cmd := exec.CommandContext(ctx, "sh", "-c", waitCmd)
		output, err := cmd.CombinedOutput()

		if err != nil {
			setupLog.Error(err, "Component not ready within 10-minute timeout - this may indicate slow container startup",
				"component", component.name,
				"namespace", component.namespace,
				"resource", component.resource,
				"output", string(output))
			return fmt.Errorf("component %s (%s/%s) not ready: %w",
				component.name, component.namespace, component.resource, err)
		}

		setupLog.Info("Component is ready", "component", component.name)
		setupLog.V(1).Info("Component ready output", "component", component.name, "output", string(output))
	}

	setupLog.Info("All essential cluster components are ready")
	return nil
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
