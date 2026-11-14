package batch

import (
	"strings"
	"testing"
)

func TestBatchLedgerSQLReadsTheBatchSchemaByIdentity(t *testing.T) {
	sql := LedgerSQL(map[string]string{"source": "database", "businessDate": "2031-05-04"})
	for _, want := range []string{
		"batch.batch_job_execution e", "batch.batch_job_execution_params p", "batch.batch_step_execution s",
		"p.identifying = 'Y'",
		"(p.parameter_name = 'businessDate' AND p.parameter_value = '2031-05-04') OR (p.parameter_name = 'source' AND p.parameter_value = 'database')",
		"HAVING count(*) = 2",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("ledger SQL lacks %q:\n%s", want, sql)
		}
	}
	if q := LedgerSQL(map[string]string{"note": "o'clock"}); !strings.Contains(q, "'o''clock'") {
		t.Errorf("a quote in a value was not escaped:\n%s", q)
	}
	if q := LedgerSQL(nil); !strings.Contains(q, "AND (false)") {
		t.Errorf("an empty identity must match nothing:\n%s", q)
	}
}

// A run killed once and resumed: execution 7 committed 3 chunks of partition0
// and all of partition1, execution 8 finished partition0 only.
const resumedLedger = `E|3|7|FAILED|FAILED|0
E|3|8|COMPLETED|COMPLETED|0
S|3|7|FAILED|managerStep|250
S|3|7|COMPLETED|workerStep:partition1|100
S|3|7|FAILED|workerStep:partition0|150
S|3|8|COMPLETED|managerStep|50
S|3|8|COMPLETED|workerStep:partition0|50
`

func TestBatchLedgerParsesWhatPsqlPrints(t *testing.T) {
	l, err := ParseLedger(resumedLedger)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Instances) != 1 || len(l.Executions) != 2 || len(l.Steps) != 5 {
		t.Fatalf("ledger: %+v", l)
	}
	if last, _ := l.Last(); last.ID != 8 || last.Status != "COMPLETED" {
		t.Errorf("last = %+v", last)
	}
	if w := l.Writes(7); w != 250 {
		t.Errorf("execution 7 committed %d worker items, want 250 (the manager's aggregate is not counted)", w)
	}
	if _, err := ParseLedger("E|x|7|FAILED|FAILED|0"); err == nil {
		t.Error("a malformed row was accepted")
	}
	if l, err := ParseLedger(""); err != nil || len(l.Executions) != 0 {
		t.Errorf("no rows: %+v %v", l, err)
	}
}

func TestBatchExactlyOnceVerdict(t *testing.T) {
	l, _ := ParseLedger(resumedLedger)
	if err := l.CheckExactlyOnce(300); err != nil {
		t.Errorf("a clean resume was refused: %v", err)
	}
	if err := l.CheckExactlyOnce(250); err == nil || !strings.Contains(err.Error(), "written twice") {
		t.Errorf("a chunk written twice passed: %v", err)
	}
	if err := l.CheckExactlyOnce(350); err == nil || !strings.Contains(err.Error(), "lost") {
		t.Errorf("lost items passed: %v", err)
	}
	for name, rows := range map[string]string{
		"two completed":      resumedLedger + "E|3|9|COMPLETED|COMPLETED|0\n",
		"an older completed": resumedLedger + "E|3|6|COMPLETED|COMPLETED|0\n",
		"newer than done":    resumedLedger + "E|3|9|STARTED||0\n",
		"two instances":      resumedLedger + "E|4|10|COMPLETED|COMPLETED|0\n",
		"a second instance":  resumedLedger + "E|4|5|FAILED|FAILED|0\n",
		"nothing completed":  "E|3|7|FAILED|FAILED|0\nS|3|7|FAILED|workerStep:partition0|300\n",
	} {
		l, err := ParseLedger(rows)
		if err != nil {
			t.Fatal(err)
		}
		if err := l.CheckExactlyOnce(300); err == nil {
			t.Errorf("%s: passed", name)
		}
	}
	// A job that is not partitioned has no worker steps: every step counts.
	flat, _ := ParseLedger("E|1|1|COMPLETED|COMPLETED|0\nS|1|1|COMPLETED|importStep|40\n")
	if err := flat.CheckExactlyOnce(40); err != nil {
		t.Error(err)
	}
}

func TestBatchInputSQLRunsAsTheApplicationsRole(t *testing.T) {
	seed := SeedSQL("app", 20000)
	for _, want := range []string{"SET ROLE app;", "CREATE TABLE IF NOT EXISTS public.batch_input (id bigint PRIMARY KEY, payload text NOT NULL)",
		"TRUNCATE public.batch_input", "generate_series(1, 20000)"} {
		if !strings.Contains(seed, want) {
			t.Errorf("seed SQL lacks %q", want)
		}
	}
	if p := PoisonSQL("app", 7); !strings.Contains(p, "'FAIL-e2e-7' WHERE id = 7") || !strings.HasPrefix(p, "SET ROLE app;") {
		t.Errorf("poison SQL: %s", p)
	}
	if h := HealSQL("app", 7); !strings.Contains(h, "'e2e-7' WHERE id = 7") || strings.Contains(h, "FAIL") {
		t.Errorf("heal SQL: %s", h)
	}
}
