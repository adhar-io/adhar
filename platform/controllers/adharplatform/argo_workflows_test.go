package adharplatform

// Argo Workflows is the platform's orchestration engine (docs/design/0026), and it
// was switched on in production only after it was made safe for a control plane
// that has run at 96% memory. Each guard below holds one of those decisions to the
// files that implement it (§15.1, the W0 rows), so the package cannot drift back to
// the v4.1.2 install it replaced: a preempting PriorityClass above the node-bound
// databases, full CRDs, no resources, SSO mapped to cluster-admin with TLS
// verification off, a PodMonitor scraping http against a TLS port, run objects
// tracked by Argo CD, and a default artifact repository whose Secret no team has.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/utils/files"
)

const argoPkg = "application/argo-workflows"

// argoPatchedFloor is the first release with GHSA-vm9v-p987-xm32 (artifact path
// traversal, CVSS 8.8) and GHSA-q65w-j2vp-47c4 (cross-namespace archive IDOR) fixed.
const argoPatchedFloor = "v4.1.4"

// The eight argoproj.io workflow CRDs the package owns.
var argoWorkflowCRDs = []string{
	"clusterworkflowtemplates.argoproj.io",
	"cronworkflows.argoproj.io",
	"workflowartifactgctasks.argoproj.io",
	"workfloweventbindings.argoproj.io",
	"workflows.argoproj.io",
	"workflowtaskresults.argoproj.io",
	"workflowtasksets.argoproj.io",
	"workflowtemplates.argoproj.io",
}

// argoStageData is what stageStack renders the package's .tmpl files with on a
// local platform that serves its own self-signed edge certificate.
const argoTestCert = "-----BEGIN CERTIFICATE-----\nMIIBszCCAVmgAwIBAgIUadhartest\n-----END CERTIFICATE-----\n"

func argoStageData() stackTemplateData {
	return stackTemplateData{BuildCustomizationSpec: v1alpha1.BuildCustomizationSpec{
		Host: "adhar.localtest.me", Port: "8443", PortSuffix: ":8443", SelfSignedCert: argoTestCert,
	}}
}

// argoDocs parses one file of the package into its objects, rendering a .tmpl
// exactly as the stack is staged.
func argoDocs(t *testing.T, rel string) []map[string]any {
	t.Helper()
	raw := []byte(readStackFile(t, argoPkg+"/"+rel))
	if strings.HasSuffix(rel, ".tmpl") {
		var err error
		if raw, err = files.ApplyTemplate(raw, argoStageData()); err != nil {
			t.Fatalf("rendering %s: %v", rel, err)
		}
	}
	return parseDocs(t, rel, string(raw))
}

func parseDocs(t *testing.T, where, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, chunk := range regexp.MustCompile(`(?m)^---\s*$`).Split(body, -1) {
		var doc map[string]any
		if err := yaml.Unmarshal([]byte(chunk), &doc); err != nil {
			t.Fatalf("%s: a document does not parse: %v", where, err)
		}
		if doc != nil {
			out = append(out, doc)
		}
	}
	return out
}

func argoFind(docs []map[string]any, kind, name string) map[string]any {
	for _, d := range docs {
		if d["kind"] == kind && dig(d, "metadata", "name") == name {
			return d
		}
	}
	return nil
}

func argoStr(v any) string {
	s, _ := v.(string)
	return s
}

func argoSemver(t *testing.T, v string) [3]int {
	t.Helper()
	m := regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`).FindStringSubmatch(v)
	if m == nil {
		t.Fatalf("%q is not a vX.Y.Z version", v)
	}
	var out [3]int
	for i := range out {
		out[i], _ = strconv.Atoi(m[i+1])
	}
	return out
}

func argoBelow(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// controllerConfig returns workflow-controller-configmap's data, each value parsed
// the way the controller parses it (YAML per key).
func controllerConfig(t *testing.T) map[string]any {
	t.Helper()
	cm := argoFind(argoDocs(t, "manifests/platform/workflow-controller-configmap.yaml.tmpl"),
		"ConfigMap", "workflow-controller-configmap")
	if cm == nil {
		t.Fatal("manifests/platform/workflow-controller-configmap.yaml.tmpl holds no workflow-controller-configmap")
	}
	data, _ := cm["data"].(map[string]any)
	out := map[string]any{}
	for k, v := range data {
		var parsed any
		if err := yaml.Unmarshal([]byte(argoStr(v)), &parsed); err != nil {
			t.Fatalf("workflow-controller-configmap key %s does not parse: %v", k, err)
		}
		out[k] = parsed
	}
	return out
}

// cfgMap is one parsed key of the controller configuration that must be a map.
func cfgMap(t *testing.T, cfg map[string]any, key string) map[string]any {
	t.Helper()
	m, ok := cfg[key].(map[string]any)
	if !ok {
		t.Fatalf("workflow-controller-configmap has no %s block (got %v)", key, cfg[key])
	}
	return m
}

func argoNum(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	}
	return 0
}

// ── §15.1: TestArgoWorkflowsIsPinnedToAPatchedRelease ───────────────────────────

// The generator, the contract, the generated files' headers and the images all
// name ONE release, at or above the patched floor (F-1).
func TestArgoWorkflowsIsPinnedToAPatchedRelease(t *testing.T) {
	gen := readStackFile(t, argoPkg+"/generate-manifests.sh")
	m := regexp.MustCompile(`(?m)^APP_VERSION="(v[0-9.]+)"`).FindStringSubmatch(gen)
	if m == nil {
		t.Fatal("generate-manifests.sh no longer sets APP_VERSION")
	}
	version := m[1]
	if argoBelow(argoSemver(t, version), argoSemver(t, argoPatchedFloor)) {
		t.Errorf("the generator pins %s, below %s — the release that fixed GHSA-vm9v-p987-xm32 (artifact path "+
			"traversal) and GHSA-q65w-j2vp-47c4 (cross-namespace archive IDOR)", version, argoPatchedFloor)
	}
	floor := regexp.MustCompile(`(?m)^MIN_VERSION="(v[0-9.]+)"`).FindStringSubmatch(gen)
	if floor == nil || argoBelow(argoSemver(t, floor[1]), argoSemver(t, argoPatchedFloor)) {
		t.Errorf("generate-manifests.sh must refuse anything below %s (MIN_VERSION), got %v", argoPatchedFloor, floor)
	}

	var contract struct {
		Version string `json:"version"`
	}
	if err := yaml.Unmarshal([]byte(readStackFile(t, argoPkg+"/adhar-package.yaml")), &contract); err != nil {
		t.Fatal(err)
	}
	if contract.Version != version {
		t.Errorf("adhar-package.yaml says %s, the generator vendors %s", contract.Version, version)
	}

	for _, f := range []string{"manifests/base/install.yaml", "manifests/base/crds-minimal.yaml", "manifests/base/kustomization.yaml"} {
		body := readStackFile(t, argoPkg+"/"+f)
		if !strings.HasPrefix(body, "# GENERATED by platform/stack/packages/application/argo-workflows/generate-manifests.sh") {
			t.Errorf("%s is not the generator's output (no GENERATED header): base/ is generated only", f)
		}
		if !strings.Contains(body, "\n# Argo Workflows "+version+" — ") {
			t.Errorf("%s was not generated from %s: re-run generate-manifests.sh", f, version)
		}
	}
	install := readStackFile(t, argoPkg+"/manifests/base/install.yaml")
	if !strings.Contains(install, "/releases/download/"+version+"/install.yaml") {
		t.Errorf("base/install.yaml does not name the %s release as its source", version)
	}
	if !strings.Contains(readStackFile(t, argoPkg+"/manifests/base/crds-minimal.yaml"), "/"+version+"/manifests/base/crds/minimal/") {
		t.Errorf("base/crds-minimal.yaml was not fetched at %s", version)
	}
	images := 0
	for _, img := range regexp.MustCompile(`image: (\S+)`).FindAllStringSubmatch(install, -1) {
		images++
		if !strings.HasSuffix(img[1], ":"+version) {
			t.Errorf("base/install.yaml runs %s, not %s", img[1], version)
		}
	}
	if images < 2 {
		t.Errorf("base/install.yaml should run the controller and argo-server, found %d images", images)
	}
}

// ── §15.1: TestArgoWorkflowsShipsMinimalCRDsThatSurviveDisable ──────────────────

// D-3: the minimal CRDs, ~0.3 MiB of schema instead of 4.84 MiB, on an API server
// that serves every CRD's schema from memory. INV-11: switching the package off
// keeps the CRDs (and so every team's templates, schedules and runs), the archive
// database and its credential, and the artifact bucket.
func TestArgoWorkflowsShipsMinimalCRDsThatSurviveDisable(t *testing.T) {
	crds := argoDocs(t, "manifests/base/crds-minimal.yaml")
	var names []string
	size := 0
	for _, crd := range crds {
		name := argoStr(dig(crd, "metadata", "name"))
		names = append(names, name)
		b, _ := json.Marshal(crd)
		size += len(b)
		ann, _ := dig(crd, "metadata", "annotations").(map[string]any)
		opts := argoStr(ann["argocd.argoproj.io/sync-options"])
		for _, want := range []string{"Delete=false", "Prune=false", "ServerSideApply=true"} {
			if !strings.Contains(opts, want) {
				t.Errorf("CRD %s lacks %s in its sync options (%q): switching the package off would delete every "+
					"team's workflow objects with it", name, want, opts)
			}
		}
		if argoStr(ann["argocd.argoproj.io/sync-wave"]) != "-10" {
			t.Errorf("CRD %s is not in wave -10, before every object that is an instance of it", name)
		}
	}
	sort.Strings(names)
	if strings.Join(names, ",") != strings.Join(argoWorkflowCRDs, ",") {
		t.Errorf("crds-minimal.yaml defines %v, want exactly %v", names, argoWorkflowCRDs)
	}
	// The minimal set leaves the four large kinds schemaless: their spec is one
	// x-kubernetes-preserve-unknown-fields. The full set describes every field.
	for _, crd := range crds {
		switch argoStr(dig(crd, "metadata", "name")) {
		case "workflows.argoproj.io", "workflowtemplates.argoproj.io", "clusterworkflowtemplates.argoproj.io", "cronworkflows.argoproj.io":
			versions, _ := dig(crd, "spec", "versions").([]any)
			for _, v := range versions {
				spec, _ := dig(v.(map[string]any), "schema", "openAPIV3Schema", "properties", "spec").(map[string]any)
				if spec["x-kubernetes-preserve-unknown-fields"] != true || spec["properties"] != nil {
					t.Errorf("%s carries a full spec schema: these are the minimal CRDs (D-3); the full ones cost the "+
						"API server ~4.8 MiB of schema — switch only after the §13.2 measurement", dig(crd, "metadata", "name"))
				}
			}
		}
	}
	if size > 512*1024 {
		t.Errorf("the workflow CRDs are %d KiB of schema; the minimal set is ~300 KiB", size/1024)
	}
	for _, d := range argoDocs(t, "manifests/base/install.yaml") {
		if d["kind"] == "CustomResourceDefinition" {
			t.Errorf("base/install.yaml still ships CRD %v: the CRDs come only from crds-minimal.yaml", dig(d, "metadata", "name"))
		}
	}

	for _, want := range []struct{ file, kind, name string }{
		{"manifests/platform/archive-db.yaml", "Database", "argo-workflows-db"},
		{"manifests/platform/archive-db.yaml", "ExternalSecret", "argo-workflows-db-credentials"},
		{"manifests/platform/artifacts.yaml", "Bucket", "argo-artifacts"},
	} {
		obj := argoFind(argoDocs(t, want.file), want.kind, want.name)
		if obj == nil {
			t.Errorf("%s no longer declares %s/%s", want.file, want.kind, want.name)
			continue
		}
		opts := argoStr(dig(obj, "metadata", "annotations", "argocd.argoproj.io/sync-options"))
		if !strings.Contains(opts, "Delete=false") || !strings.Contains(opts, "Prune=false") {
			t.Errorf("%s/%s must carry Delete=false,Prune=false (INV-11): disabling the engine must not take its "+
				"history with it — got %q", want.kind, want.name, opts)
		}
	}
}

// ── §15.1: TestNoStackPriorityClassPreemptsNodePinnedWorkloads ──────────────────

// The node-bound tier (CNPG, Valkey, ... — pods whose volume pins them to one node)
// and Keycloak's critical path may preempt; nothing else may, and nothing may ever
// outrank them. Upstream Argo Workflows shipped `workflow-controller` at 1,000,000
// with PreemptLowerPriority — above every database on the platform. Generalises
// TestPrometheusNeverPreemptsNodePinnedWorkloads to every PriorityClass.
func TestNoStackPriorityClassPreemptsNodePinnedWorkloads(t *testing.T) {
	protected := map[string]bool{"adhar-node-bound": true, "adhar-critical-path": true}
	// Classes that preempt only pods BELOW the protected tier, with the reason.
	lowerTier := map[string]string{
		"kpack-control-plane":       "upstream kpack: its controller and webhook above default-priority build pods",
		"kpack-build-high-priority": "upstream kpack: a rebuild ahead of the low-priority rebase queue",
	}
	floor := float64(-1)
	type pc struct {
		name, file string
		value      float64
		preempts   bool
	}
	var classes []pc
	for _, f := range stackPackageManifests(t) {
		for _, doc := range yamlDocs(t, f) {
			if doc["kind"] != "PriorityClass" {
				continue
			}
			c := pc{name: fmt.Sprint(metaName(doc)), file: rel(t, f), preempts: doc["preemptionPolicy"] != "Never"}
			switch v := doc["value"].(type) {
			case int:
				c.value = float64(v)
			case float64:
				c.value = v
			}
			classes = append(classes, c)
			if protected[c.name] && (floor < 0 || c.value < floor) {
				floor = c.value
			}
		}
	}
	if floor <= 0 {
		t.Fatal("found neither adhar-node-bound nor adhar-critical-path; the guard needs updating")
	}
	for _, c := range classes {
		if protected[c.name] || !c.preempts || c.value <= 0 {
			continue
		}
		if _, ok := lowerTier[c.name]; ok {
			if c.value >= floor {
				t.Errorf("%s: %s (%v) reaches the node-bound tier (%v) and preempts", c.file, c.name, c.value, floor)
			}
			continue
		}
		t.Errorf("%s: PriorityClass %s (value %v) preempts lower-priority pods. Only the node-bound tier may "+
			"(adhar-node-bound, adhar-critical-path); drop it in the package generator, or set preemptionPolicy: "+
			"Never", c.file, c.name, c.value)
	}
	for _, d := range argoDocs(t, "manifests/base/install.yaml") {
		if pcn := dig(d, "spec", "template", "spec", "priorityClassName"); pcn != nil {
			t.Errorf("base/install.yaml: %s/%v still sets priorityClassName %v", d["kind"], dig(d, "metadata", "name"), pcn)
		}
	}
}

// ── §15.1: TestArgoWorkflowsContainersAreSizedAndHaveNoServiceLinks ─────────────

// Every container the engine runs has requests and a memory limit, and no pod
// gets the ~290 Services of adhar-system injected as environment variables.
func TestArgoWorkflowsContainersAreSizedAndHaveNoServiceLinks(t *testing.T) {
	for _, p := range []struct{ file, deployment, container string }{
		{"manifests/platform/controller-patch.yaml", "workflow-controller", "workflow-controller"},
		{"manifests/platform/server-patch.yaml", "argo-server", "argo-server"},
	} {
		dep := argoFind(argoDocs(t, p.file), "Deployment", p.deployment)
		if dep == nil {
			t.Errorf("%s does not patch Deployment/%s", p.file, p.deployment)
			continue
		}
		spec, _ := dep["spec"].(map[string]any)
		if dig(spec, "template", "spec", "enableServiceLinks") != false {
			t.Errorf("%s: %s does not set enableServiceLinks: false", p.file, p.deployment)
		}
		var c map[string]any
		for _, x := range containersOf(spec) {
			if x["name"] == p.container {
				c = x
			}
		}
		if c == nil {
			t.Errorf("%s: no container %s (the patch must name upstream's container to merge into it)", p.file, p.container)
			continue
		}
		for _, path := range [][]string{{"requests", "cpu"}, {"requests", "memory"}, {"limits", "memory"}} {
			if argoStr(dig(c, append([]string{"resources"}, path...)...)) == "" {
				t.Errorf("%s: %s has no resources.%s", p.file, p.container, strings.Join(path, "."))
			}
		}
	}
	kust := readStackFile(t, argoPkg+"/manifests/platform/kustomization.yaml")
	for _, patch := range []string{"controller-patch.yaml", "server-patch.yaml", "workflow-controller-configmap.yaml"} {
		if !strings.Contains(withoutComments(kust), "path: "+patch) {
			t.Errorf("manifests/platform/kustomization.yaml does not apply %s", patch)
		}
	}

	cfg := controllerConfig(t)
	patch := argoStr(dig(cfgMap(t, cfg, "workflowDefaults"), "spec", "podSpecPatch"))
	var pod map[string]any
	if err := json.Unmarshal([]byte(patch), &pod); err != nil || pod["enableServiceLinks"] != false {
		t.Errorf("workflowDefaults.spec.podSpecPatch = %q: every step pod must set enableServiceLinks false", patch)
	}
	executor := cfgMap(t, cfg, "executor")
	for _, path := range [][]string{{"requests", "cpu"}, {"requests", "memory"}, {"limits", "memory"}} {
		if argoStr(dig(executor, append([]string{"resources"}, path...)...)) == "" {
			t.Errorf("the executor (init/wait on every step pod) has no resources.%s: the team LimitRange "+
				"would charge 100m/128Mi..500m/512Mi per sidecar against the team's quota", strings.Join(path, "."))
		}
	}
}

// ── §15.1: TestArgoWorkflowDefaultsBoundEveryRun ───────────────────────────────

// INV-5: every run is bounded — a TTL, pod clean-up, a deadline and retention —
// and parallelism is capped per controller and per namespace. Also renders the
// template both ways the stack stages it (with and without a self-signed edge
// certificate) so the SSO trust root stays well-formed YAML.
func TestArgoWorkflowDefaultsBoundEveryRun(t *testing.T) {
	cfg := controllerConfig(t)
	if p := argoNum(cfg["parallelism"]); p <= 0 {
		t.Errorf("parallelism = %v: the cluster-wide cap on running workflows must be set", cfg["parallelism"])
	} else if np := argoNum(cfg["namespaceParallelism"]); np <= 0 || np > p {
		t.Errorf("namespaceParallelism = %v: it must be set, and no larger than parallelism (%v)", cfg["namespaceParallelism"], p)
	}
	if rl, _ := cfg["resourceRateLimit"].(map[string]any); argoNum(rl["limit"]) <= 0 || argoNum(rl["burst"]) <= 0 {
		t.Errorf("resourceRateLimit = %v: pod creations per second must be capped", cfg["resourceRateLimit"])
	}
	spec, _ := dig(cfgMap(t, cfg, "workflowDefaults"), "spec").(map[string]any)
	if spec["serviceAccountName"] != "adhar-workflow" {
		t.Errorf("workflowDefaults.spec.serviceAccountName = %v, want adhar-workflow: a workflow that names no "+
			"identity must run as the tenant executor, and fail closed where it does not exist", spec["serviceAccountName"])
	}
	if d := argoNum(spec["activeDeadlineSeconds"]); d <= 0 || d > 86400 {
		t.Errorf("workflowDefaults.spec.activeDeadlineSeconds = %v, want a deadline of at most a day", spec["activeDeadlineSeconds"])
	}
	if argoNum(spec["parallelism"]) <= 0 {
		t.Error("workflowDefaults.spec.parallelism is not set: one workflow could start unbounded pods")
	}
	for _, k := range []string{"secondsAfterSuccess", "secondsAfterFailure"} {
		if argoNum(dig(spec, "ttlStrategy", k)) <= 0 {
			t.Errorf("workflowDefaults.spec.ttlStrategy.%s is not set: finished Workflows would stay in etcd until "+
				"the retention backstop", k)
		}
	}
	if argoStr(dig(spec, "podGC", "strategy")) == "" {
		t.Error("workflowDefaults.spec.podGC.strategy is not set: every step pod would outlive its workflow")
	}
	rp, _ := cfg["retentionPolicy"].(map[string]any)
	for _, k := range []string{"completed", "failed", "errored"} {
		if argoNum(rp[k]) <= 0 {
			t.Errorf("retentionPolicy.%s is not set", k)
		}
	}
	persistence, _ := cfg["persistence"].(map[string]any)
	if persistence["archive"] != true || argoStr(persistence["archiveTTL"]) == "" || persistence["nodeStatusOffLoad"] != false {
		t.Errorf("persistence = %v: history lives in the archive (archive: true, an archiveTTL), node status on the "+
			"object (nodeStatusOffLoad: false, D-9)", persistence)
	}
	if ev, _ := cfg["nodeEvents"].(map[string]any); ev["enabled"] != false {
		t.Error("nodeEvents must stay off: Events are the most numerous object kind on this API server")
	}

	// The SSO trust root renders as a block scalar inside the sso key, or not at all.
	if got := argoStr(dig(cfgMap(t, cfg, "sso"), "rootCA")); got != argoTestCert {
		t.Errorf("with a self-signed edge certificate the sso rootCA renders as %q", got)
	}
	data := argoStageData()
	data.SelfSignedCert = ""
	raw, err := files.ApplyTemplate([]byte(readStackFile(t, argoPkg+"/manifests/platform/workflow-controller-configmap.yaml.tmpl")), data)
	if err != nil {
		t.Fatal(err)
	}
	cm := argoFind(parseDocs(t, "workflow-controller-configmap (no certificate)", string(raw)), "ConfigMap", "workflow-controller-configmap")
	var sso map[string]any
	if err := yaml.Unmarshal([]byte(argoStr(dig(cm, "data", "sso"))), &sso); err != nil || sso["rootCA"] != nil || sso["issuer"] == nil {
		t.Errorf("without a self-signed certificate the sso block must parse and carry no rootCA (%v, %v)", sso, err)
	}
}

// The controller's workflowDefaults are merged under every workflow with a
// strategic merge, which keeps any field the workflow leaves out — and the TTL
// collector prefers secondsAfterSuccess/Failure over secondsAfterCompletion. So a
// workflow that names only secondsAfterCompletion silently gets the platform's
// one and three days instead of its own value.
func TestExplicitWorkflowTTLsSurviveTheDefaults(t *testing.T) {
	checked := 0
	for _, f := range stackPackageManifests(t) {
		if strings.Contains(f, string(filepath.Separator)+"policy-tests"+string(filepath.Separator)) {
			continue
		}
		for _, doc := range yamlDocs(t, f) {
			var spec map[string]any
			switch doc["kind"] {
			case "Workflow", "WorkflowTemplate", "ClusterWorkflowTemplate":
				spec, _ = doc["spec"].(map[string]any)
			case "CronWorkflow":
				spec, _ = dig(doc, "spec", "workflowSpec").(map[string]any)
			default:
				continue
			}
			if !strings.HasPrefix(fmt.Sprint(doc["apiVersion"]), "argoproj.io/") {
				continue
			}
			ttl, _ := spec["ttlStrategy"].(map[string]any)
			if ttl == nil || ttl["secondsAfterCompletion"] == nil {
				continue
			}
			checked++
			for _, k := range []string{"secondsAfterSuccess", "secondsAfterFailure"} {
				if ttl[k] == nil {
					t.Errorf("%s: %s/%v sets ttlStrategy.secondsAfterCompletion without %s; the controller's "+
						"workflowDefaults supply it, and it wins", rel(t, f), doc["kind"], metaName(doc), k)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("found no workflow with a ttlStrategy; the platform tasks set one each — the walker is broken")
	}
}

// ── §15.1: TestArgoMetricsScrapeMatchesTheControllerScheme ─────────────────────

// Upstream defaults metricsConfig.secure to TRUE; the PodMonitor this replaced
// scraped http against that TLS port and read `up == 0` for as long as it existed.
func TestArgoMetricsScrapeMatchesTheControllerScheme(t *testing.T) {
	mc := cfgMap(t, controllerConfig(t), "metricsConfig")
	if mc["enabled"] != true {
		t.Fatalf("metricsConfig.enabled = %v", mc["enabled"])
	}
	wantScheme := "https"
	if mc["secure"] == false {
		wantScheme = "http"
	}
	port := int(argoNum(mc["port"]))
	path := argoStr(mc["path"])

	pm := argoFind(argoDocs(t, "manifests/platform/metrics.yaml"), "PodMonitor", "argo-workflows-controller")
	if pm == nil {
		t.Fatal("manifests/platform/metrics.yaml declares no PodMonitor argo-workflows-controller")
	}
	if dig(pm, "spec", "selector", "matchLabels", "app") != "workflow-controller" {
		t.Errorf("the PodMonitor does not select the controller's pods (app: workflow-controller)")
	}
	endpoints, _ := dig(pm, "spec", "podMetricsEndpoints").([]any)
	if len(endpoints) != 1 {
		t.Fatalf("want one controller metrics endpoint, got %d", len(endpoints))
	}
	ep := endpoints[0].(map[string]any)
	if ep["scheme"] != wantScheme {
		t.Errorf("the PodMonitor scrapes %v but the controller serves %s (metricsConfig.secure = %v): up would read 0",
			ep["scheme"], wantScheme, mc["secure"])
	}
	if argoStr(ep["path"]) != path {
		t.Errorf("the PodMonitor scrapes %v, the controller serves %s", ep["path"], path)
	}
	// The named port the PodMonitor scrapes is the controller's metrics port.
	found := false
	for _, d := range argoDocs(t, "manifests/base/install.yaml") {
		if d["kind"] != "Deployment" || dig(d, "metadata", "name") != "workflow-controller" {
			continue
		}
		spec, _ := d["spec"].(map[string]any)
		for _, c := range containersOf(spec) {
			ports, _ := c["ports"].([]any)
			for _, p := range ports {
				pm := p.(map[string]any)
				if pm["name"] == ep["port"] {
					found = true
					if int(argoNum(pm["containerPort"])) != port {
						t.Errorf("container port %v is %v, metricsConfig.port is %d", pm["name"], pm["containerPort"], port)
					}
				}
			}
		}
	}
	if !found {
		t.Errorf("the controller has no container port named %v", ep["port"])
	}
}

// ── §15.1: TestArgoSSOMapsNoGroupToClusterAdmin ─────────────────────────────────

// argo-server makes every API call of an SSO user with the token of the
// ServiceAccount the user's groups map to. Those accounts hold Argo objects and
// pods, nothing else: the platform-admin mapping used to be bound to cluster-admin
// through a long-lived token, which made the native UI a cluster-admin console.
func TestArgoSSOMapsNoGroupToClusterAdmin(t *testing.T) {
	var docs []map[string]any
	dir := filepath.Join(stackPackagesDir(t), argoPkg, "manifests")
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !(strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".yaml.tmpl")) {
			return nil
		}
		relp, _ := filepath.Rel(filepath.Join(stackPackagesDir(t), argoPkg), p)
		docs = append(docs, argoDocs(t, filepath.ToSlash(relp))...)
		return nil
	})
	roles := map[string]map[string]any{} // kind/name -> role
	for _, d := range docs {
		if d["kind"] == "ClusterRole" || d["kind"] == "Role" {
			roles[fmt.Sprint(d["kind"], "/", dig(d, "metadata", "name"))] = d
		}
	}
	ssoAccounts := map[string]bool{}
	for _, d := range docs {
		if d["kind"] != "ServiceAccount" {
			continue
		}
		rule := argoStr(dig(d, "metadata", "annotations", "workflows.argoproj.io/rbac-rule"))
		if rule == "" {
			continue
		}
		ssoAccounts[argoStr(dig(d, "metadata", "name"))] = true
		for _, g := range regexp.MustCompile(`'([^']+)' in groups`).FindAllStringSubmatch(rule, -1) {
			if !strings.HasPrefix(g[1], "platform-") {
				t.Errorf("SSO rule %q maps group %q: the native UI is for platform staff; teams use the console (D-11)", rule, g[1])
			}
		}
	}
	if !ssoAccounts["argo-sso-platform"] || !ssoAccounts["argo-sso-viewer"] {
		t.Fatalf("expected the SSO accounts argo-sso-platform and argo-sso-viewer, found %v", ssoAccounts)
	}
	forbidden := map[string]bool{"cluster-admin": true, "admin": true, "edit": true}
	bound := 0
	for _, d := range docs {
		if d["kind"] != "ClusterRoleBinding" && d["kind"] != "RoleBinding" {
			continue
		}
		subjects, _ := d["subjects"].([]any)
		for _, s := range subjects {
			sm := s.(map[string]any)
			if sm["kind"] != "ServiceAccount" || !ssoAccounts[argoStr(sm["name"])] {
				continue
			}
			bound++
			ref := argoStr(dig(d, "roleRef", "name"))
			refKind := argoStr(dig(d, "roleRef", "kind"))
			if forbidden[ref] {
				t.Errorf("%s/%v binds SSO account %v to %s", d["kind"], dig(d, "metadata", "name"), sm["name"], ref)
				continue
			}
			role := roles[refKind+"/"+ref]
			if role == nil {
				t.Errorf("%s/%v binds %v to %s/%s, which this package does not define — the guard cannot see what it grants",
					d["kind"], dig(d, "metadata", "name"), sm["name"], refKind, ref)
				continue
			}
			rules, _ := role["rules"].([]any)
			for _, r := range rules {
				rm := r.(map[string]any)
				groups, _ := rm["apiGroups"].([]any)
				resources, _ := rm["resources"].([]any)
				verbs, _ := rm["verbs"].([]any)
				for _, x := range append(append(append([]any{}, groups...), resources...), verbs...) {
					if x == "*" {
						t.Errorf("%s/%s grants a wildcard to SSO account %v", refKind, ref, sm["name"])
					}
				}
				for _, g := range groups {
					if g != "argoproj.io" && g != "" {
						t.Errorf("%s/%s grants API group %q to SSO account %v: Argo objects and pods only", refKind, ref, g, sm["name"])
					}
				}
				for _, res := range resources {
					switch res {
					case "pods", "pods/log", "events":
					case "secrets":
						if names, _ := rm["resourceNames"].([]any); len(names) == 0 {
							t.Errorf("%s/%s lets SSO account %v read every Secret", refKind, ref, sm["name"])
						}
					default:
						if groups == nil || groups[0] != "argoproj.io" {
							t.Errorf("%s/%s grants %v to SSO account %v", refKind, ref, res, sm["name"])
						}
					}
				}
			}
		}
	}
	if bound < 2 {
		t.Errorf("found %d bindings for the SSO accounts; each needs its Argo-scoped role", bound)
	}
	for _, d := range docs {
		if d["kind"] == "Secret" && d["type"] == "kubernetes.io/service-account-token" {
			sa := argoStr(dig(d, "metadata", "annotations", "kubernetes.io/service-account.name"))
			if !ssoAccounts[sa] {
				t.Errorf("Secret %v mints a long-lived token for %s, which is not an SSO account", dig(d, "metadata", "name"), sa)
			}
		}
	}
}

// ── §15.1: TestArgoSSONeverSkipsTLSVerification ────────────────────────────────

// The dev overlay this package replaced set `insecureSkipVerify: true` on the SSO
// client. A self-signed edge is handled with a trust root, never by turning off
// verification of the identity provider.
func TestArgoSSONeverSkipsTLSVerification(t *testing.T) {
	sso := cfgMap(t, controllerConfig(t), "sso")
	if sso["insecureSkipVerify"] != nil && sso["insecureSkipVerify"] != false {
		t.Errorf("sso.insecureSkipVerify = %v", sso["insecureSkipVerify"])
	}
	for _, k := range []string{"issuer", "redirectUrl"} {
		if !strings.HasPrefix(argoStr(sso[k]), "https://") {
			t.Errorf("sso.%s = %v: the identity provider is reached over TLS only", k, sso[k])
		}
	}
	if argoStr(dig(sso, "clientSecret", "name")) != "argo-workflows-sso" {
		t.Errorf("sso.clientSecret reads %v, want the package's own argo-workflows-sso Secret", dig(sso, "clientSecret", "name"))
	}
	dir := filepath.Join(stackPackagesDir(t), argoPkg, "manifests")
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		if regexp.MustCompile(`(?mi)^\s*[^#\n]*insecureSkipVerify:\s*true`).Match(b) {
			t.Errorf("%s sets insecureSkipVerify: true", p)
		}
		return nil
	})
}

// ── §15.1: TestArgoCDDoesNotTrackArgoWorkflowRuns ───────────────────────────────

// D-10: Workflow, WorkflowTaskResult, WorkflowTaskSet and WorkflowArtifactGCTask
// are runtime state. Tracked, every node transition rewrites a cached object and
// every step pod adds one, on the API server the platform cannot spare. Pinned in
// the generator's values AND the generated manifests (a regenerate is how a
// hand-edit gets lost). The definitions — templates, schedules, event bindings —
// must stay tracked, and each has a health check so a wave holding one completes.
func TestArgoCDDoesNotTrackArgoWorkflowRuns(t *testing.T) {
	runs := []string{"Workflow", "WorkflowTaskResult", "WorkflowTaskSet", "WorkflowArtifactGCTask"}
	definitions := map[string]bool{"WorkflowTemplate": true, "ClusterWorkflowTemplate": true, "CronWorkflow": true, "WorkflowEventBinding": true}
	health := []string{
		"argoproj.io_WorkflowTemplate", "argoproj.io_ClusterWorkflowTemplate", "argoproj.io_WorkflowEventBinding",
		"platform.adhar.io_Bucket",
	}
	for _, file := range []string{
		"resources/argocd/install.yaml",
		"resources/argocd/install-ha.yaml",
		"../../../hack/argocd/values.yaml",
		"../../../hack/argocd/values-ha.yaml",
	} {
		cm := argocdCMData(t, file)
		var exclusions []struct {
			APIGroups []string `json:"apiGroups"`
			Kinds     []string `json:"kinds"`
		}
		if err := yaml.Unmarshal([]byte(cm["resource.exclusions"]), &exclusions); err != nil {
			t.Errorf("%s: resource.exclusions does not parse: %v", file, err)
			continue
		}
		excluded := map[string]bool{}
		for _, e := range exclusions {
			for _, g := range e.APIGroups {
				if g != "argoproj.io" && g != "*" {
					continue
				}
				for _, k := range e.Kinds {
					excluded[k] = true
				}
			}
		}
		for _, k := range runs {
			if !excluded[k] {
				t.Errorf("%s: resource.exclusions does not exclude argoproj.io/%s", file, k)
			}
		}
		for k := range excluded {
			if definitions[k] || k == "*" {
				t.Errorf("%s: resource.exclusions hides argoproj.io/%s — a definition Argo CD must sync", file, k)
			}
		}
		for _, kind := range health {
			body := cm["resource.customizations.health."+kind]
			if body == "" {
				t.Errorf("%s: no health check for %s; Argo CD would hold its sync wave forever", file, kind)
			}
			if kind == "platform.adhar.io_Bucket" && (!strings.Contains(body, `"Ready"`) || !strings.Contains(body, `"Degraded"`)) {
				t.Errorf("%s: the Bucket check must read the composite's Ready condition and surface failure", file)
			}
		}
	}
	// The consequence of the exclusion: a Workflow in Git would never be applied
	// or reconciled. Ship a WorkflowTemplate (or a CronWorkflow) and run it.
	for _, f := range stackPackageManifests(t) {
		if strings.Contains(f, string(filepath.Separator)+"policy-tests"+string(filepath.Separator)) {
			continue
		}
		for _, doc := range yamlDocs(t, f) {
			if doc["kind"] == "Workflow" && strings.HasPrefix(fmt.Sprint(doc["apiVersion"]), "argoproj.io/") {
				t.Errorf("%s ships Workflow %v through Git; Argo CD excludes Workflows (D-10) — ship a WorkflowTemplate",
					rel(t, f), metaName(doc))
			}
		}
	}
}

// argocdCMData reads argocd-cm's data from a generated install manifest, or the
// configs.cm block of a generator values file.
func argocdCMData(t *testing.T, file string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	src := regexp.MustCompile(`\{\{[^}]*\}\}`).ReplaceAllString(string(b), "templated")
	out := map[string]string{}
	if strings.Contains(file, "values") {
		var v struct {
			Configs struct {
				CM map[string]any `json:"cm"`
			} `json:"configs"`
		}
		if err := yaml.Unmarshal([]byte(src), &v); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		for k, x := range v.Configs.CM {
			out[k] = fmt.Sprint(x)
		}
		return out
	}
	for _, chunk := range regexp.MustCompile(`(?m)^---\s*$`).Split(src, -1) {
		if !strings.Contains(chunk, "name: argocd-cm\n") {
			continue
		}
		var cm struct {
			Kind     string                `json:"kind"`
			Metadata struct{ Name string } `json:"metadata"`
			Data     map[string]string     `json:"data"`
		}
		if yaml.Unmarshal([]byte(chunk), &cm) == nil && cm.Kind == "ConfigMap" && cm.Metadata.Name == "argocd-cm" {
			return cm.Data
		}
	}
	t.Fatalf("%s: no argocd-cm ConfigMap", file)
	return nil
}

// ── §15.1: TestArgoWorkflowsHasNoControllerWideArtifactRepository ──────────────

// D-12: a controller-wide artifactRepository is inherited by every namespace,
// and its credential Secret exists only in adhar-system — a team step that wrote
// an artifact would fail on a missing Secret, or, worse, the repository would one
// day be given credentials every team could use. adhar-system has its own
// repository through the namespace-level ConfigMap; teams get theirs with scoped
// credentials (W5).
func TestArgoWorkflowsHasNoControllerWideArtifactRepository(t *testing.T) {
	cfg := controllerConfig(t)
	if cfg["artifactRepository"] != nil {
		t.Error("workflow-controller-configmap sets a controller-wide artifactRepository (D-12)")
	}
	if c, ok := cfg["config"].(map[string]any); ok && c["artifactRepository"] != nil {
		t.Error("workflow-controller-configmap's `config` key sets a controller-wide artifactRepository (D-12)")
	}
	repos := argoFind(argoDocs(t, "manifests/platform/artifacts.yaml"), "ConfigMap", "artifact-repositories")
	if repos == nil {
		t.Fatal("artifacts.yaml no longer declares adhar-system's artifact-repositories ConfigMap")
	}
	if dig(repos, "metadata", "namespace") != "adhar-system" {
		t.Errorf("artifact-repositories lives in %v; it is adhar-system's alone", dig(repos, "metadata", "namespace"))
	}
	def := argoStr(dig(repos, "metadata", "annotations", "workflows.argoproj.io/default-artifact-repository"))
	body := argoStr(dig(repos, "data", def))
	if body == "" {
		t.Fatalf("the default artifact repository %q names no key of the ConfigMap", def)
	}
	var repo map[string]any
	if err := yaml.Unmarshal([]byte(body), &repo); err != nil {
		t.Fatal(err)
	}
	if repo["archiveLogs"] != false {
		t.Error("archiveLogs must stay off: step logs come from Loki, and archiving them makes every step depend on the bucket")
	}
}

// ── §15.1: TestOnlyOneWorkflowControllerCanExist ────────────────────────────────

// INV-1. A ChaosCenter chaos infrastructure brings Argo Workflows v3.3.1 under the
// upstream names; policy adhar-argo-single-controller refuses its images and every
// write to the controller, its ConfigMap or the CRDs that is not Argo CD's. `make
// policy-test` proves the behaviour; this holds the policy to the names the package
// actually ships, and to the settings the kyverno CLI cannot demonstrate.
func TestOnlyOneWorkflowControllerCanExist(t *testing.T) {
	docs := argoDocs(t, "manifests/platform/policies.yaml")
	pol := argoFind(docs, "ClusterPolicy", "adhar-argo-single-controller")
	if pol == nil {
		t.Fatal("policies.yaml no longer declares adhar-argo-single-controller")
	}
	rules := map[string]map[string]any{}
	list, _ := dig(pol, "spec", "rules").([]any)
	for _, r := range list {
		rm := r.(map[string]any)
		rules[argoStr(rm["name"])] = rm
	}
	litmus := []string{"*litmuschaos/workflow-controller*", "*litmuschaos/argoexec*", "*litmusportal-subscriber*", "*litmusportal-event-tracker*"}
	for name, wantLists := range map[string][]string{
		"refuse-litmus-chaos-infrastructure-images": {"request.object.spec.containers", "request.object.spec.initContainers"},
		"refuse-litmus-chaos-infrastructure-workloads": {
			"request.object.spec.template.spec.containers", "request.object.spec.template.spec.initContainers",
			"request.object.spec.jobTemplate.spec.template.spec.containers"},
	} {
		r := rules[name]
		if r == nil {
			t.Errorf("rule %s is missing", name)
			continue
		}
		foreach, _ := dig(r, "validate", "foreach").([]any)
		lists := map[string]bool{}
		for _, f := range foreach {
			fm := f.(map[string]any)
			lists[argoStr(fm["list"])] = true
			raw, _ := json.Marshal(fm["deny"])
			for _, img := range litmus {
				if !strings.Contains(string(raw), img) {
					t.Errorf("%s: the %v entry does not refuse %s", name, fm["list"], img)
				}
			}
		}
		for _, l := range wantLists {
			if !lists[l] {
				t.Errorf("%s does not check %s", name, l)
			}
		}
	}
	if kinds, _ := json.Marshal(dig(rules["refuse-litmus-chaos-infrastructure-workloads"], "match")); !strings.Contains(string(kinds), "Deployment") || !strings.Contains(string(kinds), "CronJob") {
		t.Error("the workload rule must match the controllers that create pods (Deployment ... CronJob)")
	}

	var crdNames []string
	for _, d := range argoDocs(t, "manifests/base/crds-minimal.yaml") {
		crdNames = append(crdNames, argoStr(dig(d, "metadata", "name")))
	}
	sort.Strings(crdNames)
	for name, want := range map[string][]string{
		"only-argocd-writes-the-controller":    {"workflow-controller", "workflow-controller-configmap"},
		"only-argocd-writes-the-workflow-crds": crdNames,
	} {
		r := rules[name]
		if r == nil {
			t.Errorf("rule %s is missing", name)
			continue
		}
		var got []string
		matches, _ := dig(r, "match", "any").([]any)
		for _, a := range matches {
			names, _ := dig(a.(map[string]any), "resources", "names").([]any)
			for _, n := range names {
				got = append(got, argoStr(n))
			}
		}
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s guards %v, want %v", name, got, want)
		}
		if dig(r, "validate", "allowExistingViolations") != false {
			t.Errorf("%s must set allowExistingViolations: false, or Kyverno skips every UPDATE to an object "+
				"that already exists (all of them)", name)
		}
		excl, _ := json.Marshal(dig(r, "exclude"))
		if !strings.Contains(string(excl), `"name":"argocd-application-controller"`) || strings.Count(string(excl), `"kind":`) != 1 {
			t.Errorf("%s must exclude Argo CD's application controller, and only it: %s", name, excl)
		}
	}
	if ops, _ := json.Marshal(dig(rules["only-argocd-writes-the-workflow-crds"], "match")); !strings.Contains(string(ops), "DELETE") {
		t.Error("deleting the workflow CRDs deletes every team's templates and runs; the CRD rule must refuse DELETE")
	}
	for name, r := range rules {
		if dig(r, "validate", "failureAction") != "Enforce" {
			t.Errorf("%s is not Enforce", name)
		}
	}

	cwt := argoFind(docs, "ClusterPolicy", "adhar-cwt-platform-only")
	if cwt == nil {
		t.Fatal("policies.yaml no longer declares adhar-cwt-platform-only")
	}
	r := dig(cwt, "spec", "rules").([]any)[0].(map[string]any)
	m, _ := json.Marshal(r["match"])
	if !strings.Contains(string(m), "ClusterWorkflowTemplate") || !strings.Contains(string(m), "DELETE") {
		t.Errorf("adhar-cwt-platform-only must refuse create, update and delete of ClusterWorkflowTemplates: %s", m)
	}
	if dig(r, "validate", "allowExistingViolations") != false || dig(r, "validate", "failureAction") != "Enforce" {
		t.Error("adhar-cwt-platform-only must Enforce with allowExistingViolations: false")
	}
}

// ── §15.1: TestArgoPoliciesAddNoPolicyReports ───────────────────────────────────

// Admission-only: a background scan writes a PolicyReport per matched object, and
// this API server already holds ~1,200 of them. Covers the engine's policies and
// every Argo or game-day policy elsewhere in the stack, by name.
func TestArgoPoliciesAddNoPolicyReports(t *testing.T) {
	prefixes := []string{"adhar-argo-", "adhar-workflow", "adhar-cwt-", "adhar-gameday", "adhar-chaosengine"}
	found := 0
	for _, f := range stackPackageManifests(t) {
		inPkg := strings.Contains(f, string(filepath.Separator)+"argo-workflows"+string(filepath.Separator)+"manifests"+string(filepath.Separator))
		for _, doc := range yamlDocs(t, f) {
			if doc["kind"] != "ClusterPolicy" && doc["kind"] != "Policy" {
				continue
			}
			name := fmt.Sprint(metaName(doc))
			ours := inPkg
			for _, p := range prefixes {
				ours = ours || strings.HasPrefix(name, p)
			}
			if !ours {
				continue
			}
			found++
			if bg := dig(doc, "spec", "background"); bg != false {
				t.Errorf("%s: %s %s has background %v; workflow and game-day policies are admission-only", rel(t, f), doc["kind"], name, bg)
			}
		}
	}
	if found < 2 {
		t.Fatalf("found %d Argo policies; the engine ships at least two", found)
	}
}

// ── Ordering: the controller starts only after its archive ────────────────────

// "If migration fails, the controller does not start": the archive database and
// its credential sync in earlier waves than the controller, the SSO credential
// before argo-server (which does not start without it), the route after the
// server, and the CRDs before anything that is an instance of them.
func TestArgoWorkflowsStartsAfterItsArchive(t *testing.T) {
	wave := func(file, kind, name string) int {
		t.Helper()
		d := argoFind(argoDocs(t, file), kind, name)
		if d == nil {
			t.Fatalf("%s: no %s/%s", file, kind, name)
		}
		w, _ := strconv.Atoi(argoStr(dig(d, "metadata", "annotations", "argocd.argoproj.io/sync-wave")))
		return w
	}
	order := []struct {
		what string
		w    int
	}{
		{"the workflow CRDs", wave("manifests/base/crds-minimal.yaml", "CustomResourceDefinition", "workflows.argoproj.io")},
		{"the archive credential", wave("manifests/platform/archive-db.yaml", "ExternalSecret", "argo-workflows-db-credentials")},
		{"the archive Database", wave("manifests/platform/archive-db.yaml", "Database", "argo-workflows-db")},
		{"workflow-controller", wave("manifests/platform/controller-patch.yaml", "Deployment", "workflow-controller")},
		{"the SSO credential", wave("manifests/platform/sso.yaml", "ExternalSecret", "argo-workflows-sso")},
		{"argo-server", wave("manifests/platform/server-patch.yaml", "Deployment", "argo-server")},
		{"the HTTPRoute", wave("manifests/platform/httproute.yaml.tmpl", "HTTPRoute", "argo-workflows")},
	}
	for i := 1; i < len(order); i++ {
		if order[i-1].w >= order[i].w {
			t.Errorf("%s (wave %d) must sync before %s (wave %d)", order[i-1].what, order[i-1].w, order[i].what, order[i].w)
		}
	}
}

// ── D-2: production on, local off until measured ───────────────────────────────

// The engine runs in the production profile; the local core stays without it
// until `adhar up` is measured with it on (§13.4), though it is small enough to
// enable from the Marketplace (localSafe). Both profiles use the platform overlay.
func TestArgoWorkflowsIsEnabledWhereTheDesignSays(t *testing.T) {
	for file, want := range map[string]string{"adhar-appset-production.yaml": "true", "adhar-appset-local.yaml": "false"} {
		found := false
		for _, e := range loadAppSetElements(t, file) {
			if e.Name != "argo-workflows" {
				continue
			}
			found = true
			if e.Enabled != want {
				t.Errorf("%s: argo-workflows enabled=%q, want %q (docs/design/0026 D-2)", file, e.Enabled, want)
			}
			if e.ManifestPath != argoPkg+"/manifests/platform" {
				t.Errorf("%s: argo-workflows points at %s, want %s/manifests/platform", file, e.ManifestPath, argoPkg)
			}
		}
		if !found {
			t.Errorf("%s does not wire argo-workflows", file)
		}
	}
	var contract struct {
		Resources struct {
			LocalSafe bool `json:"localSafe"`
		} `json:"resources"`
	}
	if err := yaml.Unmarshal([]byte(readStackFile(t, argoPkg+"/adhar-package.yaml")), &contract); err != nil {
		t.Fatal(err)
	}
	if !contract.Resources.LocalSafe {
		t.Error("argo-workflows is two small pods and a 10 Gi archive; its contract should say localSafe: true")
	}
	if _, err := os.Stat(filepath.Join(stackPackagesDir(t), argoPkg, "manifests", "dev")); err == nil {
		t.Error("manifests/dev is back: both profiles use manifests/platform")
	}
}

// ── The platform tasks' kubectl stays inside the supported version skew ────────

// kubectl supports one minor version of skew against the API server; the platform
// tasks ran alpine/k8s:1.31.0 against v1.37 clusters, six minors outside it.
func TestPlatformTasksKubectlMatchesTheClusterMinor(t *testing.T) {
	cluster := argoSemver(t, globals.DefaultKubernetesVersion)
	body := readStackFile(t, argoPkg+"/manifests/platform-tasks/platform-tasks.yaml")
	images := regexp.MustCompile(`image:\s*(?:docker\.io/)?alpine/k8s:(\d+)\.(\d+)\.(\d+)`).FindAllStringSubmatch(body, -1)
	if len(images) == 0 {
		t.Fatal("the platform tasks no longer run alpine/k8s; update this guard to the image they use")
	}
	for _, m := range images {
		major, _ := strconv.Atoi(m[1])
		minor, _ := strconv.Atoi(m[2])
		if major != cluster[0] || minor < cluster[1]-1 || minor > cluster[1]+1 {
			t.Errorf("a platform task runs kubectl %s.%s.%s against Kubernetes %s: outside the one-minor skew kubectl supports",
				m[1], m[2], m[3], globals.DefaultKubernetesVersion)
		}
	}
}
