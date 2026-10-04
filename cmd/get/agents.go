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
)

// agentWorkloadGVR is the AgentWorkload XRD (ADR-0024 pillar 1, controlplane
// configuration/xrd/agentworkload.xrd.yaml): a namespaced Crossplane v2 XR.
var agentWorkloadGVR = schema.GroupVersionResource{Group: "platform.adhar.io", Version: "v1alpha1", Resource: "agentworkloads"}

var agentsCmd = &cobra.Command{
	Use:     "agents [name]",
	Aliases: []string{"agent", "agentworkloads"},
	Short:   "List AI agent workloads and what each one may do",
	Long: `List the AgentWorkloads on the platform: each is an isolated AI agent
(its own namespace, ServiceAccount identity on the AI gateway, allow-listed
models and tools, token budget) owned by a project and a team.

Agent namespaces are deliberately NOT environments: 'adhar get environments'
hides them, this command shows them.

Examples:
  adhar get agents                 # every agent, across projects
  adhar get agents payments-bot    # one agent
  adhar get agents -o json`,
	RunE: runGetAgents,
}

func init() {
	GetCmd.AddCommand(agentsCmd)
}

// AgentInfo is the CLI's view of one AgentWorkload.
type AgentInfo struct {
	Name        string   `json:"name"`
	Environment string   `json:"environment"`
	Namespace   string   `json:"namespace"`
	Project     string   `json:"project"`
	Team        string   `json:"team"`
	Owner       string   `json:"owner,omitempty"`
	Models      []string `json:"models"`
	Tools       []string `json:"tools"`
	TokensHour  int64    `json:"tokensPerHour"`
	Ready       bool     `json:"ready"`
	Phase       string   `json:"phase,omitempty"`
	Age         string   `json:"age"`
}

func runGetAgents(cmd *cobra.Command, args []string) error {
	cfg, err := helpers.GetKubeConfig()
	if err != nil {
		return fmt.Errorf("failed to get kubeconfig: %w", err)
	}
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	list, err := dc.Resource(agentWorkloadGVR).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing AgentWorkloads (is the control plane installed?): %w", err)
	}
	agents := agentsFromList(list.Items, args)
	if len(agents) == 0 {
		logger.Info("No agent workloads found")
		return nil
	}
	switch outputFormat {
	case "json":
		return helpers.PrintJSON(agents)
	case "yaml":
		return helpers.PrintYAML(agents)
	default:
		return displayAgentsTable(agents)
	}
}

// agentsFromList projects AgentWorkload objects onto AgentInfo, optionally
// restricted to the named agents. Pure, so it is unit-tested without a cluster.
func agentsFromList(items []unstructured.Unstructured, only []string) []AgentInfo {
	want := map[string]bool{}
	for _, n := range only {
		want[n] = true
	}
	var out []AgentInfo
	for _, it := range items {
		name, _, _ := unstructured.NestedString(it.Object, "spec", "parameters", "name")
		if name == "" {
			name = it.GetName()
		}
		if len(want) > 0 && !want[name] && !want[it.GetName()] {
			continue
		}
		env, _, _ := unstructured.NestedString(it.Object, "spec", "parameters", "environment")
		if env == "" {
			env = "dev"
		}
		project, _, _ := unstructured.NestedString(it.Object, "spec", "parameters", "project")
		if project == "" {
			project = "adhar"
		}
		team, _, _ := unstructured.NestedString(it.Object, "spec", "parameters", "team")
		if team == "" {
			team = "platform"
		}
		owner, _, _ := unstructured.NestedString(it.Object, "spec", "parameters", "owner")
		models, _, _ := unstructured.NestedStringSlice(it.Object, "spec", "parameters", "models", "allow")
		tools, _, _ := unstructured.NestedStringSlice(it.Object, "spec", "parameters", "tools", "allow")
		tph, found, _ := unstructured.NestedInt64(it.Object, "spec", "parameters", "budget", "tokensPerHour")
		if !found {
			tph = 100000
		}
		ns, _, _ := unstructured.NestedString(it.Object, "status", "namespace")
		if ns == "" {
			ns = name + "-" + env
		}
		phase, _, _ := unstructured.NestedString(it.Object, "status", "phase")
		ready := false
		if conds, _, _ := unstructured.NestedSlice(it.Object, "status", "conditions"); conds != nil {
			for _, c := range conds {
				cm, _ := c.(map[string]interface{})
				if cm["type"] == "Ready" && cm["status"] == "True" {
					ready = true
				}
			}
		}
		age := ""
		if ts := it.GetCreationTimestamp(); !ts.IsZero() {
			age = duration.HumanDuration(time.Since(ts.Time))
		}
		out = append(out, AgentInfo{Name: name, Environment: env, Namespace: ns, Project: project, Team: team, Owner: owner,
			Models: models, Tools: tools, TokensHour: tph, Ready: ready, Phase: phase, Age: age})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func displayAgentsTable(agents []AgentInfo) error {
	logger.Info(fmt.Sprintf(helpers.IconApp+" Found %d agent workloads", len(agents)))
	t := helpers.NewTable("NAME", "ENV", "PROJECT", "TEAM", "MODELS", "TOOLS", "BUDGET/H", "READY", "AGE").WithBudget(100)
	for _, a := range agents {
		state := helpers.StateReady("Ready")
		if !a.Ready {
			state = helpers.StatePending("Pending")
		}
		t.Row(a.Name, a.Environment, a.Project, a.Team, summarizeList(a.Models), summarizeList(a.Tools),
			fmt.Sprintf("%d", a.TokensHour), state, a.Age)
	}
	fmt.Println(helpers.BorderStyle.Width(104).Render(t.Render()))
	return nil
}

// summarizeList keeps a table cell readable: the first two entries, then a count.
func summarizeList(xs []string) string {
	switch {
	case len(xs) == 0:
		return "none"
	case len(xs) <= 2:
		return strings.Join(xs, ",")
	default:
		return fmt.Sprintf("%s,+%d", strings.Join(xs[:2], ","), len(xs)-2)
	}
}
