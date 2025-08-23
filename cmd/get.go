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

package main

import (
	"context"

	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time" // Import time package

	"adhar-io/adhar/platform/config"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
	appsv1 "k8s.io/api/apps/v1" // Import apps/v1
	corev1 "k8s.io/api/core/v1" // Import core/v1
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/duration" // Import duration package
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes" // Import standard clientset
	"k8s.io/client-go/tools/clientcmd"
)

// Define lipgloss styles for formatting
var (
	getBoldStyle     = lipgloss.NewStyle().Bold(true)
	getListItemStyle = lipgloss.NewStyle().SetString("• ")
	getCodeStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Background(lipgloss.Color("236")).Padding(0, 1)

	// Simple elegant listing styles
	secretsHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("99")). // Purple
				Background(lipgloss.Color("234")).
				Padding(0, 2).
				MarginBottom(1)

	secretNameStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("86")). // Cyan
			Width(25)

	namespaceStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")). // Gray
			Width(15)

	usernameStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")). // Light gray
			Width(20)

	passwordStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("154")). // Light green
			Bold(true)

	tokenStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("220")). // Yellow
			Bold(true)

	secretUrlStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("75")). // Light blue
			Underline(true)

	separatorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")).
			SetString("─")
)

var (
	// Resource type to group version kind mapping
	resourceTypes = map[string]schema.GroupVersionResource{
		"application": {
			Group:    "adhar.example.com",
			Version:  "v1alpha1",
			Resource: "applications",
		},
		"database": {
			Group:    "adhar.example.com",
			Version:  "v1alpha1",
			Resource: "databases",
		},
		"environment": {
			Group:    "adhar.example.com",
			Version:  "v1alpha1",
			Resource: "environments",
		},
		"managedtool": {
			Group:    "adhar.example.com",
			Version:  "v1alpha1",
			Resource: "managedtools",
		},
		"route": {
			Group:    "adhar.example.com",
			Version:  "v1alpha1",
			Resource: "routes",
		},
	}

	namespace      string
	outputFormat   string
	allNamespaces  bool
	showLabels     bool
	labelSelector  string
	fieldSelector  string
	resourceName   string
	configFilePath string // Add config file flag for environment listing
	secretProvider string // Add provider flag for secrets command
)

func init() {
	// Add the get command to the root command
	AddCommand(getCmd)

	// Add subcommands for various resource types
	getCmd.AddCommand(getApplicationCmd)
	getCmd.AddCommand(getDatabaseCmd)
	getCmd.AddCommand(getEnvironmentCmd)
	getCmd.AddCommand(getManagedToolCmd)
	getCmd.AddCommand(getRouteCmd)
	getCmd.AddCommand(getStatusCmd)  // Add the status command
	getCmd.AddCommand(getClusterCmd) // Add the cluster command
	getCmd.AddCommand(getSecretsCmd) // Add the secrets command

	// For convenience, add an 'all' command to get all resources
	getCmd.AddCommand(getAllCmd)

	// Global flags for all get commands
	getCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "default", "Namespace to query")
	getCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "table", "Output format (table, json, yaml)")
	getCmd.PersistentFlags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "Query across all namespaces")
	getCmd.PersistentFlags().BoolVar(&showLabels, "show-labels", false, "Show labels in the table output")
	getCmd.PersistentFlags().StringVarP(&labelSelector, "selector", "l", "", "Label selector to filter on")
	getCmd.PersistentFlags().StringVar(&fieldSelector, "field-selector", "", "Field selector to filter on")
	getCmd.PersistentFlags().String("kubeconfig", "", "Path to the kubeconfig file")

	// Add config file flag specifically to environment command
	getEnvironmentCmd.Flags().StringVarP(&configFilePath, "file", "f", "", "Path to configuration file to list available environments")

	// Add provider flag specifically to secrets command
	getSecretsCmd.Flags().StringVarP(&secretProvider, "provider", "p", "", "Filter secrets by provider (e.g., argocd, gitea, keycloak, harbor, grafana, minio, jupyterhub, vault, redis)")
}

// getKubeconfigPath determines the path to the kubeconfig file based on flag, env var, or default
func getKubeconfigPath(cmd *cobra.Command) string {
	kubeconfigFlag, _ := cmd.Flags().GetString("kubeconfig")
	if kubeconfigFlag != "" {
		return kubeconfigFlag
	}

	kubeconfigEnv := os.Getenv("KUBECONFIG")
	if kubeconfigEnv != "" {
		return kubeconfigEnv
	}

	// Default path
	home, err := os.UserHomeDir()
	if err == nil {
		return filepath.Join(home, ".kube", "config")
	}

	// Fallback if home dir cannot be found (less common)
	return ""
}

// getCmd represents the get command
var getCmd = &cobra.Command{
	Use:   "get [resource-type] [resource-name]",
	Short: "Display one or many Adhar resources",
	Long: `List or get Adhar resources like applications, databases, environments, and more.
Examples:
  # List all applications in the default namespace
  adhar get applications
  
  # Get a specific database in JSON format
  adhar get database my-database -o json
  
  # List all environments across all namespaces
  adhar get environments --all-namespaces
  
  # List applications with a specific label
  adhar get applications -l app=frontend`,
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) == 0 {
			// Display available resource types
			fmt.Println(headerStyle.Render("AVAILABLE RESOURCE TYPES:"))

			// Create a formatted table of resource types
			var sb strings.Builder
			for resourceType := range resourceTypes {
				sb.WriteString(fmt.Sprintf("  %s\n", titleStyle.Render(resourceType+"s")))
			}

			resourcesBox := borderStyle.Render(sb.String())
			fmt.Println(resourcesBox)

			// Show examples
			examples := `
Examples:
  adhar get applications                 # List all applications
  adhar get database my-db               # Get a specific database
  adhar get environments --all-namespaces # List envs across all namespaces
  adhar get all                          # List all Adhar resources
				`
			fmt.Println(subtitleStyle.Render("Usage Examples:"))
			fmt.Println(examples)

			return
		}

		// Handle the case where a resource type and name are provided
		resourceType := strings.ToLower(args[0])

		// Remove trailing 's' for consistent mapping
		resourceType = strings.TrimSuffix(resourceType, "s")

		// Check if the resource type is valid
		gvr, ok := resourceTypes[resourceType]
		if !ok {
			fmt.Printf("%s Unknown resource type: %s\n", errorStyle.Render("Error:"), resourceType)
			fmt.Println("Run 'adhar get' to see available resource types")
			return
		}

		// Set resource name if provided
		if len(args) > 1 {
			resourceName = args[1]
		}

		// Get the resources
		getResources(cmd, gvr, resourceType) // Pass cmd
	},
}

// Subcommands for each resource type
var getApplicationCmd = &cobra.Command{
	Use:     "applications [application-name]",
	Aliases: []string{"application", "app", "apps"},
	Short:   "Get Adhar applications",
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) > 0 {
			resourceName = args[0]
		}
		getResources(cmd, resourceTypes["application"], "application") // Pass cmd
	},
}

var getDatabaseCmd = &cobra.Command{
	Use:     "databases [database-name]",
	Aliases: []string{"database", "db", "dbs"},
	Short:   "Get Adhar databases",
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) > 0 {
			resourceName = args[0]
		}
		getResources(cmd, resourceTypes["database"], "database") // Pass cmd
	},
}

var getEnvironmentCmd = &cobra.Command{
	Use:     "environments [environment-name]",
	Aliases: []string{"environment", "env", "envs"},
	Short:   "Get Adhar environments or list environments from config file",
	Long: `Get Adhar environments from the cluster or list available environments from a configuration file.

Examples:
  # Get environments from cluster
  adhar get environments
  
  # Get specific environment
  adhar get env my-env
  
  # List environments from config file
  adhar get envs -f config.yaml`,
	Run: func(cmd *cobra.Command, args []string) {
		// Check if config file is provided for listing environments
		if configFilePath != "" {
			if err := listAvailableEnvironments(configFilePath); err != nil {
				fmt.Printf("Error: %v\n", err)
				os.Exit(1)
			}
			return
		}

		// Default behavior - get environments from cluster
		if len(args) > 0 {
			resourceName = args[0]
		}
		getResources(cmd, resourceTypes["environment"], "environment")
	},
}

var getManagedToolCmd = &cobra.Command{
	Use:     "managedtools [managedtool-name]",
	Aliases: []string{"managedtool", "tool", "tools"},
	Short:   "Get Adhar managed tools",
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) > 0 {
			resourceName = args[0]
		}
		getResources(cmd, resourceTypes["managedtool"], "managedtool") // Pass cmd
	},
}

var getRouteCmd = &cobra.Command{
	Use:     "routes [route-name]",
	Aliases: []string{"route"},
	Short:   "Get Adhar routes",
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) > 0 {
			resourceName = args[0]
		}
		getResources(cmd, resourceTypes["route"], "route") // Pass cmd
	},
}

// getStatusCmd represents the command to get the status of the Adhar platform
var getStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Get the status of the Adhar platform components",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(headerStyle.Render("Adhar Platform Status"))

		// Determine kubeconfig path
		kubeconfigPath := getKubeconfigPath(cmd)

		// Get kubernetes config
		config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			fmt.Printf("%s Failed to get kubeconfig from '%s': %v\n", errorStyle.Render("Error:"), kubeconfigPath, err)
			return
		}

		// Create standard clientset
		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			fmt.Printf("%s Failed to create clientset: %v\n", errorStyle.Render("Error:"), err)
			return
		}

		// Define controller manager details (adjust if necessary)
		controllerNamespace := "adhar-system" // Common namespace for controllers
		controllerDeploymentName := "adhar-controller-manager"

		ctx := context.Background()

		// Get the controller manager deployment
		deployment, err := clientset.AppsV1().Deployments(controllerNamespace).Get(ctx, controllerDeploymentName, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				fmt.Printf("%s Controller manager deployment '%s' not found in namespace '%s'. Is Adhar running?\n",
					errorStyle.Render("Error:"),
					controllerDeploymentName,
					controllerNamespace)
			} else {
				fmt.Printf("%s Failed to get controller manager deployment: %v\n", errorStyle.Render("Error:"), err)
			}
			return
		}

		// Check deployment status
		status := deployment.Status
		availableReplicas := status.AvailableReplicas
		desiredReplicas := *deployment.Spec.Replicas // Dereference pointer

		fmt.Printf("Controller Manager (%s/%s):\n", controllerNamespace, controllerDeploymentName)

		if availableReplicas > 0 && availableReplicas == desiredReplicas {
			// Check conditions for more details (optional, but good practice)
			isAvailable := false
			for _, condition := range status.Conditions {
				if condition.Type == appsv1.DeploymentAvailable && condition.Status == corev1.ConditionTrue { // Correctly use appsv1
					isAvailable = true
					break
				}
			}
			if isAvailable {
				fmt.Printf("  Status: %s (%d/%d replicas ready)\n",
					successStyle.Render("Running"),
					availableReplicas,
					desiredReplicas)
			} else {
				fmt.Printf("  Status: %s (Deployment available condition is not True)\n", warningStyle.Render("Degraded"))
			}
		} else if availableReplicas > 0 {
			fmt.Printf("  Status: %s (%d/%d replicas ready)\n",
				warningStyle.Render("Degraded"),
				availableReplicas,
				desiredReplicas)
		} else {
			fmt.Printf("  Status: %s (0 replicas available)\n", errorStyle.Render("Unavailable"))
			// Optionally print conditions for more debugging info
			if len(status.Conditions) > 0 {
				fmt.Println("  Conditions:")
				for _, condition := range status.Conditions {
					fmt.Printf("    - Type: %s, Status: %s, Reason: %s, Message: %s\n",
						condition.Type, condition.Status, condition.Reason, condition.Message)
				}
			}
		}

		// Add checks for other components (e.g., Crossplane, specific providers) here if needed
	},
}

var getClusterCmd = &cobra.Command{
	Use:     "cluster [cluster-name]",
	Aliases: []string{"clusters"},
	Short:   "Get cluster information",
	Long: `Get information about the current Kubernetes cluster or specific cluster.

Examples:
  # Get current cluster information
  adhar get cluster
  
  # Get specific cluster information (for Kind provider)
  adhar get cluster my-cluster`,
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) > 0 {
			resourceName = args[0]
		}
		getClusterInfo(cmd)
	},
}

var getSecretsCmd = &cobra.Command{
	Use:     "secrets [secret-name]",
	Aliases: []string{"secret"},
	Short:   "Get secrets from Kubernetes cluster",
	Long: `Get secrets from the Kubernetes cluster, with optional filtering by provider.

Examples:
  # Get all secrets
  adhar get secrets
  
  # Get ArgoCD admin password
  adhar get secrets -p argocd
  
  # Get Gitea admin password  
  adhar get secrets -p gitea
  
  # Get Keycloak admin credentials
  adhar get secrets -p keycloak
  
  # Get Harbor admin credentials
  adhar get secrets -p harbor
  
  # Get Grafana admin credentials
  adhar get secrets -p grafana
  
  # Get MinIO admin credentials
  adhar get secrets -p minio
  
  # Get specific secret
  adhar get secrets my-secret`,
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) > 0 {
			resourceName = args[0]
		}
		getSecrets(cmd)
	},
}

var getAllCmd = &cobra.Command{
	Use:   "all",
	Short: "Get all Adhar resources",
	Run: func(cmd *cobra.Command, args []string) {
		// Get all resource types
		for resourceType, gvr := range resourceTypes {
			fmt.Println(titleStyle.Render(strings.ToUpper(resourceType + "S")))
			// Reset resourceName for each type in 'all'
			originalResourceName := resourceName
			resourceName = ""
			getResources(cmd, gvr, resourceType) // Pass cmd
			resourceName = originalResourceName  // Restore if needed elsewhere
			fmt.Println()
		}
	},
}

// listAvailableEnvironments lists all available environments from the configuration file
func listAvailableEnvironments(configPath string) error {
	if configPath == "" {
		return fmt.Errorf("configuration file path is required. Use --file flag to specify the path")
	}

	// Load configuration
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file %s: %w", configPath, err)
	}

	var cfg config.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("failed to parse config file %s: %w", configPath, err)
	}

	if len(cfg.Environments) == 0 {
		fmt.Println("No environments found in configuration file.")
		return nil
	}

	fmt.Println("\nAvailable environments:")
	fmt.Println("======================")

	for envName, envConfig := range cfg.Environments {
		fmt.Printf("\n%s %s\n", getBoldStyle.Render("Environment:"), envName)

		// Show environment type
		if envConfig.Type != "" {
			fmt.Printf("  %s %s\n", getListItemStyle.Render("Type:"), envConfig.Type)
		}

		// Show provider (if explicitly specified)
		if envConfig.Provider != "" {
			fmt.Printf("  %s %s\n", getListItemStyle.Render("Provider:"), envConfig.Provider)
		} else {
			// Show which provider will be auto-assigned
			envType := envConfig.Type
			if envType == "" {
				// Auto-detect based on name
				envNameLower := strings.ToLower(envName)
				if strings.Contains(envNameLower, "prod") || strings.Contains(envNameLower, "staging") {
					envType = "production"
				} else {
					envType = "non-production"
				}
			}

			if envType == "production" {
				fmt.Printf("  %s %s (auto-assigned)\n", getListItemStyle.Render("Provider:"), cfg.GlobalSettings.ProductionProvider)
			} else {
				fmt.Printf("  %s %s (auto-assigned)\n", getListItemStyle.Render("Provider:"), cfg.GlobalSettings.NonProductionProvider)
			}
		}

		if envConfig.Region != "" {
			fmt.Printf("  %s %s\n", getListItemStyle.Render("Region:"), envConfig.Region)
		}
		if envConfig.Template != "" {
			fmt.Printf("  %s %s\n", getListItemStyle.Render("Template:"), envConfig.Template)
		}

		// Show cluster config summary
		if len(envConfig.ClusterConfig) > 0 {
			fmt.Printf("  %s\n", getListItemStyle.Render("Cluster Configuration:"))
			for _, cc := range envConfig.ClusterConfig {
				fmt.Printf("    %s: %s\n", cc.Key, cc.Value)
			}
		}
	}

	fmt.Printf("\nTo provision the complete platform (all environments), use:\n")
	fmt.Printf("%s\n", getCodeStyle.Render(fmt.Sprintf("adhar up --file %s", configPath)))

	fmt.Printf("\nTo provision a specific environment, use:\n")
	fmt.Printf("%s\n", getCodeStyle.Render(fmt.Sprintf("adhar up --file %s --env <environment-name>", configPath)))

	fmt.Printf("\nNote: Management cluster and platform services are automatically provisioned.\n")
	fmt.Printf("For dry-run mode, add the --dry-run flag.\n")

	return nil
}

// getResources gets and displays resources based on the specified GVR
func getResources(cmd *cobra.Command, gvr schema.GroupVersionResource, resourceType string) { // Add cmd parameter
	// Determine kubeconfig path
	kubeconfigPath := getKubeconfigPath(cmd)

	// Get kubernetes config
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		fmt.Printf("%s Failed to get kubeconfig from '%s': %v\n", errorStyle.Render("Error:"), kubeconfigPath, err)
		return
	}

	// Create dynamic client
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		fmt.Printf("%s Failed to create client: %v\n", errorStyle.Render("Error:"), err)
		return
	}

	// Determine namespace
	ns := namespace
	if allNamespaces {
		ns = ""
	}

	ctx := context.Background()

	// Create list options
	listOptions := metav1.ListOptions{
		LabelSelector: labelSelector,
		FieldSelector: fieldSelector,
	}

	// Get a single resource or list them
	if resourceName != "" {
		// Get a specific resource
		resource, err := dynamicClient.Resource(gvr).Namespace(ns).Get(ctx, resourceName, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				fmt.Printf("%s %s '%s' not found in namespace '%s'\n",
					errorStyle.Render("Error:"),
					resourceType,
					resourceName,
					ns)
			} else {
				fmt.Printf("%s Failed to get %s: %v\n",
					errorStyle.Render("Error:"),
					resourceType,
					err)
			}
			return
		}

		// Display the resource based on output format
		switch outputFormat {
		case "json":
			// Convert to JSON and print
			jsonBytes, err := resource.MarshalJSON()
			if err != nil {
				fmt.Printf("%s Failed to marshal to JSON: %v\n", errorStyle.Render("Error:"), err)
				return
			}
			fmt.Println(string(jsonBytes))

		case "yaml":
			// For simplicity, we're not implementing full YAML serialization here
			fmt.Println(subtitleStyle.Render("YAML output not implemented in this version"))

		default: // "table" or any other value
			// Print a simple formatted output
			fmt.Println()
			fmt.Printf("%s %s\n",
				titleStyle.Render("Name:"),
				resource.GetName())
			fmt.Printf("%s %s\n",
				titleStyle.Render("Namespace:"),
				resource.GetNamespace())
			fmt.Printf("%s %s\n",
				titleStyle.Render("Created:"),
				resource.GetCreationTimestamp().String())

			// Print labels if requested
			if showLabels && len(resource.GetLabels()) > 0 {
				fmt.Printf("%s ", titleStyle.Render("Labels:"))
				for k, v := range resource.GetLabels() {
					fmt.Printf("%s=%s ", k, v)
				}
				fmt.Println()
			}

			// Print annotations (simplified)
			if len(resource.GetAnnotations()) > 0 {
				fmt.Printf("%s %d annotations\n",
					titleStyle.Render("Annotations:"),
					len(resource.GetAnnotations()))
			}
		}
	} else {
		// List resources
		resourceList, err := dynamicClient.Resource(gvr).Namespace(ns).List(ctx, listOptions)
		if err != nil {
			fmt.Printf("%s Failed to list %ss: %v\n",
				errorStyle.Render("Error:"),
				resourceType,
				err)
			return
		}

		// Handle empty list
		if len(resourceList.Items) == 0 {
			fmt.Printf("No %ss found\n", resourceType)
			return
		}

		// Display results based on output format
		switch outputFormat {
		case "json":
			// Convert to JSON and print
			jsonBytes, err := resourceList.MarshalJSON()
			if err != nil {
				fmt.Printf("%s Failed to marshal to JSON: %v\n", errorStyle.Render("Error:"), err)
				return
			}
			fmt.Println(string(jsonBytes))

		case "yaml":
			// For simplicity, we're not implementing full YAML serialization here
			fmt.Println(subtitleStyle.Render("YAML output not implemented in this version"))

		default: // "table" or any other value
			// Create a tabwriter for nice formatting
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

			// Print headers
			headers := []string{"NAME", "NAMESPACE", "AGE"}
			if showLabels {
				headers = append(headers, "LABELS")
			}
			fmt.Fprintln(w, strings.Join(headers, "\t"))

			// Print each resource
			for _, item := range resourceList.Items {
				// Calculate age using HumanDuration
				age := "unknown"
				// Check if the underlying Time object is zero
				if !item.GetCreationTimestamp().Time.IsZero() {
					age = duration.HumanDuration(time.Since(item.GetCreationTimestamp().Time))
				}

				row := []string{item.GetName(), item.GetNamespace(), age}

				// Add labels if requested
				if showLabels {
					labelStrings := []string{}
					for k, v := range item.GetLabels() {
						labelStrings = append(labelStrings, fmt.Sprintf("%s=%s", k, v))
					}
					row = append(row, strings.Join(labelStrings, ","))
				}

				fmt.Fprintln(w, strings.Join(row, "\t"))
			}
			w.Flush()
		}
	}
}

// getClusterInfo retrieves and displays cluster information
func getClusterInfo(cmd *cobra.Command) {
	fmt.Println(headerStyle.Render("Cluster Information"))

	// Determine kubeconfig path
	kubeconfigPath := getKubeconfigPath(cmd)

	// Get kubernetes config
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		fmt.Printf("%s Failed to get kubeconfig from '%s': %v\n", errorStyle.Render("Error:"), kubeconfigPath, err)
		fmt.Printf("\n%s\n", infoStyle.Render("Make sure you have a Kubernetes cluster running and kubectl is configured."))
		fmt.Printf("For local development, you can create a cluster with: %s\n", getCodeStyle.Render("adhar up"))
		return
	}

	// Create standard clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		fmt.Printf("%s Failed to create clientset: %v\n", errorStyle.Render("Error:"), err)
		return
	}

	ctx := context.Background()

	// Get cluster version
	version, err := clientset.Discovery().ServerVersion()
	if err != nil {
		fmt.Printf("%s Failed to get cluster version: %v\n", errorStyle.Render("Error:"), err)
		return
	}

	// Get current context
	currentContext := ""
	if kubeconfigPath != "" {
		if config, err := clientcmd.LoadFromFile(kubeconfigPath); err == nil {
			currentContext = config.CurrentContext
		}
	}

	// Get nodes
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		fmt.Printf("%s Failed to get nodes: %v\n", errorStyle.Render("Error:"), err)
		return
	}

	// Display cluster information
	fmt.Printf("Current Context: %s\n", successStyle.Render(currentContext))
	fmt.Printf("Kubernetes Version: %s\n", version.String())
	fmt.Printf("Nodes: %d\n", len(nodes.Items))

	// Display node information in table format
	if len(nodes.Items) > 0 {
		fmt.Println("\nNodes:")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSTATUS\tROLES\tAGE\tVERSION")

		for _, node := range nodes.Items {
			// Get node status
			status := "Unknown"
			for _, condition := range node.Status.Conditions {
				if condition.Type == corev1.NodeReady {
					if condition.Status == corev1.ConditionTrue {
						status = "Ready"
					} else {
						status = "NotReady"
					}
					break
				}
			}

			// Get node roles
			roles := []string{}
			for label := range node.Labels {
				if strings.HasPrefix(label, "node-role.kubernetes.io/") {
					role := strings.TrimPrefix(label, "node-role.kubernetes.io/")
					if role == "" {
						role = "worker"
					}
					roles = append(roles, role)
				}
			}
			if len(roles) == 0 {
				roles = append(roles, "worker")
			}

			// Calculate age
			age := "unknown"
			if !node.CreationTimestamp.Time.IsZero() {
				age = duration.HumanDuration(time.Since(node.CreationTimestamp.Time))
			}

			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				node.Name,
				status,
				strings.Join(roles, ","),
				age,
				node.Status.NodeInfo.KubeletVersion)
		}
		w.Flush()
	}

	// Check if this is a Kind cluster
	if strings.Contains(currentContext, "kind-") {
		fmt.Printf("\nProvider: %s\n", successStyle.Render("Kind (Local Development)"))
		clusterName := strings.TrimPrefix(currentContext, "kind-")
		fmt.Printf("Cluster Name: %s\n", clusterName)

		// Show additional Kind-specific information
		fmt.Printf("\n%s\n", getBoldStyle.Render("Kind Cluster Details:"))
		fmt.Printf("  • Local development cluster running in Docker\n")
		fmt.Printf("  • Access services via: https://adhar.localtest.me\n")
		fmt.Printf("  • Get service passwords with: %s\n", getCodeStyle.Render("adhar get secrets -p <provider>"))
	} else {
		// Try to detect other providers
		if strings.Contains(strings.ToLower(currentContext), "gke") {
			fmt.Printf("\nProvider: %s\n", successStyle.Render("Google Kubernetes Engine (GKE)"))
		} else if strings.Contains(strings.ToLower(currentContext), "eks") {
			fmt.Printf("\nProvider: %s\n", successStyle.Render("Amazon Elastic Kubernetes Service (EKS)"))
		} else if strings.Contains(strings.ToLower(currentContext), "aks") {
			fmt.Printf("\nProvider: %s\n", successStyle.Render("Azure Kubernetes Service (AKS)"))
		} else {
			fmt.Printf("\nProvider: %s\n", infoStyle.Render("Unknown"))
		}
	}
}

// getSecrets retrieves and displays secrets from the cluster
// Core package secrets mapping
const (
	argoCDAdminUsername          = "admin"
	argoCDInitialAdminSecretName = "argocd-initial-admin-secret"
	giteaAdminSecretName         = "gitea-credential"
)

// Well known secrets that are part of the core packages
var corePkgSecrets = map[string][]string{
	"argocd":           {argoCDInitialAdminSecretName},
	"gitea":            {"gitea"},
	"cert-manager":     {"cert-manager-webhook-ca", "letsencrypt-private-key"},
	"keycloak":         {"keycloak", "keycloak-admin"},
	"harbor":           {"harbor-core", "harbor-admin"},
	"grafana":          {"grafana", "grafana-admin"},
	"prometheus":       {"prometheus-server"},
	"minio":            {"minio", "minio-root-secret"},
	"jupyterhub":       {"jupyterhub", "hub-secret", "proxy-secret"},
	"headlamp":         {"headlamp"},
	"vault":            {"vault-unseal-keys", "vault-root-token"},
	"external-secrets": {"external-secrets-webhook"},
	"crossplane":       {"crossplane-root-ca"},
	"redis":            {"redis", "redis-auth"},
}

// SecretInfo represents a secret with its details
type SecretInfo struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	IsCore    bool              `json:"is_core"`
	Username  string            `json:"username,omitempty"`
	Password  string            `json:"password,omitempty"`
	Token     string            `json:"token,omitempty"`
	Data      map[string]string `json:"data,omitempty"`
}

func getSecrets(cmd *cobra.Command) {
	fmt.Println("🔍 Discovering platform secrets...")

	// Determine kubeconfig path
	kubeconfigPath := getKubeconfigPath(cmd)

	// Get kubernetes config
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		fmt.Printf("%s Failed to get kubeconfig from '%s': %v\n", errorStyle.Render("Error:"), kubeconfigPath, err)
		fmt.Printf("\n%s\n", infoStyle.Render("Make sure you have a Kubernetes cluster running and kubectl is configured."))
		fmt.Printf("For local development, you can create a cluster with: %s\n", getCodeStyle.Render("adhar up"))
		return
	}

	fmt.Println("📋 Retrieving secrets from cluster...")

	// Create standard clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		fmt.Printf("%s Failed to create clientset: %v\n", errorStyle.Render("Error:"), err)
		return
	}

	ctx := context.Background()

	// If a specific secret name is provided, get that secret
	if resourceName != "" {
		fmt.Println("🔍 Getting specific secret...")
		getSpecificSecret(ctx, clientset, resourceName)
		return
	}

	// If provider filter is specified, get secrets for that provider
	if secretProvider != "" {
		fmt.Println("🔍 Filtering secrets by provider...")
		getProviderSecrets(ctx, clientset, secretProvider)
		return
	}

	// Otherwise, list all secrets
	fmt.Println("📊 Compiling secrets list...")
	listAllSecrets(ctx, clientset)
}

// getSpecificSecret retrieves a specific secret by name
func getSpecificSecret(ctx context.Context, clientset *kubernetes.Clientset, secretName string) {
	// Try to find the secret in common namespaces
	namespaces := []string{"default", "adhar-system", "argocd", "gitea", "nginx-system", "kube-system"}
	if namespace != "" {
		namespaces = []string{namespace}
	}

	var foundSecret *corev1.Secret

	for _, ns := range namespaces {
		secret, err := clientset.CoreV1().Secrets(ns).Get(ctx, secretName, metav1.GetOptions{})
		if err == nil {
			foundSecret = secret
			break
		}
	}

	if foundSecret == nil {
		fmt.Printf("❌ Error: Secret '%s' not found in any namespace\n", secretName)
		return
	}

	// Clean specific secret header without boxes
	fmt.Printf("🔐 SECRET: %s\n\n", secretName)

	// Display the found secret
	secretInfo := populateSecret(*foundSecret, false)
	displaySecrets([]SecretInfo{secretInfo})
}

// getProviderSecrets retrieves secrets for a specific provider
func getProviderSecrets(ctx context.Context, clientset *kubernetes.Clientset, provider string) {
	secrets := []SecretInfo{}

	// Check if provider has core package secrets
	if secretNames, ok := corePkgSecrets[provider]; ok {
		for _, secretName := range secretNames {
			secret, err := getCorePackageSecret(ctx, clientset, provider, secretName)
			if err != nil {
				if errors.IsNotFound(err) {
					fmt.Printf("%s Secret '%s' not found for provider '%s'\n",
						errorStyle.Render("Warning:"), secretName, provider)
					continue
				}
				fmt.Printf("%s Error getting secret '%s' for provider '%s': %v\n",
					errorStyle.Render("Error:"), secretName, provider, err)
				continue
			}
			secrets = append(secrets, populateSecret(*secret, true))
		}
	} else {
		fmt.Printf("❌ Error: Unknown provider '%s'. Available providers: %v\n", provider, getAvailableProviders())
		return
	}

	if len(secrets) == 0 {
		fmt.Printf("🔍 No secrets found for provider '%s'\n", provider)
		return
	}

	// Clean provider header without boxes
	fmt.Printf("🔐 %s SECRETS • Found %d secrets\n\n", strings.ToUpper(provider), len(secrets))
	displaySecrets(secrets)
}

// Helper functions for the secrets implementation

// getCorePackageSecret retrieves a core package secret from the appropriate namespace
func getCorePackageSecret(ctx context.Context, clientset *kubernetes.Clientset, packageName, secretName string) (*corev1.Secret, error) {
	// Map package names to their namespaces
	namespaceMap := map[string]string{
		"argocd":           "adhar-system",
		"gitea":            "adhar-system",
		"cert-manager":     "cert-manager",
		"keycloak":         "adhar-system", // Most services are in adhar-system
		"harbor":           "adhar-system",
		"grafana":          "adhar-system",
		"prometheus":       "adhar-system",
		"minio":            "adhar-system",
		"jupyterhub":       "adhar-system",
		"headlamp":         "adhar-system",
		"vault":            "adhar-system",
		"external-secrets": "external-secrets",
		"crossplane":       "crossplane-system",
		"redis":            "adhar-system",
	}

	namespace, ok := namespaceMap[packageName]
	if !ok {
		namespace = "adhar-system" // default namespace
	}

	// Try to get the secret from the primary namespace
	secret, err := clientset.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		// If primary namespace fails, try adhar-system as fallback
		if namespace != "adhar-system" {
			secret, err = clientset.CoreV1().Secrets("adhar-system").Get(ctx, secretName, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}

	// Add username for ArgoCD admin secret
	if secretName == argoCDInitialAdminSecretName && secret.Data != nil {
		if secret.Data == nil {
			secret.Data = make(map[string][]byte)
		}
		secret.Data["username"] = []byte(argoCDAdminUsername)
	}

	return secret, nil
}

// getGiteaAdminCredentials extracts Gitea admin credentials from the deployment environment variables
func getGiteaAdminCredentials() (string, string) {
	// Determine kubeconfig path (similar to getKubeconfigPath but without cmd parameter)
	kubeconfigPath := os.Getenv("KUBECONFIG")
	if kubeconfigPath == "" {
		// Default path
		if home, err := os.UserHomeDir(); err == nil {
			kubeconfigPath = filepath.Join(home, ".kube", "config")
		}
	}

	// Get kubernetes config
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return "", ""
	}

	// Create clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return "", ""
	}

	ctx := context.Background()

	// Get Gitea deployment
	deployment, err := clientset.AppsV1().Deployments("adhar-system").Get(ctx, "gitea", metav1.GetOptions{})
	if err != nil {
		return "", ""
	}

	// Look for admin credentials in init containers (configure-gitea)
	for _, initContainer := range deployment.Spec.Template.Spec.InitContainers {
		if initContainer.Name == "configure-gitea" {
			var username, password string
			for _, env := range initContainer.Env {
				if env.Name == "GITEA_ADMIN_USERNAME" {
					username = env.Value
				}
				if env.Name == "GITEA_ADMIN_PASSWORD" {
					password = env.Value
				}
			}
			if username != "" && password != "" {
				return username, password
			}
		}
	}

	return "", ""
}

// populateSecret converts a Kubernetes secret to SecretInfo
func populateSecret(secret corev1.Secret, isCoreSecret bool) SecretInfo {
	secretInfo := SecretInfo{
		Name:      secret.Name,
		Namespace: secret.Namespace,
		IsCore:    isCoreSecret,
	}

	if isCoreSecret {
		if secret.Data != nil {
			// Handle standard username/password/token fields
			if username, ok := secret.Data["username"]; ok {
				secretInfo.Username = string(username)
			}
			if password, ok := secret.Data["password"]; ok {
				secretInfo.Password = string(password)
			}
			if token, ok := secret.Data["token"]; ok {
				secretInfo.Token = string(token)
			}

			// Handle platform-specific secrets
			switch secret.Name {
			case "gitea":
				// For the main gitea secret, get actual admin credentials from deployment
				if giteaUsername, giteaPassword := getGiteaAdminCredentials(); giteaUsername != "" && giteaPassword != "" {
					secretInfo.Username = giteaUsername
					secretInfo.Password = giteaPassword
					secretInfo.Token = "Admin credentials for Gitea web interface"
				} else {
					secretInfo.Token = "Contains Gitea configuration scripts"
				}
			case "keycloak-admin":
				// Extract Keycloak admin credentials
				if adminUser, ok := secret.Data["admin-user"]; ok {
					secretInfo.Username = string(adminUser)
				} else if adminUser, ok := secret.Data["username"]; ok {
					secretInfo.Username = string(adminUser)
				}
				if adminPassword, ok := secret.Data["admin-password"]; ok {
					secretInfo.Password = string(adminPassword)
				}
				secretInfo.Token = "Keycloak admin credentials"
			case "harbor-admin", "harbor-core":
				// Extract Harbor admin credentials
				if secret.Name == "harbor-admin" {
					if adminPassword, ok := secret.Data["HARBOR_ADMIN_PASSWORD"]; ok {
						secretInfo.Username = "admin"
						secretInfo.Password = string(adminPassword)
						secretInfo.Token = "Harbor admin credentials"
					}
				} else {
					secretInfo.Token = "Harbor core configuration"
				}
			case "grafana", "grafana-admin":
				// Extract Grafana admin credentials
				if adminUser, ok := secret.Data["admin-user"]; ok {
					secretInfo.Username = string(adminUser)
				} else {
					secretInfo.Username = "admin" // default
				}
				if adminPassword, ok := secret.Data["admin-password"]; ok {
					secretInfo.Password = string(adminPassword)
				}
				secretInfo.Token = "Grafana admin credentials"
			case "minio", "minio-root-secret":
				// Extract MinIO root credentials
				if rootUser, ok := secret.Data["rootUser"]; ok {
					secretInfo.Username = string(rootUser)
				} else if accessKey, ok := secret.Data["accesskey"]; ok {
					secretInfo.Username = string(accessKey)
				}
				if rootPassword, ok := secret.Data["rootPassword"]; ok {
					secretInfo.Password = string(rootPassword)
				} else if secretKey, ok := secret.Data["secretkey"]; ok {
					secretInfo.Password = string(secretKey)
				}
				secretInfo.Token = "MinIO root credentials"
			case "jupyterhub":
				// Extract JupyterHub admin token
				if _, ok := secret.Data["values.yaml"]; ok {
					// For JupyterHub, the secret often contains YAML config
					secretInfo.Token = "JupyterHub configuration (check data for admin token)"
				} else if proxyToken, ok := secret.Data["proxy.secretToken"]; ok {
					secretInfo.Token = string(proxyToken)
				}
			case "vault-root-token":
				// Extract Vault root token
				if rootToken, ok := secret.Data["root-token"]; ok {
					secretInfo.Token = string(rootToken)
				}
			case "redis-auth", "redis":
				// Extract Redis auth
				if redisPassword, ok := secret.Data["redis-password"]; ok {
					secretInfo.Password = string(redisPassword)
					secretInfo.Token = "Redis authentication password"
				} else if auth, ok := secret.Data["auth"]; ok {
					secretInfo.Password = string(auth)
					secretInfo.Token = "Redis authentication"
				}
			}
		}
	} else {
		if secret.Data != nil {
			// For generic secrets, show the first few key-value pairs
			secretInfo.Data = make(map[string]string)
			for key, value := range secret.Data {
				secretInfo.Data[key] = string(value)
			}
			// Set a descriptive token for generic secrets
			if len(secret.Data) > 0 {
				keys := make([]string, 0, len(secret.Data))
				for k := range secret.Data {
					keys = append(keys, k)
				}
				if len(keys) > 0 {
					limit := 3
					if len(keys) < limit {
						limit = len(keys)
					}
					secretInfo.Token = fmt.Sprintf("Contains: %s", strings.Join(keys[:limit], ", "))
				}
			}
		}
	}

	return secretInfo
}

// getAvailableProviders returns list of available providers
func getAvailableProviders() []string {
	providers := make([]string, 0, len(corePkgSecrets))
	for provider := range corePkgSecrets {
		providers = append(providers, provider)
	}
	return providers
}

// getSecretIcon returns the appropriate icon for a secret
func getSecretIcon(secretName string) string {
	switch {
	case strings.Contains(secretName, "argocd"):
		return "🚀"
	case strings.HasPrefix(secretName, "gitea"):
		return "📦"
	case strings.HasPrefix(secretName, "gitea"):
		return "🔐"
	case strings.HasPrefix(secretName, "harbor"):
		return "🐳"
	case strings.HasPrefix(secretName, "grafana"):
		return "📊"
	case strings.HasPrefix(secretName, "minio"):
		return "🗄️"
	case strings.HasPrefix(secretName, "vault"):
		return "🔒"
	case strings.HasPrefix(secretName, "jupyterhub"):
		return "🔬"
	case strings.HasPrefix(secretName, "prometheus"):
		return "📈"
	case strings.HasPrefix(secretName, "redis"):
		return "💾"
	case strings.HasPrefix(secretName, "postgresql"):
		return "🗃️"
	default:
		return "⚙️"
	}
}

// displaySecrets displays a list of secrets with nice formatting
// getSecretType determines the type/category of a secret for better display
func getSecretType(secretName string) (secretType, icon, url string) {
	switch {
	case strings.Contains(secretName, "argocd"):
		return "GitOps Platform", "🚀", "https://adhar.localtest.me/argocd/"
	case strings.HasPrefix(secretName, "gitea"):
		return "Git Repository", "📦", "https://adhar.localtest.me/gitea/"
	case strings.HasPrefix(secretName, "keycloak"):
		return "Identity Provider", "🔑", "https://adhar.localtest.me/keycloak/"
	case strings.HasPrefix(secretName, "harbor"):
		return "Container Registry", "🐳", "https://adhar.localtest.me/harbor/"
	case strings.HasPrefix(secretName, "grafana"):
		return "Monitoring Dashboard", "📊", "https://adhar.localtest.me/grafana/"
	case strings.HasPrefix(secretName, "minio"):
		return "Object Storage", "🗄️", "https://adhar.localtest.me/minio/"
	case strings.HasPrefix(secretName, "vault"):
		return "Secrets Management", "🔒", "https://adhar.localtest.me/vault/"
	case strings.HasPrefix(secretName, "jupyterhub"):
		return "Data Science Platform", "🔬", "https://adhar.localtest.me/jupyterhub/"
	case strings.HasPrefix(secretName, "prometheus"):
		return "Metrics Collection", "📈", ""
	case strings.HasPrefix(secretName, "redis"):
		return "In-Memory Database", "💾", ""
	case strings.HasPrefix(secretName, "postgresql"):
		return "SQL Database", "🗃️", ""
	default:
		return "Platform Component", "⚙️", ""
	}
}

// formatSecretValue applies appropriate formatting based on field type
func formatSecretValue(key, value string) string {
	sensitiveFields := []string{"password", "token", "key", "secret", "credential"}

	for _, field := range sensitiveFields {
		if strings.Contains(strings.ToLower(key), field) {
			return passwordStyle.Render(value)
		}
	}

	return value
}

func displaySecrets(secrets []SecretInfo) {
	// Create clean header with proper column widths
	headerContent := lipgloss.JoinHorizontal(
		lipgloss.Left,
		lipgloss.NewStyle().Width(35).Bold(true).Foreground(lipgloss.Color("99")).Render("🔐 SECRET"),
		lipgloss.NewStyle().Width(25).Bold(true).Foreground(lipgloss.Color("99")).Render("👤 USERNAME"),
		lipgloss.NewStyle().Width(35).Bold(true).Foreground(lipgloss.Color("99")).Render("🔑 PASSWORD"),
	)

	fmt.Println(headerContent)
	fmt.Println(strings.Repeat("─", 95)) // Simple separator line

	// Create clean rows without boxes
	for _, secret := range secrets {
		// Format secret name with icon and proper truncation
		secretName := getSecretIcon(secret.Name) + " " + secret.Name
		if len(secretName) > 33 {
			secretName = secretName[:30] + "..."
		}

		// Format username with proper truncation
		username := secret.Username
		if len(username) > 23 {
			username = username[:20] + "..."
		}

		// Format password with proper truncation
		password := secret.Password
		if len(password) > 33 {
			password = password[:30] + "..."
		}

		secretRow := lipgloss.JoinHorizontal(
			lipgloss.Left,
			lipgloss.NewStyle().Width(35).Render(secretName),
			lipgloss.NewStyle().Width(25).Render(username),
			lipgloss.NewStyle().Width(35).Render(password),
		)
		fmt.Println(secretRow)
	}
}

// hiddenSecrets are internal/technical secrets that should be hidden from user display
var hiddenSecrets = map[string]bool{
	"external-secrets-webhook": true,
	"crossplane-root-ca":       true,
	"cert-manager-webhook-ca":  true,
	"letsencrypt-private-key":  true,
	"argocd-secret":            true, // Hide internal ArgoCD config
	"argocd-server-tls":        true,
	"argocd-repo-server-tls":   true,
	"gitea-admin-secret":       true, // Hide if it's a duplicate
}

// listAllSecrets lists all core and labeled secrets in the cluster
func listAllSecrets(ctx context.Context, clientset *kubernetes.Clientset) {
	secrets := []SecretInfo{}

	// Get all core package secrets (known secrets)
	for provider, secretNames := range corePkgSecrets {
		for _, secretName := range secretNames {
			secret, err := getCorePackageSecret(ctx, clientset, provider, secretName)
			if err != nil {
				if errors.IsNotFound(err) {
					continue // Skip missing secrets
				}
				fmt.Printf("⚠️  Warning: Error getting secret '%s' for provider '%s': %v\n", secretName, provider, err)
				continue
			}
			// Skip hidden secrets
			if !hiddenSecrets[secret.Name] {
				secrets = append(secrets, populateSecret(*secret, true))
			}
		}
	}

	// Also get secrets with Adhar labels
	labeledSecrets, err := getSecretsByAdharLabel(ctx, clientset)
	if err != nil {
		fmt.Printf("⚠️  Warning: Error getting labeled secrets: %v\n", err)
	} else {
		for _, secret := range labeledSecrets.Items {
			// Skip hidden secrets
			if hiddenSecrets[secret.Name] {
				continue
			}

			// Avoid duplicates by checking if we already have this secret
			found := false
			for _, existingSecret := range secrets {
				if existingSecret.Name == secret.Name && existingSecret.Namespace == secret.Namespace {
					found = true
					break
				}
			}
			if !found {
				isCore := false
				if packageName, ok := secret.Labels["adhar.io/package-name"]; ok {
					_, isCore = corePkgSecrets[packageName]
				}
				secrets = append(secrets, populateSecret(secret, isCore))
			}
		}
	}

	if len(secrets) == 0 {
		fmt.Println("🔍 No secrets found in the cluster")
		return
	}

	// Clean title without boxes
	fmt.Printf("🔐 PLATFORM SECRETS • Found %d secrets\n\n", len(secrets))

	displaySecrets(secrets)
}

// getSecretsByAdharLabel gets secrets with Adhar labels
func getSecretsByAdharLabel(ctx context.Context, clientset *kubernetes.Clientset) (*corev1.SecretList, error) {
	// Look for secrets with the adhar.io/cli-secret=true label
	labelSelector := "adhar.io/cli-secret=true"

	secrets, err := clientset.CoreV1().Secrets("").List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})

	return secrets, err
}
