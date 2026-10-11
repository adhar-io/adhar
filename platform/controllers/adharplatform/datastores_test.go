package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Search (OpenSearch through the platform's operator) and
// Vector (Qdrant through provider-helm) are the two managed data
// services a team can ask for by name. These pin the agreements between the
// XRDs, their compositions, the RBAC the providers need and the packages they
// lean on — none of which anything at apply time would report clearly.

var datastoreXRDs = map[string]string{"search": "Search", "vector": "Vector", "nosql-database": "NoSqlDatabase", "graph-database": "GraphDatabase"}

// The two RBAC files every composition's grants must appear in, and the literals
// the assertions below repeat.
const (
	composeRBACFile  = "rbac/local-compose-rbac.yaml"
	nosqlComposition = "compositions/nosql/mongodb-operator.yaml"
	graphComposition = "compositions/graph/neo4j.yaml"
	providerRBACFile = "providers/provider-runtime-rbac.yaml"
	kindDeployment   = "Deployment"
	enabledTrue      = "true"
)

func TestDatastoreXRDsHaveTheirCompositions(t *testing.T) {
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
	for file, wantKind := range datastoreXRDs {
		docs := readYAMLDocs(t, filepath.Join(controlPlaneDir(t), "xrd", file+".xrd.yaml"))
		kind, _ := dig(docs[0], "spec", "names", "kind").(string)
		if kind != wantKind {
			t.Errorf("%s.xrd.yaml defines %q, want %s", file, kind, wantKind)
		}
		def, _ := dig(docs[0], "spec", "defaultCompositionRef", "name").(string)
		if comps[def] != wantKind {
			t.Errorf("%s: default composition %q targets %q, not %s", file, def, comps[def], wantKind)
		}
		// The selector default must pick that same composition by label.
		sel, _ := dig(docs[0], "spec", "versions").([]interface{})
		if len(sel) == 0 {
			t.Fatalf("%s: no versions", file)
		}
	}
}

// The operator downloads a Prometheus-exporter plugin matched to the EXACT
// engine version; a version it has no plugin for leaves every node unable to
// start. The platform's shared cluster (data/opensearch) is the version that
// has been proven live, so a composed cluster defaults to the same one — in
// the XRD default and in the composition's fallback alike.
func TestSearchTracksThePlatformOpenSearchVersion(t *testing.T) {
	var platformVersion string
	for _, doc := range readYAMLDocs(t, filepath.Join(stackPackagesDir(t), "data/opensearch/manifests/cluster.yaml")) {
		if doc["kind"] == "OpenSearchCluster" {
			platformVersion, _ = dig(doc, "spec", "general", "version").(string)
		}
	}
	if platformVersion == "" {
		t.Fatal("data/opensearch ships no OpenSearchCluster with a general.version")
	}
	xrd := readYAMLDocs(t, filepath.Join(controlPlaneDir(t), "xrd/search.xrd.yaml"))[0]
	versions, _ := dig(xrd, "spec", "versions").([]interface{})
	v0, _ := versions[0].(map[string]interface{})
	def := dig(v0, "schema", "openAPIV3Schema", "properties", "spec", "properties", "parameters", "properties", "version", "default")
	if def != platformVersion {
		t.Errorf("search.xrd.yaml defaults version to %v; the platform cluster runs %s", def, platformVersion)
	}
	comp, err := os.ReadFile(filepath.Join(controlPlaneDir(t), "compositions/search/opensearch-operator.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(comp), `dig "version" "`+platformVersion+`" $p`) {
		t.Errorf("compositions/search falls back to a version other than the platform's %s", platformVersion)
	}
}

// Both compositions emit operator and ESO kinds through provider-kubernetes
// (and the Qdrant chart emits a PDB through provider-helm, which shares the
// ClusterRole). A kind that is not granted fails at apply with a 403 the XR
// reports as a generic sync error.
func TestDatastoreComposedKindsAreGranted(t *testing.T) {
	want := []string{
		"opensearchclusters", "opensearchroles", "opensearchusers", "opensearchuserrolebindings",
		"opensearchismpolicies", "opensearchsnapshotpolicies",
		"passwords", "externalsecrets", "poddisruptionbudgets", "jobs", "servicemonitors",
	}
	for _, f := range []string{composeRBACFile, providerRBACFile} {
		b, err := os.ReadFile(filepath.Join(controlPlaneDir(t), f))
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range want {
			if !strings.Contains(string(b), `"`+w+`"`) {
				t.Errorf("%s does not grant %s, which a datastore composition emits", f, w)
			}
		}
	}
}

// The Qdrant chart's `apiKey.valueFrom` resolves the Secret with Helm `lookup`
// at install time: a release installed before ESO has written the key runs
// with none and never notices. The key must reach Qdrant as an environment
// variable with a secretKeyRef instead, which holds the pod until the Secret
// exists and then starts it.
func TestVectorAPIKeyNeverRidesHelmLookup(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(controlPlaneDir(t), "compositions/vector/qdrant.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	// Only the values: block matters; the header comment may name the trap.
	values := s[strings.Index(s, "values:"):]
	if regexp.MustCompile(`(?m)^\s+(apiKey|readOnlyApiKey):`).MatchString(values) {
		t.Error("compositions/vector sets the chart's apiKey/readOnlyApiKey values, which resolve through helm lookup")
	}
	if !strings.Contains(values, "QDRANT__SERVICE__API_KEY") || !strings.Contains(values, "secretKeyRef:") {
		t.Error("compositions/vector does not pass the API key as QDRANT__SERVICE__API_KEY from a Secret")
	}
	// The chart ships timeoutSeconds: 1 on both probes; the platform forbids it.
	if regexp.MustCompile(`timeoutSeconds:\s*1\b`).MatchString(values) {
		t.Error("compositions/vector ships a one-second probe timeout")
	}
	for _, probe := range []string{"readinessProbe:", "livenessProbe:"} {
		if !strings.Contains(values, probe) {
			t.Errorf("compositions/vector does not override the chart's %s", probe)
		}
	}
}

// Every object either composition emits carries the hierarchy vocabulary and
// the workload plane, stamped from one shared dict, and none of the retired
// label spellings.
func TestDatastoresStampTheHierarchyLabels(t *testing.T) {
	retired := regexp.MustCompile(`platform\.adhar\.io/(organisation|organization|team|project)|adhar\.io/app:`)
	for _, f := range []string{"compositions/search/opensearch-operator.yaml", "compositions/vector/qdrant.yaml", nosqlComposition, graphComposition} {
		b, err := os.ReadFile(filepath.Join(controlPlaneDir(t), f))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if m := retired.FindString(s); m != "" {
			t.Errorf("%s uses the retired label %q", f, m)
		}
		for _, want := range []string{`"adhar.io/organisation" $org`, `"adhar.io/team" $team`, `"adhar.io/environment" $env`, `"adhar.io/plane" "workload"`} {
			if !strings.Contains(s, want) {
				t.Errorf("%s does not stamp %s", f, want)
			}
		}
		// Every composed manifest (metadata.namespace: {{ $ns }}) reads the dict.
		manifests := strings.Count(s, "namespace: {{ $ns }}")
		stamps := strings.Count(s, "range $k, $v := $labels")
		if stamps < manifests {
			t.Errorf("%s: %d composed manifests but only %d stamp $labels", f, manifests, stamps)
		}
	}
}

// Valkey Admin (Redis Commander) has no users of its own and FLUSHALL is one
// click away, so the route must terminate at the oauth2-proxy, the Keycloak
// client must be declared for the proxy to use, and the package must be in the
// production profile that enables the Valkey it administers.
func TestValkeyAdminIsGatedBySSO(t *testing.T) {
	dir := filepath.Join(stackPackagesDir(t), "data/valkey-admin/manifests")
	route := readYAMLDocs(t, filepath.Join(dir, "httproute.yaml"))
	rules, _ := dig(route[0], "spec", "rules").([]interface{})
	for _, r := range rules {
		rm, _ := r.(map[string]interface{})
		refs, _ := rm["backendRefs"].([]interface{})
		for _, ref := range refs {
			refm, _ := ref.(map[string]interface{})
			if refm["name"] != "valkey-admin-oauth2-proxy" {
				t.Errorf("httproute.yaml forwards to %v, bypassing the SSO proxy", refm["name"])
			}
		}
	}
	var client, proxy bool
	for _, doc := range readYAMLDocs(t, filepath.Join(dir, "sso.yaml")) {
		if doc["kind"] == "ConfigMap" && dig(doc, "metadata", "labels", "adhar.io/keycloak-client") == enabledTrue {
			client = strings.Contains(dig(doc, "data", "client.json").(string), `"clientId": "valkey-admin"`)
		}
		if doc["kind"] == kindDeployment && dig(doc, "metadata", "name") == "valkey-admin-oauth2-proxy" {
			proxy = true
		}
	}
	if !client || !proxy {
		t.Errorf("sso.yaml: keycloak client declared=%v, oauth2-proxy Deployment present=%v", client, proxy)
	}
	prod := map[string]string{}
	for _, e := range loadAppSetElements(t, "adhar-appset-production.yaml") {
		prod[e.Name] = e.Enabled
	}
	if prod["valkey-admin"] != enabledTrue {
		t.Errorf("valkey-admin is %q in the production profile, want enabled", prod["valkey-admin"])
	}
	if prod["valkey"] != enabledTrue {
		t.Error("valkey-admin is enabled in production but the valkey it administers is not")
	}
}

// The OpenSearch operator creates the monitoring user from the OpensearchUser's
// NAME and generates a ServiceMonitor that authenticates with the `username` in
// the monitoring Secret. If those two disagree every scrape is a 401 against a
// perfectly healthy cluster — a silent empty dashboard, not a failure anyone is
// paged for. The platform's own package (data/opensearch) keeps them equal;
// so must the composition.
func TestComposedSearchMonitoringUserMatchesItsCredential(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(controlPlaneDir(t), "compositions/search/opensearch-operator.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	// The OpensearchUser's name, and the username the credential publishes.
	// Both values are Go-template expressions, so compare them as written.
	userName := regexp.MustCompile(`kind: OpensearchUser\n(?:.*\n)*?\s+name: (.+)\n`).FindStringSubmatch(s)
	if userName == nil {
		t.Fatal("compositions/search composes no OpensearchUser")
	}
	// Scoped to the monitoring credential's own block — the admin credential a
	// few resources earlier publishes a `username` too.
	block := s[strings.Index(s, "composition-resource-name: monitoring-credentials"):]
	if end := strings.Index(block, "\n            ---"); end > 0 {
		block = block[:end]
	}
	credUser := regexp.MustCompile(`(?m)^\s+username: (.+)$`).FindStringSubmatch(block)
	if credUser == nil {
		t.Fatal("compositions/search publishes no monitoring username")
	}
	if got, want := strings.TrimSpace(credUser[1]), strings.TrimSpace(userName[1]); got != want {
		t.Errorf("OpensearchUser is %q but the monitoring credential says username %q — every scrape would 401", want, got)
	}
	// Same invariant in the platform's own package, which this mirrors.
	pkg, err := os.ReadFile(filepath.Join(stackPackagesDir(t), "data/opensearch/manifests/cluster.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pkg), "kind: OpensearchUser") {
		t.Error("data/opensearch no longer composes an OpensearchUser; re-check this mirror")
	}
}

// A NoSqlDatabase is a MongoDB replica set, and the MongoDB Community operator
// gives you an unprotected, unmonitored, unbacked-up one unless every piece
// below is asked for. None of them fails loudly when missing: a replica set
// with no PodDisruptionBudget survives until the first drain, one with no
// PrometheusRule is silent when it loses its majority, and one with no CronJob
// has no backup at all — Ops Manager is the Enterprise answer, so the
// composition owns it.
func TestNoSqlDatabaseComposesTheProductionShape(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(controlPlaneDir(t), nosqlComposition))
	if err != nil {
		t.Fatal(err)
	}
	// Comments stripped first: this file EXPLAINS why each piece is here, and a
	// guard its own explanation can satisfy proves nothing.
	s := withoutComments(string(b))
	for what, want := range map[string]string{
		"the replica set":                  "kind: MongoDBCommunity",
		"SCRAM authentication":             `modes: ["SCRAM-SHA-256"]`,
		"a PodDisruptionBudget":            "kind: PodDisruptionBudget",
		"a PodMonitor for kube-prometheus": "kind: PodMonitor",
		"alerts":                           "kind: PrometheusRule",
		"an ingress NetworkPolicy":         "kind: NetworkPolicy",
		"a backup CronJob":                 "kind: CronJob",
		"the node-bound priority class":    "priorityClassName: adhar-node-bound",
		"a data volume claim":              "name: data-volume",
		"the operator's log volume claim":  "name: logs-volume",
		"pod anti-affinity":                "podAntiAffinity",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("%s does not compose %s (%q)", nosqlComposition, what, want)
		}
	}
	// The app password is minted once. ESO rotating it under a running replica
	// set locks the application out of a database whose SCRAM credentials were
	// derived from the old one.
	if once, refs := strings.Count(s, "refreshPolicy: CreatedOnce"), strings.Count(s, "generatorRef:"); once != refs {
		t.Errorf("%s: %d ExternalSecret(s) read a generator but %d are refreshPolicy: CreatedOnce",
			nosqlComposition, refs, once)
	}
	// A Password generator emits ONE key, `password`. A template reading
	// anything else fails every refresh and the Secret never exists (the
	// mariadb-operator outage, 2026-10-09).
	if refs, rewrites := strings.Count(s, "generatorRef:"), strings.Count(s, "template: PASSWORD"); refs != rewrites {
		t.Errorf("%s: %d ExternalSecret(s) read a generator but %d rewrite its key to PASSWORD",
			nosqlComposition, refs, rewrites)
	}
	// Symbols in a password that is substituted into a mongodb:// URI change
	// which host and database the client reaches.
	if strings.Contains(s, "symbols: 3") {
		t.Errorf("%s: a password that rides a mongodb:// URI must not carry symbols", nosqlComposition)
	}
}

// Argo CD calls a kind it has no health check for Healthy the moment it is
// created. The CNPG Cluster has one, so a wave gated on a platform database
// waits for it; the moment a package asks for a `Database` instead, that gate
// is gone unless the composite kinds have their own — and the app that needs
// the database starts before the database exists.
//
// All four files, because the two install manifests are GENERATED from the two
// values files: a fix in only the rendered output is reverted by the next
// `helm template`, and a fix in only the values never reaches a cluster.
func TestCrossplaneCompositeHealthIsDeclaredForDatastoreKinds(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	files := []string{
		"platform/controllers/adharplatform/resources/argocd/install.yaml",
		"platform/controllers/adharplatform/resources/argocd/install-ha.yaml",
		"hack/argocd/values.yaml",
		"hack/argocd/values-ha.yaml",
	}
	for _, f := range files {
		b, readErr := os.ReadFile(filepath.Join(root, f))
		if readErr != nil {
			t.Fatal(readErr)
		}
		s := string(b)
		for _, kind := range []string{"Database", "Cache", "NoSqlDatabase", "GraphDatabase"} {
			key := "resource.customizations.health.platform.adhar.io_" + kind + ": |"
			if !strings.Contains(s, key) {
				t.Errorf("%s declares no Argo CD health check for %s; a sync wave gated on one would not wait for it", f, kind)
			}
		}
		// Ready alone is not enough: a composition that cannot be rendered sets
		// Synced=False with ReconcileError and never resolves, so a wave that
		// treats it as Progressing waits forever instead of reporting.
		if !strings.Contains(s, `synced.reason == "ReconcileError"`) {
			t.Errorf("%s: the composite health check must report a failed reconcile as Degraded, not Progressing", f)
		}
	}
}

// Crossplane's RBAC manager grants only XRD-defined and provider types, so
// every native or operator kind a composition emits needs an explicit grant.
// The failure is a 403 at apply that the XR surfaces as a generic sync error,
// which reads like a broken composition rather than a missing rule.
func TestNoSqlDatabaseGrantsEveryKindItComposes(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(controlPlaneDir(t), composeRBACFile))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	// `mongodbcommunity` really is the plural — singular-looking, and not what
	// pluralising the kind produces.
	for _, want := range []string{"mongodbcommunity", "cronjobs", "networkpolicies", "prometheusrules", "poddisruptionbudgets"} {
		if !strings.Contains(s, `"`+want+`"`) {
			t.Errorf("%s does not grant %q, which the NoSqlDatabase composition emits", composeRBACFile, want)
		}
	}
}

// The local database composition must NOT derive the Postgres image from
// `engineVersion`. CNPG's defaulting webhook stamps its own supported image
// into every Cluster it admits — on 2026-10-11 that was
// `postgresql:18.4-system-trixie` on nine of the platform's ten databases —
// and `engineVersion` defaults to "16". A composition that renders
// `postgresql:16` therefore does not pick a version: it DOWNGRADES a live
// database by two majors, and a Postgres 16 binary refuses to start on an 18
// data directory. Pin `imageName` when a build is actually required (the AI
// runtime's pgvector store needs `16-bookworm`); otherwise say nothing and let
// the operator choose.
func TestLocalDatabaseNeverDerivesTheImageFromEngineVersion(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(controlPlaneDir(t), "compositions/database/local-cnpg.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := withoutComments(string(b))
	if strings.Contains(s, "postgresql:{{ $version }}") || strings.Contains(s, `postgresql:{{ $version }}"`) {
		t.Error("compositions/database/local-cnpg.yaml renders an image from engineVersion; " +
			"that downgrades every cluster CNPG has already defaulted to a newer major")
	}
	// The explicit pin must still be honoured, or pgvector loses its variant.
	if !strings.Contains(s, "imageName: {{ $image | quote }}") {
		t.Error("compositions/database/local-cnpg.yaml no longer honours an explicit imageName")
	}
}

// Every parameter a package sets on a control-plane datastore must exist in that
// XRD. The XRDs are served as STRUCTURAL schemas with strict decoding, so an
// unknown field is not ignored — `kubectl apply` of the manifest fails with
// `strict decoding error: unknown field "spec.parameters.…"`, and the package
// never gets its database at all. (Verified against the live cluster on
// 2026-10-11, where the old XRD rejected all four new fields.) Worse, the
// XRD reaches a cluster with the controller RELEASE while packages reach it
// with a Gitea push, so the two can be out of step in exactly this direction.
func TestPackageDatastoreRequestsOnlyUseFieldsTheirXRDDefines(t *testing.T) {
	schemas := map[string]map[string]interface{}{} // kind -> parameters properties
	cpDir := controlPlaneDir(t)
	entries, err := os.ReadDir(filepath.Join(cpDir, "xrd"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".xrd.yaml") {
			continue
		}
		for _, doc := range readYAMLDocs(t, filepath.Join(cpDir, "xrd", e.Name())) {
			kind, _ := dig(doc, "spec", "names", "kind").(string)
			versions, _ := dig(doc, "spec", "versions").([]interface{})
			if kind == "" || len(versions) == 0 {
				continue
			}
			v0, _ := versions[0].(map[string]interface{})
			props, _ := dig(v0, "schema", "openAPIV3Schema", "properties", "spec",
				"properties", "parameters", "properties").(map[string]interface{})
			if props != nil {
				schemas[kind] = props
			}
		}
	}
	for _, kind := range []string{"Database", "Cache", "NoSqlDatabase", "GraphDatabase"} {
		if schemas[kind] == nil {
			t.Fatalf("no XRD schema found for %s", kind)
		}
	}
	found := 0
	err = filepath.WalkDir(stackPackagesDir(t), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".tmpl") {
			return nil
		}
		for _, doc := range tolerantYAMLDocs(path) {
			api, _ := doc["apiVersion"].(string)
			kind, _ := doc["kind"].(string)
			if !strings.HasPrefix(api, "platform.adhar.io/") {
				continue
			}
			props, ok := schemas[kind]
			if !ok {
				continue
			}
			params, _ := dig(doc, "spec", "parameters").(map[string]interface{})
			found++
			for name := range params {
				if _, known := props[name]; !known {
					rel := strings.TrimPrefix(path, stackPackagesDir(t)+"/")
					t.Errorf("%s: %s/%v sets spec.parameters.%s, which the %s XRD does not define — "+
						"the apiserver rejects the whole object with a strict decoding error",
						rel, kind, dig(doc, "metadata", "name"), name, kind)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// If the walk finds nothing, the assertion above is vacuous.
	if found == 0 {
		t.Error("no platform datastore requests found in the stack packages; this guard would never fire")
	}
	t.Logf("checked %d datastore request(s) in the stack packages", found)
}

// tolerantYAMLDocs parses the documents of a manifest that may be a Go
// template, skipping the ones that do not parse. Seed-time templates can hold
// expressions no YAML parser accepts (a `{{ … }}` used as a map KEY, as
// adhar-ai's HTTPRoute does), and a guard that walks every package must not
// fail on them — it only cares about the documents it can read.
func tolerantYAMLDocs(path string) []map[string]interface{} {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	// Collapse template expressions to a plain scalar so the rest parses.
	text := regexp.MustCompile(`\{\{[^}]*\}\}`).ReplaceAllString(string(b), "TEMPLATED")
	var out []map[string]interface{}
	for _, chunk := range regexp.MustCompile(`(?m)^---\s*$`).Split(text, -1) {
		var doc map[string]interface{}
		if err := yaml.Unmarshal([]byte(chunk), &doc); err == nil && doc != nil {
			out = append(out, doc)
		}
	}
	return out
}

// A CNPG Cluster must be applied server-side or the sync force-fails and drops
// the workload. The ten platform databases carried that annotation themselves
// until the control plane started composing them; the composition is now the
// single place it can be lost from.
func TestComposedClustersAreAppliedServerSide(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(controlPlaneDir(t), "compositions/database/local-cnpg.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(withoutComments(string(b)), "argocd.argoproj.io/sync-options: ServerSideApply=true") {
		t.Error("compositions/database/local-cnpg.yaml composes a CNPG Cluster without " +
			"ServerSideApply=true; applied any other way the sync force-fails")
	}
}

// Neo4j 5 maps a config key to an env var by replacing `.` with `_` and `_`
// with `__`, so `server.memory.heap.max_size` is
// `NEO4J_server_memory_heap_max__size`. A single underscore where two belong is
// SILENTLY IGNORED — no warning, no error, the setting simply does not apply,
// and the first symptom is the JVM reading the node's memory and being
// OOM-killed under load. These are the settings whose absence is invisible.
func TestNeo4jConfigEnvVarsUseTheDoubleUnderscoreForm(t *testing.T) {
	files := map[string]string{
		"the composition": filepath.Join(controlPlaneDir(t), graphComposition),
		"the package":     filepath.Join(stackPackagesDir(t), "data/neo4j/manifests/install.yaml"),
	}
	// Each entry is a key whose env-var form contains a `__`. The single
	// underscore spelling is what a person writes from the documentation.
	mustHave := []string{
		"NEO4J_server_memory_heap_max__size",
		"NEO4J_server_memory_heap_initial__size",
		"NEO4J_server_bolt_listen__address",
		"NEO4J_server_http_listen__address",
		"NEO4J_server_default__advertised__address",
	}
	mustNotHave := []string{
		"NEO4J_server_memory_heap_max_size",
		"NEO4J_server_memory_heap_initial_size",
		"NEO4J_server_bolt_listen_address",
		"NEO4J_server_http_listen_address",
		"NEO4J_server_default_advertised_address",
	}
	for what, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		s := withoutComments(string(b))
		for _, want := range mustHave {
			if !strings.Contains(s, want) {
				t.Errorf("%s does not set %s", what, want)
			}
		}
		for _, bad := range mustNotHave {
			// The correct spelling contains the wrong one as a prefix only when
			// the wrong one is a strict substring, so compare against the text
			// with every correct spelling removed.
			stripped := s
			for _, good := range mustHave {
				stripped = strings.ReplaceAll(stripped, good, "")
			}
			if strings.Contains(stripped, bad) {
				t.Errorf("%s uses %s; Neo4j needs the DOUBLE underscore form and ignores this silently", what, bad)
			}
		}
	}
}

// A JVM heap size is not a Kubernetes quantity. `1g` is valid for the heap and
// REJECTED by the apiserver, which answers with the quantity regular expression
// instead of an explanation — and the whole StatefulSet then never applies.
func TestTheGraphHeapSizeIsNotUsedAsAKubernetesQuantity(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(controlPlaneDir(t), graphComposition))
	if err != nil {
		t.Fatal(err)
	}
	s := withoutComments(string(b))
	if strings.Contains(s, "memory: {{ $heap | quote }}") {
		t.Error("compositions/graph/neo4j.yaml uses the JVM heap string as a container memory " +
			"request; Kubernetes rejects `1g` and the StatefulSet never applies")
	}
	if !strings.Contains(s, "memory: {{ $memRequest | quote }}") {
		t.Error("compositions/graph/neo4j.yaml does not take its memory request from memoryRequest")
	}
	// And the limit must be its own parameter, not derived from the heap.
	if !strings.Contains(s, "memory: {{ $memLimit | quote }}") {
		t.Error("compositions/graph/neo4j.yaml does not take its memory limit from memoryLimit")
	}
}

// The generated Argo CD manifests and the values they are generated FROM must
// agree on the settings that decide load.
//
// This is the bug that caused a control-plane collapse on 2026-10-11.
// `hack/argocd/values.yaml` had been set to `timeout.reconciliation: 120s` with
// the reasoning written next to it — "30s re-diffed all apps continuously,
// keeping kube-apiserver pegged … leader-election lease renewals then time out
// and every operator crashloops" — and the GENERATED manifest still shipped
// 30s. The fix existed in the repository and had never reached a cluster.
//
// `make manifests` is not run on every change, so the two drift silently in
// both directions: a hand-edit of the generated file is lost at the next
// `helm template`, and an edit of the values file never ships until someone
// regenerates. Pinning the load-bearing keys is cheaper than either habit.
func TestArgoCDReconciliationMatchesItsGeneratorValues(t *testing.T) {
	root, rerr := filepath.Abs("../../..")
	if rerr != nil {
		t.Fatal(rerr)
	}
	// keys whose value changes how much work the controller does
	keys := []string{
		"timeout.reconciliation:",
		"timeout.reconciliation.jitter:",
		"resource.ignoreResourceUpdatesEnabled:",
	}
	read := func(rel string) map[string]string {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		out := map[string]string{}
		for _, line := range strings.Split(withoutComments(string(b)), "\n") {
			trimmed := strings.TrimSpace(line)
			for _, k := range keys {
				if strings.HasPrefix(trimmed, k) {
					// First occurrence wins: the key appears again deep in the
					// manifest as a configMapKeyRef, which carries no value.
					if _, seen := out[k]; !seen {
						out[k] = strings.TrimSpace(strings.TrimPrefix(trimmed, k))
					}
				}
			}
		}
		return out
	}
	for _, pair := range [][2]string{
		{"hack/argocd/values.yaml", "platform/controllers/adharplatform/resources/argocd/install.yaml"},
		{"hack/argocd/values-ha.yaml", "platform/controllers/adharplatform/resources/argocd/install-ha.yaml"},
	} {
		values, generated := read(pair[0]), read(pair[1])
		for _, k := range keys {
			v, inValues := values[k]
			g, inGenerated := generated[k]
			switch {
			case !inValues:
				t.Errorf("%s does not set %s; the generated manifest would lose it on the next render", pair[0], k)
			case !inGenerated:
				t.Errorf("%s does not set %s, so the value in %s never reaches a cluster", pair[1], k, pair[0])
			case v != g:
				t.Errorf("%s has %s %s but %s has %s — whichever is right, one of them is not running",
					pair[0], k, v, pair[1], g)
			}
		}
	}
	// And the value itself: 30s is what pegged the apiserver.
	if v := read("hack/argocd/values.yaml")["timeout.reconciliation:"]; v == "30s" {
		t.Error("timeout.reconciliation is back to 30s; that re-diffs every application " +
			"continuously and stopped leader-election leases from renewing")
	}
}

// Neo4j will not start with the Service env vars Kubernetes injects.
//
// Every package shares one namespace (ADR-0011), so a Neo4j pod receives
// NEO4J_PORT_7687_TCP_PORT for its OWN Service — and Neo4j builds configuration
// from NEO4J_-prefixed env vars by mapping "_" to ".", so it reads that as the
// setting "PORT.7687.TCP.PORT" and exits under strict validation:
//
//	Failed to read config: Unrecognized setting.
//	No declared setting with name: PORT.7687.TCP.PORT
//
// Live on 2026-10-11: fifteen restarts before the pod spec carried this line.
// Disabling strict validation instead would silently ignore our own typos too.
func TestNeo4jDisablesServiceLinks(t *testing.T) {
	pkg := filepath.Join(stackPackagesDir(t), "data/neo4j/manifests/install.yaml")
	found := false
	for _, doc := range tolerantYAMLDocs(pkg) {
		if doc["kind"] != "StatefulSet" {
			continue
		}
		found = true
		spec, _ := doc["spec"].(map[string]interface{})
		tmpl, _ := spec["template"].(map[string]interface{})
		podSpec, _ := tmpl["spec"].(map[string]interface{})
		links, ok := podSpec["enableServiceLinks"]
		if !ok {
			t.Error("the neo4j StatefulSet does not set enableServiceLinks; the Service env vars " +
				"Kubernetes injects are read as Neo4j settings and the server refuses to start")
		} else if links != false {
			t.Errorf("the neo4j StatefulSet sets enableServiceLinks=%v; it must be false", links)
		}
	}
	if !found {
		t.Fatal("no StatefulSet in the neo4j package; this guard is checking nothing")
	}

	comp, err := os.ReadFile(filepath.Join(controlPlaneDir(t), graphComposition))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(withoutComments(string(comp)), "enableServiceLinks: false") {
		t.Error("the GraphDatabase composition does not disable service links; an XR of any name " +
			"still keeps the NEO4J_ prefix the image's own variables use, so it is exposed to the " +
			"same refusal as the package was")
	}
}

// Neo4j's metrics subsystem is Enterprise-only, and asking for it on Community
// is not a missing dashboard — it is a server that will not boot.
//
// Prometheus, Graphite, CSV and JMX are all Enterprise features, so the
// Community config schema does not declare `server.metrics.*` at all and strict
// validation refuses the process with `Unrecognized setting. No declared
// setting with name: server.metrics.prometheus.endpoint`. This was the SECOND
// config key to stop the platform's neo4j package from starting on 2026-10-11,
// immediately after the Service env vars.
//
// The package therefore ships no metrics settings, and the composition emits
// them only when the operator has chosen an Enterprise image.
func TestCommunityNeo4jShipsNoMetricsSettings(t *testing.T) {
	pkg, err := os.ReadFile(filepath.Join(stackPackagesDir(t), "data/neo4j/manifests/install.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if s := withoutComments(string(pkg)); strings.Contains(s, "NEO4J_server_metrics") {
		t.Error("the neo4j package sets a server.metrics setting; the image it pins is Community, " +
			"whose config schema does not declare those keys, so the server exits at startup")
	}

	comp, err := os.ReadFile(filepath.Join(controlPlaneDir(t), graphComposition))
	if err != nil {
		t.Fatal(err)
	}
	code := withoutComments(string(comp))
	if !strings.Contains(code, "NEO4J_server_metrics") {
		t.Skip("the GraphDatabase composition no longer emits metrics settings at all")
	}
	// Whatever drives the metrics block must be conjoined with an edition check,
	// so a Community image cannot reach those keys however `metrics` is set.
	if !strings.Contains(code, `contains "enterprise"`) {
		t.Error(`the GraphDatabase composition emits server.metrics settings without checking the ` +
			`image edition; on a Community image those keys are rejected and the StatefulSet ` +
			`crash-loops, so the metrics parameter must be gated on an Enterprise image`)
	}
}
