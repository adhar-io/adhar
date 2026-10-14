package adharplatform

// Two applications carry a Keycloak login of their OWN, inside the app, behind
// the edge proxy every URL already has — and in both the platform runs a FORK
// to get it, so the wiring below is the only thing that makes the fork worth
// shipping. Found on the 2026-10-09 AWS cluster: Plane ran the
// ghcr.io/adhar-io/plane-backend image for a week with
// is_keycloak_enabled=False, because the fork reads its settings from
// InstanceConfiguration rows that nothing on the platform wrote.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// withoutComments drops whole-line `#` comments, so a guard cannot be
// satisfied by its own explanation quoting the identifier it looks for.
func withoutComments(src string) string {
	var keep []string
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		keep = append(keep, line)
	}
	return strings.Join(keep, "\n")
}

func readStackFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(stackPackagesDir(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// containerEnv returns name -> literal value for the named container of the
// first Deployment (or Job) in a multi-document manifest; valueFrom entries map
// to "".
func containerEnv(t *testing.T, manifest, kind, container string) (map[string]string, map[string]map[string]string) {
	t.Helper()
	dec := yaml.NewDecoder(strings.NewReader(manifest))
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc["kind"] != kind {
			continue
		}
		spec, _ := doc["spec"].(map[string]any)
		tmpl, _ := spec["template"].(map[string]any)
		pod, _ := tmpl["spec"].(map[string]any)
		containers, _ := pod["containers"].([]any)
		for _, c := range containers {
			cm, _ := c.(map[string]any)
			if cm["name"] != container {
				continue
			}
			values := map[string]string{}
			refs := map[string]map[string]string{}
			envs, _ := cm["env"].([]any)
			for _, e := range envs {
				em, _ := e.(map[string]any)
				name, _ := em["name"].(string)
				if v, ok := em["value"].(string); ok {
					values[name] = v
					continue
				}
				values[name] = ""
				vf, _ := em["valueFrom"].(map[string]any)
				if skr, ok := vf["secretKeyRef"].(map[string]any); ok {
					refs[name] = map[string]string{}
					for k, v := range skr {
						refs[name][k] = strings.TrimSpace(strings.Trim(toString(v), "\""))
					}
				}
			}
			return values, refs
		}
	}
	t.Fatalf("no %s container %q in manifest", kind, container)
	return nil, nil
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	}
	return ""
}

// Plane: the fork's provider reads six InstanceConfiguration rows (category
// KEYCLOAK). The setup Job must write all of them from the keycloak-clients
// Secret, invalidate the cached /api/instances/ payload it then verifies, and
// count the method it enabled as a method.
func TestPlaneInstanceSetupEnablesTheForksKeycloakLogin(t *testing.T) {
	setup := readStackFile(t, "application/plane/manifests/instance-setup.yaml")
	code := withoutComments(setup)

	for _, want := range []string{
		`cfg("KEYCLOAK_ISSUER_URL", issuer, "KEYCLOAK")`,
		`cfg("KEYCLOAK_INTERNAL_ISSUER_URL", internal, "KEYCLOAK")`,
		`cfg("KEYCLOAK_CLIENT_ID", client_id, "KEYCLOAK")`,
		`cfg("KEYCLOAK_CLIENT_SECRET", client_secret, "KEYCLOAK", encrypted=True)`,
		`cfg("IS_KEYCLOAK_ENABLED", "1", "KEYCLOAK")`,
		`cfg("ENABLE_KEYCLOAK_SYNC", "1", "KEYCLOAK")`,
		// Membership follows the realm: the fork lands every Keycloak user in
		// the platform workspace with a role from the `groups` claim, so SSO
		// never ends on "create your workspace".
		`cfg("KEYCLOAK_AUTO_JOIN_WORKSPACE", os.environ["WORKSPACE_SLUG"], "KEYCLOAK")`,
		`cfg("KEYCLOAK_ADMIN_GROUPS", "platform-admin", "KEYCLOAK")`,
		// The payload the sign-in page AND the Job's own verification read is
		// cached for two hours; cfg() bypasses the admin endpoints that
		// invalidate it. Observed: a run that switched Gitea -> Keycloak
		// verified ["is_gitea_enabled"].
		`invalidate_cache_directly(path="/api/instances/", user=False)`,
		`"is_keycloak_enabled"]`,
		// Written, then not reported, means the running image is not the fork.
		`if keycloak_ready and not conf.get("is_keycloak_enabled"):`,
	} {
		if !strings.Contains(code, want) {
			t.Errorf("plane instance-setup.yaml lost %q", want)
		}
	}

	values, refs := containerEnv(t, setup, "Job", "setup")
	if got := values["KEYCLOAK_INTERNAL_ISSUER_URL"]; !strings.HasPrefix(got, "http://keycloak.adhar-system.svc.cluster.local:8080/realms/") {
		t.Errorf("KEYCLOAK_INTERNAL_ISSUER_URL = %q; the backchannel must stay in-cluster", got)
	}
	if got := values["KEYCLOAK_ISSUER_URL"]; !strings.HasPrefix(got, "https://keycloak.adhar.localtest.me:8443/realms/") {
		t.Errorf("KEYCLOAK_ISSUER_URL = %q; must be the seed-time-rewritten public issuer", got)
	}
	if values["KEYCLOAK_CLIENT_ID_KEY"] != "PLANE_APP_CLIENT_ID" || values["KEYCLOAK_CLIENT_SECRET_KEY"] != "PLANE_APP_CLIENT_SECRET" {
		t.Errorf("the Job must read the plane-app client from keycloak-clients, got id key %q secret key %q",
			values["KEYCLOAK_CLIENT_ID_KEY"], values["KEYCLOAK_CLIENT_SECRET_KEY"])
	}
	if _, viaEnv := refs["KEYCLOAK_CLIENT_SECRET"]; viaEnv {
		t.Error("KEYCLOAK_CLIENT_SECRET must not be a secretKeyRef: an optional ref lets the Job start " +
			"without it and silently leave Keycloak off; the Job waits for the Secret through the API instead")
	}

	sso := readStackFile(t, "application/plane/manifests/sso.yaml")
	for _, uri := range []string{
		"https://plane.adhar.localtest.me:8443/auth/keycloak/callback/",
		"https://plane.adhar.localtest.me:8443/auth/spaces/keycloak/callback/",
	} {
		if !strings.Contains(sso, `"`+uri+`"`) {
			t.Errorf("plane-app Keycloak client lost redirect URI %s", uri)
		}
	}
	if !strings.Contains(sso, `"clientId": "plane-app"`) {
		t.Error("the plane-app client (Plane's own login, separate audience from the proxy) is gone")
	}
}

// Strapi: admin SSO is an Enterprise feature that the Adhar build of
// @strapi/core enables. The boot script swaps that build in through an npm
// override, config/admin.js registers a Keycloak strategy answering to the
// provider uid, and the versions must move in lockstep.
func TestStrapiBootWiresTheKeycloakAdminLogin(t *testing.T) {
	install := readStackFile(t, "application/strapi/manifests/install.yaml")
	code := withoutComments(install)

	for _, want := range []string{
		`npm pkg set "overrides.@strapi/core=$want_override"`,
		`grep -q ADHAR_BUNDLED_FEATURES node_modules/@strapi/core/dist/index.js`,
		`create-strapi@"$STRAPI_VERSION"`,
		`uid: 'keycloak',`,
		`strategy.name = 'keycloak';`,
		`getStrategyCallbackURL('keycloak')`,
		`providers: keycloakProviders(env),`,
		`autoRegister: true, defaultRole: role.id`,
		`proxy: true,`,
	} {
		if !strings.Contains(code, want) {
			t.Errorf("strapi install.yaml lost %q", want)
		}
	}

	values, refs := containerEnv(t, install, "Deployment", "strapi")
	version := values["STRAPI_VERSION"]
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(version) {
		t.Fatalf("STRAPI_VERSION = %q; the scaffold must be pinned", version)
	}
	override := values["STRAPI_CORE_OVERRIDE"]
	wantTag := "/adhar-core-v" + version + "-"
	wantFile := "/strapi-core-" + version + ".tgz"
	if !strings.Contains(override, wantTag) || !strings.HasSuffix(override, wantFile) {
		t.Errorf("STRAPI_CORE_OVERRIDE = %q; Strapi's packages move in lockstep, so the tarball must be "+
			"tagged %s* and named %s to match STRAPI_VERSION", override, wantTag, wantFile)
	}
	if !strings.HasPrefix(override, "https://github.com/adhar-io/strapi/releases/download/") {
		t.Errorf("STRAPI_CORE_OVERRIDE = %q; must be a release asset of the adhar-io/strapi fork", override)
	}
	if ref := refs["KEYCLOAK_CLIENT_SECRET"]; ref["name"] != "keycloak-clients" || ref["key"] != "STRAPI_APP_CLIENT_SECRET" {
		t.Errorf("KEYCLOAK_CLIENT_SECRET must come from keycloak-clients/STRAPI_APP_CLIENT_SECRET, got %v", ref)
	} else if ref["optional"] == "true" {
		t.Error("KEYCLOAK_CLIENT_SECRET must be required: an optional ref starts Strapi without the login " +
			"and nothing restarts it when the client reconciler exports the key")
	}
	if values["KEYCLOAK_CLIENT_ID"] != "strapi-app" {
		t.Errorf("KEYCLOAK_CLIENT_ID = %q; Strapi's own login uses the strapi-app client, not the proxy's", values["KEYCLOAK_CLIENT_ID"])
	}
	if got := values["KEYCLOAK_INTERNAL_ISSUER_URL"]; !strings.HasPrefix(got, "http://keycloak.adhar-system.svc.cluster.local:8080/realms/") {
		t.Errorf("KEYCLOAK_INTERNAL_ISSUER_URL = %q; the backchannel must stay in-cluster", got)
	}

	sso := readStackFile(t, "application/strapi/manifests/sso.yaml")
	if !strings.Contains(sso, `"clientId": "strapi-app"`) {
		t.Error("the strapi-app client (Strapi's own login, separate audience from the proxy) is gone")
	}
	if !strings.Contains(sso, `"https://strapi.adhar.localtest.me:8443/admin/connect/keycloak"`) {
		t.Error("strapi-app client lost the /admin/connect/keycloak redirect URI the EE SSO flow calls back on")
	}
}

// The library releases publish to Nexus with a Secret the nexus package
// creates, and a run fired before it exists dies at `build` with
// CreateContainerConfigError and is never retried (2026-10-09: webhook
// 15:23:19Z, Secret 15:32:52Z). The guarantee lives where the trigger is: the
// mirror Jobs push the repositories that fire the webhook, so they wait for the
// credential first — as long as the bootstrap hook's own Nexus gate — and the
// pipelines themselves carry no waiting stage (removed on request, 2026-10-10).
func TestLibraryMirrorsWaitForNexusCredentialsBeforePushing(t *testing.T) {
	for _, file := range []string{
		"application/adhar-libraries/manifests/maven-release.yaml",
		"application/adhar-libraries/manifests/npm-release.yaml",
	} {
		if strings.Contains(withoutComments(readStackFile(t, file)), "wait-for-nexus") {
			t.Errorf("%s: the pipelines must not carry a wait-for-nexus stage; the wait belongs in the mirror Jobs", file)
		}
	}
	mirror := withoutComments(readStackFile(t, "application/adhar-libraries/manifests/mirror.yaml"))
	loops := regexp.MustCompile(`for i in \$\(seq 1 (\d+)\); do\n\s*kubectl get secret nexus-credentials[^\n]*\n\s*sleep (\d+)`).FindAllStringSubmatch(mirror, -1)
	if len(loops) != 2 {
		t.Fatalf("expected both mirror Jobs to wait for nexus-credentials, found %d wait loops", len(loops))
	}
	for _, m := range loops {
		n, _ := strconv.Atoi(m[1])
		step, _ := strconv.Atoi(m[2])
		if n*step < 15*60 {
			t.Errorf("a mirror Job waits only %ds for nexus-credentials; the 2026-10-09 gap was over nine minutes, wait at least fifteen", n*step)
		}
	}
}

// Push-triggered library releases must carry the same `adhar.io/library` label
// the bootstrap hook puts on its own runs: that hook skips a library whose
// release already succeeded or is in flight BY THAT LABEL, and an unlabelled
// webhook run was invisible to it, so every bring-up queued each release twice
// (2026-10-09: the duplicate kit run then starved for CPU for an hour).
func TestPushTriggeredLibraryReleasesAreVisibleToTheBootstrapDedupe(t *testing.T) {
	triggers := readStackFile(t, "application/adhar-libraries/manifests/triggers.yaml")
	release := readStackFile(t, "application/adhar-libraries/manifests/release.yaml")
	if !strings.Contains(withoutComments(release), `-l "adhar.io/library=${lib}"`) {
		t.Fatal("the bootstrap hook no longer dedupes releases by the adhar.io/library label; this guard assumes it does")
	}
	dec := yaml.NewDecoder(strings.NewReader(triggers))
	templates := 0
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc["kind"] != "TriggerTemplate" {
			continue
		}
		templates++
		spec, _ := doc["spec"].(map[string]any)
		for _, raw := range spec["resourcetemplates"].([]any) {
			res, _ := raw.(map[string]any)
			if res["kind"] != "PipelineRun" {
				continue
			}
			meta, _ := res["metadata"].(map[string]any)
			labels, _ := meta["labels"].(map[string]any)
			pipeline, _ := res["spec"].(map[string]any)["pipelineRef"].(map[string]any)["name"].(string)
			want := strings.TrimPrefix(pipeline, "release-")
			if got, _ := labels["adhar.io/library"].(string); got != want {
				t.Errorf("TriggerTemplate %v creates runs of %s labelled adhar.io/library=%q; the bootstrap dedupe looks for %q",
					doc["metadata"].(map[string]any)["name"], pipeline, got, want)
			}
		}
	}
	if templates < 2 {
		t.Errorf("expected a TriggerTemplate per library, found %d", templates)
	}
}

// Argo CD runs PostSync hooks wave by wave. The console-reload hook waits for
// the Secret that plane-instance-setup writes, so it must run in a LATER wave
// — at the default wave 0 it ran first and blocked, for its whole wait, the
// Job that would have satisfied it (fresh AWS cluster, 2026-10-09: the console
// held an empty Plane key and answered 401).
func TestPlaneConsoleReloadRunsAfterTheInstanceSetup(t *testing.T) {
	wave := func(file, job string) int {
		t.Helper()
		dec := yaml.NewDecoder(strings.NewReader(readStackFile(t, file)))
		for {
			var doc map[string]any
			if err := dec.Decode(&doc); err != nil {
				break
			}
			if doc["kind"] != "Job" || toString(doc["metadata"].(map[string]any)["name"]) != job {
				continue
			}
			ann, _ := doc["metadata"].(map[string]any)["annotations"].(map[string]any)
			if ann["argocd.argoproj.io/hook"] != "PostSync" {
				t.Fatalf("%s is no longer a PostSync hook; this guard assumes hook-wave ordering", job)
			}
			w, _ := ann["argocd.argoproj.io/sync-wave"].(string)
			if w == "" {
				return 0
			}
			n, err := strconv.Atoi(w)
			if err != nil {
				t.Fatalf("%s has a non-numeric sync-wave %q", job, w)
			}
			return n
		}
		t.Fatalf("no Job %s in %s", job, file)
		return 0
	}
	setup := wave("application/plane/manifests/instance-setup.yaml", "plane-instance-setup")
	reload := wave("application/plane/manifests/console-reload.yaml", "plane-console-reload")
	if reload <= setup {
		t.Errorf("plane-console-reload runs at wave %d, not after plane-instance-setup (wave %d): it waits for the Secret that Job writes and blocks it", reload, setup)
	}
}

// A `Password` generator emits exactly one key, `password`. An ExternalSecret
// template that reads some other name — `{{ .PASSWORD }}` without a `rewrite`
// that produces it — fails on every refresh with "map has no entry for key",
// and nothing downstream ever gets its Secret (mariadb-operator, 2026-10-09:
// User and Grant Degraded for ten hours over one upper-cased template key).
// Every template key must be produced by something in the same ExternalSecret.
func TestGeneratorBackedTemplatesOnlyReadKeysTheyAreGiven(t *testing.T) {
	keyRef := regexp.MustCompile(`\{\{-?\s*\.([A-Za-z_][A-Za-z0-9_]*)`)
	checked := 0
	for _, file := range stackPackageManifests(t) {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), "generatorRef") {
			continue
		}
		// Seed-time directives are not relevant to the template; neutralise them.
		text := strings.NewReplacer("{{ .Host }}", "h", "{{ .Port }}", "1").Replace(string(src))
		dec := yaml.NewDecoder(strings.NewReader(text))
		for {
			var doc map[string]any
			if err := dec.Decode(&doc); err != nil {
				break
			}
			if doc == nil || doc["kind"] != "ExternalSecret" {
				continue
			}
			spec, _ := doc["spec"].(map[string]any)
			given := map[string]bool{}
			hasGenerator := false
			for _, raw := range toSlice(spec["dataFrom"]) {
				df, _ := raw.(map[string]any)
				src, _ := df["sourceRef"].(map[string]any)
				gen, _ := src["generatorRef"].(map[string]any)
				rewrites := toSlice(df["rewrite"])
				if gen != nil {
					hasGenerator = true
					if len(rewrites) == 0 && gen["kind"] == "Password" {
						given["password"] = true
					}
				} else if len(rewrites) == 0 {
					// A non-generator dataFrom (a whole remote Secret) can carry
					// any key; nothing to assert against.
					given["*"] = true
				}
				for _, r := range rewrites {
					rw, _ := r.(map[string]any)
					tr, _ := rw["transform"].(map[string]any)
					if tpl, ok := tr["template"].(string); ok && !strings.Contains(tpl, "{{") {
						given[tpl] = true
					} else {
						given["*"] = true // a computed rewrite — cannot be checked statically
					}
				}
			}
			for _, raw := range toSlice(spec["data"]) {
				d, _ := raw.(map[string]any)
				if k, ok := d["secretKey"].(string); ok {
					given[k] = true
				}
			}
			if !hasGenerator || given["*"] {
				continue
			}
			target, _ := spec["target"].(map[string]any)
			tmpl, _ := target["template"].(map[string]any)
			data, _ := tmpl["data"].(map[string]any)
			checked++
			for key, v := range data {
				for _, m := range keyRef.FindAllStringSubmatch(toString(v), -1) {
					if !given[m[1]] {
						t.Errorf("%s: ExternalSecret %v template key %q reads {{ .%s }}, which nothing in the ExternalSecret produces (available: %v)",
							strings.TrimPrefix(file, stackPackagesDir(t)+"/"), doc["metadata"].(map[string]any)["name"], key, m[1], keys(given))
					}
				}
			}
		}
	}
	if checked < 10 {
		t.Fatalf("only %d generator-backed ExternalSecrets were checked; the walk is broken", checked)
	}
}

func toSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The chart-bundled ClickHouse operator (0.19.0) can fail its first Service
// reconcile and never retry, holding the posthog sync at wave 0 for ever
// (2026-10-09). The unstick hook must run IN that wave — a PostSync hook would
// be gated on the very CHI it repairs — and must be the operator restart that
// was verified to clear it.
func TestPostHogUnstickHookRunsAlongsideTheClickHouseInstallation(t *testing.T) {
	install := readStackFile(t, "application/posthog/manifests/install.yaml")
	unstick := readStackFile(t, "application/posthog/manifests/clickhouse-unstick.yaml")
	chiWave := -1
	dec := yaml.NewDecoder(strings.NewReader(install))
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc != nil && doc["kind"] == "ClickHouseInstallation" {
			ann, _ := doc["metadata"].(map[string]any)["annotations"].(map[string]any)
			chiWave, _ = strconv.Atoi(toString(ann["argocd.argoproj.io/sync-wave"]))
		}
	}
	if chiWave < 0 {
		t.Fatal("no ClickHouseInstallation with a sync-wave in posthog's install.yaml")
	}
	dec = yaml.NewDecoder(strings.NewReader(unstick))
	found := false
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc == nil || doc["kind"] != "Job" {
			continue
		}
		found = true
		ann, _ := doc["metadata"].(map[string]any)["annotations"].(map[string]any)
		if ann["argocd.argoproj.io/hook"] != "Sync" {
			t.Errorf("the unstick Job is a %v hook; only a Sync hook runs while the wave is still open", ann["argocd.argoproj.io/hook"])
		}
		if w, _ := strconv.Atoi(toString(ann["argocd.argoproj.io/sync-wave"])); w > chiWave {
			t.Errorf("the unstick Job runs at wave %d, after the ClickHouseInstallation's wave %d — it would wait on the thing it repairs", w, chiWave)
		}
		script := toString(doc["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["args"].([]any)[0])
		for _, want := range []string{"rollout restart deployment clickhouse-operator", "already allocated", "Completed) "} {
			if !strings.Contains(script, want) {
				t.Errorf("the unstick script lost %q", want)
			}
		}
	}
	if !found {
		t.Fatal("no Job in clickhouse-unstick.yaml")
	}
}

// Tenancy created from the console's organisation switcher is an Organisation
// or Team XR applied by the console's ServiceAccount, so that account must be
// allowed to write them. Live on 2026-10-11 `kubectl auth can-i patch
// organisations.platform.adhar.io --as=system:serviceaccount:adhar-system:console`
// said no — the console could read every XR and provision none.
func TestConsoleMayWriteTenancyXRs(t *testing.T) {
	src := readStackFile(t, "core/adhar-console/manifests/install.yaml")
	dec := yaml.NewDecoder(strings.NewReader(src))
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc == nil || doc["kind"] != "ClusterRole" || toString(doc["metadata"].(map[string]any)["name"]) != "console-tenant-provisioner" {
			continue
		}
		for _, raw := range toSlice(doc["rules"]) {
			rule, _ := raw.(map[string]any)
			groups := toSlice(rule["apiGroups"])
			if len(groups) != 1 || toString(groups[0]) != "platform.adhar.io" {
				continue
			}
			have := map[string]bool{}
			for _, r := range toSlice(rule["resources"]) {
				have[toString(r)] = true
			}
			verbs := map[string]bool{}
			for _, v := range toSlice(rule["verbs"]) {
				verbs[toString(v)] = true
			}
			for _, want := range []string{"organisations", "teams"} {
				if !have[want] {
					t.Errorf("console-tenant-provisioner's platform.adhar.io rule lacks %s", want)
				}
			}
			for _, want := range []string{"create", "patch"} {
				if !verbs[want] {
					t.Errorf("console-tenant-provisioner cannot %s platform.adhar.io XRs; server-side apply needs it", want)
				}
			}
			return
		}
		t.Fatal("console-tenant-provisioner has no rule for platform.adhar.io — the console cannot provision Organisations or Teams")
	}
	t.Fatal("no ClusterRole console-tenant-provisioner in the console package")
}

// Plane's sign-in page can only draw a button for a method its FRONTEND knows.
//
// Until 1.4.2-2 the platform ran the fork's BACKEND under the STOCK frontend,
// whose auth bundle knew six `is_*_enabled` flags and no `keycloak`. On
// 2026-10-11 that locked the instance: the native provider came up, the Gitea
// bridge standing in for it was switched off, the password form was removed,
// and the page said "No authentication methods available" while the Job's own
// check — which counted a flag the page never read — printed OK.
//
// Every image is the fork's build now (TestPlaneUsesThePlatformsOwnImages), and
// its web/space/admin hooks render is_keycloak_enabled, so Keycloak is THE
// rendered method and the Gitea detour is retired. What must hold instead:
//
//  1. the verification counts is_keycloak_enabled among the renderable methods
//     (and still fails loudly when nothing renderable is on),
//  2. the password form is removed only when the Keycloak provider is verified
//     (client secret read AND the realm answering discovery), never on a flag
//     that was merely written,
//  3. there is no Gitea bridge left to draw a second button that also ends at
//     Keycloak — two buttons for one identity provider is not single sign-on.
//
// Comments are stripped first, so the prose above cannot satisfy any assertion.
func TestPlaneRendersTheKeycloakLoginAndNothingElse(t *testing.T) {
	code := withoutComments(readStackFile(t, "application/plane/manifests/instance-setup.yaml"))

	if !strings.Contains(code, "rendered_methods = [") {
		t.Fatal("the setup Job no longer declares the methods the frontend renders; without that list its " +
			"verification can pass on an instance whose sign-in page has no buttons")
	}
	rendered := code[strings.Index(code, "rendered_methods = ["):]
	rendered = rendered[:strings.Index(rendered, "]")]
	if !strings.Contains(rendered, "is_keycloak_enabled") {
		t.Error("is_keycloak_enabled is missing from the rendered methods; the fork frontend draws it, and " +
			"leaving it out means the Job fails a correctly configured Keycloak-only instance")
	}
	if !strings.Contains(code, "if not drawable:") {
		t.Error("the setup Job does not fail when no renderable method is enabled; that check is the " +
			"one that catches a locked sign-in page")
	}

	// 2. The password form comes off only behind a VERIFIED Keycloak provider.
	if !strings.Contains(code, "if keycloak_ready:\n") || !strings.Contains(code, `cfg("ENABLE_EMAIL_PASSWORD", "0", "AUTHENTICATION")`) {
		t.Error("the password form is not removed when Keycloak is verified; after an edge SSO login the " +
			"email/password box reads as a second identity and is the opposite of single sign-on")
	}
	if strings.Contains(code, "if keycloak_ready or gitea_ready:") {
		t.Error("the password form is gated on the retired Gitea bridge")
	}

	// 3. No Gitea detour: no bridge code, and the rows it wrote are switched off.
	for _, gone := range []string{"def gitea_bridge", "GITEA_PUBLIC_URL", "gitea_ready", `cfg("IS_GITEA_ENABLED", "1"`} {
		if strings.Contains(code, gone) {
			t.Errorf("the Gitea-federation bridge is still present (%q); the fork frontend renders Keycloak "+
				"directly, and a second button that also ends at Keycloak is not SSO", gone)
		}
	}
	if !strings.Contains(code, `cfg("IS_GITEA_ENABLED", "0", "GITEA")`) {
		t.Error("the setup Job must switch IS_GITEA_ENABLED off: clusters that ran the bridge still hold " +
			"its rows, and they would render a second Keycloak button")
	}
}

// A Sync hook does not "re-run on every sync" once the app has converged.
//
// OpenBao's bootstrap Job configures the OIDC auth method that puts a Keycloak
// button in its UI, and that step needs two Secrets the ExternalSecrets in
// oidc.yaml publish only after Keycloak's wave-20 config Job has written
// keycloak-clients. The Job used to SKIP the step when they were absent, on the
// reasoning — written in its own comment — that Argo CD would run the hook again
// later. It does not: a hook runs when a sync happens, and a Synced, Healthy app
// never syncs on its own.
//
// Live on 2026-10-11: the Job ran 43 minutes before the client secret existed,
// logged "OIDC prerequisites not ready … skipping for now", exited 0, and never
// ran again. The OpenBao UI offered no Keycloak sign-in, while both Secrets sat
// in the namespace looking perfectly healthy.
//
// So it must WAIT. Comments are stripped first, so the prose above cannot
// satisfy the assertions.
func TestOpenBaoBootstrapWaitsForItsOIDCPrerequisites(t *testing.T) {
	code := withoutComments(readStackFile(t, "security/openbao/manifests/bootstrap.yaml"))

	if strings.Contains(code, "skipping for now") {
		t.Error("the OpenBao bootstrap still skips the OIDC step when its prerequisites are missing; " +
			"nothing re-runs the hook once the app is Synced and Healthy, so the UI is left with no " +
			"Keycloak sign-in permanently")
	}
	if !strings.Contains(code, "read_oidc_prereqs") {
		t.Fatal("the OpenBao bootstrap no longer re-reads the OIDC prerequisites in a loop; without a " +
			"wait it races Keycloak's wave-20 config Job and loses on every fresh cluster")
	}
	// The wait must actually loop and sleep, not just read twice.
	if !strings.Contains(code, "while [ \"$i\" -lt 120 ]") || !strings.Contains(code, "sleep 10") {
		t.Error("the OIDC prerequisite wait is not a bounded retry loop; it must keep checking, because " +
			"the Secrets arrive tens of minutes after this Job starts")
	}
	// And when it genuinely gives up, it must say so loudly rather than exit 0 quietly.
	if !strings.Contains(code, "ERROR: OIDC prerequisites") {
		t.Error("the OpenBao bootstrap gives up on OIDC without logging an error; a silent success is " +
			"what hid this for the life of the cluster")
	}
}

// oidc_discovery_ca_pem REPLACES OpenBao's trust roots, it does not add to them.
//
// So pinning the self-signed platform CA breaks OIDC on every cluster whose edge
// serves a publicly trusted certificate — which is every cluster where
// cert-manager's DNS-01 solver works. Live on 2026-10-11 Keycloak presented a
// Let's Encrypt certificate that verified against the system roots on the first
// attempt, and the pinned CA turned the config write into
// `Code: 400 … error checking oidc discovery URL` — wording that points at the
// URL and never mentions trust, so it reads as an unreachable realm while the
// realm was answering 200.
//
// Which certificate the edge serves is a per-cluster fact, so the Job decides it
// by trying: system roots first, the platform CA as a fallback.
func TestOpenBaoOIDCPrefersTheSystemTrustRoots(t *testing.T) {
	code := withoutComments(readStackFile(t, "security/openbao/manifests/bootstrap.yaml"))

	// The platform CA must not be a precondition for attempting OIDC at all.
	if strings.Contains(code, `[ -n "${OIDC_SECRET}" ] && [ -n "${OIDC_CA}" ]`) {
		t.Error("the OpenBao bootstrap treats the platform CA as required for OIDC; a cluster with a " +
			"publicly trusted Keycloak certificate needs no custom CA, and requiring it skips OIDC " +
			"entirely on exactly those clusters")
	}
	// There must be an attempt that passes no CA at all.
	if !strings.Contains(code, "oidc_config()") || !strings.Contains(code, "if oidc_config >") {
		t.Fatal("the OpenBao bootstrap does not attempt the OIDC config write against the system trust " +
			"roots before pinning the platform CA; pinning replaces the trust set, so a public " +
			"certificate then fails to verify")
	}
	// And the platform CA must still be tried, for self-signed clusters.
	if !strings.Contains(code, `oidc_config oidc_discovery_ca_pem="${OIDC_CA}"`) {
		t.Error("the platform CA fallback is gone; a local or self-signed cluster has no publicly " +
			"trusted certificate and needs it")
	}
	// A failed write must not claim something will retry it.
	if strings.Contains(code, "will retry next sync") {
		t.Error(`the OIDC config write still logs "will retry next sync"; nothing re-runs a Sync hook ` +
			`on a converged app, so that message sends the reader looking for a retry that never comes`)
	}
}

// RustFS's console login is server-side configuration, and three of its details
// fail silently when wrong.
//
// The console ships OIDC support but draws a button only for what
// /rustfs/admin/v3/oidc/providers returns, and an unconfigured or
// failed-discovery RustFS returns an empty list — so the user who has already
// signed in to Keycloak at the edge is met by an Access Key / Secret Key form,
// with nothing in the API to say why (live 2026-10-11).
//
// The three silent ones:
//
//   - the IdP callback is on the ADMIN path and the provider id is a PATH
//     SEGMENT. Dropping `/default` lands on the SigV4-signed admin surface,
//     where the unsigned browser redirect is refused with
//     `AccessDenied: Signature is required`.
//   - RustFS 1.0 refuses outbound calls to private addresses as an SSRF guard,
//     so the in-cluster Keycloak origin must be allow-listed or start-up
//     discovery is blocked and the provider dropped.
//   - the redirect URI the server sends must equal the one Keycloak has
//     registered, or Keycloak answers `Invalid parameter: redirect_uri`.
func TestRustFSConsoleOffersTheKeycloakLogin(t *testing.T) {
	install := readStackFile(t, "data/rustfs/manifests/install.yaml")
	env, _ := containerEnv(t, install, "Deployment", "rustfs")

	const wantCallback = "https://rustfs.adhar.localtest.me:8443/rustfs/admin/v3/oidc/callback/default"
	if got := env["RUSTFS_IDENTITY_OPENID_REDIRECT_URI"]; got != wantCallback {
		t.Errorf("RUSTFS_IDENTITY_OPENID_REDIRECT_URI = %q, want %q: the callback lives on the admin "+
			"path and the provider id is a required path segment", got, wantCallback)
	}
	if strings.Contains(env["RUSTFS_IDENTITY_OPENID_REDIRECT_URI"], "/rustfs/console/") {
		t.Error("the IdP callback points into /rustfs/console/; that is where RustFS bounces the browser " +
			"AFTERWARDS with the STS credentials, not where the IdP must redirect")
	}
	if got := env["RUSTFS_IDENTITY_OPENID_REDIRECT_URI_DYNAMIC"]; got != "off" {
		t.Errorf("RUSTFS_IDENTITY_OPENID_REDIRECT_URI_DYNAMIC = %q, want \"off\": behind the Gateway and "+
			"the edge proxy the request headers are not the browser's URL", got)
	}

	// The discovery URL must be reachable under the egress policy, which means
	// its origin has to appear in the allow-list.
	cfgURL := env["RUSTFS_IDENTITY_OPENID_CONFIG_URL"]
	if cfgURL == "" {
		t.Fatal("RUSTFS_IDENTITY_OPENID_CONFIG_URL is unset; an empty config URL makes RustFS skip the " +
			"provider entirely and the console shows only the access-key form")
	}
	origin := cfgURL
	if i := strings.Index(strings.TrimPrefix(origin, "http://"), "/"); i >= 0 {
		origin = "http://" + strings.TrimPrefix(origin, "http://")[:i]
	}
	if allow := env["RUSTFS_OUTBOUND_ALLOW_ORIGINS"]; !strings.Contains(allow, origin) {
		t.Errorf("RUSTFS_OUTBOUND_ALLOW_ORIGINS = %q does not cover the discovery origin %q; RustFS 1.0 "+
			"blocks outbound calls to private addresses, so discovery fails at start-up and the "+
			"provider is dropped silently", allow, origin)
	}
	// A public issuer is required alongside the in-cluster discovery URL: the ID
	// token's `iss` is the public one.
	if iss := env["RUSTFS_IDENTITY_OPENID_ISSUER"]; !strings.HasPrefix(iss, "https://keycloak.") {
		t.Errorf("RUSTFS_IDENTITY_OPENID_ISSUER = %q; it must be the PUBLIC issuer, which is what the "+
			"ID token carries, while CONFIG_URL stays the in-cluster backchannel", iss)
	}

	// Keycloak must have registered exactly the callback the server sends.
	oidc := readStackFile(t, "data/rustfs/manifests/oidc.yaml")
	if !strings.Contains(oidc, wantCallback) {
		t.Errorf("the rustfs-app Keycloak client does not register %q; Keycloak refuses an unregistered "+
			"redirect_uri outright", wantCallback)
	}
	// Its own client, not the edge proxy's, so each receives a token minted for itself.
	if !strings.Contains(oidc, `"clientId": "rustfs-app"`) {
		t.Error("the RustFS console login does not use its own Keycloak client; sharing the edge proxy's " +
			"client shares an audience, which is the failure the platform already hit with Argo CD")
	}

	// The group claim is matched against policy names verbatim, so the policies
	// the activation Job writes must be named after the platform's groups.
	code := withoutComments(oidc)
	for _, g := range []string{"platform-admin", "platform-developer", "platform-viewer"} {
		if !strings.Contains(code, "admin_policy "+g) {
			t.Errorf("no RustFS policy named %q is created; RustFS matches each value of the groups "+
				"claim against a policy name verbatim, so a login whose groups resolve to no policy "+
				"is refused", g)
		}
	}
	if got := env["RUSTFS_IDENTITY_OPENID_GROUPS_CLAIM"]; got != "groups" {
		t.Errorf("RUSTFS_IDENTITY_OPENID_GROUPS_CLAIM = %q; the realm emits bare group names on "+
			"`groups`, which is what the OpenBao mapping also reads", got)
	}
}

// A RustFS policy statement may name actions from only ONE family.
//
// `{"Action":["admin:*","s3:*"]}` in a single statement is rejected outright —
// `InvalidRequest: 'Action' contains mixed action families in the same
// statement` — and that loses the WHOLE policy. Seen live on 2026-10-11: the
// platform-admin policy was refused while the other two were written, and the
// activation Job logged a warning and carried on, so the only sign was one
// group whose members could not sign in at all (a login whose claims resolve to
// no policy is refused). Split per family instead; the admin family also takes
// no S3 ARN.
func TestRustFSPolicyStatementsKeepOneActionFamily(t *testing.T) {
	code := withoutComments(readStackFile(t, "data/rustfs/manifests/oidc.yaml"))

	checked := 0
	for _, line := range strings.Split(code, "\n") {
		line = strings.TrimSpace(line)
		name, rest, ok := strings.Cut(line, "=")
		if !ok || !strings.HasSuffix(name, "_DOC") {
			continue
		}
		raw := strings.Trim(strings.TrimSpace(rest), "'")
		var doc struct {
			Statement []struct {
				Action   []string `json:"Action"`
				Resource []string `json:"Resource"`
			} `json:"Statement"`
		}
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			t.Errorf("%s is not valid JSON (%v); RustFS rejects the policy and the groups that name it "+
				"can no longer sign in", name, err)
			continue
		}
		if len(doc.Statement) == 0 {
			t.Errorf("%s has no statements", name)
			continue
		}
		checked++
		for i, st := range doc.Statement {
			families := map[string]bool{}
			for _, a := range st.Action {
				fam, _, _ := strings.Cut(a, ":")
				families[fam] = true
			}
			if len(families) > 1 {
				t.Errorf("%s statement %d mixes action families %v; RustFS refuses the whole policy "+
					"with \"'Action' contains mixed action families in the same statement\", and a "+
					"group that resolves to no policy cannot sign in", name, i, keys(families))
			}
			// admin, sts and kms are not resource-scoped, and RustFS wants the
			// key ABSENT rather than wildcarded: "Resource":["*"] on an admin
			// statement is rejected with
			// `invalid resource, type: 'unknown', pattern: '*'`, which again
			// loses the whole policy. (The upstream README shows an S3 ARN on an
			// admin statement; the parser does not accept it.)
			for fam := range families {
				switch fam {
				case "admin", "sts", "kms":
					if len(st.Resource) > 0 {
						t.Errorf("%s statement %d is a %q statement carrying Resource %v; that family "+
							"is not resource-scoped and RustFS rejects the policy outright",
							name, i, fam, st.Resource)
					}
				default:
					if len(st.Resource) == 0 {
						t.Errorf("%s statement %d grants %q actions with no Resource", name, i, fam)
					}
				}
			}
		}
	}
	if checked < 3 {
		t.Fatalf("found only %d policy documents; this guard is no longer checking the policies the "+
			"group claim resolves to", checked)
	}
}

// Nexus OSS has no native OIDC, so its Keycloak login IS the Rut Auth header
// realm — and two separate defects each disabled it completely.
//
//  1. The active-realm list contained an id Nexus does not have. Nexus
//     validates the array as a WHOLE and answers
//     `400 ValidationErrorXO{message='"Unknown realmIds: [...]"'}`, so one bad
//     id left NONE of them active. Live on the 2026-10-11 GCP cluster the
//     active list was just ["NexusAuthenticatingRealm"]: no header realm, so
//     Nexus drew its own sign-in form behind a working Keycloak proxy, and no
//     NpmToken realm either, so `npm publish` had no bearer-token realm.
//     The valid ids in 3.79.1 are ConanToken, DefaultRole, DockerToken,
//     LdapRealm, NexusAuthenticatingRealm, NpmToken, NuGetApiKey, rutauth-realm.
//  2. The capability-exists check used `grep -c ... || echo 0`. `grep -c`
//     PRINTS "0" and also EXITS 1 when it matches nothing, so the fallback
//     appended a second zero and the value became "0\n0", which never equals
//     "0" — the branch that creates the capability was therefore never taken.
//
// Both were invisible because the responses were piped to /dev/null and the
// step printed success regardless.
func TestNexusEnablesTheHeaderRealmThatCarriesKeycloak(t *testing.T) {
	raw := readStackFile(t, "application/nexus/manifests/config-job.yaml")
	code := withoutComments(raw)

	// Only ids Nexus actually knows may appear in the activation payload.
	valid := map[string]bool{
		"ConanToken": true, "DefaultRole": true, "DockerToken": true, "LdapRealm": true,
		"NexusAuthenticatingRealm": true, "NpmToken": true, "NuGetApiKey": true, "rutauth-realm": true,
	}
	i := strings.Index(code, "security/realms/active")
	if i < 0 {
		t.Fatal("the nexus config job no longer activates any auth realm; without rutauth-realm the " +
			"Keycloak proxy in front of Nexus cannot log anyone in")
	}
	seg := code[i:]
	start, end := strings.Index(seg, "'["), strings.Index(seg, "]'")
	if start < 0 || end < 0 || end < start {
		t.Fatal("cannot find the realm payload; this guard is no longer checking it")
	}
	var payload []string
	if err := json.Unmarshal([]byte(seg[start+1:end+1]), &payload); err != nil {
		t.Fatalf("the realm payload is not valid JSON (%v); Nexus rejects the whole request", err)
	}
	for _, id := range payload {
		if !valid[id] {
			t.Errorf("the realm payload names %q, which Nexus 3.79.1 does not have; it validates the "+
				"array as a whole, so one unknown id leaves NO realms active", id)
		}
	}
	for _, need := range []string{"rutauth-realm", "NpmToken"} {
		found := false
		for _, id := range payload {
			if id == need {
				found = true
			}
		}
		if !found {
			t.Errorf("the realm payload is missing %q", need)
		}
	}

	// `grep -c ... || echo 0` must never gate a branch.
	if strings.Contains(code, "grep -c") {
		t.Error(`the job gates on "grep -c", which prints 0 AND exits 1 when it matches nothing; with ` +
			`an "|| echo 0" fallback the value becomes two lines and no equality test on it can ever ` +
			`be true. Use grep -q.`)
	}
	// And the failures must be reported rather than discarded.
	if !strings.Contains(code, "ERROR: enabling the auth realms failed") {
		t.Error("the realm activation does not report a failure; discarding the response is what hid " +
			"an unknown realm id for the life of the cluster")
	}
}
