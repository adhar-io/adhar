package batch

import (
	"strings"
	"testing"
	"time"
)

// parameters.json as the composition writes it: every default written out,
// so a client needs no schema. The declarations of §6.3's importer.
const importerParams = `[
  {"name":"businessDate","type":"date","identifying":true,"required":false,"source":"schedule","sensitive":false,"description":"The day to import"},
  {"name":"region","type":"string","identifying":true,"required":false,"source":"input","sensitive":false,"default":"sg","allowed":["sg","uk"]},
  {"name":"dryRun","type":"boolean","identifying":false,"required":false,"source":"input","sensitive":false,"default":"false"},
  {"name":"source","type":"string","identifying":true,"required":true,"source":"input","sensitive":false,"pattern":"inbound/.*\\.csv"},
  {"name":"limit","type":"long","identifying":false,"required":false,"source":"input","sensitive":false}
]`

var testNow = time.Date(2026, 11, 3, 23, 30, 0, 0, time.UTC)

func importerFields(t *testing.T) []paramField {
	t.Helper()
	fields, err := parseParameterFields(importerParams)
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 5 {
		t.Fatalf("parsed %d fields, want 5", len(fields))
	}
	return fields
}

// Defaults apply BEFORE hashing, a schedule-sourced date is today in the job's
// zone, and an optional parameter with no default and no value is ABSENT:
// each of these changes a run's name, so each is pinned.
func TestResolveParamsAppliesDefaultsBeforeNaming(t *testing.T) {
	fields := importerFields(t)
	r := resolveParams(fields, map[string]string{"source": "inbound/a.csv"}, "Asia/Singapore", testNow)
	if len(r.Errors) != 0 {
		t.Fatalf("unexpected refusals: %v", r.Errors)
	}
	// 23:30 UTC is already the 4th in Singapore: the identity is the job's day, not the laptop's.
	if r.Identifying["businessDate"] != "2026-11-04" {
		t.Errorf("businessDate = %q, want today in Asia/Singapore", r.Identifying["businessDate"])
	}
	if r.Identifying["region"] != "sg" {
		t.Errorf("the default region must be applied before naming: %v", r.Identifying)
	}
	if _, present := r.Identifying["dryRun"]; present {
		t.Errorf("dryRun is not identifying and must not be in the name: %v", r.Identifying)
	}
	if r.Params["dryRun"] != "false" {
		t.Errorf("dryRun's default must still reach the run: %v", r.Params)
	}
	if _, present := r.Params["limit"]; present {
		t.Errorf("an optional parameter with no default and no value is absent, not \"\": %v", r.Params)
	}
	name, _ := runName("importer", r.Identifying)
	typed := resolveParams(fields, map[string]string{"source": "inbound/a.csv", "region": "sg", "businessDate": "2026-11-04"}, "Asia/Singapore", testNow)
	typedName, _ := runName("importer", typed.Identifying)
	if name != typedName {
		t.Errorf("a value left at its default and the same value typed must name one run: %s vs %s", name, typedName)
	}
}

func TestResolveParamsRefusesWhatTheRunnerWouldRefuse(t *testing.T) {
	fields := importerFields(t)
	cases := map[string]map[string]string{
		"source":       {"region": "sg"},                                          // required
		"region":       {"source": "inbound/a.csv", "region": "us"},               // not allowed
		"businessDate": {"source": "inbound/a.csv", "businessDate": "2026-02-30"}, // no such day
		"dryRun":       {"source": "inbound/a.csv", "dryRun": "yes"},              // not a boolean
		"limit":        {"source": "inbound/a.csv", "limit": "ten"},               // not a long
		"nope":         {"source": "inbound/a.csv", "nope": "1"},                  // not declared
	}
	for field, values := range cases {
		r := resolveParams(fields, values, "UTC", testNow)
		if _, refused := r.Errors[field]; !refused {
			t.Errorf("%s: expected a refusal for %v, got %v", field, values, r.Errors)
		}
	}
	r := resolveParams(fields, map[string]string{"source": "outbound/a.csv"}, "UTC", testNow)
	if !strings.Contains(r.Errors["source"], "must match") {
		t.Errorf("a pattern is a whole-value match: %v", r.Errors)
	}
	// The kit's SecretParameterGuard: the runner exits 2 on these names, so the CLI says it first.
	secret := []paramField{{Name: "apiKey", Type: "string", Identifying: true, Source: "input"}}
	if r := resolveParams(secret, map[string]string{"apiKey": "x"}, "UTC", testNow); r.Errors["apiKey"] == "" {
		t.Error("a parameter named like a credential must be refused")
	}
}

func TestParamArgsAreKeyEqualsValue(t *testing.T) {
	got, err := parseParamArgs([]string{"businessDate=2026-11-03", "note=a=b", "empty="})
	if err != nil || got["businessDate"] != "2026-11-03" || got["note"] != "a=b" || got["empty"] != "" {
		t.Fatalf("parseParamArgs = %v, %v", got, err)
	}
	if _, err := parseParamArgs([]string{"novalue"}); err == nil {
		t.Error("a bare word is not a parameter")
	}
	if _, err := parseParamArgs([]string{"a=1", "a=2"}); err == nil {
		t.Error("a key given twice is two run names; it must be refused")
	}
}

func TestParameterFieldsTolerateTheWrapperAndNonStringDefaults(t *testing.T) {
	fields, err := parseParameterFields(`{"parameters":[{"name":"n","type":"long","default":5},{"name":"x","type":"weird"}]}`)
	if err != nil || len(fields) != 2 {
		t.Fatalf("fields = %v, %v", fields, err)
	}
	if fields[0].Default == nil || *fields[0].Default != "5" || !fields[0].Identifying {
		t.Errorf("a numeric default is read as its string, identifying defaults to true: %+v", fields[0])
	}
	if fields[1].Type != "string" {
		t.Errorf("an unknown type reads as string: %+v", fields[1])
	}
	if f, err := parseParameterFields(""); err != nil || f != nil {
		t.Errorf("no document is no parameters: %v %v", f, err)
	}
	if _, err := parseParameterFields(`{"nope":1}`); err == nil {
		t.Error("a document that is not a list must be an error the person sees")
	}
}
