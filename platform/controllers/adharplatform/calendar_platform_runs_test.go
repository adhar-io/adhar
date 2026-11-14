package adharplatform

// The runs the PLATFORM starts — the smoke run in test (design 0025 D6) and the
// observer's dependency run (§12) — carry only the smoke parameters, or only the
// business date; every other declared parameter takes its default. A definition
// whose parameters the CLI or the observer would refuse at that moment (their
// resolveParams) would fail every promotion, or every night, behind a healthy
// BatchJob, so the composition refuses it at definition. Found in verification of
// PLAT-3.3 (2026-11-14): `smoke: {}` on a job with a required parameter rendered, and
// a job whose business date is `runDate` (type date, source schedule), which the
// observer accepts, was refused. Mutation-tested: each refusal removed, the test
// seen failing, the refusal restored.

import (
	"strings"
	"testing"
)

func TestPlatformStartedRunsOnlyUseParametersTheyCanFill(t *testing.T) {
	sched := bjXR(t, "xr-scheduled.yaml") // businessDate (date, schedule), source (default), dryRun (boolean, default)
	params := func(extra ...map[string]any) []any {
		out := []any{
			map[string]any{"name": "businessDate", "type": "date", "source": "schedule"},
			map[string]any{"name": "source", "default": "inbound/records.csv"},
		}
		for _, e := range extra {
			out = append(out, e)
		}
		return out
	}
	region := map[string]any{"name": "region", "required": true, "allowed": []any{"sg", "in"}}

	for _, c := range []struct {
		name string
		set  map[string]any
		want string
	}{
		{"smoke without a value for a required parameter", map[string]any{"parameters": params(region), "smoke": map[string]any{}}, "gives no value for parameter region, which is required"},
		{"smoke with an empty value for a required parameter", map[string]any{"parameters": params(region), "smoke": map[string]any{"parameters": map[string]any{"region": ""}}}, "gives no value for parameter region"},
		{"smoke outside allowed", map[string]any{"parameters": params(region), "smoke": map[string]any{"parameters": map[string]any{"region": "us"}}}, "region=us, which is not one of its allowed values sg, in"},
		{"smoke with a wrong date", map[string]any{"smoke": map[string]any{"parameters": map[string]any{"businessDate": "yesterday"}}}, "businessDate=yesterday, which is not a date"},
		{"smoke with a wrong boolean", map[string]any{"smoke": map[string]any{"parameters": map[string]any{"dryRun": "yes"}}}, "dryRun=yes, which is not a boolean"},
		{"smoke without a value for a schedule-supplied string", map[string]any{"parameters": params(map[string]any{"name": "batchWindow", "source": "schedule"}), "smoke": map[string]any{}}, "parameter batchWindow, which the schedule supplies and is not a date"},
		{"dependency run without a value for a required parameter", map[string]any{"parameters": params(region), "dependsOn": []any{map[string]any{"batchJob": "extract"}}}, "a dependency run (dependsOn) gives no value for parameter region"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := bjRender(t, bjWithParams(sched, c.set), bjCluster(t), nil); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("rendered without the expected refusal %q: %v", c.want, err)
			}
		})
	}

	for name, set := range map[string]map[string]any{
		// No values at all: businessDate is today, the rest take their defaults.
		"smoke with no parameters":                      {"smoke": map[string]any{}},
		"smoke filling the required one":                {"parameters": params(region), "smoke": map[string]any{"parameters": map[string]any{"region": "sg"}}},
		"an optional parameter left out":                {"parameters": params(map[string]any{"name": "note"}), "smoke": map[string]any{}},
		"a dependency run, every other one defaulted":   {"dependsOn": []any{map[string]any{"batchJob": "extract"}}},
		"a required parameter with no platform run":     {"parameters": params(region)},
		"a required parameter with a default for smoke": {"parameters": params(map[string]any{"name": "region", "required": true, "default": "sg"}), "smoke": map[string]any{}},
	} {
		if _, err := bjRender(t, bjWithParams(sched, set), bjCluster(t), nil); err != nil {
			t.Errorf("%s was refused: %v", name, err)
		}
	}

	// The observer's business date is `businessDate`, else the first date the
	// schedule supplies (platform/controllers/batch businessDateField): both are a
	// business date for dependsOn, of either kind.
	for _, same := range []bool{true, false} {
		runDate := bjWithParams(sched, map[string]any{
			"parameters": []any{map[string]any{"name": "runDate", "type": "date", "source": "schedule"}, map[string]any{"name": "source", "default": "x"}},
			"dependsOn":  []any{map[string]any{"batchJob": "extract", "sameBusinessDate": same}},
		})
		if _, err := bjRender(t, runDate, bjCluster(t), nil); err != nil {
			t.Errorf("a schedule-supplied date the observer accepts (sameBusinessDate %v) was refused: %v", same, err)
		}
	}
	inputDate := bjWithParams(sched, map[string]any{
		"parameters": []any{map[string]any{"name": "runDate", "type": "date"}},
		"dependsOn":  []any{map[string]any{"batchJob": "extract"}},
	})
	if _, err := bjRender(t, inputDate, bjCluster(t), nil); err == nil || !strings.Contains(err.Error(), "declares no businessDate parameter") {
		t.Errorf("an input date, which the observer does not set, counted as the business date: %v", err)
	}
}
