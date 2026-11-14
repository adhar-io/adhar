package batch

import (
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utiljson "k8s.io/apimachinery/pkg/util/json"
	"sigs.k8s.io/yaml"
)

// The run-template contract (design 0025 §6.5): ConfigMap <name>-run holds the
// run object exactly as the CronJob would create it. A client changes ONLY the
// name, the adhar.io/batch-trigger label and these variables, and
// adhar-batch-run-integrity (Kyverno) refuses anything else.
//
// The suite builds runs itself (NEW-2) because `adhar batch run` refuses a bad
// parameter before any Job exists, and A1.9 is about what the POD does with
// one: exit 2, and the Job failed with zero retries.

// ClientSettableEnv are the variables §6.5 lets a client set.
var ClientSettableEnv = []string{
	"ADHAR_BATCH_PARAMS", "ADHAR_BATCH_TRIGGER", "ADHAR_BATCH_RESTART_OF", "ADHAR_BATCH_COMMAND", "ADHAR_BATCH_EXECUTION_ID",
}

const (
	labelBatchJob = "adhar.io/batch-job"
	labelTrigger  = "adhar.io/batch-trigger"
	labelJobName  = "batch.kubernetes.io/job-name"
	envParams     = "ADHAR_BATCH_PARAMS"
	envTrigger    = "ADHAR_BATCH_TRIGGER"
	triggerManual = "manual"
)

// Param is one entry of parameters.json.
type Param struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Identifying bool     `json:"identifying"`
	Required    bool     `json:"required"`
	Default     *string  `json:"default,omitempty"`
	Source      string   `json:"source"`
	Allowed     []string `json:"allowed,omitempty"`
	Pattern     string   `json:"pattern,omitempty"`
}

// RunTemplate is a parsed ConfigMap <name>-run.
type RunTemplate struct {
	Kind, Naming string
	Params       []Param
	Manifest     *unstructured.Unstructured
}

// ParseRunTemplate reads the ConfigMap's data. Only naming v1 and a native Job
// are Phase 1; anything else is an error, not a guess.
func ParseRunTemplate(data map[string]string) (*RunTemplate, error) {
	t := &RunTemplate{Kind: data["kind"], Naming: data["naming"]}
	if t.Naming != "v1" {
		return nil, fmt.Errorf("run template naming %q; this suite implements v1", t.Naming)
	}
	if t.Kind != "Job" {
		return nil, fmt.Errorf("run template kind %q; Phase 1 runs a native Job", t.Kind)
	}
	// apimachinery's JSON decoding keeps integers int64, as every unstructured
	// accessor expects; yaml.Unmarshal would hand back float64s.
	var doc map[string]interface{}
	asJSON, err := yaml.YAMLToJSON([]byte(data["template.yaml"]))
	if err == nil {
		err = utiljson.Unmarshal(asJSON, &doc)
	}
	if err != nil || doc == nil {
		return nil, fmt.Errorf("template.yaml cannot be read: %v", err)
	}
	t.Manifest = &unstructured.Unstructured{Object: doc}
	if t.Manifest.GetKind() != "Job" || t.Manifest.GetName() != "" {
		return nil, fmt.Errorf("template.yaml is a %s named %q; §6.5 wants an unnamed Job", t.Manifest.GetKind(), t.Manifest.GetName())
	}
	if raw := data["parameters.json"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &t.Params); err != nil {
			return nil, fmt.Errorf("parameters.json: %w", err)
		}
	}
	return t, nil
}

// Identity is the identifying parameters of a run given these values, after the
// declared defaults (D1): what its name hashes.
func (t *RunTemplate) Identity(given map[string]string) map[string]string {
	out := map[string]string{}
	for _, p := range t.Params {
		if !p.Identifying {
			continue
		}
		if v, ok := given[p.Name]; ok {
			out[p.Name] = v
		} else if p.Default != nil {
			out[p.Name] = *p.Default
		}
	}
	return out
}

// ParamsJSON is ADHAR_BATCH_PARAMS for a set of valid values.
func ParamsJSON(values map[string]string) string {
	if len(values) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(values)
	return string(b)
}

// BuildRun is a manual run named name in namespace whose ADHAR_BATCH_PARAMS is
// rawParams VERBATIM — malformed JSON or a refused value included. Only the
// containers that carry the batch contract receive the variables.
func (t *RunTemplate) BuildRun(name, namespace, rawParams string) *unstructured.Unstructured {
	run := t.Manifest.DeepCopy()
	run.SetName(name)
	run.SetNamespace(namespace)
	labels := run.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[labelTrigger] = triggerManual
	run.SetLabels(labels)
	if podLabels, ok, _ := unstructured.NestedStringMap(run.Object, "spec", "template", "metadata", "labels"); ok {
		if _, has := podLabels[labelTrigger]; has {
			podLabels[labelTrigger] = triggerManual
			_ = unstructured.SetNestedStringMap(run.Object, podLabels, "spec", "template", "metadata", "labels")
		}
	}
	containers, _, _ := unstructured.NestedSlice(run.Object, "spec", "template", "spec", "containers")
	for _, c := range containers {
		m, _ := c.(map[string]interface{})
		env, _ := m["env"].([]interface{})
		if !hasEnv(env, envParams) {
			continue
		}
		env = setEnv(env, envParams, rawParams)
		m["env"] = setEnv(env, envTrigger, triggerManual)
	}
	_ = unstructured.SetNestedSlice(run.Object, containers, "spec", "template", "spec", "containers")
	return run
}

func hasEnv(env []interface{}, name string) bool {
	for _, e := range env {
		if m, ok := e.(map[string]interface{}); ok && m["name"] == name {
			return true
		}
	}
	return false
}

// setEnv replaces a plain-valued variable in place, keeping the template's
// order, or appends it.
func setEnv(env []interface{}, name, value string) []interface{} {
	for i, e := range env {
		if m, ok := e.(map[string]interface{}); ok && m["name"] == name {
			env[i] = map[string]interface{}{"name": name, "value": value}
			return env
		}
	}
	return append(env, map[string]interface{}{"name": name, "value": value})
}
