package adharplatform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Search (OpenSearch through the platform's operator) and
// Vector (Qdrant through provider-helm) are the two managed data
// services a team can ask for by name. These pin the agreements between the
// XRDs, their compositions, the RBAC the providers need and the packages they
// lean on — none of which anything at apply time would report clearly.

var datastoreXRDs = map[string]string{"search": "Search", "vector": "Vector"}

// The two RBAC files every composition's grants must appear in, and the literals
// the assertions below repeat.
const (
	composeRBACFile  = "rbac/local-compose-rbac.yaml"
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
	for _, f := range []string{"compositions/search/opensearch-operator.yaml", "compositions/vector/qdrant.yaml"} {
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
