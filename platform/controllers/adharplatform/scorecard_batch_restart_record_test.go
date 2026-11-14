package adharplatform

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

// batch_restart_test's proof is a restart run that completed, and a run is deleted
// ttlSecondsAfterFinished (7 days) after it ends. Graded on the runs still kept, a
// job whose restart was proven last week failed again this week, so every batch
// application failed the signal unless somebody restarted a run every seven days
// (design 0025 §15 Phase 3, PLAT-3.7, verifier 2026-11-14). The scorer now records
// each BatchJob's newest restart that ended in ConfigMap adhar-scorecards-batch-
// restarts and grades it against batch_restart_max_age_days instead of the TTL.

func restartRecord(entries map[string][3]any) string {
	var rows []string
	for k, v := range entries {
		rows = append(rows, fmt.Sprintf(`%q:{"name":%q,"o":%q,"at":%d}`, k, v[0], v[1], v[2]))
	}
	sort.Strings(rows)
	return "{" + strings.Join(rows, ",") + "}"
}

func projectWithRecord(t *testing.T, script, files map[string]string, record string, now int64) string {
	t.Helper()
	f := map[string]string{}
	for k, v := range files {
		f[k] = v
	}
	if record != "" {
		f["restartsf"] = record
	}
	return string(runJQ(t, script["batch.jq"], f, map[string]string{"now": fmt.Sprint(now), "windowdays": "7"}, nil))
}

func TestARecordedBatchRestartOutlivesItsRun(t *testing.T) {
	script := scorerScript(t)
	now := time.Now().Unix()
	files := batchFixture(now)
	restartOf := func(batch string, extra map[string]string) *scoredSignal {
		t.Helper()
		return signalOf(gradeBatchAppsWith(t, script, batchApps, batch, `{}`, now, extra)["reports"], "batch_restart_test")
	}

	// reports (test) has never kept a restart run: unproven, and the detail says so.
	if s := restartOf(projectWithRecord(t, script, files, "", now), nil); s == nil || s.Score != 0 ||
		!strings.Contains(s.Detail, "no restart proven for reports (never restarted)") {
		t.Fatalf("reports has no restart at all and must fail batch_restart_test: %+v", s)
	}
	// Its restart completed ten days ago; the run is gone, the record is not.
	tenDays := restartRecord(map[string][3]any{"test/reports": {"reports-5e5e5e5e-r1", "succeeded", now - 10*86400}})
	batch := projectWithRecord(t, script, files, tenDays, now)
	if s := restartOf(batch, nil); s == nil || s.Score != 1 || !strings.Contains(s.Detail, "reports-5e5e5e5e-r1") {
		t.Errorf("a recorded restart that completed ten days ago proves reports' restart after its run's TTL: %+v", s)
	}
	// ...for batch_restart_max_age_days, which is a tunable, not the TTL.
	if s := restartOf(batch, map[string]string{"batchrestartdays": "5"}); s == nil || s.Score != 0 ||
		!strings.Contains(s.Detail, "last proven by reports-5e5e5e5e-r1") || !strings.Contains(s.Detail, "older than 5 days") {
		t.Errorf("a proof older than batch_restart_max_age_days proves nothing and must say why: %+v", s)
	}
	old := restartRecord(map[string][3]any{"test/reports": {"reports-5e5e5e5e-r1", "succeeded", now - 40*86400}})
	if s := restartOf(projectWithRecord(t, script, files, old, now), nil); s == nil || s.Score != 0 || !strings.Contains(s.Detail, "older than 30 days") {
		t.Errorf("the default window is 30 days: %+v", s)
	}
	// A failed restart is remembered too: it does not fade back into an older success.
	failed := restartRecord(map[string][3]any{"test/reports": {"reports-5e5e5e5e-r2", "failed", now - 9*86400}})
	if s := restartOf(projectWithRecord(t, script, files, failed, now), nil); s == nil || s.Score != 0 ||
		!strings.Contains(s.Detail, "its newest restart, reports-5e5e5e5e-r2, failed") {
		t.Errorf("a recorded failed restart is the newest restart, and it failed: %+v", s)
	}
	// A record older than the BatchJob describes the job that was deleted, not this one.
	recreated := strings.Replace(files["bjf"], `{"namespace":"test","name":"reports"}`,
		fmt.Sprintf(`{"namespace":"test","name":"reports","creationTimestamp":%q}`, iso(now-3*86400)), 1)
	if recreated == files["bjf"] {
		t.Fatal("batchFixture no longer declares test/reports the way this test rewrites it")
	}
	f := map[string]string{}
	for k, v := range files {
		f[k] = v
	}
	f["bjf"] = recreated
	if s := restartOf(projectWithRecord(t, script, f, tenDays, now), nil); s == nil || s.Score != 0 || !strings.Contains(s.Detail, "never restarted") {
		t.Errorf("a record from before the BatchJob was created proves nothing about it: %+v", s)
	}

	// A kept run newer than the record decides: ledger's kept restart failed two hours
	// ago, so last week's recorded success does not prove it.
	ledger := restartRecord(map[string][3]any{"prod/ledger": {"ledger-77aa11bb-r0", "succeeded", now - 6*86400}})
	if s := signalOf(gradeWithBatch(t, script, projectWithRecord(t, script, files, ledger, now), `{}`, now)["ledger"], "batch_restart_test"); s == nil || s.Score != 0 ||
		!strings.Contains(s.Detail, "ledger-77aa11bb-r1, failed") {
		t.Errorf("the newest restart wins, and ledger's newest kept restart failed: %+v", s)
	}

	// What batch.jq hands score.sh to record: the newest restart that ended per
	// BatchJob, kept or recorded, failures included, and nothing for one with none.
	var out struct {
		LastRestarts map[string]struct {
			Name, O string
			At      int64
		}
	}
	if err := json.Unmarshal([]byte(projectWithRecord(t, script, files, failed, now)), &out); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"test/nightly": "nightly-09932cb3-r2/succeeded", "prod/ledger": "ledger-77aa11bb-r1/failed",
		"prod/digest": "digest-0f0f0f0f-r1/succeeded", "test/reports": "reports-5e5e5e5e-r2/failed",
	}
	if len(out.LastRestarts) != len(want) {
		t.Errorf("lastRestarts should name exactly %v, got %+v", want, out.LastRestarts)
	}
	for k, w := range want {
		if r := out.LastRestarts[k]; r.Name+"/"+r.O != w || r.At == 0 {
			t.Errorf("lastRestarts[%s] = %+v, want %s", k, r, w)
		}
	}
}

// restartsBlocks returns score.sh's read and record blocks for the restart proofs.
func restartsBlocks(t *testing.T) (read, record string) {
	t.Helper()
	sh := scorerScript(t)["score.sh"]
	cut := func(from, to string) string {
		i := strings.Index(sh, from)
		j := strings.Index(sh, to)
		if i < 0 || j < i {
			t.Fatalf("score.sh no longer carries the block %q … %q", from, to)
		}
		return sh[i : j+len(to)]
	}
	return cut(`RESTARTS_CM="${OUT_CM}-batch-restarts"`, `rm -f "$WORKDIR/restarts.raw" "$WORKDIR/restarts.err"`),
		cut(`if [ "$RESTARTS_OK" = 1 ]`, `rm -f "$WORKDIR/restarts.json" "$WORKDIR/restarts.next"`)
}

// runRestartsRecord runs score.sh's two blocks under bash with kubectl stubbed: get
// answers with getOut (exit 0) or getErr (exit 1), create records what it would
// write. It returns the record written, or "" when nothing was written.
func runRestartsRecord(t *testing.T, getOut, getErr, batchJSON string, now int64) string {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	read, record := restartsBlocks(t)
	dir := t.TempDir()
	written := filepath.Join(dir, "written.json")
	stub := `kubectl() {
  case "$*" in
    *"get configmap"*) if [ -n "$GET_OUT" ]; then printf '%s' "$GET_OUT"; else printf '%s\n' "$GET_ERR" >&2; return 1; fi ;;
    *"create configmap"*) for a in "$@"; do case "$a" in --from-file=restarts.json=*) cp "${a#--from-file=restarts.json=}" "$WRITTEN" ;; esac; done; echo "kind: ConfigMap" ;;
    *apply*) cat >/dev/null ;;
    *) : ;;
  esac
}
date() { if [ "$1" = "-u" ] && [ "$2" = "+%s" ]; then echo "$NOW"; else command date "$@"; fi; }
set -euo pipefail
`
	if err := os.WriteFile(filepath.Join(dir, "batch.json"), []byte(batchJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", stub+read+"\n"+record+"\n")
	cmd.Env = append(os.Environ(), "WORKDIR="+dir, "WRITTEN="+written, "OUT_CM=adhar-scorecards", "OUT_NS=adhar-system",
		"BATCH_RESTART_DAYS=30", "GET_OUT="+getOut, "GET_ERR="+getErr, fmt.Sprintf("NOW=%d", now))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("score.sh's restart-record blocks failed: %v\n%s", err, out)
	}
	b, err := os.ReadFile(written)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The record is merged, bounded, and never erased by an API error.
func TestTheBatchRestartRecordIsMergedAndSurvivesAReadError(t *testing.T) {
	now := time.Now().Unix()
	cm := func(record string) string {
		b, _ := json.Marshal(map[string]any{"data": map[string]string{"restarts.json": record}})
		return string(b)
	}
	prev := restartRecord(map[string][3]any{
		"prod/kept-away": {"kept-away-1-r1", "succeeded", now - 3*86400}, // its BatchJob missed this listing
		"prod/ancient":   {"ancient-1-r1", "succeeded", now - 45*86400},  // outside the 30-day window
		"prod/ledger":    {"ledger-0-r1", "succeeded", now - 20*86400},   // superseded by this run's
	})
	batch := `{"prometheus":true,"byKey":{},"lastRestarts":{"prod/ledger":{"name":"ledger-77aa11bb-r1","o":"failed","at":` + fmt.Sprint(now-7200) + `}}}`

	got := runRestartsRecord(t, cm(prev), "", batch, now)
	var rec map[string]struct {
		Name, O string
		At      int64
	}
	if err := json.Unmarshal([]byte(got), &rec); err != nil {
		t.Fatalf("the record written is not JSON: %v: %q", err, got)
	}
	if r, ok := rec["prod/kept-away"]; !ok || r.Name != "kept-away-1-r1" {
		t.Errorf("a BatchJob missing from one listing must keep its record (merged, never replaced): %+v", rec)
	}
	if _, ok := rec["prod/ancient"]; ok {
		t.Errorf("a record older than batch_restart_max_age_days decides nothing and must be dropped: %+v", rec)
	}
	if r := rec["prod/ledger"]; r.Name != "ledger-77aa11bb-r1" || r.O != "failed" {
		t.Errorf("this run's newest restart replaces the recorded one: %+v", r)
	}
	// Absent: a fresh record.
	if got := runRestartsRecord(t, "", `Error from server (NotFound): configmaps "adhar-scorecards-batch-restarts" not found`, batch, now); !strings.Contains(got, "ledger-77aa11bb-r1") {
		t.Errorf("with no record yet, this run's restarts are recorded: %q", got)
	}
	// Unreadable: nothing is written, so the proofs survive the next run.
	if got := runRestartsRecord(t, "", "Unable to connect to the server: i/o timeout", batch, now); got != "" {
		t.Errorf("a record that could not be read must not be rewritten from nothing: %q", got)
	}
	// batch.jq failed (batch.json is {}): nothing is written either.
	if got := runRestartsRecord(t, cm(prev), "", `{}`, now); got != "" {
		t.Errorf("without batch.jq's lastRestarts the record must not be rewritten: %q", got)
	}
}

// A JobRepository on a cloud Postgres the platform composes — a Database XR on RDS
// or Cloud SQL — is a platform Postgres; one on Azure's SQL Server composition, on
// a non-Postgres engine, or a Secret no Database writes, is not.
func TestACloudDatabaseIsAPlatformBatchJobRepository(t *testing.T) {
	script := scorerScript(t)
	now := time.Now().Unix()
	db := func(name, extra string) string {
		return fmt.Sprintf(`{"metadata":{"namespace":"prod","name":%q},"spec":{%s}}`, name, extra)
	}
	dbs := `{"items":[` + strings.Join([]string{
		db("ledger-rds", `"compositionSelector":{"matchLabels":{"provider":"aws"}},"parameters":{"engine":"postgresql"},"writeConnectionSecretToRef":{"namespace":"prod","name":"ledger-conn"}`),
		db("ledger-sql", `"crossplane":{"compositionRef":{"name":"database-gcp-cloudsql"}},"parameters":{"engine":"postgresql","enableBackup":false},"writeConnectionSecretToRef":{"namespace":"prod"}`),
		db("ledger-mssql", `"compositionSelector":{"matchLabels":{"provider":"azure"}},"parameters":{"engine":"postgresql"},"writeConnectionSecretToRef":{"namespace":"prod","name":"mssql-conn"}`),
		db("ledger-mysql", `"compositionSelector":{"matchLabels":{"provider":"aws"}},"parameters":{"engine":"mysql"},"writeConnectionSecretToRef":{"namespace":"prod","name":"mysql-conn"}`),
		db("ledger-local", `"compositionSelector":{"matchLabels":{"provider":"local"}},"parameters":{"engine":"postgresql"},"writeConnectionSecretToRef":{"namespace":"prod","name":"local-conn"}`),
		db("ledger-default", `"compositionSelector":{"matchLabels":{"provider":"aws"}},"parameters":{"engine":"postgresql"}`),
	}, ",") + `]}`
	project := func(secret string) batchRecord {
		t.Helper()
		f := batchFixture(now)
		f["bjf"] = strings.Replace(f["bjf"], `"secretRef":"somewhere-else"`, fmt.Sprintf(`"secretRef":%q`, secret), 1)
		f["dbf"] = dbs
		by, _, _ := projectBatch(t, script, f, now)
		return by["prod/ledger"]
	}
	for secret, want := range map[string]string{"ledger-conn": "ledger-rds/aws/true", "ledger-sql-db": "ledger-sql/gcp/false", "mssql-conn": "", "mysql-conn": "", "local-conn": "", "ledger-default-db": ""} {
		r := project(secret)
		got := ""
		if r.Database.Cluster != nil {
			got = fmt.Sprintf("%s/%s/%t", *r.Database.Cluster, r.Database.Provider, r.Database.Backup)
		}
		if got != want {
			t.Errorf("a JobRepository on Secret %s should read as %q, got %q", secret, want, got)
		}
	}
	// ledger-sql's ref names only a namespace, so its Secret is <name>-db there (above);
	// ledger-default names none, so its Secret is in adhar-system and no job in prod
	// can mount it. A named Secret is matched by its name.
	f := batchFixture(now)
	f["dbf"] = strings.Replace(dbs, `"writeConnectionSecretToRef":{"namespace":"prod"}`, `"writeConnectionSecretToRef":{"namespace":"prod","name":"ledger-sql-conn"}`, 1)
	f["bjf"] = strings.Replace(f["bjf"], `"secretRef":"somewhere-else"`, `"secretRef":"ledger-sql-conn"`, 1)
	by, _, _ := projectBatch(t, script, f, now)
	if r := by["prod/ledger"]; r.Database.Cluster == nil || *r.Database.Cluster != "ledger-sql" || r.Database.Provider != "gcp" || r.Database.Backup {
		t.Errorf("a Database whose compositionRef is the Cloud SQL composition is a platform Postgres (backups off as declared): %+v", r.Database)
	}
	f = batchFixture(now)
	f["dbf"] = dbs
	f["bjf"] = strings.Replace(f["bjf"], `"secretRef":"somewhere-else"`, `"secretRef":"ledger-conn"`, 1)
	batch := string(runJQ(t, script["batch.jq"], f, map[string]string{"now": fmt.Sprint(now), "windowdays": "7"}, nil))
	if s := signalOf(gradeWithBatch(t, script, batch, `{}`, now)["ledger"], "batch_jdbc_repository"); s == nil || s.Score != 1 ||
		!strings.Contains(s.Detail, "ledger on Database ledger-rds (aws) (backed up)") {
		t.Errorf("ledger's JobRepository on an RDS Database passes batch_jdbc_repository: %+v", s)
	}

	// The cloud PostgreSQL compositions batch.jq names are exactly the shipped ones
	// labelled engine: postgresql outside the local provider, so a new one (or a
	// renamed one) cannot silently read as "not a platform Postgres".
	m := regexp.MustCompile(`(\{"database-[^}]*\}) as \$pgCompositions`).FindStringSubmatch(script["batch.jq"])
	if m == nil {
		t.Fatal("batch.jq no longer names the cloud PostgreSQL compositions as $pgCompositions")
	}
	var named map[string]string
	if err := json.Unmarshal([]byte(m[1]), &named); err != nil {
		t.Fatal(err)
	}
	shipped := map[string]string{}
	dir := "../../controlplane/configuration/compositions/database"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var c struct {
			Kind     string
			Metadata struct {
				Name   string
				Labels map[string]string
			}
		}
		if err := yaml.Unmarshal(raw, &c); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		if c.Kind == "Composition" && c.Metadata.Labels["engine"] == "postgresql" && c.Metadata.Labels["provider"] != "local" {
			shipped[c.Metadata.Name] = c.Metadata.Labels["provider"]
		}
	}
	if fmt.Sprint(named) != fmt.Sprint(shipped) {
		t.Errorf("batch.jq's cloud PostgreSQL compositions %v differ from the shipped ones %v", named, shipped)
	}

	// score.sh collects the Databases and hands them, with the restart record, to batch.jq.
	sh := script["score.sh"]
	for _, want := range []string{
		"fetch_ns databases.platform.adhar.io databases",
		`--slurpfile dbf "$WORKDIR/databases.raw" --slurpfile restartsf "$WORKDIR/restarts.json"`,
		`--argjson batchrestartdays "$BATCH_RESTART_DAYS"`,
	} {
		if !strings.Contains(sh, want) {
			t.Errorf("score.sh no longer carries %s", want)
		}
	}
	reader := stackObject(t, scorecardsDir+"rbac.yaml", "ClusterRole", "adhar-scorecard-reader")
	b, _ := json.Marshal(reader["rules"])
	if !regexp.MustCompile(`"apiGroups":\["platform.adhar.io"\],"resources":\[[^\]]*"databases"`).Match(b) {
		t.Error("the scorer cannot list Databases, so a cloud JobRepository would silently read as not a platform Postgres")
	}
	if cfg := configMapData(t, scorecardsDir+"scorecard-crd-and-samples.yaml", "adhar-scorecard-config"); cfg["batch_restart_max_age_days"] != "30" {
		t.Errorf("adhar-scorecard-config should set batch_restart_max_age_days to 30, got %q", cfg["batch_restart_max_age_days"])
	}
}
