package adharplatform

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// The BatchJob composition's scheduler branch (design 0025 §4, §6.4, PLAT-2.5):
// single mode on Volcano only changes the pod; partitioned mode is a Volcano gang
// where Volcano runs and an Indexed Job where it does not; removing Volcano leaves
// every BatchJob running natively (A2.10). Rendered in-process the way
// function-go-templating does (bjExecute, batchjob_composition_test.go), through as
// many passes as Crossplane takes to settle the required resources, and through
// `crossplane render` when Docker is available. Fixtures: testdata/batchjob-volcano.

const vcFixtureDir = "testdata/batchjob-volcano"

// vcCluster is the Volcano cluster of the fixtures, minus the named objects.
func vcCluster(t *testing.T, without ...string) []map[string]any {
	t.Helper()
	var out []map[string]any
docs:
	for _, d := range readYAMLDocs(t, filepath.Join(vcFixtureDir, "required-resources.yaml")) {
		for _, w := range without {
			if dig(d, "metadata", "name") == w {
				continue docs
			}
		}
		out = append(out, d)
	}
	return out
}

// vcNoVolcano is the same cluster with Volcano removed (descriptor and CRDs gone).
func vcNoVolcano(t *testing.T) []map[string]any {
	return vcCluster(t, "adhar-batch-scheduler", "jobs.batch.volcano.sh", "cronjobs.batch.volcano.sh", "queues.scheduling.volcano.sh",
		"acme-payments-prod", "acme-payments-dev", "acme-payments-test")
}

// vcEdit returns the cluster with fn applied to the object named name.
func vcEdit(cluster []map[string]any, name string, fn func(map[string]any)) []map[string]any {
	raw, _ := json.Marshal(cluster)
	var cp []map[string]any
	_ = json.Unmarshal(raw, &cp)
	for _, d := range cp {
		if dig(d, "metadata", "name") == name {
			fn(d)
		}
	}
	return cp
}

func vcXR(t *testing.T, name string) map[string]any {
	t.Helper()
	docs := readYAMLDocs(t, filepath.Join(vcFixtureDir, name))
	if len(docs) != 1 || docs[0]["kind"] != "BatchJob" {
		t.Fatalf("%s must hold exactly one BatchJob", name)
	}
	return docs[0]
}

func vcObserved(t *testing.T, file string) map[string]any {
	t.Helper()
	return fnObserved(t, readYAMLDocs(t, filepath.Join(vcFixtureDir, file)))
}

// vcRender runs the step as Crossplane does: call, fetch what it requires, call
// again, until the requirements stop changing. The leaf queue is asked for only
// once the namespace's labels are known, so this takes three passes.
func vcRender(t *testing.T, xr map[string]any, cluster []map[string]any, observed map[string]any) ([]map[string]any, error) {
	t.Helper()
	req := map[string]any{"observed": map[string]any{"composite": map[string]any{"resource": xr}, "resources": observed}}
	last := ""
	for pass := 0; pass < 6; pass++ {
		docs, err := bjExecute(t, req)
		if err != nil {
			if pass == 0 {
				return nil, fmt.Errorf("the first pass, which cannot see the namespace, must never refuse: %w", err)
			}
			return nil, err
		}
		var reqs map[string]any
		for _, d := range docs {
			if d["kind"] == "ExtraResources" {
				reqs, _ = d["requirements"].(map[string]any)
			}
		}
		key, _ := json.Marshal(reqs)
		if string(key) == last {
			return docs, nil
		}
		last = string(key)
		required := map[string]any{}
		for k, raw := range reqs {
			sel, _ := raw.(map[string]any)
			var items []any
			for _, obj := range cluster {
				if obj["apiVersion"] == sel["apiVersion"] && obj["kind"] == sel["kind"] &&
					dig(obj, "metadata", "name") == sel["matchName"] &&
					fmt.Sprint(dig(obj, "metadata", "namespace")) == fmt.Sprint(sel["namespace"]) {
					items = append(items, map[string]any{"resource": obj})
				}
			}
			required[k] = map[string]any{"items": items}
		}
		req["requiredResources"] = required
	}
	t.Fatal("the requirements never settled; Crossplane gives up after a few passes")
	return nil, nil
}

func vcMustRender(t *testing.T, xr map[string]any, cluster []map[string]any, observed map[string]any) []map[string]any {
	t.Helper()
	docs, err := vcRender(t, xr, cluster, observed)
	if err != nil {
		t.Fatalf("the composition refused %v: %v", dig(xr, "metadata", "name"), err)
	}
	return docs
}

// vcTemplate is the run template's parsed template.yaml and its `kind`.
func vcTemplate(t *testing.T, docs []map[string]any) (map[string]any, string) {
	t.Helper()
	cm := bjMustResource(t, docs, "run-template")
	var run map[string]any
	if err := yaml.Unmarshal([]byte(dig(cm, "data", "template.yaml").(string)), &run); err != nil {
		t.Fatalf("template.yaml does not parse: %v", err)
	}
	return run, fmt.Sprint(dig(cm, "data", "kind"))
}

// vcPods are a run object's pod templates by role: a Job's one, a Volcano Job's per task.
func vcPods(run map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	if tpl, ok := dig(run, "spec", "template").(map[string]any); ok {
		out[fmt.Sprint(dig(tpl, "metadata", "labels", "adhar.io/batch-role"))] = tpl
	}
	tasks, _ := dig(run, "spec", "tasks").([]any)
	for _, task := range tasks {
		m := task.(map[string]any)
		out[m["name"].(string)] = m["template"].(map[string]any)
	}
	return out
}

func vcContainerEnv(t *testing.T, pod map[string]any) (map[string]any, map[string]any) {
	t.Helper()
	cs, _ := dig(pod, "spec", "containers").([]any)
	if len(cs) != 1 {
		t.Fatalf("a run pod has %d containers, want 1", len(cs))
	}
	c := cs[0].(map[string]any)
	return c, bjEnv(t, c)
}

// Which scheduler a job resolves to, from what the cluster offers and what the job
// asked for. `auto` never fails a job Volcano cannot take: it runs natively and says
// why (DEC-3); `volcano` insists and reads PrerequisitesReady=False instead.
func TestVolcanoBatchJobResolution(t *testing.T) {
	single := bjXR(t, "xr-scheduled.yaml") // prod, priority critical
	gang := vcXR(t, "xr-partitioned.yaml") // dev
	closed := func(d map[string]any) { d["status"] = map[string]any{"state": "Closed"} }
	foreign := func(d map[string]any) {
		d["metadata"].(map[string]any)["labels"] = map[string]any{"adhar.io/organisation": "acme-pay", "adhar.io/team": "ments"}
	}
	for _, c := range []struct {
		name        string
		xr          map[string]any
		scheduler   any
		cluster     []map[string]any
		wantSched   string
		wantKind    string
		wantFits    string // QueueFits reason
		wantPrereq  string
		wantMessage string
	}{
		{"single, auto, Volcano installed", single, nil, vcCluster(t), "volcano", "Job", "Fits", "True", ""},
		{"single, auto, Volcano absent (A2.10)", single, nil, vcNoVolcano(t), "kubernetes", "Job", "NativeScheduler", "True", ""},
		{"single, kubernetes, Volcano installed", single, "kubernetes", vcCluster(t), "kubernetes", "Job", "NativeScheduler", "True", ""},
		{"partitioned, auto, Volcano installed", gang, nil, vcCluster(t), "volcano", "VolcanoJob", "Fits", "True", ""},
		{"partitioned, auto, Volcano absent (A2.10)", gang, nil, vcNoVolcano(t), "kubernetes", "IndexedJob", "NativeScheduler", "True", ""},
		{"partitioned, kubernetes", gang, "kubernetes", vcCluster(t), "kubernetes", "IndexedJob", "NativeScheduler", "True", ""},
		{"a descriptor without the CRDs is not Volcano", gang, nil, vcCluster(t, "jobs.batch.volcano.sh"), "kubernetes", "IndexedJob", "NativeScheduler", "True", ""},
		{"a descriptor naming another scheduler", gang, nil, vcEdit(vcCluster(t), "adhar-batch-scheduler", func(d map[string]any) { d["data"].(map[string]any)["scheduler"] = "kubernetes" }), "kubernetes", "IndexedJob", "NativeScheduler", "True", ""},
		{"gang: false keeps partitioned jobs native", gang, nil, vcEdit(vcCluster(t), "adhar-batch-scheduler", func(d map[string]any) { d["data"].(map[string]any)["gang"] = "false" }), "kubernetes", "IndexedJob", "NativeScheduler", "True", ""},
		{"auto, the leaf does not exist (a prod leaf the ceiling refused)", single, nil, vcCluster(t, "acme-payments-prod"), "kubernetes", "Job", "LeafQueueUnavailable", "True", "does not exist"},
		{"auto, the leaf is another team's", single, nil, vcEdit(vcCluster(t), "acme-payments-prod", foreign), "kubernetes", "Job", "LeafQueueUnavailable", "True", "belongs to organisation"},
		{"auto, the leaf is closed", single, nil, vcEdit(vcCluster(t), "acme-payments-prod", closed), "kubernetes", "Job", "LeafQueueUnavailable", "True", "Closed"},
		{"auto, the Volcano classes are missing", single, nil, vcCluster(t, "adhar-batch-volcano-critical"), "kubernetes", "Job", "NativeScheduler", "True", ""},
		{"volcano, Volcano absent", single, "volcano", vcNoVolcano(t), "kubernetes", "Job", "NativeScheduler", "False", "not installed"},
		{"volcano, the leaf does not exist", single, "volcano", vcCluster(t, "acme-payments-prod"), "kubernetes", "Job", "LeafQueueUnavailable", "False", "waits for it"},
		{"volcano, the Volcano classes are missing", single, "volcano", vcCluster(t, "adhar-batch-volcano-critical"), "kubernetes", "Job", "NativeScheduler", "False", "adhar-batch-volcano-critical"},
		{"volcano, installed", gang, "volcano", vcCluster(t), "volcano", "VolcanoJob", "Fits", "True", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			docs := vcMustRender(t, bjWithParams(c.xr, map[string]any{"scheduler": c.scheduler}), c.cluster, nil)
			st := bjStatus(t, docs)
			if st["scheduler"] != c.wantSched {
				t.Errorf("status.scheduler = %v, want %s", st["scheduler"], c.wantSched)
			}
			run, kind := vcTemplate(t, docs)
			if kind != c.wantKind {
				t.Errorf("the run template's kind = %s, want %s", kind, c.wantKind)
			}
			fits := bjCondition(t, docs, "QueueFits")
			if fits["reason"] != c.wantFits {
				t.Errorf("QueueFits = %v, want reason %s", fits, c.wantFits)
			}
			prereq := bjCondition(t, docs, "PrerequisitesReady")
			if prereq["status"] != c.wantPrereq {
				t.Errorf("PrerequisitesReady = %v, want %s", prereq, c.wantPrereq)
			}
			if c.wantMessage != "" && !strings.Contains(fmt.Sprint(fits["message"])+fmt.Sprint(prereq["message"]), c.wantMessage) {
				t.Errorf("neither QueueFits nor PrerequisitesReady says %q: %v / %v", c.wantMessage, fits["message"], prereq["message"])
			}
			// A warning is said in the one-line summary too, which the CLI prints.
			if fits["status"] == "False" && !strings.Contains(fmt.Sprint(st["message"]), fmt.Sprint(fits["message"])) {
				t.Errorf("status.message %q does not carry the QueueFits warning", st["message"])
			}
			queue, hasQueue := st["queue"]
			if (c.wantSched == "volcano") != hasQueue {
				t.Errorf("status.queue = %v on a %s job", queue, c.wantSched)
			}
			// INV-5 / DEC-1: a pod Volcano schedules has a Volcano class (which may
			// preempt, among batch only); a pod kube-scheduler schedules has a Never
			// class, so kube-scheduler never preempts anything for batch.
			for role, pod := range vcPods(run) {
				sched, class := dig(pod, "spec", "schedulerName"), fmt.Sprint(dig(pod, "spec", "priorityClassName"))
				volcanoClass := strings.HasPrefix(class, "adhar-batch-volcano")
				if (sched == "volcano") != (c.wantSched == "volcano") || (sched == "volcano") != volcanoClass {
					t.Errorf("%s pod: schedulerName %v with PriorityClass %s on a %s job", role, sched, class, c.wantSched)
				}
			}
			if c.wantPrereq == "False" {
				if dig(bjMustResource(t, docs, "schedule-native"), "spec", "suspend") != true {
					t.Error("a job waiting for a scheduler it insists on keeps its schedule running")
				}
			}
		})
	}
}

// A named environment runs on its tier's leaf (DEC-8): staging is tier test.
func TestVolcanoLeafIsTheNamespacesTier(t *testing.T) {
	docs := vcMustRender(t, bjWithMeta(vcXR(t, "xr-partitioned.yaml"), map[string]any{"namespace": "staging"}), vcCluster(t), nil)
	if q := bjStatus(t, docs)["queue"]; q != "acme-payments-test" {
		t.Errorf("a staging (tier test) job runs on queue %v", q)
	}
	run, _ := vcTemplate(t, docs)
	if q := dig(run, "spec", "queue"); q != "acme-payments-test" {
		t.Errorf("spec.queue = %v", q)
	}
	// A namespace with no tier has no leaf; auto says so and runs natively.
	cluster := vcEdit(vcCluster(t), "staging", func(d map[string]any) {
		delete(d["metadata"].(map[string]any)["labels"].(map[string]any), "adhar.io/tier")
	})
	docs = vcMustRender(t, bjWithMeta(vcXR(t, "xr-partitioned.yaml"), map[string]any{"namespace": "staging"}), cluster, nil)
	if c := bjCondition(t, docs, "QueueFits"); c["reason"] != "LeafQueueUnavailable" || !strings.Contains(fmt.Sprint(c["message"]), "adhar.io/tier") {
		t.Errorf("QueueFits = %v", c)
	}
}

// §6.4: in single mode Volcano only changes the pod template — schedulerName, the
// queue annotation and the class. Everything else is the native run, byte for byte,
// and the CronJob stays a batch/v1 CronJob.
func TestVolcanoSingleRunOnlyChangesThePod(t *testing.T) {
	xr := bjXR(t, "xr-scheduled.yaml")
	onVolcano := vcMustRender(t, xr, vcCluster(t), nil)
	native := vcMustRender(t, xr, vcNoVolcano(t), nil)
	v, _ := vcTemplate(t, onVolcano)
	n, _ := vcTemplate(t, native)
	pod := dig(v, "spec", "template", "spec").(map[string]any)
	if pod["schedulerName"] != "volcano" || pod["priorityClassName"] != "adhar-batch-volcano-critical" {
		t.Errorf("pod schedulerName/class = %v/%v", pod["schedulerName"], pod["priorityClassName"])
	}
	if a := dig(v, "spec", "template", "metadata", "annotations", "scheduling.volcano.sh/queue-name"); a != "acme-payments-prod" {
		t.Errorf("queue annotation = %v; Volcano's PodGroup controller reads the queue from it", a)
	}
	// Undo exactly those three, and the runs must be identical.
	pod["schedulerName"] = "default-scheduler"
	pod["priorityClassName"] = "adhar-batch-critical"
	delete(dig(v, "spec", "template", "metadata", "annotations").(map[string]any), "scheduling.volcano.sh/queue-name")
	if a, b := tqJSON(v), tqJSON(n); a != b {
		t.Errorf("single mode on Volcano changed more than the pod's scheduler, class and queue:\n%s\n%s", a, b)
	}
	cron := bjMustResource(t, onVolcano, "schedule-native")
	if cron["apiVersion"] != "batch/v1" || bjResource(onVolcano, "schedule-volcano") != nil {
		t.Errorf("a single-mode job on Volcano composes a %v CronJob", cron["apiVersion"])
	}
	if dig(cron, "spec", "jobTemplate", "spec", "template", "spec", "schedulerName") != "volcano" {
		t.Error("the CronJob's runs do not go to Volcano; only the person-started ones would")
	}

	// A2.10: with Volcano removed the render is the Phase-1 native render exactly.
	phase1 := bjMustRender(t, xr, bjCluster(t), nil)
	p1, _ := vcTemplate(t, phase1)
	if a, b := tqJSON(n), tqJSON(p1); a != b {
		t.Errorf("the native path changed with Phase 2:\n%s\n%s", a, b)
	}
	if a, b := tqJSON(bjMustResource(t, native, "schedule-native")["spec"]), tqJSON(bjMustResource(t, phase1, "schedule-native")["spec"]); a != b {
		t.Errorf("the native CronJob changed with Phase 2:\n%s\n%s", a, b)
	}
}

// §4.5 native fallback: one Indexed Job, index 0 the manager, the Job done when
// index 0 is.
func TestPartitionedWithoutVolcanoIsAnIndexedJob(t *testing.T) {
	docs := vcMustRender(t, vcXR(t, "xr-partitioned.yaml"), vcNoVolcano(t), nil)
	run, kind := vcTemplate(t, docs)
	if kind != "IndexedJob" || run["apiVersion"] != "batch/v1" || run["kind"] != "Job" {
		t.Fatalf("kind %s, %v %v", kind, run["apiVersion"], run["kind"])
	}
	spec := run["spec"].(map[string]any)
	for k, v := range map[string]string{
		"completionMode": "Indexed", "completions": "5", "parallelism": "5", "podReplacementPolicy": "Failed",
		"backoffLimit": "2", "activeDeadlineSeconds": "21600", "ttlSecondsAfterFinished": "604800",
	} {
		if fmt.Sprint(spec[k]) != v {
			t.Errorf("%s = %v, want %s", k, spec[k], v)
		}
	}
	if got := tqJSON(spec["successPolicy"]); got != `{"rules":[{"succeededIndexes":"0"}]}` {
		t.Errorf("successPolicy = %s; the manager finishing must end the run", got)
	}
	rules, _ := dig(spec, "podFailurePolicy", "rules").([]any)
	if len(rules) != 2 || dig(rules[0].(map[string]any), "action") != "Ignore" || dig(rules[1].(map[string]any), "action") != "FailJob" {
		t.Errorf("podFailurePolicy = %v; the same as a single run's", spec["podFailurePolicy"])
	}
	pod := dig(spec, "template").(map[string]any)
	c, env := vcContainerEnv(t, pod)
	if env["ADHAR_BATCH_ROLE"] != "indexed" || dig(pod, "metadata", "labels", "adhar.io/batch-role") != "indexed" {
		t.Errorf("role env/label = %v/%v", env["ADHAR_BATCH_ROLE"], dig(pod, "metadata", "labels", "adhar.io/batch-role"))
	}
	if ref, _ := env["JOB_COMPLETION_INDEX"].(map[string]any); dig(ref, "fieldRef", "fieldPath") != "metadata.annotations['batch.kubernetes.io/job-completion-index']" {
		t.Errorf("JOB_COMPLETION_INDEX = %v; the kit resolves the manager from it", env["JOB_COMPLETION_INDEX"])
	}
	for k, v := range map[string]string{"ADHAR_BATCH_PARTITION_TRANSPORT": "jdbc", "ADHAR_BATCH_PARTITION_GRIDSIZE": "32", "ADHAR_BATCH_PARTITION_TIMEOUT": "21300s"} {
		if env[k] != v {
			t.Errorf("env %s = %v, want %s", k, env[k], v)
		}
	}
	if _, set := env["ADHAR_BATCH_MODE"]; set {
		t.Error("ADHAR_BATCH_MODE is set: Spring binds it to adhar.batch.mode, which is auto|kubernetes|embedded")
	}
	// One template for every index: the larger of manager and worker, per axis.
	if dig(c, "resources", "requests", "cpu") != "1" || dig(c, "resources", "requests", "memory") != "1536Mi" || dig(c, "resources", "limits", "memory") != "1536Mi" {
		t.Errorf("resources = %v, want the worker's 1 / 1536Mi (larger than the manager's 500m / 1Gi)", c["resources"])
	}
	if pod := dig(spec, "template", "spec").(map[string]any); pod["schedulerName"] != "default-scheduler" || pod["priorityClassName"] != "adhar-batch" {
		t.Errorf("pod schedulerName/class = %v/%v", pod["schedulerName"], pod["priorityClassName"])
	}
	// gridSize defaults to 4 × workers.
	run, _ = vcTemplate(t, vcMustRender(t, bjWithParams(vcXR(t, "xr-partitioned.yaml"), map[string]any{"gridSize": nil, "workers": 3}), vcNoVolcano(t), nil))
	if _, env := vcContainerEnv(t, dig(run, "spec", "template").(map[string]any)); env["ADHAR_BATCH_PARTITION_GRIDSIZE"] != "12" {
		t.Errorf("gridSize default = %v, want 12 (4 × 3 workers)", env["ADHAR_BATCH_PARTITION_GRIDSIZE"])
	}
}

// §4.5: a gang of one manager and N workers, all or nothing, the manager's end the
// gang's end, a non-retryable exit ending it, anything else restarting all of it.
func TestPartitionedOnVolcanoIsAGang(t *testing.T) {
	docs := vcMustRender(t, vcXR(t, "xr-partitioned.yaml"), vcCluster(t), nil)
	run, kind := vcTemplate(t, docs)
	if kind != "VolcanoJob" || run["apiVersion"] != "batch.volcano.sh/v1alpha1" || run["kind"] != "Job" {
		t.Fatalf("kind %s, %v %v", kind, run["apiVersion"], run["kind"])
	}
	spec := run["spec"].(map[string]any)
	for k, v := range map[string]string{
		"schedulerName": "volcano", "queue": "acme-payments-dev", "priorityClassName": "adhar-batch-volcano",
		"minAvailable": "5", "maxRetry": "3", "ttlSecondsAfterFinished": "604800", "plugins": "map[env:[]]",
	} {
		if fmt.Sprint(spec[k]) != v {
			t.Errorf("%s = %v, want %s", k, spec[k], v)
		}
	}
	if _, set := spec["volumes"]; set {
		t.Error("spec.volumes is set: Volcano creates a PersistentVolumeClaim for each entry")
	}
	var codes []string
	var last map[string]any
	for _, p := range spec["policies"].([]any) {
		m := p.(map[string]any)
		if c, ok := m["exitCode"]; ok {
			if m["action"] != "TerminateJob" {
				t.Errorf("exit code %v → %v, want TerminateJob", c, m["action"])
			}
			if last != nil {
				t.Error("an exit-code rule comes after the restart rule, which would match the same failure first")
			}
			codes = append(codes, fmt.Sprint(c))
		} else {
			last = m
		}
	}
	if strings.Join(codes, ",") != "2,3,4,6,7" {
		t.Errorf("TerminateJob exit codes = %v, want the §5.7 contract 2,3,4,6,7", codes)
	}
	if last == nil || last["action"] != "RestartJob" || fmt.Sprint(last["events"]) != "[PodEvicted PodFailed]" {
		t.Errorf("the restart rule = %v", last)
	}
	tasks := spec["tasks"].([]any)
	if len(tasks) != 2 {
		t.Fatalf("%d tasks", len(tasks))
	}
	mgr, wk := tasks[0].(map[string]any), tasks[1].(map[string]any)
	if mgr["name"] != "manager" || fmt.Sprint(mgr["replicas"]) != "1" || wk["name"] != "worker" || fmt.Sprint(wk["replicas"]) != "4" {
		t.Errorf("tasks = %v / %v", mgr["name"], wk["name"])
	}
	if got := tqJSON(mgr["policies"]); got != `[{"action":"CompleteJob","events":["TaskCompleted"]}]` {
		t.Errorf("the manager's policies = %s; its end must end the workers (A2.3)", got)
	}
	if wk["policies"] != nil {
		t.Errorf("a worker finishing ends the gang: %v", wk["policies"])
	}
	want := map[string][2]string{"manager": {"500m", "1Gi"}, "worker": {"1", "1536Mi"}}
	for role, pod := range vcPods(run) {
		ps := pod["spec"].(map[string]any)
		for k, v := range map[string]string{"schedulerName": "volcano", "restartPolicy": "Never", "priorityClassName": "adhar-batch-volcano", "activeDeadlineSeconds": "21600", "enableServiceLinks": "false"} {
			if fmt.Sprint(ps[k]) != v {
				t.Errorf("%s pod %s = %v, want %s", role, k, ps[k], v)
			}
		}
		if dig(pod, "metadata", "labels", "adhar.io/batch-role") != role || dig(pod, "metadata", "labels", "adhar.io/batch-job") != "reconciler" {
			t.Errorf("%s pod labels = %v", role, dig(pod, "metadata", "labels"))
		}
		if dig(pod, "metadata", "annotations", "scheduling.volcano.sh/queue-name") != nil {
			t.Errorf("%s pod names a queue; a gang's pods take the Job's", role)
		}
		c, env := vcContainerEnv(t, pod)
		if env["ADHAR_BATCH_ROLE"] != role {
			t.Errorf("%s pod ADHAR_BATCH_ROLE = %v", role, env["ADHAR_BATCH_ROLE"])
		}
		if ref, _ := env["ADHAR_BATCH_K8S_JOB"].(map[string]any); dig(ref, "fieldRef", "fieldPath") != "metadata.labels['volcano.sh/job-name']" {
			t.Errorf("%s pod ADHAR_BATCH_K8S_JOB = %v; a Volcano pod has no batch.kubernetes.io/job-name", role, env["ADHAR_BATCH_K8S_JOB"])
		}
		if _, set := env["ADHAR_BATCH_PARAMS"]; !set {
			t.Errorf("%s pod has no ADHAR_BATCH_PARAMS: every pod computes the same job key from the identifying parameters (§5.10)", role)
		}
		if env["ADHAR_BATCH_PARTITION_GRIDSIZE"] != "32" {
			t.Errorf("%s pod gridSize = %v", role, env["ADHAR_BATCH_PARTITION_GRIDSIZE"])
		}
		if dig(c, "resources", "requests", "cpu") != want[role][0] || dig(c, "resources", "requests", "memory") != want[role][1] ||
			dig(c, "resources", "limits", "memory") != want[role][1] {
			t.Errorf("%s resources = %v, want %v with memory request = limit", role, c["resources"], want[role])
		}
	}
	// The Job's labels become its PodGroup's, which the autoscaler reads.
	for k, v := range map[string]string{"adhar.io/workload": "batch", "adhar.io/environment": "dev", "adhar.io/batch-job": "reconciler", "adhar.io/batch-trigger": "manual"} {
		if got := dig(run, "metadata", "labels", k); got != v {
			t.Errorf("Job label %s = %v, want %s", k, got, v)
		}
	}
	if dig(run, "metadata", "labels", "adhar.io/scale-up") != nil {
		t.Error("a dev gang may buy nodes")
	}
	if bjResource(docs, "schedule-native") != nil || bjResource(docs, "schedule-volcano") != nil {
		t.Error("an on-demand job composed a schedule")
	}
}

// A scheduled gang is a Volcano CronJob stamping out the run template's Volcano Job.
func TestScheduledGangIsAVolcanoCronJob(t *testing.T) {
	xr := vcXR(t, "xr-partitioned-scheduled.yaml")
	docs := vcMustRender(t, xr, vcCluster(t), nil)
	if bjResource(docs, "schedule-native") != nil {
		t.Error("a gang on Volcano also composed a native CronJob")
	}
	cron := bjMustResource(t, docs, "schedule-volcano")
	if cron["apiVersion"] != "batch.volcano.sh/v1alpha1" || cron["kind"] != "CronJob" || dig(cron, "metadata", "name") != "settlement" {
		t.Fatalf("schedule-volcano = %v %v %v", cron["apiVersion"], cron["kind"], dig(cron, "metadata", "name"))
	}
	spec := cron["spec"].(map[string]any)
	for k, v := range map[string]string{"schedule": "0 2 * * *", "timeZone": "Asia/Singapore", "concurrencyPolicy": "Forbid", "suspend": "false", "startingDeadlineSeconds": "3600"} {
		if fmt.Sprint(spec[k]) != v {
			t.Errorf("CronJob %s = %v, want %s", k, spec[k], v)
		}
	}
	jt := spec["jobTemplate"].(map[string]any)
	if dig(jt, "spec", "queue") != "acme-payments-prod" || dig(jt, "spec", "priorityClassName") != "adhar-batch-volcano-critical" {
		t.Errorf("jobTemplate queue/class = %v/%v", dig(jt, "spec", "queue"), dig(jt, "spec", "priorityClassName"))
	}
	for k, v := range map[string]string{"adhar.io/scale-up": "true", "adhar.io/environment": "prod", "adhar.io/workload": "batch", "adhar.io/batch-trigger": "schedule"} {
		if got := dig(jt, "metadata", "labels", k); got != v {
			t.Errorf("jobTemplate label %s = %v, want %s (the PodGroup's, which the autoscaler trusts)", k, got, v)
		}
	}
	// The clock and a person start the same run.
	run, _ := vcTemplate(t, docs)
	norm := func(m map[string]any) string {
		s := tqJSON(map[string]any{"labels": dig(m, "metadata", "labels"), "spec": m["spec"]})
		for _, tr := range []string{"schedule", "manual"} {
			s = strings.ReplaceAll(s, `"adhar.io/batch-trigger":"`+tr+`"`, `"adhar.io/batch-trigger":"X"`)
			s = strings.ReplaceAll(s, `"name":"ADHAR_BATCH_TRIGGER","value":"`+tr+`"`, `"name":"ADHAR_BATCH_TRIGGER","value":"X"`)
		}
		return s
	}
	if a, b := norm(run), norm(jt); a != b {
		t.Errorf("the run template and the Volcano CronJob's jobTemplate differ beyond the trigger:\n%s\n%s", a, b)
	}
	if st := bjStatus(t, docs); st["cronJob"] != "settlement" || st["queue"] != "acme-payments-prod" || st["scheduler"] != "volcano" {
		t.Errorf("status = %v", st)
	}
	// Ready once observed; status from the Volcano CronJob.
	second := vcMustRender(t, xr, vcCluster(t), vcObserved(t, "observed-settlement.yaml"))
	if a := dig(bjMustResource(t, second, "schedule-volcano"), "metadata", "annotations", "gotemplating.fn.crossplane.io/ready"); a != "True" {
		t.Errorf("schedule-volcano once observed: ready = %v", a)
	}
	st := bjStatus(t, second)
	if st["lastSuccessfulTime"] != "2026-11-13T18:41:07Z" || fmt.Sprint(st["activeRuns"]) != "[settlement-29387160]" {
		t.Errorf("status from the Volcano CronJob = %v", st)
	}
	if c := bjCondition(t, second, "ScheduleReady"); c["reason"] != "Scheduled" {
		t.Errorf("ScheduleReady = %v", c)
	}
	// Held, it is suspended like a native one.
	held := vcMustRender(t, bjWithMeta(xr, map[string]any{"annotations": map[string]any{"adhar.io/batch-hold": "freeze"}}), vcCluster(t), nil)
	if dig(bjMustResource(t, held, "schedule-volcano"), "spec", "suspend") != true {
		t.Error("a held gang keeps firing")
	}
	// A2.10: Volcano removed, the same job is a native CronJob of Indexed Jobs.
	fallback := vcMustRender(t, xr, vcNoVolcano(t), nil)
	nc := bjMustResource(t, fallback, "schedule-native")
	if nc["apiVersion"] != "batch/v1" || dig(nc, "spec", "jobTemplate", "spec", "completionMode") != "Indexed" || bjResource(fallback, "schedule-volcano") != nil {
		t.Errorf("without Volcano the gang's schedule is %v %v", nc["apiVersion"], dig(nc, "spec", "jobTemplate", "spec", "completionMode"))
	}
}

// Switching a scheduled gang's kind deletes the old CronJob and its runs with it:
// while one runs, the switch waits and says so (§6.4 hold → switch → release).
func TestScheduleSwitchWaitsForTheRunInFlight(t *testing.T) {
	xr := vcXR(t, "xr-partitioned-scheduled.yaml")
	docs := vcMustRender(t, xr, vcCluster(t), vcObserved(t, "observed-switch.yaml"))
	if bjResource(docs, "schedule-volcano") != nil {
		t.Error("the Volcano CronJob was composed while the native one has a run in flight: Crossplane deletes the native one, and the run with it")
	}
	kept := bjMustResource(t, docs, "schedule-native")
	if kept["apiVersion"] != "batch/v1" || dig(kept, "spec", "suspend") != true {
		t.Errorf("the native CronJob is kept as %v with suspend = %v, want batch/v1 suspended", kept["apiVersion"], dig(kept, "spec", "suspend"))
	}
	if dig(kept, "spec", "jobTemplate", "spec", "completionMode") != "Indexed" || dig(kept, "spec", "timeZone") != "Asia/Singapore" {
		t.Errorf("the kept CronJob is not the observed one: %v", kept["spec"])
	}
	sw := bjCondition(t, docs, "SchedulerSwitch")
	if sw["status"] != "False" || sw["reason"] != "WaitingForRuns" || !strings.Contains(fmt.Sprint(sw["message"]), "settlement-29387160") {
		t.Errorf("SchedulerSwitch = %v", sw)
	}
	if c := bjCondition(t, docs, "ScheduleReady"); c["status"] != "False" || c["reason"] != "SwitchPending" {
		t.Errorf("ScheduleReady = %v", c)
	}
	if !strings.Contains(fmt.Sprint(bjStatus(t, docs)["message"]), "waits for settlement-29387160") {
		t.Errorf("status.message = %v", bjStatus(t, docs)["message"])
	}
	// The run template already describes the new shape: nothing deletes a ConfigMap's runs.
	if _, kind := vcTemplate(t, docs); kind != "VolcanoJob" {
		t.Errorf("run template kind = %s", kind)
	}
	// Once nothing runs, the switch happens.
	obs := vcObserved(t, "observed-switch.yaml")
	delete(dig(obs, "schedule-native", "resource", "status").(map[string]any), "active")
	docs = vcMustRender(t, xr, vcCluster(t), obs)
	if bjResource(docs, "schedule-native") != nil || bjResource(docs, "schedule-volcano") == nil {
		t.Error("the switch did not happen once the run finished")
	}
	if sw := bjCondition(t, docs, "SchedulerSwitch"); sw["status"] != "True" {
		t.Errorf("SchedulerSwitch = %v", sw)
	}
	// And back: Volcano removed while a gang runs keeps the Volcano CronJob (its CRDs
	// still served) until that run is done.
	docs = vcMustRender(t, bjWithParams(xr, map[string]any{"scheduler": "kubernetes"}), vcCluster(t), vcObserved(t, "observed-settlement.yaml"))
	if kept := bjResource(docs, "schedule-volcano"); kept == nil || dig(kept, "spec", "suspend") != true || bjResource(docs, "schedule-native") != nil {
		t.Errorf("switching a running gang back to native: schedule-volcano = %v", kept)
	}
	// With the CRDs gone there is nothing left to keep, and composing one would fail.
	docs = vcMustRender(t, bjWithParams(xr, map[string]any{"scheduler": "kubernetes"}), vcNoVolcano(t), vcObserved(t, "observed-settlement.yaml"))
	if bjResource(docs, "schedule-volcano") != nil || bjResource(docs, "schedule-native") == nil {
		t.Error("a Volcano CronJob was composed on a cluster that no longer serves the kind")
	}
	// A single-mode switch only changes the pod: no kind change, nothing waits.
	single := bjXR(t, "xr-scheduled.yaml")
	obsSingle := map[string]any{"schedule-native": map[string]any{"resource": map[string]any{
		"apiVersion": "batch/v1", "kind": "CronJob", "metadata": map[string]any{"name": "importer", "uid": "u"},
		"status": map[string]any{"active": []any{map[string]any{"name": "importer-1"}}}}}}
	if sw := bjCondition(t, vcMustRender(t, single, vcCluster(t), obsSingle), "SchedulerSwitch"); sw["status"] != "True" {
		t.Errorf("a single-mode job waits to switch: %v", sw)
	}
}

// DEC-3: a run the prod leaf cannot reclaim for is said on the BatchJob. Volcano's
// capacity plugin reclaims only for a queue under its deserved share in at least
// one dimension, so the warning is for a run over it in every dimension.
func TestQueueFitsWarnsBeforeADenial(t *testing.T) {
	xr := vcXR(t, "xr-partitioned-scheduled.yaml") // 500m/1Gi + 4 × 500m/1Gi = 2500m / 5Gi; deserved 2 / 4Gi
	c := bjCondition(t, vcMustRender(t, xr, vcCluster(t), nil), "QueueFits")
	if c["status"] != "False" || c["reason"] != "ExceedsDeserved" || !strings.Contains(fmt.Sprint(c["message"]), "acme-payments-prod") ||
		!strings.Contains(fmt.Sprint(c["message"]), "cpu 2500m") || !strings.Contains(fmt.Sprint(c["message"]), "memory 5120Mi") {
		t.Errorf("QueueFits = %v", c)
	}
	for _, f := range []struct {
		name    string
		workers int
		res     map[string]any
		want    string
	}{
		{"within the share", 2, map[string]any{"cpu": "500m", "memory": "1Gi"}, "Fits"},    // 1500m / 3Gi
		{"over in cpu only", 4, map[string]any{"cpu": "500m", "memory": "512Mi"}, "Fits"},  // 2500m / 2.5Gi
		{"over in memory only", 4, map[string]any{"cpu": "100m", "memory": "1Gi"}, "Fits"}, // 500m / 5Gi
		{"exactly the share", 3, map[string]any{"cpu": "500m", "memory": "1Gi"}, "Fits"},   // 2 / 4Gi
		{"a byte over in both", 3, map[string]any{"cpu": "500m", "memory": "1Gi", "workerCpu": "501m", "workerMemory": "1025Mi"}, "ExceedsDeserved"},
		// 100u is 0.1m, which Kubernetes reads as 1m (MilliValue rounds up): 2001m.
		{"a sub-milli request rounds up", 4, map[string]any{"cpu": "100u", "memory": "1Mi", "workerCpu": "500m", "workerMemory": "1Gi"}, "ExceedsDeserved"},
		{"decimal and binary suffixes", 1, map[string]any{"cpu": "1.5", "memory": "2.5Gi", "workerCpu": "0.6", "workerMemory": "1700M"}, "ExceedsDeserved"}, // 2100m / 4.2Gi
	} {
		got := bjCondition(t, vcMustRender(t, bjWithParams(xr, map[string]any{"workers": f.workers, "resources": f.res}), vcCluster(t), nil), "QueueFits")
		if got["reason"] != f.want {
			t.Errorf("%s: QueueFits = %v, want %s", f.name, got, f.want)
		}
	}
	// A deserved share written as a bare number arrives as a float.
	cluster := vcEdit(vcCluster(t), "acme-payments-prod", func(d map[string]any) {
		d["spec"].(map[string]any)["deserved"] = map[string]any{"cpu": 2.0, "memory": float64(4294967296)}
	})
	if got := bjCondition(t, vcMustRender(t, xr, cluster, nil), "QueueFits"); got["reason"] != "ExceedsDeserved" {
		t.Errorf("a float deserved share: QueueFits = %v", got)
	}
	// dev and test leaves have no deserved share: nothing to exceed.
	if got := bjCondition(t, vcMustRender(t, vcXR(t, "xr-partitioned.yaml"), vcCluster(t), nil), "QueueFits"); got["reason"] != "Fits" {
		t.Errorf("a dev gang: QueueFits = %v", got)
	}
	// It is a warning: the job still runs on Volcano and nothing reads it as Degraded.
	docs := vcMustRender(t, xr, vcCluster(t), nil)
	if bjStatus(t, docs)["scheduler"] != "volcano" || bjCondition(t, docs, "PrerequisitesReady")["status"] != "True" {
		t.Error("ExceedsDeserved stopped the job")
	}
}

// §6.4: a gang larger than the environment's quota could never start (on Volcano) or
// would start only part of itself (natively).
func TestPartitionedRunMustFitTheQuota(t *testing.T) {
	xr := vcXR(t, "xr-partitioned.yaml") // dev-quota memory 8Gi; every key is checked in volcano_composition_quota_test.go
	for _, c := range []struct {
		name    string
		workers int
		cluster []map[string]any
		refused bool
	}{
		{"gang of 1Gi + 4 × 1536Mi", 4, vcCluster(t), false}, // 7Gi
		{"gang of 1Gi + 5 × 1536Mi", 5, vcCluster(t), true},  // 8.5Gi
		{"indexed 5 × 1536Mi", 4, vcNoVolcano(t), false},     // 7.5Gi
		{"indexed 6 × 1536Mi", 5, vcNoVolcano(t), true},      // 9Gi
		{"no quota to check against", 9, vcCluster(t, "dev-quota"), false},
	} {
		_, err := vcRender(t, bjWithParams(xr, map[string]any{"workers": c.workers}), c.cluster, nil)
		if c.refused != (err != nil) {
			t.Errorf("%s: refused = %v (%v)", c.name, err != nil, err)
		}
	}
	// The Indexed fallback sizes every index like the larger pod, so a manager bigger
	// than its workers fits as a gang (2Gi + 6 × 1Gi = 8Gi) and not natively (7 × 2Gi).
	for _, c := range []struct {
		name    string
		cluster []map[string]any
		refused bool
	}{{"gang", vcCluster(t), false}, {"indexed", vcNoVolcano(t), true}} {
		_, err := vcRender(t, bjWithParams(xr, map[string]any{"workers": 6, "resources": map[string]any{"cpu": "100m", "memory": "2Gi", "workerCpu": "100m", "workerMemory": "1Gi"}}), c.cluster, nil)
		if c.refused != (err != nil) {
			t.Errorf("%s: refused = %v (%v)", c.name, err != nil, err)
		}
		if err != nil && (!strings.Contains(err.Error(), "dev-quota") || !strings.Contains(err.Error(), "limits.memory 14336Mi > 8Gi")) {
			t.Errorf("%s: the refusal does not name the quota: %v", c.name, err)
		}
	}
	// Exactly the quota fits.
	if _, err := vcRender(t, bjWithParams(xr, map[string]any{"workers": 7, "resources": map[string]any{"cpu": "100m", "memory": "1Gi"}}), vcCluster(t), nil); err != nil {
		t.Errorf("a gang of exactly the quota (8 × 1Gi) was refused: %v", err)
	}
	// Single mode is not checked: it is one pod, and the API server says the rest.
	if _, err := vcRender(t, bjWithParams(bjXR(t, "xr-ondemand.yaml"), map[string]any{"resources": map[string]any{"memory": "9Gi"}}), vcCluster(t), nil); err != nil {
		t.Errorf("a single run was refused by the gang check: %v", err)
	}
}

// The per-job P2 alerts (§11.3): on the gang path a run is a Volcano Job, which
// kube-state-metrics' Job series never see.
func TestGangAlertsReadTheVolcanoSeries(t *testing.T) {
	rulesOf := func(docs []map[string]any) map[string]map[string]any {
		out := map[string]map[string]any{}
		groups, _ := dig(bjMustResource(t, docs, "rules"), "spec", "groups").([]any)
		for _, g := range groups {
			for _, r := range g.(map[string]any)["rules"].([]any) {
				m := r.(map[string]any)
				out[m["alert"].(string)] = m
			}
		}
		return out
	}
	rules := rulesOf(vcMustRender(t, vcXR(t, "xr-partitioned-scheduled.yaml"), vcCluster(t), nil))
	for alert, want := range map[string][]string{
		"BatchRunFailed":         {`kube_volcano_job_status_phase{namespace="prod",batch_job="settlement",phase=~"Failed|Terminated"} == 1`, "and ignoring(phase) topk by (namespace,batch_job) (1, kube_volcano_job_created{"},
		"BatchGangPending":       {`kube_volcano_podgroup_status_phase{namespace="prod",batch_job="settlement",phase="Inqueue"} == 1`},
		"BatchGangQueueRejected": {`kube_volcano_podgroup_status_phase{namespace="prod",batch_job="settlement",phase="Pending"} == 1`},
		"BatchNoRecentSuccess":   {`kube_volcano_job_status_phase{namespace="prod",batch_job="settlement",phase="Completed"} == 1`, "kube_volcano_job_created", "> 93600"},
		"BatchRunTooLong":        {"kube_pod_start_time", "> 7200", `label_adhar_io_batch_job="settlement"`},
		"BatchRunStuckPending":   {`phase="Pending"`, `label_adhar_io_batch_job="settlement"`},
		"BatchRunOOMKilled":      {"OOMKilled", `label_adhar_io_batch_job="settlement"`},
	} {
		r := rules[alert]
		if r == nil {
			t.Errorf("no %s alert on the gang path", alert)
			continue
		}
		for _, w := range want {
			if !strings.Contains(fmt.Sprint(r["expr"]), w) {
				t.Errorf("%s does not consider %s: %s", alert, w, r["expr"])
			}
		}
		if dig(r, "labels", "severity") != "critical" || dig(r, "labels", "adhar_batch_job") != "settlement" {
			t.Errorf("%s labels = %v", alert, r["labels"])
		}
	}
	for _, a := range []string{"BatchGangPending", "BatchGangQueueRejected"} {
		if rules[a]["for"] != "30m" {
			t.Errorf("%s for = %v, want 30m (design 0025 §11.3)", a, rules[a]["for"])
		}
	}
	for name, r := range rules {
		if strings.Contains(fmt.Sprint(r["expr"]), "kube_job_") || strings.Contains(fmt.Sprint(r["expr"]), "kube_cronjob_") {
			t.Errorf("%s on the gang path reads kube_job_/kube_cronjob_ series, which a Volcano Job never has: %s", name, r["expr"])
		}
		// `job` is also the scrape target's label (kube-state-metrics is scraped with
		// honorLabels), so nothing may select or join on the Volcano Job's name by it.
		if strings.Contains(fmt.Sprint(r["expr"]), "on(namespace,job)") || strings.Contains(fmt.Sprint(r["expr"]), ",job=") {
			t.Errorf("%s selects or joins on `job`: %s", name, r["expr"])
		}
	}
	// A held schedule composes no BatchNoRecentSuccess (Volcano publishes no
	// suspension series to suppress it with).
	held := rulesOf(vcMustRender(t, bjWithMeta(vcXR(t, "xr-partitioned-scheduled.yaml"), map[string]any{"annotations": map[string]any{"adhar.io/batch-hold": "freeze"}}), vcCluster(t), nil))
	if held["BatchNoRecentSuccess"] != nil {
		t.Error("a held gang alerts that it has not succeeded")
	}
	// Off the gang path, the Phase-1 alerts are unchanged.
	native := rulesOf(vcMustRender(t, vcXR(t, "xr-partitioned-scheduled.yaml"), vcNoVolcano(t), nil))
	if native["BatchGangPending"] != nil || !strings.Contains(fmt.Sprint(native["BatchRunFailed"]["expr"]), "kube_job_failed") || native["BatchScheduleMissed"] == nil {
		t.Errorf("the native path's alerts changed: %v", native)
	}
}

// The gang alerts read series observability/kube-prometheus defines (custom-resource
// state, PLAT-2.7). Until those land the alerts would never fire, which reads exactly
// like a healthy gang; this guard then checks every name.
func TestBatchJobVolcanoAlertsReadSeriesTheStackDefines(t *testing.T) {
	root, _ := filepath.Abs("../../..")
	var cfg any
	// The chart's values, and the second values file that holds the custom-resource
	// state (kube-prometheus/custom-resource-state.yaml, passed after values.yaml).
	for _, f := range []string{"values.yaml", "custom-resource-state.yaml"} {
		raw, err := os.ReadFile(filepath.Join(root, "platform/stack/packages/observability/kube-prometheus", f))
		if os.IsNotExist(err) {
			continue
		} else if err != nil {
			t.Fatal(err)
		}
		var values map[string]any
		if err := yaml.Unmarshal(raw, &values); err != nil {
			t.Fatal(err)
		}
		if c := dig(values, "kube-state-metrics", "customResourceState", "config"); c != nil {
			cfg = c
		}
	}
	if s, ok := cfg.(string); ok {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(s), &m); err == nil {
			cfg = m
		}
	}
	cfgMap, _ := cfg.(map[string]any)
	if cfgMap == nil {
		t.Skip("PENDING (PLAT-2.7): kube-prometheus has no kube-state-metrics customResourceState yet")
	}
	defined := map[string]map[string]bool{} // metric → labels
	resources, _ := dig(cfgMap, "spec", "resources").([]any)
	for _, r := range resources {
		rm := r.(map[string]any)
		prefix := "kube_customresource"
		if p, ok := rm["metricNamePrefix"].(string); ok {
			prefix = p
		}
		common := map[string]bool{"namespace": true}
		for k := range asMap(rm["labelsFromPath"]) {
			common[k] = true
		}
		for _, m := range asList(rm["metrics"]) {
			mm := m.(map[string]any)
			labels := map[string]bool{}
			for k := range common {
				labels[k] = true
			}
			for k := range asMap(dig(mm, "each", "gauge", "labelsFromPath")) {
				labels[k] = true
			}
			for k := range asMap(dig(mm, "each", "stateSet", "labelsFromPath")) {
				labels[k] = true
			}
			if ln, ok := dig(mm, "each", "stateSet", "labelName").(string); ok {
				labels[ln] = true
			}
			defined[prefix+"_"+fmt.Sprint(mm["name"])] = labels
		}
	}
	// Every kube_volcano_* series the gang alerts name, with the labels they select or
	// keep. Read off the rendered rules, so a new alert is checked without editing this.
	used := map[string]map[string]bool{}
	groups, _ := dig(bjMustResource(t, vcMustRender(t, vcXR(t, "xr-partitioned-scheduled.yaml"), vcCluster(t), nil), "rules"), "spec", "groups").([]any)
	series := regexp.MustCompile(`(kube_volcano_[a-z_]+)\{([^}]*)\}`)
	for _, g := range groups {
		for _, r := range g.(map[string]any)["rules"].([]any) {
			for _, m := range series.FindAllStringSubmatch(fmt.Sprint(r.(map[string]any)["expr"]), -1) {
				if used[m[1]] == nil {
					used[m[1]] = map[string]bool{"namespace": true, "batch_job": true}
				}
				for _, sel := range strings.Split(m[2], ",") {
					used[m[1]][strings.TrimSpace(regexp.MustCompile(`[=!~].*$`).ReplaceAllString(sel, ""))] = true
				}
			}
		}
	}
	if len(used) < 3 {
		t.Fatalf("the gang alerts name only %v; the rule set changed shape", used)
	}
	for metric, labels := range used {
		have, ok := defined[metric]
		if !ok {
			t.Errorf("the gang alerts read %s, which kube-prometheus's custom-resource state does not define", metric)
			continue
		}
		for l := range labels {
			if !have[l] {
				t.Errorf("%s has no label %s, which the gang alerts select or join on", metric, l)
			}
		}
	}
}

// Every kind the composition reads or composes on the Volcano path is Crossplane's to
// read or manage: a required resource Crossplane may not read fails the whole
// reconcile, and a composed kind it may not manage is a 403 the XR reports only as a
// generic sync error.
func TestBatchJobVolcanoKindsAreGrantedToCrossplane(t *testing.T) {
	type rule struct {
		APIGroups []string `json:"apiGroups"`
		Resources []string `json:"resources"`
		Verbs     []string `json:"verbs"`
	}
	var rules []rule
	for _, d := range readYAMLDocs(t, filepath.Join(controlPlaneDir(t), "rbac/local-compose-rbac.yaml")) {
		if dig(d, "metadata", "name") == "crossplane-compose-local" {
			raw, _ := json.Marshal(d["rules"])
			if err := json.Unmarshal(raw, &rules); err != nil {
				t.Fatal(err)
			}
		}
	}
	holds := func(group, resource, verb string) bool {
		for _, r := range rules {
			if fnContains(r.APIGroups, group) && fnContains(r.Resources, resource) && fnContains(r.Verbs, verb) {
				return true
			}
		}
		return false
	}
	plural := map[string]string{"Namespace": "namespaces", "PriorityClass": "priorityclasses", "ConfigMap": "configmaps", "ResourceQuota": "resourcequotas", "Queue": "queues"}
	docs := vcMustRender(t, vcXR(t, "xr-partitioned-scheduled.yaml"), vcCluster(t), nil)
	for _, d := range docs {
		if d["kind"] != "ExtraResources" {
			continue
		}
		for key, raw := range d["requirements"].(map[string]any) {
			sel := raw.(map[string]any)
			kind := fmt.Sprint(sel["kind"])
			if kind == "CustomResourceDefinition" {
				continue // Crossplane's own ClusterRole reads CRDs; it installs them.
			}
			group := strings.Split(fmt.Sprint(sel["apiVersion"]), "/")[0]
			if !strings.Contains(fmt.Sprint(sel["apiVersion"]), "/") {
				group = ""
			}
			for _, v := range []string{"get", "list", "watch"} {
				if !holds(group, plural[kind], v) {
					t.Errorf("required resource %s (%s.%s) cannot be %s by Crossplane", key, plural[kind], group, v)
				}
			}
		}
	}
	for _, v := range []string{"get", "list", "watch", "create", "update", "patch", "delete"} {
		if !holds("batch.volcano.sh", "cronjobs", v) {
			t.Errorf("Crossplane may not %s cronjobs.batch.volcano.sh, which a scheduled gang composes", v)
		}
	}
	// Crossplane composes no Volcano Job and no PodGroup: Volcano's controllers do.
	if holds("batch.volcano.sh", "jobs", "create") || holds("scheduling.volcano.sh", "podgroups", "get") || holds("scheduling.volcano.sh", "queues", "create") {
		t.Error("Crossplane holds more of Volcano than the compositions use")
	}
}

// vcGolden is a render reduced to what a reviewer reads: every composed object by
// its resource name, the BatchJob's status and its conditions.
func vcGolden(t *testing.T, docs []map[string]any) []byte {
	t.Helper()
	out := map[string]any{}
	composed := map[string]any{}
	for _, d := range docs {
		switch d["kind"] {
		case "ExtraResources":
			out["requirements"] = d["requirements"]
		case "ClaimConditions":
			out["conditions"] = d["conditions"]
		case "BatchJob":
			out["status"] = d["status"]
		default:
			name := fmt.Sprint(dig(d, "metadata", "annotations", "gotemplating.fn.crossplane.io/composition-resource-name"))
			if name == "rules" {
				// The alert set is pinned by TestGangAlertsReadTheVolcanoSeries and
				// TestBatchJobAlerts; the golden file keeps the run's shape readable.
				var alerts []string
				for _, g := range dig(d, "spec", "groups").([]any) {
					for _, r := range g.(map[string]any)["rules"].([]any) {
						alerts = append(alerts, fmt.Sprint(r.(map[string]any)["alert"]))
					}
				}
				d = map[string]any{"kind": d["kind"], "alerts": alerts}
			}
			if name == "run-template" {
				var run map[string]any
				_ = yaml.Unmarshal([]byte(dig(d, "data", "template.yaml").(string)), &run)
				d = map[string]any{"kind": d["kind"], "data.kind": dig(d, "data", "kind"), "template.yaml": run}
			}
			composed[name] = d
		}
	}
	out["composed"] = composed
	b, err := yaml.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var vcBranches = []struct {
	golden, xr string
	cluster    func(t *testing.T) []map[string]any
}{
	{"golden-native-single.yaml", "xr-scheduled", vcNoVolcano},
	{"golden-native-partitioned.yaml", "xr-partitioned.yaml", vcNoVolcano},
	{"golden-volcano-single.yaml", "xr-scheduled", func(t *testing.T) []map[string]any { return vcCluster(t) }},
	{"golden-volcano-gang.yaml", "xr-partitioned.yaml", func(t *testing.T) []map[string]any { return vcCluster(t) }},
	{"golden-volcano-cron.yaml", "xr-partitioned-scheduled.yaml", func(t *testing.T) []map[string]any { return vcCluster(t) }},
	// A2.10: the descriptor removed (Volcano being uninstalled, CRDs still served).
	{"golden-volcano-absent.yaml", "xr-partitioned-scheduled.yaml", func(t *testing.T) []map[string]any { return vcCluster(t, "adhar-batch-scheduler") }},
}

// vcBranchXR: "xr-scheduled" is Phase 1's single-mode fixture.
func vcBranchXR(t *testing.T, name string) map[string]any {
	if name == "xr-scheduled" {
		return bjXR(t, "xr-scheduled.yaml")
	}
	return vcXR(t, name)
}

// One golden render per branch (PLAT-2.5). ADHAR_UPDATE_GOLDEN=1 rewrites them; read
// the diff before committing one.
func TestVolcanoBatchJobGoldenRenders(t *testing.T) {
	for _, b := range vcBranches {
		t.Run(b.golden, func(t *testing.T) {
			got := vcGolden(t, vcMustRender(t, vcBranchXR(t, b.xr), b.cluster(t), nil))
			path := filepath.Join(vcFixtureDir, b.golden)
			if os.Getenv("ADHAR_UPDATE_GOLDEN") == "1" {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (ADHAR_UPDATE_GOLDEN=1 writes it)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s differs from the render (ADHAR_UPDATE_GOLDEN=1 rewrites it; read the diff first):\n%s", b.golden, lineDiff(string(want), string(got)))
			}
		})
	}
}

func lineDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	var out []string
	for i := 0; i < len(al) || i < len(bl); i++ {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			out = append(out, fmt.Sprintf("%d:\n- %s\n+ %s", i+1, x, y))
		}
		if len(out) > 12 {
			break
		}
	}
	return strings.Join(out, "\n")
}

// The real function containers, every branch: what Crossplane composes is what the
// in-process render composed.
func TestVolcanoBatchJobRendersWithCrossplane(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: skipping `crossplane render`, which starts the function containers")
	}
	bin, err := exec.LookPath("crossplane")
	if err != nil {
		t.Skip("the crossplane CLI is not installed; the in-process renders still ran")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("Docker is not available; `crossplane render` runs each function in a container")
	}
	composition := filepath.Join(controlPlaneDir(t), bjComposition)
	dir := t.TempDir()
	for _, b := range vcBranches {
		t.Run(b.golden, func(t *testing.T) {
			xr := vcBranchXR(t, b.xr)
			cluster := b.cluster(t)
			write := func(name string, docs ...map[string]any) string {
				var parts []string
				for _, d := range docs {
					y, _ := yaml.Marshal(d)
					parts = append(parts, string(y))
				}
				p := filepath.Join(dir, strings.TrimSuffix(b.golden, ".yaml")+"-"+name)
				if err := os.WriteFile(p, []byte(strings.Join(parts, "---\n")), 0o644); err != nil {
					t.Fatal(err)
				}
				return p
			}
			args := []string{"render", write("xr.yaml", xr), composition, filepath.Join(bjFixtureDir, "functions.yaml"), "--required-resources=" + write("required.yaml", cluster...)}
			var stdout, stderr bytes.Buffer
			cmd := exec.Command(bin, args...)
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("crossplane render: %v\n%s", err, stderr.String())
			}
			real := map[string]map[string]any{}
			var status map[string]any
			for _, d := range strings.Split(stdout.String(), "\n---") {
				var m map[string]any
				if err := yaml.Unmarshal([]byte(d), &m); err != nil || len(m) == 0 {
					continue
				}
				if m["kind"] == "BatchJob" {
					status, _ = m["status"].(map[string]any)
					continue
				}
				if n, _ := dig(m, "metadata", "annotations", "crossplane.io/composition-resource-name").(string); n != "" {
					real[n] = m
				}
			}
			inProcess := map[string]map[string]any{}
			for _, d := range vcMustRender(t, xr, cluster, nil) {
				if n, _ := dig(d, "metadata", "annotations", "gotemplating.fn.crossplane.io/composition-resource-name").(string); n != "" {
					inProcess[n] = d
				}
			}
			var a, c []string
			for n := range real {
				a = append(a, n)
			}
			for n := range inProcess {
				c = append(c, n)
			}
			sort.Strings(a)
			sort.Strings(c)
			if !reflect.DeepEqual(a, c) {
				t.Fatalf("crossplane render composes %v, the in-process render %v", a, c)
			}
			for n, d := range inProcess {
				r := real[n]
				if r["apiVersion"] != d["apiVersion"] || r["kind"] != d["kind"] {
					t.Errorf("%s: crossplane render composes %v %v, the in-process render %v %v", n, r["apiVersion"], r["kind"], d["apiVersion"], d["kind"])
				}
				// template.yaml is YAML text: the real engine indents by four, so it is
				// compared as the object it spells.
				parsed := func(m map[string]any) map[string]any {
					data, _ := m["data"].(map[string]any)
					if data == nil {
						return nil
					}
					cp := map[string]any{}
					for k, v := range data {
						cp[k] = v
					}
					if s, ok := cp["template.yaml"].(string); ok {
						var o any
						if err := yaml.Unmarshal([]byte(s), &o); err != nil {
							t.Fatalf("%s template.yaml does not parse: %v", n, err)
						}
						cp["template.yaml"] = o
					}
					return cp
				}
				if tqJSON(parsed(r)) != tqJSON(parsed(d)) {
					t.Errorf("%s.data differs between crossplane render and the in-process render:\n%s\n%s", n, tqJSON(parsed(r)), tqJSON(parsed(d)))
				}
				for _, f := range []string{"spec"} {
					if tqJSON(r[f]) != tqJSON(d[f]) {
						t.Errorf("%s.%s differs between crossplane render and the in-process render:\n%s\n%s", n, f, tqJSON(r[f]), tqJSON(d[f]))
					}
				}
			}
			want := bjStatus(t, vcMustRender(t, xr, cluster, nil))
			for _, k := range []string{"scheduler", "queue", "cronJob", "message"} {
				if fmt.Sprint(status[k]) != fmt.Sprint(want[k]) {
					t.Errorf("status.%s = %v through crossplane render, %v in-process", k, status[k], want[k])
				}
			}
			for _, cond := range []string{"QueueFits", "SchedulerSwitch", "PrerequisitesReady"} {
				var got any
				for _, cc := range asList(status["conditions"]) {
					if m := cc.(map[string]any); m["type"] == cond {
						got = m["reason"]
					}
				}
				if got != bjCondition(t, vcMustRender(t, xr, cluster, nil), cond)["reason"] {
					t.Errorf("%s through crossplane render = %v", cond, got)
				}
			}
		})
	}
}

// A native render is what the live API server accepts (server-side dry run, never
// a write). Opt-in: ADHAR_LIVE_DRY_RUN=1 with a kube context whose cluster has a
// `prod` namespace; the composition lane's PROGRESS.md records the run.
func TestBatchJobNativeRenderPassesAServerDryRun(t *testing.T) {
	if os.Getenv("ADHAR_LIVE_DRY_RUN") != "1" {
		t.Skip("ADHAR_LIVE_DRY_RUN=1 runs `kubectl apply --dry-run=server` against the current context")
	}
	dir := t.TempDir()
	for _, b := range []struct{ name, xr string }{{"single", "xr-scheduled"}, {"partitioned", "xr-partitioned-scheduled.yaml"}} {
		// The fixtures' digests are made up, and the supply-chain policies (mutating,
		// so before schema validation) refuse an unsigned image; the image is not what
		// this checks.
		xr := bjWithParams(vcBranchXR(t, b.xr), map[string]any{"image": "registry.k8s.io/pause:3.10"})
		docs := vcMustRender(t, xr, vcNoVolcano(t), nil)
		run, _ := vcTemplate(t, docs)
		var objs []string
		for _, d := range docs {
			switch d["kind"] {
			case "ExtraResources", "ClaimConditions", "BatchJob":
				continue
			}
			md := d["metadata"].(map[string]any)
			delete(md["annotations"].(map[string]any), "gotemplating.fn.crossplane.io/ready")
			delete(md["annotations"].(map[string]any), "gotemplating.fn.crossplane.io/composition-resource-name")
			y, _ := yaml.Marshal(d)
			objs = append(objs, string(y))
		}
		run["metadata"].(map[string]any)["name"] = "dry-run-" + b.name
		y, _ := yaml.Marshal(run)
		objs = append(objs, string(y))
		p := filepath.Join(dir, b.name+".yaml")
		if err := os.WriteFile(p, []byte(strings.Join(objs, "---\n")), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("kubectl", "apply", "--dry-run=server", "--validate=strict", "-n", "prod", "-f", p).CombinedOutput()
		// adhar-batch-run-integrity may deny the run object (its <name>-run ConfigMap
		// is not on the cluster); a denial by a webhook comes after schema validation.
		if err != nil && !strings.Contains(string(out), "admission webhook") {
			t.Errorf("%s: kubectl apply --dry-run=server: %v\n%s", b.name, err, out)
		}
		t.Logf("%s:\n%s", b.name, out)
	}
}

// The real engine, on the two paths that depend on how Crossplane iterates: a refusal
// that depends on the shape must not fire on the pass that has asked for the leaf
// queue and not yet been given it (a fatal result ends Crossplane's passes), and the
// schedule switch must keep the observed CronJob.
func TestVolcanoBatchJobPassesWithCrossplane(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: skipping `crossplane render`, which starts the function containers")
	}
	bin, err := exec.LookPath("crossplane")
	if err != nil {
		t.Skip("the crossplane CLI is not installed; the in-process renders still ran")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("Docker is not available; `crossplane render` runs each function in a container")
	}
	dir := t.TempDir()
	render := func(name string, xr map[string]any, cluster []map[string]any, observed string) ([]map[string]any, string, error) {
		write := func(file string, docs ...map[string]any) string {
			var parts []string
			for _, d := range docs {
				y, _ := yaml.Marshal(d)
				parts = append(parts, string(y))
			}
			p := filepath.Join(dir, name+"-"+file)
			if err := os.WriteFile(p, []byte(strings.Join(parts, "---\n")), 0o644); err != nil {
				t.Fatal(err)
			}
			return p
		}
		args := []string{"render", write("xr.yaml", xr), filepath.Join(controlPlaneDir(t), bjComposition), filepath.Join(bjFixtureDir, "functions.yaml"),
			"--required-resources=" + write("required.yaml", cluster...)}
		if observed != "" {
			args = append(args, "--observed-resources="+filepath.Join(vcFixtureDir, observed))
		}
		var stdout, stderr bytes.Buffer
		cmd := exec.Command(bin, args...)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		var docs []map[string]any
		for _, d := range strings.Split(stdout.String(), "\n---") {
			var m map[string]any
			if yaml.Unmarshal([]byte(d), &m) == nil && len(m) > 0 {
				docs = append(docs, m)
			}
		}
		return docs, stderr.String(), err
	}
	named := func(docs []map[string]any, res string) map[string]any {
		for _, d := range docs {
			if dig(d, "metadata", "annotations", "crossplane.io/composition-resource-name") == res {
				return d
			}
		}
		return nil
	}
	// 2Gi + 6 × 1Gi = 8Gi fits dev-quota as a gang; 7 × 2Gi as an Indexed Job does not.
	xr := bjWithParams(vcXR(t, "xr-partitioned.yaml"), map[string]any{"workers": 6, "resources": map[string]any{"cpu": "100m", "memory": "2Gi", "workerCpu": "100m", "workerMemory": "1Gi"}})
	docs, stderr, err := render("gang-fits", xr, vcCluster(t), "")
	if err != nil {
		t.Fatalf("a gang that fits its quota was refused by the real engine (a refusal on the pass that only asks?): %v\n%s", err, stderr)
	}
	var kind string
	if cm := named(docs, "run-template"); cm != nil {
		kind = fmt.Sprint(dig(cm, "data", "kind"))
	}
	if kind != "VolcanoJob" {
		t.Errorf("the gang that fits rendered as %q", kind)
	}
	if _, stderr, err := render("indexed-refused", xr, vcNoVolcano(t), ""); err == nil || !strings.Contains(stderr, "dev-quota") {
		t.Errorf("the Indexed Job over its quota was not refused by the real engine: %v\n%s", err, stderr)
	}
	// A leaf that is asked for and not found is still an answer: the job falls back to
	// the Indexed Job, and that is refused.
	if _, stderr, err := render("leaf-missing-refused", xr, vcCluster(t, "acme-payments-dev"), ""); err == nil || !strings.Contains(stderr, "dev-quota") {
		t.Errorf("with the leaf missing the Indexed fallback over its quota was not refused by the real engine: %v\n%s", err, stderr)
	}
	// The switch keeps the observed native CronJob, suspended, while its run is in flight.
	docs, stderr, err = render("switch", vcXR(t, "xr-partitioned-scheduled.yaml"), vcCluster(t), "observed-switch.yaml")
	if err != nil {
		t.Fatalf("crossplane render: %v\n%s", err, stderr)
	}
	kept := named(docs, "schedule-native")
	if kept == nil || named(docs, "schedule-volcano") != nil || dig(kept, "spec", "suspend") != true {
		t.Errorf("through the real engine the switch composes schedule-native %v and schedule-volcano %v", kept, named(docs, "schedule-volcano"))
	}
}
