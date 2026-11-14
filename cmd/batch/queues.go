/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
)

// Queues (design 0025 §4.3, Phase 2). Volcano's Queue objects are
// cluster-scoped: one root `adhar-batch` whose capability is the cluster's
// batch ceiling, and under it one LEAF per team and tier, `<org>-<team>-<env>`,
// composed by the Team XR. Only leaves schedule. A team member may `get` its
// own three leaves by name and list nothing (§10.2), which is why --team reads
// by name and the bare command lists — the operator's view.

const (
	// rootQueueDefault is the parent every leaf hangs from, unless the scheduler
	// descriptor names another (D7).
	rootQueueDefault = "adhar-batch"
	// schedulerDescriptor is ConfigMap adhar-system/adhar-batch-scheduler (§4.1,
	// D7): the BatchJob composition takes the Volcano path only while it exists.
	schedulerDescriptor = "adhar-batch-scheduler"
)

// tiers are the three fixed environment tiers a team has a leaf for.
var tiers = []string{"dev", "test", "prod"}

var queuesTeam string

var queuesCmd = &cobra.Command{
	Use:   "queues",
	Short: "List the batch queues (Volcano) with their share and usage",
	Long: `List the Volcano queues batch runs wait in: the adhar-batch root, whose
capability is the cluster's batch ceiling, and one leaf per team and tier,
<org>-<team>-<env>. For each: what it may use (capability), what it is owed
(deserved), what it holds now (allocated), and its PodGroups by phase — a gang
in PENDING has not been admitted, one in INQUEUE is being started. A leaf at its
capability with gangs waiting is marked saturated.

--team reads that team's three leaves by name, which is all a team member may
read. Without it the command lists every queue, which needs list on queues.

On a cluster without Volcano there are no queues: batch runs on the native path.`,
	Example: `  adhar batch queues                       # the root and every team's leaves
  adhar batch queues --team acme-payments  # acme-payments-dev, -test and -prod
  adhar batch queues -o json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cl, err := connect(cmd)
		if err != nil {
			return err
		}
		report, err := readQueues(ctx(cmd), cl.dc, cl.cs.Discovery(), strings.TrimSpace(queuesTeam))
		if err != nil {
			return err
		}
		if machineReadable() {
			return printMachine(report)
		}
		printQueues(report)
		return nil
	},
}

func init() {
	queuesCmd.Flags().StringVar(&queuesTeam, "team", "", "Only this team's leaves, by its Team name <org>-<team> (what a team member can read)")
}

// queueView is one Volcano Queue as `queues` reports it.
type queueView struct {
	Name        string            `json:"name" yaml:"name"`
	Parent      string            `json:"parent,omitempty" yaml:"parent,omitempty"`
	Team        string            `json:"team,omitempty" yaml:"team,omitempty"`
	Tier        string            `json:"tier,omitempty" yaml:"tier,omitempty"`
	State       string            `json:"state,omitempty" yaml:"state,omitempty"`
	Priority    *int64            `json:"priority,omitempty" yaml:"priority,omitempty"`
	Weight      *int64            `json:"weight,omitempty" yaml:"weight,omitempty"`
	Reclaimable *bool             `json:"reclaimable,omitempty" yaml:"reclaimable,omitempty"`
	Capability  map[string]string `json:"capability,omitempty" yaml:"capability,omitempty"`
	Deserved    map[string]string `json:"deserved,omitempty" yaml:"deserved,omitempty"`
	Guarantee   map[string]string `json:"guarantee,omitempty" yaml:"guarantee,omitempty"`
	Allocated   map[string]string `json:"allocated,omitempty" yaml:"allocated,omitempty"`
	Pending     int64             `json:"pending" yaml:"pending"`
	Inqueue     int64             `json:"inqueue" yaml:"inqueue"`
	Running     int64             `json:"running" yaml:"running"`
	Completed   int64             `json:"completed" yaml:"completed"`
	// Saturated: allocated has reached capability on some resource while gangs
	// wait — the condition BatchQueueSaturated alerts on (§11.3).
	Saturated bool `json:"saturated" yaml:"saturated"`
}

// missingQueue is a leaf --team expected and could not show.
type missingQueue struct {
	Name   string `json:"name" yaml:"name"`
	Reason string `json:"reason" yaml:"reason"`
}

// queueReport is everything `queues` prints, and its -o json.
type queueReport struct {
	// Volcano says the queue API is served at all. Without it nothing else is set.
	Volcano bool `json:"volcano" yaml:"volcano"`
	// Descriptor is the scheduler descriptor's state: present, absent or
	// unreadable (a team member may not read adhar-system).
	Descriptor string         `json:"descriptor,omitempty" yaml:"descriptor,omitempty"`
	Scheduler  string         `json:"scheduler,omitempty" yaml:"scheduler,omitempty"`
	RootQueue  string         `json:"rootQueue,omitempty" yaml:"rootQueue,omitempty"`
	Root       *queueView     `json:"root,omitempty" yaml:"root,omitempty"`
	Queues     []queueView    `json:"queues" yaml:"queues"`
	Missing    []missingQueue `json:"missing,omitempty" yaml:"missing,omitempty"`
}

// volcanoServes says whether the API server serves resource in Volcano's
// scheduling group. Discovery, not a probing LIST: a NotFound from a GET of a
// queue by name would otherwise be "no such queue" and "no Volcano" at once.
func volcanoServes(disc discovery.DiscoveryInterface, resourceName string) (bool, error) {
	list, err := disc.ServerResourcesForGroupVersion(queueGVR.GroupVersion().String())
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("asking the API server whether Volcano is installed: %w", err)
	}
	for _, r := range list.APIResources {
		if r.Name == resourceName {
			return true, nil
		}
	}
	return false, nil
}

// readQueues builds the report. With team, it GETs that team's three leaves by
// name; without, it LISTs and keeps the root and its leaves.
func readQueues(c context.Context, dc dynamic.Interface, disc discovery.DiscoveryInterface, team string) (queueReport, error) {
	report := queueReport{Queues: []queueView{}}
	served, err := volcanoServes(disc, queueGVR.Resource)
	if err != nil || !served {
		return report, err
	}
	report.Volcano = true
	report.RootQueue = rootQueueDefault
	report.Descriptor = "absent"
	cm, err := dc.Resource(configMapGVR).Namespace(globals.AdharSystemNamespace).Get(c, schedulerDescriptor, metav1.GetOptions{})
	switch {
	case err == nil:
		report.Descriptor = "present"
		data, _, _ := unstructured.NestedStringMap(cm.Object, "data")
		report.Scheduler = data["scheduler"]
		if data["rootQueue"] != "" {
			report.RootQueue = data["rootQueue"]
		}
	case !apierrors.IsNotFound(err):
		report.Descriptor = "unreadable"
	}

	if team != "" {
		if root, err := dc.Resource(queueGVR).Get(c, report.RootQueue, metav1.GetOptions{}); err == nil {
			v := queueViewOf(root, report.RootQueue)
			report.Root = &v
		}
		for _, tier := range tiers {
			name := team + "-" + tier
			q, err := dc.Resource(queueGVR).Get(c, name, metav1.GetOptions{})
			switch {
			case err == nil:
				v := queueViewOf(q, report.RootQueue)
				if v.Parent != report.RootQueue {
					// A namesake outside the batch hierarchy is not the team's
					// leaf: batch runs are never bound to it (§4.3).
					report.Missing = append(report.Missing, missingQueue{Name: name, Reason: fmt.Sprintf("not a batch leaf: its parent is %q, not %s", dash(v.Parent), report.RootQueue)})
					continue
				}
				report.Queues = append(report.Queues, v)
			case apierrors.IsNotFound(err):
				report.Missing = append(report.Missing, missingQueue{Name: name, Reason: "not found: the Team composes it; its BatchQueuesReady condition says why it is not there"})
			case apierrors.IsForbidden(err):
				report.Missing = append(report.Missing, missingQueue{Name: name, Reason: "forbidden: the Team grants get on its queues to its own group"})
			default:
				return report, fmt.Errorf("reading queue %s: %w", name, err)
			}
		}
		return report, nil
	}

	list, err := dc.Resource(queueGVR).List(c, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsForbidden(err) {
			return report, fmt.Errorf("listing queues needs list on queues.scheduling.volcano.sh; a team member reads its own with --team <org>-<team>")
		}
		return report, fmt.Errorf("listing queues: %w", err)
	}
	for i := range list.Items {
		q := &list.Items[i]
		v := queueViewOf(q, report.RootQueue)
		switch {
		case q.GetName() == report.RootQueue:
			report.Root = &v
		case v.Parent == report.RootQueue:
			report.Queues = append(report.Queues, v)
		}
	}
	sort.SliceStable(report.Queues, func(i, j int) bool {
		a, b := report.Queues[i], report.Queues[j]
		if a.Team != b.Team {
			return a.Team < b.Team
		}
		if ra, rb := envRank(a.Tier), envRank(b.Tier); ra != rb {
			return ra < rb
		}
		return a.Name < b.Name
	})
	return report, nil
}

// queueViewOf reads one Queue. Every field is optional: a queue Volcano has
// not reconciled yet has no status, and shows zeros, not an error.
func queueViewOf(q *unstructured.Unstructured, root string) queueView {
	v := queueView{Name: q.GetName()}
	v.Parent, _, _ = unstructured.NestedString(q.Object, "spec", "parent")
	if v.Parent == root {
		for _, tier := range tiers {
			if team, ok := strings.CutSuffix(v.Name, "-"+tier); ok && team != "" {
				v.Team, v.Tier = team, tier
				break
			}
		}
	}
	v.State, _, _ = unstructured.NestedString(q.Object, "status", "state")
	if n, ok, _ := unstructured.NestedInt64(q.Object, "spec", "priority"); ok {
		v.Priority = &n
	}
	if n, ok, _ := unstructured.NestedInt64(q.Object, "spec", "weight"); ok {
		v.Weight = &n
	}
	if b, ok, _ := unstructured.NestedBool(q.Object, "spec", "reclaimable"); ok {
		v.Reclaimable = &b
	}
	v.Capability = quantities(q, "spec", "capability")
	v.Deserved = quantities(q, "spec", "deserved")
	v.Guarantee = quantities(q, "spec", "guarantee", "resource")
	v.Allocated = quantities(q, "status", "allocated")
	v.Pending = statusInt(q, "pending")
	v.Inqueue = statusInt(q, "inqueue")
	v.Running = statusInt(q, "running")
	v.Completed = statusInt(q, "completed")
	v.Saturated = v.Pending+v.Inqueue > 0 && atCapability(v.Allocated, v.Capability)
	return v
}

// quantities reads a resource list (int-or-string values) as strings.
func quantities(q *unstructured.Unstructured, path ...string) map[string]string {
	raw, ok, _ := unstructured.NestedMap(q.Object, path...)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		out[k] = fmt.Sprint(v)
	}
	return out
}

// atCapability says allocated has reached capability on a resource both name.
// A quantity that does not parse is not evidence of anything.
func atCapability(allocated, capability map[string]string) bool {
	for name, limit := range capability {
		used, ok := allocated[name]
		if !ok {
			continue
		}
		u, uerr := resource.ParseQuantity(used)
		l, lerr := resource.ParseQuantity(limit)
		if uerr == nil && lerr == nil && !l.IsZero() && u.Cmp(l) >= 0 {
			return true
		}
	}
	return false
}

func printQueues(r queueReport) {
	if !r.Volcano {
		helpers.Printf("%s\n", helpers.CreateMuted("Volcano is not installed on this cluster, so there are no batch queues: every run is placed by kube-scheduler on the native path."))
		return
	}
	if r.Descriptor == "absent" {
		helpers.Printf("%s\n", helpers.CreateWarning(fmt.Sprintf("Volcano is installed but ConfigMap %s/%s is absent, so BatchJobs still run on the native path and do not use these queues.", globals.AdharSystemNamespace, schedulerDescriptor)))
	}
	rows := r.Queues
	if r.Root != nil {
		rows = append([]queueView{*r.Root}, rows...)
	}
	switch {
	case len(rows) == 0 && len(r.Missing) > 0:
	case len(rows) == 0:
		helpers.Printf("%s\n", helpers.CreateMuted(fmt.Sprintf("No queue %s and no leaf under it yet.", r.RootQueue)))
	default:
		t := helpers.NewTable("QUEUE", "TIER", "STATE", "CPU", "MEMORY", "RUNNING", "WAITING", "PRIORITY")
		for _, v := range rows {
			tier := dash(v.Tier)
			if v.Name == r.RootQueue {
				tier = "ceiling"
			}
			t.Row(v.Name, tier, queueStateCell(v), usageCell(v, "cpu"), usageCell(v, "memory"),
				fmt.Sprint(v.Running), fmt.Sprint(v.Inqueue+v.Pending), int64Cell(v.Priority))
		}
		fmt.Println(t.Render())
		helpers.Printf("%s\n", helpers.CreateMuted("CPU and MEMORY are used/deserved/capability; RUNNING and WAITING count PodGroups (gangs), waiting = pending + inqueue."))
	}
	for _, m := range r.Missing {
		helpers.Printf("%s\n", helpers.CreateMuted(fmt.Sprintf("%s: %s", m.Name, m.Reason)))
	}
}

func queueStateCell(v queueView) string {
	switch {
	case v.Saturated:
		return helpers.StateDegraded("saturated")
	case v.State == "Open":
		return helpers.StateReady(v.State)
	case v.State == "Closed" || v.State == "Closing":
		return helpers.StateDisabled(v.State)
	default:
		return helpers.StateUnknown(dash(v.State))
	}
}

// usageCell is allocated/deserved/capability of one resource.
func usageCell(v queueView, name string) string {
	used := v.Allocated[name]
	if used == "" {
		used = "0"
	}
	return fmt.Sprintf("%s/%s/%s", used, dash(v.Deserved[name]), dash(v.Capability[name]))
}

func int64Cell(n *int64) string {
	if n == nil {
		return "—"
	}
	return fmt.Sprint(*n)
}
