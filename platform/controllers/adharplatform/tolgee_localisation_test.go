package adharplatform

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"text/template"

	sprig "github.com/go-task/slim-sprig/v3"
	"sigs.k8s.io/yaml"
)

// Tolgee is where every frontend application's UI text lives. These guards pin
// the four halves that have to agree for that to work: the package (pinned JVM
// with a boot budget, Keycloak sign-in, platform storage), the control plane's
// per-application provisioning (project + keys + the Secrets whose names and
// keys the frontend templates are written against), the paved-road CI stage
// that pushes and pulls before the build, and the appsets.

const tolgeeManifests = "application/tolgee/manifests"

func tolgeeDocs(t *testing.T, file string) []map[string]any {
	t.Helper()
	return yamlDocs(t, filepath.Join(stackPackagesDir(t), filepath.FromSlash(tolgeeManifests), file))
}

func docNamed(docs []map[string]any, kind, name string) map[string]any {
	for _, d := range docs {
		if d["kind"] == kind && dig(d, "metadata", "name") == name {
			return d
		}
	}
	return nil
}

func firstContainer(t *testing.T, workload map[string]any) map[string]any {
	t.Helper()
	cs, _ := dig(workload, "spec", "template", "spec", "containers").([]any)
	if len(cs) == 0 {
		t.Fatalf("%v has no containers", dig(workload, "metadata", "name"))
	}
	c, _ := cs[0].(map[string]any)
	return c
}

// envOf maps env name -> literal value, or "secret:<name>/<key>[?]" for a
// secretKeyRef ("?" when optional).
func envOf(c map[string]any) map[string]string {
	out := map[string]string{}
	envs, _ := c["env"].([]any)
	for _, e := range envs {
		m, _ := e.(map[string]any)
		name, _ := m["name"].(string)
		if v, ok := m["value"].(string); ok {
			out[name] = v
			continue
		}
		if ref, ok := dig(m, "valueFrom", "secretKeyRef").(map[string]any); ok {
			s := "secret:" + toStr(ref["name"]) + "/" + toStr(ref["key"])
			if opt, _ := ref["optional"].(bool); opt {
				s += "?"
			}
			out[name] = s
		}
	}
	return out
}

func toStr(v any) string { s, _ := v.(string); return s }

func TestTolgeeIsAPackageEnabledInProductionOnly(t *testing.T) {
	contract := readStackFile(t, "application/tolgee/adhar-package.yaml")
	var c map[string]any
	if err := yaml.Unmarshal([]byte(contract), &c); err != nil {
		t.Fatal(err)
	}
	if c["name"] != "tolgee" || c["category"] != "application" {
		t.Errorf("contract names %v/%v, want tolgee/application", c["name"], c["category"])
	}
	for file, want := range map[string]string{"adhar-appset-production.yaml": "true", "adhar-appset-local.yaml": "false"} {
		raw, err := os.ReadFile(filepath.Join(stackRoot(t), file))
		if err != nil {
			t.Fatal(err)
		}
		re := regexp.MustCompile(`(?m)^\s+- name: "tolgee"\n\s+enabled: "(\w+)"\n\s+namespace: "adhar-system"\n\s+category: "application"\n\s+manifestPath: "application/tolgee/manifests"`)
		m := re.FindStringSubmatch(string(raw))
		if m == nil {
			t.Errorf("%s has no tolgee element pointing at application/tolgee/manifests", file)
			continue
		}
		if m[1] != want {
			t.Errorf("%s: tolgee enabled=%q, want %q (a JVM: production yes, the local development core no)", file, m[1], want)
		}
	}
}

func TestTolgeeRunsAPinnedImageWithABootBudget(t *testing.T) {
	docs := tolgeeDocs(t, "install.yaml")
	dep := docNamed(docs, "Deployment", "tolgee")
	if dep == nil {
		t.Fatal("install.yaml has no Deployment tolgee")
	}
	c := firstContainer(t, dep)
	img := toStr(c["image"])
	m := regexp.MustCompile(`^docker\.io/tolgee/tolgee:v(\d+\.\d+\.\d+)$`).FindStringSubmatch(img)
	if m == nil {
		t.Fatalf("tolgee image %q is not pinned to a released vX.Y.Z tag", img)
	}
	if contract := readStackFile(t, "application/tolgee/adhar-package.yaml"); !strings.Contains(contract, "appVersion: "+m[1]) {
		t.Errorf("adhar-package.yaml appVersion does not match the image tag v%s", m[1])
	}
	// A JVM that migrates its schema on first start: liveness must not get a
	// say until a startupProbe with a real budget has passed.
	sp, _ := c["startupProbe"].(map[string]any)
	if sp == nil {
		t.Fatal("tolgee has no startupProbe; liveness would kill a JVM still booting")
	}
	budget := toFloat(sp["periodSeconds"]) * toFloat(sp["failureThreshold"])
	if budget < 300 {
		t.Errorf("startupProbe budget is %.0fs; a Spring Boot first start needs at least 5 minutes", budget)
	}
	if c["livenessProbe"] == nil || c["readinessProbe"] == nil {
		t.Error("tolgee lacks a liveness or readiness probe")
	}
	env := envOf(c)
	if !strings.Contains(env["JAVA_OPTS"], "MaxRAMPercentage") {
		t.Error("JAVA_OPTS does not bound the heap to the container (MaxRAMPercentage)")
	}
	if dig(c, "resources", "limits", "memory") == nil {
		t.Error("tolgee has no memory limit for MaxRAMPercentage to be a fraction of")
	}
	for k, v := range map[string]string{
		"TOLGEE_POSTGRES_AUTOSTART_ENABLED": "false", // the platform's Database, not an embedded one
		"TOLGEE_TELEMETRY_ENABLED":          "false",
		"TOLGEE_FILE_STORAGE_S3_ENABLED":    "true",
		"TOLGEE_AUTHENTICATION_ENABLED":     "true", // the Docker profile turns it OFF
	} {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	if !strings.Contains(env["SPRING_DATASOURCE_URL"], "tolgee-db-rw.adhar-system") {
		t.Errorf("SPRING_DATASOURCE_URL %q does not point at the platform database", env["SPRING_DATASOURCE_URL"])
	}
	if docNamed(tolgeeDocs(t, "platform-storage.yaml"), "Database", "tolgee-db") == nil {
		t.Error("the package does not ask the control plane for a Database tolgee-db")
	}
	buckets := readStackFile(t, "data/rustfs/manifests/buckets.yaml")
	if !regexp.MustCompile(`value: "[^"]*\btolgee\b[^"]*"`).MatchString(buckets) {
		t.Error("the rustfs package does not create the `tolgee` bucket Tolgee stores its files in")
	}
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}

func TestTolgeeSignsInThroughKeycloak(t *testing.T) {
	kc := tolgeeDocs(t, "keycloak-client.yaml")
	cm := docNamed(kc, "ConfigMap", "tolgee-keycloak-client")
	if cm == nil || dig(cm, "metadata", "labels", "adhar.io/keycloak-client") != "true" {
		t.Fatal("no ConfigMap tolgee-keycloak-client labelled adhar.io/keycloak-client=true for the keycloak reconciler")
	}
	var client map[string]any
	if err := json.Unmarshal([]byte(toStr(dig(cm, "data", "client.json"))), &client); err != nil {
		t.Fatalf("client.json: %v", err)
	}
	if client["clientId"] != "tolgee" || client["publicClient"] != false {
		t.Errorf("Keycloak client is %v (public=%v), want the confidential client `tolgee`", client["clientId"], client["publicClient"])
	}
	// Tolgee's frontend builds this redirect itself; any other URI is refused by Keycloak.
	if !strings.Contains(toStr(dig(cm, "data", "client.json")), "/login/auth_callback/oauth2") {
		t.Error("the client's redirect URI is not Tolgee's OAuth2 callback /login/auth_callback/oauth2")
	}
	es := docNamed(kc, "ExternalSecret", "tolgee-oidc")
	if es == nil || !strings.Contains(mustJSON(es), `"property":"TOLGEE_CLIENT_SECRET"`) || !strings.Contains(mustJSON(es), `"key":"keycloak-clients"`) {
		t.Error("ExternalSecret tolgee-oidc does not read TOLGEE_CLIENT_SECRET from keycloak-clients")
	}

	env := envOf(firstContainer(t, docNamed(tolgeeDocs(t, "install.yaml"), "Deployment", "tolgee")))
	// Not optional: a Tolgee started with an empty secret draws a button that fails.
	if env["TOLGEE_AUTHENTICATION_OAUTH2_CLIENT_SECRET"] != "secret:tolgee-oidc/clientSecret" {
		t.Errorf("OAuth2 client secret comes from %q, want the non-optional tolgee-oidc/clientSecret", env["TOLGEE_AUTHENTICATION_OAUTH2_CLIENT_SECRET"])
	}
	if !strings.HasPrefix(env["TOLGEE_AUTHENTICATION_OAUTH2_AUTHORIZATION_URL"], "https://keycloak.") {
		t.Error("the OAuth2 authorization URL is not the platform Keycloak's public endpoint")
	}
	for _, k := range []string{"TOLGEE_AUTHENTICATION_OAUTH2_TOKEN_URL", "TOLGEE_AUTHENTICATION_OAUTH2_USER_URL"} {
		if !strings.HasPrefix(env[k], "http://keycloak.adhar-system.svc.cluster.local:8080/realms/adhar/") {
			t.Errorf("%s = %q; server-to-server calls go to Keycloak adhar-controlplane (the JVM refuses a self-signed edge)", k, env[k])
		}
	}
	// Keycloak users get an account on first sign-in only while registrations are allowed …
	if env["TOLGEE_AUTHENTICATION_REGISTRATIONS_ALLOWED"] != "true" {
		t.Error("registrations are closed, so Tolgee refuses a Keycloak user's first sign-in")
	}
	// … so password SIGN-UP must be closed at the edge instead.
	route := readStackFile(t, tolgeeManifests+"/httproute.yaml.tmpl")
	for _, w := range []string{"value: /api/public/sign_up", "type: RequestRedirect", "replaceFullPath: /login", "port: {{ .Port }}"} {
		if !strings.Contains(withoutComments(route), w) {
			t.Errorf("httproute.yaml.tmpl lacks %q: native sign-up would be open to anyone who can reach the edge", w)
		}
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestTolgeeAdminBootstrapWaitsAndKeepsItsToken(t *testing.T) {
	docs := tolgeeDocs(t, "admin-bootstrap.yaml")
	job := docNamed(docs, "Job", "tolgee-admin-bootstrap")
	if job == nil {
		t.Fatal("no Job tolgee-admin-bootstrap")
	}
	if metaAnnotations(job)["argocd.argoproj.io/hook"] != "PostSync" {
		t.Error("the bootstrap must run after Tolgee is Healthy (PostSync hook)")
	}
	script := mustJSON(dig(job, "spec", "template", "spec", "containers"))
	for _, w := range []string{
		"until curl -sf -o /dev/null \\\"$API/actuator/health/readiness\\\"", // WAITS, never skips
		"/v2/pats/current", // an existing PAT is validated and kept
		"/api/public/generatetoken",
		"secret generic tolgee-admin",
		"--from-literal=pat=",
	} {
		if !strings.Contains(script, w) {
			t.Errorf("bootstrap script lacks %q", w)
		}
	}
	for _, name := range []string{"tolgee-db-credentials", "tolgee-secrets", "tolgee-admin-credentials"} {
		es := docNamed(tolgeeDocs(t, "platform-storage.yaml"), "ExternalSecret", name)
		if es == nil || dig(es, "spec", "refreshPolicy") != "CreatedOnce" {
			t.Errorf("ExternalSecret %s must be CreatedOnce: a rotated value breaks a running Tolgee", name)
		}
	}
}

// ── the control plane's half ──────────────────────────────────────────────

var appCompositions = []string{"argocd-multienv.yaml", "argocd-application.yaml", "local.yaml"}

func compositionStepTemplate(t *testing.T, file, step string) string {
	t.Helper()
	var comp struct {
		Spec struct {
			Pipeline []struct {
				Step  string `json:"step"`
				Input struct {
					Inline struct {
						Template string `json:"template"`
					} `json:"inline"`
				} `json:"input"`
			} `json:"pipeline"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal([]byte(readConfigurationFile(t, "compositions", "apps", file)), &comp); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	for _, s := range comp.Spec.Pipeline {
		if s.Step == step {
			return s.Input.Inline.Template
		}
	}
	return ""
}

func TestTheLocalisationStepIsIdenticalInEveryApplicationComposition(t *testing.T) {
	ref := compositionStepTemplate(t, appCompositions[0], "localisation")
	if ref == "" {
		t.Fatalf("%s has no `localisation` step", appCompositions[0])
	}
	for _, f := range appCompositions[1:] {
		got := compositionStepTemplate(t, f, "localisation")
		if got == "" {
			t.Errorf("%s has no `localisation` step: an Application selecting it would never get its Tolgee project", f)
		} else if got != ref {
			t.Errorf("%s's `localisation` step differs from %s's; the copies must stay identical", f, appCompositions[0])
		}
	}
}

// renderLocalisation executes the localisation step against an XR, with an
// observed Project (multienv) when envs is non-nil.
func renderLocalisation(t *testing.T, file, params string, envs []string, multienv bool) []map[string]any {
	t.Helper()
	src := compositionStepTemplate(t, file, "localisation")
	xr := fromYAML(t, `
apiVersion: platform.adhar.io/v1alpha1
kind: Application
metadata: {name: shopfront, namespace: shopfront}
spec:
  parameters:
    project: shop
    source: {repoURL: "http://gitea-http.adhar-system.svc.cluster.local:3000/adhar/shopfront"}
    destination: {namespace: shopfront-ns}
`+params)
	observed := map[string]any{}
	desired := map[string]any{}
	if multienv {
		desired["project"] = map[string]any{"resource": map[string]any{}}
		var es []any
		for _, e := range envs {
			es = append(es, map[string]any{"name": e, "namespace": "shop-" + e})
		}
		if es != nil {
			observed["project"] = map[string]any{"resource": map[string]any{"status": map[string]any{"atProvider": map[string]any{"manifest": map[string]any{"status": map[string]any{"organisation": "Acme", "environments": es}}}}}}
		}
	}
	req := map[string]any{
		"observed": map[string]any{"composite": map[string]any{"resource": xr}, "resources": observed},
		"desired":  map[string]any{"resources": desired},
	}
	tpl, err := template.New("l10n").Funcs(sprig.TxtFuncMap()).Parse(src)
	if err != nil {
		t.Fatalf("%s localisation step does not parse: %v", file, err)
	}
	raw, _ := json.Marshal(req)
	var in map[string]any
	_ = json.Unmarshal(raw, &in)
	var out bytes.Buffer
	if err := tpl.Execute(&out, in); err != nil {
		t.Fatalf("%s localisation step does not render: %v", file, err)
	}
	var docs []map[string]any
	for _, d := range strings.Split(out.String(), "\n---") {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(d), &m); err != nil {
			t.Fatalf("a rendered document is not YAML: %v\n%s", err, d)
		}
		if len(m) > 0 {
			docs = append(docs, m)
		}
	}
	return docs
}

const tolgeeParams = `
    localisation: {provider: tolgee, baseLanguage: en, languages: [de, fr]}
`

func provisioningJob(t *testing.T, docs []map[string]any) map[string]any {
	t.Helper()
	jobs := manifestsOfKind(docs, "Job")
	if len(jobs) != 1 {
		t.Fatalf("want exactly one provisioning Job, got %d", len(jobs))
	}
	return jobs[0]
}

func TestLocalisationIsComposedOnlyWhenDeclared(t *testing.T) {
	for _, f := range appCompositions {
		if docs := renderLocalisation(t, f, "", []string{"dev"}, f == "argocd-multienv.yaml"); len(docs) != 0 {
			t.Errorf("%s: an Application without localisation renders %d localisation documents", f, len(docs))
		}
	}
}

func TestLocalisationProvisionsTheSecretsTheTemplatesReadInEveryEnvironment(t *testing.T) {
	docs := renderLocalisation(t, "argocd-multienv.yaml", tolgeeParams, []string{"dev", "test", "prod"}, true)
	job := provisioningJob(t, docs)
	if dig(job, "metadata", "namespace") != "adhar-system" {
		t.Errorf("the Job runs in %v; it reads tolgee-admin and writes <app>-tolgee-ci in adhar-system", dig(job, "metadata", "namespace"))
	}
	if !strings.HasPrefix(toStr(dig(job, "metadata", "name")), "tolgee-shopfront-") {
		t.Errorf("Job name %v is not tolgee-<app>-<hash>", dig(job, "metadata", "name"))
	}
	pod, _ := dig(job, "spec", "template", "spec").(map[string]any)
	if pod["serviceAccountName"] != "tolgee-provisioner" {
		t.Errorf("Job runs as %v, want the tolgee package's tolgee-provisioner", pod["serviceAccountName"])
	}
	c := firstContainer(t, job)
	env := envOf(c)
	if env["NAMESPACES"] != "shop-dev shop-test shop-prod" {
		t.Errorf("NAMESPACES = %q, want every project environment's namespace", env["NAMESPACES"])
	}
	if env["LANGS"] != "en de fr" || env["BASE"] != "en" {
		t.Errorf("LANGS=%q BASE=%q: the base language must be carried even when not listed", env["LANGS"], env["BASE"])
	}
	if env["ORG"] != "acme" {
		t.Errorf("ORG = %q, want the Adhar organisation's slug", env["ORG"])
	}
	if env["READ_SCOPES"] != `["translations.view"]` {
		t.Errorf("the runtime key's scopes are %s; it ships into application pods, so translations.view only", env["READ_SCOPES"])
	}
	for _, s := range []string{"keys.create", "translations.edit", "translations.view"} {
		if !strings.Contains(env["CI_SCOPES"], `"`+s+`"`) {
			t.Errorf("the CI key lacks %s, which `tolgee push`/`pull` need", s)
		}
	}
	script := strings.Join(toStrings(c["args"]), "\n")
	for _, w := range []string{
		`CI_SECRET="${APP}-tolgee-ci"`, `RT_SECRET="${APP}-tolgee"`, // the contract's Secret names
		"--from-literal=api-url=", "--from-literal=project-id=", "--from-literal=api-key=", // and keys
		`put "$SYS" "$CI_SECRET"`, `for ns in $NAMESPACES`, // CI key in adhar-system, runtime key in every env
		"if valid \"$CI_KEY\"; then", // keys are KEPT while Tolgee accepts them
		"--server-side",              // no last-applied annotation carrying a key
	} {
		if !strings.Contains(script, w) {
			t.Errorf("provisioning script lacks %q", w)
		}
	}
	if regexp.MustCompile(`echo[^\n]*\$(CI_KEY|RT_KEY|AP)\b`).MatchString(script) {
		t.Error("the provisioning script echoes a key or the admin password")
	}
	// The provisioner may write Secrets in each environment namespace and nowhere else.
	rbs := manifestsOfKind(docs, "RoleBinding")
	if len(rbs) != 3 {
		t.Fatalf("want one RoleBinding per environment namespace, got %d", len(rbs))
	}
	for _, rb := range rbs {
		if dig(rb, "roleRef", "name") != "tolgee-provisioner-env-secrets" || dig(rb, "roleRef", "kind") != "ClusterRole" {
			t.Errorf("RoleBinding in %v binds %v", dig(rb, "metadata", "namespace"), dig(rb, "roleRef"))
		}
	}
	// A localisation server must never hold the application's readiness.
	for _, d := range docs {
		if d["kind"] == "Object" {
			if ann, _ := dig(d, "metadata", "annotations").(map[string]any); ann["gotemplating.fn.crossplane.io/ready"] != "True" {
				t.Errorf("composed %v does not declare itself ready; it would hold the Application unready", ann["gotemplating.fn.crossplane.io/composition-resource-name"])
			}
		}
	}
	// The package ships the role those bindings name.
	if docNamed(tolgeeDocs(t, "admin-bootstrap.yaml"), "ClusterRole", "tolgee-provisioner-env-secrets") == nil {
		t.Error("the tolgee package does not ship ClusterRole tolgee-provisioner-env-secrets")
	}
}

func toStrings(v any) []string {
	var out []string
	l, _ := v.([]any)
	for _, x := range l {
		out = append(out, toStr(x))
	}
	return out
}

func TestLocalisationWaitsForTheProjectsEnvironments(t *testing.T) {
	docs := renderLocalisation(t, "argocd-multienv.yaml", tolgeeParams, nil, true)
	if n := len(manifestsOfKind(docs, "Job")); n != 0 {
		t.Errorf("rendered %d Jobs before the project published its environments; the Job would run for no namespace and run again", n)
	}
}

func TestASingleNamespaceApplicationGetsItsKeyInItsDestination(t *testing.T) {
	for _, f := range []string{"argocd-application.yaml", "local.yaml"} {
		env := envOf(firstContainer(t, provisioningJob(t, renderLocalisation(t, f, tolgeeParams, nil, false))))
		if env["NAMESPACES"] != "shopfront-ns" || env["ORG"] != "adhar" {
			t.Errorf("%s: NAMESPACES=%q ORG=%q, want the destination namespace and the default organisation", f, env["NAMESPACES"], env["ORG"])
		}
	}
}

func TestTheLocalisationJobIsNamedByItsDeclaration(t *testing.T) {
	name := func(params string, envs ...string) string {
		return toStr(dig(provisioningJob(t, renderLocalisation(t, "argocd-multienv.yaml", params, envs, true)), "metadata", "name"))
	}
	a := name(tolgeeParams, "dev", "test")
	if b := name(tolgeeParams, "dev", "test"); a != b {
		t.Errorf("the same declaration renders %q and %q; an unchanged declaration must never run twice", a, b)
	}
	if c := name("\n    localisation: {provider: tolgee, languages: [de, fr, es]}\n", "dev", "test"); c == a {
		t.Error("adding a language does not change the Job name, so the new language is never provisioned")
	}
	if d := name(tolgeeParams, "dev", "test", "prod"); d == a {
		t.Error("a new environment does not change the Job name, so its namespace never gets <app>-tolgee")
	}
}

func TestTheApplicationXRDTypesLocalisationStrictly(t *testing.T) {
	var xrd map[string]any
	if err := yaml.Unmarshal([]byte(readConfigurationFile(t, "xrd", "apps.xrd.yaml")), &xrd); err != nil {
		t.Fatal(err)
	}
	versions, _ := dig(xrd, "spec", "versions").([]any)
	v0, _ := versions[0].(map[string]any)
	l10n, _ := dig(v0, "schema", "openAPIV3Schema", "properties", "spec", "properties", "parameters", "properties", "localisation").(map[string]any)
	if l10n == nil {
		t.Fatal("the Application XRD has no spec.parameters.localisation; a descriptor carrying it is rejected (the XRD is strict)")
	}
	if enum, _ := dig(l10n, "properties", "provider", "enum").([]any); len(enum) != 1 || enum[0] != "tolgee" {
		t.Errorf("localisation.provider enum is %v, want [tolgee]", enum)
	}
	if dig(l10n, "properties", "languages", "items", "type") != "string" || dig(l10n, "properties", "baseLanguage", "type") != "string" {
		t.Error("localisation.languages / baseLanguage are not typed as language tags")
	}
}

// ── the CI half ───────────────────────────────────────────────────────────

func supplyChainDoc(t *testing.T, file, kind, name string) map[string]any {
	t.Helper()
	d := docNamed(yamlDocs(t, filepath.Join(stackPackagesDir(t), "application", "adhar-supply-chain", "manifests", file)), kind, name)
	if d == nil {
		t.Fatalf("%s has no %s %s", file, kind, name)
	}
	return d
}

func TestAppCILocalisesBeforeTheBuildOnlyForTolgeeRepositories(t *testing.T) {
	pipe := supplyChainDoc(t, "70-app-ci.yaml", "Pipeline", "app-ci")
	tasks, _ := dig(pipe, "spec", "tasks").([]any)
	var localise, build map[string]any
	for _, x := range tasks {
		m, _ := x.(map[string]any)
		switch m["name"] {
		case "localise":
			localise = m
		case "build":
			build = m
		}
	}
	if localise == nil || build == nil {
		t.Fatal("app-ci has no localise or build task")
	}
	if dig(localise, "taskRef", "name") != "tolgee-sync" {
		t.Errorf("localise runs %v, want tolgee-sync", dig(localise, "taskRef", "name"))
	}
	when := mustJSON(localise["when"])
	for _, w := range []string{`"input":"$(tasks.toolchain.results.tolgee)"`, `"input":"$(params.git-ref)"`, `"values":["main"]`} {
		if !strings.Contains(when, w) {
			t.Errorf("localise is not gated on %s (a repository without .tolgeerc, or a PR build, must get no pod)", w)
		}
	}
	if !strings.Contains(mustJSON(build["runAfter"]), `"localise"`) {
		t.Error("build does not run after localise, so the image would not bundle the pulled translations")
	}
	detect := mustJSON(supplyChainDoc(t, "72-app-ci-stages.yaml", "Task", "detect-toolchain"))
	if !strings.Contains(detect, `"name":"tolgee"`) || !strings.Contains(detect, "$(results.tolgee.path)") || !strings.Contains(detect, ".tolgeerc") {
		t.Error("detect-toolchain does not publish the `tolgee` result from the repository's .tolgeerc")
	}

	task := supplyChainDoc(t, "72-app-ci-stages.yaml", "Task", "tolgee-sync")
	steps, _ := dig(task, "spec", "steps").([]any)
	step, _ := steps[0].(map[string]any)
	// The runtime is the repository's (TestTheCiToolchainComesFromTheRepository);
	// the CLI is the platform's and pinned below.
	if step["image"] != "docker.io/library/node:$(params.nodeVersion)-bookworm-slim" {
		t.Errorf("tolgee-sync image %v does not follow the repository's declared Node", step["image"])
	}
	if !strings.Contains(mustJSON(localise["params"]), `"value":"$(tasks.toolchain.results.node)"`) {
		t.Error("localise does not hand tolgee-sync the Node version detect-toolchain read from the repository")
	}
	env := envOf(step)
	for k, key := range map[string]string{"TOLGEE_API_KEY": "api-key", "TOLGEE_API_URL": "api-url", "TOLGEE_PROJECT_ID": "project-id"} {
		if env[k] != "secret:$(params.app)-tolgee-ci/"+key+"?" {
			t.Errorf("%s comes from %q, want the optional Secret <app>-tolgee-ci key %s", k, env[k], key)
		}
	}
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(env["TOLGEE_CLI_VERSION"]) {
		t.Errorf("the Tolgee CLI is not pinned (TOLGEE_CLI_VERSION=%q)", env["TOLGEE_CLI_VERSION"])
	}
	script := toStr(step["script"])
	for _, w := range []string{`"@tolgee/cli@${TOLGEE_CLI_VERSION}"`, "push --force-mode KEEP", "tolgee pull", "printf false"} {
		if !strings.Contains(script, w) {
			t.Errorf("tolgee-sync script lacks %q", w)
		}
	}
	if strings.Contains(script, "--api-key") || regexp.MustCompile(`echo[^\n]*TOLGEE_API_KEY`).MatchString(script) {
		t.Error("tolgee-sync puts the API key on a command line or in the log; it must reach the CLI through the environment only")
	}
	if dig(step, "computeResources") == nil || dig(step, "resources") != nil {
		t.Error("tolgee-sync must size itself with computeResources (Tekton v1), not resources")
	}
}

// A template rendered with localisation off still ships an EMPTY .tolgeerc
// (the scaffolder cannot omit a file); it must not schedule the localise
// stage. Runs the real detect-toolchain snippet against each case.
func TestAnEmptyTolgeeConfigDoesNotScheduleLocalisation(t *testing.T) {
	src := readStackFile(t, "application/adhar-supply-chain/manifests/72-app-ci-stages.yaml")
	snippet := regexp.MustCompile(`(?s)\n\s*tolgee=false\n.*?printf '%s' "\$tolgee" > "\$\(results\.tolgee\.path\)"`).FindString(src)
	if snippet == "" {
		t.Fatal("detect-toolchain no longer writes the tolgee result")
	}
	for _, c := range []struct {
		name, content, want string
	}{
		{"empty", "", "false"},
		{"whitespace only", "\n  \n", "false"},
		{"real config", `{"format":"JSON_TOLGEE"}`, "true"},
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".tolgeerc"), []byte(c.content), 0o644); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(dir, ".result")
		cmd := exec.Command("bash", "-c", "set -euo pipefail\n"+strings.ReplaceAll(snippet, "$(results.tolgee.path)", out))
		cmd.Dir = dir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: snippet failed: %v\n%s", c.name, err, b)
		}
		if got, _ := os.ReadFile(out); string(got) != c.want {
			t.Errorf("%s .tolgeerc: tolgee=%q, want %q", c.name, got, c.want)
		}
	}
}
