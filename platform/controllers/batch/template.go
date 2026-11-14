/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package batch

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// The run template (design 0025 §6.5): `ConfigMap <batchjob>-run`. The observer
// reads it for two things — the declared parameters, so an event can name a
// run's identifying values with the sensitive ones masked (D8), and the run
// object itself, so a `dependsOn` run is the deployed definition with ONLY what
// §6.5 lets a client change (the name, the adhar.io/batch-trigger label, and
// ADHAR_BATCH_PARAMS/_TRIGGER/_RESTART_OF). adhar-batch-run-integrity checks the
// observer like any other client; a run it built from a guess would be refused.
//
// This mirrors `cmd/batch/{template,params}.go`. The two are kept apart because
// a controller must not import a CLI package; the rules are the same, and the
// run name both compute is pinned by the shared naming vectors.

const (
	labelBatchJob   = "adhar.io/batch-job"
	labelTrigger    = "adhar.io/batch-trigger"
	labelRole       = "adhar.io/batch-role"
	labelWorkload   = "adhar.io/workload"
	labelNativeJob  = "batch.kubernetes.io/job-name"
	labelLegacyJob  = "job-name"
	labelNativeIdx  = "batch.kubernetes.io/job-completion-index"
	labelVolcanoJob = "volcano.sh/job-name"

	annotationHold      = "adhar.io/batch-hold"
	annotationActor     = "adhar.io/batch-actor"
	annotationScheduled = "batch.kubernetes.io/cronjob-scheduled-timestamp"
	// annotationDependsOn is written by the observer on a run it starts for
	// dependsOn: the `<namespace>/<batchjob>` list it waited for. Run integrity
	// compares labels and the pod template, never the Job's own annotations, so
	// it is client-settable like the workflow step's adhar.io/batch-workflow.
	annotationDependsOn = "adhar.io/batch-depends-on"

	envParams    = "ADHAR_BATCH_PARAMS"
	envTrigger   = "ADHAR_BATCH_TRIGGER"
	envRestartOf = "ADHAR_BATCH_RESTART_OF"
	envTimeZone  = "ADHAR_BATCH_TIME_ZONE"

	batchContainer = "batch"

	// TriggerSchedule is the one trigger a run is NOT requested with: the clock
	// starts it, so its first event is `run.started` (§12).
	TriggerSchedule = "schedule"
	// TriggerDependency is the trigger of a run the observer starts for
	// `dependsOn` (§12).
	TriggerDependency = "dependency"
)

type runTemplate struct {
	Fields   []paramField
	Manifest *unstructured.Unstructured
}

// parseRunTemplate reads the ConfigMap's data. A template this observer cannot
// read, or one written for a naming version it does not implement, is an error:
// starting a run from a guess would create the wrong identity.
func parseRunTemplate(data map[string]string) (*runTemplate, error) {
	raw := data["template.yaml"]
	if raw == "" {
		return nil, fmt.Errorf("the run template has no template.yaml")
	}
	if naming := data["naming"]; naming != "" && naming != namingVersion {
		return nil, fmt.Errorf("the observer implements run naming %s; the template asks for %s", namingVersion, naming)
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
	fields, err := parseParameterFields(data["parameters.json"])
	if err != nil {
		return nil, err
	}
	return &runTemplate{Fields: fields, Manifest: manifest}, nil
}

// podTemplatesOf returns every pod template of a run object, LIVE: a Job has
// one, a Volcano Job one per task.
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

func containersOf(run *unstructured.Unstructured) []map[string]interface{} {
	var out []map[string]interface{}
	for _, t := range podTemplatesOf(run) {
		out = append(out, liveContainers(t)...)
	}
	return out
}

// envOf reads a plain-valued env var of the batch container (the one named
// `batch`, else the first).
func envOf(run *unstructured.Unstructured, name string) string {
	all := containersOf(run)
	var c map[string]interface{}
	for _, cand := range all {
		if cand["name"] == batchContainer {
			c = cand
			break
		}
	}
	if c == nil && len(all) > 0 {
		c = all[0]
	}
	if c == nil {
		return ""
	}
	env, _ := c["env"].([]interface{})
	for _, e := range env {
		m, ok := e.(map[string]interface{})
		if ok && m["name"] == name {
			v, _ := m["value"].(string)
			return v
		}
	}
	return ""
}

// runParams are the parameters a run was given (`ADHAR_BATCH_PARAMS`).
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

// setEnv sets a plain-valued env var in place, so the order Kyverno compares
// stays the template's; an empty value removes it.
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

// buildRun is the run to create: the template with only what §6.5 lets a client
// change. Only the containers the composition gave the batch contract (they
// carry ADHAR_BATCH_PARAMS) receive the client variables.
func buildRun(t *runTemplate, name, namespace, trigger string, params map[string]string) *unstructured.Unstructured {
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
			env = setEnv(env, envRestartOf, "", false)
			c["env"] = env
		}
	}
	return run
}

/* ─────────────────────────── declared parameters ─────────────────────────── */

type paramField struct {
	Name        string
	Type        string
	Identifying bool
	Required    bool
	Default     *string
	Pattern     string
	Allowed     []string
	Source      string
	Sensitive   bool
}

var paramTypes = map[string]bool{"string": true, "long": true, "double": true, "boolean": true, "date": true, "datetime": true}

// secretName is the kit's SecretParameterGuard (§5.6).
var secretName = regexp.MustCompile(`(?i)pass(word)?|secret|token|credential|api[-_]?key|private[-_]?key`)

// parseParameterFields reads `parameters.json`, normalised as the CLI and the
// Console do: `identifying` defaults to true, an unknown type reads as string.
func parseParameterFields(doc string) ([]paramField, error) {
	if strings.TrimSpace(doc) == "" {
		return nil, nil
	}
	var parsed interface{}
	if err := json.Unmarshal([]byte(doc), &parsed); err != nil {
		return nil, fmt.Errorf("parameters.json cannot be read: %w", err)
	}
	list, ok := parsed.([]interface{})
	if !ok {
		if wrapper, isMap := parsed.(map[string]interface{}); isMap {
			list, ok = wrapper["parameters"].([]interface{})
		}
	}
	if !ok {
		return nil, fmt.Errorf("parameters.json is not a list of parameters")
	}
	var fields []paramField
	for _, item := range list {
		o, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := o["name"].(string)
		if name == "" {
			continue
		}
		f := paramField{Name: name, Type: "string", Identifying: true, Source: "input"}
		if t, _ := o["type"].(string); paramTypes[t] {
			f.Type = t
		}
		if v, present := o["identifying"]; present && v == false {
			f.Identifying = false
		}
		f.Required = o["required"] == true
		f.Sensitive = o["sensitive"] == true
		if o["source"] == "schedule" {
			f.Source = "schedule"
		}
		switch d := o["default"].(type) {
		case string:
			f.Default = &d
		case nil:
		default:
			s := fmt.Sprint(d)
			f.Default = &s
		}
		f.Pattern, _ = o["pattern"].(string)
		if allowed, ok := o["allowed"].([]interface{}); ok {
			for _, a := range allowed {
				f.Allowed = append(f.Allowed, fmt.Sprint(a))
			}
		}
		fields = append(fields, f)
	}
	return fields, nil
}

// businessDateField is the parameter a `dependsOn` run receives its business
// date in: the one named businessDate, else the first date the schedule
// supplies (§5.6: "for scheduled jobs that is normally businessDate").
func businessDateField(fields []paramField) (paramField, bool) {
	for _, f := range fields {
		if f.Name == "businessDate" {
			return f, true
		}
	}
	for _, f := range fields {
		if f.Type == "date" && f.Source == "schedule" {
			return f, true
		}
	}
	return paramField{}, false
}

type resolvedParams struct {
	Errors      map[string]string
	Params      map[string]string
	Identifying map[string]string
}

func (r resolvedParams) problems() string {
	keys := make([]string, 0, len(r.Errors))
	for k := range r.Errors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+": "+r.Errors[k])
	}
	return strings.Join(out, "; ")
}

// resolveParams applies the declared defaults and refuses what the runner would
// refuse — the same rules as `adhar batch run` (cmd/batch/params.go), so the
// identity the observer names is the one a person typing the same values gets.
func resolveParams(fields []paramField, values map[string]string, timeZone string, now time.Time) resolvedParams {
	out := resolvedParams{Errors: map[string]string{}, Params: map[string]string{}, Identifying: map[string]string{}}
	declared := map[string]bool{}
	for _, f := range fields {
		declared[f.Name] = true
		v := values[f.Name]
		if secretName.MatchString(f.Name) {
			out.Errors[f.Name] = "the runner refuses a parameter with this name"
			continue
		}
		if v == "" {
			switch {
			case f.Default != nil:
				v = *f.Default
			case f.Source == "schedule" && f.Type == "date":
				v = dateInZone(now, timeZone)
			case f.Required:
				out.Errors[f.Name] = "required"
				continue
			case f.Source == "schedule":
				out.Errors[f.Name] = "no schedule to take this from, so it needs a value"
				continue
			default:
				continue
			}
		}
		if refused := refuseValue(f, v); refused != "" {
			out.Errors[f.Name] = refused
			continue
		}
		out.Params[f.Name] = v
		if f.Identifying {
			out.Identifying[f.Name] = v
		}
	}
	for k := range values {
		if !declared[k] {
			out.Errors[k] = "not a parameter of this job"
		}
	}
	return out
}

var (
	longRe     = regexp.MustCompile(`^-?\d{1,19}$`)
	doubleRe   = regexp.MustCompile(`^-?(\d+(\.\d*)?|\.\d+)([eE][-+]?\d+)?$`)
	dateRe     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	datetimeRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(\.\d+)?)?(Z|[+-]\d{2}:\d{2})$`)
)

func refuseValue(f paramField, v string) string {
	if !utf8.ValidString(v) {
		return "not valid text"
	}
	if v == "" && f.Default != nil && *f.Default == "" {
		return ""
	}
	if len(f.Allowed) > 0 {
		allowed := false
		for _, a := range f.Allowed {
			if a == v {
				allowed = true
				break
			}
		}
		if !allowed {
			return "must be one of " + strings.Join(f.Allowed, ", ")
		}
	}
	switch f.Type {
	case "long":
		if !longRe.MatchString(v) {
			return "must be a whole number"
		}
	case "double":
		if !doubleRe.MatchString(v) {
			return "must be a number"
		}
	case "boolean":
		if v != "true" && v != "false" {
			return "must be true or false"
		}
	case "date":
		if d, err := time.Parse("2006-01-02", v); !dateRe.MatchString(v) || err != nil || d.Format("2006-01-02") != v {
			return "must be a date, yyyy-mm-dd"
		}
	case "datetime":
		if !datetimeRe.MatchString(v) {
			return "must be a date and time with an offset"
		}
	}
	if f.Pattern != "" {
		if re, err := regexp.Compile("^(?:" + f.Pattern + ")$"); err == nil && !re.MatchString(v) {
			return "must match " + f.Pattern
		}
	}
	return ""
}

// dateInZone is the calendar date of an instant in an IANA zone; UTC when the
// zone cannot be loaded, which is what the composition defaults to.
func dateInZone(at time.Time, timeZone string) string {
	return at.In(zone(timeZone)).Format("2006-01-02")
}

func zone(name string) *time.Location {
	if name == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

// maskedIdentifying is the identifying values a run was given, sensitive ones
// masked (D8): history, backfill and dependsOn need the business date, and an
// event bus is no place for a value the job marked sensitive.
func maskedIdentifying(fields []paramField, params map[string]string) map[string]string {
	if len(params) == 0 {
		return nil
	}
	known := map[string]paramField{}
	for _, f := range fields {
		known[f.Name] = f
	}
	out := map[string]string{}
	for k, v := range params {
		f, declared := known[k]
		if declared && !f.Identifying {
			continue
		}
		// A value whose declaration is unknown (no template, or a run older than
		// it) is masked when its NAME looks like a credential, which is the
		// runner's own rule.
		if (declared && f.Sensitive) || secretName.MatchString(k) {
			out[k] = "••••••"
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
