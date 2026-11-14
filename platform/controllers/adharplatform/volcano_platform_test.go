package adharplatform

// core/volcano, Phase 2 of design 0025 (§4.1–4.2, PLAT-2.1): the guards that
// hold what the package must be once it is enabled, beside the ones in
// volcano_package_test.go that hold what keeps it harmless while it is not.
// TestVolcanoComponentsRunOnTheirNarrowedRBAC (volcano_rehearsal_apiserver_test.go)
// is the behavioural half of the RBAC and scheduler-configuration guards below.

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	sigsyaml "sigs.k8s.io/yaml"

	"adhar-io/adhar/api/v1alpha1"
)

const volcanoDir = "core/volcano"

// volcanoPackageDocs returns every object in core/volcano/manifests, from the
// generated install and the hand-written files beside it.
func volcanoPackageDocs(t *testing.T) map[string][]map[string]any {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(stackPackagesDir(t), volcanoDir, "manifests", "*.yaml"))
	if err != nil || len(files) < 4 {
		t.Fatalf("expected install.yaml and the hand-written manifests, found %v (%v)", files, err)
	}
	out := map[string][]map[string]any{}
	for _, f := range files {
		rel, _ := filepath.Rel(stackPackagesDir(t), f)
		for _, doc := range manifestDocs(readStackFile(t, rel)) {
			out[filepath.Base(f)] = append(out[filepath.Base(f)], doc)
		}
	}
	return out
}

func volcanoRules(t *testing.T, role string) []map[string]any {
	t.Helper()
	for _, doc := range volcanoObjects(t) {
		if volcanoKind(doc) == "ClusterRole" && volcanoName(doc) == role {
			var out []map[string]any
			raw, _ := doc["rules"].([]any)
			for _, r := range raw {
				rule, _ := r.(map[string]any)
				out = append(out, rule)
			}
			return out
		}
	}
	t.Fatalf("no ClusterRole %s", role)
	return nil
}

func stringList(v any) []string {
	raw, _ := v.([]any)
	var out []string
	for _, x := range raw {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

// The controllers and the scheduler run cluster-wide, in the namespace every
// package shares. Each rule removed here was a way out of Volcano's own lane;
// the rehearsal shows the binaries run without them.
func TestVolcanoComponentRBACStaysNarrowed(t *testing.T) {
	roles := map[string]bool{}
	for _, doc := range volcanoObjects(t) {
		switch volcanoKind(doc) {
		case "ClusterRole", "Role":
			roles[volcanoName(doc)] = true
			for k := range asMap(dig(doc, "metadata", "labels")) {
				if strings.HasPrefix(k, "rbac.authorization.k8s.io/aggregate-to-") {
					t.Errorf("%s aggregates into the built-in %s role; user-facing Volcano grants are core/adhar-batch's (design 0025 §10.2)", volcanoName(doc), k)
				}
			}
		}
	}
	if want := []string{"volcano-admission", "volcano-controllers", "volcano-scheduler"}; strings.Join(sortedKeys(roles), ",") != strings.Join(want, ",") {
		t.Errorf("ClusterRoles %v; want exactly %v (the user-facing vcjob roles duplicate core/adhar-batch's)", sortedKeys(roles), want)
	}

	readOnly := map[string]bool{"get": true, "list": true, "watch": true}
	never := map[string]string{
		"customresourcedefinitions": "no CRD client exists in either binary; a CRD writer can rewrite any API on the cluster",
		"leases":                    "leader election is off; a cluster-wide lease writer can take any controller's leadership",
		"jobflows":                  "the JobFlow CRDs are not installed",
		"jobtemplates":              "the JobFlow CRDs are not installed",
		"colocationconfigurations":  "the colocation controller is disabled",
		"nodeshards":                "the sharding controller is disabled (the scheduler only reads them)",
	}
	for _, role := range []string{"volcano-controllers", "volcano-scheduler"} {
		for _, rule := range volcanoRules(t, role) {
			groups, resources, verbs := stringList(rule["apiGroups"]), stringList(rule["resources"]), stringList(rule["verbs"])
			for _, list := range [][]string{groups, resources, verbs} {
				for _, v := range list {
					if v == "*" {
						t.Errorf("%s: a wildcard in %v", role, rule)
					}
				}
			}
			for _, v := range verbs {
				if v == "escalate" || v == "bind" || v == "impersonate" {
					t.Errorf("%s may %s", role, v)
				}
			}
			writes := false
			for _, v := range verbs {
				writes = writes || !readOnly[v]
			}
			for _, res := range resources {
				if why, bad := never[res]; bad && !(role == "volcano-scheduler" && res == "nodeshards" && !writes) {
					t.Errorf("%s keeps %s %v: %s", role, res, verbs, why)
				}
				switch {
				case res == "secrets" && role == "volcano-controllers":
					t.Errorf("volcano-controllers keeps secrets %v: only the ssh job plugin uses them, and cluster-wide get reads every credential on the platform", verbs)
				// 2026-11-14: only the svc job plugin (and the framework plugins that
				// pull it in) writes these, and a cluster-wide ConfigMap writer can
				// rewrite volcano-scheduler-configmap, cilium-config or argocd-cm,
				// which is why design §4.1 leaves the dashboard out. Runs use `env`
				// only (§4.5, "no svc plugin").
				case (res == "configmaps" || res == "services") && writes && role == "volcano-controllers":
					t.Errorf("volcano-controllers may write %s %v; only the svc job plugin does, and the platform's runs use env only (design 0025 §4.5)", res, verbs)
				case res == "networkpolicies" && role == "volcano-controllers":
					t.Errorf("volcano-controllers keeps networkpolicies %v; only the svc job plugin uses them, and a cluster-wide writer can open or close any namespace", verbs)
				case res == "priorityclasses" && writes:
					t.Errorf("%s may write priorityclasses %v; it only reads them through an informer", role, verbs)
				case res == "hypernodes" && writes && role == "volcano-controllers":
					t.Errorf("volcano-controllers may write hypernodes %v; the hyperNode controller is disabled", verbs)
				case (res == "nodes" || res == "configmaps") && writes && role == "volcano-scheduler":
					t.Errorf("volcano-scheduler may write %s %v; only deviceshare (nodes) and fairshare (configmaps) do, neither enabled", res, verbs)
				}
			}
		}
	}

	// Leases went with leader election; the two must not part.
	for name, doc := range volcanoDeployments(t) {
		if name == "volcano-admission" {
			continue
		}
		if !containsString(volcanoArgs(t, doc), "--leader-elect=false") {
			t.Errorf("%s runs leader election, and its ClusterRole has no lease grant", name)
		}
	}
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

type volcanoSchedulerConf struct {
	Actions string `json:"actions"`
	Tiers   []struct {
		Plugins []struct {
			Name               string `json:"name"`
			EnablePreemptable  *bool  `json:"enablePreemptable"`
			EnableReclaimable  *bool  `json:"enableReclaimable"`
			EnableHierarchy    *bool  `json:"enableHierarchy"`
			EnabledJobEnqueued *bool  `json:"enableJobEnqueued"`
		} `json:"plugins"`
	} `json:"tiers"`
}

// The scheduler configuration is design 0025 §4.2's stable core, and two plugin
// options in it decide whether A2.4 and A2.7 hold (values.yaml explains each,
// from the v1.16.0 source; the rehearsal shows them on a real scheduler).
func TestVolcanoSchedulerConfigKeepsReclaimAndPreemptInsideBatch(t *testing.T) {
	var raw string
	for _, doc := range volcanoObjects(t) {
		if volcanoKind(doc) == "ConfigMap" && volcanoName(doc) == "volcano-scheduler-configmap" {
			raw, _ = dig(doc, "data", "volcano-scheduler.conf").(string)
		}
	}
	var conf volcanoSchedulerConf
	if err := sigsyaml.Unmarshal([]byte(raw), &conf); err != nil || raw == "" {
		t.Fatalf("volcano-scheduler.conf does not parse (%v):\n%s", err, raw)
	}

	var actions []string
	for _, a := range strings.Split(conf.Actions, ",") {
		actions = append(actions, strings.TrimSpace(a))
	}
	// Order matters: enqueue admits gangs whole, allocate places them, and the
	// two eviction actions run last.
	if got, want := strings.Join(actions, ","), "enqueue,allocate,backfill,reclaim,preempt"; got != want {
		t.Errorf("actions %q; want %q (shuffle evicts by node utilisation, gangpreempt/gangreclaim are alpha)", got, want)
	}

	tiers := make([][]string, len(conf.Tiers))
	tierOf := map[string]int{}
	for i, tier := range conf.Tiers {
		for _, p := range tier.Plugins {
			tiers[i] = append(tiers[i], p.Name)
			tierOf[p.Name] = i
			off := func(b *bool) bool { return b != nil && !*b }
			switch p.Name {
			case "gang":
				if !off(p.EnablePreemptable) {
					t.Error("gang votes in preempt: a gang at exactly minAvailable could then never yield to an adhar-batch-critical run in its queue")
				}
				if off(p.EnableReclaimable) {
					t.Error("gang's reclaim vote is off: conformance then decides reclaim alone and takes from leaves inside their deserved share")
				}
			case "drf":
				if !off(p.EnablePreemptable) {
					t.Error("drf votes in preempt; upstream's default config and design 0025 §4.2 keep it off")
				}
			case "capacity":
				if p.EnableHierarchy == nil || !*p.EnableHierarchy {
					t.Error("capacity runs without enableHierarchy: the adhar-batch parent and its leaves stop meaning anything")
				}
			case "priority", "conformance":
				if off(p.EnablePreemptable) {
					t.Errorf("%s does not vote in preempt; it is half of what decides it", p.Name)
				}
			case "overcommit":
				// Not shipped (2026-11-14, values.yaml): it holds a prod gang Pending
				// on a cluster dev has filled, and reclaim skips Pending jobs, so prod
				// could never take back its share (A2.4 rehearsed red with it).
				t.Error("overcommit is in the scheduler config: on a full cluster it keeps a prod gang Pending, and reclaim never runs for a Pending job (A2.4)")
			}
		}
	}
	want := [][]string{{"priority", "gang", "conformance"}, {"drf", "predicates", "capacity", "nodeorder", "binpack"}}
	for i := range want {
		if i >= len(tiers) || strings.Join(tiers[i], ",") != strings.Join(want[i], ",") {
			t.Errorf("tiers %v; want %v (design 0025 §4.2). proportion would fight capacity for the queues; overcommit blocks reclaim on a full cluster (values.yaml); deviceshare, fairshare and rescheduling are not in the stable core", tiers, want)
			break
		}
	}
	// capacity must be asked AFTER gang's empty vote, or it is never the one deciding.
	if tierOf["capacity"] <= tierOf["gang"] {
		t.Error("capacity sits in gang's tier or before it; reclaim victims must be capacity's choice")
	}

	// The scheduler reads exactly this file.
	sched := volcanoDeployments(t)["volcano-scheduler"]
	if !containsString(volcanoArgs(t, sched), "--scheduler-conf=/volcano.scheduler/volcano-scheduler.conf") {
		t.Errorf("volcano-scheduler does not read the shipped configuration: %v", volcanoArgs(t, sched))
	}
}

// adhar-batch is the parent every team leaf names (design 0025 §4.3). Without a
// deserved share every prod leaf is refused by the queue webhook.
func TestVolcanoRootQueueBoundsTheBatchTree(t *testing.T) {
	// Seed-time (D17): as a platform created before the spec carried batch sizing
	// renders it; TestVolcanoRootQueueIsRenderedFromTheCluster covers a sized one.
	q := volcanoRootQueue(t, v1alpha1.BuildCustomizationSpec{})
	if q["apiVersion"] != "scheduling.volcano.sh/v1beta1" || q["kind"] != "Queue" || volcanoName(q) != "adhar-batch" {
		t.Fatalf("root-queue.yaml is %v %v/%s; want scheduling.volcano.sh/v1beta1 Queue/adhar-batch", q["apiVersion"], q["kind"], volcanoName(q))
	}
	if dig(q, "metadata", "namespace") != nil {
		t.Error("a Queue is cluster-scoped; a namespace on it is an error Argo CD reports as drift")
	}
	if dig(q, "spec", "parent") != "root" {
		t.Errorf("adhar-batch must hang off root, has parent %v", dig(q, "spec", "parent"))
	}
	if r, _ := dig(q, "spec", "reclaimable").(bool); r {
		t.Error("adhar-batch is reclaimable: true")
	}
	qty := func(field, res string) resource.Quantity {
		s := ""
		switch v := dig(q, "spec", field, res).(type) {
		case string:
			s = v
		case float64:
			s = resource.NewQuantity(int64(v), resource.DecimalSI).String()
		}
		parsed, err := resource.ParseQuantity(s)
		if err != nil {
			t.Fatalf("spec.%s.%s = %q: %v", field, res, s, err)
		}
		return parsed
	}
	// Room for at least one prod leaf's share, as the descriptor rendered beside it
	// names it (D17).
	desc := volcanoDescriptor(t, v1alpha1.BuildCustomizationSpec{})
	floor := map[string]resource.Quantity{
		"cpu":    resource.MustParse(toString(dig(desc, "data", "prodDeservedCPU"))),
		"memory": resource.MustParse(toString(dig(desc, "data", "prodDeservedMemory"))),
	}
	for _, res := range []string{"cpu", "memory"} {
		capQ, des := qty("capability", res), qty("deserved", res)
		if des.Cmp(capQ) > 0 {
			t.Errorf("deserved %s %s exceeds capability %s; the webhook refuses that", res, des.String(), capQ.String())
		}
		if f := floor[res]; des.Cmp(f) < 0 {
			t.Errorf("deserved %s %s leaves no room for one prod leaf's default share (%s, D17)", res, des.String(), f.String())
		}
	}
	// After the scheduler, which creates `root`, and after admission, which must
	// serve the queue webhooks.
	// Compared as numbers: as strings "2" sorts after "10" (2026-11-14, verifier).
	wave := func(doc map[string]any) int {
		s, _ := dig(doc, "metadata", "annotations", "argocd.argoproj.io/sync-wave").(string)
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatalf("%s has sync-wave %q, not an integer", volcanoName(doc), s)
		}
		return n
	}
	if w := wave(q); w <= wave(volcanoDeployments(t)["volcano-scheduler"]) || w <= wave(volcanoDeployments(t)["volcano-admission"]) {
		t.Errorf("root queue sync-wave %d does not follow the scheduler's and admission's", w)
	}

	// The D7 descriptor (core/volcano, seed-time) names the root queue the Team
	// composition parents its leaves under, and only one copy of it exists.
	if dig(desc, "metadata", "namespace") != "adhar-system" || dig(desc, "data", "scheduler") != "volcano" || dig(desc, "data", "gang") != "true" {
		t.Errorf("the descriptor is not the D7 shape: %v", desc)
	}
	if w := wave(desc); w < wave(q) {
		t.Errorf("the descriptor (wave %d) arrives before the root queue it names (wave %d)", w, wave(q))
	}
	found := 0
	_ = filepath.Walk(stackPackagesDir(t), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !(strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".yaml.tmpl")) {
			return nil
		}
		b, _ := os.ReadFile(p)
		if !strings.Contains(string(b), "adhar-batch-scheduler") {
			return nil
		}
		for _, doc := range manifestDocs(string(b)) {
			if doc["kind"] != "ConfigMap" || toString(metaName(doc)) != "adhar-batch-scheduler" {
				continue
			}
			found++
			if rq, ok := dig(doc, "data", "rootQueue").(string); ok && rq != "adhar-batch" && !strings.Contains(rq, "{{") {
				t.Errorf("%s: the descriptor names rootQueue %q, the package ships adhar-batch", p, rq)
			}
		}
		return nil
	})
	if found != 1 {
		t.Errorf("the stack declares %d adhar-batch-scheduler descriptors; want exactly core/volcano's", found)
	}
}

// ADR-0011: one namespace, and the places a namespace hides (flags, env, cert
// SANs, CA-injection annotations, binding subjects, webhook clientConfig).
func TestVolcanoLivesInThePlatformNamespace(t *testing.T) {
	clusterScoped := map[string]bool{
		"CustomResourceDefinition": true, "ClusterRole": true, "ClusterRoleBinding": true,
		"MutatingWebhookConfiguration": true, "ValidatingWebhookConfiguration": true, "Queue": true,
	}
	other := regexp.MustCompile(`\b[a-z0-9]+(?:-[a-z0-9]+)*-system\b`)
	for file, docs := range volcanoPackageDocs(t) {
		for _, doc := range docs {
			kind, name := volcanoKind(doc), volcanoName(doc)
			ns, _ := dig(doc, "metadata", "namespace").(string)
			switch {
			case kind == "Namespace":
				t.Errorf("%s ships Namespace/%s", file, name)
			case clusterScoped[kind] && ns != "":
				t.Errorf("%s: %s/%s is cluster-scoped but names namespace %q", file, kind, name, ns)
			case !clusterScoped[kind] && ns != "adhar-system":
				t.Errorf("%s: %s/%s is in namespace %q, not adhar-system", file, kind, name, ns)
			}
			if a, ok := dig(doc, "metadata", "annotations", "cert-manager.io/inject-ca-from").(string); ok && !strings.HasPrefix(a, "adhar-system/") {
				t.Errorf("%s/%s injects its CA from %q", kind, name, a)
			}
			for _, s := range anyList(doc["subjects"]) {
				if n := dig(asMap(s), "namespace"); n != nil && n != "adhar-system" {
					t.Errorf("%s/%s binds a subject in %v", kind, name, n)
				}
			}
			for _, w := range anyList(doc["webhooks"]) {
				if n := dig(asMap(w), "clientConfig", "service", "namespace"); n != "adhar-system" {
					t.Errorf("%s/%s calls a Service in %v", kind, name, n)
				}
			}
			for _, dns := range stringList(dig(doc, "spec", "dnsNames")) {
				if !strings.Contains(dns, ".adhar-system.svc") {
					t.Errorf("Certificate %s covers %q, outside adhar-system", name, dns)
				}
			}
			if kind == "ServiceMonitor" {
				if got := stringList(dig(doc, "spec", "namespaceSelector", "matchNames")); strings.Join(got, ",") != "adhar-system" {
					t.Errorf("ServiceMonitor %s selects namespaces %v", name, got)
				}
			}
		}
		raw := readStackFile(t, filepath.Join(volcanoDir, "manifests", file))
		for _, tok := range other.FindAllString(raw, -1) {
			if tok != "adhar-system" && tok != "kube-system" {
				t.Errorf("%s names namespace %q", file, tok)
			}
		}
	}

	// Every flag and env var that carries a namespace, including the ones whose
	// BUILT-IN default is volcano-system (the binaries' --help, v1.16.0).
	wants := map[string][]string{
		"volcano-admission":   {"--webhook-namespace=adhar-system"},
		"volcano-scheduler":   {"--leader-elect-resource-namespace=adhar-system"},
		"volcano-controllers": {"--leader-elect-resource-namespace=adhar-system", "--sharding-configmap-namespace=adhar-system"},
	}
	for name, doc := range volcanoDeployments(t) {
		args := volcanoArgs(t, doc)
		for _, want := range wants[name] {
			if !containsString(args, want) {
				t.Errorf("%s does not pass %s; the binary's default names volcano-system", name, want)
			}
		}
		for _, e := range anyList(volcanoContainer(t, doc)["env"]) {
			env := asMap(e)
			if v, _ := env["value"].(string); strings.Contains(env["name"].(string), "NAMESPACE") && v != "adhar-system" {
				t.Errorf("%s: env %v=%q", name, env["name"], v)
			}
		}
	}
}

func anyList(v any) []any {
	l, _ := v.([]any)
	return l
}

var manifestKindName = regexp.MustCompile(`(?m)^kind:\s*["']?([A-Za-z]+)`)
var manifestMetaName = regexp.MustCompile(`(?m)^metadata:\s*\n(?:[ \t]+.*\n)*?[ \t]+name:\s*["']?([A-Za-z0-9.\-]+)`)

// Volcano was the classic webhook-certs collider in other platforms. Every name
// it puts on the cluster, namespaced or not, must be its own: two Argo CD
// Applications claiming one object flap forever and overwrite each other
// (CONFLICTS.md §1). Also the names it creates without declaring: the Secrets
// cert-manager writes, and the <SERVICE>_PORT variables its Services inject
// into every pod in adhar-system (CONFLICTS.md §2).
func TestVolcanoObjectNamesConflictWithNoOtherPackage(t *testing.T) {
	type key struct{ kind, name string }
	ours := map[key]bool{}
	var serviceEnv []string
	for _, docs := range volcanoPackageDocs(t) {
		for _, doc := range docs {
			ours[key{volcanoKind(doc), volcanoName(doc)}] = true
			if volcanoKind(doc) == "Service" {
				serviceEnv = append(serviceEnv, strings.ToUpper(strings.ReplaceAll(volcanoName(doc), "-", "_"))+"_")
			}
			if volcanoKind(doc) == "Certificate" {
				if s, ok := dig(doc, "spec", "secretName").(string); ok {
					ours[key{"Secret", s}] = true
				}
			}
		}
	}
	if len(ours) < 40 || len(serviceEnv) != 3 {
		t.Fatalf("read %d Volcano objects and %d Services; this guard read too little", len(ours), len(serviceEnv))
	}

	root := stackPackagesDir(t)
	scanned := 0
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if strings.HasPrefix(rel, volcanoDir+"/") || !(strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".yaml.tmpl")) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		scanned++
		for _, doc := range strings.Split(string(b), "\n---") {
			k, n := manifestKindName.FindStringSubmatch(doc), manifestMetaName.FindStringSubmatch(doc)
			if k != nil && n != nil && ours[key{k[1], n[1]}] {
				t.Errorf("%s declares %s/%s, which core/volcano owns", rel, k[1], n[1])
			}
		}
		for _, prefix := range serviceEnv {
			if strings.Contains(string(b), prefix+"PORT") || strings.Contains(string(b), prefix+"SERVICE_") {
				t.Errorf("%s names %s*: Volcano's Service injects that variable into every pod in adhar-system", rel, prefix)
			}
		}
		return nil
	})
	if scanned < 200 {
		t.Fatalf("scanned %d package files; expected the whole stack", scanned)
	}
}

// One version, written once per place it has to be written: the chart, its
// digest, every image, the contract.
func TestVolcanoPinsOneVersionEverywhere(t *testing.T) {
	gen := readStackFile(t, filepath.Join(volcanoDir, "generate-manifests.sh"))
	chart := regexp.MustCompile(`(?m)^CHART_VERSION="(\d+\.\d+\.\d+)"$`).FindStringSubmatch(gen)
	if chart == nil {
		t.Fatal("generate-manifests.sh pins no exact CHART_VERSION")
	}
	if !regexp.MustCompile(`(?m)^CHART_SHA256="[0-9a-f]{64}"$`).MatchString(gen) {
		t.Error("generate-manifests.sh does not check the chart against a sha256; a re-tagged release would render silently")
	}
	version := chart[1]

	var values struct {
		Basic struct {
			ImageTag string `json:"image_tag_version"`
		} `json:"basic"`
	}
	if err := sigsyaml.Unmarshal([]byte(readStackFile(t, filepath.Join(volcanoDir, "values.yaml"))), &values); err != nil {
		t.Fatal(err)
	}
	if values.Basic.ImageTag != "v"+version {
		t.Errorf("values.yaml image_tag_version %q, chart %s", values.Basic.ImageTag, version)
	}
	var contract struct {
		Version    string `json:"version"`
		AppVersion string `json:"appVersion"`
		Stability  string `json:"stability"`
	}
	if err := sigsyaml.Unmarshal([]byte(readStackFile(t, filepath.Join(volcanoDir, "adhar-package.yaml"))), &contract); err != nil {
		t.Fatal(err)
	}
	if contract.Version != version || contract.AppVersion != "v"+version {
		t.Errorf("adhar-package.yaml says %s/%s, the chart is %s", contract.Version, contract.AppVersion, version)
	}
	// Alpha until the canary passes on 1.37 (design 0025 §4.1, §15).
	if contract.Stability != "alpha" {
		t.Errorf("stability %q before the Phase-2 canary", contract.Stability)
	}
	if !strings.HasPrefix(version, "1.16.") {
		t.Errorf("chart %s: the package is pinned to v1.16.x until the production gate (ADR-0028 decision 2)", version)
	}
	for name, doc := range volcanoDeployments(t) {
		img, _ := volcanoContainer(t, doc)["image"].(string)
		if !strings.HasSuffix(img, ":v"+version) {
			t.Errorf("%s runs %s, not v%s", name, img, version)
		}
	}
	if !strings.Contains(readStackFile(t, volcanoInstall), "# Volcano v"+version+"\n") {
		t.Errorf("install.yaml was not rendered from chart %s; re-run generate-manifests.sh", version)
	}
}

// Every container asks for what it uses, so the adhar-system LimitRange
// default (20m / 64Mi, security/adhar-tenant-quotas) never sizes it, and no
// probe can kill a healthy pod on a loaded node.
func TestVolcanoContainersDeclareRequestsWithinTheLimitRange(t *testing.T) {
	for name, doc := range volcanoDeployments(t) {
		c := volcanoContainer(t, doc)
		for _, res := range []string{"cpu", "memory"} {
			if dig(c, "resources", "requests", res) == nil {
				t.Errorf("%s requests no %s; the LimitRange default would stand in", name, res)
			}
		}
		req, _ := dig(c, "resources", "requests", "memory").(string)
		lim, _ := dig(c, "resources", "limits", "memory").(string)
		r, err1 := resource.ParseQuantity(req)
		l, err2 := resource.ParseQuantity(lim)
		if err1 != nil || err2 != nil || l.Cmp(r) < 0 {
			t.Errorf("%s: memory request %q, limit %q", name, req, lim)
		}
		if dig(c, "resources", "limits", "cpu") != nil {
			t.Errorf("%s sets a CPU limit; a throttled scheduler misses its one-second session", name)
		}
		for _, probe := range []string{"readinessProbe", "livenessProbe", "startupProbe"} {
			p := asMap(c[probe])
			if p == nil {
				continue
			}
			if to, _ := p["timeoutSeconds"].(float64); to < 5 {
				t.Errorf("%s %s timeoutSeconds %v; at least 5 on a loaded node", name, probe, p["timeoutSeconds"])
			}
		}
	}
}

// An alert on Volcano's own series stops existing exactly when the component
// does. These read kube-state-metrics and the API server instead.
func TestVolcanoAlertsReadSignalsThatOutliveTheComponent(t *testing.T) {
	docs := volcanoPackageDocs(t)["alerts.yaml"]
	if len(docs) != 1 || docs[0]["kind"] != "PrometheusRule" {
		t.Fatal("alerts.yaml must be one PrometheusRule")
	}
	exprs := map[string]string{}
	for _, g := range anyList(dig(docs[0], "spec", "groups")) {
		for _, r := range anyList(asMap(g)["rules"]) {
			rule := asMap(r)
			alert, _ := rule["alert"].(string)
			exprs[alert], _ = rule["expr"].(string)
			if sev, _ := dig(rule, "labels", "severity").(string); sev != "critical" && sev != "warning" {
				t.Errorf("%s has severity %q", alert, sev)
			}
		}
	}
	for name := range volcanoDeployments(t) {
		covered := false
		for alert, expr := range exprs {
			if strings.Contains(expr, "kube_deployment_status_replicas_available") && strings.Contains(expr, `deployment="`+name+`"`) {
				covered = true
				if strings.Contains(expr, "volcano_") {
					t.Errorf("%s reads a Volcano series", alert)
				}
			}
		}
		if !covered {
			t.Errorf("no alert says %s is down (design 0025 F12/F13)", name)
		}
	}
	webhookCalls := false
	for _, expr := range exprs {
		webhookCalls = webhookCalls || strings.Contains(expr, "apiserver_admission_webhook_rejection_count")
	}
	if !webhookCalls {
		t.Error("no alert on the API server failing to call the Volcano webhooks; a Ready pod with a bad certificate goes unnoticed")
	}
}

// A2.8: the API-server cost is measured before Volcano leaves alpha, by a
// procedure the README carries and a script that does it the same way twice.
func TestVolcanoReadmeCarriesTheA28Measurement(t *testing.T) {
	readme := readStackFile(t, filepath.Join(volcanoDir, "README.md"))
	for _, want := range []string{"A2.8", "kube-apiserver", "RSS", "CRD", "before", "after", "measure-footprint.sh"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md does not mention %q", want)
		}
	}
	script := filepath.Join(stackPackagesDir(t), volcanoDir, "measure-footprint.sh")
	info, err := os.Stat(script)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error("measure-footprint.sh is not executable")
	}
	body := readStackFile(t, filepath.Join(volcanoDir, "measure-footprint.sh"))
	for _, want := range []string{"process_resident_memory_bytes", "volcano.sh", "customresourcedefinitions"} {
		if !strings.Contains(body, want) {
			t.Errorf("measure-footprint.sh does not read %q", want)
		}
	}
	// It reads; it never changes the cluster it measures.
	for _, verb := range []string{"kubectl apply", "kubectl delete", "kubectl create", "kubectl patch", "kubectl edit", "kubectl scale"} {
		if strings.Contains(body, verb) {
			t.Errorf("measure-footprint.sh runs %q", verb)
		}
	}
}
