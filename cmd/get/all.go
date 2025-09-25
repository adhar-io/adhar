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

package get

import (
	"context"
	"fmt"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// allCmd represents the all command
var allCmd = &cobra.Command{
	Use:     "all",
	Aliases: []string{"everything", "overview"},
	Short:   "Get comprehensive overview of all Adhar platform resources",
	Long: `Get a comprehensive overview of all Adhar platform resources and their status.

This command provides:
• Platform health and status summary
• All applications and their health
• Environment configurations
• Database instances and status
• Managed tools and services
• Network routes and ingress
• Cluster information
• Resource usage summary

Examples:
  adhar get all                     # Get complete platform overview
  adhar get all --detailed          # Get detailed information for all resources
  adhar get all --output json       # Output comprehensive data in JSON format`,
	RunE: runGetAll,
}

var (
	// All-specific flags
	allDetailed bool
	summary     bool
)

func init() {
	allCmd.Flags().BoolVar(&allDetailed, "detailed", false, "Show detailed information for all resources")
	allCmd.Flags().BoolVar(&summary, "summary", true, "Show summary overview")
}

func runGetAll(cmd *cobra.Command, args []string) error {
	logger.Info("🔍 Retrieving comprehensive platform overview...")

	// Get Kubernetes client
	clientset, err := getKubernetesClient()
	if err != nil {
		return fmt.Errorf("failed to get Kubernetes client: %w", err)
	}

	// Collect comprehensive platform data
	overview, err := getComprehensiveOverview(clientset)
	if err != nil {
		return fmt.Errorf("failed to collect platform overview: %w", err)
	}

	// Display overview based on output format
	switch outputFormat {
	case "json":
		return helpers.PrintJSON(overview)
	case "yaml":
		return helpers.PrintYAML(overview)
	default:
		return displayComprehensiveOverview(overview)
	}
}

type ComprehensiveOverview struct {
	Platform     PlatformOverview     `json:"platform"`
	Cluster      ClusterOverview      `json:"cluster"`
	Applications []ApplicationSummary `json:"applications"`
	Environments []EnvironmentSummary `json:"environments"`
	Services     []ServiceOverview    `json:"services"`
	Resources    ResourceOverview     `json:"resources"`
	LastUpdated  string               `json:"last_updated"`
}

type PlatformOverview struct {
	Status      string `json:"status"`
	Version     string `json:"version"`
	HealthScore int    `json:"health_score"`
	Uptime      string `json:"uptime"`
}

type ClusterOverview struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	Provider   string `json:"provider"`
	Nodes      int    `json:"nodes"`
	NodesReady int    `json:"nodes_ready"`
	Namespaces int    `json:"namespaces"`
}

type ApplicationSummary struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	Replicas  string `json:"replicas"`
}

type EnvironmentSummary struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	Applications int    `json:"applications"`
	Services     int    `json:"services"`
}

type ServiceOverview struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	Endpoints int    `json:"endpoints"`
}

type ResourceOverview struct {
	TotalPods        int `json:"total_pods"`
	RunningPods      int `json:"running_pods"`
	TotalServices    int `json:"total_services"`
	TotalSecrets     int `json:"total_secrets"`
	TotalConfigMaps  int `json:"total_config_maps"`
	TotalDeployments int `json:"total_deployments"`
}

func getComprehensiveOverview(clientset *kubernetes.Clientset) (*ComprehensiveOverview, error) {
	overview := &ComprehensiveOverview{
		LastUpdated: helpers.FormatAge(metav1.Time{Time: time.Now()}),
	}

	// Get platform status
	platformStatus, err := collectPlatformStatus(clientset)
	if err == nil {
		overview.Platform = PlatformOverview{
			Status:      platformStatus.OverallStatus,
			Version:     "v0.3.8", // TODO: Get from version
			HealthScore: platformStatus.HealthScore,
			Uptime:      formatDuration(platformStatus.PlatformUptime),
		}
	}

	// Get cluster overview
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err == nil {
		readyNodes := 0
		for _, node := range nodes.Items {
			for _, condition := range node.Status.Conditions {
				if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
					readyNodes++
					break
				}
			}
		}

		version, _ := clientset.Discovery().ServerVersion()
		currentContext := getCurrentContext()

		overview.Cluster = ClusterOverview{
			Name:       currentContext,
			Version:    version.String(),
			Provider:   getProviderFromContext(currentContext),
			Nodes:      len(nodes.Items),
			NodesReady: readyNodes,
		}
	}

	// Get applications summary
	applications, err := getApplications(clientset, "", nil)
	if err == nil {
		for _, app := range applications {
			overview.Applications = append(overview.Applications, ApplicationSummary{
				Name:      app.Name,
				Namespace: app.Namespace,
				Type:      app.Type,
				Status:    app.Status,
				Replicas:  fmt.Sprintf("%d/%d", app.Replicas.Ready, app.Replicas.Total),
			})
		}
	}

	// Get environments summary
	environments, err := getEnvironments(clientset, nil)
	if err == nil {
		for _, env := range environments {
			overview.Environments = append(overview.Environments, EnvironmentSummary{
				Name:         env.Name,
				Status:       env.Status,
				Applications: env.Workloads.Deployments + env.Workloads.StatefulSets,
				Services:     env.ResourceUsage.Services,
			})
		}
	}

	// Get resource overview
	pods, _ := clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	services, _ := clientset.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	secrets, _ := clientset.CoreV1().Secrets("").List(ctx, metav1.ListOptions{})
	configMaps, _ := clientset.CoreV1().ConfigMaps("").List(ctx, metav1.ListOptions{})
	deployments, _ := clientset.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})

	runningPods := 0
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning {
			runningPods++
		}
	}

	overview.Resources = ResourceOverview{
		TotalPods:        len(pods.Items),
		RunningPods:      runningPods,
		TotalServices:    len(services.Items),
		TotalSecrets:     len(secrets.Items),
		TotalConfigMaps:  len(configMaps.Items),
		TotalDeployments: len(deployments.Items),
	}

	return overview, nil
}

func getProviderFromContext(context string) string {
	if strings.Contains(context, "kind-") {
		return "Kind (Local)"
	} else if strings.Contains(strings.ToLower(context), "gke") {
		return "Google GKE"
	} else if strings.Contains(strings.ToLower(context), "eks") {
		return "Amazon EKS"
	} else if strings.Contains(strings.ToLower(context), "aks") {
		return "Azure AKS"
	}
	return "Unknown"
}

func displayComprehensiveOverview(overview *ComprehensiveOverview) error {
	logger.Info("🔍 Adhar Platform Comprehensive Overview")

	// Platform Summary
	platformContent := fmt.Sprintf(
		"🏥 Platform Status: %s\n"+
			"💯 Health Score: %d/100\n"+
			"📦 Version: %s\n"+
			"⏱️  Uptime: %s",
		overview.Platform.Status,
		overview.Platform.HealthScore,
		overview.Platform.Version,
		overview.Platform.Uptime)

	platformBox := helpers.BorderStyle.Width(80).Render(platformContent)
	fmt.Println(platformBox)

	// Cluster Summary
	fmt.Printf("\n%s\n", helpers.TitleStyle.Render("🏗️  Cluster Overview"))

	clusterContent := fmt.Sprintf(
		"🌐 Context: %s\n"+
			"☁️  Provider: %s\n"+
			"⚡ Version: %s\n"+
			"🖥️  Nodes: %d ready / %d total\n"+
			"📁 Namespaces: %d",
		overview.Cluster.Name,
		overview.Cluster.Provider,
		overview.Cluster.Version,
		overview.Cluster.NodesReady,
		overview.Cluster.Nodes,
		overview.Cluster.Namespaces)

	clusterBox := helpers.BorderStyle.Width(80).Render(clusterContent)
	fmt.Println(clusterBox)

	// Resources Summary
	fmt.Printf("\n%s\n", helpers.TitleStyle.Render("📊 Resource Summary"))

	var resourcesTable strings.Builder
	resourcesTable.WriteString(fmt.Sprintf("%-20s %-15s %-15s\n",
		"🏷️  RESOURCE TYPE", "📊 COUNT", "🔄 STATUS"))
	resourcesTable.WriteString(strings.Repeat("─", 50) + "\n")

	resourceData := []struct {
		name   string
		count  int
		status string
	}{
		{"🚀 Applications", len(overview.Applications), "Running"},
		{"🌍 Environments", len(overview.Environments), "Active"},
		{"🏃 Pods", overview.Resources.TotalPods, fmt.Sprintf("%d running", overview.Resources.RunningPods)},
		{"🌐 Services", overview.Resources.TotalServices, "Available"},
		{"🔐 Secrets", overview.Resources.TotalSecrets, "Managed"},
		{"⚙️ ConfigMaps", overview.Resources.TotalConfigMaps, "Available"},
	}

	for _, resource := range resourceData {
		row := fmt.Sprintf("%-20s %-15d %-15s\n",
			resource.name, resource.count, resource.status)
		resourcesTable.WriteString(row)
	}

	resourcesBox := helpers.BorderStyle.Width(55).Render(resourcesTable.String())
	fmt.Println(resourcesBox)

	// Applications Summary (if any)
	if len(overview.Applications) > 0 {
		fmt.Printf("\n%s\n", helpers.TitleStyle.Render("🚀 Applications Summary"))

		var appsTable strings.Builder
		appsTable.WriteString(fmt.Sprintf("%-25s %-15s %-12s %-15s\n",
			"🏷️  NAME", "📁 NAMESPACE", "📦 TYPE", "📊 STATUS"))
		appsTable.WriteString(strings.Repeat("─", 65) + "\n")

		for _, app := range overview.Applications[:min(5, len(overview.Applications))] {
			row := fmt.Sprintf("%-25s %-15s %-12s %-15s\n",
				truncateString(app.Name, 23),
				truncateString(app.Namespace, 13),
				app.Type,
				app.Status)
			appsTable.WriteString(row)
		}

		if len(overview.Applications) > 5 {
			appsTable.WriteString(fmt.Sprintf("... and %d more applications\n", len(overview.Applications)-5))
		}

		appsBox := helpers.BorderStyle.Width(70).Render(appsTable.String())
		fmt.Println(appsBox)
	}

	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
