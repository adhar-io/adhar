package adharplatform

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// TestArgoCDVolcanoHealthChecksJudgeEveryPhase and TestVolcanoQueueAlertsPassTheirRuleTests
// execute what they check (lua, promtool) and SKIP where the tool is missing — which
// is exactly where every alert defect of Phase 2 would have shipped unseen. The unit
// test job must install both tools and set ADHAR_REQUIRE_RULE_TOOLS=1 on the step
// that runs the tests, so a missing tool fails CI instead of skipping.
func TestTheUnitTestJobRequiresTheRuleTools(t *testing.T) {
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `json:"name"`
				Run  string            `json:"run"`
				Env  map[string]string `json:"env"`
			} `json:"steps"`
		} `json:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(readRepoFile(t, ".github", "workflows", "test.yml")), &wf); err != nil {
		t.Fatal(err)
	}
	job, ok := wf.Jobs["test"]
	if !ok {
		t.Fatal(".github/workflows/test.yml has no `test` job; the workflow layout changed")
	}
	installedAt, testsAt := -1, -1
	for i, s := range job.Steps {
		if strings.Contains(s.Run, "apt-get install -y lua5.1") && strings.Contains(s.Run, "prometheus-3.5.0.linux-amd64/promtool") {
			installedAt = i
		}
		if strings.Contains(s.Run, "make test") {
			testsAt = i
			if s.Env[ruleToolsRequired] != "1" {
				t.Errorf("the step running `make test` does not set %s=1, so a missing lua or promtool skips its guard", ruleToolsRequired)
			}
		}
	}
	if installedAt < 0 {
		t.Error("no step installs both lua5.1 and promtool")
	}
	if testsAt < 0 {
		t.Fatal("no step runs `make test`")
	}
	if installedAt > testsAt {
		t.Error("the rule tools are installed after the tests run")
	}
}
