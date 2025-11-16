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

// statusCmd represents the status command
var statusCmd = &cobra.Command{
	Use:     "status",
	Aliases: []string{"st", "health"},
	Short:   "Get platform health and status",
	Long: `Get comprehensive health and status information about the Adhar platform.

This command provides:
• Overall platform health summary
• Core service status (ArgoCD, Gitea, Nginx)
• Pod health and resource usage
• Deployment status and replica information
• Service endpoint availability
• Recent events and alerts

Examples:
  adhar get status                     # Get platform status overview
  adhar get status --detailed          # Get detailed status information
  adhar get status --watch             # Watch status changes in real-time
  adhar get status --output json       # Output status in JSON format`,
	RunE: runGetStatus,
}

var (
	// Status-specific flags
	watchStatus    bool
	healthChecks   bool
	showEvents     bool
	serviceDetails bool
)

func init() {
	statusCmd.Flags().BoolVarP(&watchStatus, "watch", "w", false, "Watch status changes in real-time")
	statusCmd.Flags().BoolVar(&healthChecks, "health", true, "Include health checks")
	statusCmd.Flags().BoolVar(&showEvents, "events", false, "Show recent events")
	statusCmd.Flags().BoolVar(&serviceDetails, "service-details", false, "Show detailed service information")
}

type PlatformStatus struct {
	OverallStatus  string
	CoreServices   []ServiceStatus
	Nodes          NodeStatus
	Workloads      WorkloadStatus
	Resources      ResourceStatus
	NetworkStatus  NetworkStatus
	LastUpdated    time.Time
	PlatformUptime time.Duration
	HealthScore    int
	Warnings       []string
	CriticalIssues []string
}

type ServiceStatus struct {
	Name           string
	Icon           string
	Status         string
	StatusColor    string
	Replicas       string
	Version        string
	Endpoints      []string
	HealthEndpoint string
	LastChecked    time.Time
	ResponseTime   time.Duration
	Issues         []string
}

type NodeStatus struct {
	Total       int
	Ready       int
	NotReady    int
	CPUUsage    string
	MemoryUsage string
}

type WorkloadStatus struct {
	TotalPods    int
	RunningPods  int
	PendingPods  int
	FailedPods   int
	Deployments  int
	StatefulSets int
	DaemonSets   int
	Jobs         int
}

type ResourceStatus struct {
	NamespaceCount int
	ServiceCount   int
	IngressCount   int
	PVCount        int
	ConfigMapCount int
	SecretCount    int
}

type NetworkStatus struct {
	ServiceEndpoints   int
	IngressControllers int
	LoadBalancers      int
	ExternalIPs        int
	ClusterIPs         int
}

func runGetStatus(cmd *cobra.Command, args []string) error {
	logger.Info("📊 Retrieving platform status...")

	// Get Kubernetes client
	clientset, err := getKubernetesClient()
	if err != nil {
		return fmt.Errorf("failed to get Kubernetes client: %w", err)
	}

	// Collect platform status
	status, err := collectPlatformStatus(clientset)
	if err != nil {
		return fmt.Errorf("failed to collect platform status: %w", err)
	}

	// Display status based on output format
	switch outputFormat {
	case "json":
		return helpers.PrintJSON(status)
	case "yaml":
		return helpers.PrintYAML(status)
	default:
		return displayStatusTable(status)
	}
}

func collectPlatformStatus(clientset *kubernetes.Clientset) (*PlatformStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	status := &PlatformStatus{
		LastUpdated: time.Now(),
	}

	// Get nodes status
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get nodes: %w", err)
	}
	status.Nodes = collectNodeStatus(nodes.Items)

	// Get all pods
	pods, err := clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get pods: %w", err)
	}
	status.Workloads = collectWorkloadStatus(clientset, ctx, pods.Items)

	// Get resource counts
	status.Resources = collectResourceStatus(clientset, ctx)

	// Get network status
	status.NetworkStatus = collectNetworkStatus(clientset, ctx)

	// Get core services status
	status.CoreServices = collectCoreServicesStatus(clientset, ctx)

	// Calculate overall status and health score
	status.OverallStatus, status.HealthScore = calculateOverallStatus(status)

	// Calculate platform uptime (approximate)
	status.PlatformUptime = calculatePlatformUptime(pods.Items)

	return status, nil
}

func collectNodeStatus(nodes []corev1.Node) NodeStatus {
	nodeStatus := NodeStatus{
		Total: len(nodes),
	}

	for _, node := range nodes {
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady {
				if condition.Status == corev1.ConditionTrue {
					nodeStatus.Ready++
				} else {
					nodeStatus.NotReady++
				}
				break
			}
		}
	}

	// TODO: Add actual CPU/Memory usage calculation
	nodeStatus.CPUUsage = "N/A"
	nodeStatus.MemoryUsage = "N/A"

	return nodeStatus
}

func collectWorkloadStatus(clientset *kubernetes.Clientset, ctx context.Context, pods []corev1.Pod) WorkloadStatus {
	workloadStatus := WorkloadStatus{
		TotalPods: len(pods),
	}

	// Count pod phases
	for _, pod := range pods {
		switch pod.Status.Phase {
		case corev1.PodRunning:
			workloadStatus.RunningPods++
		case corev1.PodPending:
			workloadStatus.PendingPods++
		case corev1.PodFailed:
			workloadStatus.FailedPods++
		}
	}

	// Get workload counts
	deployments, _ := clientset.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
	workloadStatus.Deployments = len(deployments.Items)

	statefulSets, _ := clientset.AppsV1().StatefulSets("").List(ctx, metav1.ListOptions{})
	workloadStatus.StatefulSets = len(statefulSets.Items)

	daemonSets, _ := clientset.AppsV1().DaemonSets("").List(ctx, metav1.ListOptions{})
	workloadStatus.DaemonSets = len(daemonSets.Items)

	jobs, _ := clientset.BatchV1().Jobs("").List(ctx, metav1.ListOptions{})
	workloadStatus.Jobs = len(jobs.Items)

	return workloadStatus
}

func collectResourceStatus(clientset *kubernetes.Clientset, ctx context.Context) ResourceStatus {
	resourceStatus := ResourceStatus{}

	// Get resource counts
	namespaces, _ := clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	resourceStatus.NamespaceCount = len(namespaces.Items)

	services, _ := clientset.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	resourceStatus.ServiceCount = len(services.Items)

	configMaps, _ := clientset.CoreV1().ConfigMaps("").List(ctx, metav1.ListOptions{})
	resourceStatus.ConfigMapCount = len(configMaps.Items)

	secrets, _ := clientset.CoreV1().Secrets("").List(ctx, metav1.ListOptions{})
	resourceStatus.SecretCount = len(secrets.Items)

	pvs, _ := clientset.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	resourceStatus.PVCount = len(pvs.Items)

	return resourceStatus
}

func collectNetworkStatus(clientset *kubernetes.Clientset, ctx context.Context) NetworkStatus {
	networkStatus := NetworkStatus{}

	// Get services and count different types
	services, err := clientset.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err == nil {
		for _, svc := range services.Items {
			switch svc.Spec.Type {
			case corev1.ServiceTypeLoadBalancer:
				networkStatus.LoadBalancers++
			case corev1.ServiceTypeClusterIP:
				networkStatus.ClusterIPs++
			}

			// Count external IPs
			if len(svc.Spec.ExternalIPs) > 0 {
				networkStatus.ExternalIPs++
			}

			// Count endpoints
			networkStatus.ServiceEndpoints += len(svc.Spec.Ports)
		}
	}

	return networkStatus
}

func collectCoreServicesStatus(clientset *kubernetes.Clientset, ctx context.Context) []ServiceStatus {
	coreServices := []ServiceStatus{}

	// Define core services to check
	serviceConfigs := []struct {
		name      string
		icon      string
		namespace string
		selector  string
	}{
		{"ArgoCD", "🚀", "adhar-system", "app.kubernetes.io/name=argocd-server"},
		{"Gitea", "🦊", "adhar-system", "app=gitea"},
		{"Nginx Ingress", "🌐", "adhar-system", "app.kubernetes.io/name=ingress-nginx"},
		{"Cilium", "🕸️", "kube-system", "k8s-app=cilium"},
	}

	for _, config := range serviceConfigs {
		status := ServiceStatus{
			Name:        config.name,
			Icon:        config.icon,
			LastChecked: time.Now(),
		}

		// Get deployment status
		deployments, err := clientset.AppsV1().Deployments(config.namespace).List(ctx, metav1.ListOptions{
			LabelSelector: config.selector,
		})

		if err == nil && len(deployments.Items) > 0 {
			deployment := deployments.Items[0]
			status.Replicas = fmt.Sprintf("%d/%d", deployment.Status.ReadyReplicas, deployment.Status.Replicas)

			if deployment.Status.ReadyReplicas == deployment.Status.Replicas && deployment.Status.Replicas > 0 {
				status.Status = "✅ Healthy"
				status.StatusColor = "#10b981"
			} else {
				status.Status = "⚠️ Degraded"
				status.StatusColor = "#f59e0b"
			}

			// Get version from image
			if len(deployment.Spec.Template.Spec.Containers) > 0 {
				image := deployment.Spec.Template.Spec.Containers[0].Image
				if strings.Contains(image, ":") {
					parts := strings.Split(image, ":")
					if len(parts) > 1 {
						status.Version = parts[len(parts)-1]
					}
				}
			}
		} else {
			status.Status = "❌ Not Found"
			status.StatusColor = "#ef4444"
			status.Replicas = "0/0"
		}

		coreServices = append(coreServices, status)
	}

	return coreServices
}

func calculateOverallStatus(status *PlatformStatus) (string, int) {
	healthScore := 100
	overallStatus := "✅ Healthy"

	// Check node health
	if status.Nodes.NotReady > 0 {
		healthScore -= 20
		overallStatus = "⚠️ Degraded"
	}

	// Check core services
	for _, service := range status.CoreServices {
		if strings.Contains(service.Status, "❌") {
			healthScore -= 25
			overallStatus = "❌ Critical"
		} else if strings.Contains(service.Status, "⚠️") {
			healthScore -= 10
			if overallStatus == "✅ Healthy" {
				overallStatus = "⚠️ Degraded"
			}
		}
	}

	// Check workload health
	if status.Workloads.FailedPods > 0 {
		healthScore -= 5
		if overallStatus == "✅ Healthy" {
			overallStatus = "⚠️ Degraded"
		}
	}

	if healthScore < 0 {
		healthScore = 0
	}

	return overallStatus, healthScore
}

func calculatePlatformUptime(pods []corev1.Pod) time.Duration {
	var oldestPodTime time.Time

	for _, pod := range pods {
		if pod.Namespace == "adhar-system" {
			if oldestPodTime.IsZero() || pod.CreationTimestamp.Time.Before(oldestPodTime) {
				oldestPodTime = pod.CreationTimestamp.Time
			}
		}
	}

	if oldestPodTime.IsZero() {
		return 0
	}

	return time.Since(oldestPodTime)
}

func displayStatusTable(status *PlatformStatus) error {
	logger.Info("📊 Platform Status Overview")

	// Display overall status in a header box
	overallStatusContent := fmt.Sprintf(
		"🏥 Overall Status: %s\n"+
			"💯 Health Score: %d/100\n"+
			"⏱️  Platform Uptime: %s\n"+
			"🕐 Last Updated: %s",
		status.OverallStatus,
		status.HealthScore,
		formatDuration(status.PlatformUptime),
		status.LastUpdated.Format("15:04:05 MST"))

	overallBox := helpers.BorderStyle.Width(80).Render(overallStatusContent)
	fmt.Println(overallBox)

	// Display core services status
	fmt.Printf("\n%s\n", helpers.TitleStyle.Render("🔧 Core Services"))

	var servicesTable strings.Builder
	servicesTable.WriteString(fmt.Sprintf("%-25s %-15s %-20s %-15s\n",
		"🏷️  SERVICE", "📊 STATUS", "🔄 REPLICAS", "📦 VERSION"))
	servicesTable.WriteString(strings.Repeat("─", 75) + "\n")

	for _, service := range status.CoreServices {
		serviceName := service.Icon + " " + service.Name
		version := service.Version
		if version == "" {
			version = "unknown"
		}

		row := fmt.Sprintf("%-25s %-15s %-20s %-15s\n",
			serviceName,
			service.Status,
			service.Replicas,
			version)
		servicesTable.WriteString(row)
	}

	servicesBox := helpers.BorderStyle.Width(80).Render(servicesTable.String())
	fmt.Println(servicesBox)

	// Display cluster resources
	fmt.Printf("\n%s\n", helpers.TitleStyle.Render("📊 Cluster Resources"))

	resourcesContent := fmt.Sprintf(
		"🖥️  Nodes: %d ready, %d total\n"+
			"🏗️  Workloads: %d deployments, %d pods (%d running)\n"+
			"📦 Resources: %d namespaces, %d services, %d secrets\n"+
			"💾 Storage: %d persistent volumes\n"+
			"🌐 Network: %d service endpoints, %d load balancers",
		status.Nodes.Ready, status.Nodes.Total,
		status.Workloads.Deployments, status.Workloads.TotalPods, status.Workloads.RunningPods,
		status.Resources.NamespaceCount, status.Resources.ServiceCount, status.Resources.SecretCount,
		status.Resources.PVCount,
		status.NetworkStatus.ServiceEndpoints, status.NetworkStatus.LoadBalancers)

	resourcesBox := helpers.BorderStyle.Width(80).Render(resourcesContent)
	fmt.Println(resourcesBox)

	// Display any warnings or issues
	if len(status.Warnings) > 0 || len(status.CriticalIssues) > 0 {
		fmt.Printf("\n%s\n", helpers.WarningStyle.Render("⚠️  Issues & Warnings"))

		var issuesContent strings.Builder

		if len(status.CriticalIssues) > 0 {
			issuesContent.WriteString("🚨 Critical Issues:\n")
			for _, issue := range status.CriticalIssues {
				issuesContent.WriteString(fmt.Sprintf("  • %s\n", issue))
			}
			issuesContent.WriteString("\n")
		}

		if len(status.Warnings) > 0 {
			issuesContent.WriteString("⚠️  Warnings:\n")
			for _, warning := range status.Warnings {
				issuesContent.WriteString(fmt.Sprintf("  • %s\n", warning))
			}
		}

		if issuesContent.Len() > 0 {
			issuesBox := helpers.BorderStyle.Width(80).Render(issuesContent.String())
			fmt.Println(issuesBox)
		}
	}

	return nil
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	} else if d < time.Hour {
		return fmt.Sprintf("%.0fm", d.Minutes())
	} else if d < 24*time.Hour {
		return fmt.Sprintf("%.1fh", d.Hours())
	} else {
		return fmt.Sprintf("%.1fd", d.Hours()/24)
	}
}
