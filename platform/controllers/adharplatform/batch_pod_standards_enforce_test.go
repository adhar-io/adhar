package adharplatform

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// The batch pod standards are Enforced from Phase 3 (design 0025 §10.3, PLAT-3.6):
// core/adhar-batch's adhar-batch-pod-standards and, for Volcano's kinds,
// adhar-batch-volcano-pod-standards. The kyverno CLI proves each rule both ways
// (make policy-test: policy-tests/{pod-standards,volcano-pod-standards}) and proves
// that what the BatchJob composition renders passes all of them
// (policy-tests/composed-runs). These tests hold what the CLI cannot see: that no
// rule is quietly left at Audit, that every Job rule has its CronJob twin, and that
// the composed-runs fixture IS the composition's golden renders.

// podStandardsPolicies returns the two pod-standards ClusterPolicies, by name.
func podStandardsPolicies(t *testing.T) map[string]map[string]any {
	t.Helper()
	dir := queuePolicyDir(t)
	out := map[string]map[string]any{}
	for _, f := range []string{
		filepath.Join(dir, "manifests", "kyverno-pod-standards.yaml"),
		filepath.Join(dir, "manifests", "volcano", "kyverno-queue.yaml"),
	} {
		for _, d := range readYAMLDocs(t, f) {
			if name, _ := dig(d, "metadata", "name").(string); name == "adhar-batch-pod-standards" || name == "adhar-batch-volcano-pod-standards" {
				out[name] = d
			}
		}
	}
	if len(out) != 2 {
		t.Fatalf("found %d of the two pod-standards policies", len(out))
	}
	return out
}

// Every rule refuses, and an UPDATE of an object admitted while the rule audited
// is still allowed (stop, resume and hold patch existing runs and schedules). The
// webhook stays fail-open (D18): only run integrity and the queue binding fail
// closed.
func TestBatchPodStandardsAreEnforced(t *testing.T) {
	for name, p := range podStandardsPolicies(t) {
		if fp := dig(p, "spec", "webhookConfiguration", "failurePolicy"); fp != "Ignore" {
			t.Errorf("%s: webhookConfiguration.failurePolicy is %v; D18 keeps the pod standards Ignore", name, fp)
		}
		rules := policyRules(p)
		if len(rules) == 0 {
			t.Fatalf("%s has no rules", name)
		}
		for _, r := range rules {
			if a := dig(r, "validate", "failureAction"); a != "Enforce" {
				t.Errorf("%s/%v: failureAction %v; the pod standards are Enforce from Phase 3 (§10.3)", name, r["name"], a)
			}
			if v := dig(r, "validate", "allowExistingViolations"); v != true {
				t.Errorf("%s/%v: allowExistingViolations is %v; it must be stated true, or stopping a run admitted under Audit is refused", name, r["name"], v)
			}
		}
		if dig(p, "spec", "validationFailureAction") != nil {
			t.Errorf("%s sets the policy-level validationFailureAction; the rules carry it", name)
		}
	}
}

// Kyverno's autogen gives a rule that matches Jobs no CronJob rule, so each Job (or
// Volcano Job) rule has a twin for the CronJob kind whose validation is the same
// one moved under spec.jobTemplate. The twins follow the Job rules in the same
// order; a standard changed on one side only would leave schedules creating runs
// the other side refuses, or admit what it refuses.
func TestBatchPodStandardsHaveCronJobTwins(t *testing.T) {
	kinds := map[string][2]string{
		"adhar-batch-pod-standards":         {"batch/v1/Job", "batch/v1/CronJob"},
		"adhar-batch-volcano-pod-standards": {"batch.volcano.sh/v1alpha1/Job", "batch.volcano.sh/v1alpha1/CronJob"},
	}
	for name, p := range podStandardsPolicies(t) {
		var jobs, crons []map[string]any
		for _, r := range policyRules(p) {
			b, _ := json.Marshal(dig(r, "match"))
			switch {
			case strings.Contains(string(b), `"`+kinds[name][1]+`"`):
				crons = append(crons, r)
			case strings.Contains(string(b), `"`+kinds[name][0]+`"`):
				jobs = append(jobs, r)
			default:
				t.Errorf("%s/%v matches neither %v", name, r["name"], kinds[name])
			}
		}
		if len(jobs) == 0 || len(jobs) != len(crons) {
			t.Errorf("%s: %d Job rules and %d CronJob twins; every standard needs both", name, len(jobs), len(crons))
			continue
		}
		for i := range jobs {
			j, c := jobs[i], crons[i]
			// The twin selects batch schedules by the JOB TEMPLATE's label, which the
			// runs inherit.
			pre, _ := json.Marshal(c["preconditions"])
			if !strings.Contains(string(pre), `request.object.spec.jobTemplate.metadata.labels.\"adhar.io/workload\"`) || !strings.Contains(string(pre), `"batch"`) {
				t.Errorf("%s/%v does not select batch schedules by spec.jobTemplate's adhar.io/workload label: %s", name, c["name"], pre)
			}
			if pat := dig(j, "validate", "pattern", "spec"); pat != nil {
				if got := dig(c, "validate", "pattern", "spec", "jobTemplate", "spec"); !reflect.DeepEqual(got, pat) {
					t.Errorf("%s: %v checks spec %v but its twin %v checks spec.jobTemplate.spec %v", name, j["name"], pat, c["name"], got)
				}
				continue
			}
			jf, _ := dig(j, "validate", "foreach").([]any)
			cf, _ := dig(c, "validate", "foreach").([]any)
			if len(jf) != 1 || len(cf) != 1 {
				t.Errorf("%s: %v / %v are neither a pattern nor one foreach", name, j["name"], c["name"])
				continue
			}
			je, ce := jf[0].(map[string]any), cf[0].(map[string]any)
			list, _ := je["list"].(string)
			want := strings.Replace(list, "request.object.spec.", "request.object.spec.jobTemplate.spec.", 1)
			if ce["list"] != want {
				t.Errorf("%s/%v iterates %v; its Job rule iterates %q, so it must iterate %q", name, c["name"], ce["list"], list, want)
			}
			if !reflect.DeepEqual(je["deny"], ce["deny"]) {
				t.Errorf("%s: %v and its twin %v deny on different conditions", name, j["name"], c["name"])
			}
		}
	}
}

// composedRunsFixtures are the files the kyverno suites load the composition's
// renders from: the pod standards (core/adhar-batch) and the policy packs
// (security/adhar-policy-packs), which must admit a composed run as they would a
// composed service.
var composedRunsFixtures = []string{
	filepath.Join("core", "adhar-batch", "policy-tests", "composed-runs", "resources.yaml"),
	filepath.Join("security", "adhar-policy-packs", "policy-tests", "batch-runs", "resources.yaml"),
}

// composedRuns is every run-shaped object of the BatchJob golden renders
// (testdata/batchjob-volcano: every branch of the composition): the run template a
// client creates a run from, and the CronJob (batch/v1 or Volcano) that the clock
// creates runs from. Each is named <golden>-<composed resource>, the run template
// <golden>-run, so a kyverno-test.yaml row names it.
func composedRuns(t *testing.T) []byte {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(vcFixtureDir, "golden-*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no golden renders under %s (%v)", vcFixtureDir, err)
	}
	sort.Strings(files)
	var docs [][]byte
	for _, f := range files {
		var g struct {
			Composed map[string]map[string]any `json:"composed"`
		}
		if err := yaml.Unmarshal(mustReadTestdata(t, f), &g); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		stem := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "golden-"), ".yaml")
		names := make([]string, 0, len(g.Composed))
		for n := range g.Composed {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			obj := g.Composed[n]
			name := stem + "-" + n
			if n == "run-template" {
				obj, _ = obj["template.yaml"].(map[string]any)
				name = stem + "-run"
			}
			if obj == nil || (obj["kind"] != "Job" && obj["kind"] != "CronJob") {
				continue
			}
			md, _ := obj["metadata"].(map[string]any)
			md = mapCopy(md)
			md["name"] = name
			delete(md, "annotations") // the render's bookkeeping (composition-resource-name, ready)
			obj = mapCopy(obj)
			obj["metadata"] = md
			b, err := yaml.Marshal(obj)
			if err != nil {
				t.Fatal(err)
			}
			docs = append(docs, b)
		}
	}
	var out bytes.Buffer
	out.WriteString("# GENERATED from platform/controllers/adharplatform/testdata/batchjob-volcano/golden-*.yaml by\n")
	out.WriteString("# TestComposedBatchRunsFixtureIsTheGoldenRenders (ADHAR_UPDATE_GOLDEN=1 rewrites it):\n")
	out.WriteString("# every run template and CronJob the BatchJob composition renders, one per branch.\n")
	for i, d := range docs {
		if i > 0 {
			out.WriteString("---\n")
		}
		out.Write(d)
	}
	return out.Bytes()
}

func mapCopy(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// The composed-runs suites prove what the composition renders, so their fixture
// must be the renders: when the composition changes, ADHAR_UPDATE_GOLDEN=1 (the
// same switch that rewrites the goldens) regenerates it, and make policy-test then
// says whether the new render still passes Enforce and the packs. Every resource in
// it must also be asserted by its suite, or a new branch would pass by omission.
func TestComposedBatchRunsFixtureIsTheGoldenRenders(t *testing.T) {
	want := composedRuns(t)
	var docs []map[string]any
	for _, d := range strings.Split(string(want), "\n---\n") {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(d), &m); err != nil {
			t.Fatal(err)
		}
		docs = append(docs, m)
	}
	if len(docs) < 6 {
		t.Fatalf("only %d composed runs in the goldens; expected every branch's run template and schedule", len(docs))
	}
	for _, rel := range composedRunsFixtures {
		path := filepath.Join(stackPackagesDir(t), rel)
		if os.Getenv("ADHAR_UPDATE_GOLDEN") == "1" {
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v (ADHAR_UPDATE_GOLDEN=1 writes it)", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is not the golden renders (ADHAR_UPDATE_GOLDEN=1 rewrites it, then run make policy-test):\n%s", rel, lineDiff(string(want), string(got)))
		}
		test := readStackFile(t, filepath.ToSlash(filepath.Join(filepath.Dir(rel), "kyverno-test.yaml")))
		for _, d := range docs {
			name, _ := dig(d, "metadata", "name").(string)
			if !regexp.MustCompile(`[\[\s,]` + regexp.QuoteMeta(name) + `[\],]`).MatchString(test) {
				t.Errorf("%s asserts nothing about the composed %s %s", filepath.Dir(rel), d["kind"], name)
			}
		}
	}
}
