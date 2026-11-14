package batch

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"
)

// reportAt is the clock of the schedule tests: two minutes after the example
// report of §5.20.8 was written.
var reportAt = time.Date(2026, 11, 14, 9, 2, 5, 0, time.UTC)

// exampleReport is the report of design 0025 §5.20.8, verbatim.
const exampleReport = `{"v":1,"engine":"quartz","scheduler":"importer","updated":"2026-11-14T09:00:05Z","truncated":false,
 "instances":[{"id":"importer-6c9f7d-x2k4p1731574800123","checkin":"2026-11-14T09:00:01Z"}],
 "jobs":[{"job":"importJob","name":"import-job",
   "triggers":[{"key":"adhar-batch-config.import-job.nightly","kind":"cron","expression":"0 30 1 ? * *",
                "timeZone":"Asia/Singapore","calendar":"sg-business","misfire":"latest",
                "state":"NORMAL","previous":"2026-11-13T17:30:00Z","next":"2026-11-16T17:30:00Z"}],
   "lastRun":{"run":"import-job-09932cb3","executionId":97,"status":"COMPLETED","outcome":"completed",
              "trigger":"adhar-batch-config.import-job.nightly","start":"2026-11-13T17:30:00Z","end":"2026-11-13T17:41:12Z"}}]}`

// reportConfigMap is a report as the kit writes it: labelled, in the service's
// namespace, the document under schedules.json. A nil labels map gives the
// kit's labels; data nil gives no data at all.
func reportConfigMap(ns, name string, labels map[string]interface{}, data map[string]interface{}) *unstructured.Unstructured {
	if labels == nil {
		labels = map[string]interface{}{
			labelSchedules:                 "true",
			"adhar.io/application":         "importer",
			"app.kubernetes.io/managed-by": "adhar-kit",
		}
	}
	o := map[string]interface{}{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]interface{}{"name": name, "namespace": ns, "labels": labels},
	}
	if data != nil {
		o["data"] = data
	}
	return &unstructured.Unstructured{Object: o}
}

func report(ns, name, doc string) *unstructured.Unstructured {
	return reportConfigMap(ns, name, nil, map[string]interface{}{schedulesKey: doc})
}

func readReports(t *testing.T, ns string, objs ...runtime.Object) []scheduleReport {
	t.Helper()
	reports, err := readScheduleReports(context.Background(), fakeCluster(objs...), ns)
	if err != nil {
		t.Fatal(err)
	}
	return reports
}

// None: a namespace with no report says so, names where it looked and points
// at `list` for BatchJobs, and -o json is an empty array rather than null. A
// ConfigMap that is not labelled as a report is not one, even by name, and a
// report in another namespace is not this namespace's.
func TestSchedulesWithNoReportSaySoAndReadOnlyLabelledConfigMaps(t *testing.T) {
	reports := readReports(t, "prod",
		reportConfigMap("prod", "other-batch-schedules", map[string]interface{}{"adhar.io/application": "other"}, map[string]interface{}{schedulesKey: exampleReport}),
		templateConfigMap("[]"),
		report("dev", "importer-batch-schedules", exampleReport),
	)
	if len(reports) != 0 {
		t.Fatalf("reports = %+v", reports)
	}
	if got := toJSON(reports); got != "[]" {
		t.Errorf("-o json = %s", got)
	}
	out := renderSchedules(reports, "prod", reportAt)
	if !strings.Contains(out, "No in-service schedule reports in prod") || !strings.Contains(out, "adhar batch list") {
		t.Errorf("empty view = %q", out)
	}
	if out := renderSchedules(nil, "", reportAt); !strings.Contains(out, "in any namespace") {
		t.Errorf("empty -A view = %q", out)
	}
}

// One: the §5.20.8 example read field by field, and the view carries the job,
// the trigger's own name, the schedule with its zone, calendar, misfire, state,
// how far off the next fire is, the replicas, and the job's last run with its
// status, outcome, trigger and when it ended.
func TestSchedulesReadTheExampleReport(t *testing.T) {
	reports := readReports(t, "prod", report("prod", "importer-batch-schedules", exampleReport))
	if len(reports) != 1 {
		t.Fatalf("reports = %+v", reports)
	}
	r := reports[0]
	if r.Raw != "" || r.Problem != "" || r.Version != "1" || r.Engine != "quartz" || r.Scheduler != "importer" || r.Application != "importer" || r.Truncated == nil || *r.Truncated {
		t.Fatalf("report = %+v", r)
	}
	if r.Instances == nil || len(*r.Instances) != 1 || (*r.Instances)[0].ID != "importer-6c9f7d-x2k4p1731574800123" || (*r.Instances)[0].Checkin != "2026-11-14T09:00:01Z" {
		t.Errorf("instances = %+v", r.Instances)
	}
	if len(r.Jobs) != 1 || r.Jobs[0].Job != "importJob" || r.Jobs[0].Name != "import-job" || len(r.Jobs[0].Triggers) != 1 {
		t.Fatalf("jobs = %+v", r.Jobs)
	}
	tr := r.Jobs[0].Triggers[0]
	want := scheduleTrigger{Key: "adhar-batch-config.import-job.nightly", Kind: "cron", Expression: "0 30 1 ? * *",
		TimeZone: "Asia/Singapore", Calendar: "sg-business", Misfire: "latest", State: "NORMAL",
		Previous: "2026-11-13T17:30:00Z", Next: "2026-11-16T17:30:00Z"}
	if tr != want {
		t.Errorf("trigger = %+v, want %+v", tr, want)
	}
	lr := r.Jobs[0].LastRun
	if lr == nil || lr.Run != "import-job-09932cb3" || lr.ExecutionID == nil || *lr.ExecutionID != 97 || lr.Status != "COMPLETED" ||
		lr.Outcome != "completed" || lr.Trigger != tr.Key || lr.Start != "2026-11-13T17:30:00Z" || lr.End != "2026-11-13T17:41:12Z" {
		t.Errorf("last run = %+v", lr)
	}

	out := renderSchedules(reports, "prod", reportAt)
	for _, want := range []string{
		"importer", "prod/importer-batch-schedules", "quartz", "1 replica checked in", "updated 2m",
		"import-job", "nightly", "cron 0 30 1 ? * * Asia/Singapore", "sg-business", "latest", "NORMAL",
		"in 2d8h", "import-job-09932cb3", "COMPLETED", "completed", "15h ago",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the table lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "truncated") || strings.Contains(out, "stalled") {
		t.Errorf("a whole, current report is neither truncated nor stalled:\n%s", out)
	}

	var back []map[string]interface{}
	if err := json.Unmarshal([]byte(toJSON(reports)), &back); err != nil || len(back) != 1 || back[0]["configMap"] != "importer-batch-schedules" || back[0]["truncated"] != false || back[0]["raw"] != nil {
		t.Errorf("-o json = %s (%v)", toJSON(reports), err)
	}
}

// Truncated: a report the kit cut to fit 256 KiB says so in the table and in
// -o json, and what it still carries is shown — here a job whose lastRun went
// first, so its LAST RUN is empty, not invented.
func TestSchedulesSayATruncatedReportIsTruncated(t *testing.T) {
	doc := `{"v":1,"engine":"quartz","scheduler":"importer","truncated":true,
	  "jobs":[{"job":"importJob","triggers":[{"key":"adhar-batch-config.import-job.every5","kind":"interval","state":"BLOCKED","next":"2026-11-14T09:05:00Z"}]}]}`
	reports := readReports(t, "prod", report("prod", "importer-batch-schedules", doc))
	if len(reports) != 1 || reports[0].Truncated == nil || !*reports[0].Truncated || reports[0].Raw != "" || reports[0].Jobs[0].LastRun != nil {
		t.Fatalf("report = %+v", reports)
	}
	if !strings.Contains(toJSON(reports), `"truncated":true`) {
		t.Errorf("-o json = %s", toJSON(reports))
	}
	out := renderSchedules(reports, "prod", reportAt)
	if !strings.Contains(out, "This report is truncated") || !strings.Contains(out, "256 KiB") {
		t.Errorf("a truncated report must say so:\n%s", out)
	}
	if !strings.Contains(out, "every5") || !strings.Contains(out, "BLOCKED") || !strings.Contains(out, "in 2m") {
		t.Errorf("what a truncated report still carries is shown:\n%s", out)
	}
	if !strings.Contains(out, "replicas not reported") {
		t.Errorf("a report with no instances array does not claim zero replicas:\n%s", out)
	}
}

// Unknown v: a report of a version this reader does not know — a newer one,
// or a v of another type — is shown as written, not parsed: the fields of the
// newer shape are NOT read into the v1 model, and the raw text reaches both the
// table and -o json.
func TestSchedulesShowAReportOfAnUnknownVersionRaw(t *testing.T) {
	v2 := `{"v":2,"engine":"quartz","scheduler":"importer","truncated":true,"jobs":[{"job":"importJob","triggers":[{"key":"k","next":"2026-11-16T17:30:00Z"}]}]}`
	reports := readReports(t, "prod",
		report("prod", "importer-batch-schedules", v2),
		reportConfigMap("prod", "settle-batch-schedules", map[string]interface{}{labelSchedules: "true"}, map[string]interface{}{schedulesKey: `{"v":"1","jobs":[]}`}),
	)
	if len(reports) != 2 {
		t.Fatalf("reports = %+v", reports)
	}
	r := reports[0]
	if r.Application != "importer" || r.Version != "2" || r.Raw != v2 || !strings.Contains(r.Problem, "version 2") {
		t.Errorf("v2 report = %+v", r)
	}
	if r.Jobs != nil || r.Truncated != nil || r.Engine != "" {
		t.Errorf("a v2 report must not be parsed as v1: %+v", r)
	}
	if s := reports[1]; s.Application != "settle" || s.Version != `"1"` || s.Raw == "" {
		t.Errorf("a string v is not the number 1, and a report without the label is named after its ConfigMap: %+v", s)
	}
	// JSON has one kind of number, so 1.0 is version 1; a null v is a version
	// that is not 1. The Console reads both the same way (CON-Q.1), and the two
	// must not disagree about one report.
	for doc, wantRaw := range map[string]bool{`{"v":1.0,"engine":"quartz"}`: false, `{"v":1e0}`: false, `{"v":null,"engine":"quartz"}`: true, `{"v":1.5}`: true} {
		got := readReports(t, "prod", report("prod", "importer-batch-schedules", doc))[0]
		if (got.Raw != "") != wantRaw {
			t.Errorf("%s: raw = %q, problem = %q; want raw %v", doc, got.Raw, got.Problem, wantRaw)
		}
	}
	out := renderSchedules(reports, "prod", reportAt)
	if !strings.Contains(out, "report version 2 is not one this CLI reads") || !strings.Contains(out, `"v": 2`) {
		t.Errorf("the unknown report is shown raw:\n%s", out)
	}
	if strings.Contains(out, "NEXT FIRE") {
		t.Errorf("no table is drawn from an unknown version:\n%s", out)
	}
	var back []map[string]interface{}
	if err := json.Unmarshal([]byte(toJSON(reports)), &back); err != nil || back[0]["raw"] != v2 || back[0]["version"] != "2" || back[0]["truncated"] != nil {
		t.Errorf("-o json carries the report as written: %s (%v)", toJSON(reports), err)
	}
}

// Every field is optional: `{}`, fields of another type, a job with no
// triggers, a missing key and a document that is not JSON all leave the
// service listed — with what can be read, dashes for the rest, and the reason
// when nothing can — and none fails the command.
func TestSchedulesTreatEveryReportFieldAsOptional(t *testing.T) {
	reports := readReports(t, "prod",
		reportConfigMap("prod", "a-batch-schedules", map[string]interface{}{labelSchedules: "true"}, map[string]interface{}{schedulesKey: `{}`}),
		reportConfigMap("prod", "b-batch-schedules", map[string]interface{}{labelSchedules: "true"}, map[string]interface{}{schedulesKey: `{"engine":7,"truncated":"yes","instances":"two",
		  "jobs":[3,{"name":"settle","triggers":[null,{"kind":"cron","next":"soon"}],"lastRun":{"executionId":"97","status":"WEIRD"}},{"job":"bare"}]}`}),
		reportConfigMap("prod", "c-batch-schedules", map[string]interface{}{labelSchedules: "true"}, map[string]interface{}{"other": "x"}),
		reportConfigMap("prod", "d-batch-schedules", map[string]interface{}{labelSchedules: "true"}, map[string]interface{}{schedulesKey: `not json`}),
		reportConfigMap("prod", "e-batch-schedules", map[string]interface{}{labelSchedules: "true"}, map[string]interface{}{schedulesKey: `[1]`}),
	)
	if len(reports) != 5 {
		t.Fatalf("reports = %d", len(reports))
	}
	a, b, c, d, e := reports[0], reports[1], reports[2], reports[3], reports[4]
	if a.Application != "a" || a.Problem != "" || a.Raw != "" || a.Jobs != nil || a.Version != "" {
		t.Errorf("{} = %+v", a)
	}
	if b.Engine != "" || b.Truncated == nil || *b.Truncated || b.Instances != nil || len(b.Jobs) != 2 {
		t.Fatalf("wrongly typed fields are absent, not errors: %+v", b)
	}
	if j := b.Jobs[0]; j.Name != "settle" || len(j.Triggers) != 1 || j.Triggers[0].Next != "soon" || j.LastRun == nil || j.LastRun.ExecutionID != nil || j.LastRun.Status != "WEIRD" {
		t.Errorf("settle = %+v %+v", j, j.LastRun)
	}
	if c.Problem == "" || c.Raw != "" || d.Raw != "not json" || !strings.Contains(d.Problem, "not JSON") || e.Raw != "[1]" || !strings.Contains(e.Problem, "not a JSON object") {
		t.Errorf("unreadable reports keep the reason: %+v %+v %+v", c, d, e)
	}
	out := renderSchedules(reports, "prod", reportAt)
	for _, want := range []string{"The report lists no jobs.", "settle", "soon", "WEIRD", "bare", "no schedules.json key", "not json"} {
		if !strings.Contains(out, want) {
			t.Errorf("the view lacks %q:\n%s", want, out)
		}
	}
}

// A next fire more than ten minutes in the past is the stall the template's
// alert fires on; the view marks it and says what it means.
func TestSchedulesMarkANextFireLongPast(t *testing.T) {
	doc := `{"v":1,"instances":[],"jobs":[{"job":"importJob","triggers":[{"key":"t","next":"2026-11-14T08:40:00Z"}]}]}`
	out := renderSchedules(readReports(t, "prod", report("prod", "importer-batch-schedules", doc)), "prod", reportAt)
	if !strings.Contains(out, "has stalled") || !strings.Contains(out, "0 replicas checked in") {
		t.Errorf("a stalled schedule is marked:\n%s", out)
	}
	// -o json keeps the same distinction the table makes: an empty instances
	// array is "none checked in", and a report without one says nothing.
	if got := toJSON(readReports(t, "prod", report("prod", "importer-batch-schedules", doc))); !strings.Contains(got, `"instances":[]`) {
		t.Errorf("-o json drops an empty instances array: %s", got)
	}
	if got := toJSON(readReports(t, "prod", report("prod", "importer-batch-schedules", `{"v":1}`))); strings.Contains(got, `"instances"`) {
		t.Errorf("-o json invents an instances array: %s", got)
	}
	recent := `{"v":1,"jobs":[{"job":"importJob","triggers":[{"key":"t","next":"2026-11-14T09:00:00Z"}]}]}`
	if out := renderSchedules(readReports(t, "prod", report("prod", "importer-batch-schedules", recent)), "prod", reportAt); strings.Contains(out, "has stalled") {
		t.Errorf("a fire two minutes late is a fire in progress, not a stall:\n%s", out)
	}
	// Quartz leaves a paused trigger's next fire where it was, so a trigger that
	// does not fire is long past by design; only a firing state can stall, the
	// rule the Console's FIRING_STATES applies.
	for _, state := range []string{"PAUSED", "paused", "PAUSED_BLOCKED", "COMPLETE", "ERROR", "NONE"} {
		doc := `{"v":1,"jobs":[{"job":"importJob","triggers":[{"key":"t","state":"` + state + `","next":"2026-11-14T08:40:00Z"}]}]}`
		if out := renderSchedules(readReports(t, "prod", report("prod", "importer-batch-schedules", doc)), "prod", reportAt); strings.Contains(out, "has stalled") {
			t.Errorf("a %s trigger does not fire, so its old next fire is no stall:\n%s", state, out)
		}
	}
	for _, state := range []string{"NORMAL", "normal", "BLOCKED", "WAITING", "ACQUIRED", "EXECUTING"} {
		doc := `{"v":1,"jobs":[{"job":"importJob","triggers":[{"key":"t","state":"` + state + `","next":"2026-11-14T08:40:00Z"}]}]}`
		if out := renderSchedules(readReports(t, "prod", report("prod", "importer-batch-schedules", doc)), "prod", reportAt); !strings.Contains(out, "has stalled") {
			t.Errorf("a %s trigger fires, so a next fire twenty minutes past is a stall:\n%s", state, out)
		}
	}
}

// Once a verb's arguments parse, its failure is about the cluster, and Cobra's
// usage block is not printed above it; a missing argument still gets usage.
// KUBECONFIG names no file, so every verb fails at connect and none reaches a
// cluster.
func TestBatchVerbsDoNotPrintUsageAboveARuntimeError(t *testing.T) {
	t.Setenv("KUBECONFIG", t.TempDir()+"/absent")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("HOME", t.TempDir())
	reset := func() {
		namespaceFlag, envFlag, projectFlag, outputFlag, schedulesAll = "", "", defaultProject, "table", false
		holdReason = ""
	}
	t.Cleanup(func() {
		reset()
		BatchCmd.SetArgs(nil)
		BatchCmd.SetOut(nil)
		BatchCmd.SetErr(nil)
		for _, c := range BatchCmd.Commands() {
			c.SilenceUsage = false
		}
	})
	execute := func(args ...string) (string, error) {
		reset()
		for _, c := range BatchCmd.Commands() {
			c.SilenceUsage = false
		}
		var out bytes.Buffer
		BatchCmd.SetOut(&out)
		BatchCmd.SetErr(&out)
		BatchCmd.SetArgs(args)
		_, err := BatchCmd.ExecuteC()
		return out.String(), err
	}
	for _, c := range BatchCmd.Commands() {
		if c.RunE == nil {
			continue
		}
		args := []string{c.Name()}
		if c.Args != nil && c.Args(c, nil) != nil {
			args = append(args, "x")
		}
		if c.Name() == "hold" {
			args = append(args, "--reason", "upstream feed late")
		}
		out, err := execute(args...)
		if err == nil {
			t.Errorf("adhar batch %s with no cluster should fail", c.Name())
		}
		if strings.Contains(out, "Usage:") {
			t.Errorf("adhar batch %s prints usage above a runtime error:\n%s", c.Name(), out)
		}
	}
	// A kubeconfig that loads (its server is never dialled: the refusal comes
	// first) so the -A refusal itself is the error, as the verifier saw it.
	kubeconfig := t.TempDir() + "/config"
	if err := os.WriteFile(kubeconfig, []byte(unreachableKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kubeconfig)
	if out, err := execute("schedules", "-A", "-n", "prod"); err == nil || !strings.Contains(err.Error(), "-A reads every namespace") || strings.Contains(out, "Usage:") {
		t.Errorf("-A beside -n is refused without the usage block: %v\n%s", err, out)
	}
	if out, err := execute("describe"); err == nil || !strings.Contains(out, "Usage:") {
		t.Errorf("a missing argument still shows usage: %v\n%s", err, out)
	}
}

// -A reads every namespace, ordered by application then environment in
// promotion order (dev's `sandbox` before prod's `live`, which the alphabet
// would reverse); it refuses -n/--env beside it. A reader who may not list
// ConfigMaps is told so (AQ.8), not shown "no reports".
func TestSchedulesScopeOrderAndRBAC(t *testing.T) {
	devLabels := map[string]interface{}{labelSchedules: "true", "adhar.io/application": "importer", "adhar.io/environment": "dev"}
	prodLabels := map[string]interface{}{labelSchedules: "true", "adhar.io/application": "importer", "adhar.io/environment": "prod"}
	reports := readReports(t, "",
		reportConfigMap("live", "importer-batch-schedules", prodLabels, map[string]interface{}{schedulesKey: exampleReport}),
		reportConfigMap("sandbox", "importer-batch-schedules", devLabels, map[string]interface{}{schedulesKey: exampleReport}),
		reportConfigMap("sandbox", "audit-batch-schedules", map[string]interface{}{labelSchedules: "true", "adhar.io/application": "audit"}, map[string]interface{}{schedulesKey: `{}`}),
	)
	var got []string
	for _, r := range reports {
		got = append(got, r.Application+"@"+r.Namespace)
	}
	if strings.Join(got, ",") != "audit@sandbox,importer@sandbox,importer@live" {
		t.Errorf("order = %v", got)
	}

	cmd := &cobra.Command{}
	cmd.Flags().AddFlagSet(BatchCmd.PersistentFlags())
	cmd.Flags().AddFlagSet(schedulesCmd.Flags())
	reset := func() { namespaceFlag, envFlag, projectFlag, schedulesAll = "", "", defaultProject, false }
	reset()
	t.Cleanup(reset)
	dc := fakeCluster(projectXR("adhar", map[string]string{"dev": "dev", "prod": "prod"}))
	if ns, err := schedulesNamespace(context.Background(), dc, cmd); err != nil || ns != "dev" {
		t.Errorf("no scope = %q %v", ns, err)
	}
	_ = cmd.Flags().Set("env", "prod")
	if ns, err := schedulesNamespace(context.Background(), dc, cmd); err != nil || ns != "prod" {
		t.Errorf("--env prod = %q %v", ns, err)
	}
	_ = cmd.Flags().Set("all-namespaces", "true")
	if _, err := schedulesNamespace(context.Background(), dc, cmd); err == nil {
		t.Error("-A with --env is ambiguous")
	}
	reset()
	_ = cmd.Flags().Set("namespace", "prod")
	_ = cmd.Flags().Set("all-namespaces", "true")
	if _, err := schedulesNamespace(context.Background(), dc, cmd); err == nil {
		t.Error("-A with -n is ambiguous")
	}
	reset()
	_ = cmd.Flags().Set("all-namespaces", "true")
	if ns, err := schedulesNamespace(context.Background(), dc, cmd); err != nil || ns != "" {
		t.Errorf("-A = %q %v", ns, err)
	}

	forbidden := fakeCluster()
	forbidden.PrependReactor("list", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "", nil)
	})
	if _, err := readScheduleReports(context.Background(), forbidden, "prod"); err == nil || !strings.Contains(err.Error(), "needs list on configmaps") {
		t.Errorf("a forbidden list = %v", err)
	}
	if _, err := readScheduleReports(context.Background(), forbidden, ""); err == nil || !strings.Contains(err.Error(), "cluster-wide") {
		t.Errorf("a forbidden -A = %v", err)
	}
}

const unreachableKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: none
  cluster: {server: "https://127.0.0.1:1"}
users:
- name: none
  user: {token: none}
contexts:
- name: none
  context: {cluster: none, user: none}
current-context: none
`
