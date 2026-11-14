package batch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

func loadRunTemplate(t *testing.T) *RunTemplate {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "run-template.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cm struct {
		Data map[string]string `json:"data"`
	}
	if err := yaml.Unmarshal(raw, &cm); err != nil {
		t.Fatal(err)
	}
	tpl, err := ParseRunTemplate(cm.Data)
	if err != nil {
		t.Fatal(err)
	}
	return tpl
}

// withoutClientFields strips exactly what §6.5 lets a client change, so what is
// left must be the template unchanged.
func withoutClientFields(t *testing.T, run *unstructured.Unstructured) map[string]interface{} {
	t.Helper()
	o := run.DeepCopy().Object
	unstructured.RemoveNestedField(o, "metadata", "name")
	unstructured.RemoveNestedField(o, "metadata", "namespace")
	unstructured.RemoveNestedField(o, "metadata", "labels", labelTrigger)
	unstructured.RemoveNestedField(o, "spec", "template", "metadata", "labels", labelTrigger)
	containers, _, _ := unstructured.NestedSlice(o, "spec", "template", "spec", "containers")
	for _, c := range containers {
		m := c.(map[string]interface{})
		env, _ := m["env"].([]interface{})
		var kept []interface{}
		for _, e := range env {
			if !slices.Contains(ClientSettableEnv, e.(map[string]interface{})["name"].(string)) {
				kept = append(kept, e)
			}
		}
		m["env"] = kept
	}
	_ = unstructured.SetNestedSlice(o, containers, "spec", "template", "spec", "containers")
	return o
}

func envValue(t *testing.T, run *unstructured.Unstructured, container, name string) (string, bool) {
	t.Helper()
	containers, _, _ := unstructured.NestedSlice(run.Object, "spec", "template", "spec", "containers")
	for _, c := range containers {
		m := c.(map[string]interface{})
		if m["name"] != container {
			continue
		}
		env, _ := m["env"].([]interface{})
		for _, e := range env {
			if em := e.(map[string]interface{}); em["name"] == name {
				v, _ := em["value"].(string)
				return v, true
			}
		}
	}
	return "", false
}

// A run the suite builds must be one adhar-batch-run-integrity admits: the
// composed template with only the §6.5 fields changed. The template is the one
// the composition really renders, sidecar added, since a sidecar must not
// receive the client variables.
func TestBatchRunBuiltFromTheTemplateChangesOnlyWhatSection65Allows(t *testing.T) {
	tpl := loadRunTemplate(t)
	containers, _, _ := unstructured.NestedSlice(tpl.Manifest.Object, "spec", "template", "spec", "containers")
	containers = append(containers, map[string]interface{}{"name": "sidecar", "image": "busybox",
		"env": []interface{}{map[string]interface{}{"name": "OTHER", "value": "x"}}})
	_ = unstructured.SetNestedSlice(tpl.Manifest.Object, containers, "spec", "template", "spec", "containers")
	before := tpl.Manifest.DeepCopy()

	run := tpl.BuildRun("importer-e2e-invalid", "prod", `{"businessDate":"31-12-2031"}`)

	if !reflect.DeepEqual(withoutClientFields(t, run), withoutClientFields(t, before)) {
		a, _ := json.MarshalIndent(withoutClientFields(t, run), "", " ")
		b, _ := json.MarshalIndent(withoutClientFields(t, before), "", " ")
		t.Fatalf("the run differs from its template beyond §6.5:\nrun:\n%s\ntemplate:\n%s", a, b)
	}
	if !reflect.DeepEqual(tpl.Manifest.Object, before.Object) {
		t.Error("building a run edited the template itself")
	}
	if run.GetName() != "importer-e2e-invalid" || run.GetNamespace() != "prod" || run.GetLabels()[labelTrigger] != "manual" {
		t.Errorf("name/namespace/trigger: %s/%s %v", run.GetNamespace(), run.GetName(), run.GetLabels())
	}
	if v, _ := envValue(t, run, "batch", envParams); v != `{"businessDate":"31-12-2031"}` {
		t.Errorf("ADHAR_BATCH_PARAMS = %q", v)
	}
	if v, _ := envValue(t, run, "batch", envTrigger); v != "manual" {
		t.Errorf("ADHAR_BATCH_TRIGGER = %q", v)
	}
	if _, ok := envValue(t, run, "sidecar", envParams); ok {
		t.Error("the sidecar received ADHAR_BATCH_PARAMS")
	}
}

// The composed template already says manual; a run is manual because the
// builder says so, not because the template happened to.
func TestBatchRunBuilderStatesTheManualTriggerItself(t *testing.T) {
	tpl := loadRunTemplate(t)
	tpl.Manifest.SetLabels(map[string]string{labelTrigger: "schedule", labelBatchJob: "importer"})
	_ = unstructured.SetNestedField(tpl.Manifest.Object, "schedule", "spec", "template", "metadata", "labels", labelTrigger)
	containers, _, _ := unstructured.NestedSlice(tpl.Manifest.Object, "spec", "template", "spec", "containers")
	containers[0].(map[string]interface{})["env"] = setEnv(containers[0].(map[string]interface{})["env"].([]interface{}), envTrigger, "schedule")
	_ = unstructured.SetNestedSlice(tpl.Manifest.Object, containers, "spec", "template", "spec", "containers")

	run := tpl.BuildRun("importer-x", "prod", "{}")
	pod, _, _ := unstructured.NestedString(run.Object, "spec", "template", "metadata", "labels", labelTrigger)
	env, _ := envValue(t, run, "batch", envTrigger)
	if run.GetLabels()[labelTrigger] != "manual" || pod != "manual" || env != "manual" {
		t.Errorf("trigger: label %q, pod label %q, env %q; want manual everywhere", run.GetLabels()[labelTrigger], pod, env)
	}
}

// What makes A1.9 reachable at all: a value the CLI would refuse travels to
// the pod byte for byte, not re-encoded or validated on the way.
func TestBatchRunBuilderKeepsInvalidParamsVerbatim(t *testing.T) {
	tpl := loadRunTemplate(t)
	for _, raw := range []string{`not json`, `{"source":"ftp"}`, `{"businessDate":"2031-02-30"}`, `{"businessDate": 20310101}`} {
		if v, _ := envValue(t, tpl.BuildRun("importer-x", "prod", raw), "batch", envParams); v != raw {
			t.Errorf("sent %q, the run carries %q", raw, v)
		}
	}
}

// The builder's allowance is pinned to the policy that enforces it: the env
// names run-integrity leaves out of its comparison are exactly ClientSettableEnv.
func TestBatchRunBuilderOnlyWritesEnvTheRunIntegrityPolicyLetsAClientSet(t *testing.T) {
	policy, err := os.ReadFile(filepath.Join("..", "..", "..", "platform", "stack", "packages", "core", "adhar-batch", "manifests", "kyverno-run-integrity.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile("env\\[\\?!contains\\(`(\\[[^`]*\\])`, name\\)\\]").FindSubmatch(policy)
	if m == nil {
		t.Fatal("kyverno-run-integrity.yaml no longer filters container env by a client-settable list; re-pin this test")
	}
	var settable []string
	if err := json.Unmarshal(m[1], &settable); err != nil {
		t.Fatal(err)
	}
	slices.Sort(settable)
	ours := slices.Clone(ClientSettableEnv)
	slices.Sort(ours)
	if !slices.Equal(settable, ours) {
		t.Fatalf("run-integrity lets a client set %v; the builder assumes %v", settable, ours)
	}
}

func TestBatchRunTemplateIdentityAppliesTheDeclaredDefaults(t *testing.T) {
	tpl := loadRunTemplate(t)
	got := tpl.Identity(map[string]string{"businessDate": "2031-05-04", "dryRun": "true"})
	want := map[string]string{"businessDate": "2031-05-04", "source": "inbound/records.csv"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("identity = %v, want %v (non-identifying dryRun out, source defaulted)", got, want)
	}
	if got := tpl.Identity(nil); !reflect.DeepEqual(got, map[string]string{"source": "inbound/records.csv"}) {
		t.Errorf("identity of no values = %v: an optional parameter with no default is absent, not \"\"", got)
	}
	if ParamsJSON(nil) != "{}" || ParamsJSON(map[string]string{"b": "1", "a": "2"}) != `{"a":"2","b":"1"}` {
		t.Error("ParamsJSON")
	}
}

// The suite reads the grace period and backoffLimit off the template; a
// float64 there reads as "absent" through every unstructured accessor.
func TestBatchRunTemplateKeepsIntegersInt64(t *testing.T) {
	tpl := loadRunTemplate(t)
	grace, ok, err := unstructured.NestedInt64(tpl.Manifest.Object, "spec", "template", "spec", "terminationGracePeriodSeconds")
	if err != nil || !ok || grace != 120 {
		t.Errorf("terminationGracePeriodSeconds = %d %v %v", grace, ok, err)
	}
	if b, _, _ := unstructured.NestedInt64(tpl.Manifest.Object, "spec", "backoffLimit"); b != 2 {
		t.Errorf("backoffLimit = %d", b)
	}
}

func TestBatchRunTemplateRefusesWhatPhase1DoesNotImplement(t *testing.T) {
	tpl := loadRunTemplate(t)
	manifest, _ := yaml.Marshal(tpl.Manifest.Object)
	for name, data := range map[string]map[string]string{
		"naming v2":   {"naming": "v2", "kind": "Job", "template.yaml": string(manifest)},
		"volcano":     {"naming": "v1", "kind": "VolcanoJob", "template.yaml": string(manifest)},
		"no template": {"naming": "v1", "kind": "Job"},
		"named":       {"naming": "v1", "kind": "Job", "template.yaml": "apiVersion: batch/v1\nkind: Job\nmetadata: {name: x}\n"},
	} {
		if _, err := ParseRunTemplate(data); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
