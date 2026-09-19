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

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// clusterCmd represents the cluster command
var clusterCmd = &cobra.Command{
	Use:     "cluster [cluster-name]",
	Aliases: []string{"clusters", "cl"},
	Short:   "Get cluster information and status",
	Long: `Get detailed information about Kubernetes clusters.
	
This command provides:
• Current cluster context and configuration
• Cluster version and node information
• Node status and resource details
• Provider-specific information
• Cluster health and metrics

Examples:
  adhar get cluster                    # Get current cluster information
  adhar get cluster my-cluster         # Get specific cluster information
  adhar get cluster --detailed         # Get detailed cluster information
  adhar get cluster --output json     # Output in JSON format`,
	RunE: runGetCluster,
}

var (
	// Cluster-specific flags
	detailedOutput bool
	clusterName    string
)

func init() {
	clusterCmd.Flags().BoolVarP(&detailedOutput, "detailed", "d", false, "Show detailed cluster information")
	clusterCmd.Flags().StringVarP(&clusterName, "name", "c", "", "Specific cluster name to query")
}

func runGetCluster(cmd *cobra.Command, args []string) error {
	logger.Info("🏗️ Retrieving cluster information...")

	// Get Kubernetes client
	clientset, err := getKubernetesClient()
	if err != nil {
		return fmt.Errorf("failed to get Kubernetes client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Get cluster version
	version, err := clientset.Discovery().ServerVersion()
	if err != nil {
		return fmt.Errorf("failed to get cluster version: %w", err)
	}

	// Get current context
	currentContext := getCurrentContext()

	// Get nodes
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to get nodes: %w", err)
	}

	// Get namespaces for resource count
	namespaces, err := clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to get namespaces: %w", err)
	}

	// Get pods for resource count
	pods, err := clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to get pods: %w", err)
	}

	// Display cluster overview in a bordered box
	clusterOverview := fmt.Sprintf(
		helpers.IconCluster+" Current Context: %s\n"+
			helpers.IconBullet+" Kubernetes Version: %s\n"+
			helpers.IconCluster+" "+"Total Nodes: %d\n"+
			helpers.IconApp+" "+"Total Pods: %d\n"+
			helpers.IconNamespace+" "+"Total Namespaces: %d",
		lipgloss.NewStyle().Foreground(lipgloss.Color("#10b981")).Render(currentContext),
		version.String(),
		len(nodes.Items),
		len(pods.Items),
		len(namespaces.Items))

	overviewBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#8b5cf6")).
		Padding(1, 2).
		Margin(1, 0).
		Width(80).
		Render(clusterOverview)

	fmt.Println(overviewBox)

	// Display node information in a bordered table
	if len(nodes.Items) > 0 {
		fmt.Println()
		fmt.Println(helpers.SectionHeading(helpers.IconCluster, "Cluster Nodes"))

		// Create nodes table with borders
		nodesTable := createNodesTable(nodes.Items)
		nodesBox := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#06b6d4")).
			Padding(1, 2).
			Margin(1, 0).
			Width(90).
			Render(nodesTable)

		fmt.Println(nodesBox)
	}

	// Display provider information in a bordered box
	providerInfo := getProviderInfo(currentContext)
	providerBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#10b981")).
		Padding(1, 2).
		Margin(1, 0).
		Width(80).
		Render(providerInfo)

	fmt.Println(providerBox)

	// Show detailed information if requested
	if detailedOutput {
		fmt.Println()
		fmt.Println(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#8b5cf6")).Render(helpers.IconApp + " " + "Detailed Cluster Information"))

		detailedInfo := getDetailedClusterInfo(clientset, ctx)
		detailedBox := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#f59e0b")).
			Padding(1, 2).
			Margin(1, 0).
			Width(80).
			Render(detailedInfo)

		fmt.Println(detailedBox)
	}

	return nil
}

// getCurrentContext gets the current Kubernetes context
func getCurrentContext() string {
	// Load kubeconfig
	kubeconfig := clientcmd.NewDefaultClientConfigLoadingRules().GetDefaultFilename()
	if config, err := clientcmd.LoadFromFile(kubeconfig); err == nil {
		return config.CurrentContext
	}
	return "unknown"
}

// createNodesTable renders the cluster's nodes through the shared aligned
// table. It used to separate cells with tabs, which snap to 8-column stops and
// therefore put the header, the rule and the data on three different offsets as
// soon as a cell carried an icon or a colour escape.
func createNodesTable(nodes []corev1.Node) string {
	t := helpers.NewTable("NAME", "STATUS", "ROLES", "AGE", "VERSION")
	for _, node := range nodes {
		t.Row(
			lipgloss.NewStyle().Bold(true).Render(node.Name),
			nodeStatus(node),
			nodeRoles(node),
			nodeAge(node),
			node.Status.NodeInfo.KubeletVersion,
		)
	}
	return t.Render()
}

// nodeStatus is the node's Ready condition as one status cell.
func nodeStatus(node corev1.Node) string {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			if condition.Status == corev1.ConditionTrue {
				return helpers.StateReady("Ready")
			}
			return helpers.StateFailed("NotReady")
		}
	}
	return helpers.StateUnknown("Unknown")
}

// nodeRoles lists the node's roles with the platform's role icons, naming the
// role as well as showing its glyph — a bare crown told the reader nothing.
func nodeRoles(node corev1.Node) string {
	var roles []string
	for label := range node.Labels {
		if !strings.HasPrefix(label, "node-role.kubernetes.io/") {
			continue
		}
		role := strings.TrimPrefix(label, "node-role.kubernetes.io/")
		if role == "" {
			role = "worker"
		}
		roles = append(roles, role)
	}
	if len(roles) == 0 {
		roles = []string{"worker"}
	}
	sort.Strings(roles)
	out := make([]string, 0, len(roles))
	for _, role := range roles {
		switch role {
		case "control-plane", "master":
			out = append(out, helpers.IconControlPlane+" "+role)
		case "worker":
			out = append(out, helpers.IconWorker+" "+role)
		default:
			out = append(out, helpers.IconNodeOther+" "+role)
		}
	}
	return strings.Join(out, ", ")
}

// nodeAge is the node's age, or "unknown" when it has no creation timestamp.
func nodeAge(node corev1.Node) string {
	if node.CreationTimestamp.Time.IsZero() {
		return "unknown"
	}
	return duration.HumanDuration(time.Since(node.CreationTimestamp.Time))
}

// getProviderInfo returns formatted provider information
func getProviderInfo(currentContext string) string {
	var providerInfo strings.Builder

	if strings.Contains(currentContext, "kind-") {
		clusterName := strings.TrimPrefix(currentContext, "kind-")
		providerInfo.WriteString(helpers.IconCluster + " " + "Provider: Kind (Local Development)\n")
		providerInfo.WriteString(fmt.Sprintf(helpers.IconBullet+" "+"Cluster Name: %s\n\n", clusterName))
		providerInfo.WriteString(helpers.IconBullet + " " + "Kind Cluster Details:\n")
		providerInfo.WriteString("  • Local development cluster running in Docker\n")
		providerInfo.WriteString("  • Access services via: https://adhar.localtest.me:8443\n")
		providerInfo.WriteString("  • Get service passwords with: adhar get secrets -p <provider>")
	} else {
		// Try to detect other providers
		if strings.Contains(strings.ToLower(currentContext), "gke") {
			providerInfo.WriteString("☁️  Provider: Google Kubernetes Engine (GKE)\n")
			providerInfo.WriteString("  • Managed Kubernetes service by Google Cloud\n")
			providerInfo.WriteString("  • Auto-scaling and auto-upgrades\n")
			providerInfo.WriteString("  • Integrated with Google Cloud services")
		} else if strings.Contains(strings.ToLower(currentContext), "eks") {
			providerInfo.WriteString("☁️  Provider: Amazon Elastic Kubernetes Service (EKS)\n")
			providerInfo.WriteString("  • High availability and security\n")
			providerInfo.WriteString("  • Integrated with AWS services")
		} else if strings.Contains(strings.ToLower(currentContext), "aks") {
			providerInfo.WriteString("☁️  Provider: Azure Kubernetes Service (AKS)\n")
			providerInfo.WriteString("  • Enterprise-grade security and compliance\n")
			providerInfo.WriteString("  • Integrated with Azure services")
		} else {
			providerInfo.WriteString("❓ Provider: Unknown\n")
			providerInfo.WriteString("  • Custom or self-managed Kubernetes cluster\n")
			providerInfo.WriteString("  • May be on-premises or other cloud provider")
		}
	}

	return providerInfo.String()
}

// getDetailedClusterInfo returns additional detailed cluster information
func getDetailedClusterInfo(clientset *kubernetes.Clientset, ctx context.Context) string {
	var detailedInfo strings.Builder

	// Get cluster resource quotas
	detailedInfo.WriteString(helpers.IconApp + " " + "Resource Information:\n")

	// Get node capacity and allocatable resources
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err == nil && len(nodes.Items) > 0 {
		var totalCPU, totalMemory int64
		for _, node := range nodes.Items {
			if cpu, ok := node.Status.Capacity[corev1.ResourceCPU]; ok {
				totalCPU += cpu.MilliValue()
			}
			if memory, ok := node.Status.Capacity[corev1.ResourceMemory]; ok {
				totalMemory += memory.Value()
			}
		}

		detailedInfo.WriteString(fmt.Sprintf("  • Total CPU: %dm\n", totalCPU))
		detailedInfo.WriteString(fmt.Sprintf("  • Total Memory: %dMi\n", totalMemory/(1024*1024)))
	}

	// Get storage classes
	storageClasses, err := clientset.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err == nil {
		detailedInfo.WriteString(fmt.Sprintf("  • Storage Classes: %d\n", len(storageClasses.Items)))
	}

	// Get persistent volumes
	pvs, err := clientset.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err == nil {
		detailedInfo.WriteString(fmt.Sprintf("  • Persistent Volumes: %d\n", len(pvs.Items)))
	}

	detailedInfo.WriteString("\n🔗 Access Information:\n")
	detailedInfo.WriteString("  • Dashboard: kubectl proxy\n")
	detailedInfo.WriteString("  • Cluster Info: kubectl cluster-info\n")
	detailedInfo.WriteString("  • Node Details: kubectl describe nodes\n")

	return detailedInfo.String()
}
