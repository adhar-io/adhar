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
	"os"
	"path/filepath"
	"strings"
	"time"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/cmd/version"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/controllers"
	"adhar-io/adhar/platform/k8s"
	"adhar-io/adhar/platform/logger"
	pfactory "adhar-io/adhar/platform/providers"
	"adhar-io/adhar/platform/providers/kind"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// k8sClientFromConfig builds a controller-runtime client for the given REST
// config and scheme.
func k8sClientFromConfig(restConfig *rest.Config, scheme *runtime.Scheme) (client.Client, error) {
	kubeClient, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %w", err)
	}
	return kubeClient, nil
}

// defaultControllerImage is the released adhar image matching this CLI build
// (goreleaser tags images without the leading "v").
func defaultControllerImage() string {
	return fmt.Sprintf("ghcr.io/adhar-io/adhar:%s", strings.TrimPrefix(version.Version, "v"))
}

// providerNameToEnvironmentProvider maps a resolved provider string from the
// config to the AdharPlatform API enum.
func providerNameToEnvironmentProvider(name string) v1alpha1.EnvironmentProvider {
	switch name {
	case "aws", "eks":
		return v1alpha1.ProviderAWS
	case "azure", "aks":
		return v1alpha1.ProviderAzure
	case "gcp", "gke":
		return v1alpha1.ProviderGKE
	case "digitalocean", "do", "doks":
		return v1alpha1.ProviderDO
	case "civo":
		return v1alpha1.ProviderCivo
	case "kind":
		return v1alpha1.ProviderKind
	default:
		return v1alpha1.ProviderCustom
	}
}

// bootstrapPlatformOnCluster runs the same controller-driven bootstrap used for
// local Kind clusters against a provisioned cloud or on-prem cluster: platform
// CRDs, TLS secret, the AdharPlatform resource (which drives Cilium → Gateway →
// ArgoCD → Gitea → Crossplane and the GitOps stack), and finally the in-cluster
// controller manager for continuous reconciliation once the CLI exits.
func bootstrapPlatformOnCluster(ctx context.Context, result *pfactory.ProvisionResult, envConfig *config.ResolvedEnvironmentConfig, cfg *config.Config) error {
	logger.Infof("Bootstrapping Adhar platform on cluster %s", result.Cluster.ID)

	// Retrieve and materialize the kubeconfig. The controller's GitOps repo
	// seeding shells out to kubectl, so KUBECONFIG must point at this cluster
	// for the duration of the bootstrap.
	kubeconfigStr, err := result.Provider.GetKubeconfig(ctx, result.Cluster.ID)
	if err != nil {
		return fmt.Errorf("retrieving kubeconfig for cluster %s: %w", result.Cluster.ID, err)
	}
	kubeconfigFile, err := os.CreateTemp("", "adhar-kubeconfig-")
	if err != nil {
		return fmt.Errorf("creating kubeconfig temp file: %w", err)
	}
	defer os.Remove(kubeconfigFile.Name())
	if _, err := kubeconfigFile.WriteString(kubeconfigStr); err != nil {
		return fmt.Errorf("writing kubeconfig: %w", err)
	}
	if err := kubeconfigFile.Close(); err != nil {
		return fmt.Errorf("closing kubeconfig file: %w", err)
	}
	prevKubeconfig, hadPrev := os.LookupEnv("KUBECONFIG")
	os.Setenv("KUBECONFIG", kubeconfigFile.Name())
	defer func() {
		if hadPrev {
			os.Setenv("KUBECONFIG", prevKubeconfig)
		} else {
			os.Unsetenv("KUBECONFIG")
		}
	}()

	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfigStr))
	if err != nil {
		return fmt.Errorf("building REST config from kubeconfig: %w", err)
	}

	scheme := k8s.GetScheme()
	kubeClient, err := k8sClientFromConfig(restConfig, scheme)
	if err != nil {
		return err
	}

	// Build customization: production edges terminate TLS on 443; HA rendering
	// follows the enableHAMode global (roadmap P1.2).
	host := globals.DefaultHostName
	enableHA := false
	if cfg != nil && cfg.GlobalSettings.DefaultHost != "" {
		host = cfg.GlobalSettings.DefaultHost
	}
	if cfg != nil {
		enableHA = cfg.GlobalSettings.EnableHAMode
	}
	if envConfig != nil && envConfig.GlobalSettings != nil {
		enableHA = envConfig.GlobalSettings.EnableHAMode
		if envConfig.GlobalSettings.DefaultHost != "" {
			host = envConfig.GlobalSettings.DefaultHost
		}
	}
	templateData := v1alpha1.BuildCustomizationSpec{
		Protocol:     "https",
		Host:         host,
		IngressHost:  host,
		Port:         "443",
		EnableHAMode: enableHA,
	}

	// Install platform CRDs.
	if err := controllers.EnsureCRDs(ctx, scheme, kubeClient, templateData); err != nil {
		return fmt.Errorf("installing platform CRDs: %w", err)
	}

	// Self-signed certificate as the initial Gateway TLS material; the
	// production edge (cert-manager ClusterIssuer) replaces it when configured.
	cert, err := kind.SetupSelfSignedCertificate(ctx, kubeClient, templateData)
	if err != nil {
		return fmt.Errorf("setting up TLS certificate: %w", err)
	}
	templateData.SelfSignedCert = string(cert)

	// The stack directory is required for GitOps repo seeding.
	stackDir, err := filepath.Abs("platform/stack")
	if err != nil {
		return fmt.Errorf("resolving stack directory: %w", err)
	}
	if _, err := os.Stat(stackDir); err != nil {
		return fmt.Errorf("platform stack directory not found at %s (run from the adhar repository root or provide -p): %w", stackDir, err)
	}

	tmpDir, err := os.MkdirTemp("", "adhar-bootstrap-")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	mgr, err := manager.New(restConfig, manager.Options{
		Scheme:  scheme,
		Metrics: server.Options{BindAddress: "0"},
		GracefulShutdownTimeout: func() *time.Duration {
			d := 30 * time.Second
			return &d
		}(),
	})
	if err != nil {
		return fmt.Errorf("creating controller manager: %w", err)
	}

	bootstrapCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	exitCh := make(chan error)
	if err := controllers.RunControllers(bootstrapCtx, mgr, exitCh, cancel, true, templateData, tmpDir, stackDir); err != nil {
		return fmt.Errorf("starting controllers: %w", err)
	}

	platformName := globals.DefaultClusterName
	if envConfig != nil && envConfig.Name != "" {
		platformName = envConfig.Name
	}
	providerName := ""
	if envConfig != nil {
		providerName = envConfig.ResolvedProvider
	}
	platform := v1alpha1.AdharPlatform{
		ObjectMeta: metav1.ObjectMeta{
			Name:      platformName,
			Namespace: globals.AdharSystemNamespace,
		},
	}
	_, err = controllerutil.CreateOrUpdate(bootstrapCtx, kubeClient, &platform, func() error {
		if platform.ObjectMeta.Annotations == nil {
			platform.ObjectMeta.Annotations = map[string]string{}
		}
		platform.ObjectMeta.Annotations[v1alpha1.CliStartTimeAnnotation] = time.Now().Format(time.RFC3339Nano)
		platform.Spec = v1alpha1.AdharPlatformSpec{
			Provider:           providerNameToEnvironmentProvider(providerName),
			BuildCustomization: templateData,
			PackageConfigs: v1alpha1.PackageConfigsSpec{
				Argo:                     v1alpha1.ArgoPackageConfigSpec{Enabled: true},
				EmbeddedArgoApplications: v1alpha1.EmbeddedArgoApplicationsPackageConfigSpec{Enabled: true},
			},
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("creating AdharPlatform resource: %w", err)
	}

	// Wait for the bootstrap controller to converge and shut down (ExitOnSync).
	select {
	case mgrErr := <-exitCh:
		if mgrErr != nil && !isShutdownError(mgrErr) {
			return mgrErr
		}
	case <-bootstrapCtx.Done():
		if mgrErr := <-exitCh; mgrErr != nil && !isShutdownError(mgrErr) {
			return mgrErr
		}
	}

	// Production posture: continuous reconciliation via the in-cluster manager.
	installCtx, installCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer installCancel()
	image := controllerImage
	if image == "" {
		image = defaultControllerImage()
	}
	if err := controllers.EnsureControllerManager(installCtx, kubeClient, controllers.ManagerConfig{
		Image:     image,
		Namespace: globals.AdharSystemNamespace,
	}); err != nil {
		return fmt.Errorf("installing in-cluster controller manager: %w", err)
	}

	logger.Infof("✅ Platform bootstrapped on cluster %s (HA mode: %t)", result.Cluster.ID, enableHA)
	return nil
}
