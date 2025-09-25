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
	"sort"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/client-go/kubernetes"
)

// applicationsCmd represents the applications command
var applicationsCmd = &cobra.Command{
	Use:     "applications [application-name]",
	Aliases: []string{"apps", "app", "deployments", "deploy"},
	Short:   "Get application information and status",
	Long: `Get detailed information about applications running on the Adhar platform.

This command provides:
• Application deployment status and health
• Pod status and resource usage
• Service endpoints and ingress routes
• Application configuration and environment
• Scaling information and replica counts
• Application logs and events

Examples:
  adhar get applications                    # List all applications
  adhar get applications my-app             # Get specific application info
  adhar get applications --all-namespaces  # List apps across all namespaces
  adhar get applications --status           # Include detailed status info
  adhar get applications --output json      # Output in JSON format`,
	RunE: runGetApplications,
}

var (
	// Applications-specific flags
	showStatus    bool
	showResources bool
	showEndpoints bool
	appNamespace  string
	labelSelector string
	fieldSelector string
)

func init() {
	applicationsCmd.Flags().BoolVar(&showStatus, "status", false, "Show detailed application status")
	applicationsCmd.Flags().BoolVar(&showResources, "resources", false, "Show resource usage and limits")
	applicationsCmd.Flags().BoolVar(&showEndpoints, "endpoints", false, "Show service endpoints and ingress")
	applicationsCmd.Flags().StringVarP(&appNamespace, "namespace", "n", "", "Namespace to query (overrides global namespace)")
	applicationsCmd.Flags().StringVarP(&labelSelector, "selector", "l", "", "Label selector to filter applications")
	applicationsCmd.Flags().StringVar(&fieldSelector, "field-selector", "", "Field selector to filter applications")
}

type ApplicationInfo struct {
	Name          string            `json:"name"`
	Namespace     string            `json:"namespace"`
	Type          string            `json:"type"`
	Status        string            `json:"status"`
	StatusColor   string            `json:"status_color"`
	Replicas      ReplicaInfo       `json:"replicas"`
	Age           string            `json:"age"`
	Images        []string          `json:"images"`
	Services      []ServiceInfo     `json:"services,omitempty"`
	Ingresses     []IngressInfo     `json:"ingresses,omitempty"`
	Endpoints     []string          `json:"endpoints,omitempty"`
	ResourceUsage ResourceUsage     `json:"resource_usage,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	Annotations   map[string]string `json:"annotations,omitempty"`
	Conditions    []ConditionInfo   `json:"conditions,omitempty"`
	Events        []EventInfo       `json:"events,omitempty"`
	CreationTime  time.Time         `json:"creation_time"`
	LastUpdated   time.Time         `json:"last_updated"`
}

type ReplicaInfo struct {
	Ready       int32 `json:"ready"`
	Total       int32 `json:"total"`
	Available   int32 `json:"available"`
	Unavailable int32 `json:"unavailable"`
}

type ServiceInfo struct {
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	ClusterIP string   `json:"cluster_ip"`
	Ports     []string `json:"ports"`
}

type IngressInfo struct {
	Name  string   `json:"name"`
	Hosts []string `json:"hosts"`
	Paths []string `json:"paths"`
}

type ResourceUsage struct {
	CPURequests    string `json:"cpu_requests"`
	CPULimits      string `json:"cpu_limits"`
	MemoryRequests string `json:"memory_requests"`
	MemoryLimits   string `json:"memory_limits"`
}

type ConditionInfo struct {
	Type    string    `json:"type"`
	Status  string    `json:"status"`
	Reason  string    `json:"reason,omitempty"`
	Message string    `json:"message,omitempty"`
	Since   time.Time `json:"since"`
}

type EventInfo struct {
	Type      string    `json:"type"`
	Reason    string    `json:"reason"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
}

func runGetApplications(cmd *cobra.Command, args []string) error {
	logger.Info("🚀 Retrieving application information...")

	// Get Kubernetes client
	clientset, err := getKubernetesClient()
	if err != nil {
		return fmt.Errorf("failed to get Kubernetes client: %w", err)
	}

	// Determine namespace
	queryNamespace := appNamespace
	if queryNamespace == "" {
		queryNamespace = namespace
	}
	if allNamespaces {
		queryNamespace = ""
	}

	// Get applications
	applications, err := getApplications(clientset, queryNamespace, args)
	if err != nil {
		return fmt.Errorf("failed to get applications: %w", err)
	}

	if len(applications) == 0 {
		logger.Info("No applications found")
		return nil
	}

	// Display applications based on output format
	switch outputFormat {
	case "json":
		return helpers.PrintJSON(applications)
	case "yaml":
		return helpers.PrintYAML(applications)
	default:
		return displayApplicationsTable(applications)
	}
}

func getApplications(clientset *kubernetes.Clientset, namespace string, appNames []string) ([]ApplicationInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var applications []ApplicationInfo

	// Build list options
	listOptions := metav1.ListOptions{}
	if labelSelector != "" {
		listOptions.LabelSelector = labelSelector
	}
	if fieldSelector != "" {
		listOptions.FieldSelector = fieldSelector
	}

	// Get deployments
	deployments, err := clientset.AppsV1().Deployments(namespace).List(ctx, listOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to list deployments: %w", err)
	}

	for _, deployment := range deployments.Items {
		// Filter by specific app name if provided
		if len(appNames) > 0 && !contains(appNames, deployment.Name) {
			continue
		}

		app := ApplicationInfo{
			Name:         deployment.Name,
			Namespace:    deployment.Namespace,
			Type:         "Deployment",
			CreationTime: deployment.CreationTimestamp.Time,
			LastUpdated:  time.Now(),
			Labels:       deployment.Labels,
			Annotations:  deployment.Annotations,
		}

		// Get replica information
		app.Replicas = ReplicaInfo{
			Ready:       deployment.Status.ReadyReplicas,
			Total:       deployment.Status.Replicas,
			Available:   deployment.Status.AvailableReplicas,
			Unavailable: deployment.Status.UnavailableReplicas,
		}

		// Calculate age
		app.Age = duration.HumanDuration(time.Since(deployment.CreationTimestamp.Time))

		// Determine status
		app.Status, app.StatusColor = getDeploymentStatus(deployment)

		// Get images
		app.Images = getDeploymentImages(deployment)

		// Get conditions
		for _, condition := range deployment.Status.Conditions {
			app.Conditions = append(app.Conditions, ConditionInfo{
				Type:    string(condition.Type),
				Status:  string(condition.Status),
				Reason:  condition.Reason,
				Message: condition.Message,
				Since:   condition.LastTransitionTime.Time,
			})
		}

		// Get associated services if requested
		if showEndpoints {
			services, err := getDeploymentServices(clientset, ctx, deployment)
			if err == nil {
				app.Services = services
			}

			// Get ingresses
			ingresses, err := getDeploymentIngresses(clientset, ctx, deployment)
			if err == nil {
				app.Ingresses = ingresses
			}
		}

		// Get resource usage if requested
		if showResources {
			app.ResourceUsage = getDeploymentResourceUsage(deployment)
		}

		applications = append(applications, app)
	}

	// Get StatefulSets
	statefulSets, err := clientset.AppsV1().StatefulSets(namespace).List(ctx, listOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to list statefulsets: %w", err)
	}

	for _, sts := range statefulSets.Items {
		// Filter by specific app name if provided
		if len(appNames) > 0 && !contains(appNames, sts.Name) {
			continue
		}

		app := ApplicationInfo{
			Name:         sts.Name,
			Namespace:    sts.Namespace,
			Type:         "StatefulSet",
			CreationTime: sts.CreationTimestamp.Time,
			LastUpdated:  time.Now(),
			Labels:       sts.Labels,
			Annotations:  sts.Annotations,
		}

		// Get replica information
		app.Replicas = ReplicaInfo{
			Ready: sts.Status.ReadyReplicas,
			Total: sts.Status.Replicas,
		}

		// Calculate age
		app.Age = duration.HumanDuration(time.Since(sts.CreationTimestamp.Time))

		// Determine status
		app.Status, app.StatusColor = getStatefulSetStatus(sts)

		// Get images
		app.Images = getStatefulSetImages(sts)

		applications = append(applications, app)
	}

	// Get DaemonSets
	daemonSets, err := clientset.AppsV1().DaemonSets(namespace).List(ctx, listOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to list daemonsets: %w", err)
	}

	for _, ds := range daemonSets.Items {
		// Filter by specific app name if provided
		if len(appNames) > 0 && !contains(appNames, ds.Name) {
			continue
		}

		app := ApplicationInfo{
			Name:         ds.Name,
			Namespace:    ds.Namespace,
			Type:         "DaemonSet",
			CreationTime: ds.CreationTimestamp.Time,
			LastUpdated:  time.Now(),
			Labels:       ds.Labels,
			Annotations:  ds.Annotations,
		}

		// Get replica information
		app.Replicas = ReplicaInfo{
			Ready: ds.Status.NumberReady,
			Total: ds.Status.DesiredNumberScheduled,
		}

		// Calculate age
		app.Age = duration.HumanDuration(time.Since(ds.CreationTimestamp.Time))

		// Determine status
		app.Status, app.StatusColor = getDaemonSetStatus(ds)

		// Get images
		app.Images = getDaemonSetImages(ds)

		applications = append(applications, app)
	}

	// Sort applications by namespace and name
	sort.Slice(applications, func(i, j int) bool {
		if applications[i].Namespace != applications[j].Namespace {
			return applications[i].Namespace < applications[j].Namespace
		}
		return applications[i].Name < applications[j].Name
	})

	return applications, nil
}

func getDeploymentStatus(deployment appsv1.Deployment) (string, string) {
	if deployment.Status.ReadyReplicas == deployment.Status.Replicas && deployment.Status.Replicas > 0 {
		return "✅ Ready", "#10b981"
	} else if deployment.Status.ReadyReplicas > 0 {
		return "⚠️ Degraded", "#f59e0b"
	} else {
		return "❌ Not Ready", "#ef4444"
	}
}

func getStatefulSetStatus(sts appsv1.StatefulSet) (string, string) {
	if sts.Status.ReadyReplicas == sts.Status.Replicas && sts.Status.Replicas > 0 {
		return "✅ Ready", "#10b981"
	} else if sts.Status.ReadyReplicas > 0 {
		return "⚠️ Degraded", "#f59e0b"
	} else {
		return "❌ Not Ready", "#ef4444"
	}
}

func getDaemonSetStatus(ds appsv1.DaemonSet) (string, string) {
	if ds.Status.NumberReady == ds.Status.DesiredNumberScheduled && ds.Status.DesiredNumberScheduled > 0 {
		return "✅ Ready", "#10b981"
	} else if ds.Status.NumberReady > 0 {
		return "⚠️ Degraded", "#f59e0b"
	} else {
		return "❌ Not Ready", "#ef4444"
	}
}

func getDeploymentImages(deployment appsv1.Deployment) []string {
	var images []string
	for _, container := range deployment.Spec.Template.Spec.Containers {
		images = append(images, container.Image)
	}
	return images
}

func getStatefulSetImages(sts appsv1.StatefulSet) []string {
	var images []string
	for _, container := range sts.Spec.Template.Spec.Containers {
		images = append(images, container.Image)
	}
	return images
}

func getDaemonSetImages(ds appsv1.DaemonSet) []string {
	var images []string
	for _, container := range ds.Spec.Template.Spec.Containers {
		images = append(images, container.Image)
	}
	return images
}

func getDeploymentServices(clientset *kubernetes.Clientset, ctx context.Context, deployment appsv1.Deployment) ([]ServiceInfo, error) {
	var services []ServiceInfo

	// Get services in the same namespace
	svcList, err := clientset.CoreV1().Services(deployment.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	// Find services that match the deployment's labels
	for _, svc := range svcList.Items {
		if matchesSelector(svc.Spec.Selector, deployment.Spec.Template.Labels) {
			var ports []string
			for _, port := range svc.Spec.Ports {
				ports = append(ports, fmt.Sprintf("%d:%d/%s", port.Port, port.TargetPort.IntVal, port.Protocol))
			}

			services = append(services, ServiceInfo{
				Name:      svc.Name,
				Type:      string(svc.Spec.Type),
				ClusterIP: svc.Spec.ClusterIP,
				Ports:     ports,
			})
		}
	}

	return services, nil
}

func getDeploymentIngresses(clientset *kubernetes.Clientset, ctx context.Context, deployment appsv1.Deployment) ([]IngressInfo, error) {
	var ingresses []IngressInfo

	// Get ingresses in the same namespace
	ingressList, err := clientset.NetworkingV1().Ingresses(deployment.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	// Find ingresses that might be related (this is a heuristic)
	for _, ingress := range ingressList.Items {
		var hosts []string
		var paths []string

		for _, rule := range ingress.Spec.Rules {
			hosts = append(hosts, rule.Host)
			if rule.HTTP != nil {
				for _, path := range rule.HTTP.Paths {
					paths = append(paths, path.Path)
				}
			}
		}

		// Only include if it has hosts or paths
		if len(hosts) > 0 || len(paths) > 0 {
			ingresses = append(ingresses, IngressInfo{
				Name:  ingress.Name,
				Hosts: hosts,
				Paths: paths,
			})
		}
	}

	return ingresses, nil
}

func getDeploymentResourceUsage(deployment appsv1.Deployment) ResourceUsage {
	usage := ResourceUsage{}

	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Resources.Requests != nil {
			if cpu, ok := container.Resources.Requests[corev1.ResourceCPU]; ok {
				usage.CPURequests = cpu.String()
			}
			if memory, ok := container.Resources.Requests[corev1.ResourceMemory]; ok {
				usage.MemoryRequests = memory.String()
			}
		}

		if container.Resources.Limits != nil {
			if cpu, ok := container.Resources.Limits[corev1.ResourceCPU]; ok {
				usage.CPULimits = cpu.String()
			}
			if memory, ok := container.Resources.Limits[corev1.ResourceMemory]; ok {
				usage.MemoryLimits = memory.String()
			}
		}
	}

	return usage
}

func displayApplicationsTable(applications []ApplicationInfo) error {
	logger.Info(fmt.Sprintf("📋 Found %d applications", len(applications)))

	// Create table header
	var table strings.Builder
	table.WriteString(fmt.Sprintf("%-30s %-15s %-12s %-15s %-12s %-8s\n",
		"🏷️  NAME", "📁 NAMESPACE", "📦 TYPE", "📊 STATUS", "🔄 REPLICAS", "📅 AGE"))
	table.WriteString(strings.Repeat("─", 90) + "\n")

	// Group by namespace for better readability
	namespaceGroups := make(map[string][]ApplicationInfo)
	for _, app := range applications {
		namespaceGroups[app.Namespace] = append(namespaceGroups[app.Namespace], app)
	}

	// Display applications grouped by namespace
	for namespace, apps := range namespaceGroups {
		if len(namespaceGroups) > 1 {
			table.WriteString(fmt.Sprintf("\n%s:\n", helpers.TitleStyle.Render("📁 "+namespace)))
		}

		for _, app := range apps {
			replicas := fmt.Sprintf("%d/%d", app.Replicas.Ready, app.Replicas.Total)

			row := fmt.Sprintf("%-30s %-15s %-12s %-15s %-12s %-8s\n",
				truncateString(app.Name, 28),
				truncateString(app.Namespace, 13),
				app.Type,
				app.Status,
				replicas,
				app.Age)
			table.WriteString(row)

			// Show additional details if requested
			if showStatus && len(app.Conditions) > 0 {
				for _, condition := range app.Conditions {
					if condition.Type == "Available" || condition.Type == "Progressing" {
						conditionLine := fmt.Sprintf("  └─ %s: %s", condition.Type, condition.Status)
						if condition.Message != "" {
							conditionLine += fmt.Sprintf(" (%s)", truncateString(condition.Message, 40))
						}
						table.WriteString(conditionLine + "\n")
					}
				}
			}

			if showEndpoints && len(app.Services) > 0 {
				for _, svc := range app.Services {
					serviceLine := fmt.Sprintf("  🌐 Service: %s (%s) - %s",
						svc.Name, svc.Type, strings.Join(svc.Ports, ", "))
					table.WriteString(serviceLine + "\n")
				}
			}

			if showResources && (app.ResourceUsage.CPURequests != "" || app.ResourceUsage.MemoryRequests != "") {
				resourceLine := fmt.Sprintf("  💾 Resources: CPU: %s/%s, Memory: %s/%s",
					app.ResourceUsage.CPURequests, app.ResourceUsage.CPULimits,
					app.ResourceUsage.MemoryRequests, app.ResourceUsage.MemoryLimits)
				table.WriteString(resourceLine + "\n")
			}
		}
	}

	// Display the table in a bordered box
	tableBox := helpers.BorderStyle.Width(95).Render(table.String())
	fmt.Println(tableBox)

	return nil
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func matchesSelector(selector map[string]string, labels map[string]string) bool {
	if len(selector) == 0 {
		return false
	}

	for key, value := range selector {
		if labels[key] != value {
			return false
		}
	}
	return true
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
