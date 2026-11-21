package get

import (
	"context"
	"fmt"
	"strings"
	"time"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NodeAutoscalingView is what `adhar get status` shows of the platform's own node
// autoscaler: the bounds it was given (spec.autoscaling) and what it last saw and
// did (status.autoscaling), for the general workers and, when the environment
// declares one, the batch node pool (design 0025 D14), which scales on its own
// clocks and is never counted among the workers.
type NodeAutoscalingView struct {
	Spec   *v1alpha1.AutoscalingSpec   `json:"spec,omitempty"`
	Status *v1alpha1.AutoscalingStatus `json:"status,omitempty"`
}

// collectAutoscaling reads the autoscaler's spec and status off the AdharPlatform.
// nil when there is no platform, or when the autoscaler was never configured and
// has never reported.
func collectAutoscaling(ctx context.Context) *NodeAutoscalingView {
	cl, err := getControllerRuntimeClient()
	if err != nil {
		return nil
	}
	platforms := &v1alpha1.AdharPlatformList{}
	if err := cl.List(ctx, platforms, client.InNamespace(globals.AdharSystemNamespace)); err != nil || len(platforms.Items) == 0 {
		return nil
	}
	p := platforms.Items[0]
	if p.Spec.Autoscaling == nil && p.Status.Autoscaling == nil {
		return nil
	}
	return &NodeAutoscalingView{Spec: p.Spec.Autoscaling, Status: p.Status.Autoscaling}
}

// autoscalingRows is one row per node group the autoscaler manages: GROUP, NODES,
// RANGE, SCALED UP, SCALED DOWN, IDLE SINCE, LAST DECISION. Times are relative to
// now. The batch pool's row appears when the spec declares a pool or the status
// still reports one.
func autoscalingRows(v *NodeAutoscalingView, now time.Time) [][]string {
	if v == nil {
		return nil
	}
	spec := v.Spec.WithDefaults() // nil-safe
	st := v.Status
	enabled := v.Spec != nil && v.Spec.Enabled
	if !enabled && st != nil {
		enabled = st.Enabled
	}

	workers, up, down, idle, reason := "-", "never", "never", "-", "no decision yet"
	if st != nil {
		workers = fmt.Sprintf("%d", st.Workers)
		up, down, idle = ago(st.LastScaleUp, now, "never"), ago(st.LastScaleDown, now, "never"), ago(st.UnderutilizedSince, now, "-")
		if st.LastReason != "" {
			reason = st.LastReason
		}
	}
	rng := "off"
	if enabled {
		rng = fmt.Sprintf("%d–%d", spec.MinWorkers, spec.MaxWorkers)
	}
	rows := [][]string{{spec.NodeGroup, workers, rng, up, down, idle, reason}}

	var pool *v1alpha1.BatchPoolSpec
	if v.Spec != nil && v.Spec.BatchPool != nil {
		p := v.Spec.BatchPool.WithDefaults()
		pool = &p
	}
	var pst *v1alpha1.BatchPoolStatus
	if st != nil {
		pst = st.BatchPool
	}
	if pool == nil && pst == nil {
		return rows
	}
	group, prng := v1alpha1.DefaultBatchPoolNodeGroup, "off"
	if pool != nil {
		group = pool.NodeGroup
		if enabled {
			prng = fmt.Sprintf("%d–%d", pool.MinNodes, pool.MaxNodes)
		}
	}
	nodes, pup, pdown, pidle, preason := "-", "never", "never", "-", "no decision yet"
	if pst != nil {
		nodes = fmt.Sprintf("%d", pst.Nodes)
		pup, pdown, pidle = ago(pst.LastScaleUp, now, "never"), ago(pst.LastScaleDown, now, "never"), ago(pst.UnderutilizedSince, now, "-")
		if pst.LastReason != "" {
			preason = pst.LastReason
		}
	}
	if pool == nil {
		preason = "no longer declared: " + preason
	}
	return append(rows, []string{group + " (batch pool)", nodes, prng, pup, pdown, pidle, preason})
}

func ago(t *metav1.Time, now time.Time, none string) string {
	if t == nil || t.IsZero() {
		return none
	}
	d := now.Sub(t.Time)
	if d < 0 {
		d = 0
	}
	return formatDuration(d) + " ago"
}

// renderAutoscaling is the section's table, nil-safe ("" when there is nothing).
func renderAutoscaling(v *NodeAutoscalingView, now time.Time) string {
	rows := autoscalingRows(v, now)
	if len(rows) == 0 {
		return ""
	}
	t := helpers.NewTable("GROUP", "NODES", "RANGE", "SCALED UP", "SCALED DOWN", "IDLE SINCE", "LAST DECISION").Nested().WithBudget(96)
	for _, r := range rows {
		t.Row(r...)
	}
	return strings.TrimRight(t.Render(), "\n")
}

// displayAutoscaling prints the "Node Autoscaling" section of `adhar get status`.
func displayAutoscaling(v *NodeAutoscalingView) {
	body := renderAutoscaling(v, time.Now())
	if body == "" {
		return
	}
	fmt.Printf("\n%s\n", helpers.TitleStyle.Render(helpers.IconCluster+" "+"Node Autoscaling"))
	fmt.Println(helpers.BorderStyle.Width(100).Render(body))
}
