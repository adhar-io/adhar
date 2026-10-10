package adharplatform

// Two applications carry a Keycloak login of their OWN, inside the app, behind
// the edge proxy every URL already has — and in both the platform runs a FORK
// to get it, so the wiring below is the only thing that makes the fork worth
// shipping. Found on the 2026-10-09 AWS cluster: Plane ran the
// ghcr.io/adhar-io/plane-backend image for a week with
// is_keycloak_enabled=False, because the fork reads its settings from
// InstanceConfiguration rows that nothing on the platform wrote.

import (
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
// creates; a run fired before it exists must WAIT, not fail at `build` with
// CreateContainerConfigError (2026-10-09: webhook 15:23:19Z, Secret 15:32:52Z,
// no retry). Both pipelines gate build on the wait Task.
func TestLibraryReleasesWaitForNexusCredentialsBeforeBuilding(t *testing.T) {
	maven := readStackFile(t, "application/adhar-libraries/manifests/maven-release.yaml")
	if !strings.Contains(withoutComments(maven), "\n  name: wait-for-nexus-credentials\n") {
		t.Fatal("Task wait-for-nexus-credentials is gone from maven-release.yaml")
	}
	// tekton.dev/v1 steps spell their limits `computeResources`; the v1beta1
	// `resources` key is rejected by Tekton's webhook as an unknown field and
	// the whole adhar-libraries Application then fails to sync (2026-10-09).
	dec := yaml.NewDecoder(strings.NewReader(maven))
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc["kind"] != "Task" {
			continue
		}
		spec, _ := doc["spec"].(map[string]any)
		steps, _ := spec["steps"].([]any)
		for _, raw := range steps {
			step, _ := raw.(map[string]any)
			if _, bad := step["resources"]; bad {
				t.Errorf("Task %v step %v uses v1beta1 `resources`; tekton.dev/v1 wants computeResources",
					doc["metadata"].(map[string]any)["name"], step["name"])
			}
		}
	}
	for _, file := range []string{
		"application/adhar-libraries/manifests/maven-release.yaml",
		"application/adhar-libraries/manifests/npm-release.yaml",
	} {
		dec := yaml.NewDecoder(strings.NewReader(readStackFile(t, file)))
		pipelines := 0
		for {
			var doc map[string]any
			if err := dec.Decode(&doc); err != nil {
				break
			}
			if doc["kind"] != "Pipeline" {
				continue
			}
			pipelines++
			spec, _ := doc["spec"].(map[string]any)
			tasks, _ := spec["tasks"].([]any)
			waiters := map[string]bool{}
			var build map[string]any
			for _, raw := range tasks {
				task, _ := raw.(map[string]any)
				ref, _ := task["taskRef"].(map[string]any)
				if ref["name"] == "wait-for-nexus-credentials" {
					waiters[toString(task["name"])] = true
				}
				if task["name"] == "build" {
					build = task
				}
			}
			if len(waiters) == 0 {
				t.Errorf("%s: no task references wait-for-nexus-credentials", file)
				continue
			}
			if build == nil {
				t.Errorf("%s: no build task", file)
				continue
			}
			after, _ := build["runAfter"].([]any)
			gated := false
			for _, a := range after {
				if waiters[toString(a)] {
					gated = true
				}
			}
			if !gated {
				t.Errorf("%s: build.runAfter = %v does not include the nexus-credentials wait", file, after)
			}
		}
		if pipelines == 0 {
			t.Errorf("%s: no Pipeline found", file)
		}
	}
}

// The AI runtime completes on its own behalf — chores, webhooks, anonymous
// chats, and retrieval's rewrite and rerank on EVERY run — with a token for
// the `adhar-ai` Keycloak client, whose service-account user is in no platform
// group. Without a grant keyed on that identity the gateway answers 403 and
// the runtime produces nothing but CircuitOpen (AWS cluster, 2026-10-09).
func TestTheAiRuntimeIdentityIsGrantedOnTheGateway(t *testing.T) {
	grant := readStackFile(t, "ai/adhar-ai/manifests/gateway-grant.yaml")
	code := withoutComments(grant)
	for _, want := range []string{
		"kind: AgentgatewayPolicy",
		"name: adhar-ai-gateway",
		"action: Allow",
		`default(jwt.azp, "") == "adhar-ai"`,
		`request.path.startsWith("/v1/")`,
	} {
		if !strings.Contains(code, want) {
			t.Errorf("ai/adhar-ai/manifests/gateway-grant.yaml lost %q", want)
		}
	}
	// One expression: the gateway OR's the entries of an Allow rule, so a second
	// entry would widen the grant to whatever it matched on its own.
	if n := strings.Count(code, "\n          - "); n != 1 {
		t.Errorf("the runtime grant must be exactly one matchExpressions entry, found %d", n)
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
