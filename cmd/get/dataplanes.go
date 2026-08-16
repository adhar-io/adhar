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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	dpFormatJSON = "json"
	dpFormatYAML = "yaml"
	dpReadyLabel = "✅ Ready"
)

// dataPlaneGVR is the DataPlane custom resource (platform.adhar.io/v1alpha1).
var dataPlaneGVR = schema.GroupVersionResource{
	Group:    "platform.adhar.io",
	Version:  "v1alpha1",
	Resource: "dataplanes",
}

// dataplanesCmd implements `adhar get dataplanes` (ADR-0023 §9). The control
// plane manages N data planes through the DataPlane API; this surfaces the fleet
// the same way `kubectl get dataplanes` would, but formatted with the platform's
// table style and tolerant of the CRD not being installed yet.
var dataplanesCmd = &cobra.Command{
	Use:     "dataplanes [name]",
	Aliases: []string{"dataplane", "dp", "dps"},
	Short:   "List data planes registered with the control plane",
	Long: `List the data planes managed by this control plane (ADR-0023).

Each DataPlane is a first-class cluster role that runs application workloads,
registered with ArgoCD and (optionally) joined to the Cilium Cluster Mesh. The
control plane itself runs only fleet/platform services.

Examples:
  adhar get dataplanes                 # Table of all data planes
  adhar get dataplanes prod-sgp        # A single data plane
  adhar get dataplanes -o json         # Machine-readable output`,
	RunE: runGetDataPlanes,
}

// DataPlaneInfo is the flattened view rendered by the command.
type DataPlaneInfo struct {
	Name        string    `json:"name"`
	Mode        string    `json:"mode"`
	Provider    string    `json:"provider"`
	Profile     string    `json:"profile"`
	Apps        int64     `json:"apps"`
	Ready       string    `json:"ready"`
	K8sVersion  string    `json:"kubernetes_version"`
	Endpoint    string    `json:"endpoint"`
	Age         string    `json:"age"`
	CreatedTime time.Time `json:"created_time"`
}

func runGetDataPlanes(cmd *cobra.Command, args []string) error {
	logger.Info("🚀 Retrieving data planes...")

	client, err := getDynamicClient()
	if err != nil {
		return fmt.Errorf("failed to get Kubernetes client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	list, err := client.Resource(dataPlaneGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		// The CRD may not be installed on a control plane that has not yet
		// adopted plane separation — say so plainly rather than erroring hard.
		if strings.Contains(err.Error(), "could not find the requested resource") ||
			strings.Contains(err.Error(), "the server could not find") {
			logger.Info("No DataPlane CRD installed on this cluster (plane separation not yet enabled — ADR-0023).")
			return nil
		}
		return fmt.Errorf("failed to list dataplanes: %w", err)
	}

	var planes []DataPlaneInfo
	for _, item := range list.Items {
		if len(args) > 0 && !contains(args, item.GetName()) {
			continue
		}
		planes = append(planes, flattenDataPlane(item))
	}

	if len(planes) == 0 {
		logger.Info("No data planes found")
		return nil
	}

	sort.Slice(planes, func(i, j int) bool { return planes[i].Name < planes[j].Name })

	switch outputFormat {
	case dpFormatJSON:
		return helpers.PrintJSON(planes)
	case dpFormatYAML:
		return helpers.PrintYAML(planes)
	default:
		displayDataPlanesTable(planes)
		return nil
	}
}

func flattenDataPlane(item unstructured.Unstructured) DataPlaneInfo {
	mode, _, _ := unstructured.NestedString(item.Object, "spec", "infrastructure", "mode")
	provider, _, _ := unstructured.NestedString(item.Object, "spec", "infrastructure", "provider")
	profile, _, _ := unstructured.NestedString(item.Object, "spec", "profile")
	apps, _, _ := unstructured.NestedInt64(item.Object, "status", "appCount")
	k8sVer, _, _ := unstructured.NestedString(item.Object, "status", "kubernetesVersion")
	endpoint, _, _ := unstructured.NestedString(item.Object, "status", "endpoint")

	info := DataPlaneInfo{
		Name:        item.GetName(),
		Mode:        orDash(mode),
		Provider:    orDash(provider),
		Profile:     orDash(profile),
		Apps:        apps,
		Ready:       readyFromConditions(item.Object),
		K8sVersion:  orDash(k8sVer),
		Endpoint:    orDash(endpoint),
		CreatedTime: item.GetCreationTimestamp().Time,
	}
	if !info.CreatedTime.IsZero() {
		info.Age = duration.HumanDuration(time.Since(info.CreatedTime))
	} else {
		info.Age = "-"
	}
	return info
}

// readyFromConditions reads the aggregate "Ready" condition status.
func readyFromConditions(obj map[string]interface{}) string {
	conds, _, _ := unstructured.NestedSlice(obj, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t == "Ready" {
			switch s, _ := m["status"].(string); s {
			case "True":
				return dpReadyLabel
			case "False":
				return "❌ NotReady"
			default:
				return "⏳ Pending"
			}
		}
	}
	return "⏳ Pending"
}

func displayDataPlanesTable(planes []DataPlaneInfo) {
	logger.Info(fmt.Sprintf("📋 Found %d data plane(s)", len(planes)))

	var table strings.Builder
	fmt.Fprintf(&table, "%-24s %-11s %-12s %-10s %-6s %-12s %-8s\n",
		"🏷️  NAME", "🧩 MODE", "☁️  PROVIDER", "📦 PROFILE", "📊 APPS", "📶 READY", "📅 AGE")
	table.WriteString(strings.Repeat("─", 92) + "\n")
	for _, p := range planes {
		fmt.Fprintf(&table, "%-24s %-11s %-12s %-10s %-6d %-12s %-8s\n",
			truncateString(p.Name, 22), p.Mode, truncateString(p.Provider, 10),
			p.Profile, p.Apps, p.Ready, p.Age)
	}
	fmt.Println(helpers.BorderStyle.Width(95).Render(table.String()))
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// getDynamicClient builds a dynamic client from the default kubeconfig, matching
// getKubernetesClient()'s loading rules but for custom resources.
func getDynamicClient() (dynamic.Interface, error) {
	kubeconfig := clientcmd.NewDefaultClientConfigLoadingRules().GetDefaultFilename()
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}
	return dynamic.NewForConfig(config)
}
