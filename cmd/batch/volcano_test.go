package batch

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
)

// The Volcano half of `adhar batch` (design 0025 §4.5, §8.1 Phase 2). The
// fixtures are what Volcano v1.16 writes: a partitioned run is a Volcano Job
// with a manager and a worker task, its PodGroup is `<job>-<uid>` owned by it,
// and its pods carry volcano.sh/job-name, volcano.sh/task-spec and
// volcano.sh/task-index.

// volcanoRun is a partitioned run of `settle` as a Volcano Job in phase.
func volcanoRun(name, phase string, created time.Time) *unstructured.Unstructured {
	task := func(role string, replicas int64) map[string]interface{} {
		return map[string]interface{}{"name": role, "replicas": replicas, "template": map[string]interface{}{
			"metadata": map[string]interface{}{"labels": map[string]interface{}{labelBatchJob: "settle", labelRole: role}},
			"spec": map[string]interface{}{"containers": []interface{}{map[string]interface{}{
				"name": "batch", "image": "img", "env": []interface{}{
					map[string]interface{}{"name": envParams, "value": `{"businessDate":"2026-11-03"}`},
					map[string]interface{}{"name": "ADHAR_BATCH_ROLE", "value": role},
				}}}},
		}}
	}
	status := map[string]interface{}{"state": map[string]interface{}{"phase": phase}}
	switch phase {
	case "Pending":
		status["pending"] = int64(5)
	case "Running":
		status["running"] = int64(5)
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "batch.volcano.sh/v1alpha1", "kind": "Job",
		"metadata": map[string]interface{}{
			"name": name, "namespace": "prod", "uid": "uid-" + name,
			"creationTimestamp": created.Format(time.RFC3339),
			"labels":            map[string]interface{}{labelBatchJob: "settle", labelTrigger: "manual"},
		},
		"spec": map[string]interface{}{
			"schedulerName": "volcano", "queue": "acme-payments-prod", "minAvailable": int64(5),
			"tasks": []interface{}{task("manager", 1), task("worker", 4)},
		},
		"status": status,
	}}
}

func podGroup(name, ownerUID, phase string, minMember int64, conds ...map[string]interface{}) *unstructured.Unstructured {
	var raw []interface{}
	for _, c := range conds {
		raw = append(raw, c)
	}
	status := map[string]interface{}{"phase": phase, "conditions": raw}
	if phase == "Running" {
		status["running"] = int64(minMember)
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "scheduling.volcano.sh/v1beta1", "kind": "PodGroup",
		"metadata": map[string]interface{}{
			"name": name, "namespace": "prod",
			"ownerReferences": []interface{}{map[string]interface{}{"apiVersion": "batch.volcano.sh/v1alpha1", "kind": "Job", "name": "owner", "uid": ownerUID, "controller": true}},
		},
		"spec":   map[string]interface{}{"minMember": minMember, "queue": "acme-payments-prod"},
		"status": status,
	}}
}

func unschedulable(msg string, at time.Time) map[string]interface{} {
	return map[string]interface{}{"type": "Unschedulable", "status": "True", "reason": "NotEnoughResources", "message": msg, "lastTransitionTime": at.Format(time.RFC3339)}
}

// A run Volcano places shows its queue and its gang, and a gang the queue has
// not admitted says why in the run's message — the newest Unschedulable
// condition, never one left over from before the gang started.
func TestVolcanoRunsShowTheirQueueAndGang(t *testing.T) {
	fixedClock(t)
	created := testNow.Add(-time.Hour)
	waiting := volcanoRun("settle-09932cb3", "Pending", created)
	running := volcanoRun("settle-aaaaaaaa", "Running", created.Add(-time.Hour))
	native := jobObject("settle-bbbbbbbb", "prod", "settle", "succeeded", created.Add(-2*time.Hour))
	// A single run under Volcano: a plain Job whose pods name the scheduler.
	single := jobObject("settle-cccccccc", "prod", "settle", "running", created.Add(-3*time.Hour))
	tpl := single.Object["spec"].(map[string]interface{})["template"].(map[string]interface{})
	tpl["spec"].(map[string]interface{})["schedulerName"] = "volcano"
	tpl["metadata"] = map[string]interface{}{"annotations": map[string]interface{}{annotationQueueName: "acme-payments-prod"}}
	dc := fakeCluster(batchJobXR("settle", "prod", "", ""), waiting, running, native, single,
		podGroup("settle-09932cb3-uid-settle-09932cb3", "uid-settle-09932cb3", "Pending", 5,
			unschedulable("old message", created), unschedulable("5/5 tasks in gang unschedulable: 0/3 nodes are available", created.Add(time.Minute))),
		podGroup("settle-aaaaaaaa-uid-settle-aaaaaaaa", "uid-settle-aaaaaaaa", "Running", 5, unschedulable("left over from the wait", created)),
		podGroup("podgroup-uid-settle-cccccccc", "uid-settle-cccccccc", "Running", 1),
	)
	rows, _, err := runsOfJob(context.Background(), dc, "prod", "settle")
	if err != nil || len(rows) != 4 {
		t.Fatalf("runs = %d %v", len(rows), err)
	}
	by := map[string]runRow{}
	for _, r := range rows {
		by[r.Name] = r
	}
	w := by["settle-09932cb3"]
	if w.Scheduler != "volcano" || w.Queue != "acme-payments-prod" || w.Gang == nil || w.Gang.MinMember != 5 || w.Gang.Phase != "Pending" || w.Gang.Pending != 5 {
		t.Fatalf("waiting gang = %+v %+v", w, w.Gang)
	}
	if w.Gang.Message != "5/5 tasks in gang unschedulable: 0/3 nodes are available" || !strings.Contains(w.Message, "gang of 5 waiting in queue acme-payments-prod") {
		t.Errorf("a waiting gang says why: gang %q, run %q", w.Gang.Message, w.Message)
	}
	if r := by["settle-aaaaaaaa"]; r.Gang == nil || r.Gang.Message != "" || gangCell(r.Gang) != "running 5/5" {
		t.Errorf("a started gang carries no stale wait: %+v", r.Gang)
	}
	if s := by["settle-cccccccc"]; s.Scheduler != "volcano" || s.Queue != "acme-payments-prod" || gangCell(s.Gang) != "running 1/1" {
		t.Errorf("a single run under Volcano = %+v %+v", s, s.Gang)
	}
	// Found by owner whatever its name, and by Volcano's names with no owner.
	owned := podGroup("named-by-an-older-volcano", "uid-settle-09932cb3", "Pending", 5)
	if podGroupOf(waiting, []unstructured.Unstructured{*owned}) == nil {
		t.Error("a PodGroup the run owns is its PodGroup")
	}
	byName := podGroup("settle-09932cb3-uid-settle-09932cb3", "someone-else", "Pending", 5)
	byName.SetOwnerReferences(nil)
	if podGroupOf(waiting, []unstructured.Unstructured{*byName}) == nil {
		t.Error("a PodGroup with the run's Volcano name is its PodGroup")
	}
	if podGroupOf(waiting, []unstructured.Unstructured{*podGroup("other", "uid-other", "Pending", 5)}) != nil {
		t.Error("another run's PodGroup is not this run's")
	}
	if n := by["settle-bbbbbbbb"]; n.Scheduler != "" || n.Queue != "" || n.Gang != nil {
		t.Errorf("a run kube-scheduler placed has no Volcano fields: %+v", n)
	}

	views := make([]runView, 0, len(rows))
	for _, r := range rows {
		views = append(views, runViewOf(r, testNow))
	}
	if out := runsTable(views).Unbounded().Render(); !strings.Contains(out, "GANG") || !strings.Contains(out, "pending 0/5") {
		t.Errorf("the runs table shows queue and gang:\n%s", out)
	}
	if j := toJSON(runViewOf(by["settle-09932cb3"], testNow)); !strings.Contains(j, `"gang":{"minMember":5`) || !strings.Contains(j, `"queue":"acme-payments-prod"`) {
		t.Errorf("-o json carries the gang: %s", j)
	}
}

// On a cluster without Volcano the commands are what they were before Phase 2:
// no PodGroup is ever listed, the table has no QUEUE or GANG column, and -o
// json gains no key.
func TestNativeRunsAreUnchangedByVolcanoSupport(t *testing.T) {
	fixedClock(t)
	created := testNow.Add(-time.Hour)
	dc := fakeCluster(batchJobXR("importer", "prod", "", ""),
		jobObject("importer-09932cb3", "prod", "importer", "failed", created),
		jobObject("importer-aaaaaaaa", "prod", "importer", "succeeded", created.Add(-time.Hour)))
	dc.PrependReactor("list", "podgroups", func(k8stesting.Action) (bool, runtime.Object, error) {
		t.Error("PodGroups were listed for runs Volcano does not place")
		return false, nil, nil
	})
	rows, _, err := runsOfJob(context.Background(), dc, "prod", "importer")
	if err != nil || len(rows) != 2 {
		t.Fatalf("runs = %d %v", len(rows), err)
	}
	if _, err := listJobs(context.Background(), dc, ""); err != nil {
		t.Fatal(err)
	}
	var views []runView
	for _, r := range rows {
		views = append(views, runViewOf(r, testNow))
	}
	out := runsTable(views).Unbounded().Render()
	if strings.Contains(out, "GANG") {
		t.Errorf("a native table gained Volcano columns:\n%s", out)
	}
	for _, key := range []string{`"gang"`, `"queue"`, `"scheduler"`} {
		if j := toJSON(views[0]); strings.Contains(j, key) {
			t.Errorf("a native run's JSON gained %s: %s", key, j)
		}
	}
}

// A Volcano Job numbers each task from 0, so worker-0 has task index 0 too.
// The manager speaks for the run, and `logs --index 0` on a Volcano run is
// worker 0, never the manager.
func TestVolcanoTaskRolesPickTheManagerAndTheWorker(t *testing.T) {
	created := testNow.Add(-time.Hour)
	pod := func(name, task string, exit int, outcome string, at time.Time) unstructured.Unstructured {
		p := podObject(name, "prod", "", exit, map[string]interface{}{"v": 1, "outcome": outcome, "exitCode": exit}, at)
		p.Object["metadata"].(map[string]interface{})["labels"] = map[string]interface{}{
			labelVolcanoJob: "settle-09932cb3", labelBatchJob: "settle", labelVolcanoTask: task, labelVolcanoIndex: "0",
		}
		return *p
	}
	run := volcanoRun("settle-09932cb3", "Failed", created)
	pods := []unstructured.Unstructured{
		pod("settle-09932cb3-manager-0", "manager", 3, "failed", created),
		pod("settle-09932cb3-worker-0", "worker", 143, "stopped", created.Add(time.Minute)),
	}
	row := runRowOf(run, pods, testNow)
	if row.ExitCode == nil || *row.ExitCode != 3 {
		t.Errorf("the manager's exit code is the run's: %v", row.ExitCode)
	}
	if got := selectPods(pods, "", true, 0, true); len(got) != 1 || got[0].GetName() != "settle-09932cb3-worker-0" {
		t.Errorf("--index 0 on a Volcano run = %v, want worker 0", podNames(got))
	}
	if got := selectPods(pods, "manager", false, 0, true); len(got) != 1 || got[0].GetName() != "settle-09932cb3-manager-0" {
		t.Errorf("--role manager = %v", podNames(got))
	}
	if got := selectPods(pods, "worker", false, 0, true); len(got) != 1 {
		t.Errorf("--role worker = %v", podNames(got))
	}
}

// Stop and resume on a Volcano run create the bus.volcano.sh Command, never a
// patch of the Volcano Job; a finished one is refused.
func TestVolcanoStopAndResumeCreateCommands(t *testing.T) {
	ctx := context.Background()
	running := volcanoRun("settle-09932cb3", "Running", testNow)
	aborted := volcanoRun("settle-aaaaaaaa", "Aborted", testNow)
	done := volcanoRun("settle-bbbbbbbb", "Completed", testNow)
	dc := fakeCluster(running, aborted, done)
	actions := map[string]string{}
	dc.PrependReactor("create", "commands", func(a k8stesting.Action) (bool, runtime.Object, error) {
		obj := a.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		target, _, _ := unstructured.NestedString(obj.Object, "target", "name")
		action, _, _ := unstructured.NestedString(obj.Object, "action")
		actions[target] = action
		return true, obj, nil
	})
	dc.PrependReactor("patch", "*", func(a k8stesting.Action) (bool, runtime.Object, error) {
		t.Errorf("a Volcano run was patched: %v", a)
		return true, nil, nil
	})
	for _, c := range []struct {
		run  *unstructured.Unstructured
		stop bool
	}{{running, true}, {aborted, false}} {
		req, err := suspendRequest(c.run, c.stop)
		if err != nil {
			t.Fatal(err)
		}
		if err := perform(ctx, dc, req); err != nil {
			t.Fatal(err)
		}
	}
	if actions["settle-09932cb3"] != "AbortJob" || actions["settle-aaaaaaaa"] != "ResumeJob" {
		t.Errorf("commands = %v", actions)
	}
	if _, err := suspendRequest(done, true); err == nil {
		t.Error("a completed Volcano run cannot be stopped")
	}
	if _, err := suspendRequest(running, false); err == nil {
		t.Error("a running Volcano run cannot be resumed")
	}
}

// Restart of a failed Volcano run is a NEW run from the template, <run>-r<n>,
// with the same identity on every task — not Volcano's RestartJob, which on a
// finished job kills rather than restarts (§7.4, §8.1).
func TestRestartOfAVolcanoRunIsANewVolcanoRun(t *testing.T) {
	fixedClock(t)
	quietOutput(t)
	tplRun := volcanoRun("", "", testNow)
	delete(tplRun.Object, "status")
	meta := tplRun.Object["metadata"].(map[string]interface{})
	delete(meta, "name")
	delete(meta, "uid")
	delete(meta, "creationTimestamp")
	body := toJSON(tplRun.Object)
	cm := templateConfigMap(`[{"name":"businessDate","type":"date","identifying":true,"source":"schedule"}]`)
	cm.SetName("settle-run")
	data := cm.Object["data"].(map[string]interface{})
	data["template.yaml"], data["kind"] = body, "VolcanoJob"
	xr := batchJobXR("settle", "prod", "", "")
	failed := volcanoRun("settle-09932cb3", "Failed", testNow.Add(-time.Hour))
	dc := fakeCluster(xr, cm, failed)
	dc.PrependReactor("create", "commands", func(k8stesting.Action) (bool, runtime.Object, error) {
		t.Error("restart sent a Volcano Command instead of creating a run")
		return true, nil, nil
	})
	created, plan, err := restartRun(context.Background(), dc, "prod", "settle-09932cb3")
	if err != nil || !created || plan.Name != "settle-09932cb3-r1" {
		t.Fatalf("restart = %v %+v %v", created, plan, err)
	}
	run, err := dc.Resource(volcanoJobGVR).Namespace("prod").Get(context.Background(), "settle-09932cb3-r1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the restart is a Volcano Job: %v", err)
	}
	for _, tpl := range podTemplatesOf(run) {
		for _, c := range liveContainers(tpl) {
			env, _ := c["env"].([]interface{})
			if !hasEnv(env, envRestartOf) {
				t.Errorf("a task lacks %s: %v", envRestartOf, env)
			}
		}
	}
}

// A Volcano Job that is not a batch run (a team's training job, no
// adhar.io/batch-job label) is refused exactly as an unlabelled Job is: stop
// would otherwise send it an AbortJob.
func TestAVolcanoJobThatIsNotABatchRunIsRefused(t *testing.T) {
	vc := volcanoRun("training", "Running", testNow)
	vc.SetLabels(map[string]string{"app": "training"})
	dc := fakeCluster(vc)
	dc.PrependReactor("create", "commands", func(a k8stesting.Action) (bool, runtime.Object, error) {
		t.Errorf("a Command was sent to a Volcano Job that is not a run: %v", a)
		return true, nil, nil
	})
	if _, err := getRun(context.Background(), dc, "prod", "training"); err == nil || !strings.Contains(err.Error(), "not a batch run") {
		t.Errorf("getRun = %v", err)
	}
	if _, err := getRun(context.Background(), dc, "prod", "settle-09932cb3"); err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Errorf("a missing run = %v", err)
	}
}

// Two task roles with no adhar.io/batch-role label and the task index only as
// Volcano's annotation (every release writes it; the label is newer): the
// manager still speaks for the run however new a worker's summary is, and
// --index N is worker N.
func TestVolcanoTaskIndexFromTheAnnotation(t *testing.T) {
	created := testNow.Add(-time.Hour)
	pod := func(name, task, idx string, exit int, at time.Time) unstructured.Unstructured {
		p := podObject(name, "prod", "", exit, map[string]interface{}{"v": 1, "outcome": "failed", "exitCode": exit}, at)
		p.Object["metadata"].(map[string]interface{})["labels"] = map[string]interface{}{
			labelVolcanoJob: "settle-09932cb3", labelBatchJob: "settle", labelVolcanoTask: task,
		}
		p.SetAnnotations(map[string]string{labelVolcanoIndex: idx})
		return *p
	}
	pods := []unstructured.Unstructured{
		pod("settle-09932cb3-manager-0", "manager", "0", 4, created),
		pod("settle-09932cb3-worker-0", "worker", "0", 143, created.Add(time.Minute)),
		pod("settle-09932cb3-worker-1", "worker", "1", 1, created.Add(2*time.Minute)),
	}
	row := runRowOf(volcanoRun("settle-09932cb3", "Failed", created), pods, testNow)
	if row.ExitCode == nil || *row.ExitCode != 4 {
		t.Errorf("the manager's exit code is the run's: %v", row.ExitCode)
	}
	if got := podNames(selectPods(pods, "", true, 1, true)); len(got) != 1 || got[0] != "settle-09932cb3-worker-1" {
		t.Errorf("--index 1 = %v", got)
	}
	if got := podNames(selectPods(pods, "manager", true, 0, true)); len(got) != 1 || got[0] != "settle-09932cb3-manager-0" {
		t.Errorf("--role manager --index 0 = %v", got)
	}
}
