package adharplatform

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The scorecard's batch rules (design 0025 §15 Phase 3, PLAT-3.7): restart test
// present, alerts present, JDBC repository, deadline set, 7-day success rate. They
// run through the scorer's REAL programs — batch.jq projects the cluster's batch
// objects, score.jq grades — on fixtures, end to end.

// scorerScript returns the scorer's mounted programs, keyed by file name.
func scorerScript(t *testing.T) map[string]string {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed")
	}
	return configMapData(t, scorecardsDir+"scorer-cronjob.yaml", "adhar-scorecard-script")
}

// runJQ runs a program file with -n, binding each of files as --slurpfile <key>
// and each of args as --argjson.
func runJQ(t *testing.T, program string, files map[string]string, args map[string]string, strArgs map[string]string) []byte {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cmdArgs := []string{"-n", "-c", "-f", write("program.jq", program)}
	for k, body := range files {
		cmdArgs = append(cmdArgs, "--slurpfile", k, write(k+".json", body))
	}
	for k, v := range args {
		cmdArgs = append(cmdArgs, "--argjson", k, v)
	}
	for k, v := range strArgs {
		cmdArgs = append(cmdArgs, "--arg", k, v)
	}
	out, err := exec.Command("jq", cmdArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("jq failed: %v\n%s", err, out)
	}
	return out
}

func iso(epoch int64) string { return time.Unix(epoch, 0).UTC().Format(time.RFC3339) }

// batchFixture is a cluster with two batch applications and the evidence each
// leaves: nightly (prod) does everything right, ledger (prod) does nothing right,
// reports (test only) runs on demand with no schedule.
func batchFixture(now int64) map[string]string {
	job := func(ns, name, bj, trigger, cond string, at int64) string {
		return fmt.Sprintf(`{"apiVersion":"batch/v1","kind":"Job","metadata":{"namespace":%q,"name":%q,"labels":{"adhar.io/batch-job":%q,"adhar.io/batch-trigger":%q}},`+
			`"status":{"conditions":[{"type":%q,"status":"True","lastTransitionTime":%q}]}}`, ns, name, bj, trigger, cond, iso(at))
	}
	bjs := `{"items":[
	  {"metadata":{"namespace":"prod","name":"nightly"},"spec":{"parameters":{"schedule":{"cron":"30 1 * * *"},"sla":{"finishBy":"06:00"},"database":{"secretRef":"nightly-db-app"}}}},
	  {"metadata":{"namespace":"test","name":"nightly"},"spec":{"parameters":{"schedule":{"cron":"30 1 * * *"},"sla":{"finishBy":"06:00"},"database":{"secretRef":"nightly-db-app"}}}},
	  {"metadata":{"namespace":"prod","name":"ledger"},"spec":{"parameters":{"schedule":{"cron":"0 2 * * *"},"alerts":{"enabled":false},"database":{"secretRef":"somewhere-else"}}}},
	  {"metadata":{"namespace":"test","name":"reports"},"spec":{"parameters":{"database":{"secretRef":"reports-db"}}}},
	  {"metadata":{"namespace":"prod","name":"digest"},"spec":{"parameters":{"schedule":{"cron":"0 5 * * *"},"sla":{"finishBy":"07:00"},"database":{"secretRef":"nightly-db-app"}}}}]}`
	rule := func(ns, bj string, alerts ...string) string {
		var rs []string
		for _, a := range alerts {
			rs = append(rs, fmt.Sprintf(`{"alert":%q,"expr":"vector(1)"}`, a))
		}
		return fmt.Sprintf(`{"metadata":{"namespace":%q,"name":"%s-batch","labels":{"adhar.io/batch-job":%q}},"spec":{"groups":[{"name":"g","rules":[%s]}]}}`, ns, bj, bj, strings.Join(rs, ","))
	}
	rules := `{"items":[` + strings.Join([]string{
		rule("prod", "nightly", "BatchRunFailed", "BatchScheduleMissed", "BatchNoRecentSuccess"),
		rule("test", "reports", "BatchRunFailed", "BatchRunTooLong"),
		// a schedule whose rules page on a failed run but not on runs that stopped coming
		rule("prod", "digest", "BatchRunFailed"),
	}, ",") + `]}`
	runs := `{"items":[` + strings.Join([]string{
		// nightly's restart was proven in test; a run 9 days old is outside the window.
		job("test", "nightly-09932cb3-r2", "nightly", "restart", "Complete", now-3600),
		job("prod", "nightly-1a2b3c4d", "nightly", "schedule", "Complete", now-7200),
		job("prod", "nightly-old", "nightly", "schedule", "Failed", now-9*86400),
		// ledger's only restart failed, and its runs mostly failed.
		job("prod", "ledger-77aa11bb-r1", "ledger", "restart", "Failed", now-7200),
		job("prod", "ledger-77aa11bb", "ledger", "schedule", "Failed", now-8000),
		job("prod", "ledger-88cc22dd", "ledger", "schedule", "Complete", now-90000),
		job("test", "reports-5e5e5e5e", "reports", "manual", "Complete", now-600),
		job("prod", "digest-0f0f0f0f-r1", "digest", "restart", "Complete", now-600),
	}, ",") + `]}`
	vcruns := `{"items":[{"apiVersion":"batch.volcano.sh/v1alpha1","kind":"Job","metadata":{"namespace":"prod","name":"nightly-gang","labels":{"adhar.io/batch-job":"nightly","adhar.io/batch-trigger":"manual"}},"status":{"state":{"phase":"Completed","lastTransitionTime":"` + iso(now-500) + `"}}}]}`
	cnpg := `{"items":[
	  {"metadata":{"namespace":"prod","name":"nightly-db"},"spec":{"backup":{"barmanObjectStore":{}}}},
	  {"metadata":{"namespace":"test","name":"nightly-db"},"spec":{}},
	  {"metadata":{"namespace":"test","name":"reports-pg"},"spec":{"bootstrap":{"initdb":{"secret":{"name":"reports-db"}}}}}]}`
	vec := func(label string, rows map[string]int) string {
		var rs []string
		for k, v := range rows {
			ns, bj, _ := strings.Cut(k, "/")
			rs = append(rs, fmt.Sprintf(`{"metric":{"namespace":%q,%q:%q},"value":[%d,"%d"]}`, ns, label, bj, now, v))
		}
		return `{"status":"success","data":{"resultType":"vector","result":[` + strings.Join(rs, ",") + `]}}`
	}
	prom := fmt.Sprintf(`{"native":{"succeeded":%s,"failed":%s},"volcano":{"succeeded":%s,"failed":%s}}`,
		vec("label_adhar_io_batch_job", map[string]int{"prod/nightly": 19, "prod/ledger": 7, "test/reports": 3}),
		vec("label_adhar_io_batch_job", map[string]int{"prod/ledger": 3}),
		vec("batch_job", map[string]int{"prod/nightly": 1}),
		vec("batch_job", map[string]int{}))
	return map[string]string{"bjf": bjs, "rulesf": rules, "runsf": runs, "vcrunsf": vcruns, "cnpgf": cnpg, "promf": prom}
}

type batchRecord struct {
	Scheduled     bool
	FinishBy      *string
	AlertsEnabled bool
	Alerts        []string
	Database      struct {
		Secret   string
		Cluster  *string
		Provider string
		Backup   bool
	}
	Restarts []struct {
		Name, O string
	}
	Outcomes struct {
		Source            string
		Succeeded, Failed int
	}
}

func projectBatch(t *testing.T, script map[string]string, files map[string]string, now int64) (map[string]batchRecord, bool, string) {
	t.Helper()
	out := runJQ(t, script["batch.jq"], files, map[string]string{"now": fmt.Sprint(now), "windowdays": "7"}, nil)
	var got struct {
		Prometheus bool
		ByKey      map[string]batchRecord
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return got.ByKey, got.Prometheus, string(out)
}

// batch.jq turns the cluster's objects into one record per BatchJob: what the
// signals need and nothing else.
func TestTheBatchProjectionReadsRulesRunsAndPostgres(t *testing.T) {
	script := scorerScript(t)
	now := time.Now().Unix()
	files := batchFixture(now)
	by, promOk, raw := projectBatch(t, script, files, now)
	if !promOk || len(by) != 5 {
		t.Fatalf("want 5 BatchJobs with Prometheus answering, got %d (prometheus %t): %s", len(by), promOk, raw)
	}
	n, l, r := by["prod/nightly"], by["prod/ledger"], by["test/reports"]
	if !n.Scheduled || n.FinishBy == nil || *n.FinishBy != "06:00" || !n.AlertsEnabled {
		t.Errorf("nightly: scheduled %t, finishBy %v, alerts %t", n.Scheduled, n.FinishBy, n.AlertsEnabled)
	}
	if strings.Join(n.Alerts, ",") != "BatchNoRecentSuccess,BatchRunFailed,BatchScheduleMissed" {
		t.Errorf("nightly's composed alerts read as %v", n.Alerts)
	}
	if n.Database.Cluster == nil || *n.Database.Cluster != "nightly-db" || !n.Database.Backup {
		t.Errorf("nightly's JobRepository Secret nightly-db-app is CNPG cluster nightly-db's, backed up; got %+v", n.Database)
	}
	// A cluster that bootstraps from a Secret of its own publishes that Secret.
	if r.Database.Cluster == nil || *r.Database.Cluster != "reports-pg" || r.Scheduled {
		t.Errorf("reports runs on demand on reports-pg (bootstrap Secret reports-db); got %+v scheduled %t", r.Database, r.Scheduled)
	}
	if l.AlertsEnabled || l.Database.Cluster != nil || len(l.Alerts) != 0 {
		t.Errorf("ledger: alerts.enabled false and an external database; got alerts %t %v, database %+v", l.AlertsEnabled, l.Alerts, l.Database)
	}
	// Prometheus is the history: native and gang counts add up, per BatchJob.
	if n.Outcomes.Source != "prometheus" || n.Outcomes.Succeeded != 20 || n.Outcomes.Failed != 0 {
		t.Errorf("nightly's outcomes should be 19 native + 1 gang successes from Prometheus, got %+v", n.Outcomes)
	}
	if l.Outcomes.Succeeded != 7 || l.Outcomes.Failed != 3 {
		t.Errorf("ledger's outcomes from Prometheus: 7/3, got %+v", l.Outcomes)
	}
	if len(l.Restarts) != 1 || l.Restarts[0].O != "failed" || len(by["test/nightly"].Restarts) != 1 || by["test/nightly"].Restarts[0].O != "succeeded" {
		t.Errorf("restart runs: ledger's failed, nightly's (test) succeeded; got %+v / %+v", l.Restarts, by["test/nightly"].Restarts)
	}

	// Without Prometheus the runs still kept are the history, inside the window only.
	files["promf"] = `{"native":{"succeeded":null,"failed":null},"volcano":{"succeeded":null,"failed":null}}`
	by, promOk, raw = projectBatch(t, script, files, now)
	if promOk {
		t.Fatalf("Prometheus did not answer, yet the projection says it did: %s", raw)
	}
	if o := by["prod/nightly"].Outcomes; o.Source != "runs" || o.Succeeded != 2 || o.Failed != 0 {
		t.Errorf("nightly from its kept runs: 2 successes (the Job and the gang), the 9-day-old failure outside the window; got %+v", o)
	}
	if o := by["prod/ledger"].Outcomes; o.Succeeded != 1 || o.Failed != 2 {
		t.Errorf("ledger from its kept runs: 1 success, 2 failures (the restart counts); got %+v", o)
	}
}

type scoredSignal struct {
	Name       string
	Applicable bool
	Score      float64
	Detail     string
}

func gradeWithBatch(t *testing.T, script map[string]string, batch, evidence string, now int64) map[string][]scoredSignal {
	t.Helper()
	return gradeBatchApps(t, script, batchApps, batch, evidence, now)
}

// batchApps is the Argo CD applications batchFixture's objects belong to.
const batchApps = `{"items":[
		  {"name":"adhar-nightly-prod","app":"nightly","env":"prod","ns":"prod","health":"Healthy","sync":"Synced","own":{"total":1,"wl":[],"routes":0,"cnpg":[],"batch":["prod/nightly"]}},
		  {"name":"adhar-nightly-test","app":"nightly","env":"test","ns":"test","health":"Healthy","sync":"Synced","own":{"total":1,"wl":[],"routes":0,"cnpg":[],"batch":["test/nightly"]}},
		  {"name":"adhar-ledger-prod","app":"ledger","env":"prod","ns":"prod","health":"Healthy","sync":"Synced","own":{"total":1,"wl":[],"routes":0,"cnpg":[],"batch":["prod/ledger"]}},
		  {"name":"adhar-reports-test","app":"reports","env":"test","ns":"test","health":"Healthy","sync":"Synced","own":{"total":1,"wl":[],"routes":0,"cnpg":[],"batch":["test/reports"]}},
		  {"name":"adhar-digest-prod","app":"digest","env":"prod","ns":"prod","health":"Healthy","sync":"Synced","own":{"total":1,"wl":[],"routes":0,"cnpg":[],"batch":["prod/digest"]}},
		  {"name":"adhar-shop-prod","app":"shop","env":"prod","ns":"prod","health":"Healthy","sync":"Synced","own":{"total":2,"wl":["prod/Rollout/shop"],"routes":1,"cnpg":[]}}]}`

// gradeBatchApps runs score.jq over the given applications and batch projection.
func gradeBatchApps(t *testing.T, script map[string]string, apps, batch, evidence string, now int64) map[string][]scoredSignal {
	t.Helper()
	return gradeBatchAppsWith(t, script, apps, batch, evidence, now, nil)
}

// gradeBatchAppsWith is gradeBatchApps with extra --argjson tunables (they win).
func gradeBatchAppsWith(t *testing.T, script map[string]string, apps, batch, evidence string, now int64, extra map[string]string) map[string][]scoredSignal {
	t.Helper()
	files := map[string]string{
		"appsf": apps,
		"workloadsf": `{"byKey":{"prod/Rollout/shop":{"kind":"Rollout","ns":"prod","name":"shop","replicas":2,"podLabels":{"app":"shop"},"progressive":true,"spread":true,` +
			`"containers":[{"image":"h/library/shop@sha256:1","probes":true,"res":true,"preStop":true}]}},"byInstance":{},"byNs":{}}`,
		"reportsf": `{"byKey":{},"byNs":{}}`, "routesf": `{"nsSet":{}}`, "schedulesf": `{"all":false,"nsSet":{}}`,
		"cnpgf": `{"byKey":{},"byNs":{}}`, "hpasf": `{"byTarget":{}}`, "pdbsf": `{"byNs":{}}`, "podsf": `{}`,
		"evidencef": evidence, "perfcenterf": `{}`, "gamedayf": `{}`, "vulnsf": `{}`, "metricsf": `{}`,
		"batchf": batch,
	}
	args := map[string]string{"batchminpct": "95", "batchwindow": "7", "now": fmt.Sprint(now)}
	for k, v := range map[string]string{"wrel": "35", "wsec": "25", "wobs": "15", "wops": "25", "wperf": "20", "wres": "25",
		"wsup": "20", "weff": "10", "cpumin": "10", "cpumax": "100", "memmin": "20", "memmax": "100",
		"ga": "90", "gb": "80", "gc": "70", "gd": "60", "evdays": "7", "p95default": "500", "recoverymax": "120"} {
		args[k] = v
	}
	for k, v := range extra {
		args[k] = v
	}
	out := runJQ(t, script["score.jq"], files, args, map[string]string{"envorder": "prod,test,dev", "ts": "now", "trusted": "harbor"})
	var res struct {
		Services []struct {
			Name    string
			Signals []scoredSignal
		}
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	by := map[string][]scoredSignal{}
	for _, s := range res.Services {
		by[s.Name] = s.Signals
	}
	return by
}

func signalOf(sigs []scoredSignal, name string) *scoredSignal {
	for i := range sigs {
		if sigs[i].Name == name {
			return &sigs[i]
		}
	}
	return nil
}

var batchSignalNames = []string{"batch_restart_test", "batch_alerts", "batch_jdbc_repository", "batch_deadline", "batch_success_rate"}

// The five rules, graded end to end: a batch application that does everything right
// passes all five; one that does nothing right fails all five, each saying why; an
// on-demand job has no deadline to set; a service gets none of them. A batch
// application serves nothing, so the HTTPRoute, load-test and pod-delete signals,
// which it could never earn, do not count against it.
func TestTheScorerGradesBatchApplications(t *testing.T) {
	script := scorerScript(t)
	if !strings.Contains(script["score.sh"], `jq -n -c -f "$SCRIPT_DIR/batch.jq"`) || !strings.Contains(script["score.sh"], `--slurpfile batchf "$WORKDIR/batch.json"`) {
		t.Fatal("score.sh no longer runs batch.jq and hands its output to score.jq as batchf")
	}
	now := time.Now().Unix()
	batch := runJQ(t, script["batch.jq"], batchFixture(now), map[string]string{"now": fmt.Sprint(now), "windowdays": "7"}, nil)
	by := gradeWithBatch(t, script, string(batch), `{}`, now)

	for _, n := range batchSignalNames {
		s := signalOf(by["nightly"], n)
		if s == nil || !s.Applicable || s.Score != 1 {
			t.Errorf("nightly should pass %s; got %+v", n, s)
		}
		s = signalOf(by["ledger"], n)
		if s == nil || !s.Applicable || s.Score != 0 || s.Detail == "" {
			t.Errorf("ledger should fail %s with a reason; got %+v", n, s)
		}
		if s := signalOf(by["shop"], n); s != nil {
			t.Errorf("the service shop carries %s; a service's ledger must not change", n)
		}
	}
	for n, want := range map[string]string{
		"batch_restart_test":    "no restart proven for ledger",
		"batch_alerts":          "alerts.enabled is false on ledger",
		"batch_jdbc_repository": "ledger (somewhere-else)",
		"batch_deadline":        "no sla.finishBy on ledger",
		"batch_success_rate":    "7 of 10 runs succeeded over 7 days in prod (70%, at least 95% required)",
	} {
		if s := signalOf(by["ledger"], n); s == nil || !strings.Contains(s.Detail, want) {
			t.Errorf("ledger's %s should say %q; got %+v", n, want, s)
		}
	}
	if s := signalOf(by["nightly"], "batch_success_rate"); s == nil || !strings.Contains(s.Detail, "20 of 20 runs") {
		t.Errorf("nightly's success rate counts native and gang runs from Prometheus: %+v", s)
	}
	// digest's rules page on a failed run, but a schedule that stops firing pages nobody.
	if s := signalOf(by["digest"], "batch_alerts"); s == nil || s.Score != 0 || !strings.Contains(s.Detail, "for digest in prod") {
		t.Errorf("a schedule without BatchNoRecentSuccess fails batch_alerts: %+v", s)
	}
	if s := signalOf(by["digest"], "batch_restart_test"); s == nil || s.Score != 1 {
		t.Errorf("digest's restart run completed in prod; that proves it: %+v", s)
	}
	// reports runs on demand: no schedule, so no deadline to set; its restart was never proven.
	if s := signalOf(by["reports"], "batch_deadline"); s == nil || s.Applicable {
		t.Errorf("an on-demand job has no schedule to set a deadline on; batch_deadline should not apply: %+v", s)
	}
	for _, app := range []string{"nightly", "ledger", "reports"} {
		for _, n := range []string{"httproute_exposed", "load_test", "chaos_pod_delete"} {
			if s := signalOf(by[app], n); s == nil || s.Applicable {
				t.Errorf("%s is a batch application; %s should not apply to it: %+v", app, n, s)
			}
		}
	}
	for _, n := range []string{"httproute_exposed", "load_test", "chaos_pod_delete"} {
		if s := signalOf(by["shop"], n); s == nil || !s.Applicable {
			t.Errorf("the service shop must still be graded on %s: %+v", n, s)
		}
	}

	// Without Prometheus, the rate is the kept runs', and the detail says so.
	f := batchFixture(now)
	f["promf"] = `{}`
	batch = runJQ(t, script["batch.jq"], f, map[string]string{"now": fmt.Sprint(now), "windowdays": "7"}, nil)
	by = gradeWithBatch(t, script, string(batch), `{}`, now)
	if s := signalOf(by["ledger"], "batch_success_rate"); s == nil || !strings.Contains(s.Detail, "1 of 3 runs") || !strings.Contains(s.Detail, "Prometheus did not answer") {
		t.Errorf("without Prometheus the rate must come from the kept runs and say so: %+v", s)
	}

	// The resilience runner's batch restart test (adhar-resilience-results
	// .<app>.batchRestart, design 0025 §17 "kill mid-run in test") proves a restart
	// as well as a restarted run does — while it is fresh.
	batch = runJQ(t, script["batch.jq"], batchFixture(now), map[string]string{"now": fmt.Sprint(now), "windowdays": "7"}, nil)
	ev := func(at int64) string {
		return fmt.Sprintf(`{"ledger":{"batchRestart":{"status":"passed","environment":"test","run":"ledger-1111aaaa","testedAtEpoch":%d}}}`, at)
	}
	by = gradeWithBatch(t, script, string(batch), ev(now-3600), now)
	if s := signalOf(by["ledger"], "batch_restart_test"); s == nil || s.Score != 1 || !strings.Contains(s.Detail, "batch restart test passed in test") {
		t.Errorf("a fresh passed batch restart test proves ledger's restart: %+v", s)
	}
	by = gradeWithBatch(t, script, string(batch), ev(now-8*86400), now)
	if s := signalOf(by["ledger"], "batch_restart_test"); s == nil || s.Score != 0 {
		t.Errorf("a batch restart test older than evidence_max_age_days proves nothing: %+v", s)
	}
}

// What score.sh collects for the batch rules, and that the scorer may read it.
func TestTheScorerCollectsTheBatchEvidence(t *testing.T) {
	src := readStackFile(t, "application/adhar-scorecards/manifests/scorer-cronjob.yaml")
	for _, want := range []string{
		"fetch_ns batchjobs.platform.adhar.io batchjobs",
		"fetch_ns prometheusrules.monitoring.coreos.com batchrules adhar.io/batch-job",
		"fetch_ns jobs.batch batchruns adhar.io/batch-job",
		"fetch_ns jobs.batch.volcano.sh vcbatchruns adhar.io/batch-job",
		`kubectl -n "$ns" get "$1" ${3:+-l "$3"} -o json`,
		`select(.kind == "BatchJob" and ((.group // "") == "platform.adhar.io"))`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("score.sh no longer carries %s", want)
		}
	}
	// batch.jq reads cnpg.raw, so it must run before `project cnpg` consumes it.
	if b, p := strings.Index(src, `-f "$SCRIPT_DIR/batch.jq"`), strings.Index(src, "    project cnpg '"); b < 0 || p < 0 || b > p {
		t.Errorf("batch.jq (%d) must run before project cnpg (%d) removes cnpg.raw", b, p)
	}
	reader := stackObject(t, scorecardsDir+"rbac.yaml", "ClusterRole", "adhar-scorecard-reader")
	b, _ := json.Marshal(reader["rules"])
	for _, want := range []string{`"platform.adhar.io"`, `"batchjobs"`, `"monitoring.coreos.com"`, `"prometheusrules"`, `"batch.volcano.sh"`, `"jobs"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the scorer cannot read %s, so its batch signals would silently read empty", want)
		}
	}
	cfg := configMapData(t, scorecardsDir+"scorecard-crd-and-samples.yaml", "adhar-scorecard-config")
	if cfg["batch_success_rate_min_pct"] == "" || cfg["batch_window_days"] != "7" {
		t.Errorf("adhar-scorecard-config lacks batch_success_rate_min_pct or a 7-day batch_window_days: %q / %q", cfg["batch_success_rate_min_pct"], cfg["batch_window_days"])
	}
	// The series the history is read from are the ones kube-state-metrics publishes
	// with the batch-job label (PLAT-1.7 allowlist, PLAT-2.7 Volcano state).
	ksm := readStackFile(t, "observability/kube-prometheus/values.yaml")
	if !regexp.MustCompile(`(?m)^\s*- jobs=\[[^\]]*adhar\.io/batch-job`).MatchString(ksm) {
		t.Error("kube-state-metrics no longer exports the adhar.io/batch-job label on Jobs; the success rate's query would match nothing")
	}
	csm := readStackFile(t, "observability/kube-prometheus/custom-resource-state.yaml")
	if !strings.Contains(csm, "batch_job: [metadata, labels, adhar.io/batch-job]") || !strings.Contains(csm, "path: [status, state, phase]") {
		t.Error("the Volcano Job state metric no longer carries batch_job and the phase; gang runs would drop out of the success rate")
	}
}

// batchQueries runs score.sh's own Prometheus block under bash with promraw stubbed,
// so the queries tested are exactly the ones the scorer sends, window included.
func batchQueries(t *testing.T, windowDays string) []string {
	t.Helper()
	sh := configMapData(t, scorecardsDir+"scorer-cronjob.yaml", "adhar-scorecard-script")["score.sh"]
	start := strings.Index(sh, `W="${BATCH_WINDOW_DAYS}d"`)
	endMarker := `>"$WORKDIR/batchprom.json" 2>/dev/null || echo '{}' >"$WORKDIR/batchprom.json"`
	end := strings.Index(sh, endMarker)
	if start < 0 || end < start {
		t.Fatal("cannot find the batch Prometheus block in score.sh")
	}
	block := sh[start : end+len(endMarker)]
	dir := t.TempDir()
	q := filepath.Join(dir, "queries.txt")
	script := `promraw() { printf '%s\n' "$1" >>"$Q"; echo null; }` + "\n" + block
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), "Q="+q, "WORKDIR="+dir, "BATCH_WINDOW_DAYS="+windowDays)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("score.sh's batch Prometheus block failed: %v\n%s", err, out)
	}
	b, err := os.ReadFile(q)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 4 {
		t.Fatalf("want the 4 batch queries (native succeeded/failed, Volcano succeeded/failed), got %d:\n%s", len(lines), b)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "batchprom.json")); !strings.Contains(string(got), `"native":{"succeeded":null`) {
		t.Errorf("unanswered queries must reach batch.jq as null, got %s", got)
	}
	return lines
}

// The success rate's history, evaluated by Prometheus itself: a run counts ONCE at
// the outcome it reached, after kube-state-metrics restarted (two series for one
// Job) and after the Job was deleted (its series ended); a Job that never completed
// is no success, a non-batch Job is not counted, and a gang counts by its phase.
// promtool runs where it is installed (CI sets ADHAR_REQUIRE_RULE_TOOLS=1); the
// queries are taken from score.sh with a 1-day window so the series stay short.
func TestTheBatchSuccessQueriesCountEachRunOnce(t *testing.T) {
	qs := batchQueries(t, "1")
	for _, q := range qs {
		if !strings.Contains(q, "[1d]") || strings.Contains(q, "[7d]") {
			t.Errorf("the window is not batch_window_days: %s", q)
		}
	}
	promtool := ruleTool(t, "docker run --rm --entrypoint promtool quay.io/prometheus/prometheus:v3.5.0 is the same check", "promtool")
	type expr struct {
		q    string
		want string
	}
	tests := []expr{
		{qs[0], `{namespace="prod", label_adhar_io_batch_job="importer"}`},
		{qs[1], `{namespace="prod", label_adhar_io_batch_job="importer"}`},
		{qs[2], `{namespace="prod", batch_job="settle"}`},
		{qs[3], `{namespace="prod", batch_job="settle"}`},
	}
	var b strings.Builder
	b.WriteString("rule_files: []\nevaluation_interval: 10m\ntests:\n  - interval: 10m\n    input_series:\n")
	for _, s := range [][2]string{
		// importer-a completed, was scraped by two kube-state-metrics pods, then deleted.
		{`kube_job_complete{namespace="prod",job_name="importer-a",condition="true",instance="ksm-1"}`, `_ 1x11`},
		{`kube_job_complete{namespace="prod",job_name="importer-a",condition="true",instance="ksm-2"}`, `_x12 1x5`},
		{`kube_job_labels{namespace="prod",job_name="importer-a",label_adhar_io_batch_job="importer"}`, `_ 1x17`},
		// kube-state-metrics publishes one series per condition status; only "true" is the outcome.
		{`kube_job_failed{namespace="prod",job_name="importer-a",condition="false"}`, `_ 1x11`},
		// importer-b failed; its Complete condition never became true.
		{`kube_job_complete{namespace="prod",job_name="importer-b",condition="true"}`, `_ 0x30`},
		{`kube_job_complete{namespace="prod",job_name="importer-b",condition="false"}`, `_ 1x30`},
		{`kube_job_failed{namespace="prod",job_name="importer-b",condition="true"}`, `_ 0x10 1x20`},
		{`kube_job_labels{namespace="prod",job_name="importer-b",label_adhar_io_batch_job="importer"}`, `_ 1x30`},
		// not a batch run: no batch-job label
		{`kube_job_complete{namespace="prod",job_name="cleanup-1",condition="true"}`, `_ 1x5`},
		{`kube_job_labels{namespace="prod",job_name="cleanup-1"}`, `_ 1x5`},
		// a gang that ran then completed, and one that failed
		{`kube_volcano_job_status_phase{namespace="prod",vcjob="settle-g1",batch_job="settle",phase="Running"}`, `_ 1x5 0x10`},
		{`kube_volcano_job_status_phase{namespace="prod",vcjob="settle-g1",batch_job="settle",phase="Completed"}`, `_ 0x5 1x10`},
		{`kube_volcano_job_status_phase{namespace="prod",vcjob="settle-g2",batch_job="settle",phase="Failed"}`, `_ 1x3`},
		{`kube_volcano_job_status_phase{namespace="prod",vcjob="other-g",phase="Completed"}`, `_ 1x3`},
	} {
		fmt.Fprintf(&b, "      - series: '%s'\n        values: '%s'\n", s[0], s[1])
	}
	b.WriteString("    promql_expr_test:\n")
	for _, e := range tests {
		fmt.Fprintf(&b, "      - expr: %s\n        eval_time: 1d\n        exp_samples:\n          - labels: '%s'\n            value: 1\n", fmt.Sprintf("%q", e.q), e.want)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tests.yaml"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(promtool, "test", "rules", "tests.yaml")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("promtool test rules: %v\n%s\n%s", err, out, b.String())
	}
}
