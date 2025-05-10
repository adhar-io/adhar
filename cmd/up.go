package main

import (
	"errors" // Added errors import
	"fmt"
	"io"         // Added for file operations
	"io/ioutil"  // Import for log redirection
	stdlog "log" // Rename standard log package to avoid conflict
	"net/http"   // Added for HTTP requests
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra" // Added cobra import
	"go.uber.org/zap/zapcore"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	zapcr "sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	// Import necessary types and schemes
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	// +kubebuilder:scaffold:imports includes Adhar API types

	"adhar-io/adhar/api/v1alpha1"    // Correct import path for v1alpha1
	"adhar-io/adhar/platform/config" // Correct import path for config
	"adhar-io/adhar/platform/k8s"    // Import for KCL module
)

const (
	kindClusterName = "adhar"
)

var (
	scheme = runtime.NewScheme()

	// Platform flags for up command
	waitForReadiness    bool
	timeout             int
	kubeconfigNamespace string // Renamed to avoid redeclaration
	noSpinner           bool
	environmentName     string // Flag to specify which environment to create/update
	verboseUp           bool   // Flag for verbose output
)

// upModel is the Bubble Tea model for the up command
type upModel struct {
	spinner       spinner.Model
	step          string
	status        string
	done          bool
	err           error
	quitting      bool
	startTime     time.Time
	elapsedTime   string
	showExtraInfo bool
	clusterInfo   string
	logBuffer     []string // Add buffer to store logs
}

// Add a function to continuously update the progress
func updateProgress() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return statusMsg("progressing...")
	})
}

// Init implements tea.Model
func (m upModel) Init() tea.Cmd {
	// Record the start time for tracking elapsed time
	m.startTime = time.Now()

	return tea.Batch(
		m.spinner.Tick,
		startClusterSetup(),
		updateElapsedTime(),
		updateProgress(), // Add progress updates
	)
}

// Update implements tea.Model
func (m upModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "i":
			// Toggle extra info
			m.showExtraInfo = !m.showExtraInfo
			return m, nil
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case stepMsg:
		m.step = string(msg)
		return m, nil

	case statusMsg:
		m.status = string(msg)
		return m, nil

	case clusterInfoMsg:
		m.clusterInfo = string(msg)
		return m, nil

	case logMsg:
		// Append to log buffer, limiting size to prevent memory issues
		m.logBuffer = append(m.logBuffer, string(msg))
		const maxLogLines = 100
		if len(m.logBuffer) > maxLogLines {
			m.logBuffer = m.logBuffer[len(m.logBuffer)-maxLogLines:]
		}
		return m, nil

	case errorMsg:
		m.err = msg.err
		m.done = true
		return m, tea.Quit

	case doneMsg:
		m.done = true
		return m, tea.Quit

	case elapsedTimeMsg:
		// Use String() method for duration formatting
		m.elapsedTime = time.Since(m.startTime).Round(time.Second).String()
		return m, updateElapsedTime()

	default:
		return m, nil
	}
}

// View implements tea.Model
func (m upModel) View() string {
	if m.quitting {
		return "Operation canceled\nExiting...\n"
	}

	if m.err != nil {
		// Keep error styling simple for post-run printing
		return fmt.Sprintf("Error: %s\nFailed to set up Adhar environment", m.err.Error())
	}

	if m.done {
		// --- Success message rendering ---
		// Content for the box (excluding ASCII art)
		successBoxContent := fmt.Sprintf(
			"%s %s\n\n"+
				"%s\n"+
				"- Run %s to view Adhar resources\n"+
				"- Run %s for interactive help and options\n"+
				"- Run %s to tear down when finished\n\n"+
				"%s %s",
			successStyle.Render("✅"),
			successStyle.Render("Adhar platform set up successfully!"),
			titleStyle.Render("Next Steps:"),
			highlightStyle.Render("`adhar get all`"),
			highlightStyle.Render("`adhar help`"),
			highlightStyle.Render("`adhar down`"),
			infoStyle.Render("Setup completed in:"),
			successStyle.Render(m.elapsedTime), // Ensure all arguments are provided
		)

		// Define the box style
		boxWidth := 100 // Consistent width
		boxStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(primaryColor).
			Padding(1).
			Width(boxWidth)

		// Render the box content
		renderedBox := boxStyle.Render(strings.TrimSpace(successBoxContent))

		// Return the ASCII art followed by the rendered box, separated by newlines
		return renderedBox
	}

	// --- In-progress view ---
	status := m.status
	if status == "" {
		status = "initializing..."
	}

	// Add ASCII header to the progress view
	printHeader() // Call the function to display the header

	view := fmt.Sprintf("%s\n%s", m.spinner.View(), m.step)

	timeInfo := fmt.Sprintf("\n\n%s %s",
		infoStyle.Render("Elapsed time:"),
		m.elapsedTime)

	// Add TWO newlines before the toggle hint for better spacing
	toggleHint := subtitleStyle.Render("\n\nPress 'i' to toggle details")

	var extraInfo string
	if m.showExtraInfo {
		// If extra info is enabled, show cluster info if available
		if m.clusterInfo != "" {
			extraInfo = fmt.Sprintf("\n\n%s\n%s",
				titleStyle.Render("Cluster Information:"),
				borderStyle.Render(m.clusterInfo))
		}

		// Also show logs if available
		if len(m.logBuffer) > 0 {
			// Format logs in a clean, readable way
			logContent := strings.Join(m.logBuffer, "\n")
			extraInfo += fmt.Sprintf("\n\n%s\n%s",
				titleStyle.Render("Logs:"),
				borderStyle.Render(strings.TrimSpace(logContent)))
		}
	}

	boxWidth := 100 // Consistent width
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(primaryColor).
		Padding(1).     // Removed margins
		Width(boxWidth) // Consistent width

	// Ensure extraInfo is appended to the view content
	// Define the header variable with appropriate content
	header := "Adhar Platform Setup" // Example header text, replace with actual content if needed
	content := header + "\n\n" + titleStyle.Render("Please wait while Adhar is setting up your environment") +
		"\n\n" + view + timeInfo + toggleHint

	// Append extraInfo only if it is not empty
	if extraInfo != "" {
		content += extraInfo
	}

	// Return the styled box content directly
	return boxStyle.Render(content)
}

// Custom message types for the Bubble Tea model
type stepMsg string
type statusMsg string
type errorMsg struct{ err error }
type doneMsg struct{}
type clusterInfoMsg string
type elapsedTimeMsg time.Time
type logMsg string // New message type for logs

// updateElapsedTime updates the elapsed time every second
func updateElapsedTime() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return elapsedTimeMsg(t)
	})
}

// startClusterSetup starts the asynchronous operation to set up the cluster
func startClusterSetup() tea.Cmd {
	return func() tea.Msg {
		// Example of using the new KCL module
		kclManifestPath := "config/kcl/sample.kcl"
		if err := k8s.ApplyKCLManifest(kclManifestPath); err != nil {
			return errorMsg{fmt.Errorf("failed to apply KCL manifest: %w", err)}
		}
		return doneMsg{}
	}
}

// getClusterInfo retrieves and sends information about the cluster
func getClusterInfo() {
	// Wait a moment to let the cluster finish starting
	time.Sleep(2 * time.Second)

	// Run kubectl to get node information
	cmd := exec.Command("kubectl", "get", "nodes", "-o", "wide")
	nodeInfo, err := cmd.CombinedOutput()
	if err != nil {
		send(clusterInfoMsg("Unable to get node information"))
		return
	}

	// Get pod information
	cmd = exec.Command("kubectl", "get", "pods", "--all-namespaces")
	podInfo, err := cmd.CombinedOutput()
	if err != nil {
		send(clusterInfoMsg(string(nodeInfo)))
		return
	}

	// Combine the information
	info := fmt.Sprintf("NODES:\n%s\n\nPODS:\n%s",
		strings.TrimSpace(string(nodeInfo)),
		strings.TrimSpace(string(podInfo)))

	send(clusterInfoMsg(info))
}

// installCrossplane installs Crossplane in the Kubernetes cluster
func installCrossplane() error {
	send(statusMsg("adding Crossplane Helm repository"))
	addRepoCmd := exec.Command("helm", "repo", "add", "crossplane-stable", "https://charts.crossplane.io/stable")
	if output, err := addRepoCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to add Crossplane helm repo: %w\n%s", err, output)
	}

	send(statusMsg("updating Helm repositories"))
	updateCmd := exec.Command("helm", "repo", "update")
	if output, err := updateCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to update Helm repos: %w\n%s", err, output)
	}

	// Create crossplane-system namespace if it doesn't exist
	send(statusMsg("creating crossplane-system namespace"))
	createNsCmd := exec.Command("kubectl", "create", "namespace", "crossplane-system", "--dry-run=client", "-o", "yaml")
	createNsOutput, err := createNsCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to generate namespace manifest: %w", err)
	}

	applyNsCmd := exec.Command("kubectl", "apply", "-f", "-")
	applyNsCmd.Stdin = strings.NewReader(string(createNsOutput))
	if output, err := applyNsCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create namespace: %w\n%s", err, output)
	}

	// Install Crossplane
	send(statusMsg("installing Crossplane (this may take a few minutes)"))
	installCmd := exec.Command("helm", "install", "crossplane",
		"--namespace", "crossplane-system",
		"--create-namespace",
		"--set", "args={--enable-composition-revisions}",
		"--wait", "crossplane-stable/crossplane")

	if output, err := installCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to install Crossplane: %w\n%s", err, output)
	}

	// Wait for Crossplane to be ready
	send(statusMsg("waiting for Crossplane controllers to be ready"))
	waitCmd := exec.Command("kubectl", "wait", "--for=condition=ready", "pod", "-l", "app=crossplane", "--namespace", "crossplane-system", "--timeout=120s")
	if output, err := waitCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed waiting for Crossplane to be ready: %w\n%s", err, output)
	}

	send(statusMsg("Crossplane installed successfully"))
	return nil
}

// installProvider installs the required cloud provider in Crossplane
func installProvider(envConfig *config.ResolvedEnvironmentConfig) error {
	var providerPackage string
	var secretName string
	var providerCR string

	switch envConfig.ResolvedProvider {
	case "gke":
		providerPackage = "crossplane/provider-gcp:v1.0.0"
		secretName = "gcp-creds"
		// Get Project ID from config, default to placeholder if missing
		projectID := "gcp-project-id-placeholder"
		if envConfig.GlobalSettings.ProviderCredentials.GKE != nil && envConfig.GlobalSettings.ProviderCredentials.GKE.ProjectID != "" {
			projectID = envConfig.GlobalSettings.ProviderCredentials.GKE.ProjectID
		}
		providerCR = fmt.Sprintf(`
apiVersion: gcp.crossplane.io/v1beta1
kind: ProviderConfig
metadata:
  name: default
spec:
  projectID: %s
  credentials:
    source: Secret
    secretRef:
      namespace: crossplane-system
      name: gcp-creds
      key: creds
`, projectID)
		// Create GCP credentials secret
		if envConfig.GlobalSettings.ProviderCredentials.GKE != nil {
			credSource := envConfig.GlobalSettings.ProviderCredentials.GKE
			if credSource.Type == "file" && credSource.Path != "" {
				credsPath := os.ExpandEnv(credSource.Path)
				cmd := exec.Command("kubectl", "create", "secret", "generic", secretName,
					"--namespace", "crossplane-system",
					"--from-file=creds="+credsPath)
				if output, err := cmd.CombinedOutput(); err != nil {
					return fmt.Errorf("failed to create GCP credentials secret: %w\n%s", err, output)
				}
			} else if credSource.Type == "environment" {
				gkeProjectID := ""
				if credSource.ProjectID != "" {
					gkeProjectID = credSource.ProjectID
				} else {
					gkeProjectID = os.Getenv("GCP_PROJECT_ID")
					if gkeProjectID == "" {
						return fmt.Errorf("GCP project ID not found in config or GCP_PROJECT_ID environment variable")
					}
				}

				// Check for Application Default Credentials or specific env vars
				gkeCredentialsJSON := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
				if gkeCredentialsJSON != "" {
					if _, err := os.Stat(gkeCredentialsJSON); os.IsNotExist(err) {
						return fmt.Errorf("GCP credentials file specified in GOOGLE_APPLICATION_CREDENTIALS does not exist: %s", gkeCredentialsJSON)
					}

					cmd := exec.Command("kubectl", "create", "secret", "generic", secretName,
						"--namespace", "crossplane-system",
						"--from-file=creds="+gkeCredentialsJSON)
					if output, err := cmd.CombinedOutput(); err != nil {
						return fmt.Errorf("failed to create GCP credentials secret from GOOGLE_APPLICATION_CREDENTIALS: %w\n%s", err, output)
					}
				} else {
					return fmt.Errorf("GCP credentials via environment variables require GOOGLE_APPLICATION_CREDENTIALS to be set")
				}

				// Update the provider CR with the project ID
				providerCR = fmt.Sprintf(`
apiVersion: gcp.crossplane.io/v1beta1
kind: ProviderConfig
metadata:
  name: default
spec:
  projectID: %s
  credentials:
    source: Secret
    secretRef:
      namespace: crossplane-system
      name: gcp-creds
      key: creds
`, gkeProjectID)
			} else {
				// Default error if no valid credential source is found
				return fmt.Errorf("no valid GCP credential source (file path or environment variables) configured or found")
			}
		} else {
			return fmt.Errorf("GCP provider credentials not configured in adhar-config.yaml")
		}

	case "aws":
		providerPackage = "crossplane/provider-aws:v1.0.0"
		secretName = "aws-creds"
		providerCR = `
apiVersion: aws.crossplane.io/v1beta1
kind: ProviderConfig
metadata:
  name: default
spec:
  credentials:
    source: Secret
    secretRef:
      namespace: crossplane-system
      name: aws-creds
      key: creds
`
		// Create AWS credentials secret
		if envConfig.GlobalSettings.ProviderCredentials.AWS != nil {
			awsAccessKeyID := os.Getenv("AWS_ACCESS_KEY_ID")
			awsSecretAccessKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
			if awsAccessKeyID != "" && awsSecretAccessKey != "" {
				credsContent := fmt.Sprintf("[default]\naws_access_key_id = %s\naws_secret_access_key = %s\n",
					awsAccessKeyID, awsSecretAccessKey)
				cmd := exec.Command("kubectl", "create", "secret", "generic", secretName,
					"--namespace", "crossplane-system",
					"--from-literal=creds="+credsContent)
				if output, err := cmd.CombinedOutput(); err != nil {
					return fmt.Errorf("failed to create AWS credentials secret: %w\n%s", err, output)
				}
			} else {
				return fmt.Errorf("AWS credentials (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY) not found in environment variables")
			}
		}

	case "do":
		providerPackage = "crossplane-contrib/provider-digitalocean:v0.8.0"
		secretName = "do-creds"
		providerCR = `
apiVersion: digitalocean.crossplane.io/v1alpha1
kind: ProviderConfig
metadata:
  name: default
spec:
  credentials:
    source: Secret
    secretRef:
      namespace: crossplane-system
      name: do-creds
      key: token
`
		// Create DO credentials secret
		if envConfig.GlobalSettings.ProviderCredentials.DO != nil {
			credSource := envConfig.GlobalSettings.ProviderCredentials.DO
			if credSource.Type == "environment" && credSource.EnvVar != "" {
				doToken := os.Getenv(credSource.EnvVar)
				if doToken != "" {
					cmd := exec.Command("kubectl", "create", "secret", "generic", secretName,
						"--namespace", "crossplane-system",
						"--from-literal=token="+doToken)
					if output, err := cmd.CombinedOutput(); err != nil {
						return fmt.Errorf("failed to create DigitalOcean credentials secret: %w\n%s", err, output)
					}
				} else {
					return fmt.Errorf("digitalocean token environment variable '%s' not found or empty", credSource.EnvVar)
				}
			}
		}

	case "azure":
		providerPackage = "crossplane/provider-azure:v1.0.0"
		secretName = "azure-creds"
		providerCR = `
apiVersion: azure.crossplane.io/v1beta1
kind: ProviderConfig
metadata:
  name: default
spec:
  credentials:
    source: Secret
    secretRef:
      namespace: crossplane-system
      name: azure-creds
      key: creds
`
		// Create Azure credentials secret from environment variables
		clientID := os.Getenv("AZURE_CLIENT_ID")
		clientSecret := os.Getenv("AZURE_CLIENT_SECRET")
		tenantID := os.Getenv("AZURE_TENANT_ID")
		subscriptionID := os.Getenv("AZURE_SUBSCRIPTION_ID")

		if clientID != "" && clientSecret != "" && tenantID != "" && subscriptionID != "" {
			credsContent := fmt.Sprintf(`{"clientId": "%s", "clientSecret": "%s", "tenantId": "%s", "subscriptionId": "%s"}`,
				clientID, clientSecret, tenantID, subscriptionID)
			cmd := exec.Command("kubectl", "create", "secret", "generic", secretName,
				"--namespace", "crossplane-system",
				"--from-literal=creds="+credsContent)
			if output, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("failed to create Azure credentials secret: %w\n%s", err, output)
			}
		} else {
			return fmt.Errorf("azure credentials (AZURE_CLIENT_ID, AZURE_CLIENT_SECRET, AZURE_TENANT_ID, AZURE_SUBSCRIPTION_ID) not found in environment variables")
		}

	case "civo":
		providerPackage = "crossplane-contrib/provider-civo:v0.4.0"
		secretName = "civo-creds"
		providerCR = `
apiVersion: civo.crossplane.io/v1alpha1
kind: ProviderConfig
metadata:
  name: default
spec:
  credentials:
    source: Secret
    secretRef:
      namespace: crossplane-system
      name: civo-creds
      key: credentials
`
		// Create Civo credentials secret
		if envConfig.GlobalSettings.ProviderCredentials.Civo != nil {
			credSource := envConfig.GlobalSettings.ProviderCredentials.Civo
			if credSource.Type == "environment" && credSource.EnvVar != "" {
				civoToken := os.Getenv(credSource.EnvVar)
				if civoToken != "" {
					cmd := exec.Command("kubectl", "create", "secret", "generic", secretName,
						"--namespace", "crossplane-system",
						"--from-literal=credentials="+civoToken)
					if output, err := cmd.CombinedOutput(); err != nil {
						return fmt.Errorf("failed to create Civo credentials secret: %w\n%s", err, output)
					}
				} else {
					return fmt.Errorf("civo token environment variable '%s' not found or empty", credSource.EnvVar)
				}
			}
		}

	default:
		return fmt.Errorf("unsupported provider: %s", envConfig.ResolvedProvider)
	}

	// Install the provider
	send(statusMsg(fmt.Sprintf("installing %s provider package", envConfig.ResolvedProvider)))
	installCmd := exec.Command("kubectl", "crossplane", "install", "provider", providerPackage)
	if output, err := installCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to install provider: %w\n%s", err, output)
	}

	// Wait for the provider to be ready
	send(statusMsg("waiting for provider to be ready"))
	time.Sleep(10 * time.Second) // Brief delay to allow CRDs to be registered

	// Apply the provider configuration
	if providerCR != "" {
		send(statusMsg("configuring provider"))
		applyProviderCmd := exec.Command("kubectl", "apply", "-f", "-")
		applyProviderCmd.Stdin = strings.NewReader(providerCR)
		if output, err := applyProviderCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to apply provider configuration: %w\n%s", err, output)
		}
	}

	return nil
}

// provisionCluster provisions a Kubernetes cluster through Crossplane according to environment config
func provisionCluster(envConfig *config.ResolvedEnvironmentConfig) error {
	send(statusMsg("generating cluster manifest"))

	// Create a YAML for the cluster based on environment configuration
	var clusterManifest string
	var clusterType string

	switch envConfig.ResolvedProvider {
	case "gke":
		clusterType = "GKECluster"
		clusterManifest = createGKEClusterManifest(envConfig)
	case "aws":
		clusterType = "EKSCluster"
		clusterManifest = createEKSClusterManifest(envConfig)
	case "do":
		clusterType = "DOKubernetesCluster"
		clusterManifest = createDOClusterManifest(envConfig)
	case "azure":
		clusterType = "AKSCluster"
		clusterManifest = createAKSClusterManifest(envConfig)
	case "civo":
		clusterType = "CivoKubernetesCluster"
		clusterManifest = createCivoClusterManifest(envConfig)
	default:
		return fmt.Errorf("unsupported provider for cluster provisioning: %s", envConfig.ResolvedProvider)
	}

	if clusterManifest == "" {
		return fmt.Errorf("failed to generate cluster manifest for provider %s", envConfig.ResolvedProvider)
	}

	// Apply the cluster manifest
	send(statusMsg(fmt.Sprintf("creating %s", clusterType)))
	applyCmd := exec.Command("kubectl", "apply", "-f", "-")
	applyCmd.Stdin = strings.NewReader(clusterManifest)
	if output, err := applyCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to apply cluster manifest: %w\n%s", err, output)
	}

	// Wait for cluster to be ready (checking status with kubectl)
	send(statusMsg(fmt.Sprintf("waiting for %s to be ready (this may take several minutes)", clusterType)))
	waitCmd := exec.Command("kubectl", "wait", "--for=condition=ready",
		fmt.Sprintf("%s.%s/%s", strings.ToLower(clusterType),
			getAPIGroupForProvider(envConfig.ResolvedProvider),
			environmentName),
		"--timeout=30m")

	if output, err := waitCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed waiting for cluster to be ready: %w\n%s", err, output)
	}

	// Get kubeconfig from the cluster
	send(statusMsg("fetching kubeconfig from cloud provider"))
	kubeconfigFilename := fmt.Sprintf("%s-kubeconfig.yaml", environmentName)

	// Command to get the secret content, decode it, and save to file
	// Using sh -c to handle the pipe and redirection correctly
	getSecretCmd := fmt.Sprintf("kubectl get secret %s-kubeconfig -o jsonpath='{.data.kubeconfig}' | base64 -d > %s",
		environmentName, kubeconfigFilename)

	cmd := exec.Command("sh", "-c", getSecretCmd)
	output, err := cmd.CombinedOutput()

	if err != nil {
		// Return an error if kubeconfig cannot be fetched
		errMsg := fmt.Sprintf("failed to fetch kubeconfig automatically using command:\n  %s\nError: %v\nOutput: %s\nYou may need to obtain it manually from the secret '%s-kubeconfig' in the control plane cluster.", getSecretCmd, err, string(output), environmentName)
		return errors.New(errMsg) // Use errors.New for pre-formatted string
	}

	send(statusMsg(fmt.Sprintf("kubeconfig saved to %s", kubeconfigFilename)))

	send(statusMsg(fmt.Sprintf("%s cluster provisioned successfully", clusterType)))
	return nil
}

// Helper function to determine API group for Crossplane provider
func getAPIGroupForProvider(provider v1alpha1.EnvironmentProvider) string {
	providerStr := string(provider)
	switch providerStr {
	case "gke":
		return "gcp.crossplane.io"
	case "aws":
		return "aws.crossplane.io"
	case "do":
		return "digitalocean.crossplane.io"
	case "azure":
		return "azure.crossplane.io"
	case "civo":
		return "civo.crossplane.io"
	default:
		return "unknown.crossplane.io"
	}
}

// Helper functions to create provider-specific cluster manifests
func createGKEClusterManifest(envConfig *config.ResolvedEnvironmentConfig) string {
	// Extract GKE specific configuration from envConfig.ResolvedClusterConfig
	machineType := "n1-standard-2" // Default value
	numNodes := 3                  // Default value

	// Ensure 'machineType' is treated as a string
	// if machineTypeValue, ok := envConfig.ResolvedClusterConfig["machineType"].(string); ok {
	// 	machineType = machineTypeValue
	// }

	// // Ensure 'numNodes' is treated as an integer
	// if nn, ok := envConfig.ResolvedClusterConfig["numNodes"].(float64); ok && nn > 0 {
	// 	numNodes = int(nn)
	// }

	return fmt.Sprintf(`
apiVersion: container.gcp.crossplane.io/v1beta1
kind: GKECluster
metadata:
  name: %s
spec:
  forProvider:
    location: %s
    initialNodeCount: %d
    nodeConfig:
      machineType: %s
    masterAuth:
      clientCertificateConfig:
        issueClientCertificate: false
    loggingService: logging.googleapis.com/kubernetes
    monitoringService: monitoring.googleapis.com/kubernetes
    networkRef:
      name: %s-network
  providerConfigRef:
    name: default
  writeConnectionSecretToRef:
    namespace: default
    name: %s-kubeconfig
---
apiVersion: compute.gcp.crossplane.io/v1beta1
kind: Network
metadata:
  name: %s-network
spec:
  forProvider:
    autoCreateSubnetworks: true
    routingConfig:
      routingMode: REGIONAL
  providerConfigRef:
    name: default
`, environmentName, envConfig.ResolvedRegion, numNodes, machineType, environmentName, environmentName, environmentName)
}

// Refactor to extract values from ResolvedClusterConfig slice
func createEKSClusterManifest(envConfig *config.ResolvedEnvironmentConfig) string {
	var nodeSize, k8sVersion string
	var nodeCount int

	// Iterate over ResolvedClusterConfig to extract values
	for _, config := range envConfig.ResolvedClusterConfig {
		switch config.Key {
		case "nodeSize":
			nodeSize = config.Value
		case "nodeCount":
			fmt.Sscanf(config.Value, "%d", &nodeCount) // Convert string to int
		case "kubernetesVersion":
			k8sVersion = config.Value
		}
	}

	// Set default values if not provided
	if nodeSize == "" {
		nodeSize = "t3.medium"
	}
	if nodeCount <= 0 {
		nodeCount = 2
	}
	if k8sVersion == "" {
		k8sVersion = "1.29"
	}

	return fmt.Sprintf(`
apiVersion: eks.aws.crossplane.io/v1beta1
kind: Cluster
metadata:
  name: %s
spec:
  forProvider:
    region: %s
    version: "%s"
    roleArnSelector:
      matchControllerRef: true
    resourcesVpcConfig:
      endpointPrivateAccess: true
      endpointPublicAccess: true
      subnetIdSelector:
        matchControllerRef: true
  writeConnectionSecretToRef:
    namespace: default
    name: %s-kubeconfig
  providerConfigRef:
    name: default
---
# Additional manifests for VPC, Subnets, and NodeGroups can be added here
`,
		environmentName, envConfig.ResolvedRegion, k8sVersion, environmentName)
}

// Refactor to extract values from ResolvedClusterConfig slice
func createDOClusterManifest(envConfig *config.ResolvedEnvironmentConfig) string {
	nodeSize := "s-2vcpu-2gb" // Default value
	nodeCount := 2            // Default value

	// Iterate over ResolvedClusterConfig to extract values
	for _, config := range envConfig.ResolvedClusterConfig {
		switch config.Key {
		case "nodeSize":
			nodeSize = config.Value
		case "nodeCount":
			fmt.Sscanf(config.Value, "%d", &nodeCount) // Convert string to int
		}
	}

	return fmt.Sprintf(`
apiVersion: kubernetes.digitalocean.crossplane.io/v1alpha1
kind: DOKubernetesCluster
metadata:
  name: %s
spec:
  forProvider:
    region: %s
    version: latest
    nodePools:
    - size: %s
      count: %d
      name: worker-pool
    maintenancePolicy:
      startTime: "00:00"
      day: wednesday
  providerConfigRef:
    name: default
  writeConnectionSecretToRef:
    namespace: default
    name: %s-kubeconfig
`, environmentName, envConfig.ResolvedRegion, nodeSize, nodeCount, environmentName)
}

// Refactor to extract values from ResolvedClusterConfig slice
func createAKSClusterManifest(envConfig *config.ResolvedEnvironmentConfig) string {
	vmSize := "Standard_D2_v2" // Default value
	nodeCount := 2             // Default value
	sshPublicKey := ""         // Will be populated from config or generated

	// Iterate over ResolvedClusterConfig to extract values
	for _, config := range envConfig.ResolvedClusterConfig {
		switch config.Key {
		case "vmSize":
			vmSize = config.Value
		case "nodeCount":
			fmt.Sscanf(config.Value, "%d", &nodeCount) // Convert string to int
		case "sshPublicKey":
			sshPublicKey = config.Value
		}
	}

	// Generate a placeholder SSH public key if not provided
	if sshPublicKey == "" {
		send(statusMsg("WARNING: No SSH public key found for AKS cluster. Using a placeholder value."))
		send(statusMsg("For production use, specify sshPublicKey in your environment config or set AZURE_SSH_PUBLIC_KEY env var."))
		sshPublicKey = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQD3F6tyPEFEzV0LX3X8BsXdMsQz1x2cEikKDEY0aIj41qgxMCP/iteneqXSIFZBp5vizPvaoIR3Um9xK7PGoW8giupGn+EPuxIA4cDM4vzOqOkiMPhz5XK0whEjkVzTo4+S0puvDZuwIsdiW9mxhJc7tgBNL0cYlWSYVkz4G/fslNfRPW5mYAM49f4fhtxPb5ok4Q2Lg9dPKVHO/Bgeu5woMc7RY0p1ej6D4CKFE6lymSDJpW0YHX/wqE9+cfEauh7xZcG0q9t2ta6F6fmX0agvpFyZo8aFbXeUBr7osSCJNgvavWbM/06niWrOvYX2xwWdhXmXSrbX8ZbabVohBK41 adhar-placeholder-key"
	}

	return fmt.Sprintf(`
apiVersion: compute.azure.crossplane.io/v1alpha1
kind: AKSCluster
metadata:
  name: %s
spec:
  forProvider:
    location: %s
    resourceGroupName: %s-rg
    dnsNamePrefix: %s
    nodeResourceGroup: %s-node-rg
    agentPoolProfiles:
    - name: agentpool
      count: %d
      vmSize: %s
      osType: Linux
      type: VirtualMachineScaleSets
      mode: System
    linuxProfile:
      adminUsername: adharadmin
      ssh:
        publicKeys:
        - keyData: %s
  providerConfigRef:
    name: default
  writeConnectionSecretToRef:
    namespace: default
    name: %s-kubeconfig
`, environmentName, envConfig.ResolvedRegion, environmentName, environmentName, environmentName, nodeCount, vmSize, sshPublicKey, environmentName)
}

// Refactor to extract values from ResolvedClusterConfig slice
func createCivoClusterManifest(envConfig *config.ResolvedEnvironmentConfig) string {
	nodeSize := "g4s.kube.small" // Default value
	nodeCount := 2               // Default value

	// Iterate over ResolvedClusterConfig to extract values
	for _, config := range envConfig.ResolvedClusterConfig {
		switch config.Key {
		case "nodeSize":
			nodeSize = config.Value
		case "nodeCount":
			fmt.Sscanf(config.Value, "%d", &nodeCount) // Convert string to int
		}
	}

	return fmt.Sprintf(`
apiVersion: k3s.civo.crossplane.io/v1alpha1
kind: CivoKubernetesCluster
metadata:
  name: %s
spec:
  forProvider:
    region: %s
    name: %s
    numTargetNodes: %d
    targetNodesSize: %s
    kubernetesVersion: 1.27.0+k3s1
    networkID: "default"
    applications: ""
    firewallID: "default"
    tags: "adhar"
  writeConnectionSecretToRef:
    namespace: default
    name: %s-kubeconfig
  providerConfigRef:
    name: default
`, environmentName, envConfig.ResolvedRegion, environmentName, nodeCount, nodeSize, environmentName)
}

// Custom writer to capture logs
type logWriter struct{}

func (w *logWriter) Write(p []byte) (n int, err error) {
	// Instead of discarding, send log messages to the UI
	if len(p) > 0 {
		send(logMsg(string(p)))
	}
	return len(p), nil
}

// A global channel for sending updates from the business logic to the UI
var updateChan = make(chan tea.Msg)

// send sends a message to the UI
func send(msg tea.Msg) {
	updateChan <- msg
}

// Listen for UI updates in a separate goroutine
func listenForUpdates(p *tea.Program) {
	for msg := range updateChan {
		p.Send(msg)
	}
}

// Add logic to download and store the latest manifests for core services
func downloadCoreServiceManifests() error {
	hackDir := "hack/"

	// Ensure the hack directory exists
	if _, err := os.Stat(hackDir); os.IsNotExist(err) {
		if err := os.Mkdir(hackDir, 0755); err != nil {
			return fmt.Errorf("failed to create hack directory: %w", err)
		}
	}

	// Download Crossplane manifest
	crossplaneURL := "https://raw.githubusercontent.com/crossplane/crossplane/release-2.0/install.yaml"
	if err := downloadFile(hackDir+"crossplane.yaml", crossplaneURL); err != nil {
		return fmt.Errorf("failed to download Crossplane manifest: %w", err)
	}

	// Download ArgoCD manifest
	argocdURL := "https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml"
	if err := downloadFile(hackDir+"argocd.yaml", argocdURL); err != nil {
		return fmt.Errorf("failed to download ArgoCD manifest: %w", err)
	}

	// Download Cilium manifest with all features enabled
	ciliumURL := "https://raw.githubusercontent.com/cilium/cilium/v1.14/install/kubernetes/cilium.yaml"
	if err := downloadFile(hackDir+"cilium.yaml", ciliumURL); err != nil {
		return fmt.Errorf("failed to download Cilium manifest: %w", err)
	}

	// Download Gitea manifest
	giteaURL := "https://raw.githubusercontent.com/go-gitea/gitea/main/contrib/k8s/gitea.yaml"
	if err := downloadFile(hackDir+"gitea.yaml", giteaURL); err != nil {
		return fmt.Errorf("failed to download Gitea manifest: %w", err)
	}

	return nil
}

// Helper function to download a file from a URL
func downloadFile(filepath string, url string) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("failed to download file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download file: status code %d", resp.StatusCode)
	}

	out, err := os.Create(filepath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to save file: %w", err)
	}

	return nil
}

func init() {
	// Register Kubernetes and Adhar types with the scheme
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme registers Adhar API types

	// Add the up command to the root command
	AddCommand(upCmd)

	// Define flags for the up command
	upCmd.Flags().BoolVarP(&waitForReadiness, "wait", "w", false, "Wait for all resources to be ready")
	upCmd.Flags().IntVarP(&timeout, "timeout", "t", 300, "Timeout in seconds for the operation")
	upCmd.Flags().StringVarP(&kubeconfigNamespace, "namespace", "n", "default", "Namespace to operate in")
	upCmd.Flags().BoolVar(&noSpinner, "no-spinner", false, "Disable spinner animation")
	upCmd.Flags().BoolVar(&verboseUp, "verbose", false, "Enable verbose output")
	upCmd.Flags().StringVarP(&environmentName, "file", "f", "adhar-config.yaml", "Path to the configuration file (defaults to adhar-config.yaml)")
}

// upCmd represents the up command
var upCmd = &cobra.Command{
	Use:   "up",
	Short: "Creates a local Kind cluster and starts the Adhar controller manager.",
	Long: `The 'up' command first checks for a local Kubernetes cluster managed by Kind
with the name '` + kindClusterName + `'. If the cluster does not exist, it creates one.
After ensuring the cluster is available, it starts the Adhar controller manager
targeting this local cluster.

During execution:
- Press 'i' to toggle detailed information about the cluster
- Press Ctrl+C to cancel the operation

Examples:
  # Start the local environment with default settings
  adhar up

  # Start and wait for all resources to be ready
  adhar up --wait

  # Specify a namespace and timeout
  adhar up --namespace=adhar-system --timeout=600`,
	Run: func(cmd *cobra.Command, args []string) {
		// Initialize spinner model
		s := spinner.New()

		// Use a more interesting spinner if animations are enabled
		if !noSpinner {
			s.Spinner = spinner.Spinner{
				Frames: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
				FPS:    10,
			}
		} else {
			s.Spinner = spinner.Dot
		}

		s.Style = lipgloss.NewStyle().Foreground(primaryColor)

		// Initialize model
		m := upModel{
			spinner:   s,
			startTime: time.Now(),
		}

		// Initialize Bubble Tea program with AltScreen enabled
		p := tea.NewProgram(m, tea.WithAltScreen()) // Explicitly use alt screen

		// Listen for updates in a separate goroutine
		go listenForUpdates(p)

		// Run the UI
		if _, err := p.Run(); err != nil { // Assign to blank identifier if finalModel isn't used
			fmt.Println("Error running program:", err)
			os.Exit(1)
		}

		// Close the update channel when done
		close(updateChan)
	},
}

// ensureKindCluster checks if the target Kind cluster exists and creates it if not.
func ensureKindCluster() error {
	// Check if kind executable exists
	_, err := exec.LookPath("kind")
	if err != nil {
		return fmt.Errorf("kind command not found in PATH. Please install kind: https://kind.sigs.k8s.io/docs/user/quick-start/#installation")
	}

	// Check if Docker is running
	dockerCmd := exec.Command("docker", "info")
	if err := dockerCmd.Run(); err != nil {
		return fmt.Errorf("docker is not running or not accessible. Please start Docker and try again")
	}

	cmd := exec.Command("kind", "get", "clusters")
	output, err := cmd.CombinedOutput()
	if err != nil {
		// If `kind get clusters` fails for other reasons, report it
		return fmt.Errorf("failed to check for existing kind clusters: %w\nOutput: %s", err, string(output))
	}

	if strings.Contains(string(output), kindClusterName) {
		send(statusMsg("cluster '" + kindClusterName + "' already exists"))

		// Verify that kubectl can connect to the cluster
		kubectlCmd := exec.Command("kubectl", "cluster-info")
		kubectlOutput, err := kubectlCmd.CombinedOutput()
		if err != nil {
			send(statusMsg("existing cluster found but kubectl cannot connect to it"))
			send(statusMsg("attempting to fix kubectl configuration..."))

			// Try to update kubeconfig
			fixCmd := exec.Command("kind", "export", "kubeconfig", "--name", kindClusterName)
			if fixOutput, fixErr := fixCmd.CombinedOutput(); fixErr != nil {
				return fmt.Errorf("found existing cluster but could not connect to it: %w\nOutput: %s", fixErr, string(fixOutput))
			}

			send(statusMsg("kubectl configuration updated"))
		} else if verboseUp {
			send(clusterInfoMsg(fmt.Sprintf("Cluster info:\n%s", string(kubectlOutput))))
		}

		time.Sleep(1 * time.Second) // A small delay for better UX
		return nil
	}

	send(stepMsg("Creating Kind cluster"))
	send(statusMsg("this may take a few minutes..."))

	// Create a config file with proper settings if needed
	configPath := ""
	if len(os.Getenv("KUBECONFIG")) > 0 {
		// User has custom KUBECONFIG, we should respect it
		send(statusMsg("using custom KUBECONFIG environment variable"))
	} else {
		// Create a temporary file for the Kind config
		tmpConfig, err := createKindConfig()
		if err != nil {
			send(statusMsg("using default kind configuration"))
		} else {
			configPath = tmpConfig
			defer os.Remove(tmpConfig) // Clean up the file when done
		}
	}

	// Build the create command with appropriate flags
	var createCmd *exec.Cmd
	if configPath != "" {
		createCmd = exec.Command("kind", "create", "cluster", "--name", kindClusterName, "--config", configPath)
	} else {
		createCmd = exec.Command("kind", "create", "cluster", "--name", kindClusterName)
	}

	// Capture output but don't display it directly
	output, err = createCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create kind cluster: %w\nOutput: %s", err, string(output))
	}

	send(statusMsg("cluster created successfully"))

	// Verify the cluster is responsive
	verifyCmd := exec.Command("kubectl", "cluster-info")
	_, err = verifyCmd.CombinedOutput()
	if err != nil {
		send(statusMsg("warning: cluster created but kubectl cannot connect to it yet"))
		send(statusMsg("waiting for cluster to become ready..."))

		// Wait for up to 30 seconds for the cluster to become ready
		for i := 0; i < 6; i++ {
			time.Sleep(5 * time.Second)
			verifyCmd := exec.Command("kubectl", "cluster-info")
			if _, verifyErr := verifyCmd.CombinedOutput(); verifyErr == nil {
				send(statusMsg("cluster is now ready"))
				break
			} else if i == 5 {
				send(statusMsg("cluster is still not fully responsive but continuing anyway"))
			}
		}
	}

	time.Sleep(1 * time.Second) // A small delay for better UX
	return nil
}

// createKindConfig creates a temporary file with a Kind cluster configuration
func createKindConfig() (string, error) {
	// Basic configuration with reasonable defaults
	config := `kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  kubeadmConfigPatches:
  - |
    kind: InitConfiguration
    nodeRegistration:
      kubeletExtraArgs:
        node-labels: "ingress-ready=true"
  extraPortMappings:
  - containerPort: 80
    hostPort: 80
    protocol: TCP
  - containerPort: 443
    hostPort: 443
    protocol: TCP
`

	// Create a temporary file
	tmpFile, err := os.CreateTemp("", "kind-config-*.yaml")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary kind config file: %w", err)
	}

	defer tmpFile.Close()

	// Write the configuration to the file
	if _, err := tmpFile.WriteString(config); err != nil {
		return "", fmt.Errorf("failed to write kind config: %w", err)
	}

	return tmpFile.Name(), nil
}

// startManager sets up and runs the controller manager.
// This function contains the core logic previously in main.go
func startManager() error {
	send(stepMsg("Starting controller manager"))
	send(statusMsg("configuring manager..."))

	// Redirect standard Go logger to prevent it from breaking TUI
	stdlog.SetOutput(ioutil.Discard)

	// Reuse flags or define defaults for manager configuration
	metricsAddr := ":8082" // Changed default port from :8080
	probeAddr := ":8081"
	enableLeaderElection := false
	secureMetrics := false

	// Get Kubernetes config (should now point to the Kind cluster)
	kubeConfig, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("unable to get kubeconfig: %w", err)
	}

	send(statusMsg("setting up manager..."))

	// Setup logging configuration to prevent output from interfering with UI
	// But capture logs for display in details mode
	logOpts := zapcr.Options{
		Development: false,
		TimeEncoder: zapcore.ISO8601TimeEncoder,
		DestWriter:  &logWriter{}, // Use custom writer to capture logs
	}

	// Create the logger using the configured options
	ctrlLogger := zapcr.New(zapcr.UseFlagOptions(&logOpts))

	// Configure controller-runtime to use our custom logger
	ctrl.SetLogger(ctrlLogger)

	// Setup Manager with custom log options
	mgr, err := ctrl.NewManager(kubeConfig, ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "271865fd.adhar.io",
		Metrics: metricsserver.Options{
			BindAddress:   metricsAddr,
			SecureServing: secureMetrics,
		},
		// Pass the logger directly
		Logger: ctrlLogger,
	})
	if err != nil {
		return fmt.Errorf("unable to start manager: %w", err)
	}

	// Add health checks
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up ready check: %w", err)
	}

	send(statusMsg("manager starting..."))

	// Start the manager in a goroutine
	go func() {
		if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
			send(errorMsg{fmt.Errorf("problem running manager: %w", err)})
			return
		}
		// This point is reached upon shutdown
		send(statusMsg("Manager stopped"))
	}()

	// Wait a moment to ensure startup begins properly
	time.Sleep(2 * time.Second)
	send(statusMsg("manager running"))

	return nil
}
