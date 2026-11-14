package adharplatform

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// A schedule that has stopped is the batch failure the 7-day success rate exists to
// show (design 0025 §15 Phase 3, PLAT-3.7), and it is the one that leaves no runs to
// count: a nightly job whose CronJob was refused, lost its prerequisites or was
// suspended by hand finishes nothing, so a rate over the runs that ended read "not
// applicable" and the application looked as healthy as one that never failed. A
// schedule older than the window, neither held nor declared suspended, with no run
// ended in it per Prometheus, fails the rate. One that is held, suspended, younger
// than the window, or graded without Prometheus (the TTL has removed the kept runs
// of a healthy job too) is not called stopped.
func TestAStoppedScheduleFailsTheBatchSuccessRate(t *testing.T) {
	script := scorerScript(t)
	now := time.Now().Unix()
	// digest (prod) is scheduled, has alerts and a deadline, and Prometheus counts no
	// run of it in the window.
	digest := `{"metadata":{"namespace":"prod","name":"digest"},"spec":{"parameters":{"schedule":{"cron":"0 5 * * *"},"sla":{"finishBy":"07:00"},"database":{"secretRef":"nightly-db-app"}}}}`
	grade := func(replacement, prom string) *scoredSignal {
		t.Helper()
		f := batchFixture(now)
		if !strings.Contains(f["bjf"], digest) {
			t.Fatal("batchFixture no longer declares the digest BatchJob this test rewrites")
		}
		f["bjf"] = strings.Replace(f["bjf"], digest, replacement, 1)
		if prom != "" {
			f["promf"] = prom
			// and digest's one kept run has gone with its TTL, as a healthy job's do
			if !strings.Contains(f["runsf"], `"digest-0f0f0f0f-r1"`) {
				t.Fatal("batchFixture no longer keeps the digest run this test removes")
			}
			f["runsf"] = strings.Replace(f["runsf"], `"digest-0f0f0f0f-r1","labels":{"adhar.io/batch-job":"digest"`, `"digest-0f0f0f0f-r1","labels":{"adhar.io/batch-job":"gone"`, 1)
		}
		batch := runJQ(t, script["batch.jq"], f, map[string]string{"now": fmt.Sprint(now), "windowdays": "7"}, nil)
		return signalOf(gradeWithBatch(t, script, string(batch), `{}`, now)["digest"], "batch_success_rate")
	}
	withMeta := func(meta, schedule string) string {
		return fmt.Sprintf(`{"metadata":{"namespace":"prod","name":"digest"%s},"spec":{"parameters":{"schedule":{"cron":"0 5 * * *"%s},"sla":{"finishBy":"07:00"},"database":{"secretRef":"nightly-db-app"}}}}`, meta, schedule)
	}
	created := func(daysAgo int64) string {
		return fmt.Sprintf(`,"creationTimestamp":%q`, iso(now-daysAgo*86400))
	}

	if s := grade(withMeta(created(30), ""), ""); s == nil || !s.Applicable || s.Score != 0 ||
		!strings.Contains(s.Detail, "no run of digest finished in the last 7 days in prod") {
		t.Errorf("a 30-day-old schedule with no run in 7 days has stopped and must fail the success rate: %+v", s)
	}
	for why, bj := range map[string]string{
		"held":                    withMeta(created(30)+`,"annotations":{"adhar.io/batch-hold":"quarter close"}`, ""),
		"declared suspended":      withMeta(created(30), `,"suspend":true`),
		"younger than the window": withMeta(created(2), ""),
	} {
		if s := grade(bj, ""); s == nil || s.Applicable {
			t.Errorf("a schedule %s is not expected to have run; batch_success_rate should not apply: %+v", why, s)
		}
	}
	// Without Prometheus only the kept runs are known, and a healthy job's are gone
	// after its TTL: nothing is called stopped.
	noProm := `{"native":{"succeeded":null,"failed":null},"volcano":{"succeeded":null,"failed":null}}`
	if s := grade(withMeta(created(30), ""), noProm); s == nil || s.Applicable {
		t.Errorf("without Prometheus a schedule must not be called stopped: %+v", s)
	}
	// The aggregate does not hide it: digest's application also owning nightly, whose
	// 20 runs all succeeded, still fails on the schedule that stopped and names it.
	f := batchFixture(now)
	f["bjf"] = strings.Replace(f["bjf"], digest, withMeta(created(30), ""), 1)
	batch := string(runJQ(t, script["batch.jq"], f, map[string]string{"now": fmt.Sprint(now), "windowdays": "7"}, nil))
	apps := strings.Replace(batchApps, `"batch":["prod/digest"]`, `"batch":["prod/digest","prod/nightly"]`, 1)
	if s := signalOf(gradeBatchApps(t, script, apps, batch, `{}`, now)["digest"], "batch_success_rate"); s == nil || !s.Applicable || s.Score != 0 ||
		!strings.Contains(s.Detail, "no run of digest finished") || !strings.Contains(s.Detail, "20 of 20 runs of the others succeeded") {
		t.Errorf("a stopped schedule beside a sibling whose runs succeeded must still fail the rate: %+v", s)
	}
	// A 30-day-old schedule that did run is graded on its runs.
	prom := strings.Replace(f["promf"], `"label_adhar_io_batch_job":"ledger"}`, `"label_adhar_io_batch_job":"digest"}`, 1)
	if s := grade(withMeta(created(30), ""), prom); s == nil || !s.Applicable || strings.Contains(s.Detail, "neither held nor suspended") {
		t.Errorf("a schedule with runs in the window has not stopped: %+v", s)
	}
}
