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
	getCmd.AddCommand(getStatusCmd) // Add the status command

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
