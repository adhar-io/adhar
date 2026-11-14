//go:build e2e

// Package batch is the Phase-1 batch suite on Kind (design 0025 §17, E2E-1.1):
// scaffold a Spring Batch job, then run, kill, evict, stop, resume, restart and
// refuse it, and watch the clock run it — A1.1, A1.2, A1.3, A1.7 and A1.9.
// Opt-in (ADHAR_E2E_BATCH=1); the knobs are in config.go, how to run it in
// README.md.
package batch

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// The scaffolded overlays place the job in the pinned environments: dev
// suspends the cron (a dev run is started by a person), test runs it.
const (
	devNS  = "dev"
	testNS = "test"
)

var applicationGVR = schema.GroupVersionResource{Group: "platform.adhar.io", Version: "v1alpha1", Resource: "applications"}

type suite struct {
	cfg Config
	c   *Cluster
	ctx context.Context
	tpl *RunTemplate // dev's ConfigMap <app>-run
}

func Test_BatchOnKind(t *testing.T) {
	cfg, _ := settings(t)
	if !cfg.Enabled {
		t.Skip("the batch suite is opt-in: ADHAR_E2E_BATCH=1 (it scaffolds a JVM job and waits for its kpack build)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Overall)
	defer cancel()
	// One bring-up for the package (harness_e2e_test.go): with ADHAR_E2E_VOLCANO
	// it is the Kind + Volcano variant, and this suite is A2.9's evidence that the
	// Phase-1 behaviours hold with Volcano scheduling the runs.
	e := platform(t)
	s := &suite{cfg: cfg, c: e.c, ctx: ctx}

	if !t.Run("scaffold: the template becomes a built, composed batch job (A1.7)", s.scaffold) {
		t.FailNow()
	}
	t.Run("a manual run completes and is named by D1 (A1.7)", s.manualRun)
	t.Run("invalid parameters fail the Job with no retry (A1.9)", s.invalidParams)
	t.Run("two runs of one identity execute once (A1.2)", s.doubleFire)
	if t.Run("seed the input the interrupted runs read", s.seed) {
		t.Run("a deleted pod resumes from its last commit (A1.1)", func(t *testing.T) { s.interrupted(t, 3, Delete) })
		t.Run("an evicted pod resumes from its last commit (A1.1)", func(t *testing.T) { s.interrupted(t, 4, Evict) })
		t.Run("a SIGKILLed pod is recovered and resumes from its last commit (A1.1, F1)", func(t *testing.T) { s.interrupted(t, 7, Kill) })
		t.Run("stop, then resume (A1.3)", s.stopResume)
		t.Run("a failed run restarts as -r1 from its last commit", s.restart)
	}
	t.Run("the clock runs the job in test, once per business date (A1.7, A1.2)", s.scheduled)
}

func (s *suite) budget(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(s.ctx, d)
}

/* ─────────────── scaffold (A1.7) ─────────────── */

// scaffold is shared with the Volcano canary through ensureApp: the first
// suite to ask builds the app, the other reads what it built.
func (s *suite) scaffold(t *testing.T) {
	tpl, err := ensureApp(t, shared.env, phaseOneApp(s.cfg))
	require.NoError(t, err)
	s.tpl = tpl
}

/* ─────────────── manual run (A1.7, D1, D2) ─────────────── */

// runCLI is `adhar batch run <app> -n ns -p … [--wait] -o json`.
func (s *suite) runCLI(t *testing.T, ns string, values map[string]string, wait bool) (RunOutcome, CLIResult) {
	t.Helper()
	args := []string{"batch", "run", s.cfg.App, "-n", ns, "-o", "json"}
	for k, v := range values {
		args = append(args, "-p", k+"="+v)
	}
	if wait {
		args = append(args, "--wait", "--timeout", s.cfg.Run.String())
	}
	r, err := Adhar(s.ctx, s.cfg.Run+2*time.Minute, args...)
	require.NoError(t, err)
	o, err := ParseRunOutcome(r.Stdout)
	require.NoError(t, err, "%s", r)
	return o, r
}

func (s *suite) manualRun(t *testing.T) {
	values := map[string]string{"businessDate": s.cfg.Date(0)}
	identity := s.tpl.Identity(values)
	want, err := RunName(s.cfg.App, identity)
	require.NoError(t, err)

	o, r := s.runCLI(t, devNS, values, true)
	assert.Equal(t, want, o.Run, "the run's name is D1's hash of %v", identity)
	require.Equal(t, "Succeeded", o.Status, "%s%s", r, s.c.DescribeRun(s.ctx, devNS, o.Run))
	assert.Zero(t, r.ExitCode, "the CLI exits with the run's code")
	sum := o.ParsedSummary()
	require.NotNil(t, sum, "the termination summary (§5.13)")
	assert.Equal(t, "completed", sum.Outcome)
	assert.Equal(t, values["businessDate"], sum.BusinessDate)
	if assert.NotNil(t, sum.Read) && assert.NotNil(t, sum.Written) {
		assert.Positive(t, *sum.Read)
		assert.Equal(t, *sum.Read, *sum.Written, "the sample job writes every item it reads")
	}
	assert.Equal(t, o.ExecutionID, sum.ExecutionID)

	l, err := s.c.Ledger(s.ctx, devNS, s.cfg.App, identity)
	require.NoError(t, err)
	assert.Len(t, l.Executions, 1, "one execution for one manual run: %+v", l)

	// D2: the same identity again is the completed run, not a second one.
	runs := s.runCount(t, devNS)
	again, r2 := s.runCLI(t, devNS, values, true)
	assert.Equal(t, want, again.Run)
	assert.Equal(t, "Succeeded", again.Status, "%s", r2)
	assert.Equal(t, runs, s.runCount(t, devNS), "running a completed identity again created a Job")
}

func (s *suite) runCount(t *testing.T, ns string) int {
	t.Helper()
	jobs, err := s.c.CS.BatchV1().Jobs(ns).List(s.ctx, metav1.ListOptions{LabelSelector: labelBatchJob + "=" + s.cfg.App})
	require.NoError(t, err)
	return len(jobs.Items)
}

/* ─────────────── invalid parameters (A1.9, NEW-2) ─────────────── */

func (s *suite) invalidParams(t *testing.T) {
	// The CLI refuses first, so no Job exists — which is why the pod's own
	// refusal has to be reached with a run built from the template.
	before := s.runCount(t, devNS)
	r, err := Adhar(s.ctx, 2*time.Minute, "batch", "run", s.cfg.App, "-n", devNS, "-p", "businessDate=31-12-2031")
	require.NoError(t, err)
	assert.NotZero(t, r.ExitCode, "the CLI accepted businessDate=31-12-2031: %s", r)
	assert.Equal(t, before, s.runCount(t, devNS), "a refused parameter still created a Job")

	for i, tc := range []struct{ what, raw string }{
		{"a businessDate that is not a date", `{"businessDate":"31-12-2031"}`},
		{"a source outside its allowed values", `{"businessDate":"` + s.cfg.Date(1) + `","source":"ftp"}`},
		{"parameters that are not JSON", `businessDate=2031-12-31`},
	} {
		t.Run(tc.what, func(t *testing.T) {
			name := fmt.Sprintf("%s-invalid-%s-%d", s.cfg.App, s.cfg.Tag, i)
			require.NoError(t, s.c.CreateRun(s.ctx, s.tpl.BuildRun(name, devNS, tc.raw)),
				"adhar-batch-run-integrity refused a run that changes only ADHAR_BATCH_PARAMS (§6.5)")
			ctx, cancel := s.budget(s.cfg.Run)
			defer cancel()
			job, err := s.c.WaitRunFinished(ctx, devNS, name)
			require.NoError(t, err, s.c.DescribeRun(s.ctx, devNS, name))
			cond, reason := Finished(job)
			assert.Equal(t, "Failed", cond)
			assert.Equal(t, "PodFailurePolicy", reason, "exit 2 is FailJob in the run's podFailurePolicy (§5.7)")
			assert.EqualValues(t, 1, job.Status.Failed, "zero retries")
			pods, err := s.c.RunPods(s.ctx, devNS, name)
			require.NoError(t, err)
			require.Len(t, pods, 1, "one pod, no replacement")
			term, ok := Terminated(pods[0])
			require.True(t, ok)
			assert.EqualValues(t, 2, term.ExitCode, "invalid request (§5.7)%s", s.c.DescribeRun(s.ctx, devNS, name))
			// What the Console shows (D13): an invalid outcome with its reason.
			assert.NoError(t, ParseSummary(term.Message).CheckInvalid("parameters"), "termination message %q", term.Message)
		})
	}
}

/* ─────────────── double fire (A1.2) ─────────────── */

func (s *suite) doubleFire(t *testing.T) {
	values := map[string]string{"businessDate": s.cfg.Date(2)}
	identity := s.tpl.Identity(values)
	names := []string{s.cfg.App + "-fire-" + s.cfg.Tag + "-a", s.cfg.App + "-fire-" + s.cfg.Tag + "-b"}
	// Created back to back under different names, so neither the CLI's naming
	// nor the API server's AlreadyExists stands between them: only the
	// repository's instance identity does.
	for _, n := range names {
		require.NoError(t, s.c.CreateRun(s.ctx, s.tpl.BuildRun(n, devNS, ParamsJSON(values))))
	}
	completed := 0
	for _, n := range names {
		ctx, cancel := s.budget(s.cfg.Run)
		job, err := s.c.WaitRunFinished(ctx, devNS, n)
		cancel()
		require.NoError(t, err, s.c.DescribeRun(s.ctx, devNS, n))
		cond, _ := Finished(job)
		assert.Equal(t, "Complete", cond, "both runs exit 0%s", s.c.DescribeRun(s.ctx, devNS, n))
		sum, code, err := s.c.LastSummary(s.ctx, devNS, n)
		require.NoError(t, err)
		assert.Zero(t, code)
		if sum != nil && sum.Outcome == "completed" {
			completed++
			continue
		}
		assert.NoError(t, sum.CheckSkipped("duplicate", "already-complete"), "run %s", n)
	}
	assert.Equal(t, 1, completed, "exactly one of the two runs executed")
	l, err := s.c.Ledger(s.ctx, devNS, s.cfg.App, identity)
	require.NoError(t, err)
	assert.Len(t, l.Executions, 1, "one execution for one identity: %+v", l)

	// A person running the same date afterwards: a new run name, nothing executed.
	o, r := s.runCLI(t, devNS, values, true)
	assert.Equal(t, "Succeeded", o.Status, "%s", r)
	assert.NoError(t, o.ParsedSummary().CheckSkipped("already-complete"))
	l, _ = s.c.Ledger(s.ctx, devNS, s.cfg.App, identity)
	assert.Len(t, l.Executions, 1, "the manual duplicate executed: %+v", l)
}

/* ─────────────── interrupted runs (A1.1, A1.3, restart) ─────────────── */

func (s *suite) seed(t *testing.T) {
	_, err := s.c.Psql(s.ctx, devNS, s.cfg.App+"-db", SeedSQL(databaseOwner, s.cfg.Items))
	require.NoError(t, err)
	out, err := s.c.Psql(s.ctx, devNS, s.cfg.App+"-db", "SELECT count(*) FROM "+inputTable)
	require.NoError(t, err)
	require.Equal(t, strconv.Itoa(s.cfg.Items), strings.TrimSpace(out))
}

func dbValues(date string) map[string]string {
	return map[string]string{"businessDate": date, "source": "database"}
}

// startAndCatch starts a database run and returns it once an execution is
// STARTED and has committed at least one chunk — mid-run, after a commit.
func (s *suite) startAndCatch(t *testing.T, values map[string]string) (string, Execution) {
	t.Helper()
	identity := s.tpl.Identity(values)
	name, err := RunName(s.cfg.App, identity)
	require.NoError(t, err)
	o, r := s.runCLI(t, devNS, values, false)
	require.Zero(t, r.ExitCode, "%s", r)
	require.Equal(t, name, o.Run)
	var caught Execution
	ctx, cancel := s.budget(s.cfg.Run)
	defer cancel()
	require.NoError(t, Eventually(ctx, time.Second, "a chunk of "+name+" to commit", func(ctx context.Context) (bool, string, error) {
		l, err := s.c.Ledger(ctx, devNS, s.cfg.App, identity)
		if err != nil {
			return false, err.Error(), nil
		}
		if len(l.Completed()) > 0 {
			return false, "", fmt.Errorf("the run completed before it could be interrupted; raise ADHAR_E2E_BATCH_ITEMS (now %d)", s.cfg.Items)
		}
		last, ok := l.Last()
		if ok && last.Status == "STARTED" && l.Writes(last.ID) > 0 {
			caught = last
			return true, "", nil
		}
		return false, fmt.Sprintf("%+v, %d committed", last, l.Writes()), nil
	}), s.c.DescribeRun(s.ctx, devNS, name))
	return name, caught
}

func (s *suite) runningPod(t *testing.T, name string) corev1.Pod {
	t.Helper()
	pods, err := s.c.RunPods(s.ctx, devNS, name)
	require.NoError(t, err)
	for _, p := range pods {
		if p.Status.Phase == corev1.PodRunning && p.DeletionTimestamp == nil {
			return p
		}
	}
	t.Fatalf("no running pod of %s%s", name, s.c.DescribeRun(s.ctx, devNS, name))
	return corev1.Pod{}
}

// interrupted is A1.1: the pod is taken away mid-run, and the run still ends
// in ONE completed execution of the same JobInstance that wrote every item once.
// A deleted or evicted pod gets SIGTERM and stops its execution (F2); a
// SIGKILLed one, like an OOM-killed one, leaves it STARTED and the replacement
// recovers it (F1). The Job counts each as the run's podFailurePolicy says.
func (s *suite) interrupted(t *testing.T, day int, how Interruption) {
	want, err := s.tpl.Expect(how)
	require.NoError(t, err)
	values := dbValues(s.cfg.Date(day))
	name, caught := s.startAndCatch(t, values)
	pod := s.runningPod(t, name)
	switch how {
	case Delete:
		require.NoError(t, s.c.CS.CoreV1().Pods(devNS).Delete(s.ctx, pod.Name, metav1.DeleteOptions{}))
	case Evict:
		require.NoError(t, s.c.Evict(s.ctx, devNS, pod.Name))
	case Kill:
		require.NoError(t, s.c.KillBatchContainer(s.ctx, pod))
	}
	t.Logf("%s %s after execution %d committed", how, pod.Name, caught.ID)

	ctx, cancel := s.budget(s.cfg.Run)
	defer cancel()
	job, err := s.c.WaitRunFinished(ctx, devNS, name)
	require.NoError(t, err, s.c.DescribeRun(s.ctx, devNS, name))
	cond, _ := Finished(job)
	require.Equal(t, "Complete", cond, s.c.DescribeRun(s.ctx, devNS, name))
	assert.Equal(t, want.Failed, job.Status.Failed, "pod failures the Job counted after a %s (§5.7)", how)

	l, err := s.c.Ledger(s.ctx, devNS, s.cfg.App, s.tpl.Identity(values))
	require.NoError(t, err)
	require.NoError(t, l.CheckExactlyOnce(int64(s.cfg.Items)), "%+v", l)
	assert.GreaterOrEqual(t, len(l.Executions), 2, "the replacement pod started a new execution of the instance")
	assert.Positive(t, l.Writes(caught.ID), "the interrupted execution's commits are kept, not redone")
	assert.Equal(t, want.Interrupted, l.StatusOf(caught.ID), "execution %d after a %s (§5.5, §5.9)", caught.ID, how)
	if how == Kill {
		// The Job keeps a failed pod, so the kill can be read back: 137, and no
		// summary, since nothing ran after the signal (§5.14).
		killed, err := s.c.CS.CoreV1().Pods(devNS).Get(s.ctx, pod.Name, metav1.GetOptions{})
		require.NoError(t, err)
		term, ok := Terminated(*killed)
		require.True(t, ok, "the killed container's end")
		assert.Equal(t, want.ExitCode, term.ExitCode)
		assert.Nil(t, ParseSummary(term.Message), "a SIGKILLed run wrote a summary %q", term.Message)
	}
	sum, _, err := s.c.LastSummary(s.ctx, devNS, name)
	require.NoError(t, err)
	if assert.NotNil(t, sum) {
		assert.Equal(t, "completed", sum.Outcome)
	}
}

func (s *suite) stopResume(t *testing.T) {
	values := dbValues(s.cfg.Date(5))
	name, caught := s.startAndCatch(t, values)
	grace, _, _ := unstructured.NestedInt64(s.tpl.Manifest.Object, "spec", "template", "spec", "terminationGracePeriodSeconds")
	r, err := Adhar(s.ctx, 2*time.Minute, "batch", "stop", name, "-n", devNS)
	require.NoError(t, err)
	require.Zero(t, r.ExitCode, "%s", r)

	// §5.9: SIGTERM becomes jobOperator.stop, so the execution ends STOPPED
	// within one chunk plus the grace period — not STARTED behind a dead pod.
	ctx, cancel := s.budget(time.Duration(grace)*time.Second + time.Minute)
	defer cancel()
	identity := s.tpl.Identity(values)
	require.NoError(t, Eventually(ctx, 2*time.Second, "execution "+strconv.FormatInt(caught.ID, 10)+" to be STOPPED", func(ctx context.Context) (bool, string, error) {
		l, err := s.c.Ledger(ctx, devNS, s.cfg.App, identity)
		if err != nil {
			return false, err.Error(), nil
		}
		last, _ := l.Last()
		pods, _ := s.c.RunPods(ctx, devNS, name)
		alive := 0
		for _, p := range pods {
			if _, done := Terminated(p); !done {
				alive++
			}
		}
		return last.Status == "STOPPED" && alive == 0, fmt.Sprintf("execution %d %s, %d pods still alive", last.ID, last.Status, alive), nil
	}), s.c.DescribeRun(s.ctx, devNS, name))

	// D2: a stopped run is Stopped with exit 4 until someone resumes it.
	o, r2 := s.runCLI(t, devNS, values, true)
	assert.Equal(t, "Stopped", o.Status, "%s", r2)
	assert.Equal(t, 4, r2.ExitCode)

	r, err = Adhar(s.ctx, 2*time.Minute, "batch", "resume", name, "-n", devNS)
	require.NoError(t, err)
	require.Zero(t, r.ExitCode, "%s", r)
	ctx2, cancel2 := s.budget(s.cfg.Run)
	defer cancel2()
	job, err := s.c.WaitRunFinished(ctx2, devNS, name)
	require.NoError(t, err, s.c.DescribeRun(s.ctx, devNS, name))
	cond, _ := Finished(job)
	require.Equal(t, "Complete", cond, s.c.DescribeRun(s.ctx, devNS, name))
	l, err := s.c.Ledger(s.ctx, devNS, s.cfg.App, identity)
	require.NoError(t, err)
	require.NoError(t, l.CheckExactlyOnce(int64(s.cfg.Items)), "%+v", l)
	stopped := false
	for _, e := range l.Executions {
		stopped = stopped || (e.ID == caught.ID && e.Status == "STOPPED")
	}
	assert.True(t, stopped, "the stopped execution stays STOPPED in the history: %+v", l.Executions)
	sum, _, _ := s.c.LastSummary(s.ctx, devNS, name)
	if assert.NotNil(t, sum) {
		assert.Equal(t, "completed", sum.Outcome)
	}
}

// restart: a run that fails on a bad row exhausts its retries; once the row is
// fixed `adhar batch restart` creates <run>-r1, which completes the SAME
// instance from where the failures left it.
func (s *suite) restart(t *testing.T) {
	values := dbValues(s.cfg.Date(6))
	identity := s.tpl.Identity(values)
	poisoned := s.cfg.Items / 2
	db := s.cfg.App + "-db"
	_, err := s.c.Psql(s.ctx, devNS, db, PoisonSQL(databaseOwner, poisoned))
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = s.c.Psql(context.Background(), devNS, db, HealSQL(databaseOwner, poisoned)) })

	o, r := s.runCLI(t, devNS, values, true)
	require.Equal(t, "Failed", o.Status, "%s%s", r, s.c.DescribeRun(s.ctx, devNS, o.Run))
	assert.Equal(t, 5, o.ExitCode, "an item failing after its retries is FAILED, retryable (§5.7)")
	job, err := s.c.CS.BatchV1().Jobs(devNS).Get(s.ctx, o.Run, metav1.GetOptions{})
	require.NoError(t, err)
	_, reason := Finished(job)
	assert.Equal(t, "BackoffLimitExceeded", reason)
	if job.Spec.BackoffLimit != nil {
		assert.Equal(t, *job.Spec.BackoffLimit+1, job.Status.Failed, "exit 5 is counted against backoffLimit")
	}

	_, err = s.c.Psql(s.ctx, devNS, db, HealSQL(databaseOwner, poisoned))
	require.NoError(t, err)
	r, err = Adhar(s.ctx, 2*time.Minute, "batch", "restart", o.Run, "-n", devNS)
	require.NoError(t, err)
	require.Zero(t, r.ExitCode, "%s", r)
	r1, err := RestartName(o.Run, 1)
	require.NoError(t, err)
	ctx, cancel := s.budget(s.cfg.Run)
	defer cancel()
	finished, err := s.c.WaitRunFinished(ctx, devNS, r1)
	require.NoError(t, err, s.c.DescribeRun(s.ctx, devNS, r1))
	cond, _ := Finished(finished)
	require.Equal(t, "Complete", cond, s.c.DescribeRun(s.ctx, devNS, r1))

	l, err := s.c.Ledger(s.ctx, devNS, s.cfg.App, identity)
	require.NoError(t, err)
	require.NoError(t, l.CheckExactlyOnce(int64(s.cfg.Items)), "%+v", l)
	assert.Greater(t, len(l.Executions), 1, "the restart continued the failed instance")
	sum, _, _ := s.c.LastSummary(s.ctx, devNS, r1)
	if assert.NotNil(t, sum) && assert.NotNil(t, sum.InstanceID) {
		assert.Equal(t, l.Instances[0], *sum.InstanceID)
	}
}

/* ─────────────── the clock (A1.7 scheduled, A1.2 double fire) ─────────────── */

// scheduled watches the CronJob in test, where the scaffold's schedule is
// live. Every firing in one day addresses that day's JobInstance, so the second
// firing of a day is the scheduler's own double fire.
func (s *suite) scheduled(t *testing.T) {
	app := s.cfg.App
	tpl, err := s.c.RunTemplate(s.ctx, testNS, app)
	require.NoError(t, err)
	zone := time.UTC
	if tz := envValueOf(tpl, "ADHAR_BATCH_TIME_ZONE"); tz != "" {
		zone, err = time.LoadLocation(tz)
		require.NoError(t, err)
	}
	var date string
	var group []batchv1.Job
	ctx, cancel := s.budget(s.cfg.Scheduled)
	defer cancel()
	require.NoError(t, Eventually(ctx, 15*time.Second, "two scheduled runs of one business date to finish", func(ctx context.Context) (bool, string, error) {
		jobs, err := s.c.CS.BatchV1().Jobs(testNS).List(ctx, metav1.ListOptions{LabelSelector: labelBatchJob + "=" + app})
		if err != nil {
			return false, err.Error(), nil
		}
		byDate := map[string][]batchv1.Job{}
		for _, j := range jobs.Items {
			cond, _ := Finished(&j)
			if !ownedByCronJob(j) || cond == "" {
				continue
			}
			if d, ok := ScheduledBusinessDate(app, j.Name, j.Annotations, zone); ok {
				byDate[d] = append(byDate[d], j)
			}
		}
		for d, js := range byDate {
			if len(js) >= 2 {
				date, group = d, js
				return true, "", nil
			}
		}
		return false, fmt.Sprintf("%d runs, finished scheduled runs by date: %v", len(jobs.Items), countByDate(byDate)), nil
	}))

	completed := 0
	for _, j := range group {
		cond, _ := Finished(&j)
		assert.Equal(t, "Complete", cond, "scheduled run %s%s", j.Name, s.c.DescribeRun(s.ctx, testNS, j.Name))
		assert.Equal(t, "schedule", j.Labels[labelTrigger], "scheduled run %s trigger label", j.Name)
		sum, _, err := s.c.LastSummary(s.ctx, testNS, j.Name)
		require.NoError(t, err)
		if sum != nil && sum.Outcome == "completed" {
			completed++
			assert.Equal(t, date, sum.BusinessDate, "the business date is the scheduled time's day, never now()")
			assert.Equal(t, "schedule", sum.Trigger)
			continue
		}
		assert.NoError(t, sum.CheckSkipped("already-complete", "duplicate"), "scheduled run %s", j.Name)
	}
	assert.LessOrEqual(t, completed, 1, "two scheduled runs of %s both executed", date)

	identity := tpl.Identity(map[string]string{"businessDate": date})
	l, err := s.c.Ledger(s.ctx, testNS, app, identity)
	require.NoError(t, err)
	require.Len(t, l.Executions, 1, "one execution for business date %s: %+v", date, l)
	assert.Equal(t, "COMPLETED", l.Executions[0].Status)

	// A person re-running the night by hand: exit 0, skipped, still one execution.
	o, r := s.runCLI(t, testNS, map[string]string{"businessDate": date}, true)
	want, _ := RunName(app, identity)
	assert.Equal(t, want, o.Run)
	assert.Equal(t, "Succeeded", o.Status, "%s", r)
	assert.Zero(t, r.ExitCode)
	assert.NoError(t, o.ParsedSummary().CheckSkipped("already-complete"))
	l, _ = s.c.Ledger(s.ctx, testNS, app, identity)
	assert.Len(t, l.Executions, 1, "the manual re-run of %s executed: %+v", date, l)
}

func ownedByCronJob(j batchv1.Job) bool {
	for _, o := range j.OwnerReferences {
		if o.Kind == "CronJob" {
			return true
		}
	}
	return false
}

func countByDate(m map[string][]batchv1.Job) map[string]int {
	out := map[string]int{}
	for d, js := range m {
		out[d] = len(js)
	}
	return out
}

func envValueOf(t *RunTemplate, name string) string {
	containers, _, _ := unstructured.NestedSlice(t.Manifest.Object, "spec", "template", "spec", "containers")
	for _, c := range containers {
		m, _ := c.(map[string]interface{})
		env, _ := m["env"].([]interface{})
		for _, e := range env {
			if em, ok := e.(map[string]interface{}); ok && em["name"] == name {
				v, _ := em["value"].(string)
				return v
			}
		}
	}
	return ""
}
