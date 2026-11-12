/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"encoding/json"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

// The run template (design 0025 §6.5): `ConfigMap <name>-run`, which the
// composition builds from the SAME loop as the CronJob's jobTemplate, so a run
// a person starts cannot drift from a run the clock starts. A client changes
// only the name, the adhar.io/batch-trigger label and the ADHAR_BATCH_PARAMS,
// _TRIGGER, _RESTART_OF, _COMMAND and _EXECUTION_ID environment variables;
// adhar-batch-run-integrity (Kyverno) refuses anything else. An edit here that
// "helps" is therefore a run nobody can start.

type runTemplate struct {
	// Kind is Job, IndexedJob or VolcanoJob, as `kind` in the ConfigMap says.
	Kind string
	// Naming is the scheme the template asks for; only v1 is implemented.
	Naming string
	// Fields are the declared parameters (`parameters.json`).
	Fields []paramField
	// ParamsError is set when parameters.json was present and could not be read.
	ParamsError string
	// Manifest is `template.yaml`: the run object, unnamed.
	Manifest *unstructured.Unstructured
}

// parseRunTemplate reads the ConfigMap. JSON is tried first because it is exact
// and JSON is YAML; sigs.k8s.io/yaml covers the block style a composition
// writes. A template this client cannot read, or written for a naming version
// it does not implement, is an error the person sees: starting a run from a
// guess would create the wrong identity, and Kyverno only catches a changed
// image or env.
func parseRunTemplate(cm *unstructured.Unstructured) (*runTemplate, error) {
	data, _, _ := unstructured.NestedStringMap(cm.Object, "data")
	raw := data["template.yaml"]
	if raw == "" {
		return nil, fmt.Errorf("the run template has no template.yaml")
	}
	naming := data["naming"]
	if naming == "" {
		naming = namingVersion
	}
	if naming != namingVersion {
		return nil, fmt.Errorf("this CLI implements run naming %s; the template asks for %s — update adhar", namingVersion, naming)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		asJSON, yerr := yaml.YAMLToJSON([]byte(raw))
		if yerr != nil {
			return nil, fmt.Errorf("template.yaml cannot be read: %w", yerr)
		}
		if err := json.Unmarshal(asJSON, &doc); err != nil {
			return nil, fmt.Errorf("template.yaml cannot be read: %w", err)
		}
	}
	manifest := &unstructured.Unstructured{Object: doc}
	if manifest.GetKind() != "Job" || manifest.GetAPIVersion() == "" {
		return nil, fmt.Errorf("template.yaml is not a Job")
	}
	if len(containersOf(manifest)) == 0 {
		return nil, fmt.Errorf("template.yaml has no containers")
	}
	kind := data["kind"]
	if kind != "Job" && kind != "IndexedJob" && kind != "VolcanoJob" {
		kind = "Job"
		if runKindOf(manifest) == "VolcanoJob" {
			kind = "VolcanoJob"
		}
	}
	t := &runTemplate{Kind: kind, Naming: naming, Manifest: manifest}
	fields, err := parseParameterFields(data["parameters.json"])
	if err != nil {
		t.ParamsError = err.Error()
	}
	t.Fields = fields
	return t, nil
}

// podTemplatesOf returns every pod template of a run object, LIVE (no copy, so
// buildRun's edits land in the object): a Job has one, a Volcano Job one per
// task.
func podTemplatesOf(run *unstructured.Unstructured) []map[string]interface{} {
	var out []map[string]interface{}
	if t, ok, _ := unstructured.NestedFieldNoCopy(run.Object, "spec", "template"); ok {
		if m, ok := t.(map[string]interface{}); ok {
			out = append(out, m)
		}
	}
	tasks, _, _ := unstructured.NestedFieldNoCopy(run.Object, "spec", "tasks")
	list, _ := tasks.([]interface{})
	for _, task := range list {
		if m, ok := task.(map[string]interface{}); ok {
			if t, ok := m["template"].(map[string]interface{}); ok {
				out = append(out, t)
			}
		}
	}
	return out
}

// containersOf returns every container of every pod template, live.
func containersOf(run *unstructured.Unstructured) []map[string]interface{} {
	var out []map[string]interface{}
	for _, t := range podTemplatesOf(run) {
		for _, c := range liveContainers(t) {
			out = append(out, c)
		}
	}
	return out
}

func liveContainers(tpl map[string]interface{}) []map[string]interface{} {
	raw, _, _ := unstructured.NestedFieldNoCopy(tpl, "spec", "containers")
	list, _ := raw.([]interface{})
	var out []map[string]interface{}
	for _, c := range list {
		if m, ok := c.(map[string]interface{}); ok {
			out = append(out, m)
		}
	}
	return out
}

// batchContainerOf is the container the runner lives in: the one named
// `batch` (§6.4), else the first.
func batchContainerOf(run *unstructured.Unstructured) map[string]interface{} {
	all := containersOf(run)
	for _, c := range all {
		if c["name"] == batchContainer {
			return c
		}
	}
	if len(all) > 0 {
		return all[0]
	}
	return nil
}

// envOf reads a plain-valued env var of the batch container.
func envOf(run *unstructured.Unstructured, name string) string {
	c := batchContainerOf(run)
	if c == nil {
		return ""
	}
	env, _, _ := unstructured.NestedSlice(c, "env")
	for _, e := range env {
		m, ok := e.(map[string]interface{})
		if ok && m["name"] == name {
			v, _ := m["value"].(string)
			return v
		}
	}
	return ""
}

// runImage is the image of the run's batch container.
func runImage(run *unstructured.Unstructured) string {
	if c := batchContainerOf(run); c != nil {
		img, _ := c["image"].(string)
		return img
	}
	return ""
}

// runParams are the parameters a run was given (`ADHAR_BATCH_PARAMS`); empty
// for what the variable does not say.
func runParams(run *unstructured.Unstructured) map[string]string {
	out := map[string]string{}
	raw := envOf(run, envParams)
	if raw == "" {
		return out
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return out
	}
	for k, v := range m {
		if v != nil {
			out[k] = fmt.Sprint(v)
		}
	}
	return out
}

func hasEnv(env []interface{}, name string) bool {
	for _, e := range env {
		if m, ok := e.(map[string]interface{}); ok && m["name"] == name {
			return true
		}
	}
	return false
}

// setEnv sets a plain-valued env var, replacing an existing entry in place so
// the order Kyverno compares stays the template's; an empty value removes it.
func setEnv(env []interface{}, name, value string, present bool) []interface{} {
	for i, e := range env {
		m, ok := e.(map[string]interface{})
		if !ok || m["name"] != name {
			continue
		}
		if !present {
			return append(env[:i:i], env[i+1:]...)
		}
		env[i] = map[string]interface{}{"name": name, "value": value}
		return env
	}
	if present {
		env = append(env, map[string]interface{}{"name": name, "value": value})
	}
	return env
}

// buildRun is the run object to create: the template with ONLY what §6.5 lets a
// client change. The namespace is the BatchJob's: the template is unnamed and
// normally unplaced. ADHAR_BATCH_PARAMS carries the params as a JSON object;
// only the containers the composition gave the batch contract to receive the
// client variables, because a sidecar has none of them and must not gain them.
func buildRun(t *runTemplate, name, namespace, trigger string, params map[string]string, restartOf string) *unstructured.Unstructured {
	run := t.Manifest.DeepCopy()
	run.SetName(name)
	run.SetNamespace(namespace)
	labels := run.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[labelTrigger] = trigger
	run.SetLabels(labels)
	encoded, _ := json.Marshal(params)
	if params == nil {
		encoded = []byte("{}")
	}
	for _, tpl := range podTemplatesOf(run) {
		if raw, ok, _ := unstructured.NestedFieldNoCopy(tpl, "metadata", "labels"); ok {
			if tplLabels, ok := raw.(map[string]interface{}); ok {
				if _, has := tplLabels[labelTrigger]; has {
					tplLabels[labelTrigger] = trigger
				}
			}
		}
		for _, c := range liveContainers(tpl) {
			env, _ := c["env"].([]interface{})
			if !hasEnv(env, envParams) {
				continue
			}
			env = setEnv(env, envParams, string(encoded), true)
			env = setEnv(env, envTrigger, trigger, true)
			env = setEnv(env, envRestartOf, restartOf, restartOf != "")
			c["env"] = env
		}
	}
	return run
}

// runKindOf tells a native Job from a Volcano one by its API group.
func runKindOf(run *unstructured.Unstructured) string {
	if strings.HasPrefix(run.GetAPIVersion(), "batch.volcano.sh") {
		return "VolcanoJob"
	}
	return "Job"
}

func gvrOfRun(run *unstructured.Unstructured) schema.GroupVersionResource {
	if runKindOf(run) == "VolcanoJob" {
		return volcanoJobGVR
	}
	return jobGVR
}
