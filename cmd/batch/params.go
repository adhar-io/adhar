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
)

// The declared parameters (design 0025 §6.5): `parameters.json` in the run
// template is the composition's rendering of `spec.parameters.parameters` with
// every default written out, so a client needs no schema to check a value. The
// Console's run form and this command read the same document and must refuse
// the same things, because what they do not refuse the runner refuses with
// exit 2 — after a pod and a JVM start.

type paramField struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Identifying bool     `json:"identifying"`
	Required    bool     `json:"required"`
	Default     *string  `json:"default,omitempty"`
	Pattern     string   `json:"pattern,omitempty"`
	Allowed     []string `json:"allowed,omitempty"`
	Source      string   `json:"source"`
	Sensitive   bool     `json:"sensitive"`
	Description string   `json:"description,omitempty"`
}

var paramTypes = map[string]bool{"string": true, "long": true, "double": true, "boolean": true, "date": true, "datetime": true}

// secretName is the kit's SecretParameterGuard (§5.6): the runner exits 2 on a
// parameter named like a credential, so the CLI says it first.
var secretName = regexp.MustCompile(`(?i)pass(word)?|secret|token|credential|api[-_]?key|private[-_]?key`)

// parseParameterFields reads `parameters.json`. An absent document is a job
// with no parameters, not an error. Each entry is normalised the way the
// Console's fieldOf does: `identifying` defaults to true, a non-string default
// is stringified, an unknown type is read as string.
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
		f.Description, _ = o["description"].(string)
		if allowed, ok := o["allowed"].([]interface{}); ok {
			for _, a := range allowed {
				f.Allowed = append(f.Allowed, fmt.Sprint(a))
			}
		}
		fields = append(fields, f)
	}
	return fields, nil
}

// parseParamArgs reads the `-p key=value` flags. A key given twice is refused
// rather than silently last-wins: two values for one identifying parameter
// are two different run names, and the person should say which they meant.
func parseParamArgs(args []string) (map[string]string, error) {
	out := map[string]string{}
	for _, a := range args {
		k, v, ok := strings.Cut(a, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("-p %q: expected key=value", a)
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("-p %s is given twice", k)
		}
		out[k] = v
	}
	return out, nil
}

// resolvedParams is what a run is given and what it is named after.
type resolvedParams struct {
	// Errors is field name → why its value is refused.
	Errors map[string]string
	// Params is everything the run receives (`ADHAR_BATCH_PARAMS`).
	Params map[string]string
	// Identifying is the subset the run is named after (§8.3).
	Identifying map[string]string
}

func (r resolvedParams) problems() []string {
	keys := make([]string, 0, len(r.Errors))
	for k := range r.Errors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+": "+r.Errors[k])
	}
	return out
}

// resolveParams validates the given values against the declared fields and
// applies the defaults. Defaults apply BEFORE hashing, so a parameter left at
// its default and the same value typed produce one name. An optional parameter
// with no default and no value is ABSENT, not "" (D1): a default of "" is
// present, and hashes differently from no default at all.
//
// A parameter the schedule normally supplies (`source: schedule`, type date)
// has no schedule on a manual run; like the Console's form, it is filled with
// today in the job's time zone when nobody gives it, so the identity the person
// is about to create is visible and deliberate rather than refused.
//
// A value for a parameter the job does not declare is refused: the runner
// would refuse it too, after the pod and the JVM had started.
func resolveParams(fields []paramField, values map[string]string, timeZone string, now time.Time) resolvedParams {
	out := resolvedParams{Errors: map[string]string{}, Params: map[string]string{}, Identifying: map[string]string{}}
	declared := map[string]bool{}
	for _, f := range fields {
		declared[f.Name] = true
		v := values[f.Name]
		if secretName.MatchString(f.Name) {
			out.Errors[f.Name] = "the runner refuses a parameter with this name: parameters are stored in clear text; pass it through a Secret"
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
				out.Errors[f.Name] = "a manual run has no schedule to take this from, so it needs a value"
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
			out.Errors[k] = "not a parameter of this job (declared: " + strings.Join(fieldNames(fields), ", ") + ")"
		}
	}
	return out
}

func fieldNames(fields []paramField) []string {
	names := make([]string, 0, len(fields))
	for _, f := range fields {
		names = append(names, f.Name)
	}
	if len(names) == 0 {
		return []string{"none"}
	}
	return names
}

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
	if msg := typeError(f.Type, v); msg != "" {
		return msg
	}
	return patternError(f.Pattern, v)
}

var (
	longRe     = regexp.MustCompile(`^-?\d{1,19}$`)
	doubleRe   = regexp.MustCompile(`^-?(\d+(\.\d*)?|\.\d+)([eE][-+]?\d+)?$`)
	dateRe     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	datetimeRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(\.\d+)?)?(Z|[+-]\d{2}:\d{2})$`)
)

func typeError(typ, v string) string {
	switch typ {
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
		if !dateRe.MatchString(v) {
			return "must be a date, yyyy-mm-dd"
		}
		if d, err := time.Parse("2006-01-02", v); err != nil || d.Format("2006-01-02") != v {
			return "that date does not exist"
		}
	case "datetime":
		if !datetimeRe.MatchString(v) {
			return "must be a date and time with an offset, 2026-11-03T17:30:00Z"
		}
		if _, err := time.Parse(time.RFC3339Nano, v); err != nil {
			if _, err2 := time.Parse("2006-01-02T15:04Z07:00", v); err2 != nil {
				return "must be a date and time with an offset, 2026-11-03T17:30:00Z"
			}
		}
	}
	return ""
}

// patternError is a whole-value match, as the runner reads it. A pattern Go
// cannot compile (Java's syntax is wider) is left to the runner.
func patternError(pattern, v string) string {
	if pattern == "" {
		return ""
	}
	re, err := regexp.Compile("^(?:" + pattern + ")$")
	if err != nil {
		return ""
	}
	if !re.MatchString(v) {
		return "must match " + pattern
	}
	return ""
}

// dateInZone is the calendar date of an instant in an IANA time zone; UTC when
// the zone cannot be loaded, which is what the composition defaults to.
func dateInZone(at time.Time, timeZone string) string {
	loc, err := time.LoadLocation(timeZone)
	if timeZone == "" || err != nil {
		loc = time.UTC
	}
	return at.In(loc).Format("2006-01-02")
}

// displayParam is what may be shown of a parameter's value.
func displayParam(fields []paramField, name, value string) string {
	for _, f := range fields {
		if f.Name == name && f.Sensitive {
			return "••••••"
		}
	}
	return value
}
