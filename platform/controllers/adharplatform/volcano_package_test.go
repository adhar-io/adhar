package adharplatform

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
)

// core/volcano is the platform's batch scheduler (ADR-0028). A scheduler that
// registers admission webhooks with failurePolicy Fail, in the one namespace
// every package shares, is a way to stop every pod on the platform. These guards
// hold the two properties that make it safe to offer from the Marketplace: its
// webhooks can only ever refuse Volcano's own objects, and it schedules only the
// pods that ask for it.

const volcanoInstall = "core/volcano/manifests/install.yaml"

// The API groups a Volcano webhook may match. Nothing else on the platform lives
// in them, so an outage of volcano-admission is contained by construction.
var volcanoGroups = map[string]bool{
	"batch.volcano.sh":      true,
	"bus.volcano.sh":        true,
	"scheduling.volcano.sh": true,
	"topology.volcano.sh":   true,
}

func volcanoObjects(t *testing.T) []map[string]any {
	t.Helper()
	f, err := os.Open(filepath.Join(stackPackagesDir(t), volcanoInstall))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []map[string]any
	dec := k8syaml.NewYAMLOrJSONDecoder(f, 4096)
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				return out
			}
			t.Fatalf("%s: %v", volcanoInstall, err)
		}
		if doc != nil {
			out = append(out, doc)
		}
	}
}

func volcanoName(doc map[string]any) string {
	name, _ := dig(doc, "metadata", "name").(string)
	return name
}

func volcanoKind(doc map[string]any) string {
	kind, _ := doc["kind"].(string)
	return kind
}

func volcanoDeployments(t *testing.T) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, doc := range volcanoObjects(t) {
		if volcanoKind(doc) == "Deployment" {
			out[volcanoName(doc)] = doc
		}
	}
	if len(out) != 3 {
		t.Fatalf("expected the scheduler, controllers and admission Deployments, found %d", len(out))
	}
	return out
}

func volcanoContainer(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	spec, _ := doc["spec"].(map[string]any)
	containers := containersOf(spec)
	if len(containers) != 1 {
		t.Fatalf("%s: expected one container, found %d", volcanoName(doc), len(containers))
	}
	return containers[0]
}

func volcanoArgs(t *testing.T, doc map[string]any) []string {
	t.Helper()
	raw, _ := volcanoContainer(t, doc)["args"].([]any)
	var args []string
	for _, a := range raw {
		s, _ := a.(string)
		args = append(args, s)
	}
	return args
}

// A dead volcano-admission must not be able to stop a pod. Rehearsed against a
// real API server with the admission process stopped: a Pod, Deployment and
// ConfigMap in a workload namespace are all admitted, a Volcano Job and a Queue
// are refused, and that is the whole blast radius. The upstream binary's built-in
// default enables /pods/mutate and /pods/validate, so this is one flag away from
// being untrue.
func TestVolcanoWebhooksCannotBlockAnythingButVolcanoObjects(t *testing.T) {
	webhooks := 0
	for _, doc := range volcanoObjects(t) {
		kind := volcanoKind(doc)
		switch kind {
		case "MutatingAdmissionPolicy", "ValidatingAdmissionPolicy",
			"MutatingAdmissionPolicyBinding", "ValidatingAdmissionPolicyBinding":
			t.Errorf("%s/%s: the chart's admission policies match pods with failurePolicy Fail and hardcode volcano-system",
				kind, volcanoName(doc))
		case "MutatingWebhookConfiguration", "ValidatingWebhookConfiguration":
		default:
			continue
		}
		items, _ := doc["webhooks"].([]any)
		for _, item := range items {
			webhooks++
			w, _ := item.(map[string]any)
			rules, _ := w["rules"].([]any)
			if len(rules) == 0 {
				t.Errorf("%s: a webhook with no rules", volcanoName(doc))
			}
			namespaced := false
			for _, r := range rules {
				rule, _ := r.(map[string]any)
				groups, _ := rule["apiGroups"].([]any)
				if len(groups) == 0 {
					t.Errorf("%s: a rule names no apiGroups", volcanoName(doc))
				}
				for _, g := range groups {
					if group, _ := g.(string); !volcanoGroups[group] {
						t.Errorf("%s matches apiGroup %q, so an admission outage (failurePolicy %v) refuses objects "+
							"that are not Volcano's", volcanoName(doc), group, w["failurePolicy"])
					}
				}
				resources, _ := rule["resources"].([]any)
				for _, res := range resources {
					name, _ := res.(string)
					if name == "*" || name == "*/*" || strings.HasPrefix(name, "pods") {
						t.Errorf("%s matches resource %q", volcanoName(doc), name)
					}
					namespaced = namespaced || (name != "queues" && name != "hypernodes")
				}
			}
			// Namespaced kinds stay out of the platform's own namespaces. A selector
			// cannot exempt a cluster-scoped Queue or HyperNode, which is why the
			// group check above is the real guarantee and this one is depth.
			if !namespaced {
				continue
			}
			excluded := map[string]bool{}
			exprs, _ := dig(w, "namespaceSelector", "matchExpressions").([]any)
			for _, e := range exprs {
				expr, _ := e.(map[string]any)
				if expr["key"] == "kubernetes.io/metadata.name" && expr["operator"] == "NotIn" {
					values, _ := expr["values"].([]any)
					for _, v := range values {
						s, _ := v.(string)
						excluded[s] = true
					}
				}
			}
			if !excluded["adhar-system"] || !excluded["kube-system"] {
				t.Errorf("%s: namespaceSelector must exclude adhar-system and kube-system, excludes %v", volcanoName(doc), excluded)
			}
		}
	}
	if webhooks == 0 {
		t.Fatal("no webhooks found in the Volcano manifest; this guard read nothing")
	}

	admission := volcanoDeployments(t)["volcano-admission"]
	enabled := ""
	for _, a := range volcanoArgs(t, admission) {
		if v, ok := strings.CutPrefix(a, "--enabled-admission="); ok {
			enabled = v
		}
	}
	if enabled == "" {
		t.Fatal("--enabled-admission is unset, and the binary's default turns on /pods/mutate and /pods/validate")
	}
	if strings.Contains(enabled, "/pods/") {
		t.Errorf("--enabled-admission=%s includes a pod webhook", enabled)
	}
}

// Volcano takes only the pods that name it. kube-scheduler keeps everything else.
// Rehearsed on two fake nodes with no kube-scheduler running: a 3-pod gang and a
// bare volcano-named pod were bound, a default-scheduler pod stayed unbound and
// got no PodGroup.
func TestVolcanoIsNotTheDefaultScheduler(t *testing.T) {
	for name, doc := range volcanoDeployments(t) {
		for _, a := range volcanoArgs(t, doc) {
			if v, ok := strings.CutPrefix(a, "--scheduler-name="); ok && v != "volcano" {
				t.Errorf("%s: --scheduler-name=%s hands Volcano pods that never asked for it", name, v)
			}
		}
		// Its own components stay with kube-scheduler: a scheduler cannot start itself.
		if s := dig(doc, "spec", "template", "spec", "schedulerName"); s != nil {
			t.Errorf("%s sets schedulerName %v", name, s)
		}
	}
	// resourceGroups is the admission feature that rewrites a pod's schedulerName.
	for _, doc := range volcanoObjects(t) {
		if volcanoKind(doc) == "ConfigMap" && volcanoName(doc) == "volcano-admission-configmap" {
			data, _ := doc["data"].(map[string]any)
			conf, _ := data["volcano-admission.conf"].(string)
			for _, line := range strings.Split(conf, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "resourceGroups:") {
					t.Error("volcano-admission.conf declares resourceGroups, which patches schedulerName onto pods")
				}
			}
		}
	}
	// No profile turns it on: the production gate is v1.17 or a maintainer's
	// sign-off (design 0025 §4.6). The Marketplace flips Gitea's copy, never these.
	for _, appset := range []string{"adhar-appset-local.yaml", "adhar-appset-production.yaml", "adhar-appset-gitops.yaml"} {
		found := false
		for _, e := range loadAppSetElements(t, appset) {
			if e.Name != "volcano" {
				continue
			}
			found = true
			if e.Enabled != "false" {
				t.Errorf("%s ships volcano enabled=%q; it is opt-in from the Marketplace", appset, e.Enabled)
			}
			if e.Namespace != "adhar-system" || e.ManifestPath != "core/volcano/manifests" || e.Category != "core" {
				t.Errorf("%s wires volcano wrongly: %+v", appset, e)
			}
		}
		if !found {
			t.Errorf("%s has no volcano element, so the Marketplace cannot list it", appset)
		}
		// The Marketplace and `adhar upgrade` edit this file as TEXT, line by line, so
		// the element has to survive their scan as well as a YAML parser.
		raw, err := os.ReadFile(filepath.Join(stackRoot(t), appset))
		if err != nil {
			t.Fatal(err)
		}
		if on, listed := appSetEnabledFlags(raw)["volcano"]; !listed || on {
			t.Errorf("%s: the text scan reads volcano as listed=%v enabled=%v", appset, listed, on)
		}
		if !appSetEnabledFlags(setAppSetFlags(raw, map[string]bool{"volcano": true}))["volcano"] {
			t.Errorf("%s: a Marketplace flip does not reach volcano's enabled line", appset)
		}
	}
}

// The pieces of the render that a chart bump could silently take away. Each has a
// reason in generate-manifests.sh; this is what fails when the generator's anchors
// are satisfied but the outcome is not.
func TestVolcanoRenderKeepsWhatTheChartWouldNot(t *testing.T) {
	pinned := regexp.MustCompile(`^docker\.io/volcanosh/vc-[a-z-]+:v\d+\.\d+\.\d+$`)
	for name, doc := range volcanoDeployments(t) {
		spec, _ := dig(doc, "spec", "template", "spec").(map[string]any)
		if spec["enableServiceLinks"] != false {
			t.Errorf("%s: enableServiceLinks must be false in the shared namespace (CONFLICTS.md §2)", name)
		}
		// A value of 2e9 preempts node-pinned pods, which can only return to their
		// own node. See TestPrometheusNeverPreemptsNodePinnedWorkloads.
		if pc := spec["priorityClassName"]; pc != nil {
			t.Errorf("%s: priorityClassName %v; a Pending scheduler is a capacity problem, not a priority one", name, pc)
		}
		if r, ok := dig(doc, "spec", "replicas").(float64); !ok || r != 1 {
			t.Errorf("%s: one replica by default, got %v", name, dig(doc, "spec", "replicas"))
		}
		c := volcanoContainer(t, doc)
		if img, _ := c["image"].(string); !pinned.MatchString(img) {
			t.Errorf("%s: image %q is not pinned to an exact version", name, img)
		}
		if c["imagePullPolicy"] != "IfNotPresent" {
			t.Errorf("%s: imagePullPolicy %v", name, c["imagePullPolicy"])
		}
		if c["readinessProbe"] == nil || c["livenessProbe"] == nil {
			t.Errorf("%s: the chart ships no probes, so a pod reads Ready before it can serve", name)
		}
		if dig(c, "resources", "requests", "memory") == nil || dig(c, "resources", "limits", "memory") == nil {
			t.Errorf("%s: no memory request+limit; the chart sets none", name)
		}
	}

	// Admission's webhook port opens only after it has patched its configurations,
	// and its /healthz serves TLS once it has certificates: a plain-HTTP probe
	// returns 400 and would kill a healthy pod (rehearsed).
	adm := volcanoContainer(t, volcanoDeployments(t)["volcano-admission"])
	if dig(adm, "readinessProbe", "tcpSocket", "port") != float64(8443) {
		t.Errorf("admission readiness must watch the webhook port: %v", adm["readinessProbe"])
	}
	if dig(adm, "livenessProbe", "httpGet", "scheme") != "HTTPS" {
		t.Errorf("admission liveness must use HTTPS: %v", adm["livenessProbe"])
	}

	// With the JobFlow CRDs gone, their controllers must be off too.
	ctrl := strings.Join(volcanoArgs(t, volcanoDeployments(t)["volcano-controllers"]), " ")
	for _, off := range []string{"-jobflow-controller", "-jobtemplate-controller", "-colocation-config-controller", "-sharding-controller", "-hyperNode-controller"} {
		if !strings.Contains(ctrl, off) {
			t.Errorf("volcano-controllers does not disable %s: %s", off, ctrl)
		}
	}

	crds := map[string]bool{}
	for _, doc := range volcanoObjects(t) {
		name := volcanoName(doc)
		switch volcanoKind(doc) {
		case "CustomResourceDefinition":
			crds[name] = true
			if strings.HasSuffix(name, ".flow.volcano.sh") {
				t.Errorf("%s is installed; JobFlow is ≈0.5 MB of API server memory for a feature the platform defers", name)
			}
			if dig(doc, "metadata", "annotations", "argocd.argoproj.io/sync-options") != "ServerSideApply=true" {
				t.Errorf("%s: large CRDs need ServerSideApply=true", name)
			}
		case "Namespace":
			t.Errorf("Namespace/%s: packages must never ship a Namespace (CONFLICTS.md)", name)
		case "Job":
			t.Errorf("Job/%s: the admission-init hook is replaced by cert-manager", name)
		case "DaemonSet":
			t.Errorf("DaemonSet/%s: the colocation agent is not installed", name)
		}
	}
	for _, must := range []string{"jobs.batch.volcano.sh", "cronjobs.batch.volcano.sh", "queues.scheduling.volcano.sh", "podgroups.scheduling.volcano.sh", "commands.bus.volcano.sh", "hypernodes.topology.volcano.sh"} {
		if !crds[must] {
			t.Errorf("%s is missing; the components' informers would never sync", must)
		}
	}

	// ADR-0011: no upstream namespace may survive, in any field or flag.
	raw := readStackFile(t, volcanoInstall)
	if strings.Contains(raw, "volcano-system") {
		t.Error("the manifest still names volcano-system")
	}
}

// Upstream can create and update every webhook configuration on the cluster and
// approve CSRs. The binary Gets and Updates its own configurations at startup and
// never touches CSRs (rehearsed with its real ServiceAccount token).
func TestVolcanoAdmissionRBACIsNarrowed(t *testing.T) {
	configs := map[string][]string{}
	for _, doc := range volcanoObjects(t) {
		switch volcanoKind(doc) {
		case "MutatingWebhookConfiguration":
			configs["mutatingwebhookconfigurations"] = append(configs["mutatingwebhookconfigurations"], volcanoName(doc))
		case "ValidatingWebhookConfiguration":
			configs["validatingwebhookconfigurations"] = append(configs["validatingwebhookconfigurations"], volcanoName(doc))
		}
	}
	seen := map[string]bool{}
	for _, doc := range volcanoObjects(t) {
		if volcanoKind(doc) != "ClusterRole" || volcanoName(doc) != "volcano-admission" {
			continue
		}
		rules, _ := doc["rules"].([]any)
		for _, r := range rules {
			rule, _ := r.(map[string]any)
			groups, _ := rule["apiGroups"].([]any)
			for _, g := range groups {
				switch g {
				case "certificates.k8s.io":
					t.Errorf("volcano-admission keeps a CSR rule: %v", rule)
				case "admissionregistration.k8s.io":
					resources, _ := rule["resources"].([]any)
					names, _ := rule["resourceNames"].([]any)
					if len(names) == 0 {
						t.Errorf("volcano-admission can write webhook configurations it does not own: %v", rule)
					}
					verbs, _ := rule["verbs"].([]any)
					for _, v := range verbs {
						if v != "get" && v != "update" {
							t.Errorf("volcano-admission has verb %v on webhook configurations", v)
						}
					}
					for _, res := range resources {
						resource, _ := res.(string)
						seen[resource] = true
						if len(names) != len(configs[resource]) {
							t.Errorf("%s: the rule names %d configurations, the manifest has %d; the binary exits if it cannot patch one",
								resource, len(names), len(configs[resource]))
						}
					}
				}
			}
		}
	}
	if !seen["mutatingwebhookconfigurations"] || !seen["validatingwebhookconfigurations"] {
		t.Errorf("the binary must be able to write the caBundle on both webhook kinds, found rules for %v", seen)
	}
}

// Volcano's kinds get a health check in all four Argo CD files, and its per-run
// objects stay out of the application controller's cache. The manifests are hand
// edited while the Argo CD upgrade is unfinished, so this holds them together
// until the next generation run does.
func TestArgoCDKnowsVolcanoInEveryRendering(t *testing.T) {
	health := []string{
		"resource.customizations.health.batch.volcano.sh_Job",
		"resource.customizations.health.batch.volcano.sh_CronJob",
		"resource.customizations.health.scheduling.volcano.sh_Queue",
		"resource.customizations.health.scheduling.volcano.sh_PodGroup",
	}
	for _, file := range []string{
		"resources/argocd/install.yaml",
		"resources/argocd/install-ha.yaml",
		"../../../hack/argocd/values.yaml",
		"../../../hack/argocd/values-ha.yaml",
	} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, key := range health {
			if !strings.Contains(src, key+": |") {
				t.Errorf("%s has no %s", file, key)
			}
		}
		// A check that always says Healthy hides a failed gang.
		i := strings.Index(src, "resource.customizations.health.batch.volcano.sh_Job")
		if i >= 0 {
			body := src[i:]
			if j := strings.Index(body, "return hs"); j > 0 {
				body = body[:j]
			}
			for _, want := range []string{`"Completed"`, `"Failed"`, `"Degraded"`, `"Progressing"`} {
				if !strings.Contains(body, want) {
					t.Errorf("%s: the Volcano Job check does not consider %s", file, want)
				}
			}
		}
		i = strings.Index(src, "resource.exclusions: |")
		if i < 0 {
			t.Errorf("%s: no resource.exclusions block", file)
			continue
		}
		var lines []string
		for _, l := range strings.Split(src[i:], "\n")[1:] {
			line := strings.TrimSpace(l)
			if line == "" || !(strings.HasPrefix(line, "-") || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "kinds:")) {
				break
			}
			lines = append(lines, strings.TrimSpace(l))
		}
		block := strings.Join(lines, "\n") + "\n"
		for _, want := range []string{"- scheduling.volcano.sh\nkinds:\n- PodGroup\n", "- bus.volcano.sh\nkinds:\n- Command\n"} {
			if !strings.Contains(block, want) {
				t.Errorf("%s: resource.exclusions lacks %q", file, want)
			}
		}
	}
}
