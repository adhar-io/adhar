package adharplatform

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"text/template"

	sprig "github.com/go-task/slim-sprig/v3"
	"sigs.k8s.io/yaml"
)

// The BatchJob composition (compositions/batchjob/kubernetes.yaml, design 0025 §6.4,
// §6.5) turns a job's definition into a CronJob, the run template every client starts
// a run from, the runner's identity and its alerts. These tests render it the way
// function-go-templating does, in-process with the same template language and through
// `crossplane render` when Docker is available, against testdata/batchjob.

const (
	bjComposition = "compositions/batchjob/kubernetes.yaml"
	bjFixtureDir  = "testdata/batchjob"
)

func bjXR(t *testing.T, name string) map[string]any {
	t.Helper()
	docs := readYAMLDocs(t, filepath.Join(bjFixtureDir, name))
	if len(docs) != 1 || docs[0]["kind"] != "BatchJob" {
		t.Fatalf("%s must hold exactly one BatchJob", name)
	}
	return docs[0]
}

func bjCluster(t *testing.T, without ...string) []map[string]any {
	t.Helper()
	var out []map[string]any
docs:
	for _, d := range readYAMLDocs(t, filepath.Join(bjFixtureDir, "required-resources.yaml")) {
		for _, w := range without {
			if dig(d, "metadata", "name") == w {
				continue docs
			}
		}
		out = append(out, d)
	}
	return out
}

func bjExecute(t *testing.T, req map[string]any) ([]map[string]any, error) {
	t.Helper()
	var comp struct {
		Spec struct {
			Pipeline []struct {
				FunctionRef struct {
					Name string `json:"name"`
				} `json:"functionRef"`
				Input struct {
					Inline struct {
						Template string `json:"template"`
					} `json:"inline"`
				} `json:"input"`
			} `json:"pipeline"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal([]byte(readConfigurationFile(t, bjComposition)), &comp); err != nil {
		t.Fatalf("%s does not parse: %v", bjComposition, err)
	}
	src := ""
	for _, s := range comp.Spec.Pipeline {
		if s.FunctionRef.Name == "function-go-templating" {
			src = s.Input.Inline.Template
		}
	}
	if src == "" {
		t.Fatalf("%s has no function-go-templating step", bjComposition)
	}
	funcs := template.FuncMap{
		"getExtraResources": func(r map[string]any, name string) []any {
			if m, ok := r["requiredResources"].(map[string]any); ok {
				if rs, ok := m[name].(map[string]any); ok {
					items, _ := rs["items"].([]any)
					return items
				}
			}
			return nil
		},
		"toYaml": func(v any) (string, error) { b, err := yaml.Marshal(v); return string(b), err },
	}
	tpl, err := template.New("manifests").Funcs(sprig.TxtFuncMap()).Funcs(funcs).Parse(src)
	if err != nil {
		t.Fatalf("the template does not parse: %v", err)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var in map[string]any
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := tpl.Execute(&out, in); err != nil {
		return nil, err
	}
	var docs []map[string]any
	for _, d := range strings.Split(out.String(), "\n---") {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(d), &m); err != nil {
			t.Fatalf("a rendered document is not YAML: %v\n%s", err, d)
		}
		if len(m) > 0 {
			docs = append(docs, m)
		}
	}
	return docs, nil
}

// bjRender runs the step twice, as Crossplane does: once to learn what it requires,
// then again with it.
func bjRender(t *testing.T, xr map[string]any, cluster []map[string]any, observed map[string]any) ([]map[string]any, error) {
	t.Helper()
	req := map[string]any{"observed": map[string]any{"composite": map[string]any{"resource": xr}, "resources": observed}}
	docs, err := bjExecute(t, req)
	if err != nil {
		return nil, fmt.Errorf("the first pass, which cannot see the namespace, must never refuse: %w", err)
	}
	required := map[string]any{}
	for _, d := range docs {
		if d["kind"] != "ExtraResources" {
			continue
		}
		reqs, _ := d["requirements"].(map[string]any)
		for key, raw := range reqs {
			sel, _ := raw.(map[string]any)
			var items []any
			for _, obj := range cluster {
				if obj["apiVersion"] == sel["apiVersion"] && obj["kind"] == sel["kind"] &&
					dig(obj, "metadata", "name") == sel["matchName"] &&
					fmt.Sprint(dig(obj, "metadata", "namespace")) == fmt.Sprint(sel["namespace"]) {
					items = append(items, map[string]any{"resource": obj})
				}
			}
			required[key] = map[string]any{"items": items}
		}
	}
	req["requiredResources"] = required
	return bjExecute(t, req)
}

func bjMustRender(t *testing.T, xr map[string]any, cluster []map[string]any, observed map[string]any) []map[string]any {
	t.Helper()
	docs, err := bjRender(t, xr, cluster, observed)
	if err != nil {
		t.Fatalf("the composition refused %v: %v", dig(xr, "metadata", "name"), err)
	}
	return docs
}

func bjResource(docs []map[string]any, name string) map[string]any {
	for _, d := range docs {
		if dig(d, "metadata", "annotations", "gotemplating.fn.crossplane.io/composition-resource-name") == name {
			return d
		}
	}
	return nil
}

func bjMustResource(t *testing.T, docs []map[string]any, name string) map[string]any {
	t.Helper()
	d := bjResource(docs, name)
	if d == nil {
		t.Fatalf("the composition did not compose %q", name)
	}
	return d
}

func bjStatus(t *testing.T, docs []map[string]any) map[string]any {
	t.Helper()
	for _, d := range docs {
		if d["kind"] == "BatchJob" {
			s, _ := d["status"].(map[string]any)
			return s
		}
	}
	t.Fatal("the composition writes no status to the BatchJob")
	return nil
}

func bjCondition(t *testing.T, docs []map[string]any, typ string) map[string]any {
	t.Helper()
	for _, d := range docs {
		if d["kind"] != "ClaimConditions" {
			continue
		}
		conds, _ := d["conditions"].([]any)
		for _, c := range conds {
			if m, _ := c.(map[string]any); m["type"] == typ {
				return m
			}
		}
	}
	t.Fatalf("the composition reports no %s condition", typ)
	return nil
}

func bjWithParams(xr map[string]any, set map[string]any) map[string]any {
	raw, _ := json.Marshal(xr)
	var cp map[string]any
	_ = json.Unmarshal(raw, &cp)
	p := dig(cp, "spec", "parameters").(map[string]any)
	for k, v := range set {
		if v == nil {
			delete(p, k)
			continue
		}
		p[k] = v
	}
	return cp
}

func bjWithMeta(xr map[string]any, set map[string]any) map[string]any {
	raw, _ := json.Marshal(xr)
	var cp map[string]any
	_ = json.Unmarshal(raw, &cp)
	m := cp["metadata"].(map[string]any)
	for k, v := range set {
		m[k] = v
	}
	return cp
}

func bjObserved(t *testing.T, file string) map[string]any {
	t.Helper()
	return fnObserved(t, readYAMLDocs(t, filepath.Join(bjFixtureDir, file)))
}

// bjRunTemplate parses the ConfigMap's template.yaml.
func bjRunTemplate(t *testing.T, docs []map[string]any) map[string]any {
	t.Helper()
	cm := bjMustResource(t, docs, "run-template")
	var run map[string]any
	if err := yaml.Unmarshal([]byte(dig(cm, "data", "template.yaml").(string)), &run); err != nil {
		t.Fatalf("template.yaml does not parse: %v", err)
	}
	return run
}

func bjContainer(t *testing.T, jobSpecHolder map[string]any) map[string]any {
	t.Helper()
	cs, _ := dig(jobSpecHolder, "spec", "template", "spec", "containers").([]any)
	if len(cs) != 1 {
		t.Fatalf("a single-pod run has %d containers, want 1", len(cs))
	}
	return cs[0].(map[string]any)
}

// bjEnv maps env names to their literal value or their valueFrom.
func bjEnv(t *testing.T, c map[string]any) map[string]any {
	t.Helper()
	out := map[string]any{}
	env, _ := c["env"].([]any)
	for _, e := range env {
		m := e.(map[string]any)
		name := m["name"].(string)
		if _, dup := out[name]; dup {
			t.Errorf("env %s is set twice", name)
		}
		if v, ok := m["value"]; ok {
			out[name] = v
		} else {
			out[name] = m["valueFrom"]
		}
	}
	return out
}

// Design 0025 §6.4 lists what a scheduled BatchJob composes, and all of it stays in
// the XR's namespace, composed directly.
func TestBatchJobComposesItsObjects(t *testing.T) {
	docs := bjMustRender(t, bjXR(t, "xr-scheduled.yaml"), bjCluster(t), nil)
	want := map[string][2]string{
		"runner-sa":          {"ServiceAccount", "importer-batch"},
		"runner-role":        {"Role", "importer-batch"},
		"runner-rolebinding": {"RoleBinding", "importer-batch"},
		"run-template":       {"ConfigMap", "importer-run"},
		"schedule-native":    {"CronJob", "importer"},
		"rules":              {"PrometheusRule", "importer-batch"},
	}
	for res, w := range want {
		d := bjMustResource(t, docs, res)
		if d["kind"] != w[0] || dig(d, "metadata", "name") != w[1] || dig(d, "metadata", "namespace") != "prod" {
			t.Errorf("%s composed %v %v/%v, want %s prod/%s", res, d["kind"], dig(d, "metadata", "namespace"), dig(d, "metadata", "name"), w[0], w[1])
		}
		if d["kind"] == "Object" {
			t.Errorf("%s is wrapped in a provider-kubernetes Object; nothing here leaves the namespace", res)
		}
		// Every object a hierarchy composition emits carries the hierarchy.
		for k, v := range map[string]string{
			"adhar.io/organisation": "acme", "adhar.io/team": "payments", "adhar.io/project": "shop",
			"adhar.io/environment": "prod", "adhar.io/application": "importer", "adhar.io/plane": "workload",
			"adhar.io/workload": "batch", "adhar.io/batch-job": "importer",
		} {
			if got := dig(d, "metadata", "labels", k); got != v {
				t.Errorf("%s label %s = %v, want %s", res, k, got, v)
			}
		}
	}
	st := bjStatus(t, docs)
	if st["scheduler"] != "kubernetes" || st["runTemplate"] != "importer-run" || st["cronJob"] != "importer" {
		t.Errorf("status = %v", st)
	}

	// On demand: no CronJob, and the schedule's status is not a lie.
	docs = bjMustRender(t, bjXR(t, "xr-ondemand.yaml"), bjCluster(t), nil)
	if bjResource(docs, "schedule-native") != nil {
		t.Error("a job with no schedule composed a CronJob")
	}
	if c := bjCondition(t, docs, "ScheduleReady"); c["status"] != "True" || c["reason"] != "OnDemand" {
		t.Errorf("ScheduleReady of an on-demand job = %v", c)
	}
	if _, set := bjStatus(t, docs)["cronJob"]; set {
		t.Error("status.cronJob names a CronJob that does not exist")
	}
	bjMustResource(t, docs, "run-template")
}

// The pod the design specifies (§6.4), field by field: the Kyverno comparison, the
// runner, the autoscaler and the alerts all depend on these exact values.
func TestBatchJobRunPodContract(t *testing.T) {
	docs := bjMustRender(t, bjXR(t, "xr-scheduled.yaml"), bjCluster(t), nil)
	run := bjRunTemplate(t, docs)
	spec, _ := dig(run, "spec", "template", "spec").(map[string]any)
	for k, v := range map[string]any{
		"restartPolicy": "Never", "priorityClassName": "adhar-batch-critical", "serviceAccountName": "importer-batch",
		"enableServiceLinks": false, "schedulerName": "default-scheduler",
	} {
		if spec[k] != v {
			t.Errorf("pod spec %s = %v, want %v", k, spec[k], v)
		}
	}
	if fmt.Sprint(spec["terminationGracePeriodSeconds"]) != "120" {
		t.Errorf("terminationGracePeriodSeconds = %v", spec["terminationGracePeriodSeconds"])
	}
	if dig(spec, "securityContext", "runAsNonRoot") != true || dig(spec, "securityContext", "seccompProfile", "type") != "RuntimeDefault" {
		t.Errorf("pod securityContext = %v", spec["securityContext"])
	}
	secrets, _ := spec["imagePullSecrets"].([]any)
	if len(secrets) != 1 || dig(secrets[0].(map[string]any), "name") != "harbor-dockercfg" {
		t.Errorf("imagePullSecrets = %v", secrets)
	}

	c := bjContainer(t, run)
	if c["name"] != "batch" {
		// podFailurePolicy.onExitCodes names this container.
		t.Errorf("the container is %v, want batch", c["name"])
	}
	if dig(c, "securityContext", "allowPrivilegeEscalation") != false || fmt.Sprint(dig(c, "securityContext", "capabilities", "drop")) != "[ALL]" {
		t.Errorf("container securityContext = %v", c["securityContext"])
	}
	// Request = limit on memory, no CPU limit unless asked (INV/§6.4).
	if dig(c, "resources", "requests", "memory") != "1536Mi" || dig(c, "resources", "limits", "memory") != "1536Mi" {
		t.Errorf("memory request/limit = %v", c["resources"])
	}
	if dig(c, "resources", "requests", "cpu") != "1" || dig(c, "resources", "limits", "cpu") != nil {
		t.Errorf("cpu request/limit = %v", c["resources"])
	}
	env := bjEnv(t, c)
	for k, v := range map[string]string{
		"ADHAR_BATCH_JOB": "importJob", "ADHAR_BATCH_NAME": "importer", "ADHAR_BATCH_ROLE": "single",
		"ADHAR_BATCH_TRIGGER": "manual", "ADHAR_BATCH_PARAMS": "{}", "ADHAR_BATCH_TIME_ZONE": "Asia/Singapore",
		"ADHAR_BATCH_REPOSITORY_SCHEMA": "batch", "ADHAR_BATCH_SHUTDOWN_TIMEOUT": "90s", "ADHAR_ENVIRONMENT": "prod",
		"OTEL_SERVICE_NAME": "importer", "OTEL_EXPORTER_OTLP_ENDPOINT": "http://alloy.adhar-system.svc.cluster.local:4318",
		"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf", "OTEL_METRICS_EXPORTER": "otlp",
		"BPL_JVM_THREAD_COUNT": "50", "JAVA_TOOL_OPTIONS": "-XX:ReservedCodeCacheSize=64M",
	} {
		if env[k] != v {
			t.Errorf("env %s = %v, want %s", k, env[k], v)
		}
	}
	for k, path := range map[string]string{
		"ADHAR_BATCH_K8S_JOB": "metadata.labels['batch.kubernetes.io/job-name']",
		"ADHAR_BATCH_ACTOR":   "metadata.annotations['adhar.io/batch-actor']",
		"POD_NAME":            "metadata.name", "POD_UID": "metadata.uid", "NODE_NAME": "spec.nodeName",
	} {
		ref, _ := env[k].(map[string]any)
		if dig(ref, "fieldRef", "fieldPath") != path {
			t.Errorf("env %s = %v, want the downward API's %s", k, env[k], path)
		}
		// The API server defaults this on the way in, and the integrity policy
		// compares exactly: it must already be in the template.
		if dig(ref, "fieldRef", "apiVersion") != "v1" {
			t.Errorf("env %s has no fieldRef.apiVersion, so every run would differ from its template", k)
		}
	}
	for k, key := range map[string]string{"SPRING_DATASOURCE_URL": "jdbc-uri", "SPRING_DATASOURCE_USERNAME": "username", "SPRING_DATASOURCE_PASSWORD": "password"} {
		ref, _ := env[k].(map[string]any)
		if dig(ref, "secretKeyRef", "name") != "importer-db-app" || dig(ref, "secretKeyRef", "key") != key {
			t.Errorf("env %s = %v", k, env[k])
		}
	}
	from, _ := c["envFrom"].([]any)
	if len(from) != 1 || dig(from[0].(map[string]any), "secretRef", "name") != "importer-inbound-bucket" {
		t.Errorf("envFrom = %v", c["envFrom"])
	}
	vols, _ := spec["volumes"].([]any)
	if len(vols) != 1 || dig(vols[0].(map[string]any), "emptyDir", "sizeLimit") != "1Gi" {
		t.Errorf("volumes = %v", vols)
	}

	labels, _ := dig(run, "spec", "template", "metadata", "labels").(map[string]any)
	for k, v := range map[string]string{
		"adhar.io/batch-job": "importer", "adhar.io/workload": "batch", "adhar.io/batch-role": "single",
		"adhar.io/organisation": "acme", "adhar.io/team": "payments", "adhar.io/project": "shop",
		"adhar.io/application": "importer", "adhar.io/environment": "prod", "adhar.io/plane": "workload",
	} {
		if labels[k] != v {
			t.Errorf("pod label %s = %v, want %s", k, labels[k], v)
		}
	}
	if _, set := labels["adhar.io/scale-up"]; set {
		t.Error("a job that did not ask to buy nodes carries adhar.io/scale-up")
	}
	ann, _ := dig(run, "spec", "template", "metadata", "annotations").(map[string]any)
	if v, ok := ann["adhar.io/batch-actor"]; !ok || v != "" {
		t.Errorf("the pod template's actor annotation = %v, want the empty value Kyverno overwrites", v)
	}
	if ann["cluster-autoscaler.kubernetes.io/safe-to-evict"] != "false" {
		t.Errorf("safe-to-evict = %v, want \"false\" for disruption: avoid", ann["cluster-autoscaler.kubernetes.io/safe-to-evict"])
	}
}

// The failure policy is the exit-code contract (§5.7): a preemption or a drain is
// not the job's fault, an invalid request or a non-retryable failure must not burn
// retries.
func TestBatchJobFailurePolicyFollowsTheExitCodeContract(t *testing.T) {
	run := bjRunTemplate(t, bjMustRender(t, bjXR(t, "xr-scheduled.yaml"), bjCluster(t), nil))
	spec := run["spec"].(map[string]any)
	if spec["podReplacementPolicy"] != "Failed" {
		t.Errorf("podReplacementPolicy = %v; Failed is the only value permitted with a failure policy", spec["podReplacementPolicy"])
	}
	for k, v := range map[string]string{"backoffLimit": "2", "activeDeadlineSeconds": "21600", "ttlSecondsAfterFinished": "604800"} {
		if fmt.Sprint(spec[k]) != v {
			t.Errorf("%s = %v, want %s (INV-1: a run object does not outlive its TTL)", k, spec[k], v)
		}
	}
	rules, _ := dig(spec, "podFailurePolicy", "rules").([]any)
	if len(rules) != 2 {
		t.Fatalf("podFailurePolicy has %d rules: %v", len(rules), rules)
	}
	// First match wins, so the disruption rule must come first.
	first, second := rules[0].(map[string]any), rules[1].(map[string]any)
	conds, _ := first["onPodConditions"].([]any)
	if first["action"] != "Ignore" || len(conds) != 1 || dig(conds[0].(map[string]any), "type") != "DisruptionTarget" {
		t.Errorf("the first rule is %v, want Ignore on DisruptionTarget", first)
	}
	// Defaulted by the API server, and compared exactly by the integrity policy.
	if dig(conds[0].(map[string]any), "status") != "True" {
		t.Error("onPodConditions[0].status is absent, so every run would differ from its template")
	}
	if second["action"] != "FailJob" || dig(second, "onExitCodes", "containerName") != "batch" || dig(second, "onExitCodes", "operator") != "In" ||
		fmt.Sprint(dig(second, "onExitCodes", "values")) != "[2 3 4 6 7]" {
		t.Errorf("the second rule is %v, want FailJob on 2,3,4,6,7 of container batch", second)
	}
}

// The clock and a person must start the same run (§6.5): the template and the
// CronJob's jobTemplate come from one loop and differ only in the trigger.
func TestBatchJobTemplateAndCronJobAreTheSameRun(t *testing.T) {
	for _, fixture := range []string{"xr-scheduled.yaml"} {
		docs := bjMustRender(t, bjXR(t, fixture), bjCluster(t), nil)
		run := bjRunTemplate(t, docs)
		cron := bjMustResource(t, docs, "schedule-native")
		jt, _ := dig(cron, "spec", "jobTemplate").(map[string]any)
		if run["metadata"].(map[string]any)["name"] != nil {
			t.Error("the run template names itself; a client must choose the name (§8.3)")
		}

		// Normalise the one thing that differs, then they must be byte-equal.
		normalise := func(m map[string]any) string {
			raw, _ := json.Marshal(map[string]any{"labels": dig(m, "metadata", "labels"), "spec": m["spec"]})
			s := string(raw)
			s = strings.ReplaceAll(s, `"adhar.io/batch-trigger":"schedule"`, `"adhar.io/batch-trigger":"X"`)
			s = strings.ReplaceAll(s, `"adhar.io/batch-trigger":"manual"`, `"adhar.io/batch-trigger":"X"`)
			s = strings.ReplaceAll(s, `"name":"ADHAR_BATCH_TRIGGER","value":"schedule"`, `"name":"ADHAR_BATCH_TRIGGER","value":"X"`)
			s = strings.ReplaceAll(s, `"name":"ADHAR_BATCH_TRIGGER","value":"manual"`, `"name":"ADHAR_BATCH_TRIGGER","value":"X"`)
			return s
		}
		if a, b := normalise(run), normalise(jt); a != b {
			t.Errorf("the run template and the CronJob's jobTemplate differ beyond the trigger:\n%s\n%s", a, b)
		}
		if dig(run, "metadata", "labels", "adhar.io/batch-trigger") != "manual" || dig(jt, "metadata", "labels", "adhar.io/batch-trigger") != "schedule" {
			t.Error("the trigger labels are wrong")
		}
		if bjEnv(t, bjContainer(t, run))["ADHAR_BATCH_TRIGGER"] != "manual" || bjEnv(t, bjContainer(t, jt))["ADHAR_BATCH_TRIGGER"] != "schedule" {
			t.Error("the trigger env vars are wrong")
		}

		// The contract's other keys (§6.5).
		cm := bjMustResource(t, docs, "run-template")
		if dig(cm, "data", "kind") != "Job" || dig(cm, "data", "naming") != "v1" {
			t.Errorf("template kind/naming = %v/%v", dig(cm, "data", "kind"), dig(cm, "data", "naming"))
		}
	}
}

// parameters.json is what a client builds a form from, and what names a run: every
// default written out, so it needs no schema of its own.
func TestBatchJobParametersJSONIsComplete(t *testing.T) {
	cm := bjMustResource(t, bjMustRender(t, bjXR(t, "xr-scheduled.yaml"), bjCluster(t), nil), "run-template")
	var params []map[string]any
	if err := json.Unmarshal([]byte(dig(cm, "data", "parameters.json").(string)), &params); err != nil {
		t.Fatalf("parameters.json does not parse: %v", err)
	}
	if len(params) != 3 {
		t.Fatalf("parameters.json holds %d parameters", len(params))
	}
	byName := map[string]map[string]any{}
	for _, p := range params {
		byName[p["name"].(string)] = p
		for _, f := range []string{"type", "identifying", "required", "source", "sensitive"} {
			if _, ok := p[f]; !ok {
				t.Errorf("parameter %v has no %s", p["name"], f)
			}
		}
	}
	if d := byName["businessDate"]; d["type"] != "date" || d["source"] != "schedule" || d["identifying"] != true || d["required"] != false {
		t.Errorf("businessDate = %v", d)
	}
	if s := byName["source"]; s["default"] != "inbound/records.csv" || s["pattern"] != "^[a-zA-Z0-9/._-]+$" || s["source"] != "input" {
		t.Errorf("source = %v", s)
	}
	if d := byName["dryRun"]; d["identifying"] != false || d["type"] != "boolean" || d["default"] != "false" {
		t.Errorf("dryRun = %v; a false `identifying` must survive", d)
	}
}

func TestBatchJobScheduleFollowsTheDefinition(t *testing.T) {
	cron := bjMustResource(t, bjMustRender(t, bjXR(t, "xr-scheduled.yaml"), bjCluster(t), nil), "schedule-native")
	spec := cron["spec"].(map[string]any)
	for k, v := range map[string]string{
		"schedule": "30 1 * * *", "timeZone": "Asia/Singapore", "startingDeadlineSeconds": "3600",
		"concurrencyPolicy": "Forbid", "suspend": "false", "successfulJobsHistoryLimit": "3", "failedJobsHistoryLimit": "3",
	} {
		if fmt.Sprint(spec[k]) != v {
			t.Errorf("CronJob %s = %v, want %s", k, spec[k], v)
		}
	}
	xr := bjWithParams(bjXR(t, "xr-scheduled.yaml"), map[string]any{
		"schedule": map[string]any{"cron": "0 2 * * *", "suspend": true, "concurrencyPolicy": "Allow", "startingDeadlineSeconds": 600},
		"history":  map[string]any{"successfulJobsHistoryLimit": 1, "failedJobsHistoryLimit": 5},
	})
	docs := bjMustRender(t, xr, bjCluster(t), nil)
	spec = bjMustResource(t, docs, "schedule-native")["spec"].(map[string]any)
	for k, v := range map[string]string{
		"timeZone": "UTC", "startingDeadlineSeconds": "600", "concurrencyPolicy": "Allow", "suspend": "true",
		"successfulJobsHistoryLimit": "1", "failedJobsHistoryLimit": "5",
	} {
		if fmt.Sprint(spec[k]) != v {
			t.Errorf("CronJob %s = %v, want %s", k, spec[k], v)
		}
	}
}

// Holding is one annotation, set by an operator; the composition does the rest.
// Argo CD does not revert it, because it is not in Git (§3.4).
func TestBatchJobHoldSuspendsTheSchedule(t *testing.T) {
	docs := bjMustRender(t, bjXR(t, "xr-held.yaml"), bjCluster(t), nil)
	if s := dig(bjMustResource(t, docs, "schedule-native"), "spec", "suspend"); s != true {
		t.Errorf("a held job's CronJob has suspend = %v", s)
	}
	if c := bjCondition(t, docs, "Held"); c["status"] != "True" || !strings.Contains(fmt.Sprint(c["message"]), "quarter-end freeze") {
		t.Errorf("Held = %v", c)
	}
	st := bjStatus(t, docs)
	held, _ := st["held"].(map[string]any)
	if held["reason"] != "quarter-end freeze" || held["since"] == "" || held["since"] == nil {
		t.Errorf("status.held = %v", st["held"])
	}
	// `since` must not move on every reconcile.
	xr := bjXR(t, "xr-held.yaml")
	xr["status"] = map[string]any{"held": map[string]any{"since": "2026-11-01T00:00:00Z"}}
	docs = bjMustRender(t, xr, bjCluster(t), nil)
	if dig(bjStatus(t, docs), "held", "since") != "2026-11-01T00:00:00Z" {
		t.Errorf("status.held.since moved to %v", dig(bjStatus(t, docs), "held", "since"))
	}
	// A hold with no reason is still a hold.
	xr = bjWithMeta(bjXR(t, "xr-scheduled.yaml"), map[string]any{"annotations": map[string]any{"adhar.io/batch-hold": ""}})
	docs = bjMustRender(t, xr, bjCluster(t), nil)
	if dig(bjMustResource(t, docs, "schedule-native"), "spec", "suspend") != true {
		t.Error("an empty hold annotation did not suspend the schedule")
	}
	// And releasing it undoes it.
	docs = bjMustRender(t, bjXR(t, "xr-scheduled.yaml"), bjCluster(t), nil)
	if c := bjCondition(t, docs, "Held"); c["status"] != "False" {
		t.Errorf("Held = %v on a job with no hold", c)
	}
	if _, set := bjStatus(t, docs)["held"]; set {
		t.Error("status.held is set on a job with no hold")
	}
}

// Anything that would otherwise fail in the middle of a night is refused at once,
// with a message that names the fix (§6.4).
func TestBatchJobRefusals(t *testing.T) {
	sched := bjXR(t, "xr-scheduled.yaml")
	ondemand := bjXR(t, "xr-ondemand.yaml")
	for _, c := range []struct {
		name string
		xr   map[string]any
		want string
	}{
		{"name too long", bjWithMeta(ondemand, map[string]any{"name": strings.Repeat("a", 41)}), "at most 40"},
		{"name not DNS-1035", bjWithMeta(ondemand, map[string]any{"name": "1importer"}), "DNS-1035"},
		{"in the platform namespace", bjWithMeta(ondemand, map[string]any{"namespace": "adhar-system"}), "may not live in adhar-system"},
		{"outside an environment namespace", bjWithMeta(ondemand, map[string]any{"namespace": "team-media"}), "not an environment namespace"},
		{"critical outside prod", bjWithParams(ondemand, map[string]any{"priority": "critical"}), "for prod only"},
		{"scaleUp outside prod", bjWithParams(ondemand, map[string]any{"scaleUp": true}), "never buys a node"},
		{"concurrency Replace", bjWithParams(sched, map[string]any{"schedule": map[string]any{"cron": "0 1 * * *", "concurrencyPolicy": "Replace"}}), "SIGTERM a healthy run"},
		{"grace not below the deadline", bjWithParams(ondemand, map[string]any{"timeouts": map[string]any{"activeDeadlineSeconds": 300, "terminationGracePeriodSeconds": 300}}), "must be shorter"},
		{"no image", bjWithParams(ondemand, map[string]any{"image": ""}), "parameters.image"},
		{"no database", bjWithParams(ondemand, map[string]any{"database": map[string]any{}}), "secretRef"},
		{"the runner's own env", bjWithParams(ondemand, map[string]any{"env": map[string]any{"ADHAR_BATCH_PARAMS": "{}"}}), "owned by the runner"},
		{"the datasource env", bjWithParams(ondemand, map[string]any{"env": map[string]any{"SPRING_DATASOURCE_URL": "jdbc:evil"}}), "owned by the runner"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := bjRender(t, c.xr, bjCluster(t), nil)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("rendered without the expected refusal %q: %v", c.want, err)
			}
		})
	}
	// Forty characters is allowed: CronJob names are at most 52 and run names add 9.
	if _, err := bjRender(t, bjWithMeta(ondemand, map[string]any{"name": strings.Repeat("a", 40)}), bjCluster(t), nil); err != nil {
		t.Errorf("a 40-character name was refused: %v", err)
	}
}

// A missing prerequisite must read as one and must not delete anything: removing the
// CronJob would cascade to a run in flight.
func TestBatchJobMissingPrerequisitesSuspendTheScheduleAndSaySo(t *testing.T) {
	docs := bjMustRender(t, bjXR(t, "xr-scheduled.yaml"), bjCluster(t, "adhar-batch-critical"), nil)
	c := bjCondition(t, docs, "PrerequisitesReady")
	if c["status"] != "False" || c["reason"] != "PriorityClassMissing" || !strings.Contains(fmt.Sprint(c["message"]), "adhar-batch-critical") {
		t.Errorf("PrerequisitesReady = %v", c)
	}
	cron := bjMustResource(t, docs, "schedule-native")
	if dig(cron, "spec", "suspend") != true {
		t.Error("the schedule keeps firing runs whose pods the API server will refuse")
	}
	if c := bjCondition(t, docs, "ScheduleReady"); c["status"] != "False" {
		t.Errorf("ScheduleReady = %v", c)
	}
	if !strings.Contains(fmt.Sprint(bjStatus(t, docs)["message"]), "core/adhar-batch") {
		t.Errorf("status.message does not name the package to install: %v", bjStatus(t, docs)["message"])
	}
	// With everything observed the XR is still not Ready while a prerequisite is missing.
	docs = bjMustRender(t, bjXR(t, "xr-scheduled.yaml"), bjCluster(t, "adhar-batch-critical"), bjObserved(t, "observed-importer.yaml"))
	if a := dig(bjMustResource(t, docs, "runner-role"), "metadata", "annotations", "gotemplating.fn.crossplane.io/ready"); a != "False" {
		t.Errorf("runner-role is ready=%v, so the XR would read Ready with a prerequisite missing", a)
	}

	// `standard` needs only its own class.
	docs = bjMustRender(t, bjWithParams(bjXR(t, "xr-scheduled.yaml"), map[string]any{"priority": "standard"}), bjCluster(t, "adhar-batch-critical"), nil)
	if c := bjCondition(t, docs, "PrerequisitesReady"); c["status"] != "True" {
		t.Errorf("a standard job waits on a class it does not use: %v", c)
	}
}

// Without Volcano there is one path. `auto` is that path, and `volcano` is reported
// rather than quietly run somewhere else (D3). A descriptor alone is not Volcano: its
// CRDs must be served too (the Volcano branches: volcano_composition_test.go).
func TestBatchJobSchedulerResolution(t *testing.T) {
	descriptor := map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": "adhar-batch-scheduler", "namespace": "adhar-system"},
		"data":     map[string]any{"scheduler": "volcano"},
	}
	xr := bjXR(t, "xr-ondemand.yaml")
	for _, c := range []struct {
		name          string
		scheduler     any
		cluster       []map[string]any
		wantPrereq    string
		wantMessageIn string
	}{
		{"unset", nil, bjCluster(t), "True", ""},
		{"auto without Volcano", "auto", bjCluster(t), "True", ""},
		{"auto with a descriptor but no Volcano CRDs stays native", "auto", append(bjCluster(t), descriptor), "True", ""},
		{"kubernetes", "kubernetes", append(bjCluster(t), descriptor), "True", ""},
		{"volcano without Volcano", "volcano", bjCluster(t), "False", "not installed"},
		{"volcano with a stale descriptor", "volcano", append(bjCluster(t), descriptor), "False", "CRDs are not served"},
	} {
		t.Run(c.name, func(t *testing.T) {
			docs := bjMustRender(t, bjWithParams(xr, map[string]any{"scheduler": c.scheduler}), c.cluster, nil)
			if got := bjCondition(t, docs, "PrerequisitesReady"); got["status"] != c.wantPrereq || !strings.Contains(fmt.Sprint(got["message"]), c.wantMessageIn) {
				t.Errorf("PrerequisitesReady = %v", got)
			}
			if bjStatus(t, docs)["scheduler"] != "kubernetes" {
				t.Errorf("status.scheduler = %v", bjStatus(t, docs)["scheduler"])
			}
			// INV-3: no pod names Volcano on this path.
			run := bjRunTemplate(t, docs)
			if s := dig(run, "spec", "template", "spec", "schedulerName"); s != "default-scheduler" {
				t.Errorf("schedulerName = %v", s)
			}
		})
	}
}

// §4.7's two autoscaler rules read what this composition stamps.
func TestBatchJobStampsWhatTheAutoscalerReads(t *testing.T) {
	ann := func(xr map[string]any) (map[string]any, map[string]any) {
		run := bjRunTemplate(t, bjMustRender(t, xr, bjCluster(t), nil))
		a, _ := dig(run, "spec", "template", "metadata", "annotations").(map[string]any)
		l, _ := dig(run, "spec", "template", "metadata", "labels").(map[string]any)
		return a, l
	}
	xr := bjXR(t, "xr-scheduled.yaml")
	if a, _ := ann(bjWithParams(xr, map[string]any{"disruption": "allow"})); a["cluster-autoscaler.kubernetes.io/safe-to-evict"] != nil {
		t.Errorf("disruption: allow still stamped safe-to-evict = %v", a["cluster-autoscaler.kubernetes.io/safe-to-evict"])
	}
	if _, l := ann(bjWithParams(xr, map[string]any{"scaleUp": true})); l["adhar.io/scale-up"] != "true" {
		t.Errorf("scaleUp: true did not stamp adhar.io/scale-up: %v", l)
	}
}

func TestBatchJobOverrides(t *testing.T) {
	xr := bjWithParams(bjXR(t, "xr-ondemand.yaml"), map[string]any{
		"application":        "settlement",
		"serviceAccountName": "settlement-runner",
		"imagePullSecrets":   []any{},
		"resources":          map[string]any{"cpu": "2", "memory": "4Gi", "cpuLimit": "4", "tmpSize": "5Gi"},
		"jvm":                map[string]any{"threads": 120, "codeCache": "128M", "options": "-XX:+UseZGC"},
		"retry":              map[string]any{"backoffLimit": 0},
		"timeouts":           map[string]any{"activeDeadlineSeconds": 7200, "terminationGracePeriodSeconds": 300},
		"history":            map[string]any{"ttlSecondsAfterFinished": 3600},
		"database":           map[string]any{"secretRef": "shared-db", "schema": "settlement_batch"},
		"env":                map[string]any{"OTEL_SERVICE_NAME": "settlement", "LOG_LEVEL": "debug"},
		"priority":           "standard",
	})
	docs := bjMustRender(t, xr, bjCluster(t), nil)
	run := bjRunTemplate(t, docs)
	spec := run["spec"].(map[string]any)
	pod, _ := dig(spec, "template", "spec").(map[string]any)
	if pod["serviceAccountName"] != "settlement-runner" {
		t.Errorf("serviceAccountName = %v", pod["serviceAccountName"])
	}
	if bjResource(docs, "runner-sa") != nil {
		t.Error("a ServiceAccount was composed although the job names its own")
	}
	// Its own ServiceAccount still gets the runner's Role, or the two GETs fail.
	subjects, _ := dig(bjMustResource(t, docs, "runner-rolebinding"), "subjects").([]any)
	if len(subjects) != 1 || dig(subjects[0].(map[string]any), "name") != "settlement-runner" {
		t.Errorf("RoleBinding subjects = %v", subjects)
	}
	if _, set := pod["imagePullSecrets"]; set {
		t.Error("imagePullSecrets: [] still names a pull secret")
	}
	if dig(run, "metadata", "labels", "adhar.io/application") != "settlement" {
		t.Errorf("application label = %v", dig(run, "metadata", "labels", "adhar.io/application"))
	}
	c := bjContainer(t, run)
	if dig(c, "resources", "requests", "cpu") != "2" || dig(c, "resources", "limits", "cpu") != "4" || dig(c, "resources", "limits", "memory") != "4Gi" {
		t.Errorf("resources = %v", c["resources"])
	}
	vols, _ := pod["volumes"].([]any)
	if dig(vols[0].(map[string]any), "emptyDir", "sizeLimit") != "5Gi" {
		t.Errorf("volumes = %v", vols)
	}
	env := bjEnv(t, c)
	for k, v := range map[string]string{
		"BPL_JVM_THREAD_COUNT": "120", "JAVA_TOOL_OPTIONS": "-XX:ReservedCodeCacheSize=128M -XX:+UseZGC",
		"ADHAR_BATCH_REPOSITORY_SCHEMA": "settlement_batch", "OTEL_SERVICE_NAME": "settlement", "LOG_LEVEL": "debug",
		// The kit's graceful stop stays inside the grace period, whatever it is.
		"ADHAR_BATCH_SHUTDOWN_TIMEOUT": "270s",
	} {
		if env[k] != v {
			t.Errorf("env %s = %v, want %s", k, env[k], v)
		}
	}
	for k, v := range map[string]string{"backoffLimit": "0", "activeDeadlineSeconds": "7200", "ttlSecondsAfterFinished": "3600"} {
		if fmt.Sprint(spec[k]) != v {
			t.Errorf("%s = %v, want %s", k, spec[k], v)
		}
	}
	if fmt.Sprint(pod["terminationGracePeriodSeconds"]) != "300" {
		t.Errorf("grace = %v", pod["terminationGracePeriodSeconds"])
	}
}

// Readiness: each statusless kind is ready once it exists, so the XR is not Ready on
// the first pass and is on the second (auto-ready would otherwise hold it forever).
func TestBatchJobStatuslessKindsAreReadyOnceObserved(t *testing.T) {
	names := []string{"runner-sa", "runner-role", "runner-rolebinding", "run-template", "schedule-native", "rules"}
	first := bjMustRender(t, bjXR(t, "xr-scheduled.yaml"), bjCluster(t), nil)
	second := bjMustRender(t, bjXR(t, "xr-scheduled.yaml"), bjCluster(t), bjObserved(t, "observed-importer.yaml"))
	for _, n := range names {
		const a = "gotemplating.fn.crossplane.io/ready"
		if got := dig(bjMustResource(t, first, n), "metadata", "annotations", a); got != "False" {
			t.Errorf("%s on the first pass: ready = %v", n, got)
		}
		if got := dig(bjMustResource(t, second, n), "metadata", "annotations", a); got != "True" {
			t.Errorf("%s once observed: ready = %v, and function-auto-ready would hold the XR unready forever", n, got)
		}
	}
	if c := bjCondition(t, second, "TemplateReady"); c["status"] != "True" {
		t.Errorf("TemplateReady = %v", c)
	}
	if c := bjCondition(t, second, "ScheduleReady"); c["status"] != "True" || c["reason"] != "Scheduled" {
		t.Errorf("ScheduleReady = %v", c)
	}
}

// Status is what the CronJob reports.
func TestBatchJobStatusComesFromTheCronJob(t *testing.T) {
	st := bjStatus(t, bjMustRender(t, bjXR(t, "xr-scheduled.yaml"), bjCluster(t), bjObserved(t, "observed-importer.yaml")))
	if st["lastScheduleTime"] != "2026-11-05T17:30:00Z" || st["lastSuccessfulTime"] != "2026-11-05T17:43:36Z" {
		t.Errorf("status = %v", st)
	}
	if fmt.Sprint(st["activeRuns"]) != "[importer-29374530]" {
		t.Errorf("activeRuns = %v", st["activeRuns"])
	}
}

// An environment namespace IS the environment; a disagreeing parameter is reported.
func TestBatchJobPlacementFollowsTheNamespace(t *testing.T) {
	xr := bjWithParams(bjXR(t, "xr-scheduled.yaml"), map[string]any{"environment": "dev", "team": "someone-else"})
	docs := bjMustRender(t, xr, bjCluster(t), nil)
	run := bjRunTemplate(t, docs)
	if dig(run, "metadata", "labels", "adhar.io/environment") != "prod" || dig(run, "metadata", "labels", "adhar.io/team") != "payments" {
		t.Errorf("the labels followed the parameters: %v", dig(run, "metadata", "labels"))
	}
	c := bjCondition(t, docs, "Placement")
	if c["reason"] != "ParameterOverridden" || !strings.Contains(fmt.Sprint(c["message"]), `environment "dev"`) {
		t.Errorf("Placement = %v", c)
	}
	// A cadence under ten minutes is accepted, and the cost is said out loud (§14).
	for _, cron := range []string{"* * * * *", "*/5 * * * *", "0,5,10,15,20,25,30,35,40,45,50,55 * * * *"} {
		docs = bjMustRender(t, bjWithParams(bjXR(t, "xr-scheduled.yaml"), map[string]any{"schedule": map[string]any{"cron": cron}}), bjCluster(t), nil)
		if c := bjCondition(t, docs, "Placement"); !strings.Contains(fmt.Sprint(c["message"]), "JVM start") {
			t.Errorf("cron %q: Placement does not report the JVM-per-run cost: %v", cron, c)
		}
	}
	docs = bjMustRender(t, bjWithParams(bjXR(t, "xr-scheduled.yaml"), map[string]any{"schedule": map[string]any{"cron": "*/15 * * * *"}}), bjCluster(t), nil)
	if c := bjCondition(t, docs, "Placement"); strings.Contains(fmt.Sprint(c["message"]), "JVM start") {
		t.Errorf("a 15-minute cadence was warned about: %v", c)
	}
}

// The alerts of §11.3 and the label join they depend on.
func TestBatchJobAlerts(t *testing.T) {
	rulesOf := func(docs []map[string]any) map[string]map[string]any {
		out := map[string]map[string]any{}
		groups, _ := dig(bjMustResource(t, docs, "rules"), "spec", "groups").([]any)
		for _, g := range groups {
			rules, _ := g.(map[string]any)["rules"].([]any)
			for _, r := range rules {
				m := r.(map[string]any)
				out[m["alert"].(string)] = m
			}
		}
		return out
	}
	docs := bjMustRender(t, bjXR(t, "xr-scheduled.yaml"), bjCluster(t), nil)
	rules := rulesOf(docs)
	for alert, wantExpr := range map[string][]string{
		"BatchRunFailed":       {`kube_job_failed{namespace="prod",condition="true"}`, `label_adhar_io_batch_job="importer"`, "topk by"},
		"BatchScheduleMissed":  {`kube_cronjob_next_schedule_time{namespace="prod",cronjob="importer"}`, "kube_cronjob_spec_suspend", "600", "kube_cronjob_status_active"},
		"BatchNoRecentSuccess": {`kube_cronjob_status_last_successful_time{namespace="prod",cronjob="importer"}`, "> 93600", "kube_cronjob_spec_suspend"},
		// sla.maxDuration 3h.
		"BatchRunTooLong":      {"kube_job_status_start_time", "> 10800", "kube_job_status_active", `label_adhar_io_batch_job="importer"`},
		"BatchRunStuckPending": {`phase="Pending"`, `label_adhar_io_batch_job="importer"`},
		// A run pod is restartPolicy: Never: its OOM is its CURRENT state, and
		// last_terminated_reason alone would never fire.
		"BatchRunOOMKilled": {`kube_pod_container_status_terminated_reason{namespace="prod",reason="OOMKilled"}`, `label_adhar_io_batch_job="importer"`},
	} {
		r := rules[alert]
		if r == nil {
			t.Errorf("no %s alert", alert)
			continue
		}
		expr := fmt.Sprint(r["expr"])
		for _, w := range wantExpr {
			if !strings.Contains(expr, w) {
				t.Errorf("%s does not consider %s: %s", alert, w, expr)
			}
		}
		labels, _ := r["labels"].(map[string]any)
		if labels["severity"] != "critical" || labels["team"] != "payments" || labels["adhar_batch_job"] != "importer" {
			t.Errorf("%s labels = %v; prod is critical, and the team label routes it", alert, labels)
		}
	}
	if f := rules["BatchRunStuckPending"]["for"]; f != "15m" {
		t.Errorf("BatchRunStuckPending for = %v", f)
	}
	if f := rules["BatchRunFailed"]["for"]; f != "1m" {
		t.Errorf("BatchRunFailed for = %v", f)
	}

	// On demand: nothing to miss, and warning outside prod.
	docs = bjMustRender(t, bjXR(t, "xr-ondemand.yaml"), bjCluster(t), nil)
	rules = rulesOf(docs)
	if rules["BatchScheduleMissed"] != nil || rules["BatchNoRecentSuccess"] != nil {
		t.Error("an on-demand job has schedule alerts; they can only ever misfire")
	}
	if dig(rules["BatchRunFailed"], "labels", "severity") != "warning" {
		t.Errorf("severity outside prod = %v", dig(rules["BatchRunFailed"], "labels", "severity"))
	}
	if !strings.Contains(fmt.Sprint(rules["BatchRunTooLong"]["expr"]), "> 21600") {
		t.Errorf("maxDuration must default to the deadline: %v", rules["BatchRunTooLong"]["expr"])
	}

	// Explicit severity and staleAfter; alerts can be turned off.
	docs = bjMustRender(t, bjWithParams(bjXR(t, "xr-scheduled.yaml"), map[string]any{"alerts": map[string]any{"severity": "warning", "staleAfter": "1h30m"}}), bjCluster(t), nil)
	rules = rulesOf(docs)
	if dig(rules["BatchRunFailed"], "labels", "severity") != "warning" || !strings.Contains(fmt.Sprint(rules["BatchNoRecentSuccess"]["expr"]), "> 5400") {
		t.Errorf("alerts settings ignored: %v", rules["BatchNoRecentSuccess"])
	}
	docs = bjMustRender(t, bjWithParams(bjXR(t, "xr-scheduled.yaml"), map[string]any{"alerts": map[string]any{"enabled": false}}), bjCluster(t), nil)
	if bjResource(docs, "rules") != nil {
		t.Error("alerts.enabled: false still composed the rules")
	}
}

// Crossplane composes these with its own identity. A kind it may not manage is a 403
// the XR reports only as a generic sync error, and a Role can grant only what its
// creator holds.
func TestBatchJobComposedKindsAreGrantedToCrossplane(t *testing.T) {
	var rules []struct {
		APIGroups []string `json:"apiGroups"`
		Resources []string `json:"resources"`
		Verbs     []string `json:"verbs"`
	}
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
	for _, k := range [][2]string{
		{"", "serviceaccounts"}, {"", "configmaps"}, {"rbac.authorization.k8s.io", "roles"}, {"rbac.authorization.k8s.io", "rolebindings"},
		{"batch", "cronjobs"}, {"monitoring.coreos.com", "prometheusrules"},
	} {
		for _, v := range []string{"get", "list", "watch", "create", "patch", "delete"} {
			if !holds(k[0], k[1], v) {
				t.Errorf("Crossplane may not %s %s.%s, which the BatchJob composition emits", v, k[1], k[0])
			}
		}
	}
	// The runner's Role names these; Crossplane must hold them to grant them.
	role := bjMustResource(t, bjMustRender(t, bjXR(t, "xr-scheduled.yaml"), bjCluster(t), nil), "runner-role")
	roleRules, _ := role["rules"].([]any)
	for _, r := range roleRules {
		m := r.(map[string]any)
		for _, g := range m["apiGroups"].([]any) {
			for _, res := range m["resources"].([]any) {
				for _, v := range m["verbs"].([]any) {
					if !holds(g.(string), res.(string), v.(string)) {
						t.Errorf("the runner's Role grants %s on %s.%s, which Crossplane does not hold: the API server refuses it as privilege escalation", v, res, g)
					}
				}
			}
		}
		if fmt.Sprint(m["verbs"]) != "[get]" {
			t.Errorf("the runner's Role grants %v; the runner makes two GETs", m["verbs"])
		}
	}
	if !holds("scheduling.k8s.io", "priorityclasses", "get") {
		t.Error("Crossplane may not read PriorityClasses, so every BatchJob would read as missing its prerequisite")
	}
}

// D3: the schema is strict and carries only what the composition renders: Phase 1's
// fields and Phase 2's partitioned shape, nothing of Phase 3.
func TestBatchJobXRDCarriesThePhaseOneFields(t *testing.T) {
	docs := readYAMLDocs(t, filepath.Join(controlPlaneDir(t), "xrd/batchjob.xrd.yaml"))
	if len(docs) != 1 {
		t.Fatal("batchjob.xrd.yaml must hold one XRD")
	}
	x := docs[0]
	if x["apiVersion"] != "apiextensions.crossplane.io/v2" || dig(x, "spec", "scope") != "Namespaced" ||
		dig(x, "spec", "names", "kind") != "BatchJob" || dig(x, "spec", "defaultCompositionRef", "name") != "batchjob-kubernetes" {
		t.Errorf("the XRD's identity changed: %v", x["spec"])
	}
	v0 := dig(x, "spec", "versions").([]any)[0].(map[string]any)
	params := dig(v0, "schema", "openAPIV3Schema", "properties", "spec", "properties", "parameters", "properties").(map[string]any)
	for _, f := range []string{"displayName", "application", "job", "image", "imagePullSecrets", "scheduler", "schedule", "parameters", "resources", "jvm",
		"retry", "timeouts", "history", "priority", "disruption", "scaleUp", "database", "env", "envFrom", "serviceAccountName", "sla", "alerts",
		"organisation", "team", "project", "environment", "mode", "workers", "gridSize"} {
		if params[f] == nil {
			t.Errorf("parameter %s is missing", f)
		}
	}
	for path, fields := range map[string][]string{"resources": {"workerCpu", "workerMemory"}, "retry": {"maxRetry"}} {
		sub, _ := dig(params, path, "properties").(map[string]any)
		for _, f := range fields {
			if sub[f] == nil {
				t.Errorf("%s.%s is missing; Phase 2's composition renders it", path, f)
			}
		}
	}
	for _, f := range []string{"dependsOn", "retention", "smoke"} {
		if params[f] != nil {
			t.Errorf("parameter %s arrives with a later phase (D3): an earlier control plane must reject it, not ignore it", f)
		}
	}
	for path, fields := range map[string][]string{
		"schedule": {"calendar"}, "sla": {"finishBy", "timeZone"},
	} {
		sub, _ := dig(params, path, "properties").(map[string]any)
		for _, f := range fields {
			if sub[f] != nil {
				t.Errorf("%s.%s arrives with a later phase (D3)", path, f)
			}
		}
	}
	if fmt.Sprint(dig(params, "schedule", "properties", "concurrencyPolicy", "enum")) != "[Forbid Allow]" {
		t.Errorf("concurrencyPolicy enum = %v", dig(params, "schedule", "properties", "concurrencyPolicy", "enum"))
	}
	status := dig(v0, "schema", "openAPIV3Schema", "properties", "status", "properties").(map[string]any)
	for _, f := range []string{"scheduler", "queue", "runTemplate", "cronJob", "lastScheduleTime", "lastSuccessfulTime", "activeRuns", "held", "message"} {
		if status[f] == nil {
			t.Errorf("status.%s is not in the schema; the composition writes it and the API server would prune it", f)
		}
	}
	// Whatever the composition writes must survive the schema.
	for k := range bjStatus(t, bjMustRender(t, bjXR(t, "xr-held.yaml"), bjCluster(t), nil)) {
		if status[k] == nil {
			t.Errorf("the composition writes status.%s, which the schema prunes", k)
		}
	}
	// The fixtures a person would apply use only declared fields.
	for _, fixture := range []string{"xr-scheduled.yaml", "xr-ondemand.yaml", "xr-held.yaml"} {
		for k := range dig(bjXR(t, fixture), "spec", "parameters").(map[string]any) {
			if params[k] == nil {
				t.Errorf("%s uses parameters.%s, which the XRD does not define", fixture, k)
			}
		}
	}
}

// Argo CD treats a kind it has no health check for as Healthy on creation. All four
// files: the install manifests are generated from the values, and in this tree they
// were hand-edited in step.
func TestArgoCDAssessesBatchJobHealth(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{
		"platform/controllers/adharplatform/resources/argocd/install.yaml",
		"platform/controllers/adharplatform/resources/argocd/install-ha.yaml",
		"hack/argocd/values.yaml",
		"hack/argocd/values-ha.yaml",
	} {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		i := strings.Index(s, "resource.customizations.health.platform.adhar.io_BatchJob: |")
		if i < 0 {
			t.Errorf("%s has no health check for platform.adhar.io/BatchJob", f)
			continue
		}
		body := s[i:]
		if j := strings.Index(body[1:], "resource.customizations."); j > 0 {
			body = body[:j]
		}
		for _, want := range []string{
			`synced.reason == "ReconcileError"`, `prerequisites.status == "False"`, `ready.status == "True"`,
			`held.status == "True"`, `"Progressing"`, `"Degraded"`, `"Healthy"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: the BatchJob health check does not consider %s", f, want)
			}
		}
		// A run failing is an alert, never health: this must read nothing about runs,
		// or a failed night would hold a promotion back (§6.6, A1.4).
		if strings.Contains(body, "lastSuccessfulTime") || strings.Contains(body, "activeRuns") {
			t.Errorf("%s: the BatchJob health check reads run state", f)
		}
	}
}

func TestBatchJobRenderFixturesTrackTheInstalledFunctions(t *testing.T) {
	installed := map[string]string{}
	for _, d := range readYAMLDocs(t, filepath.Join(controlPlaneDir(t), "functions/functions.yaml")) {
		installed[dig(d, "metadata", "name").(string)], _ = dig(d, "spec", "package").(string)
	}
	fixtures := map[string]string{}
	for _, d := range readYAMLDocs(t, filepath.Join(bjFixtureDir, "functions.yaml")) {
		fixtures[dig(d, "metadata", "name").(string)], _ = dig(d, "spec", "package").(string)
	}
	var comp map[string]any
	if err := yaml.Unmarshal([]byte(readConfigurationFile(t, bjComposition)), &comp); err != nil {
		t.Fatal(err)
	}
	for _, s := range dig(comp, "spec", "pipeline").([]any) {
		name := dig(s.(map[string]any), "functionRef", "name").(string)
		if installed[name] == "" {
			t.Errorf("the composition runs %s, which the platform does not install", name)
		}
		if fixtures[name] != installed[name] {
			t.Errorf("testdata runs %s as %q, the platform as %q", name, fixtures[name], installed[name])
		}
	}
}

// The real function containers, the same path Crossplane takes, every §6.3 example.
func TestBatchJobCompositionRendersWithCrossplane(t *testing.T) {
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
	render := func(xr string, extra ...string) []map[string]any {
		t.Helper()
		args := append([]string{"render", filepath.Join(bjFixtureDir, xr), composition, filepath.Join(bjFixtureDir, "functions.yaml"),
			"--required-resources=" + filepath.Join(bjFixtureDir, "required-resources.yaml")}, extra...)
		var stdout, stderr bytes.Buffer
		cmd := exec.Command(bin, args...)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("crossplane render %s: %v\n%s", xr, err, stderr.String())
		}
		var docs []map[string]any
		for _, d := range strings.Split(stdout.String(), "\n---") {
			var m map[string]any
			if err := yaml.Unmarshal([]byte(d), &m); err == nil && len(m) > 0 {
				docs = append(docs, m)
			}
		}
		return docs
	}
	names := func(docs []map[string]any) string {
		var out []string
		for _, d := range docs {
			if n, _ := dig(d, "metadata", "annotations", "crossplane.io/composition-resource-name").(string); n != "" {
				out = append(out, n)
			}
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	readyOf := func(docs []map[string]any) any {
		for _, d := range docs {
			if d["kind"] != "BatchJob" {
				continue
			}
			for _, c := range dig(d, "status", "conditions").([]any) {
				if m := c.(map[string]any); m["type"] == "Ready" {
					return m["status"]
				}
			}
		}
		t.Fatal("the rendered BatchJob has no Ready condition")
		return nil
	}
	first := render("xr-scheduled.yaml")
	if got := names(first); got != "rules,run-template,runner-role,runner-rolebinding,runner-sa,schedule-native" {
		t.Errorf("a scheduled job composes %s", got)
	}
	if r := readyOf(first); r != "False" {
		t.Errorf("before anything is observed the BatchJob is Ready=%v", r)
	}
	second := render("xr-scheduled.yaml", "--observed-resources="+filepath.Join(bjFixtureDir, "observed-importer.yaml"))
	if r := readyOf(second); r != "True" {
		t.Errorf("with everything observed the BatchJob is Ready=%v", r)
	}
	if got := names(render("xr-ondemand.yaml")); got != "rules,run-template,runner-role,runner-rolebinding,runner-sa" {
		t.Errorf("an on-demand job composes %s", got)
	}
	// The real engine's template.yaml parses and is the object the in-process render saw.
	for _, d := range first {
		if d["kind"] == "ConfigMap" {
			var run map[string]any
			if err := yaml.Unmarshal([]byte(dig(d, "data", "template.yaml").(string)), &run); err != nil {
				t.Fatal(err)
			}
			if !regexp.MustCompile(`^importer$`).MatchString(fmt.Sprint(dig(run, "metadata", "labels", "adhar.io/batch-job"))) {
				t.Errorf("template.yaml = %v", run)
			}
		}
	}
	for _, d := range render("xr-held.yaml") {
		if d["kind"] == "CronJob" && dig(d, "spec", "suspend") != true {
			t.Errorf("a held job through the real functions: suspend = %v", dig(d, "spec", "suspend"))
		}
	}
}
