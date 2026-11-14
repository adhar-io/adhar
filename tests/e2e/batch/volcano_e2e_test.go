//go:build e2e

// Test_BatchOnVolcano is the Phase-2 canary (design 0025 §15 Phase 2,
// PLAT-2.8): A2.1–A2.7 and A2.10 on a Kind cluster that runs Volcano. Opt-in
// (ADHAR_E2E_VOLCANO=1); without SKIP_UP the harness stages a copy of the stack
// with Volcano on and brings the platform up from it (A2.9). On a cluster
// without Volcano every subtest skips, saying so. The kit's side of the
// partitioned contract (§5.10: ADHAR_BATCH_ROLE=manager|worker|indexed, the JDBC
// partition channel, worker-only stop) arrives with KIT-2.1 and the template
// springboot-batch-partitioned with TPL-2.1; until then the partitioned
// subtests skip at their scaffold, and what they assert is the design's
// contract, not today's kit.
package batch

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"adhar-io/adhar/tests/e2e"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

var adharPlatformGVR = schema.GroupVersionResource{Group: "platform.adhar.io", Version: "v1alpha1", Resource: "adharplatforms"}

const prodNS = "prod"

type volcanoSuite struct {
	e   *env
	c   *Cluster
	ctx context.Context
	// ns is the suite's own namespace (no team labels): ballast lives here.
	ns string
	// leaves are dev/test/prod's queues, by namespace.
	leaves map[string]string
	conf   SchedulerConf
	// p1 is the Phase-1 suite over the same app, for its run helpers.
	p1 *suite
	// The partitioned app, once scaffolded.
	part        *RunTemplate
	partWorkers int
}

func Test_BatchOnVolcano(t *testing.T) {
	cfg, vc := settings(t)
	if !vc.Enabled {
		t.Skip("the Volcano canary is opt-in: ADHAR_E2E_VOLCANO=1 (make e2e-batch-volcano brings up Kind with Volcano)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Overall)
	defer cancel()
	e := platform(t)
	v := &volcanoSuite{e: e, c: e.c, ctx: ctx, ns: "batch-e2e-" + cfg.Tag, leaves: map[string]string{}}

	if missing := v.unserved(); len(missing) > 0 {
		if e.staged != "" {
			t.Fatalf("the harness brought the platform up with Volcano enabled (%s) and the cluster does not serve %v", e.staged, missing)
		}
		t.Skipf("the cluster has no Volcano (%v not served): run `make e2e-batch-volcano`, or point ADHAR_E2E_SKIP_UP at a cluster "+
			"with the volcano and adhar-batch-volcano elements enabled", missing)
	}
	if !t.Run("the cluster is the canary's: Kubernetes, Volcano, descriptor, classes and leaves (A2.9)", v.preflight) {
		t.FailNow()
	}
	require.NoError(t, v.ensureNamespace())
	defer v.deleteNamespace()

	t.Run("a run naming another team's queue is denied (A2.5)", v.crossTeam)
	t.Run("a gang with room for N members never starts N+1, nor half of them (A2.1, DEC-2)", v.gangAllOrNothing)
	t.Run("prod reclaims from a dev run over its share; the run stops gracefully and completes (A2.4, A2.7)", v.reclaim)
	t.Run("a Volcano Job claiming a BatchJob is held to its run template (§10.3)", v.forgedVolcanoRun)
	t.Run("a prod job larger than its leaf's deserved share is told so on its BatchJob (DEC-3)", v.prodLeafDoesNotFit)
	t.Run("a job larger than its leaf's capability is told so on its BatchJob, on any tier (DEC-3)", v.leafCapabilityExceeded)
	t.Run("one scale-up for an opted-in prod gang, none for dev (A2.6)", v.scaleUp)
	if t.Run("scaffold the partitioned job (TPL-2.1, KIT-2.1)", v.scaffoldPartitioned) && v.part != nil {
		t.Run("the manager completing ends the workers (A2.3)", v.managerEndsWorkers)
		t.Run("a killed worker restarts the gang and only unfinished partitions rerun (A2.2)", v.killWorker)
		t.Run("stop and resume a Volcano run (AbortJob/ResumeJob)", v.stopResume)
	}
	t.Run("removing the descriptor leaves every BatchJob native, with no edit (A2.10)", v.removeVolcano)
}

func (v *volcanoSuite) budget(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(v.ctx, d)
}

/* ─────────────── preflight ─────────────── */

// unserved are the Volcano kinds the API server does not serve.
func (v *volcanoSuite) unserved() []string {
	var missing []string
	for gv, kinds := range map[string][]string{
		"scheduling.volcano.sh/v1beta1": {"queues", "podgroups"},
		"batch.volcano.sh/v1alpha1":     {"jobs"},
	} {
		list, err := v.c.CS.Discovery().ServerResourcesForGroupVersion(gv)
		served := map[string]bool{}
		if err == nil {
			for _, r := range list.APIResources {
				served[r.Name] = true
			}
		}
		for _, k := range kinds {
			if !served[k] {
				missing = append(missing, k+"."+strings.Split(gv, "/")[0])
			}
		}
	}
	slices.Sort(missing)
	return missing
}

func (v *volcanoSuite) preflight(t *testing.T) {
	ctx := v.ctx
	// A2.9 / §4.6: the canary is the batch suite on Kubernetes 1.37 with Volcano.
	ver, err := v.c.CS.Discovery().ServerVersion()
	require.NoError(t, err)
	minor, _ := strconv.Atoi(strings.TrimRight(ver.Minor, "+"))
	require.GreaterOrEqual(t, minor, v.e.vc.MinKubeMinor, "Kubernetes %s.%s: the canary is the 1.%d gap (§4.6)", ver.Major, ver.Minor, v.e.vc.MinKubeMinor)
	t.Logf("Kubernetes %s, Volcano components in %s", ver.GitVersion, e2e.PlatformNamespace)

	for _, d := range []string{"volcano-scheduler", "volcano-controllers", "volcano-admission"} {
		dep, err := v.c.CS.AppsV1().Deployments(e2e.PlatformNamespace).Get(ctx, d, metav1.GetOptions{})
		require.NoError(t, err, "Deployment %s", d)
		require.Positive(t, dep.Status.AvailableReplicas, "Deployment %s has no available replica", d)
	}

	// D7 / FU-13: the descriptor is what puts the BatchJob composition on Volcano.
	desc, err := v.c.CS.CoreV1().ConfigMaps(e2e.PlatformNamespace).Get(ctx, DescriptorName, metav1.GetOptions{})
	require.NoError(t, err, "ConfigMap %s/%s, design D7: without it every BatchJob takes the native path (FU-13)", e2e.PlatformNamespace, DescriptorName)
	assert.Equal(t, "volcano", desc.Data["scheduler"], "descriptor scheduler")
	assert.Equal(t, RootQueue, desc.Data["rootQueue"], "descriptor rootQueue")
	for _, k := range []string{"version", "gang", "prodDeservedCPU", "prodDeservedMemory"} {
		assert.NotEmpty(t, desc.Data[k], "descriptor %s (D7)", k)
	}

	// DEC-1: Volcano runs have their own PreemptLowerPriority classes; the native ones stay Never.
	for name, want := range map[string]struct {
		value  int32
		policy corev1.PreemptionPolicy
	}{
		ClassVolcano: {-100, corev1.PreemptLowerPriority}, ClassVolcanoCritical: {0, corev1.PreemptLowerPriority},
		"adhar-batch": {-100, corev1.PreemptNever}, "adhar-batch-critical": {0, corev1.PreemptNever},
	} {
		pc, err := v.c.CS.SchedulingV1().PriorityClasses().Get(ctx, name, metav1.GetOptions{})
		if !assert.NoError(t, err, "PriorityClass %s (DEC-1, FU-4)", name) {
			continue
		}
		assert.Equal(t, want.value, pc.Value, "PriorityClass %s value", name)
		if assert.NotNil(t, pc.PreemptionPolicy, name) {
			assert.Equal(t, want.policy, *pc.PreemptionPolicy, "PriorityClass %s preemptionPolicy", name)
		}
	}

	cm, err := v.c.CS.CoreV1().ConfigMaps(e2e.PlatformNamespace).Get(ctx, SchedulerConfigMap, metav1.GetOptions{})
	require.NoError(t, err)
	v.conf, err = ParseSchedulerConf(cm.Data[schedulerConfKey])
	require.NoError(t, err)
	if f, ok := v.conf.OvercommitFactor(); ok {
		t.Logf("scheduler: actions %v, overcommit %.2f in tier %d (DEC-2)", v.conf.Actions, f, v.conf.Tier["overcommit"])
	} else {
		t.Logf("scheduler: actions %v, no overcommit plugin (DEC-2 not configured)", v.conf.Actions)
	}

	root, err := v.c.DC.Resource(queueGVR).Get(ctx, RootQueue, metav1.GetOptions{})
	require.NoError(t, err, "the root queue %s", RootQueue)
	rq := ParseQueue(root)
	t.Logf("root queue %s: capability %s deserved %s allocated %s", rq.Name, roomOf(rq.Capability), roomOf(rq.Deserved), roomOf(rq.Allocated))

	// Every pinned environment has its team's leaf, open, under the root, labelled
	// for the team — what the Team composition composes (§4.3) and the
	// Console and CLI read (FU-11: the Queue status fields).
	for _, ns := range []string{devNS, testNS, prodNS} {
		n, err := v.c.CS.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
		require.NoError(t, err, "namespace %s", ns)
		leaf, err := LeafQueue(n.Labels)
		require.NoError(t, err, "namespace %s", ns)
		v.leaves[ns] = leaf
		require.NoError(t, Eventually(ctx, 5*time.Second, "leaf queue "+leaf+" Open", func(ctx context.Context) (bool, string, error) {
			u, err := v.c.DC.Resource(queueGVR).Get(ctx, leaf, metav1.GetOptions{})
			if err != nil {
				return false, err.Error(), nil
			}
			q := ParseQueue(u)
			return q.State == "Open" && q.Parent == RootQueue, fmt.Sprintf("state %q parent %q", q.State, q.Parent), nil
		}))
		u, _ := v.c.DC.Resource(queueGVR).Get(ctx, leaf, metav1.GetOptions{})
		q := ParseQueue(u)
		status, _, _ := unstructured.NestedMap(u.Object, "status")
		t.Logf("leaf %s: reclaimable %v capability %s deserved %s; status keys %v", leaf, q.Reclaimable, roomOf(q.Capability), roomOf(q.Deserved), keysOf(status))
		assert.Equal(t, n.Labels[labelTeam], q.Labels[labelTeam], "leaf %s team label", leaf)
		tier := n.Labels[labelTier]
		if tier == "prod" {
			assert.Positive(t, roomOf(q.Deserved).MilliCPU, "the prod leaf's deserved share (D8, DEC-3)")
			if assert.NotNil(t, q.Reclaimable) {
				assert.False(t, *q.Reclaimable, "prod is not reclaimable")
			}
		} else if assert.NotNil(t, q.Reclaimable, leaf) {
			assert.True(t, *q.Reclaimable, "%s is reclaimable", leaf)
		}
	}
}

func keysOf(m map[string]interface{}) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func (v *volcanoSuite) ensureNamespace() error {
	_, err := v.c.CS.CoreV1().Namespaces().Create(v.ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: v.ns,
		Labels: map[string]string{labelE2E: "batch-volcano"}}}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func (v *volcanoSuite) deleteNamespace() {
	if v.e.cfg.Keep {
		return
	}
	_ = v.c.CS.CoreV1().Namespaces().Delete(context.Background(), v.ns, metav1.DeleteOptions{})
}

/* ─────────────── helpers ─────────────── */

func (v *volcanoSuite) room(ctx context.Context) (Room, map[string]Room, error) {
	nodes, err := v.c.CS.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return Room{}, nil, err
	}
	pods, err := v.c.CS.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return Room{}, nil, err
	}
	total, idle := ClusterRoom(nodes.Items, pods.Items)
	return total, idle, nil
}

func (v *volcanoSuite) queue(ctx context.Context, name string) (Queue, error) {
	u, err := v.c.DC.Resource(queueGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return Queue{}, err
	}
	return ParseQueue(u), nil
}

// freeIn is what a namespace's leaf and its quotas still admit.
func (v *volcanoSuite) freeIn(ctx context.Context, ns string) (Room, error) {
	q, err := v.queue(ctx, v.leaves[ns])
	if err != nil {
		return Room{}, err
	}
	quotas, err := v.c.CS.CoreV1().ResourceQuotas(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Room{}, err
	}
	return Tighter(QueueFree(q), QuotaFree(quotas.Items)), nil
}

// placeBallast fills the nodes as planned and waits until every ballast pod
// runs; it removes them when the subtest ends.
func (v *volcanoSuite) placeBallast(t *testing.T, what string, ballast map[string]Room) {
	t.Helper()
	var names []string
	i := 0
	for node, r := range ballast {
		if r.MilliCPU < 10 {
			continue
		}
		p := Ballast(fmt.Sprintf("%s-ballast-%d", what, i), v.ns, node, r)
		i++
		_, err := v.c.CS.CoreV1().Pods(v.ns).Create(v.ctx, p, metav1.CreateOptions{})
		require.NoError(t, err, "ballast on %s", node)
		names = append(names, p.Name)
	}
	// Removed before the next scenario measures its room.
	t.Cleanup(func() {
		bg := context.Background()
		for _, n := range names {
			_ = v.c.CS.CoreV1().Pods(v.ns).Delete(bg, n, metav1.DeleteOptions{GracePeriodSeconds: new(int64)})
		}
		ctx, cancel := context.WithTimeout(bg, 2*time.Minute)
		defer cancel()
		_ = Eventually(ctx, time.Second, "ballast gone", func(ctx context.Context) (bool, string, error) {
			for _, n := range names {
				if _, err := v.c.CS.CoreV1().Pods(v.ns).Get(ctx, n, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
					return false, n, nil
				}
			}
			return true, "", nil
		})
	})
	ctx, cancel := v.budget(5 * time.Minute)
	defer cancel()
	require.NoError(t, Eventually(ctx, 2*time.Second, "ballast running", func(ctx context.Context) (bool, string, error) {
		for _, n := range names {
			p, err := v.c.CS.CoreV1().Pods(v.ns).Get(ctx, n, metav1.GetOptions{})
			if err != nil || p.Status.Phase != corev1.PodRunning {
				return false, fmt.Sprintf("%s %v %v", n, p.Status.Phase, err), nil
			}
		}
		return true, "", nil
	}))
}

// createGang creates a synthetic gang and deletes it when the subtest ends.
func (v *volcanoSuite) createGang(t *testing.T, g GangSpec) {
	t.Helper()
	_, err := v.c.DC.Resource(vcJobGVR).Namespace(g.Namespace).Create(v.ctx, SyntheticGang(g), metav1.CreateOptions{})
	require.NoError(t, err, "Volcano Job %s/%s", g.Namespace, g.Name)
	t.Cleanup(func() { v.deleteGang(g.Namespace, g.Name) })
}

func (v *volcanoSuite) deleteGang(ns, name string) {
	bg := metav1.DeletePropagationBackground
	_ = v.c.DC.Resource(vcJobGVR).Namespace(ns).Delete(context.Background(), name, metav1.DeleteOptions{PropagationPolicy: &bg})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_ = Eventually(ctx, 2*time.Second, "gang "+name+" gone", func(ctx context.Context) (bool, string, error) {
		pods, err := v.gangPods(ctx, ns, name)
		return err == nil && len(pods) == 0, fmt.Sprintf("%d pods", len(pods)), nil
	})
}

func (v *volcanoSuite) gangPods(ctx context.Context, ns, name string) ([]corev1.Pod, error) {
	pods, err := v.c.CS.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: labelVCJobName + "=" + name})
	if err != nil {
		return nil, err
	}
	return pods.Items, nil
}

func (v *volcanoSuite) vcjob(ctx context.Context, ns, name string) (VolcanoJob, error) {
	u, err := v.c.DC.Resource(vcJobGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return VolcanoJob{}, err
	}
	return ParseVolcanoJob(u), nil
}

func (v *volcanoSuite) podGroupOf(ctx context.Context, job VolcanoJob) (PodGroup, bool, error) {
	list, err := v.c.DC.Resource(podGroupGVR).Namespace(job.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return PodGroup{}, false, err
	}
	var groups []PodGroup
	for i := range list.Items {
		groups = append(groups, ParsePodGroup(&list.Items[i]))
	}
	g, ok := PodGroupOf(job, groups)
	return g, ok, nil
}

// waitGangRunning waits for every member of a gang to run at once.
func (v *volcanoSuite) waitGangRunning(ctx context.Context, ns, name string, members int) error {
	return Eventually(ctx, 3*time.Second, "gang "+ns+"/"+name+" running", func(ctx context.Context) (bool, string, error) {
		pods, err := v.gangPods(ctx, ns, name)
		if err != nil {
			return false, err.Error(), nil
		}
		running := 0
		for _, p := range pods {
			if p.Status.Phase == corev1.PodRunning {
				running++
			}
		}
		why := fmt.Sprintf("%d/%d running", running, members)
		if j, err := v.vcjob(ctx, ns, name); err == nil {
			if g, ok, _ := v.podGroupOf(ctx, j); ok {
				msg, _ := g.Unschedulable()
				why += fmt.Sprintf("; job %s, gang %s %s", j.Phase, g.Phase, msg)
			}
		}
		return running == members, why, nil
	})
}

func (v *volcanoSuite) describeGang(ctx context.Context, ns, name string) string {
	var b strings.Builder
	if j, err := v.vcjob(ctx, ns, name); err == nil {
		fmt.Fprintf(&b, "\nVolcano Job %s/%s: %+v", ns, name, j)
		if g, ok, _ := v.podGroupOf(ctx, j); ok {
			fmt.Fprintf(&b, "\n  PodGroup %s phase %s min %d resources %s conditions %+v", g.Name, g.Phase, g.MinMember, roomOf(g.MinResources), g.Conditions)
		}
	}
	pods, _ := v.gangPods(ctx, ns, name)
	for _, p := range pods {
		fmt.Fprintf(&b, "\n  pod %s %s node %q", p.Name, p.Status.Phase, p.Spec.NodeName)
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status != corev1.ConditionTrue {
				fmt.Fprintf(&b, " (%s: %s)", c.Reason, c.Message)
			}
		}
	}
	return b.String()
}

// events are the cluster's events since a time.
func (v *volcanoSuite) events(ctx context.Context, since time.Time) []corev1.Event {
	list, err := v.c.CS.CoreV1().Events("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}
	var out []corev1.Event
	for _, e := range list.Items {
		at := e.LastTimestamp.Time
		if at.IsZero() {
			at = e.EventTime.Time
		}
		if !at.Before(since.Add(-time.Second)) {
			out = append(out, e)
		}
	}
	return out
}

func (v *volcanoSuite) dryRunCreate(ctx context.Context, gvr schema.GroupVersionResource, obj *unstructured.Unstructured) error {
	_, err := v.c.DC.Resource(gvr).Namespace(obj.GetNamespace()).Create(ctx, obj, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
	return err
}

/* ─────────────── A2.5: another team's queue ─────────────── */

// crossTeam: a team's namespace may not name another team's leaf, whether a
// person writes a Volcano Job, a PodGroup or a pod. Team B is a real Team XR,
// so its leaves exist and are its own (§4.3): the refusal is the binding
// policy's, not a missing queue. Admission is exercised with server dry-run —
// Kyverno answers dry-run requests (its webhooks have no side effects) — so a
// wrongly admitted object is never created.
func (v *volcanoSuite) crossTeam(t *testing.T) {
	ctx := v.ctx
	devNs, err := v.c.CS.CoreV1().Namespaces().Get(ctx, devNS, metav1.GetOptions{})
	require.NoError(t, err)
	org := devNs.Labels[labelOrganisation]
	teamB := "e2e" + v.e.cfg.Tag
	xr := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "platform.adhar.io/v1alpha1", "kind": "Team",
		"metadata": map[string]interface{}{"name": org + "-" + teamB, "namespace": e2e.PlatformNamespace, "labels": map[string]interface{}{labelE2E: "batch-volcano"}},
		"spec": map[string]interface{}{
			"crossplane": map[string]interface{}{"compositionSelector": map[string]interface{}{"matchLabels": map[string]interface{}{"provider": "local"}}},
			"parameters": map[string]interface{}{"name": teamB, "organisation": org, "displayName": "batch e2e team B", "giteaPermission": "read"},
		},
	}}
	_, err = v.c.DC.Resource(teamGVR).Namespace(e2e.PlatformNamespace).Create(ctx, xr, metav1.CreateOptions{})
	require.NoError(t, err, "Team %s", xr.GetName())
	t.Cleanup(func() {
		bg := context.Background()
		_ = v.c.DC.Resource(teamGVR).Namespace(e2e.PlatformNamespace).Delete(bg, xr.GetName(), metav1.DeleteOptions{})
		// The leaves are composed with deletionPolicy Orphan: the queues a team
		// held outlive it, so the suite removes team B's itself.
		_ = v.c.DC.Resource(queueGVR).DeleteCollection(bg, metav1.DeleteOptions{}, metav1.ListOptions{LabelSelector: labelTeam + "=" + teamB})
	})
	leafB := org + "-" + teamB + "-dev"
	bctx, cancel := v.budget(v.e.vc.Scenario)
	defer cancel()
	require.NoError(t, Eventually(bctx, 5*time.Second, "team B's leaf "+leafB, func(ctx context.Context) (bool, string, error) {
		q, err := v.queue(ctx, leafB)
		if err != nil {
			team, _ := v.c.DC.Resource(teamGVR).Namespace(e2e.PlatformNamespace).Get(ctx, xr.GetName(), metav1.GetOptions{})
			_, why := ConditionTrue(team, "BatchQueuesReady")
			return false, err.Error() + "; " + why, nil
		}
		return q.State == "Open" && q.Labels[labelTeam] == teamB, fmt.Sprintf("state %s labels %v", q.State, q.Labels), nil
	}))

	member := Room{MilliCPU: 50, Memory: memberMemory}
	own := SyntheticGang(GangSpec{Name: gangName(v.e.cfg.Tag, "own"), Namespace: devNS, Queue: v.leaves[devNS], PriorityClass: ClassVolcano, Members: 1, Member: member})
	require.NoError(t, v.dryRunCreate(ctx, vcJobGVR, own), "the team's own leaf must be admitted, or every denial below is meaningless")

	denied := func(what string, gvr schema.GroupVersionResource, obj *unstructured.Unstructured) {
		err := v.dryRunCreate(ctx, gvr, obj)
		if assert.Error(t, err, "%s naming team B's queue %s was admitted in team A's namespace", what, leafB) {
			// FU-11: the refusal's exact text, recorded live.
			t.Logf("%s refused: %v", what, err)
			assert.Contains(t, err.Error(), "adhar-batch-queue-binding", "%s was refused by something other than the binding policy", what)
		}
	}
	foreign := SyntheticGang(GangSpec{Name: gangName(v.e.cfg.Tag, "foreign"), Namespace: devNS, Queue: leafB, PriorityClass: ClassVolcano, Members: 1, Member: member})
	denied("a Volcano Job", vcJobGVR, foreign)
	denied("a PodGroup", podGroupGVR, &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "scheduling.volcano.sh/v1beta1", "kind": "PodGroup",
		"metadata": map[string]interface{}{"name": gangName(v.e.cfg.Tag, "foreign-pg"), "namespace": devNS},
		"spec":     map[string]interface{}{"queue": leafB, "minMember": int64(1)},
	}})
	pod := Sentinel(gangName(v.e.cfg.Tag, "foreign-pod"), devNS, ClassVolcano, "")
	pod.Spec.SchedulerName = VolcanoScheduler
	pod.Annotations = map[string]string{annotationQueueName: leafB}
	raw, err := json.Marshal(pod)
	require.NoError(t, err)
	podObj := &unstructured.Unstructured{}
	require.NoError(t, podObj.UnmarshalJSON(raw))
	podObj.SetAPIVersion("v1")
	podObj.SetKind("Pod")
	denied("a pod", schema.GroupVersionResource{Version: "v1", Resource: "pods"}, podObj)
	// The prod leaf is the same team's, and still not dev's.
	prodFromDev := SyntheticGang(GangSpec{Name: gangName(v.e.cfg.Tag, "prod-from-dev"), Namespace: devNS, Queue: v.leaves[prodNS], PriorityClass: ClassVolcano, Members: 1, Member: member})
	denied("a dev Volcano Job on the team's prod leaf", vcJobGVR, prodFromDev)
}

/* ─────────────── A2.1: all or nothing ─────────────── */

// gangAllOrNothing leaves room for exactly N members on one node (every other
// node filled) and shows that a gang of N+1 never binds a member, while a gang
// of N in the same room starts — the proof that the room was there.
func (v *volcanoSuite) gangAllOrNothing(t *testing.T) {
	ctx := v.ctx
	n := v.e.vc.Gang
	total, idle, err := v.room(ctx)
	require.NoError(t, err)
	node, nodeIdle, ok := RoomiestNode(idle)
	require.True(t, ok, "no schedulable node")
	free, err := v.freeIn(ctx, devNS)
	require.NoError(t, err)
	plan, err := PlanGangFit(nodeIdle, free, n)
	require.NoError(t, err)
	ballast, err := BallastFor(idle, node, plan.Room)
	require.NoError(t, err)
	t.Logf("node %s idle %s; dev free %s; room %s for %d members of %s", node, nodeIdle, free, plan.Room, n, plan.Member)
	v.placeBallast(t, "fit", ballast)

	_, after, err := v.room(ctx)
	require.NoError(t, err)
	require.InDelta(t, plan.Room.MilliCPU, after[node].MilliCPU, float64(plan.Member.MilliCPU)/2,
		"the platform moved by more than half a member while the ballast was placed (room now %s); rerun on a quieter cluster", after[node])

	over := GangSpec{Name: gangName(v.e.cfg.Tag, "over"), Namespace: devNS, Queue: v.leaves[devNS], PriorityClass: ClassVolcano, Members: n + 1, Member: plan.Member}
	v.createGang(t, over)
	factor, overcommit := v.conf.OvercommitFactor()
	sawCapacity, phases := false, map[string]bool{}
	hold, cancel := v.budget(v.e.vc.Hold)
	defer cancel()
	for hold.Err() == nil {
		pods, err := v.gangPods(ctx, devNS, over.Name)
		require.NoError(t, err)
		for _, p := range pods {
			require.Empty(t, p.Spec.NodeName, "member %s of the %d-gang was bound with room for %d (A2.1)%s", p.Name, n+1, n, v.describeGang(ctx, devNS, over.Name))
			require.NotEqual(t, corev1.PodRunning, p.Status.Phase)
			for _, c := range p.Status.Conditions {
				if c.Type == corev1.PodScheduled && MentionsCapacity(c.Message) {
					sawCapacity = true
				}
			}
		}
		if j, err := v.vcjob(ctx, devNS, over.Name); err == nil {
			if g, ok, _ := v.podGroupOf(ctx, j); ok {
				phases[g.Phase] = true
				require.True(t, g.Unstarted(), "the %d-gang's PodGroup is %s with room for %d", n+1, g.Phase, n)
				if msg, ok := g.Unschedulable(); ok && MentionsCapacity(msg) {
					sawCapacity = true
				}
				// DEC-2: a gang Volcano has not enqueued has no pods, so it holds nothing.
				if g.Phase == "Pending" {
					require.Empty(t, pods, "a Pending gang has pods holding quota (DEC-2)")
				}
			}
		}
		select {
		case <-hold.Done():
		case <-time.After(5 * time.Second):
		}
	}
	t.Logf("the %d-gang stayed unstarted for %s; its PodGroup was %v (FU-11)%s", n+1, v.e.vc.Hold, keysOfBool(phases), v.describeGang(ctx, devNS, over.Name))
	// The node autoscaler buys a worker for a gang only when Volcano says it is
	// short of capacity in words it knows (§4.7, A2.6).
	assert.True(t, sawCapacity, "Volcano never said the gang was short of capacity in the autoscaler's words %v", CapacityMarkers)
	if overcommit {
		_, nowIdle, _ := v.room(ctx)
		var idleSum int64
		for _, r := range nowIdle {
			idleSum += r.MilliCPU
		}
		used := total.MilliCPU - idleSum
		if room := int64(factor*float64(total.MilliCPU)) - used; int64(n+1)*plan.Member.MilliCPU > room {
			assert.True(t, phases["Pending"] && !phases["Inqueue"], "with overcommit %.2f the gang exceeds the enqueue room %dm and must stay Pending (DEC-2)", factor, room)
		}
	}
	v.deleteGang(devNS, over.Name)

	fits := GangSpec{Name: gangName(v.e.cfg.Tag, "fits"), Namespace: devNS, Queue: v.leaves[devNS], PriorityClass: ClassVolcano, Members: n, Member: plan.Member}
	v.createGang(t, fits)
	rctx, rcancel := v.budget(v.e.vc.Scenario)
	defer rcancel()
	require.NoError(t, v.waitGangRunning(rctx, devNS, fits.Name, n),
		"a gang of %d did not start in the room the %d-gang was refused: the room was not there%s", n, n+1, v.describeGang(ctx, devNS, fits.Name))
	if j, err := v.vcjob(ctx, devNS, fits.Name); err == nil {
		if g, ok, _ := v.podGroupOf(ctx, j); ok {
			t.Logf("PodGroup of Volcano Job %s is named %s (FU-11: <job>-<uid> or podgroup-<uid>)", fits.Name, g.Name)
		}
	}
	if pods, _ := v.gangPods(ctx, devNS, fits.Name); len(pods) > 0 {
		idx, where, ok := TaskIndex(pods[0])
		t.Logf("volcano.sh/task-index on a member: %q as %s (present %v) (FU-11)", idx, where, ok)
	}
}

func keysOfBool(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

/* ─────────────── A2.4 + A2.7: reclaim ─────────────── */

// phaseOne is the Phase-1 suite over the shared app, whose dev runs single
// mode on Volcano once the descriptor exists (§6.4).
func (v *volcanoSuite) phaseOne(t *testing.T) *suite {
	t.Helper()
	if v.p1 != nil {
		return v.p1
	}
	tpl, err := ensureApp(t, v.e, phaseOneApp(v.e.cfg))
	require.NoError(t, err, "the Phase-1 application")
	v.p1 = &suite{cfg: v.e.cfg, c: v.c, ctx: v.ctx, tpl: tpl}
	if p := CheckVolcanoSingle(tpl, v.leaves[devNS]); len(p) > 0 {
		t.Fatalf("dev's run template is not single mode on Volcano (§6.4, FU-3e): %v", p)
	}
	return v.p1
}

// reclaim: a dev run (the Phase-1 job, single mode on Volcano) and a dev
// filler hold all the room on one node, over dev's share (dev deserves
// nothing); a prod gang within prod's deserved share needs more than the room
// plus the filler. Volcano must reclaim the filler and the dev run, the run
// must stop gracefully (its execution STOPPED, §5.9) and complete once prod is
// done. Throughout, the pods Volcano did not schedule — ballast, a native
// sentinel at the lowest class on the cluster, and an annotated sentinel (the
// stage-1 edge ST-2) — are never its victims (A2.7).
func (v *volcanoSuite) reclaim(t *testing.T) {
	ctx := v.ctx
	s := v.phaseOne(t)
	if !t.Run("seed", s.seed) {
		t.FailNow()
	}
	values := dbValues(s.cfg.Date(20))
	name, caught := s.startAndCatch(t, values)
	runPod := s.runningPod(t, name)
	require.Equal(t, VolcanoScheduler, runPod.Spec.SchedulerName, "the dev run is scheduled by Volcano (§6.4)")
	app := PodRequests(runPod)

	// A2.7's sentinels, placed before the room is measured.
	native := Sentinel(gangName(v.e.cfg.Tag, "sentinel"), devNS, "adhar-batch", "")
	_, err := v.c.CS.CoreV1().Pods(devNS).Create(ctx, native, metav1.CreateOptions{})
	require.NoError(t, err)
	sentinels := []string{native.Name}
	t.Cleanup(func() {
		_ = v.c.CS.CoreV1().Pods(devNS).Delete(context.Background(), native.Name, metav1.DeleteOptions{})
	})
	edge := Sentinel(gangName(v.e.cfg.Tag, "sentinel-annotated"), devNS, "adhar-batch", "podgroup-"+v.e.cfg.Tag)
	if _, err := v.c.CS.CoreV1().Pods(devNS).Create(ctx, edge, metav1.CreateOptions{}); err != nil {
		t.Logf("a default-scheduler pod with %s was refused at admission (ST-2): %v", annotationGroupName, err)
	} else {
		t.Logf("ST-2 is not enforced: a default-scheduler pod with %s was admitted; it is a sentinel too", annotationGroupName)
		sentinels = append(sentinels, edge.Name)
		t.Cleanup(func() {
			_ = v.c.CS.CoreV1().Pods(devNS).Delete(context.Background(), edge.Name, metav1.DeleteOptions{})
		})
	}
	sctx, scancel := v.budget(3 * time.Minute)
	require.NoError(t, Eventually(sctx, 2*time.Second, "sentinels running", func(ctx context.Context) (bool, string, error) {
		for _, n := range sentinels {
			if p, err := v.c.CS.CoreV1().Pods(devNS).Get(ctx, n, metav1.GetOptions{}); err != nil || p.Status.Phase != corev1.PodRunning {
				return false, n + " not running", nil
			}
		}
		return true, "", nil
	}))
	scancel()

	prodQ, err := v.queue(ctx, v.leaves[prodNS])
	require.NoError(t, err)
	prodFree, err := v.freeIn(ctx, prodNS)
	require.NoError(t, err)
	prodRoom := Tighter(roomOf(prodQ.Deserved), prodFree)
	_, idle, err := v.room(ctx)
	require.NoError(t, err)
	plan, err := PlanReclaim(idle[runPod.Spec.NodeName], app, prodRoom)
	require.NoError(t, err, "prod deserved %s, prod free %s", roomOf(prodQ.Deserved), prodFree)
	devFree, err := v.freeIn(ctx, devNS)
	require.NoError(t, err)
	if devFree.MilliCPU > 0 {
		require.GreaterOrEqual(t, devFree.MilliCPU, plan.Filler.MilliCPU, "dev's leaf or quota cannot take the filler")
	}
	ballast, err := BallastFor(idle, runPod.Spec.NodeName, Room{MilliCPU: plan.Room.MilliCPU - app.MilliCPU})
	require.NoError(t, err)
	t.Logf("node %s: room %s = run %s + filler %s + spare %s; prod gang 2 x %s (prod room %s)",
		runPod.Spec.NodeName, plan.Room, plan.App, plan.Filler, plan.Spare, plan.ProdMember, prodRoom)
	v.placeBallast(t, "reclaim", ballast)

	filler := GangSpec{Name: gangName(v.e.cfg.Tag, "filler"), Namespace: devNS, Queue: v.leaves[devNS], PriorityClass: ClassVolcano, Members: 1, Member: plan.Filler}
	v.createGang(t, filler)
	fctx, fcancel := v.budget(5 * time.Minute)
	require.NoError(t, v.waitGangRunning(fctx, devNS, filler.Name, 1), v.describeGang(ctx, devNS, filler.Name))
	fcancel()

	pods, err := v.c.CS.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	before := Snapshot(pods.Items)
	keep := map[string]types.UID{}
	for _, p := range pods.Items {
		if p.DeletionTimestamp == nil && (p.Labels[labelE2E] == "ballast" || p.Labels[labelE2E] == "sentinel") {
			keep[p.Namespace+"/"+p.Name] = p.UID
		}
	}
	started := time.Now()

	prod := GangSpec{Name: gangName(v.e.cfg.Tag, "prod"), Namespace: prodNS, Queue: v.leaves[prodNS], PriorityClass: ClassVolcano,
		Members: 2, Member: plan.ProdMember, Seconds: 90}
	v.createGang(t, prod)
	pctx, pcancel := v.budget(v.e.vc.Scenario)
	defer pcancel()
	require.NoError(t, v.waitGangRunning(pctx, prodNS, prod.Name, 2),
		"prod never got its deserved share back from dev (A2.4)%s%s", v.describeGang(ctx, prodNS, prod.Name), v.describeGang(ctx, devNS, filler.Name))
	t.Logf("prod ran %s after it asked", time.Since(started).Round(time.Second))

	// The dev run's pod was taken, and its execution stopped rather than died.
	gone, err := v.c.CS.CoreV1().Pods(devNS).Get(ctx, runPod.Name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err) || gone.DeletionTimestamp != nil || gone.Status.Phase == corev1.PodFailed,
		"the dev run's pod %s survived a reclaim that needed its CPU", runPod.Name)
	identity := s.tpl.Identity(values)
	stopCtx, stopCancel := v.budget(5 * time.Minute)
	require.NoError(t, Eventually(stopCtx, 2*time.Second, "the reclaimed execution to be STOPPED (§5.9)", func(ctx context.Context) (bool, string, error) {
		l, err := s.c.Ledger(ctx, devNS, s.cfg.App, identity)
		if err != nil {
			return false, err.Error(), nil
		}
		return l.StatusOf(caught.ID) == "STOPPED", "execution " + strconv.FormatInt(caught.ID, 10) + " " + l.StatusOf(caught.ID), nil
	}), s.c.DescribeRun(ctx, devNS, name))
	stopCancel()

	// ...and later completes, once prod's gang is done. The filler is removed
	// first: Volcano may have needed only the run's CPU, and a filler that
	// outlived the reclaim would keep the run's replacement out for good.
	v.deleteGang(devNS, filler.Name)
	rctx, rcancel := v.budget(s.cfg.Run + 5*time.Minute)
	defer rcancel()
	job, err := s.c.WaitRunFinished(rctx, devNS, name)
	require.NoError(t, err, s.c.DescribeRun(ctx, devNS, name))
	cond, _ := Finished(job)
	require.Equal(t, "Complete", cond, s.c.DescribeRun(ctx, devNS, name))
	assert.LessOrEqual(t, job.Status.Failed, int32(1), "a reclaim costs the run at most one counted failure")
	t.Logf("the reclaimed pod was counted %d time(s) against backoffLimit (0: Volcano's eviction carried DisruptionTarget) (FU-11)", job.Status.Failed)
	l, err := s.c.Ledger(ctx, devNS, s.cfg.App, identity)
	require.NoError(t, err)
	require.NoError(t, l.CheckExactlyOnce(int64(s.cfg.Items)), "%+v", l)

	// A2.7: nothing Volcano did not schedule was taken.
	for key, uid := range keep {
		ns, n, _ := strings.Cut(key, "/")
		p, err := v.c.CS.CoreV1().Pods(ns).Get(ctx, n, metav1.GetOptions{})
		if assert.NoError(t, err, "%s, which Volcano did not schedule, is gone (A2.7)", key) {
			assert.Equal(t, uid, p.UID, "%s was replaced", key)
			assert.Nil(t, p.DeletionTimestamp, "%s is being deleted (A2.7)", key)
		}
	}
	victims := VolcanoEvictions(v.events(ctx, started))
	t.Logf("Volcano wrote %d eviction event(s) (FU-11: their wording)", len(victims))
	for _, e := range victims {
		t.Logf("  %s %s/%s %s: %s", e.Reason, e.InvolvedObject.Namespace, e.InvolvedObject.Name, e.Source.Component+e.ReportingController, e.Message)
	}
	assert.Empty(t, EvictedNotScheduled(v.events(ctx, started), before), "Volcano evicted pods it did not schedule (A2.7)")
}

/* ─────────────── §10.3: run integrity on Volcano ─────────────── */

// forgedVolcanoRun: run integrity covers jobs.batch.volcano.sh as well as
// jobs.batch (design §10.3) — the Volcano half is its own fail-closed policy,
// adhar-batch-run-integrity-volcano (core/adhar-batch/manifests/volcano/), whose
// name starts with the native one's, so the assertion below matches either half.
// A person who may create Volcano Jobs must
// not start a "run" of a BatchJob that is not its template. The gang below
// claims the Phase-1 application but carries a busybox sleeper, so it is
// refused — by the integrity policy, not by the queue binding (its queue is
// the namespace's own leaf). Server dry-run: nothing is created either way.
func (v *volcanoSuite) forgedVolcanoRun(t *testing.T) {
	v.phaseOne(t)
	forged := SyntheticGang(GangSpec{Name: gangName(v.e.cfg.Tag, "forged"), Namespace: devNS, Queue: v.leaves[devNS], PriorityClass: ClassVolcano,
		Members: 1, Member: Room{MilliCPU: 50, Memory: memberMemory},
		Labels: map[string]string{labelBatchJob: v.e.cfg.App, labelWorkload: "batch"}})
	err := v.dryRunCreate(v.ctx, vcJobGVR, forged)
	if assert.Error(t, err, "a Volcano Job labelled %s=%s that is not its run template was admitted (§10.3)", labelBatchJob, v.e.cfg.App) {
		t.Logf("forged Volcano run refused: %v", err)
		assert.Contains(t, err.Error(), "adhar-batch-run-integrity", "the forged Volcano run was refused by something other than run integrity")
	}
}

/* ─────────────── DEC-3: a job its leaf cannot hold ─────────────── */

// prodLeafDoesNotFit: a prod gang larger than the prod leaf's deserved share
// in every dimension can only start on idle room and never reclaims, so the
// BatchJob says so (QueueFits False, ExceedsDeserved) instead of the team
// finding out from a run that waits all night (DEC-3). The gang stays within
// the leaf's capability, which the composition checks first. The XR composes
// a run template and no pods, so nothing is scheduled.
func (v *volcanoSuite) prodLeafDoesNotFit(t *testing.T) {
	q, err := v.queue(v.ctx, v.leaves[prodNS])
	require.NoError(t, err)
	run, err := RunOverDeserved(q.Deserved, q.Capability)
	require.NoError(t, err, "prod leaf %s (deserved %v, capability %v)", q.Name, q.Deserved, q.Capability)
	status, reason, message := v.oversizeQueueFits(t, prodNS, "oversize", run)
	assert.Equal(t, "False", status, "a gang of %s over the prod leaf's deserved share %v fits it?", run.Ask(), q.Deserved)
	assert.Equal(t, "ExceedsDeserved", reason)
	assert.Contains(t, message, "globalSettings.batch", "the warning names the knob that raises the share")
}

// leafCapabilityExceeded: a gang larger than its leaf's capability (the batch
// ceiling, or the team's allowance) is never enqueued on ANY tier, so a dev
// BatchJob asking for it says ExceedsCapability rather than Fits and leaves
// the team to learn it from BatchGangQueueRejected half an hour into a run.
func (v *volcanoSuite) leafCapabilityExceeded(t *testing.T) {
	q, err := v.queue(v.ctx, v.leaves[devNS])
	require.NoError(t, err)
	run, err := RunOverCapability(q.Capability)
	require.NoError(t, err, "dev leaf %s", q.Name)
	status, reason, message := v.oversizeQueueFits(t, devNS, "overcap", run)
	assert.Equal(t, "False", status, "a gang of %s over the dev leaf's capability %v fits it?", run.Ask(), q.Capability)
	assert.Equal(t, "ExceedsCapability", reason)
	assert.Contains(t, message, q.Name, "the warning names the leaf")
	assert.Contains(t, message, "globalSettings.batch", "the warning names the knob that raises the ceiling")
}

// oversizeQueueFits creates a partitioned BatchJob of run's size in ns and
// returns its QueueFits condition. It is deleted when the subtest ends.
func (v *volcanoSuite) oversizeQueueFits(t *testing.T, ns, suffix string, run SizedRun) (status, reason, message string) {
	ctx := v.ctx
	name := gangName(v.e.cfg.Tag, suffix)
	bj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "platform.adhar.io/v1alpha1", "kind": "BatchJob",
		"metadata": map[string]interface{}{"name": name, "namespace": ns, "labels": map[string]interface{}{labelE2E: "batch-volcano"}},
		"spec": map[string]interface{}{"parameters": map[string]interface{}{
			"job": "oversizeJob", "image": gangImage, "mode": "partitioned", "workers": run.Workers,
			"resources": run.Resources(),
			"database":  map[string]interface{}{"secretRef": name + "-db-app"},
		}},
	}}
	_, err := v.c.DC.Resource(batchJobGVR).Namespace(ns).Create(ctx, bj, metav1.CreateOptions{})
	require.NoError(t, err, "BatchJob %s/%s", ns, name)
	t.Cleanup(func() {
		_ = v.c.DC.Resource(batchJobGVR).Namespace(ns).Delete(context.Background(), name, metav1.DeleteOptions{})
	})
	fctx, cancel := v.budget(5 * time.Minute)
	defer cancel()
	require.NoError(t, Eventually(fctx, 5*time.Second, "QueueFits on "+name, func(ctx context.Context) (bool, string, error) {
		u, err := v.c.DC.Resource(batchJobGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error(), nil
		}
		conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
		for _, c := range conds {
			cm, _ := c.(map[string]interface{})
			if cm["type"] == "QueueFits" {
				status, _ = cm["status"].(string)
				reason, _ = cm["reason"].(string)
				message, _ = cm["message"].(string)
				return true, "", nil
			}
		}
		return false, fmt.Sprintf("no QueueFits condition yet (%d conditions)", len(conds)), nil
	}))
	t.Logf("%s/%s asks %s: QueueFits %s %s: %s (FU-11)", ns, name, run.Ask(), status, reason, message)
	return status, reason, message
}

/* ─────────────── A2.6: one scale-up ─────────────── */

// scaleUp needs the platform's node autoscaler running against a cluster it
// can grow; on Kind it cannot add a node, and decision_test.go is A2.6's
// evidence there.
func (v *volcanoSuite) scaleUp(t *testing.T) {
	ctx := v.ctx
	list, err := v.c.DC.Resource(adharPlatformGVR).Namespace("").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	var platform *unstructured.Unstructured
	for i := range list.Items {
		platform = &list.Items[i]
	}
	require.NotNil(t, platform, "no AdharPlatform")
	enabled, _, _ := unstructured.NestedBool(platform.Object, "spec", "autoscaling", "enabled")
	observed, _, _ := unstructured.NestedBool(platform.Object, "status", "autoscaling", "enabled")
	if !enabled || !observed {
		t.Skipf("A2.6's canary half needs the node autoscaler running on a cluster it can grow (spec.autoscaling.enabled %v, observed %v); "+
			"a Kind cluster cannot add a worker, and decision_test.go (TestAPendingGangBuysWorkersOnlyWhenItMay) is the evidence there", enabled, observed)
	}
	maxWorkers, _, _ := unstructured.NestedInt64(platform.Object, "spec", "autoscaling", "maxWorkers")
	nodes, err := v.c.CS.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	var workers []corev1.Node
	for _, n := range nodes.Items {
		if Schedulable(n) {
			workers = append(workers, n)
		}
	}
	require.NotEmpty(t, workers)
	if maxWorkers > 0 && int64(len(workers)) >= maxWorkers {
		t.Skipf("the cluster is at maxWorkers (%d): no room to buy a worker", maxWorkers)
	}
	alloc := roomOf(workers[0].Status.Allocatable)
	_, idle, err := v.room(ctx)
	require.NoError(t, err)
	// Fill every worker, then ask for two members that together fit one new
	// worker (at the autoscaler's 0.85 margin) and no existing one.
	ballast := map[string]Room{}
	for n, r := range idle {
		ballast[n] = Room{MilliCPU: max(r.MilliCPU-20, 0)}
	}
	v.placeBallast(t, "scale", ballast)
	member := Room{MilliCPU: alloc.MilliCPU * 35 / 100, Memory: memberMemory}
	devGang := GangSpec{Name: gangName(v.e.cfg.Tag, "dev-scale"), Namespace: devNS, Queue: v.leaves[devNS], PriorityClass: ClassVolcano, Members: 2, Member: member,
		Labels: map[string]string{labelWorkload: "batch"}}
	prodGang := GangSpec{Name: gangName(v.e.cfg.Tag, "prod-scale"), Namespace: prodNS, Queue: v.leaves[prodNS], PriorityClass: ClassVolcano, Members: 2, Member: member,
		Labels: map[string]string{labelWorkload: "batch", labelScaleUp: "true", "adhar.io/environment": prodNS}}
	since := time.Now()
	v.createGang(t, devGang)
	time.Sleep(4 * time.Minute) // longer than a tick and the scale-up cooldown's first window
	assert.Empty(t, v.scaleUpsFor(ctx, since, devGang.Name), "a dev gang bought a worker (A2.6)")

	since = time.Now()
	v.createGang(t, prodGang)
	wctx, cancel := v.budget(v.e.vc.Scenario)
	defer cancel()
	require.NoError(t, Eventually(wctx, 15*time.Second, "a scale-up for "+prodGang.Name, func(ctx context.Context) (bool, string, error) {
		n := len(v.scaleUpsFor(ctx, since, prodGang.Name))
		return n > 0, fmt.Sprintf("%d ScalingUp events", n), nil
	}))
	require.NoError(t, v.waitGangRunning(wctx, prodNS, prodGang.Name, 2), v.describeGang(ctx, prodNS, prodGang.Name))
	ups := v.scaleUpsFor(ctx, since, prodGang.Name)
	assert.Len(t, ups, 1, "an opted-in prod gang that fits one worker buys exactly one: %v", ups)
}

// scaleUpsFor are the autoscaler's ScalingUp events naming a gang.
func (v *volcanoSuite) scaleUpsFor(ctx context.Context, since time.Time, gang string) []string {
	var out []string
	for _, e := range v.events(ctx, since) {
		if e.Reason == "ScalingUp" && e.InvolvedObject.Kind == "AdharPlatform" && strings.Contains(e.Message, gang) {
			out = append(out, e.Message)
		}
	}
	return out
}

/* ─────────────── the partitioned job ─────────────── */

// scaffoldPartitioned builds springboot-batch-partitioned (TPL-2.1), whose image
// carries the kit's remote partitioning (KIT-2.1). Without the template the
// partitioned criteria skip, saying which tasks they wait for.
func (v *volcanoSuite) scaffoldPartitioned(t *testing.T) {
	vc := v.e.vc
	tpl, err := ensureApp(t, v.e, appSpec{Name: vc.App, Templates: vc.Templates, Phase2: true,
		Params: []string{"--param", "batchMode=cron", "--param", "schedule=" + v.e.cfg.Schedule, "--param", "timeZone=UTC",
			"--param", "description=adhar batch e2e partitioned"}})
	if _, ok := err.(errNoTemplate); ok {
		t.Skipf("%v: A2.2, A2.3 and the partitioned half of A2.10 need TPL-2.1 (springboot-batch-partitioned) and KIT-2.1 (remote partitioning, ADHAR_BATCH_ROLE)", err)
	}
	require.NoError(t, err)
	bj, err := v.c.DC.Resource(batchJobGVR).Namespace(devNS).Get(v.ctx, vc.App, metav1.GetOptions{})
	require.NoError(t, err)
	mode, _, _ := unstructured.NestedString(bj.Object, "spec", "parameters", "mode")
	require.Equal(t, "partitioned", mode, "the partitioned template's BatchJob mode")
	workers, ok, _ := unstructured.NestedInt64(bj.Object, "spec", "parameters", "workers")
	if !ok {
		workers = 4 // the XRD's default (§6.2)
	}
	scheduler, _, _ := unstructured.NestedString(bj.Object, "status", "scheduler")
	queue, _, _ := unstructured.NestedString(bj.Object, "status", "queue")
	assert.Equal(t, "volcano", scheduler, "BatchJob status.scheduler (scheduler: auto with the descriptor)")
	assert.Equal(t, v.leaves[devNS], queue, "BatchJob status.queue (the Console reads it)")
	g, problems := CheckVolcanoGang(tpl, int(workers), v.leaves[devNS])
	require.Empty(t, problems, "dev's run template is not the §4.5 gang (PLAT-2.5)")
	t.Logf("partitioned %s: %d workers, gang %+v", vc.App, workers, g)
	v.part, v.partWorkers = tpl, int(workers)
}

// partSuite is the Phase-1 helpers over the partitioned app.
func (v *volcanoSuite) partSuite() *suite {
	cfg := v.e.cfg
	cfg.App = v.e.vc.App
	return &suite{cfg: cfg, c: v.c, ctx: v.ctx, tpl: v.part}
}

// startPartitioned seeds and starts a partitioned run of a business date and
// returns its Volcano Job's name once the gang runs.
func (v *volcanoSuite) startPartitioned(t *testing.T, day int) (string, map[string]string) {
	t.Helper()
	s := v.partSuite()
	_, err := s.c.Psql(v.ctx, devNS, s.cfg.App+"-db", SeedSQL(databaseOwner, s.cfg.Items))
	require.NoError(t, err, "seed the partitioned job's input (§5.10: SqlKeyRangePartitioner over public.batch_input)")
	values := dbValues(s.cfg.Date(day))
	o, r := s.runCLI(t, devNS, values, false)
	require.Zero(t, r.ExitCode, "%s", r)
	require.Equal(t, "VolcanoJob", o.Kind, "the D2 document's kind")
	ctx, cancel := v.budget(v.e.vc.Scenario)
	defer cancel()
	require.NoError(t, v.waitGangRunning(ctx, devNS, o.Run, v.partWorkers+1), v.describeGang(v.ctx, devNS, o.Run))
	return o.Run, values
}

func (v *volcanoSuite) waitVolcanoFinished(t *testing.T, ns, name string) VolcanoJob {
	t.Helper()
	var j VolcanoJob
	ctx, cancel := v.budget(v.e.cfg.Run)
	defer cancel()
	require.NoError(t, Eventually(ctx, 3*time.Second, "Volcano Job "+name+" to finish", func(ctx context.Context) (bool, string, error) {
		var err error
		j, err = v.vcjob(ctx, ns, name)
		if err != nil {
			return false, err.Error(), nil
		}
		return j.Finished(), fmt.Sprintf("%+v", j), nil
	}), v.describeGang(v.ctx, ns, name))
	return j
}

func endedAt(p corev1.Pod) (time.Time, bool) {
	for _, s := range p.Status.ContainerStatuses {
		if s.Name == batchContainer && s.State.Terminated != nil {
			return s.State.Terminated.FinishedAt.Time, true
		}
	}
	return time.Time{}, false
}

// managerEndsWorkers (A2.3): the manager completing is the job completing —
// CompleteJob ends the workers within seconds, they do not drain on their own.
func (v *volcanoSuite) managerEndsWorkers(t *testing.T) {
	s := v.partSuite()
	name, values := v.startPartitioned(t, 30)
	var managerEnd time.Time
	workerSeen := map[string]time.Time{}
	ctx, cancel := v.budget(v.e.cfg.Run)
	defer cancel()
	require.NoError(t, Eventually(ctx, time.Second, "the manager to complete and the workers to end", func(ctx context.Context) (bool, string, error) {
		pods, err := v.gangPods(ctx, devNS, name)
		if err != nil {
			return false, err.Error(), nil
		}
		alive := 0
		for _, p := range pods {
			switch RoleOf(p) {
			case "manager":
				if at, ok := endedAt(p); ok && p.Status.Phase == corev1.PodSucceeded {
					managerEnd = at
				}
			case "worker":
				if at, ok := endedAt(p); ok {
					workerSeen[p.Name] = at
				} else if p.DeletionTimestamp != nil {
					workerSeen[p.Name] = p.DeletionTimestamp.Time
				} else {
					alive++
				}
			}
		}
		return !managerEnd.IsZero() && alive == 0, fmt.Sprintf("manager ended %v, %d workers alive", managerEnd, alive), nil
	}), v.describeGang(v.ctx, devNS, name))
	last := managerEnd
	for _, at := range workerSeen {
		if at.After(last) {
			last = at
		}
	}
	assert.LessOrEqual(t, last.Sub(managerEnd), v.e.vc.CompleteWithin, "the last worker ended %s after the manager (CompleteJob, A2.3)", last.Sub(managerEnd))
	j := v.waitVolcanoFinished(t, devNS, name)
	assert.Equal(t, "Completed", j.Phase, "the Volcano Job after its manager completed")

	l, err := s.c.Ledger(v.ctx, devNS, s.cfg.App, s.tpl.Identity(values))
	require.NoError(t, err)
	require.NoError(t, l.CheckPartitioned(int64(s.cfg.Items), v.partWorkers), "%+v", l)
	// D2 on a Volcano run: the same identity is the completed run, nothing new.
	again, r := s.runCLI(t, devNS, values, true)
	assert.Equal(t, name, again.Run)
	assert.Equal(t, "Succeeded", again.Status, "%s", r)
}

// killWorker (A2.2): a worker SIGKILLed mid-partition fails its pod, the
// gang's policy restarts the whole Volcano Job, and the new execution runs
// only the partitions that had not completed — every row once.
func (v *volcanoSuite) killWorker(t *testing.T) {
	s := v.partSuite()
	name, values := v.startPartitioned(t, 31)
	identity := s.tpl.Identity(values)
	ctx, cancel := v.budget(v.e.cfg.Run)
	defer cancel()
	require.NoError(t, Eventually(ctx, time.Second, "a partition to complete while another is mid-flight", func(ctx context.Context) (bool, string, error) {
		l, err := s.c.Ledger(ctx, devNS, s.cfg.App, identity)
		if err != nil {
			return false, err.Error(), nil
		}
		if len(l.Completed()) > 0 {
			return false, "", fmt.Errorf("the run completed before a worker could be killed; raise ADHAR_E2E_BATCH_ITEMS (now %d)", s.cfg.Items)
		}
		done, open := 0, 0
		for _, st := range l.Steps {
			if strings.Contains(st.Name, ":") {
				if st.Status == "COMPLETED" {
					done++
				} else if st.Status == "STARTED" && st.Written > 0 {
					open++
				}
			}
		}
		return done > 0 && open > 0, fmt.Sprintf("%d partitions done, %d mid-flight", done, open), nil
	}), v.describeGang(v.ctx, devNS, name))

	pods, err := v.gangPods(v.ctx, devNS, name)
	require.NoError(t, err)
	var worker *corev1.Pod
	for i := range pods {
		if RoleOf(pods[i]) == "worker" && pods[i].Status.Phase == corev1.PodRunning {
			worker = &pods[i]
			break
		}
	}
	require.NotNil(t, worker, "no running worker%s", v.describeGang(v.ctx, devNS, name))
	require.NoError(t, s.c.KillBatchContainer(v.ctx, *worker))
	t.Logf("SIGKILLed worker %s", worker.Name)

	rctx, rcancel := v.budget(v.e.cfg.Run)
	defer rcancel()
	require.NoError(t, Eventually(rctx, 2*time.Second, "RestartJob", func(ctx context.Context) (bool, string, error) {
		j, err := v.vcjob(ctx, devNS, name)
		if err != nil {
			return false, err.Error(), nil
		}
		return j.RetryCount >= 1, fmt.Sprintf("%+v", j), nil
	}), v.describeGang(v.ctx, devNS, name))
	j := v.waitVolcanoFinished(t, devNS, name)
	require.Equal(t, "Completed", j.Phase, v.describeGang(v.ctx, devNS, name))

	l, err := s.c.Ledger(v.ctx, devNS, s.cfg.App, identity)
	require.NoError(t, err)
	require.NoError(t, l.CheckPartitioned(int64(s.cfg.Items), v.partWorkers), "%+v", l)
	assert.GreaterOrEqual(t, len(l.Executions), 2, "the restarted gang started a new execution of the instance")
	r := l.Partitions()
	assert.NotEmpty(t, r.Resumed, "no partition was left unfinished and resumed: the kill did not land mid-partition (%+v)", r)
	t.Logf("partitions %d, resumed after the restart %v", len(r.All), r.Resumed)
}

// stopResume: `adhar batch stop` aborts a Volcano run (an AbortJob Command),
// the execution ends STOPPED, D2 reports Stopped/4, and `resume` (ResumeJob)
// completes the same instance exactly once (FU-11: the round trip, live).
func (v *volcanoSuite) stopResume(t *testing.T) {
	s := v.partSuite()
	name, values := v.startPartitioned(t, 32)
	identity := s.tpl.Identity(values)
	ctx, cancel := v.budget(v.e.cfg.Run)
	defer cancel()
	require.NoError(t, Eventually(ctx, time.Second, "a partition to commit", func(ctx context.Context) (bool, string, error) {
		l, err := s.c.Ledger(ctx, devNS, s.cfg.App, identity)
		if err != nil {
			return false, err.Error(), nil
		}
		return l.Writes() > 0 && len(l.Completed()) == 0, fmt.Sprintf("%d written", l.Writes()), nil
	}))
	r, err := Adhar(v.ctx, 2*time.Minute, "batch", "stop", name, "-n", devNS)
	require.NoError(t, err)
	require.Zero(t, r.ExitCode, "%s", r)
	require.NoError(t, Eventually(ctx, 2*time.Second, "the Volcano run aborted and its execution STOPPED", func(ctx context.Context) (bool, string, error) {
		j, err := v.vcjob(ctx, devNS, name)
		if err != nil {
			return false, err.Error(), nil
		}
		l, err := s.c.Ledger(ctx, devNS, s.cfg.App, identity)
		if err != nil {
			return false, err.Error(), nil
		}
		last, _ := l.Last()
		pods, _ := v.gangPods(ctx, devNS, name)
		alive := 0
		for _, p := range pods {
			if p.Status.Phase == corev1.PodRunning {
				alive++
			}
		}
		return (j.Phase == "Aborted" || j.Phase == "Aborting") && last.Status == "STOPPED" && alive == 0,
			fmt.Sprintf("job %s, execution %d %s, %d pods running", j.Phase, last.ID, last.Status, alive), nil
	}), v.describeGang(v.ctx, devNS, name))
	o, r2 := s.runCLI(t, devNS, values, true)
	assert.Equal(t, "Stopped", o.Status, "%s", r2)
	assert.Equal(t, 4, r2.ExitCode)

	r, err = Adhar(v.ctx, 2*time.Minute, "batch", "resume", name, "-n", devNS)
	require.NoError(t, err)
	require.Zero(t, r.ExitCode, "%s", r)
	j := v.waitVolcanoFinished(t, devNS, name)
	require.Equal(t, "Completed", j.Phase, v.describeGang(v.ctx, devNS, name))
	l, err := s.c.Ledger(v.ctx, devNS, s.cfg.App, identity)
	require.NoError(t, err)
	require.NoError(t, l.CheckPartitioned(int64(s.cfg.Items), v.partWorkers), "%+v", l)
}

/* ─────────────── A2.10: Volcano removed ─────────────── */

// removeVolcano removes the descriptor the way the volcano README orders it
// (descriptor first): by switching off, in the live ApplicationSet, the element
// whose Application manages it — so Argo CD prunes it and nothing recreates
// it. No BatchJob is touched; every one must reach the native path by itself,
// and a run must complete there. The element is switched back on afterwards.
func (v *volcanoSuite) removeVolcano(t *testing.T) {
	ctx := v.ctx
	vc := v.e.vc
	desc, err := v.c.CS.CoreV1().ConfigMaps(e2e.PlatformNamespace).Get(ctx, DescriptorName, metav1.GetOptions{})
	require.NoError(t, err)
	app, err := TrackingApp(desc.Annotations)
	require.NoError(t, err)
	appset, err := v.c.DC.Resource(appSetGVR).Namespace(e2e.PlatformNamespace).Get(ctx, e2e.PlatformAppSet, metav1.GetOptions{})
	require.NoError(t, err)
	element, err := ElementForApplication(appset, app)
	require.NoError(t, err)
	require.Contains(t, []string{"adhar-batch-volcano", "volcano"}, element,
		"the descriptor is managed by %s, which carries more than Volcano: it must ship in the Volcano elements (FU-13) for A2.10 to remove it alone", element)

	// The BatchJobs, as they are before; nothing below may edit them.
	jobs := map[string]int64{}
	apps := []string{v.e.cfg.App}
	if v.part != nil {
		apps = append(apps, vc.App)
	}
	for _, a := range apps {
		if bj, err := v.c.DC.Resource(batchJobGVR).Namespace(devNS).Get(ctx, a, metav1.GetOptions{}); err == nil {
			jobs[a] = bj.GetGeneration()
		}
	}
	require.NotEmpty(t, jobs, "no BatchJob to watch fall back: the Phase-1 app was never scaffolded")

	patch := func(on bool) error {
		cur, err := v.c.DC.Resource(appSetGVR).Namespace(e2e.PlatformNamespace).Get(context.Background(), e2e.PlatformAppSet, metav1.GetOptions{})
		if err != nil {
			return err
		}
		p, err := ElementPatch(cur, element, on)
		if err != nil {
			return err
		}
		_, err = v.c.DC.Resource(appSetGVR).Namespace(e2e.PlatformNamespace).Patch(context.Background(), e2e.PlatformAppSet, types.JSONPatchType, p, metav1.PatchOptions{})
		return err
	}
	// The ApplicationSet is itself under GitOps: its owner Application would
	// self-heal the patch below away within seconds, and the element with it.
	// Its automated sync is held for the scenario and given back after.
	argoApps := v.c.DC.Resource(argoAppGVR).Namespace(e2e.PlatformNamespace)
	var release []byte
	if owner, err := argoApps.Get(ctx, AppSetOwner, metav1.GetOptions{}); err == nil {
		var hold []byte
		hold, release, err = SelfHealHold(owner)
		require.NoError(t, err)
		if hold != nil {
			_, err = argoApps.Patch(ctx, AppSetOwner, types.JSONPatchType, hold, metav1.PatchOptions{})
			require.NoError(t, err, "hold the automated sync of %s, which owns ApplicationSet %s", AppSetOwner, e2e.PlatformAppSet)
		}
	} else if !apierrors.IsNotFound(err) {
		require.NoError(t, err, "Application %s", AppSetOwner)
	}
	require.NoError(t, patch(false), "switch off %s in ApplicationSet %s", element, e2e.PlatformAppSet)
	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		err := patch(true)
		if release != nil {
			if _, rerr := argoApps.Patch(context.Background(), AppSetOwner, types.JSONPatchType, release, metav1.PatchOptions{}); rerr != nil {
				t.Errorf("could not give %s its automated sync back: %v", AppSetOwner, rerr)
			}
		}
		if err != nil {
			t.Errorf("could not switch %s back on: %v", element, err)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), vc.Native)
		defer cancel()
		_ = Eventually(ctx, 5*time.Second, "the descriptor back", func(ctx context.Context) (bool, string, error) {
			_, err := v.c.CS.CoreV1().ConfigMaps(e2e.PlatformNamespace).Get(ctx, DescriptorName, metav1.GetOptions{})
			return err == nil, fmt.Sprint(err), nil
		})
	}
	t.Cleanup(restore)

	nctx, cancel := v.budget(vc.Native)
	defer cancel()
	require.NoError(t, Eventually(nctx, 5*time.Second, "the descriptor pruned", func(ctx context.Context) (bool, string, error) {
		_, err := v.c.CS.CoreV1().ConfigMaps(e2e.PlatformNamespace).Get(ctx, DescriptorName, metav1.GetOptions{})
		return apierrors.IsNotFound(err), fmt.Sprint(err), nil
	}))
	for a := range jobs {
		require.NoError(t, Eventually(nctx, 10*time.Second, "BatchJob "+a+" native", func(ctx context.Context) (bool, string, error) {
			bj, err := v.c.DC.Resource(batchJobGVR).Namespace(devNS).Get(ctx, a, metav1.GetOptions{})
			if err != nil {
				return false, err.Error(), nil
			}
			scheduler, _, _ := unstructured.NestedString(bj.Object, "status", "scheduler")
			cm, err := v.c.CS.CoreV1().ConfigMaps(devNS).Get(ctx, a+"-run", metav1.GetOptions{})
			if err != nil {
				return false, err.Error(), nil
			}
			tpl, err := ParseRunTemplateKinds(cm.Data)
			if err != nil {
				return false, err.Error(), nil
			}
			var problems []string
			if a == vc.App {
				problems = CheckIndexedFallback(tpl, v.partWorkers)
			} else {
				problems = CheckNativeRun(tpl)
			}
			ready, why := ConditionTrue(bj, "Ready")
			return scheduler == "kubernetes" && len(problems) == 0 && ready,
				fmt.Sprintf("status.scheduler %q, template %v, %s", scheduler, problems, why), nil
		}))
	}
	for a, gen := range jobs {
		bj, err := v.c.DC.Resource(batchJobGVR).Namespace(devNS).Get(ctx, a, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, gen, bj.GetGeneration(), "BatchJob %s was edited: A2.10 is 'no manual edits'", a)
	}

	// A run completes on the native path, the partitioned one without a gang.
	p1 := &suite{cfg: v.e.cfg, c: v.c, ctx: v.ctx}
	tpl, err := p1.c.RunTemplate(ctx, devNS, v.e.cfg.App)
	require.NoError(t, err)
	p1.tpl = tpl
	o, r := p1.runCLI(t, devNS, map[string]string{"businessDate": v.e.cfg.Date(40)}, true)
	assert.Equal(t, "Succeeded", o.Status, "a native run after Volcano was removed: %s", r)
	if pods, err := p1.c.RunPods(ctx, devNS, o.Run); err == nil && len(pods) > 0 {
		assert.Equal(t, corev1.DefaultSchedulerName, pods[len(pods)-1].Spec.SchedulerName, "the run's pod after the removal")
	}
	if v.part != nil {
		s := v.partSuite()
		cm, err := v.c.CS.CoreV1().ConfigMaps(devNS).Get(ctx, vc.App+"-run", metav1.GetOptions{})
		require.NoError(t, err)
		s.tpl, err = ParseRunTemplateKinds(cm.Data)
		require.NoError(t, err)
		_, err = s.c.Psql(ctx, devNS, s.cfg.App+"-db", SeedSQL(databaseOwner, s.cfg.Items))
		require.NoError(t, err)
		values := dbValues(s.cfg.Date(41))
		o, r := s.runCLI(t, devNS, values, true)
		assert.Equal(t, "IndexedJob", o.Kind, "the partitioned run's kind without Volcano")
		assert.Equal(t, "Succeeded", o.Status, "%s%s", r, s.c.DescribeRun(ctx, devNS, o.Run))
		l, err := s.c.Ledger(ctx, devNS, s.cfg.App, s.tpl.Identity(values))
		require.NoError(t, err)
		assert.NoError(t, l.CheckPartitioned(int64(s.cfg.Items), v.partWorkers), "the Indexed fallback (index 0 the manager): %+v", l)
	}
	restore()
}
