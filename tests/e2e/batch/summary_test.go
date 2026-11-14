package batch

import (
	"strings"
	"testing"
)

// The example document of §5.13, with D13's reason fields absent (a completed run).
const kitSummary = `{"v":1,"job":"importJob","batchJob":"importer","instanceId":42,"executionId":97,"status":"COMPLETED",
 "exitCode":0,"exitDescription":"","outcome":"completed","trigger":"schedule","businessDate":"2026-11-03",
 "read":120000,"written":119990,"skipped":10,"deadLettered":10,"filtered":0,
 "start":"2026-11-03T17:30:04Z","end":"2026-11-03T17:43:36Z","image":"sha256:abc",
 "steps":[{"name":"importStep","status":"COMPLETED","read":120000,"written":119990,"skipped":10}]}`

func TestBatchSummaryReadsTheKitsV1Document(t *testing.T) {
	s := ParseSummary(kitSummary)
	if s == nil {
		t.Fatal("the §5.13 example was not read as a summary")
	}
	if s.Truncated || *s.V != 1 || *s.InstanceID != 42 || *s.ExecutionID != 97 || *s.ExitCode != 0 {
		t.Errorf("identity fields: %+v", s)
	}
	if s.Outcome != "completed" || s.Status != "COMPLETED" || s.BusinessDate != "2026-11-03" || s.Trigger != "schedule" {
		t.Errorf("outcome fields: %+v", s)
	}
	if *s.Read != 120000 || *s.Written != 119990 || *s.DeadLettered != 10 || *s.Filtered != 0 {
		t.Errorf("counts: read %d written %d", *s.Read, *s.Written)
	}
	if len(s.Steps) != 1 || s.Steps[0].Written != 119990 {
		t.Errorf("steps: %+v", s.Steps)
	}
	if err := s.CheckSkipped(); err == nil {
		t.Error("a completed run passed as skipped")
	}
}

// D13: exit 0 covers completed, already-complete, duplicate and calendar, and
// only `reason` tells them apart; an invalid request has no execution.
func TestBatchSummarySkipAndInvalidCarryAD13Reason(t *testing.T) {
	skipped := ParseSummary(`{"v":1,"job":"importJob","batchJob":"importer","exitCode":0,"outcome":"skipped","reason":"already-complete","trigger":"manual"}`)
	if err := skipped.CheckSkipped("already-complete", "duplicate"); err != nil {
		t.Error(err)
	}
	if err := skipped.CheckSkipped("calendar"); err == nil {
		t.Error("already-complete passed as calendar")
	}
	invalid := ParseSummary(`{"v":1,"job":"importJob","batchJob":"importer","exitCode":2,"outcome":"invalid","reason":"parameters","exitDescription":"businessDate must be a date"}`)
	if err := invalid.CheckInvalid("parameters"); err != nil {
		t.Error(err)
	}
	withExecution := ParseSummary(`{"v":1,"executionId":5,"exitCode":2,"outcome":"invalid","reason":"parameters"}`)
	if err := withExecution.CheckInvalid(); err == nil {
		t.Error("an invalid request naming an execution passed")
	}
	unknown := ParseSummary(`{"v":1,"exitCode":0,"outcome":"skipped","reason":"weekend"}`)
	if err := unknown.CheckSkipped(); err == nil {
		t.Error("a reason D13 does not define passed")
	}
	var none *Summary
	if err := none.CheckInvalid(); err == nil {
		t.Error("a missing summary passed as invalid")
	}
}

// The template's stand-in launcher folds the reason into the outcome. That is
// not D13 v1, and the suite says so instead of reading it as a skip.
func TestBatchSummaryPreD13SkippedOutcomeIsRefused(t *testing.T) {
	for _, doc := range []string{
		`{"v":1,"job":"enterprise-job","batchJob":"orders","outcome":"skipped(duplicate)","exitCode":0,"trigger":"manual"}`,
		`{"v":1,"job":"enterprise-job","batchJob":"orders","outcome":"skipped(duplicate)","reason":"duplicate","exitCode":0}`,
	} {
		err := ParseSummary(doc).CheckSkipped()
		if err == nil || !strings.Contains(err.Error(), `want "skipped"`) {
			t.Errorf("%s gave %v", doc, err)
		}
	}
}

func TestBatchSummarySurvivesTheKubeletsTruncation(t *testing.T) {
	cut := kitSummary[:strings.Index(kitSummary, `"steps":`)+40]
	s := ParseSummary(cut)
	if s == nil || !s.Truncated {
		t.Fatalf("a cut document: %+v", s)
	}
	if s.ExecutionID == nil || *s.ExecutionID != 97 || s.Outcome != "completed" || *s.Written != 119990 {
		t.Errorf("fields before the cut were lost: %+v", s)
	}
	if s.Steps != nil {
		t.Errorf("steps were guessed from a cut document: %+v", s.Steps)
	}
	// Each step has its own read/written: a cut inside `steps` must not let a
	// step's count overwrite the run's.
	twoSteps := `{"v":1,"outcome":"completed","read":30,"written":30,"steps":[{"name":"a","status":"COMPLETED","read":10,"written":10},{"name":"b","read":20,"wri`
	if s := ParseSummary(twoSteps); s == nil || *s.Read != 30 || *s.Written != 30 {
		t.Errorf("a step's counts replaced the run's: %+v", s)
	}
	if mid := ParseSummary(`{"v":1,"outcome":"completed","exitCode":0,"read":12`); mid == nil || mid.Read != nil || *mid.ExitCode != 0 {
		t.Errorf("a number cut mid-digits was read: %+v", mid)
	}
}

func TestBatchSummaryIsNilForWhatIsNotASummary(t *testing.T) {
	for _, text := range []string{"", "Error: could not find or load main class", `{"level":"error"}`, "  \n"} {
		if s := ParseSummary(text); s != nil {
			t.Errorf("%q read as %+v", text, s)
		}
	}
}
