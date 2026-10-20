package adharplatform

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Scan governance (application/adhar-supply-chain/manifests/74-scan-governance.yaml).
//
// The paved road's vulnerability gate became configurable: a project may set a
// scan policy (severity threshold, warn vs enforce, manual review), reviewers
// may grant waivers, and a paused run can be approved. Every one of those is a
// way to LOOSEN a control, so these tests pin the parts that keep it honest:
//
//   - with nothing configured, the gate behaves exactly as before;
//   - the scanners measure and ONE task judges, so a waiver applies everywhere
//     at once and a finding is never retried into passing;
//   - the review is driven by an annotation only reviewers may set, read by an
//     identity the repository's own test code does not run as;
//   - what was waived is attached to the image, signed, for anyone downstream.

type k8sDoc = map[string]any

func docsOf(t *testing.T, rel string) []k8sDoc {
	t.Helper()
	var out []k8sDoc
	for _, raw := range strings.Split(readStackFile(t, rel), "\n---") {
		var d k8sDoc
		if err := yaml.Unmarshal([]byte(raw), &d); err != nil {
			t.Fatalf("%s: a document does not parse: %v", rel, err)
		}
		if d != nil {
			out = append(out, d)
		}
	}
	return out
}

func findDoc(t *testing.T, docs []k8sDoc, kind, name string) k8sDoc {
	t.Helper()
	for _, d := range docs {
		meta, _ := d["metadata"].(map[string]any)
		if d["kind"] == kind && meta["name"] == name {
			return d
		}
	}
	t.Fatalf("no %s/%s", kind, name)
	return nil
}

func sgDig(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			a, _ := v.([]any)
			if k >= len(a) {
				return nil
			}
			v = a[k]
		}
	}
	return v
}

func pipelineTasks(t *testing.T, pipeline k8sDoc, section string) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, raw := range sgDig(pipeline, "spec", section).([]any) {
		task := raw.(map[string]any)
		out[task["name"].(string)] = task
	}
	return out
}

func taskParams(task map[string]any) map[string]string {
	out := map[string]string{}
	params, _ := task["params"].([]any)
	for _, raw := range params {
		p := raw.(map[string]any)
		v, _ := p["value"].(string)
		if v == "" {
			v, _ = p["default"].(string)
		}
		out[p["name"].(string)] = v
	}
	return out
}

func stepScript(t *testing.T, task k8sDoc, step string) string {
	t.Helper()
	for _, raw := range sgDig(task, "spec", "steps").([]any) {
		s := raw.(map[string]any)
		if s["name"] == step {
			script, _ := s["script"].(string)
			return script
		}
	}
	t.Fatalf("task %v has no step %q", sgDig(task, "metadata", "name"), step)
	return ""
}

func strList(v any) []string {
	var out []string
	a, _ := v.([]any)
	for _, x := range a {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func sgHas(list []string, want string) bool {
	for _, x := range list {
		if x == want {
			return true
		}
	}
	return false
}

const supplyChain = "application/adhar-supply-chain/manifests/"

// The scanners measure, scan-gate judges, and nothing is signed before it has.
func TestAppCiScannersMeasureAndTheScanGateJudges(t *testing.T) {
	pipeline := findDoc(t, docsOf(t, supplyChain+"70-app-ci.yaml"), "Pipeline", "app-ci")
	tasks := pipelineTasks(t, pipeline, "tasks")

	for _, name := range []string{"scan", "source-scan"} {
		task := tasks[name]
		if task == nil {
			t.Fatalf("app-ci has no %s task", name)
		}
		p := taskParams(task)
		if p["blocking"] != "false" {
			t.Errorf("app-ci runs %s with blocking=%q; in app-ci the scanners only record a verdict and "+
				"scan-gate decides, or a waiver or a review could never let a run through", name, p["blocking"])
		}
		if p["severity"] != "$(tasks.scan-policy.results.failOnSeverity)" {
			t.Errorf("%s does not take its threshold from the resolved scan policy", name)
		}
		if !sgHas(strList(task["runAfter"]), "scan-policy") {
			t.Errorf("%s does not wait for scan-policy, so it can scan before the waivers are merged", name)
		}
	}
	if ws, _ := tasks["scan"]["workspaces"].([]any); len(ws) == 0 || sgDig(ws, 0, "name") != "reports" {
		t.Error("the image scan does not bind the reports workspace: it would read no waivers and publish no report")
	}

	gate := tasks["scan-gate"]
	if gate == nil {
		t.Fatal("app-ci has no scan-gate task")
	}
	after := strList(gate["runAfter"])
	if !sgHas(after, "scan") || !sgHas(after, "source-scan") {
		t.Errorf("scan-gate runs after %v; it must judge BOTH scanners", after)
	}
	gp := taskParams(gate)
	for k, v := range map[string]string{
		"imageVerdict":  "$(tasks.scan.results.verdict)",
		"sourceVerdict": "$(tasks.source-scan.results.verdict)",
		"pipelinerun":   "$(context.pipelineRun.name)",
		"mode":          "$(tasks.scan-policy.results.mode)",
	} {
		if gp[k] != v {
			t.Errorf("scan-gate param %s = %q, want %q", k, gp[k], v)
		}
	}
	if _, retried := gate["retries"]; retried {
		t.Error("scan-gate is retried; a retry re-opens a review that already has an answer")
	}

	sign := strList(tasks["sign"]["runAfter"])
	for _, want := range []string{"scan-gate", "scan-attest", "test", "secrets", "sbom"} {
		if !sgHas(sign, want) {
			t.Errorf("sign does not wait for %s; an image could be signed — and so admitted — before it", want)
		}
	}
	if sgHas(sign, "scan") || sgHas(sign, "source-scan") {
		t.Error("sign still depends on the scanners directly; with blocking=false they always succeed, " +
			"so only scan-gate may stand between them and the signature")
	}
	// Two writers of the attestation manifest race; the SBOM attest follows sign.
	if !sgHas(strList(tasks["scan-attest"]["runAfter"]), "scan") {
		t.Error("scan-attest does not wait for the image scan whose report it attaches")
	}

	// The PipelineRun results the Console reads, by name.
	results := map[string]string{}
	for _, raw := range sgDig(pipeline, "spec", "results").([]any) {
		results[sgDig(raw, "name").(string)], _ = sgDig(raw, "value").(string)
	}
	for name, want := range map[string]string{
		"scan-gate":     "$(tasks.scan-gate.results.state)",
		"scan-summary":  "$(tasks.scan-gate.results.summary)",
		"scan-findings": "$(tasks.scan-gate.results.findings)",
		"IMAGE_DIGEST":  "$(tasks.scan.results.digest)",
	} {
		if results[name] != want {
			t.Errorf("Pipeline result %s = %q, want %q (the Console reads it by that name)", name, results[name], want)
		}
	}

	finally := pipelineTasks(t, pipeline, "finally")
	if finally["scan-report"] == nil {
		t.Error("no scan-report finally task: a run that fails before the gate publishes no scan summary")
	}
	stages := taskParams(finally["summary"])["stages"]
	for _, want := range []string{"scan-gate=$(tasks.scan-gate.status)", "scan-policy=", "scan-attest="} {
		if !strings.Contains(stages, want) {
			t.Errorf("ci-summary stages lack %q", want)
		}
	}
}

// Both trigger templates run the governance tasks as adhar-scan-gate and give
// the run room to wait for a reviewer.
func TestAppCiRunsGovernanceTasksUnderTheirOwnIdentity(t *testing.T) {
	docs := docsOf(t, supplyChain+"70-app-ci.yaml")
	pipeline := findDoc(t, docs, "Pipeline", "app-ci")
	gateTimeout, _ := pipelineTasks(t, pipeline, "tasks")["scan-gate"]["timeout"].(string)
	if gateTimeout == "" {
		t.Fatal("scan-gate has no timeout; it would inherit Tekton's 60-minute task default and a review could not outlast it")
	}
	for _, name := range []string{"run-app-ci", "run-app-pr-ci"} {
		tt := findDoc(t, docs, "TriggerTemplate", name)
		spec := sgDig(tt, "spec", "resourcetemplates", 0, "spec")
		if got := sgDig(spec, "taskRunTemplate", "serviceAccountName"); got != "adhar-pipeline" {
			t.Errorf("%s: default identity is %v, want adhar-pipeline (it holds the signing key)", name, got)
		}
		sa := map[string]string{}
		for _, raw := range sgDig(spec, "taskRunSpecs").([]any) {
			s := raw.(map[string]any)
			sa[s["pipelineTaskName"].(string)], _ = s["serviceAccountName"].(string)
		}
		for _, task := range []string{"scan-policy", "scan-gate", "scan-report"} {
			if sa[task] != "adhar-scan-gate" {
				t.Errorf("%s: %s runs as %q, want adhar-scan-gate — adhar-pipeline also runs the repository's "+
					"own tests, and that code must not be able to approve its own review", name, task, sa[task])
			}
		}
		if sgDig(spec, "timeouts", "pipeline") != "24h" {
			t.Errorf("%s: timeouts.pipeline = %v; it must exceed scan-gate's %s or a review is cut short", name,
				sgDig(spec, "timeouts", "pipeline"), gateTimeout)
		}
	}
}

// With no scan-policy anywhere, the resolved policy is the gate app-ci had
// before governance existed, and every unparseable value lands on the strict side.
func TestScanPolicyDefaultsAreTheOriginalGate(t *testing.T) {
	docs := docsOf(t, supplyChain+"74-scan-governance.yaml")
	script := stepScript(t, findDoc(t, docs, "Task", "scan-policy"), "resolve")
	for _, want := range []string{
		"pick mode enforce",
		"pick manualApproval false",
		"pick ignoreUnfixed false",
		"pick failOnSeverity HIGH,CRITICAL",
		"pick scanners vuln,misconfig",
		"pick approvalTimeoutMinutes 720",
		// unknown values fall back to the defaults, not to something looser
		`*) echo "mode '$MODE' unknown → enforce"; MODE=enforce ;;`,
		`[ -n "$SEV" ] || SEV=HIGH,CRITICAL`,
		// the image is always scanned for vulnerabilities
		"IMG=vuln;",
		// project overrides global overrides default
		`v=$(jq -r --arg k "$1" '.data[$k] // empty' <<<"$PP")`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("scan-policy no longer contains %q", want)
		}
	}
	if strings.Index(script, `<<<"$PP"`) > strings.Index(script, `<<<"$GP"`) {
		t.Error("scan-policy reads the global policy before the project's; the project must win")
	}

	// And the standalone defaults of the scanners themselves.
	src := findDoc(t, docsOf(t, supplyChain+"72-app-ci-stages.yaml"), "Task", "source-scan")
	sp := taskParams(src["spec"].(map[string]any))
	if sp["severity"] != "HIGH,CRITICAL" || sp["blocking"] != "true" || sp["scanners"] != "vuln,misconfig" || sp["ignoreUnfixed"] != "false" {
		t.Errorf("source-scan defaults changed: %v", sp)
	}
}

// One merged ignore file: buildpack exceptions + platform + project + app
// waivers, read by the image scan from the workspace.
func TestApprovedWaiversReachTheScanners(t *testing.T) {
	docs := docsOf(t, supplyChain+"74-scan-governance.yaml")
	policy := findDoc(t, docs, "Task", "scan-policy")
	script := stepScript(t, policy, "resolve")
	for _, want := range []string{
		"cm adhar-system scan-policy-global", `cm "$NS" scan-policy`,
		"cm adhar-system scan-waivers-global", `cm "$NS" scan-waivers`,
		`.data["trivyignore.yaml"]`, `KEY="trivyignore.app.${APP}.yaml"`, `.data["waivers.json"]`,
		"/etc/trivy-exceptions/ignore.yaml",
		`"$OUT/trivyignore.yaml"`,
		"{.status.namespace}", "{.spec.parameters.project}",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("scan-policy does not merge/read %q", want)
		}
	}
	if !strings.Contains(script, "*+") {
		t.Error("the ignore files are not merged with array append (*+); a later file would REPLACE the buildpack exceptions")
	}
	vols, _ := sgDig(policy, "spec", "volumes").([]any)
	mounted := false
	for _, v := range vols {
		if sgDig(v, "configMap", "name") == "trivy-buildpack-exceptions" {
			mounted = true
		}
	}
	if !mounted {
		t.Error("scan-policy does not mount trivy-buildpack-exceptions; merging the waivers would DROP them")
	}

	scan := findDoc(t, docsOf(t, supplyChain+"30-scan-verify-pipeline.yaml"), "Task", "trivy-scan")
	s := stepScript(t, scan, "scan")
	if !strings.Contains(s, `"$(workspaces.reports.path)/.git/adhar-scan/trivyignore.yaml"`) {
		t.Error("trivy-scan does not read the merged ignore file from the reports workspace")
	}
	ev := stepScript(t, scan, "evaluate")
	if !strings.Contains(ev, "-f /lib/adhar-scan/digest-waivers.jq") ||
		strings.Index(ev, "digest-waivers.jq") > strings.Index(ev, "evaluate.jq") {
		t.Error("trivy-scan must apply digest-scoped waivers (waivers.json) BEFORE judging; Trivy cannot scope an ignore entry to a digest")
	}
	if !strings.Contains(s, "--show-suppressed") {
		t.Error("trivy-scan drops waived findings from its report; the Console and the attestation must show them as waived")
	}
	if !strings.Contains(stepScript(t, findDoc(t, docsOf(t, supplyChain+"72-app-ci-stages.yaml"), "Task", "source-scan"), "deps"),
		"--ignorefile .git/adhar-scan/source-trivyignore.yaml") {
		t.Error("source-scan does not read the reviewed waivers")
	}
}

// The review: annotation-driven, by an identity code under review does not
// hold, failing closed on rejection and on timeout.
func TestTheScanReviewIsAnnotationDrivenAndFailsClosed(t *testing.T) {
	docs := docsOf(t, supplyChain+"74-scan-governance.yaml")
	script := stepScript(t, findDoc(t, docs, "Task", "scan-gate"), "gate")
	for _, want := range []string{
		"adhar.io/scan-approval=pending",
		`kubectl label --overwrite pipelinerun "$PR" -n adhar-system adhar.io/scan-approval=pending`,
		`approval() { kubectl get pipelinerun "$PR" -n adhar-system -o "jsonpath={.metadata.annotations.adhar\.io/scan-approval$1}"`,
		"approved|rejected)",
		`[ "$v" = approved ] && exit 0 || exit 1 ;;`,
		"publish expired expired",
		`if [ "$MODE" = warn ]; then`,
		"publish blocked; exit 1",
		"adhar.io/scan-summary=$SUMMARY",
		// the Console renders this annotation
		`annotate "adhar.io/scan-gate=$(state "$1")"`,
		`pass|approved) echo passed ;; warn) echo warned ;; pending) echo awaiting-approval ;; *) echo failed ;;`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("scan-gate no longer contains %q", want)
		}
	}
	// pending is written BEFORE polling, so a pre-set approval counts for nothing.
	if strings.Index(script, "adhar.io/scan-approval=pending") > strings.Index(script, "while [") {
		t.Error("scan-gate polls before it writes `pending`; an approval set at creation would pass unreviewed")
	}
	// Anything but an explicit `pass` verdict is a failure.
	if !strings.Contains(script, `[ "$(params.imageVerdict)" = pass ]  || failed=`) {
		t.Error("scan-gate must treat any verdict other than `pass` (including a missing one) as failed")
	}

	role := findDoc(t, docs, "Role", "adhar-scan-gate")
	patch := false
	for _, r := range sgDig(role, "rules").([]any) {
		if sgHas(strList(sgDig(r, "resources")), "pipelineruns") && sgHas(strList(sgDig(r, "verbs")), "patch") {
			patch = true
		}
	}
	if !patch {
		t.Error("adhar-scan-gate cannot patch PipelineRuns, so it cannot request a review")
	}
	rb := findDoc(t, docs, "RoleBinding", "adhar-scan-gate")
	if sgDig(rb, "subjects", 0, "name") != "adhar-scan-gate" {
		t.Error("the PipelineRun patch right must belong to adhar-scan-gate, not to adhar-pipeline")
	}
	for _, d := range docs {
		if subj := sgDig(d, "subjects", 0, "name"); subj == "adhar-pipeline" {
			t.Errorf("74-scan-governance.yaml grants %v to adhar-pipeline; the identity that runs repository code must not gain review rights", sgDig(d, "metadata", "name"))
		}
	}
}

// Who may approve: security reviewers by RBAC, and nobody below them by Kyverno.
func TestOnlyReviewersCanRelaxTheScanGate(t *testing.T) {
	docs := docsOf(t, supplyChain+"74-scan-governance.yaml")
	reviewer := findDoc(t, docs, "ClusterRole", "adhar-scan-reviewer")
	var cmNames []string
	prPatch := false
	for _, r := range sgDig(reviewer, "rules").([]any) {
		res, verbs := strList(sgDig(r, "resources")), strList(sgDig(r, "verbs"))
		if sgHas(res, "configmaps") && sgHas(verbs, "update") {
			cmNames = strList(sgDig(r, "resourceNames"))
		}
		if sgHas(res, "pipelineruns") && sgHas(verbs, "patch") {
			prPatch = true
		}
	}
	for _, n := range []string{"scan-policy", "scan-waivers", "scan-policy-global", "scan-waivers-global"} {
		if !sgHas(cmNames, n) {
			t.Errorf("adhar-scan-reviewer cannot update ConfigMap %s", n)
		}
	}
	if len(cmNames) != 4 {
		t.Errorf("adhar-scan-reviewer may update ConfigMaps %v; it must be exactly the four governance names", cmNames)
	}
	if !prPatch {
		t.Error("adhar-scan-reviewer cannot patch PipelineRuns, so a reviewer cannot answer a review")
	}

	rbac := docsOf(t, "security/keycloak/manifests/k8s-rbac.yaml")
	crb := findDoc(t, rbac, "ClusterRoleBinding", "adhar-oidc-security-reviewer")
	var groups []string
	for _, raw := range sgDig(crb, "subjects").([]any) {
		groups = append(groups, sgDig(raw, "name").(string))
	}
	if sgDig(crb, "roleRef", "name") != "adhar-scan-reviewer" || !sgHas(groups, "oidc:security-reviewer") || !sgHas(groups, "oidc:security-admin") {
		t.Errorf("adhar-oidc-security-reviewer binds %v; it must bind oidc:security-reviewer and oidc:security-admin to adhar-scan-reviewer", groups)
	}
	kc := readStackFile(t, "security/keycloak/manifests/keycloak-config.yaml")
	if !strings.Contains(kc, `{"name":"security-reviewer"}`) || !strings.Contains(kc, "for g in security-reviewer security-admin; do") {
		t.Error("the realm job does not ensure the security-reviewer group on existing realms")
	}

	kyv := docsOf(t, "application/adhar-supply-chain/kyverno/50-scan-governance.yaml")
	pol := findDoc(t, kyv, "ClusterPolicy", "scan-governance")
	if sgDig(pol, "spec", "validationFailureAction") != "Enforce" {
		t.Error("scan-governance must Enforce; an Audit guard lets a developer waive their own finding")
	}
	rules := sgDig(pol, "spec", "rules").([]any)
	names := strList(sgDig(rules[0], "match", "any", 0, "resources", "names"))
	if len(names) != 4 || !sgHas(names, "scan-waivers") || !sgHas(names, "scan-policy-global") {
		t.Errorf("scan-governance guards ConfigMaps %v; it must guard all four governance names", names)
	}
	body := readStackFile(t, "application/adhar-supply-chain/kyverno/50-scan-governance.yaml")
	if !strings.Contains(body, `{kind: Group, name: "oidc:security-reviewer"}`) || !strings.Contains(body, `{kind: Group, name: "oidc:security-admin"}`) ||
		strings.Contains(body, "oidc:platform-developer") {
		t.Error("scan-governance must exempt oidc:security-reviewer and must never exempt platform-developer")
	}
	if !strings.Contains(body, `adhar.io/scan-approval-by: "{{ request.userInfo.username }}"`) {
		t.Error("the reviewer is not stamped from the request; the approver would be whatever the client claimed")
	}
}

// What the scan found and what was waived travel with the image, signed with
// the platform key; the admission side reports on it and never blocks.
func TestScanReportsAreSignedAttestations(t *testing.T) {
	docs := docsOf(t, supplyChain+"74-scan-governance.yaml")
	attest := findDoc(t, docs, "Task", "scan-attest")
	var types []string
	for _, raw := range sgDig(attest, "spec", "steps").([]any) {
		args := strList(sgDig(raw, "args"))
		if len(args) == 0 {
			continue
		}
		if !sgHas(args, "--key=k8s://adhar-system/cosign-key") {
			t.Errorf("step %v attests without the platform key", sgDig(raw, "name"))
		}
		for _, a := range args {
			if strings.HasPrefix(a, "--type=") {
				types = append(types, strings.TrimPrefix(a, "--type="))
			}
		}
	}
	if !sgHas(types, "vuln") || !sgHas(types, "openvex") {
		t.Errorf("scan-attest attaches %v; want both the vuln report and the OpenVEX of applied waivers", types)
	}
	lib := findDoc(t, docs, "ConfigMap", "adhar-scan-lib")
	for _, k := range []string{"evaluate.jq", "failing.jq", "vuln-predicate.jq", "openvex.jq", "summary.jq"} {
		if s, _ := sgDig(lib, "data", k).(string); strings.TrimSpace(s) == "" {
			t.Errorf("adhar-scan-lib has no %s", k)
		}
	}
	if ex, _ := sgDig(lib, "data", "openvex.jq").(string); !strings.Contains(ex, `select((.status // "approved") == "approved")`) {
		t.Error("the OpenVEX statements must only cite APPROVED waiver records")
	}

	audit := findDoc(t, docsOf(t, "application/adhar-supply-chain/kyverno/45-verify-scan-attestation.yaml"), "ClusterPolicy", "verify-scan-attestation")
	if sgDig(audit, "spec", "validationFailureAction") != "Audit" {
		t.Error("verify-scan-attestation must be Audit: admission is the signature policy's decision")
	}
	if sgDig(audit, "spec", "rules", 0, "verifyImages", 0, "mutateDigest") != false {
		t.Error("verify-scan-attestation must not rewrite images; 40-verify-images.yaml owns that")
	}
}

// The Console treats CI as enforcing waivers once scan-policy-global exists;
// it is seeded with the original gate, only when absent, and is never an
// Argo-managed object a sync would revert.
func TestScanPolicyGlobalIsSeededWithTheDefaults(t *testing.T) {
	docs := docsOf(t, supplyChain+"74-scan-governance.yaml")
	for _, d := range docs {
		if d["kind"] == "ConfigMap" && sgDig(d, "metadata", "name") == "scan-policy-global" {
			t.Fatal("scan-policy-global is declared to Argo CD; every sync would revert a reviewer's edit")
		}
	}
	job := findDoc(t, docs, "Job", "seed-scan-policy-global")
	script, _ := sgDig(job, "spec", "template", "spec", "containers", 0, "args", 0).(string)
	for _, want := range []string{
		"if kubectl get configmap scan-policy-global -n adhar-system",
		"failOnSeverity: CRITICAL,HIGH", `ignoreUnfixed: "false"`, "scanners: vuln,misconfig",
		"mode: enforce", `manualApproval: "false"`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the scan-policy-global seed lacks %q", want)
		}
	}
	if !strings.Contains(readStackFile(t, "application/adhar-supply-chain/kyverno/50-scan-governance.yaml"),
		"{kind: ServiceAccount, name: scan-policy-seed, namespace: adhar-system}") {
		t.Error("scan-governance would refuse the seed Job's create")
	}
}
