package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"
)

// Memory requests must describe what a process holds, in BOTH directions.
//
// Measured on the DigitalOcean cluster on 2026-10-22 (working set now, and the
// peak over three hours from Prometheus): seven workers at 96-103% of real
// memory while their requests read 94-99%. The two numbers agreed only by
// accident. ~20 containers had no request at all and took the namespace's 64Mi
// default while holding 0.4-1.6 GiB (Penpot's backend, Trino, six Airbyte JVMs,
// the trivy operator, Tempo, ClickHouse...), so the scheduler packed them onto
// nodes that were already full and the kubelet evicted them — Penpot five
// times, Trino three, Airbyte four, and a CI build pod ten minutes into its
// build, every message reading "request is 64Mi, has larger consumption of
// memory". At the same time Falco (x9), Kubeflow, RabbitMQ and the trivy
// server reserved ~3.9 GiB they never touched.
//
// Floors keep the under-requested honest (raise them, never drop them below the
// measured working set); ceilings keep the right-sized ones from drifting back
// to their chart defaults on the next regeneration. Each row also checks the
// generator input (values.yaml, or the generator script), because the rendered
// manifest alone would be reverted by the next generate-manifests.sh run.
func TestMeasuredMemoryRequestsStayRightSized(t *testing.T) {
	type row struct {
		file, kind, name, container string
		floor, ceiling              string   // either may be ""
		values                      []string // path into <pkg>/values.yaml, nil = no values file
		generatorContains           string   // for kustomize/static generators
	}
	rows := []row{
		// ── under-requested: floors ──
		{file: "application/penpot/manifests/install.yaml", kind: "Deployment", name: "penpot-backend", container: "penpot-backend",
			floor: "1Gi", values: []string{"backend", "resources", "requests", "memory"}},
		{file: "data/trino/manifests/install.yaml", kind: "Deployment", name: "trino-coordinator", container: "trino-coordinator",
			floor: "768Mi", values: []string{"coordinator", "resources", "requests", "memory"}},
		{file: "data/trino/manifests/install.yaml", kind: "Deployment", name: "trino-worker", container: "trino-worker",
			floor: "768Mi", values: []string{"worker", "resources", "requests", "memory"}},
		{file: "data/airbyte/manifests/install.yaml", kind: "Deployment", name: "airbyte-server", container: "airbyte-server-container",
			floor: "768Mi", values: []string{"server", "resources", "requests", "memory"}},
		{file: "data/airbyte/manifests/install.yaml", kind: "Deployment", name: "airbyte-cron", container: "airbyte-cron",
			floor: "512Mi", values: []string{"cron", "resources", "requests", "memory"}},
		{file: "data/airbyte/manifests/install.yaml", kind: "Deployment", name: "airbyte-worker", container: "airbyte-worker-container",
			floor: "512Mi", values: []string{"worker", "resources", "requests", "memory"}},
		{file: "data/airbyte/manifests/install.yaml", kind: "Deployment", name: "airbyte-workload-api-server", container: "airbyte-workload-api-server-container",
			floor: "512Mi", values: []string{"workload-api-server", "resources", "requests", "memory"}},
		{file: "data/airbyte/manifests/install.yaml", kind: "Deployment", name: "airbyte-workload-launcher", container: "airbyte-workload-launcher-container",
			floor: "512Mi", values: []string{"workload-launcher", "resources", "requests", "memory"}},
		{file: "data/airbyte/manifests/install.yaml", kind: "Deployment", name: "airbyte-connector-builder-server", container: "airbyte-connector-builder-server",
			floor: "320Mi", values: []string{"connector-builder-server", "resources", "requests", "memory"}},
		{file: "security/trivy/manifests/install.yaml", kind: "Deployment", name: "trivy-operator", container: "trivy-operator",
			floor: "1Gi", values: []string{"resources", "requests", "memory"}},
		{file: "observability/tempo/manifests/install.yaml", kind: "StatefulSet", name: "tempo", container: "tempo",
			floor: "512Mi", values: []string{"tempo", "resources", "requests", "memory"}},
		{file: "application/n8n/manifests/install.yaml", kind: "Deployment", name: "n8n", container: "n8n",
			floor: "384Mi", values: []string{"main", "resources", "requests", "memory"}},
		{file: "observability/oncall/manifests/install.yaml", kind: "Deployment", name: "oncall-engine", container: "oncall",
			floor: "256Mi", values: []string{"engine", "resources", "requests", "memory"}},
		{file: "observability/oncall/manifests/install.yaml", kind: "Deployment", name: "oncall-celery", container: "oncall",
			floor: "256Mi", values: []string{"celery", "resources", "requests", "memory"}},
		{file: "security/tetragon/manifests/install.yaml", kind: "DaemonSet", name: "tetragon", container: "tetragon",
			floor: "192Mi", values: []string{"tetragon", "resources", "requests", "memory"}},
		{file: "application/plane/manifests/install.yaml", kind: "Deployment", name: "plane-worker-wl", container: "plane-worker",
			floor: "512Mi", values: []string{"worker", "memoryRequest"}},
		{file: "application/posthog/manifests/install.yaml", kind: "Deployment", name: "posthog-worker-default", container: "posthog-workers",
			floor: "1Gi", values: []string{"worker", "resources", "requests", "memory"}},
		{file: "application/posthog/manifests/install.yaml", kind: "Deployment", name: "posthog-web", container: "posthog-web",
			floor: "4Gi", values: []string{"web", "resources", "requests", "memory"}},
		{file: "application/posthog/manifests/install.yaml", kind: "Deployment", name: "posthog-events", container: "posthog-events",
			floor: "4Gi", values: []string{"events", "resources", "requests", "memory"}},
		// ── over-requested: ceilings ──
		{file: "security/falco/manifests/install.yaml", kind: "DaemonSet", name: "falco", container: "falco",
			ceiling: "256Mi", values: []string{"resources", "requests", "memory"}},
		{file: "security/trivy/manifests/install.yaml", kind: "StatefulSet", name: "trivy-server", container: "trivy-server",
			ceiling: "256Mi", values: []string{"trivy", "server", "resources", "requests", "memory"}},
		{file: "data/rabbitmq/manifests/install.yaml", kind: "StatefulSet", name: "rabbitmq", container: "rabbitmq",
			ceiling: "256Mi", values: []string{"resources", "requests", "memory"}},
		{file: "data/prefect/manifests/install.yaml", kind: "Deployment", name: "prefect-server", container: "server",
			ceiling: "384Mi"},
		{file: "data/kubeflow/manifests/install.yaml", kind: "Deployment", name: "ml-pipeline", container: "ml-pipeline-api-server",
			ceiling: "256Mi", generatorContains: `("ml-pipeline", "ml-pipeline-api-server"): "192Mi"`},
		{file: "data/kubeflow/manifests/install.yaml", kind: "Deployment", name: "ml-pipeline-persistenceagent", container: "ml-pipeline-persistenceagent",
			ceiling: "128Mi", generatorContains: `("ml-pipeline-persistenceagent", "ml-pipeline-persistenceagent"): "96Mi"`},
		{file: "data/kubeflow/manifests/install.yaml", kind: "Deployment", name: "ml-pipeline-visualizationserver", container: "ml-pipeline-visualizationserver",
			ceiling: "512Mi", generatorContains: `("ml-pipeline-visualizationserver", "ml-pipeline-visualizationserver"): "448Mi"`},
		{file: "data/kubeflow/manifests/install.yaml", kind: "Deployment", name: "mysql", container: "mysql",
			ceiling: "512Mi", generatorContains: `("mysql", "mysql"): "512Mi"`},
	}

	for _, r := range rows {
		got := workloadContainerMemoryRequest(t, r.file, r.kind, r.name, r.container)
		where := r.file + " " + r.kind + "/" + r.name + " container " + r.container
		if got == "" {
			t.Errorf("%s declares no memory request; it takes the namespace's 64Mi default and is invisible to the scheduler", where)
			continue
		}
		have := resource.MustParse(got)
		if r.floor != "" && have.Cmp(resource.MustParse(r.floor)) < 0 {
			t.Errorf("%s requests %s memory but was measured above %s; the scheduler packs it onto a full node and the kubelet evicts it", where, got, r.floor)
		}
		if r.ceiling != "" && have.Cmp(resource.MustParse(r.ceiling)) > 0 {
			t.Errorf("%s requests %s memory but peaks well under %s; the excess is reserved memory nothing uses on a cluster that is out of it", where, got, r.ceiling)
		}
		pkgDir := filepath.Dir(filepath.Dir(r.file))
		if r.values != nil {
			raw := readStackFile(t, pkgDir+"/values.yaml")
			var values map[string]interface{}
			if err := yaml.Unmarshal([]byte(raw), &values); err != nil {
				t.Fatalf("%s/values.yaml: %v", pkgDir, err)
			}
			v, _ := dig(values, r.values...).(string)
			vq, perr := resource.ParseQuantity(v)
			if perr != nil || vq.Cmp(have) != 0 {
				t.Errorf("%s/values.yaml %s = %q but the rendered manifest requests %s; the next generate-manifests.sh run reverts the manifest",
					pkgDir, strings.Join(r.values, "."), v, got)
			}
		}
		if r.generatorContains != "" && !strings.Contains(readStackFile(t, pkgDir+"/generate-manifests.sh"), r.generatorContains) {
			t.Errorf("%s/generate-manifests.sh does not set %s; regenerating would restore the upstream request", pkgDir, r.generatorContains)
		}
	}
}

// Trino's heap must fit what the pod requests, and Trino's own startup check.
//
// The chart default is -Xmx8G per JVM. With a 64Mi request on 12 GiB nodes that
// is a process the scheduler thinks is tiny and that may legitimately grow to
// two-thirds of the node. The heap is now the bound; it must stay large enough
// that query.max-memory-per-node plus Trino's 30% heap headroom fits inside it,
// or Trino refuses to start ("Invalid memory configuration").
func TestTrinoHeapIsBoundedAndStillFitsItsQueryMemory(t *testing.T) {
	body := readStackFile(t, "data/trino/manifests/install.yaml")
	xmx := regexp.MustCompile(`(?m)^\s*-Xmx(\d+)G\s*$`).FindAllStringSubmatch(body, -1)
	perNode := regexp.MustCompile(`(?m)query\.max-memory-per-node=(\d+)GB`).FindAllStringSubmatch(body, -1)
	if len(xmx) != 2 || len(perNode) != 2 {
		t.Fatalf("expected a heap and a per-node query limit for coordinator and worker, found %d and %d", len(xmx), len(perNode))
	}
	for i := range xmx {
		heap, node := atoiOrZero(xmx[i][1]), atoiOrZero(perNode[i][1])
		if heap > 4 {
			t.Errorf("trino JVM %d has a %dG heap; with a ~1Gi request that lets one pod take a third of a node", i, heap)
		}
		if float64(node)+0.3*float64(heap) > float64(heap) {
			t.Errorf("trino JVM %d: query.max-memory-per-node %dGB + 30%% headroom does not fit a %dG heap; Trino will not start", i, node, heap)
		}
	}
	values := readStackFile(t, "data/trino/values.yaml")
	if !strings.Contains(values, `maxHeapSize: "`+xmx[0][1]+`G"`) {
		t.Errorf("data/trino/values.yaml does not pin maxHeapSize to the %sG the manifest renders", xmx[0][1])
	}
}

// Celery forks one child per CPU unless told otherwise, so a worker's footprint
// follows the NODE it lands on. PostHog's worker on an 8-vCPU node ran eight
// children (~2 GiB) against a 2Gi limit and was OOMKilled 8 times in an hour.
func TestPostHogWorkerBoundsItsCeleryConcurrency(t *testing.T) {
	body := readStackFile(t, "application/posthog/manifests/install.yaml")
	for _, doc := range regexp.MustCompile(`(?m)^---\s*$`).Split(body, -1) {
		if !strings.Contains(doc, "kind: Deployment") || !strings.Contains(doc, "\n  name: posthog-worker-default\n") {
			continue
		}
		if !regexp.MustCompile(`- name: WEB_CONCURRENCY\n\s+value: "?\d+"?`).MatchString(doc) {
			t.Error("posthog-worker-default sets no WEB_CONCURRENCY; Celery forks one child per node CPU and outgrows its limit")
		}
		values := readStackFile(t, "application/posthog/values.yaml")
		var v map[string]interface{}
		if err := yaml.Unmarshal([]byte(values), &v); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(asYAML(t, dig(v, "worker", "env")), "WEB_CONCURRENCY") {
			t.Error("posthog values.yaml worker.env lacks WEB_CONCURRENCY; regenerating would drop the bound")
		}
		return
	}
	t.Fatal("no posthog-worker-default Deployment found; update this guard")
}

// A CronJob under concurrencyPolicy: Forbid with no activeDeadlineSeconds is a
// single hung run away from never running again: Forbid skips every later
// schedule while the hung Job is "active", and nothing ends it. Five of the
// platform's CronJobs were in that state, including nexus-reconcile — the
// repair that exists because Nexus once lost its repositories silently.
func TestForbidCronJobsHaveADeadline(t *testing.T) {
	// chart-rendered CronJobs whose chart offers no knob for it
	exempt := map[string]string{
		"kargo-garbage-collector": "kargo chart 1.11.4 renders no activeDeadlineSeconds and has no value for one",
	}
	walkProductionManifests(t, func(pkg, file string, obj map[string]interface{}) {
		if obj["kind"] != "CronJob" {
			return
		}
		name, _ := dig(obj, "metadata", "name").(string)
		if dig(obj, "spec", "suspend") == true || dig(obj, "spec", "concurrencyPolicy") != "Forbid" {
			return
		}
		if _, ok := exempt[name]; ok {
			return
		}
		if d, ok := dig(obj, "spec", "jobTemplate", "spec", "activeDeadlineSeconds").(float64); !ok || d <= 0 {
			t.Errorf("%s: CronJob %s runs under Forbid with no jobTemplate activeDeadlineSeconds; one hung run blocks every later one", file, name)
		}
	})
}

// The heavy CI steps must carry a request. A Tekton step with none receives a
// SHARE of the LimitRange default (64Mi / number of steps = 21Mi for the
// buildpacks Task), and a build holding 1.5 GiB on a 21Mi request is the first
// pod the kubelet evicts — which it did, ten minutes into a build.
func TestHeavyCIStepsDeclareTheirMemory(t *testing.T) {
	want := map[[2]string]string{
		{"buildpacks-build", "build"}: "1Gi",
		{"trivy-scan", "scan"}:        "256Mi",
		{"syft-sbom", "sbom"}:         "384Mi",
		{"test-auto", "maven"}:        "512Mi",
	}
	seen := map[[2]string]bool{}
	dir := filepath.Join(stackPackagesDir(t), "application", "adhar-supply-chain", "manifests")
	files, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		for _, doc := range regexp.MustCompile(`(?m)^---\s*$`).Split(string(raw), -1) {
			var obj map[string]interface{}
			if yaml.Unmarshal([]byte(doc), &obj) != nil || obj["kind"] != "Task" {
				continue
			}
			task, _ := dig(obj, "metadata", "name").(string)
			steps, _ := dig(obj, "spec", "steps").([]interface{})
			for _, s := range steps {
				step, _ := s.(map[string]interface{})
				sname, _ := step["name"].(string)
				floor, ok := want[[2]string{task, sname}]
				if !ok {
					continue
				}
				seen[[2]string{task, sname}] = true
				got, _ := dig(step, "computeResources", "requests", "memory").(string)
				gq, perr := resource.ParseQuantity(got)
				if perr != nil || gq.Cmp(resource.MustParse(floor)) < 0 {
					t.Errorf("Task %s step %s requests %q memory; it was measured well above %s and is evicted first on a full node", task, sname, got, floor)
				}
			}
		}
	}
	for k := range want {
		if !seen[k] {
			t.Errorf("Task %s step %s not found; update this guard", k[0], k[1])
		}
	}
}

// An OpenSearchISMPolicy that spells out a zero value is OutOfSync forever:
// the API server drops omitempty fields (`transitions: []` on the last state,
// `ismTemplate.priority: 0`), Argo CD compares the stored object with the
// manifest, and the opensearch Application never reads Synced.
func TestISMPoliciesCarryNoZeroValuesTheAPIServerDrops(t *testing.T) {
	for _, rel := range []string{"data/opensearch/manifests/cluster.yaml", "data/opensearch/values-cluster.yaml"} {
		body := readStackFile(t, rel)
		for _, bad := range []*regexp.Regexp{
			regexp.MustCompile(`(?m)^\s*transitions:\s*\[\]\s*$`),
			regexp.MustCompile(`(?m)^\s*priority:\s*0\s*$`),
		} {
			if loc := bad.FindStringIndex(body); loc != nil {
				t.Errorf("%s carries %q; the API server drops it and Argo CD reports the ISM policy OutOfSync forever", rel, strings.TrimSpace(body[loc[0]:loc[1]]))
			}
		}
	}
}

// The trivy node-collector runs once per node, the control plane included, and
// the control plane is tainted NoSchedule: with no toleration its collector sat
// Pending, was timed out and re-created, forever, and the master was never
// assessed.
func TestTrivyNodeCollectorToleratesTheControlPlane(t *testing.T) {
	install := readStackFile(t, "security/trivy/manifests/install.yaml")
	if !strings.Contains(install, `nodeCollector.tolerations: "[{\"effect\":\"NoSchedule\",\"key\":\"node-role.kubernetes.io/control-plane\",\"operator\":\"Exists\"}]"`) {
		t.Error("trivy-operator's config gives the node-collector no control-plane toleration")
	}
	if !strings.Contains(readStackFile(t, "security/trivy/values.yaml"), "key: node-role.kubernetes.io/control-plane") {
		t.Error("security/trivy/values.yaml lacks the node-collector toleration; regenerating would drop it")
	}
}

/* ───── helpers ───── */

func workloadContainerMemoryRequest(t *testing.T, rel, kind, name, container string) string {
	t.Helper()
	body := readStackFile(t, rel)
	for _, doc := range regexp.MustCompile(`(?m)^---\s*$`).Split(body, -1) {
		var obj map[string]interface{}
		if yaml.Unmarshal([]byte(doc), &obj) != nil || obj["kind"] != kind || dig(obj, "metadata", "name") != name {
			continue
		}
		spec, _ := obj["spec"].(map[string]interface{})
		for _, c := range containersOf(spec) {
			if c["name"] == container {
				v, _ := dig(c, "resources", "requests", "memory").(string)
				return v
			}
		}
		t.Fatalf("%s: %s/%s has no container %q; update this guard", rel, kind, name, container)
	}
	t.Fatalf("%s: no %s named %s; update this guard", rel, kind, name)
	return ""
}

// walkProductionManifests calls fn for every object in every package the
// production profile enables.
func walkProductionManifests(t *testing.T, fn func(pkg, file string, obj map[string]interface{})) {
	t.Helper()
	appset, err := os.ReadFile(filepath.Join(stackPackagesDir(t), "..", "adhar-appset-production.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]interface{}
	if err := yaml.Unmarshal(appset, &doc); err != nil {
		t.Fatal(err)
	}
	gens, _ := dig(doc, "spec", "generators").([]interface{})
	for _, g := range gens {
		elements, _ := dig(g.(map[string]interface{}), "list", "elements").([]interface{})
		for _, el := range elements {
			e := el.(map[string]interface{})
			if e["enabled"] != "true" {
				continue
			}
			pkg, _ := e["name"].(string)
			path, _ := e["manifestPath"].(string)
			if path == "" {
				path, _ = e["path"].(string)
			}
			_ = filepath.Walk(filepath.Join(stackPackagesDir(t), path), func(p string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() || !strings.HasSuffix(p, ".yaml") {
					return nil
				}
				raw, _ := os.ReadFile(p)
				for _, part := range regexp.MustCompile(`(?m)^---\s*$`).Split(string(raw), -1) {
					var obj map[string]interface{}
					if yaml.Unmarshal([]byte(part), &obj) != nil || obj == nil {
						continue
					}
					rel, _ := filepath.Rel(stackPackagesDir(t), p)
					fn(pkg, rel, obj)
				}
				return nil
			})
		}
	}
}

func atoiOrZero(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func asYAML(t *testing.T, v interface{}) string {
	t.Helper()
	b, err := yaml.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
