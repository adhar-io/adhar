package autoscaler

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
)

// The batch node pool (design 0025 D14, §4.7 Phase 4, A4.x in §15): batch demand
// grows the POOL and never the general workers, a general pod never grows the
// pool, and an idle pool shrinks to its floor. Each rule is asserted against both
// decisions, because the failure that matters is the other group moving.

const capacityShort = "0/4 nodes are available: 1 node(s) had untolerated taint {node-role.kubernetes.io/control-plane: }, 3 Insufficient cpu."

// poolNode is a pool node as a compute-mode join registers it.
func poolNode(name string) corev1.Node {
	n := worker(name)
	n.Labels = map[string]string{globals.BatchNodePoolLabel: globals.BatchNodePoolValue}
	n.Spec.Taints = []corev1.Taint{{Key: globals.BatchNodePoolTaintKey, Value: globals.BatchNodePoolTaintValue, Effect: corev1.TaintEffectNoSchedule}}
	return n
}

// poolToleration is what adhar-batch-node-pool adds to a batch run's pod.
var poolToleration = corev1.Toleration{
	Key: globals.BatchNodePoolTaintKey, Operator: corev1.TolerationOpEqual, Value: globals.BatchNodePoolTaintValue, Effect: corev1.TaintEffectNoSchedule,
}

// waitingRun is a pending prod run that opted into scale-up and was admitted
// while the pool existed (so it carries the toleration).
func waitingRun(name string) corev1.Pod {
	p := pending(name, capacityShort)
	p.Namespace = "acme-payments-prod"
	p.Labels = map[string]string{workloadLabel: "batch", scaleUpLabel: "true", environmentLabel: "prod"}
	p.Spec.Tolerations = []corev1.Toleration{poolToleration}
	return p
}

func poolSpec(minNodes, maxNodes int32) v1alpha1.AutoscalingSpec {
	s := enabledSpec() // general workers 1..5
	s.BatchPool = &v1alpha1.BatchPoolSpec{MinNodes: minNodes, MaxNodes: maxNodes}
	return s
}

// fullCluster: two general workers and one pool node, every one of them full.
func fullCluster(spec v1alpha1.AutoscalingSpec, pods ...corev1.Pod) Snapshot {
	return Snapshot{
		Spec:  spec,
		Nodes: []corev1.Node{controlPlane(), worker("w1"), worker("w2"), poolNode("p1")},
		Pods: append([]corev1.Pod{
			placed("apps", "busy-1", "w1", 3000, 6), placed("apps", "busy-2", "w2", 3000, 6),
			placed("acme-payments-prod", "run-0", "p1", 3800, 7),
		}, pods...),
		Now: testNow,
	}
}

func TestBatchDemandGrowsThePoolNotTheGeneralWorkers(t *testing.T) {
	s := fullCluster(poolSpec(1, 3), waitingRun("nightly-1"))
	general, pool := Decide(s), DecidePool(s)
	if general.Action != ActionNone {
		t.Errorf("a waiting batch run grew the general workers: %s (%s)", general.Action, general.Reason)
	}
	if general.Workers != 2 {
		t.Errorf("general workers = %d, want 2: a pool node is not a general worker", general.Workers)
	}
	if pool.Action != ActionScaleUp || pool.ScaleUpBy != 1 || pool.Workers != 1 {
		t.Fatalf("pool: %s by %d from %d (%s), want a scale-up by 1 from 1", pool.Action, pool.ScaleUpBy, pool.Workers, pool.Reason)
	}
	if !strings.HasPrefix(pool.Reason, "batch pool: ") {
		t.Errorf("pool reason %q does not say it is the pool's", pool.Reason)
	}

	// Without a pool the same run grows the general workers, exactly as before
	// Phase 4 (and DecidePool says nothing).
	s.Spec = enabledSpec()
	if d := Decide(s); d.Action != ActionScaleUp {
		t.Errorf("without a pool an opted-in prod run must still buy a general worker: %s (%s)", d.Action, d.Reason)
	}
	if d := DecidePool(s); d.Action != ActionNone || d.Reason != "" {
		t.Errorf("no pool, yet a pool decision: %+v", d)
	}
}

func TestAGeneralPodNeverGrowsThePool(t *testing.T) {
	s := fullCluster(poolSpec(1, 3), pending("web-7", capacityShort))
	if d := DecidePool(s); d.Action != ActionNone {
		t.Errorf("a general pod grew the pool: %s (%s)", d.Action, d.Reason)
	}
	if d := Decide(s); d.Action != ActionScaleUp {
		t.Errorf("a general pod must still grow the general workers: %s (%s)", d.Action, d.Reason)
	}
	// Not even one that tolerates everything: the pool is for batch runs.
	anything := pending("tolerant", capacityShort)
	anything.Spec.Tolerations = []corev1.Toleration{{Operator: corev1.TolerationOpExists}}
	if d := DecidePool(fullCluster(poolSpec(1, 3), anything)); d.Action != ActionNone {
		t.Errorf("a non-batch pod that tolerates every taint grew the pool: %s (%s)", d.Action, d.Reason)
	}
}

// The Phase-1 rules stand on the pool: only a run that opted in may buy a pool
// node, and only one that can run there (it carries the pool's toleration).
func TestOnlyRunsThatMayBuyAndCanUseAPoolNodeGrowIt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*corev1.Pod)
		grows  bool
	}{
		{"an opted-in run", func(*corev1.Pod) {}, true},
		{"a run tolerating by Exists", func(p *corev1.Pod) {
			p.Spec.Tolerations = []corev1.Toleration{{Key: globals.BatchNodePoolTaintKey, Operator: corev1.TolerationOpExists}}
		}, true},
		{"a dev run (not opted in)", func(p *corev1.Pod) {
			delete(p.Labels, scaleUpLabel)
			p.Labels[environmentLabel] = "dev"
		}, false},
		{"a run admitted before the pool existed (no toleration)", func(p *corev1.Pod) { p.Spec.Tolerations = nil }, false},
		{"a run tolerating the pool's taint for another value", func(p *corev1.Pod) {
			p.Spec.Tolerations = []corev1.Toleration{{Key: globals.BatchNodePoolTaintKey, Operator: corev1.TolerationOpEqual, Value: "false", Effect: corev1.TaintEffectNoSchedule}}
		}, false},
		{"a run blocked by something a node cannot fix", func(p *corev1.Pod) {
			p.Status.Conditions[0].Message = "0/4 nodes are available: 4 node(s) had volume node affinity conflict."
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := waitingRun("nightly-1")
			tc.mutate(&run)
			s := fullCluster(poolSpec(1, 3), run)
			if got := DecidePool(s).Action == ActionScaleUp; got != tc.grows {
				t.Errorf("pool grows = %v, want %v (%s)", got, tc.grows, DecidePool(s).Reason)
			}
			if d := Decide(s); d.Action != ActionNone {
				t.Errorf("a batch run grew the general workers while a pool exists: %s (%s)", d.Action, d.Reason)
			}
		})
	}
}

// A Volcano gang that opted in is pool demand, sized the all-or-nothing way, and
// the pool's taint does not hide the room its own nodes have.
func TestAGangGrowsThePoolByWhatItNeeds(t *testing.T) {
	s := fullCluster(poolSpec(1, 4))
	s.Queues = batchQueues()
	s.Gangs = []Gang{prodGang("settlement", 2, 1500, 2)}
	if d := Decide(s); d.Action != ActionNone || d.PendingGangs != 0 {
		t.Errorf("a gang grew the general workers: %s (%s)", d.Action, d.Reason)
	}
	pool := DecidePool(s)
	if pool.Action != ActionScaleUp || pool.ScaleUpBy != 1 || pool.PendingGangs != 1 {
		t.Fatalf("pool: %s by %d for %d gang(s) (%s), want one node for one gang", pool.Action, pool.ScaleUpBy, pool.PendingGangs, pool.Reason)
	}

	// Room on an existing pool node counts although the node is tainted: with p1
	// empty, one of three 2500m members goes there and two nodes are bought, not
	// three.
	s.Pods = s.Pods[:2]
	s.Gangs = []Gang{prodGang("settlement", 3, 2500, 5)}
	if d := DecidePool(s); d.Action != ActionScaleUp || d.ScaleUpBy != 2 {
		t.Errorf("a gang one member of which fits the empty pool node: %s by %d (%s), want 2", d.Action, d.ScaleUpBy, d.Reason)
	}

	// maxNodes bounds it, all or nothing.
	s = fullCluster(poolSpec(1, 2))
	s.Queues = batchQueues()
	s.Gangs = []Gang{prodGang("settlement", 3, 2500, 5)}
	if d := DecidePool(s); d.Action != ActionNone || !strings.Contains(d.Reason, "not buying part of a gang") {
		t.Errorf("a gang needing more than maxNodes leaves was bought part of it: %s (%s)", d.Action, d.Reason)
	}
}

func TestThePoolStaysWithinItsBounds(t *testing.T) {
	s := fullCluster(poolSpec(1, 1), waitingRun("nightly-1"))
	if d := DecidePool(s); d.Action != ActionNone || !strings.Contains(d.Reason, "maxNodes=1 reached") {
		t.Errorf("pool at maxNodes: %s (%s)", d.Action, d.Reason)
	}
	// The general workers' ceiling does not count pool nodes: two general
	// workers under maxWorkers=2 is "reached" with or without a pool node.
	spec := poolSpec(1, 3)
	spec.MaxWorkers = 2
	if d := Decide(fullCluster(spec, pending("web-7", capacityShort))); !strings.Contains(d.Reason, "maxWorkers=2 reached") {
		t.Errorf("general ceiling: %s", d.Reason)
	}
	spec.MaxWorkers = 3
	if d := Decide(fullCluster(spec, pending("web-7", capacityShort))); d.Action != ActionScaleUp {
		t.Errorf("a pool node was counted against maxWorkers: %s (%s)", d.Action, d.Reason)
	}
}

// The pool keeps its own clocks: a general scale-up a minute ago does not make
// a waiting run wait, and the pool's own does.
func TestThePoolHasItsOwnCooldown(t *testing.T) {
	recent := metav1.NewTime(testNow.Add(-time.Minute))
	s := fullCluster(poolSpec(1, 3), waitingRun("nightly-1"))
	s.Status.LastScaleUp = &recent
	if d := DecidePool(s); d.Action != ActionScaleUp {
		t.Errorf("the general workers' cooldown held the pool: %s (%s)", d.Action, d.Reason)
	}
	s.Status.LastScaleUp = nil
	s.PoolStatus.LastScaleUp = &recent
	if d := DecidePool(s); d.Action != ActionNone || !strings.Contains(d.Reason, "cooldown") {
		t.Errorf("the pool ignored its own cooldown: %s (%s)", d.Action, d.Reason)
	}
}

// A pool kept busy by the runs it holds does not grow: a node is bought for a
// run that is waiting, never ahead of one (the general workers' proactive
// scale-up does not apply to the pool), and busy batch on a general worker does
// not grow the general workers either.
func TestABusyPoolDoesNotGrowAheadOfDemand(t *testing.T) {
	run := batchRun("run-1", "p1")
	run.Labels[scaleUpLabel] = "true"
	run.Spec.Containers[0].Resources.Requests = cpuMem(3900, 7)
	onGeneral := batchRun("run-2", "w1")
	onGeneral.Labels[scaleUpLabel] = "true"
	onGeneral.Spec.Containers[0].Resources.Requests = cpuMem(3900, 7)
	s := Snapshot{
		Spec:  poolSpec(1, 3),
		Nodes: []corev1.Node{controlPlane(), worker("w1"), worker("w2"), poolNode("p1")},
		Pods:  []corev1.Pod{run, onGeneral, placed("apps", "web", "w2", 3700, 7)},
		Now:   testNow,
	}
	if d := DecidePool(s); d.Action != ActionNone {
		t.Errorf("a busy pool with nothing waiting grew: %s (%s)", d.Action, d.Reason)
	}
	if d := Decide(s); d.Action != ActionNone {
		t.Errorf("batch on a general worker grew the general workers through utilization: %s (%s)", d.Action, d.Reason)
	}
	// Without the pool the same picture is the general workers' business again:
	// opted-in batch is real load that may buy a worker.
	s.Spec = enabledSpec()
	if d := Decide(s); d.Action != ActionScaleUp {
		t.Errorf("without a pool, opted-in batch at 95%% must still count: %s (%s)", d.Action, d.Reason)
	}
}

func TestAnIdlePoolShrinksToItsFloor(t *testing.T) {
	since := metav1.NewTime(testNow.Add(-30 * time.Minute))
	idle := func(minNodes int32, pool ...corev1.Node) Snapshot {
		return Snapshot{
			Spec:       poolSpec(minNodes, 4),
			PoolStatus: v1alpha1.BatchPoolStatus{UnderutilizedSince: &since},
			Nodes:      append([]corev1.Node{controlPlane(), worker("w1"), worker("w2")}, pool...),
			Pods: []corev1.Pod{
				placed("apps", "busy-1", "w1", 3000, 6), placed("apps", "busy-2", "w2", 3000, 6),
				placed("acme-payments-prod", "tail", "p1", 300, 1),
			},
			Now: testNow,
		}
	}
	d := DecidePool(idle(1, poolNode("p1"), poolNode("p2"), poolNode("p3")))
	if d.Action != ActionScaleDown || d.Node != "p2" {
		t.Fatalf("idle pool: %s %q (%s), want p2 (the emptiest) retired", d.Action, d.Node, d.Reason)
	}
	if d := DecidePool(idle(1, poolNode("p1"))); d.Action != ActionNone || !strings.Contains(d.Reason, "minNodes=1 reached") {
		t.Errorf("pool at its floor: %s (%s)", d.Action, d.Reason)
	}
	if d := DecidePool(idle(2, poolNode("p1"), poolNode("p2"))); d.Action != ActionNone {
		t.Errorf("pool shrank below minNodes=2: %s %s", d.Action, d.Node)
	}
	// A pool node is never the GENERAL workers' scale-down candidate, however
	// empty it is.
	s := idle(1, poolNode("p1"), poolNode("p2"))
	s.Pods = []corev1.Pod{placed("apps", "a", "w1", 100, 1), placed("apps", "b", "w2", 50, 1)}
	s.Status.UnderutilizedSince = &since
	if d := Decide(s); d.Action != ActionScaleDown || isPoolNode(nodeNamed(s.Nodes, d.Node)) {
		t.Errorf("general scale-down picked %q (%s)", d.Node, d.Reason)
	}
	// And a run that asked not to be evicted keeps its pool node.
	s = idle(1, poolNode("p1"), poolNode("p2"))
	hold := batchRun("long", "p2")
	hold.Spec.Containers[0].Resources.Requests = cpuMem(100, 1)
	s.Pods = append(s.Pods, hold)
	if d := DecidePool(s); d.Node == "p2" {
		t.Errorf("retired the pool node a run asked to keep: %s", d.Reason)
	}
}

func nodeNamed(nodes []corev1.Node, name string) corev1.Node {
	for _, n := range nodes {
		if n.Name == name {
			return n
		}
	}
	return corev1.Node{}
}

// The controller acts on the pool's decision, on the pool's node group, keeps
// the pool's status apart, and takes one cloud action per tick.
func TestATickGrowsThePoolForAWaitingRun(t *testing.T) {
	f := &fakeScaler{}
	cp, w1, p1 := controlPlane(), worker("w1"), poolNode("p1")
	busy := placed("apps", "busy", "w1", 3000, 6)
	inPool := placed("acme-payments-prod", "run-0", "p1", 3800, 7)
	run := waitingRun("nightly-1")
	spec := &v1alpha1.AutoscalingSpec{Enabled: true, MinWorkers: 1, MaxWorkers: 4,
		BatchPool: &v1alpha1.BatchPoolSpec{MinNodes: 1, MaxNodes: 3}}
	r := newReconciler(t, f, platformWith(spec), clusterSpecConfigMap(), &cp, &w1, &p1, &busy, &inPool, &run)
	tick(t, r)
	if len(f.upCalls) != 1 || f.lastGroup != globals.BatchNodeGroupName || f.lastDesired != 2 {
		t.Fatalf("scale-ups %v to %q x%d, want one to %q x2", f.upCalls, f.lastGroup, f.lastDesired, globals.BatchNodeGroupName)
	}
	st := loadPlatform(t, r).Status.Autoscaling
	if st == nil || st.BatchPool == nil || st.BatchPool.LastScaleUp == nil || st.BatchPool.Nodes != 1 ||
		!strings.HasPrefix(st.BatchPool.LastReason, "batch pool: ") {
		t.Fatalf("pool status %+v", st)
	}
	if st.LastScaleUp != nil || st.Workers != 1 {
		t.Errorf("the pool's scale-up moved the general workers' status: %+v", st)
	}

	// A general scale-up in the same tick goes first, and the pool waits a tick.
	f = &fakeScaler{}
	web := pending("web-7", capacityShort)
	r = newReconciler(t, f, platformWith(spec), clusterSpecConfigMap(), &cp, &w1, &p1, &busy, &inPool, &run, &web)
	tick(t, r)
	if len(f.upCalls) != 1 || f.lastGroup != "workers" {
		t.Fatalf("scale-ups %v (last %q), want exactly the general one", f.upCalls, f.lastGroup)
	}
	if st := loadPlatform(t, r).Status.Autoscaling; st.BatchPool == nil || !strings.Contains(st.BatchPool.LastReason, "deferred") {
		t.Errorf("pool status does not say it waited: %+v", st.BatchPool)
	}
}

// A pool that has lost every node (a node failed, or was cordoned) is rebuilt by
// the runs that wait for it, and those runs still never grow the general workers.
func TestAPoolWithNoNodeLeftIsRegrownByAWaitingRun(t *testing.T) {
	gone := poolNode("p1")
	gone.Spec.Unschedulable = true
	s := fullCluster(poolSpec(1, 3))
	s.Nodes = []corev1.Node{controlPlane(), worker("w1"), worker("w2"), gone}
	s.Pods = append(s.Pods[:2], waitingRun("nightly-1"))
	pool := DecidePool(s)
	if pool.Action != ActionScaleUp || pool.Workers != 0 || pool.ScaleUpBy != 1 {
		t.Errorf("empty pool with a waiting run: %s by %d from %d (%s), want one node", pool.Action, pool.ScaleUpBy, pool.Workers, pool.Reason)
	}
	if d := Decide(s); d.Action != ActionNone {
		t.Errorf("a waiting batch run grew the general workers because the pool is empty: %s (%s)", d.Action, d.Reason)
	}
}
