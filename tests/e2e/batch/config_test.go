package batch

import (
	"strings"
	"testing"
	"time"
)

var configNow = time.Date(2026, 11, 14, 9, 30, 0, 0, time.UTC)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// `make e2e` compiles this package beside bootstrap; unless asked for, the
// batch suite must stay out of the way of a bootstrap-only run.
func TestBatchSuiteIsOptIn(t *testing.T) {
	for v, want := range map[string]bool{"": false, "0": false, "false": false, "1": true, "true": true, "yes": true} {
		c, err := LoadConfig(envOf(map[string]string{"ADHAR_E2E_BATCH": v}), configNow)
		if err != nil || c.Enabled != want {
			t.Errorf("ADHAR_E2E_BATCH=%q: enabled %v (%v), want %v", v, c.Enabled, err, want)
		}
	}
}

func TestBatchSuiteDefaultsFitAKindRunner(t *testing.T) {
	c, err := LoadConfig(envOf(nil), configNow)
	if err != nil {
		t.Fatal(err)
	}
	if !batchJobName.MatchString(c.App) || len(c.App) > 40 || !strings.HasPrefix(c.App, "batch-e2e-") {
		t.Errorf("default app %q is not a valid BatchJob name", c.App)
	}
	if c.Templates[0] != "springboot-batch-job" || c.Schedule != "*/2 * * * *" || c.Items != 50000 {
		t.Errorf("defaults: %+v", c)
	}
	if c.Apps != 40*time.Minute || c.Build+c.Scheduled+5*c.Run > c.Overall {
		t.Errorf("budgets do not add up: %+v", c)
	}
	if c.Date(0) == c.Date(1) || c.Date(1) != c.DateBase.AddDate(0, 0, 1).Format(time.DateOnly) {
		t.Errorf("dates: %s %s", c.Date(0), c.Date(1))
	}
	later, _ := LoadConfig(envOf(nil), configNow.Add(time.Minute))
	if later.App == c.App || later.Tag == c.Tag || later.Date(0) == c.Date(0) {
		t.Error("two suite instances a minute apart share an app, a tag or a business date")
	}
	// The longest name the suite gives a run it builds, at the longest app name,
	// is still a label (63): `<app>-invalid-<tag>-<n>` and `<app>-fire-<tag>-b`.
	longest, _ := LoadConfig(envOf(map[string]string{"ADHAR_E2E_BATCH_APP": strings.Repeat("a", 40)}), time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC))
	if n := len(longest.App + "-invalid-" + longest.Tag + "-9"); n > 63 {
		t.Errorf("a built run's name is %d characters", n)
	}
}

func TestBatchSuiteKnobsOverrideAndRefuseNonsense(t *testing.T) {
	c, err := LoadConfig(envOf(map[string]string{
		"ADHAR_E2E_BATCH_APP": "orders", "ADHAR_E2E_BATCH_REUSE": "1", "ADHAR_E2E_SKIP_UP": "1", "ADHAR_E2E_BATCH_TEMPLATE": "springboot-batch-service",
		"ADHAR_E2E_BATCH_ITEMS": "200000", "ADHAR_E2E_BATCH_RUN_TIMEOUT": "30m", "ADHAR_E2E_UP_TIMEOUT": "35m",
		"ADHAR_E2E_BATCH_SCHEDULE": "*/3 * * * *",
	}), configNow)
	if err != nil {
		t.Fatal(err)
	}
	if c.App != "orders" || !c.Reuse || len(c.Templates) != 1 || c.Items != 200000 || c.Run != 30*time.Minute ||
		c.Apps != 15*time.Minute || c.Schedule != "*/3 * * * *" {
		t.Errorf("overrides: %+v", c)
	}
	for name, env := range map[string]map[string]string{
		"duration typo":   {"ADHAR_E2E_BATCH_RUN_TIMEOUT": "15"},
		"too few items":   {"ADHAR_E2E_BATCH_ITEMS": "10"},
		"not a name":      {"ADHAR_E2E_BATCH_APP": "Orders_Job"},
		"too long":        {"ADHAR_E2E_BATCH_APP": strings.Repeat("a", 41)},
		"reuse what":      {"ADHAR_E2E_BATCH_REUSE": "1", "ADHAR_E2E_SKIP_UP": "1"},
		"reuse, then up":  {"ADHAR_E2E_BATCH_REUSE": "1", "ADHAR_E2E_BATCH_APP": "orders"},
		"negative budget": {"ADHAR_E2E_BATCH_TIMEOUT": "-5m"},
	} {
		if _, err := LoadConfig(envOf(env), configNow); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
