//go:build e2e

// Test_BatchWorkflowStep is A3.8 (design 0025 §12.1, PLAT-3.10): with
// argo-workflows enabled, the catalogue step adhar-batch-steps/run starts a run
// of the Phase-1 job, waits for it and returns its summary; a second workflow
// with the same parameters re-executes nothing; and a run that fails fails its
// step with the run's own exit code in the outputs.
//
// Opt-in with the rest of the package (ADHAR_E2E_BATCH=1), and it SKIPS, with
// the reason, on a platform that does not serve the step: the local profile
// leaves argo-workflows off, and the step runs as the environment's
// adhar-workflow ServiceAccount, which the environment composition provides
// once design 0026 W1 lands. It reuses the app Test_BatchOnKind scaffolds.
//
// The failing case poisons one input row, which the sample processor refuses
// with a RETRYABLE exception: the run ends exit 5 after its backoff. §12.1
// states the case for exit 3 (failed, not retryable); the sample job has no
// non-retryable path, so the assertion is the contract itself — the step's
// exitCode output IS the run's exit code, whatever that code is.
package batch

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	workflowGVR    = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "workflows"}
	clusterWfTmGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "clusterworkflowtemplates"}
)

const (
	batchStepsTemplate     = "adhar-batch-steps"
	workflowServiceAccount = "adhar-workflow"
	// annotationBatchWorkflow is what the step stamps on the run it starts (§12.1).
	annotationBatchWorkflow = "adhar.io/batch-workflow"
)

type wfSuite struct {
	cfg Config
	c   *Cluster
	ctx context.Context
	tpl *RunTemplate
}

func Test_BatchWorkflowStep(t *testing.T) {
	cfg, _ := settings(t)
	if !cfg.Enabled {
		t.Skip("the batch suite is opt-in: ADHAR_E2E_BATCH=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Overall)
	defer cancel()
	e := platform(t)
	if _, err := e.c.DC.Resource(clusterWfTmGVR).Get(ctx, batchStepsTemplate, metav1.GetOptions{}); err != nil {
		t.Skipf("ClusterWorkflowTemplate %s is not served (argo-workflows is off in this profile?): %v", batchStepsTemplate, err)
	}
	if _, err := e.c.CS.CoreV1().ServiceAccounts(devNS).Get(ctx, workflowServiceAccount, metav1.GetOptions{}); err != nil {
		t.Skipf("ServiceAccount %s/%s, the workflow executor identity (design 0026 §5.6), does not exist: %v", devNS, workflowServiceAccount, err)
	}
	tpl, err := ensureApp(t, e, phaseOneApp(cfg))
	require.NoError(t, err)
	s := &wfSuite{cfg: cfg, c: e.c, ctx: ctx, tpl: tpl}

	var first stepResult
	if t.Run("a two-step workflow runs the job, then checks over HTTP (A3.8)", func(t *testing.T) { first = s.twoStep(t) }) {
		t.Run("the same workflow again does not re-execute the completed run (A3.8)", func(t *testing.T) { s.again(t, first) })
	}
	t.Run("a failing run fails its step, with the run's exit code in the outputs (§12.1)", s.failing)
}

// stepResult is what one workflow's batch step reported.
type stepResult struct {
	Workflow string
	Phase    string
	Values   map[string]string
	Batch    workflowNode
	Check    *workflowNode
}

type workflowNode struct {
	Phase    string
	ExitCode string
	Outputs  map[string]string
	Message  string
}

// batchStepWorkflow is the two-step workflow of §12.1's test row: the batch run,
// then an HTTP check that runs only when the step reported Succeeded, so the
// outputs are proven to flow to the next step and not merely to exist.
func batchStepWorkflow(app, ns, tag string, values map[string]string) *unstructured.Unstructured {
	params, _ := json.Marshal(values)
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Workflow",
		"metadata": map[string]interface{}{
			"generateName": app + "-wf-",
			"namespace":    ns,
			"labels":       map[string]interface{}{"adhar.io/e2e": "batch-workflow-step", "adhar.io/e2e-tag": tag},
		},
		"spec": map[string]interface{}{
			"entrypoint":         "main",
			"serviceAccountName": workflowServiceAccount,
			"templates": []interface{}{
				map[string]interface{}{
					"name": "main",
					"steps": []interface{}{
						[]interface{}{map[string]interface{}{
							"name":        "batch",
							"templateRef": map[string]interface{}{"name": batchStepsTemplate, "template": "run", "clusterScope": true},
							"arguments": map[string]interface{}{"parameters": []interface{}{
								map[string]interface{}{"name": "batchJob", "value": app},
								map[string]interface{}{"name": "parameters", "value": string(params)},
							}},
						}},
						[]interface{}{map[string]interface{}{
							"name":     "check",
							"template": "check",
							"when":     "{{steps.batch.outputs.parameters.status}} == Succeeded",
							"arguments": map[string]interface{}{"parameters": []interface{}{
								map[string]interface{}{"name": "run", "value": "{{steps.batch.outputs.parameters.run}}"},
							}},
						}},
					},
				},
				map[string]interface{}{
					"name":   "check",
					"inputs": map[string]interface{}{"parameters": []interface{}{map[string]interface{}{"name": "run"}}},
					"http": map[string]interface{}{
						"url":                "https://kubernetes.default.svc/readyz",
						"insecureSkipVerify": true,
						"successCondition":   "response.statusCode == 200",
					},
				},
			},
		},
	}}
}

// runWorkflow submits the workflow and waits for it to finish.
func (s *wfSuite) runWorkflow(t *testing.T, values map[string]string) stepResult {
	t.Helper()
	created, err := s.c.DC.Resource(workflowGVR).Namespace(devNS).Create(s.ctx, batchStepWorkflow(s.cfg.App, devNS, s.cfg.Tag, values), metav1.CreateOptions{})
	require.NoError(t, err, "submitting the workflow")
	name := created.GetName()
	ctx, cancel := context.WithTimeout(s.ctx, s.cfg.Run+10*time.Minute)
	defer cancel()
	var wf *unstructured.Unstructured
	require.NoError(t, Eventually(ctx, 10*time.Second, "workflow "+name+" to finish", func(ctx context.Context) (bool, string, error) {
		got, err := s.c.DC.Resource(workflowGVR).Namespace(devNS).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error(), nil
		}
		phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
		wf = got
		switch phase {
		case "Succeeded", "Failed", "Error":
			return true, phase, nil
		}
		return false, "phase " + phase, nil
	}))
	phase, _, _ := unstructured.NestedString(wf.Object, "status", "phase")
	res := stepResult{Workflow: name, Phase: phase, Values: values}
	batch, ok := nodeNamed(wf, "batch")
	require.True(t, ok, "workflow %s has no node for the batch step: %v", name, wf.Object["status"])
	res.Batch = batch
	if check, ok := nodeNamed(wf, "check"); ok {
		res.Check = &check
	}
	return res
}

// nodeNamed reads the step node by its display name. A retried step is a Retry
// node whose children are batch(0), batch(1)…; Argo copies the last child's
// outputs to it, and the newest child stands in when it has not.
func nodeNamed(wf *unstructured.Unstructured, display string) (workflowNode, bool) {
	nodes, _, _ := unstructured.NestedMap(wf.Object, "status", "nodes")
	var parent map[string]interface{}
	var children []map[string]interface{}
	for _, raw := range nodes {
		n, _ := raw.(map[string]interface{})
		switch d, _ := n["displayName"].(string); {
		case d == display:
			parent = n
		case strings.HasPrefix(d, display+"("):
			children = append(children, n)
		}
	}
	if parent == nil {
		return workflowNode{}, false
	}
	sort.Slice(children, func(i, j int) bool {
		a, _ := children[i]["startedAt"].(string)
		b, _ := children[j]["startedAt"].(string)
		return a < b
	})
	read := func(n map[string]interface{}) workflowNode {
		out := workflowNode{Outputs: map[string]string{}}
		out.Phase, _ = n["phase"].(string)
		out.Message, _ = n["message"].(string)
		if v, ok, _ := unstructured.NestedFieldNoCopy(n, "outputs", "exitCode"); ok {
			out.ExitCode = fmt.Sprint(v)
		}
		params, _, _ := unstructured.NestedSlice(n, "outputs", "parameters")
		for _, p := range params {
			m, _ := p.(map[string]interface{})
			name, _ := m["name"].(string)
			value, _ := m["value"].(string)
			out.Outputs[name] = value
		}
		return out
	}
	node := read(parent)
	if len(node.Outputs) == 0 && len(children) > 0 {
		last := read(children[len(children)-1])
		last.Phase = node.Phase
		node = last
	}
	return node, true
}

func (s *wfSuite) twoStep(t *testing.T) stepResult {
	values := map[string]string{"businessDate": s.cfg.Date(60)}
	identity := s.tpl.Identity(values)
	want, err := RunName(s.cfg.App, identity)
	require.NoError(t, err)

	r := s.runWorkflow(t, values)
	require.Equal(t, "Succeeded", r.Phase, "workflow %s: batch step %+v%s", r.Workflow, r.Batch, s.c.DescribeRun(s.ctx, devNS, want))
	out := r.Batch.Outputs
	assert.Equal(t, want, out["run"], "the step names the run by D1, as `adhar batch run` does")
	assert.Equal(t, "Succeeded", out["status"])
	assert.Equal(t, "0", out["exitCode"])
	assert.NotEmpty(t, out["executionId"], "the step returns the execution id")
	sum := ParseSummary(out["summary"])
	if assert.NotNil(t, sum, "the step returns the termination summary (§5.13): %q", out["summary"]) {
		assert.Equal(t, "completed", sum.Outcome)
		assert.Equal(t, values["businessDate"], sum.BusinessDate)
		assert.Equal(t, out["executionId"], fmtID(sum.ExecutionID))
	}
	if assert.NotNil(t, r.Check, "the HTTP check did not run after a Succeeded batch step") {
		assert.Equal(t, "Succeeded", r.Check.Phase, "the HTTP check: %s", r.Check.Message)
	}

	// §12.1: the run carries the workflow trigger and names the workflow.
	job, err := s.c.CS.BatchV1().Jobs(devNS).Get(s.ctx, want, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "workflow", job.Labels[labelTrigger], "the run's trigger label")
	assert.Equal(t, r.Workflow, job.Annotations[annotationBatchWorkflow], "the run names the workflow that started it")

	l, err := s.c.Ledger(s.ctx, devNS, s.cfg.App, identity)
	require.NoError(t, err)
	assert.Len(t, l.Executions, 1, "one execution for one workflow run: %+v", l)
	return r
}

func (s *wfSuite) again(t *testing.T, first stepResult) {
	require.NotEmpty(t, first.Batch.Outputs["run"], "the first workflow reported no run")
	identity := s.tpl.Identity(first.Values)
	jobs := s.jobCount(t)

	r := s.runWorkflow(t, first.Values)
	require.Equal(t, "Succeeded", r.Phase, "workflow %s: batch step %+v", r.Workflow, r.Batch)
	assert.Equal(t, first.Batch.Outputs["run"], r.Batch.Outputs["run"], "the same parameters address the same run")
	assert.Equal(t, "Succeeded", r.Batch.Outputs["status"])
	assert.Equal(t, first.Batch.Outputs["executionId"], r.Batch.Outputs["executionId"], "the completed run's execution, not a new one")
	assert.Equal(t, jobs, s.jobCount(t), "re-running the workflow created a Job")
	l, err := s.c.Ledger(s.ctx, devNS, s.cfg.App, identity)
	require.NoError(t, err)
	assert.Len(t, l.Executions, 1, "re-running the workflow executed the job again: %+v", l)
}

func (s *wfSuite) failing(t *testing.T) {
	db := s.cfg.App + "-db"
	_, err := s.c.Psql(s.ctx, devNS, db, SeedSQL(databaseOwner, s.cfg.Items))
	require.NoError(t, err)
	poisoned := s.cfg.Items / 2
	_, err = s.c.Psql(s.ctx, devNS, db, PoisonSQL(databaseOwner, poisoned))
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = s.c.Psql(context.Background(), devNS, db, HealSQL(databaseOwner, poisoned)) })

	values := dbValues(s.cfg.Date(61))
	want, err := RunName(s.cfg.App, s.tpl.Identity(values))
	require.NoError(t, err)

	r := s.runWorkflow(t, values)
	assert.Equal(t, "Failed", r.Phase, "a failed run must fail the workflow")
	assert.Equal(t, "Failed", r.Batch.Phase, "the batch step: %s", r.Batch.Message)
	out := r.Batch.Outputs
	assert.Equal(t, want, out["run"])
	assert.Equal(t, "Failed", out["status"])

	_, code, err := s.c.LastSummary(s.ctx, devNS, want)
	require.NoError(t, err, s.c.DescribeRun(s.ctx, devNS, want))
	require.NotZero(t, code, "the poisoned run exited 0")
	assert.Equal(t, strconv.Itoa(int(code)), out["exitCode"], "the step's exitCode output is the run's exit code")
	assert.Equal(t, strconv.Itoa(int(code)), r.Batch.ExitCode, "the step's container exits with the run's code")
	assert.NotEqual(t, "{}", out["summary"], "a failed run still returns its summary")
	if r.Check != nil {
		assert.Contains(t, []string{"Skipped", "Omitted"}, r.Check.Phase, "the HTTP check ran after a failed batch step")
	}

	// A step retry is OnError only: a failure is the run's answer, so the run
	// was never restarted behind the workflow's back.
	r1, err := RestartName(want, 1)
	require.NoError(t, err)
	_, err = s.c.CS.BatchV1().Jobs(devNS).Get(s.ctx, r1, metav1.GetOptions{})
	assert.Error(t, err, "the failed step restarted its run as %s", r1)
}

func (s *wfSuite) jobCount(t *testing.T) int {
	t.Helper()
	jobs, err := s.c.CS.BatchV1().Jobs(devNS).List(s.ctx, metav1.ListOptions{LabelSelector: labelBatchJob + "=" + s.cfg.App})
	require.NoError(t, err)
	return len(jobs.Items)
}

func fmtID(id *int64) string {
	if id == nil {
		return ""
	}
	return strconv.FormatInt(*id, 10)
}
