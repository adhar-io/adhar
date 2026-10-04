package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// ADR-0026: Organisation → Team → Project → Application → Environment → Release
// is one declarative control plane with one label vocabulary. These tests pin
// the agreements between its parts that nothing at apply time would catch.

var hierarchyXRDs = []string{"organisation", "team", "project", "apps", "env", "release", "agentworkload"}

func readYAMLDocs(t *testing.T, path string) []map[string]interface{} {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var docs []map[string]interface{}
	for _, d := range strings.Split(string(b), "\n---\n") {
		var m map[string]interface{}
		if err := yaml.Unmarshal([]byte(d), &m); err != nil {
			t.Fatalf("%s does not parse: %v", path, err)
		}
		if len(m) > 0 {
			docs = append(docs, m)
		}
	}
	return docs
}

func dig(m map[string]interface{}, path ...string) interface{} {
	var cur interface{} = m
	for _, p := range path {
		mm, ok := cur.(map[string]interface{})
		if !ok {
			return nil
		}
		cur = mm[p]
	}
	return cur
}

// Every XRD of the hierarchy names a default composition that exists and
// targets its kind. A dangling defaultCompositionRef is an XR that never
// renders and never says why.
func TestHierarchyXRDsHaveTheirCompositions(t *testing.T) {
	comps := map[string]string{} // composition name -> compositeTypeRef.kind
	err := filepath.WalkDir(filepath.Join(controlPlaneDir(t), "compositions"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return err
		}
		for _, doc := range readYAMLDocs(t, path) {
			if doc["kind"] == "Composition" {
				name, _ := dig(doc, "metadata", "name").(string)
				kind, _ := dig(doc, "spec", "compositeTypeRef", "kind").(string)
				comps[name] = kind
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range hierarchyXRDs {
		docs := readYAMLDocs(t, filepath.Join(controlPlaneDir(t), "xrd", x+".xrd.yaml"))
		kind, _ := dig(docs[0], "spec", "names", "kind").(string)
		def, _ := dig(docs[0], "spec", "defaultCompositionRef", "name").(string)
		if def == "" {
			t.Errorf("%s: no defaultCompositionRef", x)
			continue
		}
		if comps[def] != kind {
			t.Errorf("%s: default composition %q targets %q, not %s", x, def, comps[def], kind)
		}
	}
}

// Three environments exist on this platform — dev, test, prod — everywhere:
// the Kargo stages, the environments repository, the CLI (environment_model_test)
// and now every enum in the hierarchy's XRDs. `staging` had survived in the
// project and environment XRDs after it was removed everywhere else.
func TestHierarchyTiersAreDevTestProd(t *testing.T) {
	want := `["dev","test","prod"]`
	for _, x := range []string{"project", "env", "release"} {
		b, err := os.ReadFile(filepath.Join(controlPlaneDir(t), "xrd", x+".xrd.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "staging") {
			t.Errorf("%s.xrd.yaml still knows a `staging` tier", x)
		}
		enums := regexp.MustCompile(`enum:\s*\n((?:\s+- \w+\n)+)|enum: \[([^\]]+)\]`).FindAllStringSubmatch(string(b), -1)
		if len(enums) == 0 {
			t.Errorf("%s.xrd.yaml declares no tier/environment enum", x)
		}
		for _, e := range enums {
			var items []string
			if e[1] != "" {
				for _, l := range strings.Split(strings.TrimSpace(e[1]), "\n") {
					items = append(items, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "-")))
				}
			} else {
				for _, i := range strings.Split(e[2], ",") {
					items = append(items, strings.Trim(strings.TrimSpace(i), `"'`))
				}
			}
			got := `["` + strings.Join(items, `","`) + `"]`
			// Access and promotion enums are allowed; environment enums must be exact.
			if strings.Contains(got, "dev") && got != want {
				t.Errorf("%s.xrd.yaml: environment enum %s, want %s", x, got, want)
			}
		}
	}
}

// One label vocabulary. Every Namespace a hierarchy composition emits carries
// `adhar.io/organisation` and `adhar.io/plane: workload`; the retired
// `platform.adhar.io/organisation|team|project` and `adhar.io/app` spellings
// appear nowhere, so one selector can answer "everything belonging to team X".
func TestHierarchyLabelsAreUniform(t *testing.T) {
	retired := regexp.MustCompile(`platform\.adhar\.io/(organisation|organization|team|project)|adhar\.io/app:`)
	files := []string{
		"compositions/organisation/local.yaml", "compositions/team/local.yaml", "compositions/project/local.yaml",
		"compositions/env/local.yaml", "compositions/apps/argocd-multienv.yaml", "compositions/agent/kubernetes.yaml",
		"compositions/release/kargo.yaml",
	}
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(controlPlaneDir(t), f))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if m := retired.FindString(s); m != "" {
			t.Errorf("%s uses the retired label %q", f, m)
		}
		// Every Namespace manifest in the template must carry the vocabulary.
		for _, idx := range regexp.MustCompile(`kind: Namespace\n`).FindAllStringIndex(s, -1) {
			block := s[idx[1]:]
			if end := strings.Index(block, "\n            ---"); end > 0 {
				block = block[:end]
			}
			// Either spelled out, or stamped from the file's shared $labels dict.
			viaDict := strings.Contains(block, "range $k, $v := $labels") && strings.Contains(s, `"adhar.io/organisation" $org`)
			if !strings.Contains(block, "adhar.io/organisation") && !viaDict {
				t.Errorf("%s: a composed Namespace lacks adhar.io/organisation", f)
			}
			if !strings.Contains(block, "adhar.io/plane: workload") {
				t.Errorf("%s: a composed Namespace lacks adhar.io/plane: workload", f)
			}
		}
	}
	task, err := os.ReadFile(filepath.Join(stackPackagesDir(t), "application/adhar-supply-chain/manifests/90-app-environments.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if retired.MatchString(string(task)) {
		t.Error("the paved-road onboarding Task still stamps a retired label")
	}
}

// The project's promotion policy is the platform's own: dev and test promote
// themselves, prod never does — the rule application/kargo applies to the
// platform's packages. It is keyed on the environment LABEL the application
// composition stamps on every Stage, so one policy per project covers every
// application in it.
func TestProjectPromotionPolicyMatchesThePlatformPipeline(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(controlPlaneDir(t), "compositions/project/local.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	comp := string(b)
	if !strings.Contains(comp, `(ne $en "prod")`) {
		t.Error("the project composition no longer defaults autoPromote to `environment != prod`")
	}
	if !strings.Contains(comp, "kind: ProjectConfig") || !strings.Contains(comp, "adhar.io/environment: {{ .name | quote }}\n                        autoPromotionEnabled: {{ .autoPromote }}") {
		t.Error("the Kargo ProjectConfig must select Stages by their adhar.io/environment label and carry the per-environment autoPromote flag")
	}
	apps, err := os.ReadFile(filepath.Join(controlPlaneDir(t), "compositions/apps/argocd-multienv.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(apps), "kind: Stage") || !strings.Contains(string(apps), "adhar.io/environment: {{ quote $envName }}") {
		t.Error("the application composition must label every Kargo Stage with adhar.io/environment, or the project's promotion policy selects nothing")
	}
	// The platform's own pipeline says the same thing.
	for _, doc := range readYAMLDocs(t, filepath.Join(stackPackagesDir(t), "application/kargo/manifests/promotion-pipeline.yaml")) {
		if doc["kind"] != "ProjectConfig" {
			continue
		}
		pols, _ := dig(doc, "spec", "promotionPolicies").([]interface{})
		got := map[string]bool{}
		for _, p := range pols {
			pm := p.(map[string]interface{})
			name, _ := dig(pm, "stageSelector", "name").(string)
			if name == "" {
				name, _ = pm["stage"].(string)
			}
			auto, _ := pm["autoPromotionEnabled"].(bool)
			got[name] = auto
		}
		if !got["dev"] || !got["test"] || got["prod"] {
			t.Errorf("platform promotion policy is %v; the project default (dev/test auto, prod manual) must match it", got)
		}
	}
}

// An application never guesses where an environment lives: it observes its
// project (read-only) and reads status.environments. Composing namespaces by
// convention was how `<app>-<env>` and `<project>-<env>` drifted apart.
func TestApplicationReadsEnvironmentsFromItsProject(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(controlPlaneDir(t), "compositions/apps/argocd-multienv.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`managementPolicies: ["Observe"]`, "kind: CompositeProject", `dig "environments" (list) $projStatus`, `namespace: {{ quote $ns }}`} {
		if !strings.Contains(s, want) {
			t.Errorf("application composition lost %q — it must observe the project and use its namespaces", want)
		}
	}
	if regexp.MustCompile(`\$ns :=.*printf "%s-%s"`).MatchString(s) {
		t.Error("the application composition derives a namespace from `<app>-<env>` again")
	}
	// The project publishes exactly what the application reads.
	pb, err := os.ReadFile(filepath.Join(controlPlaneDir(t), "compositions/project/local.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"environments:\n                {{- range $resolved }}\n                - name: {{ .name }}\n                  namespace: {{ .namespace }}", "imageRegistry: {{ $registry }}"} {
		if !strings.Contains(string(pb), want) {
			t.Errorf("project composition no longer publishes %q on its status", strings.Fields(want)[0])
		}
	}
}

// A release is a Kargo Promotion with ONLY stage and freight: the steps come
// from the Stage's template through Kargo's webhook, so a release can never
// carry a pipeline that drifts from the Stage's.
func TestReleaseComposesAPromotionWithoutItsOwnSteps(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(controlPlaneDir(t), "compositions/release/kargo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	// `| quote`: a digits-only freight hash renders as an integer otherwise and
	// Kargo refuses the Promotion (seen live, 2026-10-04).
	for _, want := range []string{"kind: Promotion", "stage: {{ $stage | quote }}", "freight: {{ $freight | quote }}"} {
		if !strings.Contains(s, want) {
			t.Errorf("release composition lost %q", want)
		}
	}
	if regexp.MustCompile(`(?m)^\s+steps:`).MatchString(s) {
		t.Error("the release composition spells out promotion steps; they belong to the Stage")
	}
}

// Both identities that create composed objects must be allowed to create the
// kinds the hierarchy emits — Kargo's, ESO's and RBAC's included.
func TestHierarchyComposedKindsAreGranted(t *testing.T) {
	for _, f := range []string{"rbac/local-compose-rbac.yaml", "providers/provider-runtime-rbac.yaml"} {
		b, err := os.ReadFile(filepath.Join(controlPlaneDir(t), f))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		for _, want := range []string{"projects", "projectconfigs", "warehouses", "stages", "promotions", "externalsecrets", "appprojects", "rolebindings", "limitranges", "namespaces", "jobs"} {
			if !strings.Contains(s, `"`+want+`"`) {
				t.Errorf("%s does not grant %s, which a hierarchy composition emits", f, want)
			}
		}
	}
}

// k8sgpt is a platform service identity on the AI gateway: its ServiceAccount
// token, one model, no tools. The CR and its grant must name the same model
// and the same gateway, or k8sgpt silently gets 403 on every analysis.
func TestK8sGPTUsesOneModelThroughTheGateway(t *testing.T) {
	docs := readYAMLDocs(t, filepath.Join(stackPackagesDir(t), "ai/k8sgpt/manifests/k8sgpt.yaml"))
	var model, baseURL, rule, tokenSA string
	var jobMintsForGateway bool
	for _, d := range docs {
		switch d["kind"] {
		case "K8sGPT":
			model, _ = dig(d, "spec", "ai", "model").(string)
			baseURL, _ = dig(d, "spec", "ai", "baseUrl").(string)
			tokenSA, _ = dig(d, "spec", "ai", "secret", "name").(string)
		case "AgentgatewayPolicy":
			exprs, _ := dig(d, "spec", "traffic", "authorization", "policy", "matchExpressions").([]interface{})
			if len(exprs) != 1 {
				t.Fatalf("k8sgpt's grant has %d matchExpressions; the gateway OR's them, so it must be one", len(exprs))
			}
			rule = exprs[0].(string)
		case "Job":
			args, _ := dig(d, "spec", "template", "spec", "containers").([]interface{})
			for _, c := range args {
				for _, a := range c.(map[string]interface{})["args"].([]interface{}) {
					if strings.Contains(a.(string), "--audience agentgateway") && strings.Contains(a.(string), "k8sgpt-gateway") {
						jobMintsForGateway = true
					}
				}
			}
		}
	}
	if model == "" || !strings.Contains(rule, `== "`+model+`"`) {
		t.Errorf("K8sGPT asks for model %q but its gateway grant allows %q", model, rule)
	}
	if !strings.Contains(baseURL, "adhar-ai-gateway.adhar-system.svc.cluster.local") {
		t.Errorf("K8sGPT must reach models through the gateway, not %q", baseURL)
	}
	if !strings.Contains(rule, `"system:serviceaccount:adhar-system:k8sgpt-gateway"`) {
		t.Error("the grant must be bound to the k8sgpt-gateway ServiceAccount subject")
	}
	if tokenSA != "k8sgpt-gateway-token" || !jobMintsForGateway {
		t.Error("the token Job must mint an `agentgateway`-audience token for k8sgpt-gateway into the Secret the CR reads")
	}
	// Enabled exactly where agentgateway is.
	for _, f := range []string{"adhar-appset-production.yaml", "adhar-appset-local.yaml", "adhar-appset-gitops.yaml"} {
		b, err := os.ReadFile(filepath.Join(stackPackagesDir(t), "..", f))
		if err != nil {
			t.Fatal(err)
		}
		en := func(name string) string {
			m := regexp.MustCompile(`- name: "` + name + `"\n\s+enabled: "(true|false)"`).FindStringSubmatch(string(b))
			if m == nil {
				return "missing"
			}
			return m[1]
		}
		if en("k8sgpt") != en("agentgateway") {
			t.Errorf("%s: k8sgpt enabled=%s but agentgateway enabled=%s — k8sgpt has no model without the gateway", f, en("k8sgpt"), en("agentgateway"))
		}
	}
}
