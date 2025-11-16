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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/client-go/kubernetes"
)

// environmentsCmd represents the environments command
var environmentsCmd = &cobra.Command{
	Use:     "environments [environment-name]",
	Aliases: []string{"envs", "env", "namespaces", "ns"},
	Short:   "Get environment information and configurations",
	Long: `Get detailed information about environments (namespaces) and their configurations.

This command provides:
• Environment status and resource usage
• Namespace quotas and limits
• ConfigMaps and secrets in each environment
• Network policies and RBAC configurations
• Resource consumption and health metrics
• Environment-specific services and workloads

Examples:
  adhar get environments                    # List all environments
  adhar get environments production         # Get specific environment info
  adhar get environments --all-namespaces  # List all namespaces (same as above)
  adhar get environments --resources        # Include resource usage
  adhar get environments --output json      # Output in JSON format`,
	RunE: runGetEnvironments,
}

var (
	// Environment-specific flags
	showQuotas    bool
	showPolicies  bool
	envResources  bool
	envConfigMaps bool
	envSecrets    bool
)

func init() {
	environmentsCmd.Flags().BoolVar(&showQuotas, "quotas", false, "Show resource quotas and limits")
	environmentsCmd.Flags().BoolVar(&showPolicies, "policies", false, "Show network policies and RBAC")
	environmentsCmd.Flags().BoolVar(&envResources, "resources", false, "Show resource usage statistics")
	environmentsCmd.Flags().BoolVar(&envConfigMaps, "configmaps", false, "Show ConfigMaps in each environment")
	environmentsCmd.Flags().BoolVar(&envSecrets, "secrets", false, "Show secrets in each environment")
}

type EnvironmentInfo struct {
	Name            string              `json:"name"`
	Status          string              `json:"status"`
	StatusColor     string              `json:"status_color"`
	Age             string              `json:"age"`
	Labels          map[string]string   `json:"labels,omitempty"`
	Annotations     map[string]string   `json:"annotations,omitempty"`
	ResourceQuotas  []ResourceQuotaInfo `json:"resource_quotas,omitempty"`
	LimitRanges     []LimitRangeInfo    `json:"limit_ranges,omitempty"`
	NetworkPolicies []NetworkPolicyInfo `json:"network_policies,omitempty"`
	ResourceUsage   EnvironmentUsage    `json:"resource_usage,omitempty"`
	ConfigMaps      []ConfigMapInfo     `json:"config_maps,omitempty"`
	Secrets         []EnvSecretInfo     `json:"secrets,omitempty"`
	Services        []ServiceSummary    `json:"services,omitempty"`
	Workloads       WorkloadSummary     `json:"workloads,omitempty"`
	CreationTime    time.Time           `json:"creation_time"`
	LastUpdated     time.Time           `json:"last_updated"`
}

type ResourceQuotaInfo struct {
	Name   string            `json:"name"`
	Hard   map[string]string `json:"hard"`
	Used   map[string]string `json:"used"`
	Status string            `json:"status"`
}

type LimitRangeInfo struct {
	Name   string               `json:"name"`
	Limits []LimitRangeItemInfo `json:"limits"`
}

type LimitRangeItemInfo struct {
	Type           string            `json:"type"`
	Default        map[string]string `json:"default,omitempty"`
	DefaultRequest map[string]string `json:"default_request,omitempty"`
	Max            map[string]string `json:"max,omitempty"`
	Min            map[string]string `json:"min,omitempty"`
}

type NetworkPolicyInfo struct {
	Name        string   `json:"name"`
	PodSelector string   `json:"pod_selector"`
	Ingress     []string `json:"ingress,omitempty"`
	Egress      []string `json:"egress,omitempty"`
}

type EnvironmentUsage struct {
	Pods         int `json:"pods"`
	Services     int `json:"services"`
	ConfigMaps   int `json:"config_maps"`
	Secrets      int `json:"secrets"`
	Deployments  int `json:"deployments"`
	StatefulSets int `json:"stateful_sets"`
	DaemonSets   int `json:"daemon_sets"`
	Jobs         int `json:"jobs"`
	PVCs         int `json:"pvcs"`
}

type ConfigMapInfo struct {
	Name string `json:"name"`
	Age  string `json:"age"`
	Keys int    `json:"keys"`
}

type EnvSecretInfo struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Age  string `json:"age"`
	Keys int    `json:"keys"`
}

type ServiceSummary struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	ClusterIP string `json:"cluster_ip"`
	Ports     int    `json:"ports"`
}

type WorkloadSummary struct {
	Deployments  int `json:"deployments"`
	StatefulSets int `json:"stateful_sets"`
	DaemonSets   int `json:"daemon_sets"`
	Jobs         int `json:"jobs"`
	Pods         int `json:"pods"`
}

func runGetEnvironments(cmd *cobra.Command, args []string) error {
	logger.Info("🌍 Retrieving environment information...")

	// Get Kubernetes client
	clientset, err := getKubernetesClient()
	if err != nil {
		return fmt.Errorf("failed to get Kubernetes client: %w", err)
	}

	// Get environments
	environments, err := getEnvironments(clientset, args)
	if err != nil {
		return fmt.Errorf("failed to get environments: %w", err)
	}

	if len(environments) == 0 {
		logger.Info("No environments found")
		return nil
	}

	// Display environments based on output format
	switch outputFormat {
	case "json":
		return helpers.PrintJSON(environments)
	case "yaml":
		return helpers.PrintYAML(environments)
	default:
		return displayEnvironmentsTable(environments)
	}
}

func getEnvironments(clientset *kubernetes.Clientset, envNames []string) ([]EnvironmentInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var environments []EnvironmentInfo

	// Get namespaces
	namespaces, err := clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list namespaces: %w", err)
	}

	for _, ns := range namespaces.Items {
		// Filter by specific environment name if provided
		if len(envNames) > 0 && !contains(envNames, ns.Name) {
			continue
		}

		env := EnvironmentInfo{
			Name:         ns.Name,
			CreationTime: ns.CreationTimestamp.Time,
			LastUpdated:  time.Now(),
			Labels:       ns.Labels,
			Annotations:  ns.Annotations,
		}

		// Calculate age
		env.Age = duration.HumanDuration(time.Since(ns.CreationTimestamp.Time))

		// Determine status
		env.Status, env.StatusColor = getNamespaceStatus(ns)

		// Get resource usage
		if envResources {
			env.ResourceUsage = getEnvironmentUsage(clientset, ctx, ns.Name)
		}

		// Get resource quotas if requested
		if showQuotas {
			quotas, err := getResourceQuotas(clientset, ctx, ns.Name)
			if err == nil {
				env.ResourceQuotas = quotas
			}

			limits, err := getLimitRanges(clientset, ctx, ns.Name)
			if err == nil {
				env.LimitRanges = limits
			}
		}

		// Get network policies if requested
		if showPolicies {
			policies, err := getNetworkPolicies(clientset, ctx, ns.Name)
			if err == nil {
				env.NetworkPolicies = policies
			}
		}

		// Get ConfigMaps if requested
		if envConfigMaps {
			configMaps, err := getConfigMaps(clientset, ctx, ns.Name)
			if err == nil {
				env.ConfigMaps = configMaps
			}
		}

		// Get Secrets if requested
		if envSecrets {
			secrets, err := getSecrets(clientset, ctx, ns.Name)
			if err == nil {
				env.Secrets = secrets
			}
		}

		// Get workload summary
		env.Workloads = getWorkloadSummary(clientset, ctx, ns.Name)

		environments = append(environments, env)
	}

	// Sort environments by name
	sort.Slice(environments, func(i, j int) bool {
		return environments[i].Name < environments[j].Name
	})

	return environments, nil
}

func getNamespaceStatus(ns corev1.Namespace) (string, string) {
	switch ns.Status.Phase {
	case corev1.NamespaceActive:
		return "✅ Active", "#10b981"
	case corev1.NamespaceTerminating:
		return "⚠️ Terminating", "#f59e0b"
	default:
		return "❓ Unknown", "#64748b"
	}
}

func getEnvironmentUsage(clientset *kubernetes.Clientset, ctx context.Context, namespace string) EnvironmentUsage {
	usage := EnvironmentUsage{}

	// Count pods
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		usage.Pods = len(pods.Items)
	}

	// Count services
	services, err := clientset.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		usage.Services = len(services.Items)
	}

	// Count ConfigMaps
	configMaps, err := clientset.CoreV1().ConfigMaps(namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		usage.ConfigMaps = len(configMaps.Items)
	}

	// Count Secrets
	secrets, err := clientset.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		usage.Secrets = len(secrets.Items)
	}

	// Count workloads
	deployments, err := clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		usage.Deployments = len(deployments.Items)
	}

	statefulSets, err := clientset.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		usage.StatefulSets = len(statefulSets.Items)
	}

	daemonSets, err := clientset.AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		usage.DaemonSets = len(daemonSets.Items)
	}

	jobs, err := clientset.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		usage.Jobs = len(jobs.Items)
	}

	// Count PVCs
	pvcs, err := clientset.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		usage.PVCs = len(pvcs.Items)
	}

	return usage
}

func getResourceQuotas(clientset *kubernetes.Clientset, ctx context.Context, namespace string) ([]ResourceQuotaInfo, error) {
	var quotas []ResourceQuotaInfo

	quotaList, err := clientset.CoreV1().ResourceQuotas(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	for _, quota := range quotaList.Items {
		hard := make(map[string]string)
		used := make(map[string]string)

		for resource, quantity := range quota.Status.Hard {
			hard[string(resource)] = quantity.String()
		}

		for resource, quantity := range quota.Status.Used {
			used[string(resource)] = quantity.String()
		}

		quotas = append(quotas, ResourceQuotaInfo{
			Name:   quota.Name,
			Hard:   hard,
			Used:   used,
			Status: "Active", // ResourceQuotas don't have a phase, assume Active
		})
	}

	return quotas, nil
}

func getLimitRanges(clientset *kubernetes.Clientset, ctx context.Context, namespace string) ([]LimitRangeInfo, error) {
	var limits []LimitRangeInfo

	limitList, err := clientset.CoreV1().LimitRanges(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	for _, limit := range limitList.Items {
		var limitItems []LimitRangeItemInfo

		for _, item := range limit.Spec.Limits {
			limitItem := LimitRangeItemInfo{
				Type: string(item.Type),
			}

			if item.Default != nil {
				limitItem.Default = make(map[string]string)
				for resource, quantity := range item.Default {
					limitItem.Default[string(resource)] = quantity.String()
				}
			}

			if item.DefaultRequest != nil {
				limitItem.DefaultRequest = make(map[string]string)
				for resource, quantity := range item.DefaultRequest {
					limitItem.DefaultRequest[string(resource)] = quantity.String()
				}
			}

			if item.Max != nil {
				limitItem.Max = make(map[string]string)
				for resource, quantity := range item.Max {
					limitItem.Max[string(resource)] = quantity.String()
				}
			}

			if item.Min != nil {
				limitItem.Min = make(map[string]string)
				for resource, quantity := range item.Min {
					limitItem.Min[string(resource)] = quantity.String()
				}
			}

			limitItems = append(limitItems, limitItem)
		}

		limits = append(limits, LimitRangeInfo{
			Name:   limit.Name,
			Limits: limitItems,
		})
	}

	return limits, nil
}

func getNetworkPolicies(clientset *kubernetes.Clientset, ctx context.Context, namespace string) ([]NetworkPolicyInfo, error) {
	var policies []NetworkPolicyInfo

	policyList, err := clientset.NetworkingV1().NetworkPolicies(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	for _, policy := range policyList.Items {
		policyInfo := NetworkPolicyInfo{
			Name:        policy.Name,
			PodSelector: metav1.FormatLabelSelector(&policy.Spec.PodSelector),
		}

		// Simplified ingress/egress representation
		if len(policy.Spec.Ingress) > 0 {
			policyInfo.Ingress = []string{fmt.Sprintf("%d rules", len(policy.Spec.Ingress))}
		}

		if len(policy.Spec.Egress) > 0 {
			policyInfo.Egress = []string{fmt.Sprintf("%d rules", len(policy.Spec.Egress))}
		}

		policies = append(policies, policyInfo)
	}

	return policies, nil
}

func getConfigMaps(clientset *kubernetes.Clientset, ctx context.Context, namespace string) ([]ConfigMapInfo, error) {
	var configMaps []ConfigMapInfo

	cmList, err := clientset.CoreV1().ConfigMaps(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	for _, cm := range cmList.Items {
		configMaps = append(configMaps, ConfigMapInfo{
			Name: cm.Name,
			Age:  duration.HumanDuration(time.Since(cm.CreationTimestamp.Time)),
			Keys: len(cm.Data) + len(cm.BinaryData),
		})
	}

	return configMaps, nil
}

func getSecrets(clientset *kubernetes.Clientset, ctx context.Context, namespace string) ([]EnvSecretInfo, error) {
	var secrets []EnvSecretInfo

	secretList, err := clientset.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	for _, secret := range secretList.Items {
		secrets = append(secrets, EnvSecretInfo{
			Name: secret.Name,
			Type: string(secret.Type),
			Age:  duration.HumanDuration(time.Since(secret.CreationTimestamp.Time)),
			Keys: len(secret.Data),
		})
	}

	return secrets, nil
}

func getWorkloadSummary(clientset *kubernetes.Clientset, ctx context.Context, namespace string) WorkloadSummary {
	summary := WorkloadSummary{}

	// Count deployments
	deployments, err := clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		summary.Deployments = len(deployments.Items)
	}

	// Count StatefulSets
	statefulSets, err := clientset.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		summary.StatefulSets = len(statefulSets.Items)
	}

	// Count DaemonSets
	daemonSets, err := clientset.AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		summary.DaemonSets = len(daemonSets.Items)
	}

	// Count Jobs
	jobs, err := clientset.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		summary.Jobs = len(jobs.Items)
	}

	// Count Pods
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		summary.Pods = len(pods.Items)
	}

	return summary
}

func displayEnvironmentsTable(environments []EnvironmentInfo) error {
	logger.Info(fmt.Sprintf("📋 Found %d environments", len(environments)))

	// Create table header
	var table strings.Builder
	table.WriteString(fmt.Sprintf("%-25s %-12s %-8s %-10s %-10s %-8s\n",
		"🏷️  NAME", "📊 STATUS", "📅 AGE", "🚀 WORKLOADS", "💾 RESOURCES", "🔐 SECRETS"))
	table.WriteString(strings.Repeat("─", 75) + "\n")

	// Display environments
	for _, env := range environments {
		workloadCount := env.Workloads.Deployments + env.Workloads.StatefulSets + env.Workloads.DaemonSets + env.Workloads.Jobs
		resourceCount := env.ResourceUsage.Services + env.ResourceUsage.ConfigMaps + env.ResourceUsage.PVCs

		row := fmt.Sprintf("%-25s %-12s %-8s %-10d %-10d %-8d\n",
			truncateString(env.Name, 23),
			env.Status,
			env.Age,
			workloadCount,
			resourceCount,
			env.ResourceUsage.Secrets)
		table.WriteString(row)

		// Show additional details if requested
		if showQuotas && len(env.ResourceQuotas) > 0 {
			for _, quota := range env.ResourceQuotas {
				quotaLine := fmt.Sprintf("  📊 Quota: %s", quota.Name)
				table.WriteString(quotaLine + "\n")
			}
		}

		if envResources && (env.ResourceUsage.Pods > 0 || env.ResourceUsage.Services > 0) {
			resourceLine := fmt.Sprintf("  💾 Usage: %d pods, %d services, %d configmaps",
				env.ResourceUsage.Pods, env.ResourceUsage.Services, env.ResourceUsage.ConfigMaps)
			table.WriteString(resourceLine + "\n")
		}
	}

	// Display the table in a bordered box
	tableBox := helpers.BorderStyle.Width(80).Render(table.String())
	fmt.Println(tableBox)

	return nil
}
