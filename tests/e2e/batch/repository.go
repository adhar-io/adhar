package batch

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// The countable sink for "no duplicate rows" (A1.1, NEW-3). The template's
// sample writer only logs, so the suite reads what Spring Batch COMMITTED: a
// chunk's WRITE_COUNT is updated in the same transaction as the chunk's
// writes (§5.9), so the write counts of every execution of one JobInstance add
// up to the input size exactly when nothing was written twice and nothing was
// lost — whichever pod was killed, and however often. The JobRepository is the
// application's own Postgres, schema `batch` (§5.8).

const repositorySchema = "batch"

// Execution is a row of BATCH_JOB_EXECUTION.
type Execution struct {
	ID, Instance     int64
	Status, ExitCode string
}

// StepExecution is a row of BATCH_STEP_EXECUTION.
type StepExecution struct {
	Execution    int64
	Name, Status string
	Written      int64
}

// Ledger is everything the repository holds for one identity.
type Ledger struct {
	Instances  []int64
	Executions []Execution
	Steps      []StepExecution
}

func sqlString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// LedgerSQL selects the executions and steps of every JobInstance whose
// identifying parameters include all of identifying, for `psql -At -F'|'`.
func LedgerSQL(identifying map[string]string) string {
	keys := make([]string, 0, len(identifying))
	for k := range identifying {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var match []string
	for _, k := range keys {
		match = append(match, fmt.Sprintf("(p.parameter_name = %s AND p.parameter_value = %s)", sqlString(k), sqlString(identifying[k])))
	}
	if len(match) == 0 {
		match = []string{"false"}
	}
	s := repositorySchema
	return fmt.Sprintf(`WITH matched AS (
  SELECT e.job_execution_id, e.job_instance_id FROM %[1]s.batch_job_execution e
  JOIN %[1]s.batch_job_execution_params p ON p.job_execution_id = e.job_execution_id
  WHERE p.identifying = 'Y' AND (%[2]s)
  GROUP BY e.job_execution_id, e.job_instance_id HAVING count(*) = %[3]d),
inst AS (SELECT DISTINCT job_instance_id FROM matched)
SELECT 'E', e.job_instance_id, e.job_execution_id, e.status, coalesce(e.exit_code, ''), 0
  FROM %[1]s.batch_job_execution e WHERE e.job_instance_id IN (SELECT job_instance_id FROM inst)
UNION ALL
SELECT 'S', e.job_instance_id, s.job_execution_id, s.status, s.step_name, s.write_count
  FROM %[1]s.batch_step_execution s JOIN %[1]s.batch_job_execution e ON e.job_execution_id = s.job_execution_id
  WHERE e.job_instance_id IN (SELECT job_instance_id FROM inst)
ORDER BY 1, 3, 5;`, s, strings.Join(match, " OR "), len(keys))
}

// ParseLedger reads LedgerSQL's output.
func ParseLedger(out string) (Ledger, error) {
	var l Ledger
	seen := map[int64]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "|")
		if len(f) != 6 {
			return l, fmt.Errorf("unexpected repository row %q", line)
		}
		inst, err1 := strconv.ParseInt(f[1], 10, 64)
		exec, err2 := strconv.ParseInt(f[2], 10, 64)
		n, err3 := strconv.ParseInt(f[5], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			return l, fmt.Errorf("unexpected repository row %q", line)
		}
		if !seen[inst] {
			seen[inst] = true
			l.Instances = append(l.Instances, inst)
		}
		switch f[0] {
		case "E":
			l.Executions = append(l.Executions, Execution{ID: exec, Instance: inst, Status: f[3], ExitCode: f[4]})
		case "S":
			l.Steps = append(l.Steps, StepExecution{Execution: exec, Status: f[3], Name: f[4], Written: n})
		default:
			return l, fmt.Errorf("unexpected repository row %q", line)
		}
	}
	return l, nil
}

// Completed are the COMPLETED executions.
func (l Ledger) Completed() []Execution {
	var out []Execution
	for _, e := range l.Executions {
		if e.Status == "COMPLETED" {
			out = append(out, e)
		}
	}
	return out
}

// Last is the newest execution.
func (l Ledger) Last() (Execution, bool) {
	var last Execution
	for _, e := range l.Executions {
		if e.ID > last.ID {
			last = e
		}
	}
	return last, last.ID != 0
}

// StatusOf is the status of execution id, "" when the ledger lacks it.
func (l Ledger) StatusOf(id int64) string {
	for _, e := range l.Executions {
		if e.ID == id {
			return e.Status
		}
	}
	return ""
}

// Writes is the committed items of the given executions (all when none is
// named). A partitioned step's manager aggregates its workers' counts, so when
// worker steps exist (`<step>:<partition>`) only those are counted.
func (l Ledger) Writes(executions ...int64) int64 {
	workers := false
	for _, s := range l.Steps {
		workers = workers || strings.Contains(s.Name, ":")
	}
	var n int64
	for _, s := range l.Steps {
		if workers && !strings.Contains(s.Name, ":") {
			continue
		}
		if len(executions) > 0 && !containsID(executions, s.Execution) {
			continue
		}
		n += s.Written
	}
	return n
}

func containsID(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// CheckExactlyOnce is A1.1's verdict: one JobInstance, ONE completed execution
// and it is the newest, and the committed writes of all executions equal the
// input — every item written once, however many pods it took.
func (l Ledger) CheckExactlyOnce(items int64) error {
	if len(l.Instances) != 1 {
		return fmt.Errorf("the identity has %d JobInstances %v, want 1", len(l.Instances), l.Instances)
	}
	done := l.Completed()
	if len(done) != 1 {
		return fmt.Errorf("%d COMPLETED executions %+v, want exactly 1", len(done), done)
	}
	if last, _ := l.Last(); last.ID != done[0].ID {
		return fmt.Errorf("execution %d is newer than the COMPLETED one (%d): %+v", last.ID, done[0].ID, last)
	}
	if w := l.Writes(); w != items {
		return fmt.Errorf("%d items committed over %d executions for an input of %d: %s", w, len(l.Executions), items,
			map[bool]string{true: "written twice", false: "lost"}[w > items])
	}
	return nil
}

// The input table the database reader pages (`batch.table`, key `id`, payload
// `payload` in the template's application.properties), owned by the
// application's database role so the run can read it.
const inputTable = "public.batch_input"

// SeedSQL replaces the input with items rows; run as the cluster's superuser.
func SeedSQL(owner string, items int) string {
	return fmt.Sprintf(`SET ROLE %[1]s;
CREATE TABLE IF NOT EXISTS %[2]s (id bigint PRIMARY KEY, payload text NOT NULL);
TRUNCATE %[2]s;
INSERT INTO %[2]s SELECT g, 'e2e-' || g FROM generate_series(1, %[3]d) g;`, owner, inputTable, items)
}

// PoisonSQL makes one row fail the sample processor (it refuses any payload
// containing FAIL); HealSQL restores it.
func PoisonSQL(owner string, id int) string {
	return fmt.Sprintf("SET ROLE %s; UPDATE %s SET payload = 'FAIL-e2e-%d' WHERE id = %d;", owner, inputTable, id, id)
}

func HealSQL(owner string, id int) string {
	return fmt.Sprintf("SET ROLE %s; UPDATE %s SET payload = 'e2e-%d' WHERE id = %d;", owner, inputTable, id, id)
}
