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
	getSecretsCmd.Flags().StringVarP(&secretProvider, "provider", "p", "", "Filter secrets by provider (e.g., argocd, gitea, nginx)")
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
	"argocd": {argoCDInitialAdminSecretName},
	"gitea":  {"gitea"},
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
	fmt.Println(headerStyle.Render("Kubernetes Secrets"))

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

	// If a specific secret name is provided, get that secret
	if resourceName != "" {
		getSpecificSecret(ctx, clientset, resourceName)
		return
	}

	// If provider filter is specified, get secrets for that provider
	if secretProvider != "" {
		getProviderSecrets(ctx, clientset, secretProvider)
		return
	}

	// Otherwise, list all secrets
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
		fmt.Printf("%s Secret '%s' not found in any namespace\n", errorStyle.Render("Error:"), secretName)
		return
	}

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
		fmt.Printf("%s Unknown provider '%s'. Available providers: %v\n",
			errorStyle.Render("Error:"), provider, getAvailableProviders())
		return
	}

	if len(secrets) == 0 {
		fmt.Printf("No secrets found for provider '%s'\n", provider)
		return
	}

	displaySecrets(secrets)
}

// Helper functions for the secrets implementation

// getCorePackageSecret retrieves a core package secret from the appropriate namespace
func getCorePackageSecret(ctx context.Context, clientset *kubernetes.Clientset, packageName, secretName string) (*corev1.Secret, error) {
	// Map package names to their namespaces
	namespaceMap := map[string]string{
		"argocd": "adhar-system",
		"gitea":  "adhar-system",
	}

	namespace, ok := namespaceMap[packageName]
	if !ok {
		namespace = "adhar-system" // default namespace
	}

	secret, err := clientset.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return nil, err
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

			// Handle Gitea-specific secrets
			if secret.Name == "gitea" {
				// For the main gitea secret, indicate it contains configuration
				secretInfo.Token = "Contains Gitea configuration scripts"
			}
		}
	} else {
		if secret.Data != nil {
			secretInfo.Data = make(map[string]string)
			for key, value := range secret.Data {
				secretInfo.Data[key] = string(value)
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

// displaySecrets displays a list of secrets with nice formatting
func displaySecrets(secrets []SecretInfo) {
	for _, secret := range secrets {
		fmt.Printf("\n%s %s\n", getBoldStyle.Render("Secret:"), secret.Name)
		fmt.Printf("  %s %s\n", getListItemStyle.Render("Namespace:"), secret.Namespace)

		if secret.IsCore {
			if secret.Username != "" {
				fmt.Printf("  %s %s\n", getListItemStyle.Render("Username:"), secret.Username)
			}
			if secret.Password != "" {
				fmt.Printf("  %s %s\n", getListItemStyle.Render("Password:"), secret.Password)
			}
			if secret.Token != "" {
				fmt.Printf("  %s %s\n", getListItemStyle.Render("Token:"), secret.Token)
			}

			// Add helpful notes for Gitea secrets
			if strings.HasPrefix(secret.Name, "gitea") {
				fmt.Printf("  %s %s\n", getListItemStyle.Render("Note:"), "Complete setup at https://adhar.localtest.me/gitea/")
				fmt.Printf("  %s %s\n", getListItemStyle.Render("Web Setup:"), "No pre-configured admin user - complete initial setup via web interface")
			}
		} else if secret.Data != nil {
			fmt.Printf("  %s\n", getListItemStyle.Render("Data:"))
			for key, value := range secret.Data {
				// Truncate long values for display
				displayValue := value
				if len(value) > 50 {
					displayValue = value[:47] + "..."
				}
				fmt.Printf("    %s: %s\n", key, displayValue)
			}
		}
	}
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
				fmt.Printf("%s Error getting secret '%s' for provider '%s': %v\n",
					errorStyle.Render("Warning:"), secretName, provider, err)
				continue
			}
			secrets = append(secrets, populateSecret(*secret, true))
		}
	}

	// Also get secrets with Adhar labels
	labeledSecrets, err := getSecretsByAdharLabel(ctx, clientset)
	if err != nil {
		fmt.Printf("%s Error getting labeled secrets: %v\n", errorStyle.Render("Warning:"), err)
	} else {
		for _, secret := range labeledSecrets.Items {
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
		fmt.Printf("No secrets found\n")
		return
	}

	fmt.Printf("Found %d secrets:\n", len(secrets))
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
