package adharplatform

// Guards for the batch platform's PLATFORM pieces (design 0025 §16 PLAT-1.1, 1.7, 1.9
// and the composition README of PLAT-1.2): the things the BatchJob composition relies
// on and does not itself compose. Each reads the stack or control-plane tree as
// seeded, so it runs in `make test` without a cluster. Each has been mutation-tested
// (the behaviour reverted, the test seen failing, the behaviour restored).

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// Batch never preempts a service (design 0025 INV-5, §4.4). A batch pod that cannot
// be placed is a capacity question for the autoscaler, never a priority one: every
// batch class has a value at or below zero, so a run can never evict a service or a
// node-pinned stateful pod, and every class yields to every platform class.
//
// Two pairs (user decision DEC-1, 2026-11-14). The native classes, for pods
// kube-scheduler schedules, are preemptionPolicy: Never. The Volcano classes are
// PreemptLowerPriority, because Volcano v1.16 refuses every reclaimer and preemptor on
// a Never class, and each carries its native twin's value so a Volcano run ranks
// exactly where its native counterpart would. What keeps kube-scheduler from
// preempting for a Volcano class is adhar-batch-volcano-classes, which this test holds
// to EVERY preempting batch class: a new one the policy does not name fails here.
// The generic TestNoStackPriorityClassPreemptsNodePinnedWorkloads skips exactly the
// classes that are Never or <= 0, so it cannot see any of these four mutate.
func TestBatchPriorityClassesNeverPreempt(t *testing.T) {
	// name -> the policy it must have, and the value it must carry.
	type class struct {
		policy string
		value  float64
	}
	want := map[string]class{
		"adhar-batch":                  {"Never", -100},
		"adhar-batch-critical":         {"Never", 0},
		"adhar-batch-volcano":          {"PreemptLowerPriority", -100},
		"adhar-batch-volcano-critical": {"PreemptLowerPriority", 0},
	}
	seen := map[string]bool{}
	var preempting []string
	for _, f := range stackPackageManifests(t) {
		for _, doc := range yamlDocs(t, f) {
			if doc["kind"] != "PriorityClass" {
				continue
			}
			name, _ := metaName(doc).(string)
			if !strings.HasPrefix(name, "adhar-batch") {
				continue
			}
			w, known := want[name]
			if !known {
				t.Errorf("%s: PriorityClass %s is a batch class this test does not know; add it here, with the same rules", rel(t, f), name)
			}
			seen[name] = true
			if doc["preemptionPolicy"] != "Never" {
				preempting = append(preempting, name)
			}
			if known && doc["preemptionPolicy"] != w.policy {
				t.Errorf("%s: PriorityClass %s must be preemptionPolicy: %s, got %v", rel(t, f), name, w.policy, doc["preemptionPolicy"])
			}
			var value float64
			switch v := doc["value"].(type) {
			case int:
				value = float64(v)
			case int64:
				value = float64(v)
			case float64:
				value = v
			default:
				t.Errorf("%s: PriorityClass %s has no numeric value (%v)", rel(t, f), name, doc["value"])
			}
			if value > 0 {
				t.Errorf("%s: PriorityClass %s has value %v; a batch class is at or below zero so it outranks nothing", rel(t, f), name, value)
			}
			if known && value != w.value {
				t.Errorf("%s: PriorityClass %s has value %v, want %v (a Volcano class ranks with its native twin)", rel(t, f), name, value, w.value)
			}
			if gd, ok := doc["globalDefault"].(bool); ok && gd {
				t.Errorf("%s: PriorityClass %s is globalDefault; a service would silently become a batch pod", rel(t, f), name)
			}
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("PriorityClass %s is not shipped by core/adhar-batch", name)
		}
	}

	// Every preempting batch class is held to schedulerName: volcano, Enforced.
	sort.Strings(preempting)
	docs := yamlDocs(t, filepath.Join(stackPackagesDir(t), "core", "adhar-batch", "manifests", "kyverno-volcano-classes.yaml"))
	if len(docs) != 1 || metaName(docs[0]) != "adhar-batch-volcano-classes" {
		t.Fatalf("kyverno-volcano-classes.yaml must hold exactly ClusterPolicy adhar-batch-volcano-classes, got %d docs", len(docs))
	}
	rules, _ := dig(docs[0], "spec", "rules").([]any)
	if len(rules) != 1 {
		t.Fatalf("adhar-batch-volcano-classes has %d rules, want 1", len(rules))
	}
	rule := rules[0].(map[string]any)
	if kinds := dig(rule, "match", "any").([]any)[0].(map[string]any)["resources"].(map[string]any)["kinds"]; !reflectEqualStrings(kinds, []string{"Pod"}) {
		t.Errorf("adhar-batch-volcano-classes matches %v; it must match Pods (autogen carries it to Jobs and CronJobs)", kinds)
	}
	if dig(rule, "validate", "failureAction") != "Enforce" {
		t.Errorf("adhar-batch-volcano-classes is %v; a preempting class on a kube-scheduler pod must be refused", dig(rule, "validate", "failureAction"))
	}
	spec, _ := dig(rule, "validate", "pattern", "spec").(map[string]any)
	if spec["schedulerName"] != "volcano" {
		t.Errorf("adhar-batch-volcano-classes requires schedulerName %v, want volcano", spec["schedulerName"])
	}
	anchored, _ := spec["(priorityClassName)"].(string)
	var named []string
	for _, n := range strings.Split(anchored, "|") {
		named = append(named, strings.TrimSpace(n))
	}
	sort.Strings(named)
	if strings.Join(named, ",") != strings.Join(preempting, ",") {
		t.Errorf("adhar-batch-volcano-classes holds %v to schedulerName volcano, but the preempting batch classes are %v", named, preempting)
	}
}

func reflectEqualStrings(v any, want []string) bool {
	got, ok := v.([]any)
	if !ok || len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// kube-state-metrics exports no labels by default, so without this allowlist every
// alert the BatchJob composition writes (kube_job_labels{label_adhar_io_batch_job=…},
// kube_pod_labels{…}) and Kargo's batch verification join on a series that does not
// exist and never fire. The values are the source; the rendered install.yaml is what
// the cluster runs; both must say exactly what design 0025 §16 PLAT-1.7 lists.
func TestKubeStateMetricsExportsTheBatchLabels(t *testing.T) {
	want := []string{
		"jobs=[adhar.io/batch-job,adhar.io/application,adhar.io/environment]",
		"pods=[adhar.io/batch-job,adhar.io/application,adhar.io/environment]",
		"cronjobs=[adhar.io/batch-job,adhar.io/application]",
	}
	var values struct {
		KSM struct {
			Allowlist []string `json:"metricLabelsAllowlist"`
		} `json:"kube-state-metrics"`
	}
	if err := yaml.Unmarshal([]byte(readStackFile(t, "observability/kube-prometheus/values.yaml")), &values); err != nil {
		t.Fatal(err)
	}
	if got := values.KSM.Allowlist; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("values.yaml kube-state-metrics.metricLabelsAllowlist = %q, want %q", got, want)
	}

	var rendered []string
	for _, doc := range strings.Split(readStackFile(t, "observability/kube-prometheus/manifests/install.yaml"), "\n---\n") {
		if !strings.Contains(doc, "\nkind: Deployment\n") || !strings.Contains(doc, "kube-state-metrics") {
			continue
		}
		var d struct {
			Spec struct {
				Template struct {
					Spec struct {
						Containers []struct {
							Name string   `json:"name"`
							Args []string `json:"args"`
						} `json:"containers"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal([]byte(doc), &d); err != nil {
			continue
		}
		for _, c := range d.Spec.Template.Spec.Containers {
			if c.Name != "kube-state-metrics" {
				continue
			}
			for _, a := range c.Args {
				if strings.HasPrefix(a, "--metric-labels-allowlist=") {
					rendered = append(rendered, strings.TrimPrefix(a, "--metric-labels-allowlist="))
				}
			}
		}
	}
	if len(rendered) != 1 {
		t.Fatalf("install.yaml: want exactly one --metric-labels-allowlist on the kube-state-metrics container, found %d: %q (regenerate with packages/observability/kube-prometheus/generate-manifests.sh)", len(rendered), rendered)
	}
	if rendered[0] != strings.Join(want, ",") {
		t.Errorf("install.yaml --metric-labels-allowlist=%q, want %q (regenerate with generate-manifests.sh from values.yaml)", rendered[0], strings.Join(want, ","))
	}

	// The allowlist is read back as label_<label with . and / as _>: every
	// kube_*_labels selector a batch reader writes must name a label it exports.
	// That is also why cronjobs carries adhar.io/application beyond §16's list: the
	// batch-jvm dashboard joins kube_cronjob_labels on it.
	exported := map[string]bool{}
	for _, entry := range want {
		kind := entry[:strings.Index(entry, "=")]
		for _, l := range strings.Split(strings.Trim(entry[strings.Index(entry, "[")+1:], "[]"), ",") {
			exported[kind+"/label_"+strings.NewReplacer(".", "_", "/", "_", "-", "_").Replace(l)] = true
		}
	}
	readers := map[string]string{
		bjComposition:                                     readConfigurationFile(t, bjComposition),
		"compositions/apps/argocd-multienv.yaml":          readConfigurationFile(t, "compositions/apps/argocd-multienv.yaml"),
		"compositions/project/local.yaml":                 readConfigurationFile(t, "compositions/project/local.yaml"),
		"core/adhar-batch/manifests/dashboard-batch.yaml": readStackFile(t, "core/adhar-batch/manifests/dashboard-batch.yaml"),
	}
	for file, src := range readers {
		// A label copied across with group_left(...) is read from the same series.
		for _, m := range regexp.MustCompile(`group_left\(([^)]*)\)\s*\(?\s*(kube_(job|pod|cronjob)_labels)`).FindAllStringSubmatch(src, -1) {
			kind := map[string]string{"job": "jobs", "pod": "pods", "cronjob": "cronjobs"}[m[3]]
			for _, l := range regexp.MustCompile(`label_[a-z0-9_]+`).FindAllString(m[1], -1) {
				if !exported[kind+"/"+l] {
					t.Errorf("%s copies %s from %s with group_left, which kube-state-metrics is not told to export", file, l, m[2])
				}
			}
		}
		for _, m := range regexp.MustCompile(`kube_(job|pod|cronjob)_labels\{[^}]*\}`).FindAllString(src, -1) {
			kind := map[string]string{"kube_job_labels": "jobs", "kube_pod_labels": "pods", "kube_cronjob_labels": "cronjobs"}[m[:strings.Index(m, "{")]]
			for _, l := range regexp.MustCompile(`label_[a-z0-9_]+`).FindAllString(m, -1) {
				if !exported[kind+"/"+l] {
					t.Errorf("%s selects %s on %s, which kube-state-metrics is not told to export", file, l, kind)
				}
			}
		}
	}
	// The composition builds its selectors with printf, so the scan above cannot see
	// them; every label it names is joined on both kube_job_labels and kube_pod_labels.
	for _, l := range regexp.MustCompile(`label_adhar_io_[a-z0-9_]+`).FindAllString(readers[bjComposition], -1) {
		if !exported["jobs/"+l] || !exported["pods/"+l] {
			t.Errorf("%s joins on %s, which kube-state-metrics is not told to export for jobs and pods", bjComposition, l)
		}
	}
}

// The composition header promises that README.md beside it maps every parameter to
// what it becomes (design 0025 §6.4). A parameter added to the XRD without a row
// there is a field a team has to reverse-engineer from a 600-line template.
func TestBatchJobCompositionReadmeMapsEveryParameter(t *testing.T) {
	var xrd struct {
		Spec struct {
			Versions []struct {
				Schema struct {
					OpenAPIV3Schema struct {
						Properties struct {
							Spec struct {
								Properties struct {
									Parameters struct {
										Properties map[string]struct {
											Properties map[string]any `json:"properties"`
										} `json:"properties"`
									} `json:"parameters"`
								} `json:"properties"`
							} `json:"spec"`
						} `json:"properties"`
					} `json:"openAPIV3Schema"`
				} `json:"schema"`
			} `json:"versions"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal([]byte(readConfigurationFile(t, "xrd/batchjob.xrd.yaml")), &xrd); err != nil {
		t.Fatal(err)
	}
	params := xrd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties.Spec.Properties.Parameters.Properties
	if len(params) < 20 {
		t.Fatalf("parsed only %d BatchJob parameters; the XRD layout changed", len(params))
	}
	var paths []string
	for name, p := range params {
		paths = append(paths, name)
		for sub := range p.Properties {
			paths = append(paths, name+"."+sub)
		}
	}
	sort.Strings(paths)

	readme, err := os.ReadFile(filepath.Join(controlPlaneDir(t), "compositions/batchjob/README.md"))
	if err != nil {
		t.Fatalf("compositions/batchjob/README.md: %v (the composition header promises it)", err)
	}
	for _, p := range paths {
		if !strings.Contains(string(readme), "`"+p+"`") {
			t.Errorf("compositions/batchjob/README.md has no row for parameter `%s`", p)
		}
	}
	// And nothing the parameter table maps is a field the XRD does not define (D3:
	// later-phase fields arrive with their phase, and a row for one would say otherwise).
	table := string(readme)
	if i := strings.Index(table, "## Every parameter"); i < 0 {
		t.Fatal("compositions/batchjob/README.md has no '## Every parameter' section")
	} else if j := strings.Index(table[i+1:], "\n## "); j > 0 {
		table = table[i : i+1+j]
	}
	for _, m := range regexp.MustCompile("(?m)^\\| `([a-zA-Z.]+)`").FindAllStringSubmatch(table, -1) {
		if i := sort.SearchStrings(paths, m[1]); i == len(paths) || paths[i] != m[1] {
			t.Errorf("compositions/batchjob/README.md maps `%s`, which the Phase 1 XRD does not define", m[1])
		}
	}
}

// Design 0025 R5: every Bucket's connection Secret hands the application the object
// store's ROOT credential. PLAT-1.9 was a spike with two outcomes, per-bucket users
// or a recorded risk acceptance, and the acceptance must live where the credential
// is handed out: as long as the composition still points at root-creds, its header
// records R5, so the next person to open the file reads the decision, not a TODO.
func TestBucketRootCredentialRiskIsRecordedWhereItIsTaken(t *testing.T) {
	src := readConfigurationFile(t, "compositions/bucket/local-rustfs.yaml")
	handsOutRoot := strings.Contains(src, `credentialsSecret: "root-creds"`)
	recordsRisk := strings.Contains(src, "design 0025 R5") && strings.Contains(src, "rc admin user add")
	if handsOutRoot && !recordsRisk {
		t.Error("compositions/bucket/local-rustfs.yaml still hands every bucket root-creds and no longer records the R5 risk acceptance (with the rc admin commands a scoped user would use) in its header")
	}
	if !handsOutRoot && recordsRisk {
		t.Error("compositions/bucket/local-rustfs.yaml no longer hands out root-creds; retire the R5 acceptance in its header and in docs/BATCH.md")
	}
}

// Design 0025 R6. Until DEC-4 (2026-11-14) security/kyverno set
// forceFailurePolicyIgnore, which overrode the integrity policy's own failurePolicy:
// Fail, so a manual run was admitted UNCHECKED while Kyverno was down (live on
// v1.19.0). The flag is off now and the webhook fails closed, but R6's fallback stays:
// an audit alert on the API server's fail-open counter, matching the webhook name
// Kyverno registers for the policy under EITHER failure policy, so turning the flag
// back on, or the policy to Ignore, cannot make it count nothing and read exactly like
// a quiet week.
func TestManualRunsAdmittedUncheckedRaiseAnAlert(t *testing.T) {
	pkg := filepath.Join(stackRoot(t), "packages", "core", "adhar-batch", "manifests")
	var policy string
	for _, doc := range yamlDocs(t, filepath.Join(pkg, "kyverno-run-integrity.yaml")) {
		// Only a policy with its own matchConditions gets a fine-grained webhook
		// (Kyverno 1.19 Spec.CustomWebhookMatchConditions); a failurePolicy alone
		// does not, and every policy states one since DEC-4.
		if doc["kind"] == "ClusterPolicy" && len(toSlice(dig(doc, "spec", "webhookConfiguration", "matchConditions"))) > 0 {
			policy, _ = metaName(doc).(string)
		}
	}
	if policy == "" {
		t.Fatal("kyverno-run-integrity.yaml ships no ClusterPolicy with its own webhook matchConditions")
	}
	matcher := regexp.MustCompile(`apiserver_admission_webhook_fail_open_count\{[^}]*name=~"([^"]+)"`)
	var exprs []string
	for _, doc := range yamlDocs(t, filepath.Join(pkg, "alerts.yaml")) {
		if doc["kind"] != "PrometheusRule" {
			continue
		}
		spec, _ := doc["spec"].(map[string]any)
		groups, _ := spec["groups"].([]any)
		for _, g := range groups {
			rules, _ := g.(map[string]any)["rules"].([]any)
			for _, r := range rules {
				if expr, ok := r.(map[string]any)["expr"].(string); ok && matcher.MatchString(expr) {
					exprs = append(exprs, expr)
				}
			}
		}
	}
	if len(exprs) == 0 {
		t.Fatal("core/adhar-batch ships no alert on apiserver_admission_webhook_fail_open_count; a manual run admitted while Kyverno is down would leave no trace")
	}
	for _, failure := range []string{"ignore", "fail"} {
		webhook := "validate.kyverno.svc-" + failure + "-finegrained-" + policy
		seen := false
		for _, expr := range exprs {
			pattern := strings.ReplaceAll(matcher.FindStringSubmatch(expr)[1], `\\`, `\`)
			if regexp.MustCompile("^(?:" + pattern + ")$").MatchString(webhook) {
				seen = true
			}
		}
		if !seen {
			t.Errorf("no fail-open alert matches webhook %s, the name Kyverno registers for %s", webhook, policy)
		}
	}
}
