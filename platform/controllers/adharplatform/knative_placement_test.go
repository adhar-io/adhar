package adharplatform

// Where Knative runs and how a function is reached: the ADR-0011 exception for
// Knative's data planes and the edge contract of ADR-0027. Each guard is a
// failure the GCP cluster showed on 2026-10-26, while KnativeServing and
// KnativeEventing sat in adhar-system.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
)

const (
	knativePackageDir = "application/knative"
	knativeServingNS  = "knative-serving"
	knativeEventingNS = "knative-eventing"
)

// knativeObject is one parsed document of the knative package, with the file it
// came from for error messages.
type knativeObject struct {
	file string
	doc  map[string]any
}

func knativeObjects(t *testing.T) []knativeObject {
	t.Helper()
	dir := filepath.Join(stackPackagesDir(t), filepath.FromSlash(knativePackageDir), "manifests")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []knativeObject
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		for _, doc := range yamlDocs(t, filepath.Join(dir, e.Name())) {
			out = append(out, knativeObject{file: e.Name(), doc: doc})
		}
	}
	return out
}

// knativeCR returns the knative package's only object of kind, failing when
// there is not exactly one.
func knativeCR(t *testing.T, kind string) map[string]any {
	t.Helper()
	var found []map[string]any
	for _, o := range knativeObjects(t) {
		if o.doc["kind"] == kind {
			found = append(found, o.doc)
		}
	}
	if len(found) != 1 {
		t.Fatalf("the knative package ships %d %s objects, want exactly 1", len(found), kind)
	}
	return found[0]
}

func knativeString(doc map[string]any, path ...string) string {
	s, _ := dig(doc, path...).(string)
	return s
}

// The operator installs Serving and Eventing into the namespace of their CR. In
// adhar-system that put Knative's webhook beside kpack's — both compiled with
// the leader-election component name `webhook` and Secret/webhook-certs, so one
// of them never got a certificate — wrote Knative's config-* ConfigMaps over
// Tekton's on every reconcile, and injected WEBHOOK_PORT into every platform
// pod through Service/webhook. Worse, the operator's finalizer uninstalls every
// non-CRD object of its manifest, Namespace included: deleting a CR that lives
// in adhar-system runs `DELETE namespace adhar-system`.
//
// A CR with no namespace is the same fault: Argo CD puts it in the
// Application's destination, which is adhar-system.
func TestKnativeComponentsNeverInstallIntoThePlatformNamespace(t *testing.T) {
	want := map[string]string{"KnativeServing": knativeServingNS, "KnativeEventing": knativeEventingNS}
	found := map[string][]string{}
	for _, f := range stackPackageManifests(t) {
		for _, doc := range yamlDocs(t, f) {
			kind, _ := doc["kind"].(string)
			if _, ok := want[kind]; !ok {
				continue
			}
			ns := knativeString(doc, "metadata", "namespace")
			found[kind] = append(found[kind], fmt.Sprintf("%s (%s)", nonEmpty(ns, "no namespace"), rel(t, f)))
			if ns != want[kind] {
				t.Errorf("%s: %s %v is in %q; Knative's data planes run in %s (ADR-0011 exception) — "+
					"in adhar-system they collide with kpack and Tekton, and the operator's uninstall deletes the namespace",
					rel(t, f), kind, metaName(doc), ns, want[kind])
			}
		}
	}
	for kind := range want {
		if len(found[kind]) != 1 {
			t.Errorf("the stack ships %d %s objects (%s), want exactly one", len(found[kind]), kind, strings.Join(found[kind], ", "))
		}
	}
}

// The namespaces carry the ADR-0023 placement label, because the plane and
// supply-chain admission policies treat `adhar.io/plane: control` as "platform
// services" and everything else as tenant workloads. The operator re-applies
// its own copy of each Namespace on every reconcile, so the CRs must ask for the
// label as well or the two writers disagree. And nothing may ship the platform
// namespace itself: an Application that tracks Namespace/adhar-system deletes it
// when it prunes.
func TestKnativeNamespacesCarryThePlacementLabel(t *testing.T) {
	declared := map[string]bool{}
	for _, o := range knativeObjects(t) {
		if o.doc["kind"] != "Namespace" {
			continue
		}
		name, _ := metaName(o.doc).(string)
		declared[name] = true
		if metaLabels(o.doc)["adhar.io/plane"] != "control" {
			t.Errorf("%s: Namespace %s does not carry adhar.io/plane: control", o.file, name)
		}
	}
	for _, ns := range []string{knativeServingNS, knativeEventingNS} {
		if !declared[ns] {
			t.Errorf("the knative package does not declare Namespace %s", ns)
		}
	}
	for _, kind := range []string{"KnativeServing", "KnativeEventing"} {
		if got := knativeString(knativeCR(t, kind), "spec", "namespace", "labels", "adhar.io/plane"); got != "control" {
			t.Errorf("%s spec.namespace.labels[adhar.io/plane] = %q, want control: the operator re-applies the namespace it installs into", kind, got)
		}
	}
	for _, f := range stackPackageManifests(t) {
		for _, doc := range yamlDocs(t, f) {
			if doc["kind"] == "Namespace" && metaName(doc) == globals.AdharSystemNamespace {
				t.Errorf("%s ships Namespace/%s; pruning that Application would delete the platform", rel(t, f), globals.AdharSystemNamespace)
			}
		}
	}
}

// Kourier is reached only through the platform Gateway. Upstream's default is a
// LoadBalancer, which on GCP was a billed public address (34.124.203.128) that
// bypassed the Gateway's TLS, external-dns and SSO — a second edge nobody had
// decided to have.
func TestKourierIsNeverAPublicLoadBalancer(t *testing.T) {
	ks := knativeCR(t, "KnativeServing")
	if enabled, _ := dig(ks, "spec", "ingress", "kourier", "enabled").(bool); !enabled {
		t.Fatal("KnativeServing does not enable Kourier; the Function edge contract (ADR-0027) routes the Gateway to Service kourier")
	}
	if got := knativeString(ks, "spec", "ingress", "kourier", "service-type"); got != "ClusterIP" {
		t.Errorf("spec.ingress.kourier.service-type = %q, want ClusterIP; anything else publishes functions outside the platform Gateway", got)
	}
	for _, other := range []string{"istio", "contour", "gateway-api"} {
		if on, _ := dig(ks, "spec", "ingress", other, "enabled").(bool); on {
			t.Errorf("spec.ingress.%s is enabled; Kourier is the one ingress behind the Gateway", other)
		}
	}
	if got := knativeString(ks, "spec", "config", "network", "ingress-class"); got != "kourier.ingress.networking.knative.dev" {
		t.Errorf("config.network.ingress-class = %q: Kourier is installed but routes would be programmed elsewhere", got)
	}
}

// Knative resolves every image tag to a digest before it creates a revision,
// and the platform's in-cluster Harbor serves a certificate the controller does
// not trust. Without the skip, every function the pipelines build fails with
// x509 before a pod exists. The registry is read from the Project composition,
// which is where the platform decides what its pipelines push to.
func TestKnativeSkipsTagResolutionForTheInClusterHarbor(t *testing.T) {
	comp, err := os.ReadFile(filepath.Join("..", "..", "controlplane", "configuration", "compositions", "project", "local.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`dig "imageRegistry" "([^"/]+)/`).FindSubmatch(comp)
	if m == nil {
		t.Fatal("cannot find the Project composition's default imageRegistry; update this guard")
	}
	harbor := string(m[1])
	skip := knativeString(knativeCR(t, "KnativeServing"), "spec", "config", "deployment", "registries-skipping-tag-resolving")
	var registries []string
	for _, r := range strings.Split(skip, ",") {
		registries = append(registries, strings.TrimSpace(r))
	}
	if !slices.Contains(registries, harbor) {
		t.Errorf("config.deployment.registries-skipping-tag-resolving = %q does not include %s, the registry the platform's pipelines push to", skip, harbor)
	}
}

// The operator's own ConfigMaps live in adhar-system with every other platform
// controller. Upstream calls them config-logging and config-observability —
// knative.dev/pkg's defaults, which Tekton ships in adhar-system too — so two
// Applications owned one object and flapped it OutOfSync on every sync. Renamed
// objects only help if the env vars that name them follow.
func TestKnativeOperatorConfigMapsDoNotCollide(t *testing.T) {
	defaults := []string{"config-logging", "config-observability", "config-leader-election", "config-features"}
	operator := map[string]bool{}
	var deployments []map[string]any
	for _, o := range knativeObjects(t) {
		if o.file != "operator.yaml" {
			continue
		}
		switch o.doc["kind"] {
		case "ConfigMap":
			name, _ := metaName(o.doc).(string)
			operator[name] = true
		case "Deployment":
			deployments = append(deployments, o.doc)
		}
	}
	if len(operator) == 0 || len(deployments) == 0 {
		t.Fatalf("operator.yaml has %d ConfigMaps and %d Deployments; the walker is broken", len(operator), len(deployments))
	}
	for name := range operator {
		if slices.Contains(defaults, name) {
			t.Errorf("operator.yaml ships ConfigMap/%s, a knative.dev/pkg default name other packages in adhar-system use too; "+
				"rename it in generate-manifests.sh", name)
		}
	}
	for _, f := range stackPackageManifests(t) {
		if strings.Contains(filepath.ToSlash(f), "/"+knativePackageDir+"/") {
			continue
		}
		for _, doc := range yamlDocs(t, f) {
			if name, _ := metaName(doc).(string); doc["kind"] == "ConfigMap" && operator[name] {
				t.Errorf("%s also ships ConfigMap/%s, which the Knative operator owns in adhar-system", rel(t, f), name)
			}
		}
	}
	for _, d := range deployments {
		for _, c := range containersOf(d["spec"].(map[string]any)) {
			env, _ := c["env"].([]any)
			for _, raw := range env {
				e, _ := raw.(map[string]any)
				switch e["name"] {
				case "CONFIG_LOGGING_NAME", "CONFIG_OBSERVABILITY_NAME":
					if v, _ := e["value"].(string); !operator[v] {
						t.Errorf("Deployment/%v sets %v=%q, which names no ConfigMap in operator.yaml; it would read another package's",
							metaName(d), e["name"], v)
					}
				}
			}
		}
	}
}

// Both operator Deployments read knative.dev/pkg's *_PORT-shaped variables from
// the environment, in a namespace of ~300 Services (the standing ADR-0011 rule).
func TestKnativeOperatorRunsWithoutServiceLinks(t *testing.T) {
	seen := 0
	for _, o := range knativeObjects(t) {
		if o.file != "operator.yaml" || o.doc["kind"] != "Deployment" {
			continue
		}
		seen++
		if links, present := dig(o.doc, "spec", "template", "spec", "enableServiceLinks").(bool); !present || links {
			t.Errorf("Deployment/%v does not set enableServiceLinks: false", metaName(o.doc))
		}
	}
	if seen != 2 {
		t.Errorf("operator.yaml has %d Deployments, want the operator and its webhook", seen)
	}
}

// An aggregated ClusterRole's rules belong to the controller manager. An
// Application that declares them — even as `rules: []` — is OutOfSync forever
// and strips the aggregated permissions on every self-heal: the knative app was
// permanently OutOfSync on its two -aggregated-stable roles.
func TestAggregatedClusterRolesLeaveRulesToTheControllerManager(t *testing.T) {
	seen := 0
	for _, f := range stackPackageManifests(t) {
		for _, doc := range yamlDocs(t, f) {
			if doc["kind"] != "ClusterRole" || doc["aggregationRule"] == nil {
				continue
			}
			seen++
			if _, declared := doc["rules"]; declared {
				t.Errorf("%s: ClusterRole/%v has an aggregationRule and also declares rules; drop them in the generator",
					rel(t, f), metaName(doc))
			}
		}
	}
	if seen == 0 {
		t.Fatal("found no aggregated ClusterRole in the stack; the walker is broken")
	}
}

// Since Knative moved to OpenTelemetry its components export metrics only when
// asked (metrics-protocol defaults to none), and nothing scrapes them unless a
// monitor exists. Monitoring CRs sync at wave 10 like every package's.
func TestKnativeServingControlPlaneIsScraped(t *testing.T) {
	ks := knativeCR(t, "KnativeServing")
	if got := knativeString(ks, "spec", "config", "observability", "metrics-protocol"); got != "prometheus" {
		t.Errorf("config.observability.metrics-protocol = %q, want prometheus; the components serve no /metrics otherwise", got)
	}
	covered := map[string]bool{}
	for _, o := range knativeObjects(t) {
		kind := o.doc["kind"]
		if kind != "ServiceMonitor" && kind != "PodMonitor" {
			continue
		}
		if ns := knativeString(o.doc, "metadata", "namespace"); ns != knativeServingNS {
			t.Errorf("%s: %v/%v is in %q, not %s", o.file, kind, metaName(o.doc), ns, knativeServingNS)
		}
		if wave, _ := metaAnnotations(o.doc)["argocd.argoproj.io/sync-wave"].(string); wave != "10" {
			t.Errorf("%s: %v/%v has sync-wave %q, want \"10\"", o.file, kind, metaName(o.doc), wave)
		}
		if knativeString(o.doc, "spec", "selector", "matchLabels", "app.kubernetes.io/name") != "knative-serving" {
			continue
		}
		exprs, _ := dig(o.doc, "spec", "selector", "matchExpressions").([]any)
		for _, raw := range exprs {
			e, _ := raw.(map[string]any)
			if e["key"] != "app.kubernetes.io/component" || e["operator"] != "In" {
				continue
			}
			values, _ := e["values"].([]any)
			for _, v := range values {
				covered[fmt.Sprint(v)] = true
			}
		}
	}
	var missing []string
	for _, c := range []string{"activator", "autoscaler", "controller"} {
		if !covered[c] {
			missing = append(missing, c)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("no ServiceMonitor/PodMonitor in %s selects Knative Serving's %s", knativeServingNS, strings.Join(missing, ", "))
	}
}

// A Knative Service that is not cluster-local is published at
// <name>.<namespace>.<domain>. The domain is written in the stack's local host
// convention so that staging rewrites it to the cluster's host, as it does every
// other hostname; a literal cloud host here would be wrong on every other
// cluster, and a .tmpl would be a second mechanism for the same thing.
func TestKnativeDomainIsTheStagedPlatformHost(t *testing.T) {
	domains, _ := dig(knativeCR(t, "KnativeServing"), "spec", "config", "domain").(map[string]any)
	if len(domains) != 1 || domains[globals.DefaultHostName] != "" {
		t.Fatalf("config.domain = %v, want exactly {%q: \"\"} (the local host stageStack rewrites)", domains, globals.DefaultHostName)
	}

	src := t.TempDir()
	serving, err := os.ReadFile(filepath.Join(stackPackagesDir(t), filepath.FromSlash(knativePackageDir), "manifests", "serving.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(src, "serving.yaml"), string(serving))
	spec := v1alpha1.BuildCustomizationSpec{Protocol: "https", Host: "cloud.example.io", Port: "443"}
	spec.Normalize()
	out, cleanup, err := stageStack(src, spec, spec.TXTOwnerID())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	staged := read(t, filepath.Join(out, "serving.yaml"))
	if !strings.Contains(staged, "cloud.example.io: \"\"") || strings.Contains(staged, globals.DefaultHostName) {
		t.Errorf("staging for host cloud.example.io did not rewrite config.domain:\n%s", staged)
	}
}

// Upstream's PodDisruptionBudgets say minAvailable 80%, which assumes several
// replicas. The platform runs one, and 80% of one is one: zero allowed
// disruptions, so no node drain, kubeadm upgrade or autoscaler scale-down can
// evict the pod (all four read disruptionsAllowed=0 on the GCP cluster).
func TestKnativeSingleReplicaComponentsStayDrainable(t *testing.T) {
	for kind, budgets := range map[string][]string{
		"KnativeServing":  {"activator-pdb", "webhook-pdb", "3scale-kourier-gateway-pdb"},
		"KnativeEventing": {"eventing-webhook"},
	} {
		cr := knativeCR(t, kind)
		if replicas, _ := dig(cr, "spec", "high-availability", "replicas").(int); replicas != 1 {
			t.Errorf("%s spec.high-availability.replicas = %v, want 1 (one copy per component)", kind, dig(cr, "spec", "high-availability", "replicas"))
		}
		overrides := map[string]map[string]any{}
		list, _ := dig(cr, "spec", "podDisruptionBudgets").([]any)
		for _, raw := range list {
			if o, ok := raw.(map[string]any); ok {
				name, _ := o["name"].(string)
				overrides[name] = o
			}
		}
		for _, name := range budgets {
			o, ok := overrides[name]
			if !ok {
				t.Errorf("%s does not override PodDisruptionBudget %s; upstream's minAvailable 80%% blocks every drain of a single replica", kind, name)
				continue
			}
			if _, set := o["minAvailable"]; set {
				t.Errorf("%s PodDisruptionBudget %s sets minAvailable; a single replica needs maxUnavailable", kind, name)
			}
			if n, _ := o["maxUnavailable"].(int); n < 1 {
				t.Errorf("%s PodDisruptionBudget %s maxUnavailable = %v, want at least 1", kind, name, o["maxUnavailable"])
			}
		}
	}
}
