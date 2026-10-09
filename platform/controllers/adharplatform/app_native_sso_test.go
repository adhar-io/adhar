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
