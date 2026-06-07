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

package health

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/k8s"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

// componentCheck describes a core platform component and how to locate its
// workload in the cluster via a label selector.
type componentCheck struct {
	Name      string
	Icon      string
	Namespace string
	Selector  string
}

// coreComponents is the canonical list of platform components inspected by the
// health command. Selectors mirror those used by `adhar get status`.
var coreComponents = []componentCheck{
	{"Cilium", "🕸️", globals.AdharSystemNamespace, "app.kubernetes.io/name=cilium-agent"},
	// The Gateway data path is served by Cilium's Envoy DaemonSet, so we report
	// the cilium-envoy DaemonSet health for the "Cilium Gateway" row.
	{"Cilium Gateway", "🌐", globals.AdharSystemNamespace, "app.kubernetes.io/name=cilium-envoy"},
	{"ArgoCD", "🚀", globals.AdharSystemNamespace, "app.kubernetes.io/name=argocd-server"},
	{"Gitea", "🦊", globals.AdharSystemNamespace, "app=gitea"},
	{"Crossplane", "🔧", globals.AdharSystemNamespace, "app=crossplane"},
}

// componentResult holds the outcome of a single component health check.
type componentResult struct {
	Name     string
	Icon     string
	Status   string // "Healthy", "Degraded", "Not Found"
	Replicas string
	Healthy  bool
}

// platformHealth is the aggregate health snapshot.
type platformHealth struct {
	APIReachable bool
	APIError     string
	NodesTotal   int
	NodesReady   int
	Components   []componentResult
	HealthScore  int
	Overall      string
}

// getClientset returns a Kubernetes clientset using the shared platform helper.
func getClientset() (*kubernetes.Clientset, error) {
	return k8s.GetClientset()
}

// parseTimeout parses the --timeout flag, falling back to 30s on error.
func parseTimeout(s string) time.Duration {
	if s == "" {
		return 30 * time.Second
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 30 * time.Second
	}
	return d
}

// checkComponent inspects a single component's Deployment or DaemonSet and
// returns its health result.
func checkComponent(ctx context.Context, clientset *kubernetes.Clientset, c componentCheck) componentResult {
	res := componentResult{Name: c.Name, Icon: c.Icon}

	deployments, err := clientset.AppsV1().Deployments(c.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: c.Selector,
	})
	if err == nil && len(deployments.Items) > 0 {
		dep := deployments.Items[0]
		res.Replicas = fmt.Sprintf("%d/%d", dep.Status.ReadyReplicas, dep.Status.Replicas)
		if dep.Status.Replicas > 0 && dep.Status.ReadyReplicas == dep.Status.Replicas {
			res.Status, res.Healthy = "✅ Healthy", true
		} else {
			res.Status = "⚠️ Degraded"
		}
		return res
	}

	daemonSets, dsErr := clientset.AppsV1().DaemonSets(c.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: c.Selector,
	})
	if dsErr == nil && len(daemonSets.Items) > 0 {
		ds := daemonSets.Items[0]
		res.Replicas = fmt.Sprintf("%d/%d", ds.Status.NumberReady, ds.Status.DesiredNumberScheduled)
		if ds.Status.DesiredNumberScheduled > 0 && ds.Status.NumberReady == ds.Status.DesiredNumberScheduled {
			res.Status, res.Healthy = "✅ Healthy", true
		} else {
			res.Status = "⚠️ Degraded"
		}
		return res
	}

	res.Status = "❌ Not Found"
	res.Replicas = "0/0"
	return res
}

// collectPlatformHealth performs the full health sweep: API reachability, node
// readiness, and per-component checks, then computes an overall score.
func collectPlatformHealth(clientset *kubernetes.Clientset, components []componentCheck, to time.Duration) *platformHealth {
	ctx, cancel := context.WithTimeout(context.Background(), to)
	defer cancel()

	h := &platformHealth{}

	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		h.APIReachable = false
		h.APIError = err.Error()
		h.Overall = "❌ Unreachable"
		return h
	}
	h.APIReachable = true
	h.NodesTotal = len(nodes.Items)
	for _, node := range nodes.Items {
		for _, cond := range node.Status.Conditions {
			if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
				h.NodesReady++
				break
			}
		}
	}

	for _, c := range components {
		h.Components = append(h.Components, checkComponent(ctx, clientset, c))
	}

	h.HealthScore, h.Overall = scoreHealth(h)
	return h
}

// scoreHealth derives an overall status string and 0-100 score.
func scoreHealth(h *platformHealth) (int, string) {
	score := 100
	overall := "✅ Healthy"

	if h.NodesTotal == 0 || h.NodesReady < h.NodesTotal {
		score -= 20
		overall = "⚠️ Degraded"
	}

	for _, c := range h.Components {
		if strings.Contains(c.Status, "❌") {
			score -= 25
			overall = "❌ Critical"
		} else if strings.Contains(c.Status, "⚠️") {
			score -= 10
			if overall == "✅ Healthy" {
				overall = "⚠️ Degraded"
			}
		}
	}

	if score < 0 {
		score = 0
	}
	return score, overall
}

// resolveComponents filters coreComponents by a user-supplied name (case
// insensitive, matches the leading word, e.g. "cilium" or "argocd"). Returns the
// full list when name is empty.
func resolveComponents(name string) ([]componentCheck, error) {
	if name == "" {
		return coreComponents, nil
	}
	want := strings.ToLower(strings.TrimSpace(name))
	var matched []componentCheck
	for _, c := range coreComponents {
		key := strings.ToLower(c.Name)
		if key == want || strings.HasPrefix(key, want) || strings.Contains(key, want) {
			matched = append(matched, c)
		}
	}
	if len(matched) == 0 {
		var names []string
		for _, c := range coreComponents {
			names = append(names, strings.ToLower(strings.Fields(c.Name)[0]))
		}
		return nil, fmt.Errorf("unknown component %q (available: %s)", name, strings.Join(names, ", "))
	}
	return matched, nil
}

// renderHealth prints a clean summary table for the given health snapshot.
func renderHealth(h *platformHealth) {
	if !h.APIReachable {
		fmt.Println(helpers.ErrorStyle.Render("❌ Kubernetes API unreachable"))
		if h.APIError != "" {
			fmt.Println(helpers.CreateMuted("   " + h.APIError))
		}
		fmt.Println(helpers.CreateMuted("   Is the cluster running? Try `adhar up` or check your kubeconfig context."))
		return
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("🏥 Overall Health: %s\n", h.Overall))
	b.WriteString(fmt.Sprintf("💯 Health Score:   %d/100\n", h.HealthScore))
	b.WriteString("☸️  Kubernetes API: ✅ Reachable\n")
	b.WriteString(fmt.Sprintf("🖥️  Nodes Ready:    %d/%d", h.NodesReady, h.NodesTotal))
	fmt.Println(helpers.BorderStyle.Width(70).Render(b.String()))

	fmt.Printf("\n%s\n", helpers.TitleStyle.Render("🔧 Component Health"))

	var t strings.Builder
	t.WriteString(fmt.Sprintf("%-22s %-15s %-10s\n", "🏷️  COMPONENT", "📊 STATUS", "🔄 READY"))
	t.WriteString(strings.Repeat("─", 55) + "\n")
	for _, c := range h.Components {
		t.WriteString(fmt.Sprintf("%-22s %-15s %-10s\n", c.Icon+" "+c.Name, c.Status, c.Replicas))
	}
	fmt.Println(helpers.BorderStyle.Width(70).Render(t.String()))
}

// runHealthSweep is the shared entry point used by the root command and the
// `check`/`report` subcommands. It builds the client, runs the checks against
// the resolved component set, prints the result, and returns a non-nil error
// when the cluster is unreachable.
func runHealthSweep(componentName string, to time.Duration) (*platformHealth, error) {
	clientset, err := getClientset()
	if err != nil {
		fmt.Println(helpers.ErrorStyle.Render("❌ Could not connect to the cluster"))
		fmt.Println(helpers.CreateMuted("   " + err.Error()))
		return nil, fmt.Errorf("failed to get Kubernetes client: %w", err)
	}

	components, err := resolveComponents(componentName)
	if err != nil {
		return nil, err
	}

	h := collectPlatformHealth(clientset, components, to)
	renderHealth(h)

	if !h.APIReachable {
		return h, fmt.Errorf("kubernetes API unreachable")
	}
	return h, nil
}

// healthLevels returns a stable, sorted list of "name: status" lines, used by
// the report subcommand for non-table output.
func (h *platformHealth) summaryLines() []string {
	lines := []string{
		fmt.Sprintf("overall: %s", h.Overall),
		fmt.Sprintf("score: %d", h.HealthScore),
		fmt.Sprintf("nodesReady: %d/%d", h.NodesReady, h.NodesTotal),
	}
	for _, c := range h.Components {
		lines = append(lines, fmt.Sprintf("%s: %s (%s)", strings.ToLower(c.Name), c.Status, c.Replicas))
	}
	sort.Strings(lines)
	return lines
}

func mustJSON(v interface{}) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return []byte(fmt.Sprintf("error marshaling json: %v", err))
	}
	return append(b, '\n')
}

func mustYAML(v interface{}) []byte {
	b, err := yaml.Marshal(v)
	if err != nil {
		return []byte(fmt.Sprintf("error marshaling yaml: %v", err))
	}
	return b
}
