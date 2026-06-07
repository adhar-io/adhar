/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the file at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package health

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

// HealthCmd represents the health command
var HealthCmd = &cobra.Command{
	Use:   "health",
	Short: "Check platform health and status",
	Long: `Check the health and status of the Adhar platform and its components.
	
This command provides:
• Overall platform health status
• Component-specific health checks
• Detailed health reports
• Health history and trends
• Troubleshooting recommendations

Examples:
  adhar health                    # Overall platform health
  adhar health --detailed         # Detailed health report
  adhar health --namespace=prod   # Health for specific namespace
  adhar health --component=argocd # Health for specific component
  adhar health --watch            # Watch health in real-time`,
	RunE: runHealth,
}

var (
	// Health command flags
	detailed  bool
	namespace string
	component string
	watch     bool
	timeout   string
	export    string
)

func init() {
	// Health command flags
	HealthCmd.Flags().BoolVarP(&detailed, "detailed", "d", false, "Show detailed health information")
	HealthCmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Check health for specific namespace")
	HealthCmd.Flags().StringVarP(&component, "component", "c", "", "Check health for specific component")
	HealthCmd.Flags().BoolVarP(&watch, "watch", "w", false, "Watch health status in real-time")
	HealthCmd.Flags().StringVarP(&timeout, "timeout", "t", "30s", "Health check timeout")
	HealthCmd.Flags().StringVarP(&export, "export", "e", "", "Export health report (json, yaml, html)")

	// Add subcommands
	HealthCmd.AddCommand(checkCmd)
	HealthCmd.AddCommand(reportCmd)
	HealthCmd.AddCommand(historyCmd)
}

func runHealth(cmd *cobra.Command, args []string) error {
	logger.Info("🏥 Checking Adhar platform health...")

	if component != "" {
		return checkComponentHealth(component)
	}

	if namespace != "" {
		return checkNamespaceHealth(namespace)
	}

	return checkOverallHealth()
}

func checkOverallHealth() error {
	logger.Info("🔍 Performing overall platform health check...")

	_, err := runHealthSweep("", parseTimeout(timeout))
	return err
}

func checkComponentHealth(componentName string) error {
	logger.Info("🔍 Checking component health: " + componentName)

	_, err := runHealthSweep(componentName, parseTimeout(timeout))
	return err
}

func checkNamespaceHealth(namespaceName string) error {
	logger.Info("🔍 Checking namespace health: " + namespaceName)

	clientset, err := getClientset()
	if err != nil {
		fmt.Println(helpers.ErrorStyle.Render("❌ Could not connect to the cluster"))
		fmt.Println(helpers.CreateMuted("   " + err.Error()))
		return fmt.Errorf("failed to get Kubernetes client: %w", err)
	}
	return reportNamespaceHealth(clientset, namespaceName, parseTimeout(timeout))
}

// reportNamespaceHealth summarizes pod health for a single namespace.
func reportNamespaceHealth(clientset *kubernetes.Clientset, ns string, to time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), to)
	defer cancel()

	pods, err := clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list pods in namespace %q: %w", ns, err)
	}

	var running, pending, failed, succeeded int
	var problems []string
	for _, p := range pods.Items {
		switch p.Status.Phase {
		case corev1.PodRunning:
			running++
		case corev1.PodPending:
			pending++
			problems = append(problems, fmt.Sprintf("%s is Pending", p.Name))
		case corev1.PodFailed:
			failed++
			problems = append(problems, fmt.Sprintf("%s is Failed", p.Name))
		case corev1.PodSucceeded:
			succeeded++
		}
		for _, cs := range p.Status.ContainerStatuses {
			if cs.State.Waiting != nil && (cs.State.Waiting.Reason == "CrashLoopBackOff" || cs.State.Waiting.Reason == "ImagePullBackOff") {
				problems = append(problems, fmt.Sprintf("%s: %s", p.Name, cs.State.Waiting.Reason))
			}
		}
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("📦 Namespace: %s\n", ns))
	b.WriteString(fmt.Sprintf("🟢 Running: %d   🟡 Pending: %d   🔴 Failed: %d   ✅ Succeeded: %d\n", running, pending, failed, succeeded))
	b.WriteString(fmt.Sprintf("📊 Total Pods: %d", len(pods.Items)))
	fmt.Println(helpers.BorderStyle.Width(70).Render(b.String()))

	if len(problems) > 0 {
		fmt.Printf("\n%s\n", helpers.WarningStyle.Render("⚠️  Issues"))
		for _, p := range problems {
			fmt.Println("  • " + p)
		}
	}
	return nil
}
