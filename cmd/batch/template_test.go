package batch

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func envMap(c map[string]interface{}) map[string]string {
	out := map[string]string{}
	env, _ := c["env"].([]interface{})
	for _, e := range env {
		m := e.(map[string]interface{})
		v, _ := m["value"].(string)
		out[m["name"].(string)] = v
	}
	return out
}

// adhar-batch-run-integrity compares the run with the template and refuses any
// difference but the name, the trigger label and the client variables (§6.5).
// So buildRun must change exactly those, on exactly the containers that carry
// the contract — a run that differs anywhere else is one nobody can start.
func TestBuildRunChangesOnlyWhatTheIntegrityPolicyAllows(t *testing.T) {
	tpl := mustParseTemplate(t, importerParams)
	if tpl.Kind != "Job" || tpl.Naming != "v1" || len(tpl.Fields) != 5 {
		t.Fatalf("template = kind %s naming %s fields %d", tpl.Kind, tpl.Naming, len(tpl.Fields))
	}
	before := tpl.Manifest.DeepCopy()
	run := buildRun(tpl, "importer-09932cb3", "prod", "manual", map[string]string{"businessDate": "2026-11-03"}, "")
	if !reflect.DeepEqual(before.Object, tpl.Manifest.Object) {
		t.Fatal("buildRun must not mutate the template it was given")
	}
	if run.GetName() != "importer-09932cb3" || run.GetNamespace() != "prod" {
		t.Errorf("name/namespace = %s/%s", run.GetNamespace(), run.GetName())
	}
	if run.GetLabels()[labelTrigger] != "manual" {
		t.Errorf("trigger label = %q", run.GetLabels()[labelTrigger])
	}
	cs := containersOf(run)
	batch, sidecar := envMap(cs[0]), envMap(cs[1])
	if batch[envParams] != `{"businessDate":"2026-11-03"}` || batch[envTrigger] != "manual" {
		t.Errorf("batch container env = %v", batch)
	}
	if _, has := batch[envRestartOf]; has {
		t.Errorf("a manual run names no run it restarts: %v", batch)
	}
	if !reflect.DeepEqual(sidecar, map[string]string{"OTHER": "x"}) {
		t.Errorf("the sidecar carries no batch contract and must not gain one: %v", sidecar)
	}

	// Everything else is byte-for-byte the template: strip what a client may
	// set from both and compare the rest.
	strip := func(o *unstructured.Unstructured) map[string]interface{} {
		c := o.DeepCopy()
		unstructured.RemoveNestedField(c.Object, "metadata", "name")
		unstructured.RemoveNestedField(c.Object, "metadata", "namespace")
		unstructured.RemoveNestedField(c.Object, "metadata", "labels", labelTrigger)
		unstructured.RemoveNestedField(c.Object, "spec", "template", "metadata", "labels", labelTrigger)
		for _, cont := range containersOf(c) {
			var kept []interface{}
			env, _ := cont["env"].([]interface{})
			for _, e := range env {
				name := e.(map[string]interface{})["name"]
				if name != envParams && name != envTrigger && name != envRestartOf {
					kept = append(kept, e)
				}
			}
			cont["env"] = kept
		}
		return c.Object
	}
	if a, b := strip(tpl.Manifest), strip(run); !reflect.DeepEqual(a, b) {
		aj, _ := json.Marshal(a)
		bj, _ := json.Marshal(b)
		t.Errorf("the run differs from the template beyond what Kyverno admits:\n%s\n%s", aj, bj)
	}

	restart := buildRun(tpl, "importer-09932cb3-r1", "prod", "restart", map[string]string{"businessDate": "2026-11-03"}, "importer-09932cb3")
	env := envMap(containersOf(restart)[0])
	if env[envTrigger] != "restart" || env[envRestartOf] != "importer-09932cb3" {
		t.Errorf("a restart names its trigger and the run it continues: %v", env)
	}
	if l, _, _ := unstructured.NestedString(restart.Object, "spec", "template", "metadata", "labels", labelTrigger); l != "restart" {
		t.Errorf("the pod template's trigger label follows the run's: %q", l)
	}
	if gvrOfRun(run) != jobGVR {
		t.Errorf("a batch/v1 Job is created as jobs.batch, got %v", gvrOfRun(run))
	}
}

func TestRunTemplateRefusesWhatItCannotHonour(t *testing.T) {
	cm := templateConfigMap(importerParams)
	cm.Object["data"].(map[string]interface{})["naming"] = "v2"
	if _, err := parseRunTemplate(cm); err == nil || !strings.Contains(err.Error(), "naming v1") {
		t.Errorf("a naming version this client does not implement must be refused: %v", err)
	}
	cm = templateConfigMap(importerParams)
	delete(cm.Object["data"].(map[string]interface{}), "template.yaml")
	if _, err := parseRunTemplate(cm); err == nil {
		t.Error("no template.yaml must be an error")
	}
	cm = templateConfigMap(importerParams)
	cm.Object["data"].(map[string]interface{})["template.yaml"] = `{"apiVersion":"v1","kind":"ConfigMap"}`
	if _, err := parseRunTemplate(cm); err == nil {
		t.Error("a template that is not a Job must be an error")
	}
	// JSON is YAML, and the composition writes JSON: both forms read the same.
	cm = templateConfigMap(importerParams)
	asJSON, _ := json.Marshal(mustParseTemplate(t, importerParams).Manifest.Object)
	cm.Object["data"].(map[string]interface{})["template.yaml"] = string(asJSON)
	if tpl, err := parseRunTemplate(cm); err != nil || runImage(tpl.Manifest) == "" {
		t.Errorf("a JSON template.yaml must read: %v", err)
	}
	// A parameters.json that cannot be read is reported with the template, not swallowed.
	tpl, err := parseRunTemplate(templateConfigMap("{not json"))
	if err != nil || tpl.ParamsError == "" {
		t.Errorf("a broken parameters.json must be reported: %v %+v", err, tpl)
	}
}

func TestRunParamsReadTheRunsOwnVariables(t *testing.T) {
	run := jobObject("importer-09932cb3", "prod", "importer", "failed", testNow)
	if p := runParams(run); p["businessDate"] != "2026-11-03" || p["region"] != "sg" {
		t.Errorf("runParams = %v", p)
	}
	if envOf(run, envTimeZone) != "Asia/Singapore" {
		t.Errorf("envOf time zone = %q", envOf(run, envTimeZone))
	}
	if runImage(run) != "img" {
		t.Errorf("runImage = %q", runImage(run))
	}
}
