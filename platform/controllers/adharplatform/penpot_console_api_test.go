package adharplatform

// The Adhar Console calls Penpot's RPC API as a robot profile with an access
// token, and embeds Penpot's viewer in an iframe. Each half needs several
// things to agree, and each failed without a word before:
//   - Penpot compiles access-token authentication out unless `access-tokens`
//     is in PENPOT_FLAGS, so every token is answered as anonymous (401);
//   - nothing minted a token, so the console could only link to Penpot;
//   - the console reads the token from env, which is resolved once when the
//     container starts, so a Secret that lands later stays invisible;
//   - Penpot's nginx sends `X-Frame-Options: SAMEORIGIN`, so the browser
//     refused to frame it from console.<host>.
// The token Job's behaviour is exercised against an in-process Penpot that
// answers the 2.17.2 RPC commands the way the backend does
// (backend/src/app/rpc/commands/{auth,access_token,teams}.clj and
// http/{middleware,session,access_token}.clj): a token is `Authorization:
// Token`, a session is the `auth-token` cookie or `Authorization: Bearer`.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"adhar-io/adhar/api/v1alpha1"
)

const (
	penpotTokenSecret   = "penpot-api-token"
	penpotRobotSecret   = "penpot-console-robot"
	penpotTokenJob      = "penpot-console-token"
	penpotInClusterURL  = "http://penpot.adhar-system.svc.cluster.local:8080"
	penpotConsoleDeploy = "adhar-console"
)

// The keys the console reads from Secret penpot-api-token.
var penpotTokenContract = []string{"PENPOT_TEAM_ID", "PENPOT_TOKEN", "PENPOT_URL"}

func penpotTokenDocs(t *testing.T) []map[string]any {
	t.Helper()
	return yamlDocs(t, filepath.Join(penpotDir(t), "manifests", "console-token.yaml"))
}

func penpotDocNamed(t *testing.T, docs []map[string]any, kind, name string) map[string]any {
	t.Helper()
	for _, d := range docs {
		if d["kind"] == kind && metaName(d) == name {
			return d
		}
	}
	t.Fatalf("manifests/console-token.yaml has no %s %s", kind, name)
	return nil
}

func penpotAt(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			s, _ := v.([]any)
			if k >= len(s) {
				return nil
			}
			v = s[k]
		}
	}
	return v
}

// The shipped script, exactly as the ConfigMap carries it.
func penpotTokenScript(t *testing.T) string {
	t.Helper()
	cm := penpotDocNamed(t, penpotTokenDocs(t), "ConfigMap", penpotTokenJob)
	s, _ := penpotAt(cm, "data", "run.sh").(string)
	if s == "" {
		t.Fatalf("ConfigMap %s has no run.sh", penpotTokenJob)
	}
	return s
}

// scriptDefault returns the value run.sh falls back to for VAR (`VAR=${VAR:-x}`).
func penpotScriptDefault(t *testing.T, script, name string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^` + name + `=\$\{[A-Z_]+:-([^}]*)\}$`).FindStringSubmatch(script)
	if m == nil {
		t.Fatalf("run.sh has no default for %s", name)
	}
	return m[1]
}

// Penpot compiles token authentication out without `access-tokens`
// (2.17.2 backend/src/app/http/access_token.clj `authz`). `webhooks` stays
// off: nothing consumes Penpot webhooks, and the flag lets every team point
// penpot-backend at a URL of its choice. install.yaml is generated, so the
// flags must come from values.yaml, and the chart's literal "$PENPOT_FLAGS "
// must be dropped by the generator (post-render.py), not by hand.
func TestPenpotEnablesAccessTokensButNotWebhooks(t *testing.T) {
	var values struct {
		Config struct {
			Flags string `yaml:"flags"`
		} `yaml:"config"`
	}
	if err := yaml.Unmarshal([]byte(penpotRead(t, "values.yaml")), &values); err != nil {
		t.Fatal(err)
	}
	want := strings.Fields(values.Config.Flags)
	if !slices.Contains(want, "enable-access-tokens") {
		t.Errorf("values.yaml config.flags lacks enable-access-tokens (got %q): without it the console's access token is answered 401", values.Config.Flags)
	}
	if slices.Contains(want, "enable-webhooks") {
		t.Errorf("values.yaml config.flags carries enable-webhooks (got %q): nothing on the platform consumes Penpot webhooks, and the flag lets every team point penpot-backend at a URL of its choice", values.Config.Flags)
	}

	install := penpotRead(t, "manifests/install.yaml")
	if strings.Contains(install, "$PENPOT_FLAGS") {
		t.Error("install.yaml still carries the chart's literal $PENPOT_FLAGS (post-render.py strips it)")
	}
	for _, d := range []string{"penpot-backend", "penpot-frontend"} {
		_, cs := penpotDeployment(t, install, d)
		got := strings.Fields(penpotLiteral(t, cs[d].env(), "PENPOT_FLAGS"))
		if !slices.Equal(got, want) {
			t.Errorf("%s: PENPOT_FLAGS = %q, want exactly values.yaml config.flags %q — regenerate install.yaml with generate-manifests.sh", d, got, want)
		}
		for _, w := range got {
			if !strings.HasPrefix(w, "enable-") && !strings.HasPrefix(w, "disable-") {
				t.Errorf("%s: PENPOT_FLAGS carries %q, which is not a flag", d, w)
			}
		}
	}

	// The generator keeps it that way: hand post-render.py what the chart
	// renders (the placeholder back in front of the flags) and it must give
	// back install.yaml byte for byte.
	python3, err := exec.LookPath("python3")
	if err != nil {
		t.Log("python3 not available; post-render.py not exercised")
		return
	}
	const flagsValue = "- name: PENPOT_FLAGS\n              value: \""
	chart := strings.ReplaceAll(install, flagsValue, flagsValue+"$PENPOT_FLAGS ")
	if strings.Count(chart, "$PENPOT_FLAGS ") != 2 {
		t.Fatalf("could not rebuild the chart's rendering of PENPOT_FLAGS from install.yaml")
	}
	tmp := filepath.Join(t.TempDir(), "install.yaml")
	if err := os.WriteFile(tmp, []byte(chart), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(python3, filepath.Join(penpotDir(t), "post-render.py"), tmp).CombinedOutput(); err != nil {
		t.Fatalf("post-render.py: %v\n%s", err, out)
	}
	if got, _ := os.ReadFile(tmp); string(got) != install {
		t.Error("post-render.py does not turn the chart's rendering into install.yaml: the $PENPOT_FLAGS strip lives only in the generated file")
	}
}

// What the console is promised: Secret penpot-api-token with exactly
// PENPOT_TOKEN, PENPOT_TEAM_ID and PENPOT_URL (the frontend Service, which is
// also the console's own PENPOT_URL), written by the Job, from a robot whose
// password is generated once.
func TestPenpotConsoleTokenSecretContract(t *testing.T) {
	script := penpotTokenScript(t)
	m := regexp.MustCompile(`(?s)data: \{(.*?)\}\}'`).FindStringSubmatch(script)
	if m == nil {
		t.Fatal("run.sh no longer builds the Secret's data in one jq object; update this guard with it")
	}
	var keys []string
	for _, k := range regexp.MustCompile(`([A-Z_]+):`).FindAllStringSubmatch(m[1], -1) {
		keys = append(keys, k[1])
	}
	sort.Strings(keys)
	if !slices.Equal(keys, penpotTokenContract) {
		t.Errorf("run.sh writes %v into %s, want exactly %v (the console's contract)", keys, penpotTokenSecret, penpotTokenContract)
	}
	if got := penpotScriptDefault(t, script, "PENPOT_URL"); got != penpotInClusterURL {
		t.Errorf("run.sh publishes PENPOT_URL=%s, want %s: the frontend nginx proxies /api and resolves /assets redirects in the cluster", got, penpotInClusterURL)
	}
	if got := penpotScriptDefault(t, script, "SECRET"); got != penpotTokenSecret {
		t.Errorf("run.sh writes Secret %s, want %s (the name the console's env reads)", got, penpotTokenSecret)
	}

	docs := penpotTokenDocs(t)
	gen := penpotDocNamed(t, docs, "Password", penpotRobotSecret)
	if n, _ := penpotAt(gen, "spec", "length").(int); n < 24 {
		t.Errorf("Password %s length %v; want at least 24", penpotRobotSecret, n)
	}
	es := penpotDocNamed(t, docs, "ExternalSecret", penpotRobotSecret)
	if p := penpotAt(es, "spec", "refreshPolicy"); p != "CreatedOnce" {
		t.Errorf("ExternalSecret %s refreshPolicy = %v, want CreatedOnce: the robot registers with this password once, and Penpot cannot reset another profile's password", penpotRobotSecret, p)
	}
	if n := penpotAt(es, "spec", "target", "name"); n != penpotRobotSecret {
		t.Errorf("ExternalSecret %s writes Secret %v", penpotRobotSecret, n)
	}
	data, _ := penpotAt(es, "spec", "target", "template", "data").(map[string]any)
	if data["email"] == nil || !strings.Contains(fmt.Sprint(data["password"]), "{{ .password }}") {
		t.Errorf("ExternalSecret %s must template email and password (from the generator's `password` key), got %v", penpotRobotSecret, data)
	}
	if g := penpotAt(es, "spec", "dataFrom", 0, "sourceRef", "generatorRef", "name"); g != penpotRobotSecret {
		t.Errorf("ExternalSecret %s reads generator %v, want Password %s", penpotRobotSecret, g, penpotRobotSecret)
	}

	job := penpotDocNamed(t, docs, "Job", penpotTokenJob)
	refs := map[string]string{}
	for _, e := range penpotSlice(penpotAt(job, "spec", "template", "spec", "containers", 0, "env")) {
		ref, _ := penpotAt(e, "valueFrom", "secretKeyRef").(map[string]any)
		if ref != nil {
			refs[fmt.Sprint(penpotAt(e, "name"))] = fmt.Sprint(ref["name"]) + "/" + fmt.Sprint(ref["key"])
		}
	}
	if refs["ROBOT_EMAIL"] != penpotRobotSecret+"/email" || refs["ROBOT_PASSWORD"] != penpotRobotSecret+"/password" {
		t.Errorf("the Job reads the robot from %v, want %s keys email and password", refs, penpotRobotSecret)
	}

	// The Job owns the Secret. Declared in Git as well, Argo CD would revert or
	// prune what the Job writes.
	for _, f := range stackPackageManifests(t) {
		for _, d := range yamlDocs(t, f) {
			declares := d["kind"] == "Secret" && metaName(d) == penpotTokenSecret
			targets := d["kind"] == "ExternalSecret" && penpotAt(d, "spec", "target", "name") == penpotTokenSecret
			if declares || targets {
				t.Errorf("%s declares %s %s; only the %s Job writes it", f, d["kind"], penpotTokenSecret, penpotTokenJob)
			}
		}
	}
}

// Argo CD leaves hooks out of the app's sync status, so a change made only
// inside a hook never re-runs it. The script therefore lives in a tracked
// ConfigMap the hook mounts, and the hook's env carries nothing that decides
// behaviour.
func TestPenpotConsoleTokenHookRunsAScriptFromATrackedConfigMap(t *testing.T) {
	docs := penpotTokenDocs(t)
	job := penpotDocNamed(t, docs, "Job", penpotTokenJob)
	ann := metaAnnotations(job)
	if ann["argocd.argoproj.io/hook"] != "PostSync" || ann["argocd.argoproj.io/hook-delete-policy"] != "BeforeHookCreation" {
		t.Errorf("Job %s must be a PostSync hook with BeforeHookCreation, got %v", penpotTokenJob, ann)
	}
	pod := penpotAt(job, "spec", "template", "spec")
	c := penpotAt(pod, "containers", 0)
	cmd := penpotSlice(penpotAt(c, "command"))
	if len(cmd) != 2 || !strings.HasSuffix(fmt.Sprint(cmd[0]), "bash") || penpotAt(c, "args") != nil {
		t.Fatalf("Job %s must run `bash <mounted script>` with no inline args, got command %v args %v", penpotTokenJob, cmd, penpotAt(c, "args"))
	}
	script := fmt.Sprint(cmd[1])
	var volume string
	for _, vm := range penpotSlice(penpotAt(c, "volumeMounts")) {
		if mp := fmt.Sprint(penpotAt(vm, "mountPath")); strings.HasPrefix(script, strings.TrimSuffix(mp, "/")+"/") {
			volume = fmt.Sprint(penpotAt(vm, "name"))
		}
	}
	var configMap string
	for _, v := range penpotSlice(penpotAt(pod, "volumes")) {
		if penpotAt(v, "name") == volume {
			configMap, _ = penpotAt(v, "configMap", "name").(string)
		}
	}
	if volume == "" || configMap == "" {
		t.Fatalf("Job %s runs %s, which is not mounted from a ConfigMap", penpotTokenJob, script)
	}
	cm := penpotDocNamed(t, docs, "ConfigMap", configMap)
	if metaAnnotations(cm)["argocd.argoproj.io/hook"] != nil {
		t.Errorf("ConfigMap %s is a hook itself; it must be tracked so a script change re-syncs the app", configMap)
	}
	if s, _ := penpotAt(cm, "data", filepath.Base(script)).(string); !strings.Contains(s, "create-access-token") {
		t.Errorf("ConfigMap %s key %s is not the token script", configMap, filepath.Base(script))
	}
	for _, e := range penpotSlice(penpotAt(c, "env")) {
		switch n := fmt.Sprint(penpotAt(e, "name")); n {
		case "HOME", "NAMESPACE", "ROBOT_EMAIL", "ROBOT_PASSWORD":
		default:
			t.Errorf("Job %s sets %s in its env; behaviour belongs in the tracked script (a hook-only change never runs)", penpotTokenJob, n)
		}
	}

	// Least privilege: the Secret it owns, the console it restarts. `create`
	// narrows to the name as well: the Job writes the Secret only with
	// server-side apply, a PATCH to the object's URL, and the API server
	// authorises the create it implies with that name (k8s.io/apiserver
	// handlers/patch.go createAuthorizerAttributes). An unnamed `create` let
	// this ServiceAccount make any Secret in adhar-system.
	role := penpotDocNamed(t, docs, "Role", penpotTokenJob)
	secretVerbs := map[string]bool{}
	for _, r := range penpotSlice(penpotAt(role, "rules")) {
		res := fmt.Sprint(penpotAt(r, "resources"))
		verbs := fmt.Sprint(penpotAt(r, "verbs"))
		names := fmt.Sprint(penpotAt(r, "resourceNames"))
		switch {
		case strings.Contains(res, "secrets") && names != "["+penpotTokenSecret+"]":
			t.Errorf("Role %s grants %s on secrets beyond %s", penpotTokenJob, verbs, penpotTokenSecret)
		case strings.Contains(res, "secrets"):
			for _, v := range penpotSlice(penpotAt(r, "verbs")) {
				secretVerbs[fmt.Sprint(v)] = true
			}
		case strings.Contains(res, "deployments") && names != "["+penpotConsoleDeploy+"]":
			t.Errorf("Role %s grants %s on deployments beyond %s", penpotTokenJob, verbs, penpotConsoleDeploy)
		}
	}
	for _, v := range []string{"get", "create", "patch"} {
		if !secretVerbs[v] {
			t.Errorf("Role %s cannot %s Secret %s: the Job reads it, and server-side apply creates it on the first run and patches it after", penpotTokenJob, v, penpotTokenSecret)
		}
	}
	// The narrowed `create` holds only for that write path: a POST create
	// carries no name, so the API server would refuse it.
	runSh := penpotTokenScript(t)
	if !strings.Contains(runSh, "apply --server-side") || regexp.MustCompile(`\b(create|replace)\s+(secret|-f)`).MatchString(runSh) {
		t.Errorf("run.sh must write %s with `kubectl apply --server-side` and nothing else: the Role's name-scoped `create` admits only that", penpotTokenSecret)
	}
}

// The console Deployment exactly as core/adhar-console ships it.
func penpotConsoleDeployment(t *testing.T) map[string]any {
	t.Helper()
	path := filepath.Join(stackRoot(t), "packages", "core", "adhar-console", "manifests", "install.yaml")
	for _, d := range yamlDocs(t, path) {
		if d["kind"] == "Deployment" && metaName(d) == penpotConsoleDeploy {
			return d
		}
	}
	t.Fatalf("no Deployment %s in %s", penpotConsoleDeploy, path)
	return nil
}

// The console reads PENPOT_TOKEN and PENPOT_TEAM_ID from the Secret KEY BY
// KEY, optional (penpot is off locally), and never imports it whole: envFrom
// takes every key, so the Job's ServiceAccount, which may patch the Secret,
// could set any console variable (AUTH_COOKIE_SECRET, DATABASE_URL, …).
// PENPOT_URL is the console's own literal, equal to what the Job publishes.
// The Job restarts the console by Deployment name and finds its pods by
// label; both must be what the console actually ships.
func TestConsoleLoadsThePenpotTokenOptionally(t *testing.T) {
	deploy := penpotConsoleDeployment(t)
	var c any
	for _, x := range penpotSlice(penpotAt(deploy, "spec", "template", "spec", "containers")) {
		if penpotAt(x, "name") == "adhar-console" {
			c = x
		}
	}
	if c == nil {
		t.Fatal("the console Deployment has no adhar-console container")
	}
	for _, ef := range penpotSlice(penpotAt(c, "envFrom")) {
		if penpotAt(ef, "secretRef", "name") == penpotTokenSecret {
			t.Errorf("the console imports %s whole (envFrom): whatever can write it — the %s Job's ServiceAccount — could set any of the console's variables; read PENPOT_TOKEN and PENPOT_TEAM_ID with env secretKeyRef", penpotTokenSecret, penpotTokenJob)
		}
	}
	script := penpotTokenScript(t)
	var read []string
	url := false
	for _, e := range penpotSlice(penpotAt(c, "env")) {
		n := fmt.Sprint(penpotAt(e, "name"))
		if ref, _ := penpotAt(e, "valueFrom", "secretKeyRef").(map[string]any); ref != nil && ref["name"] == penpotTokenSecret {
			read = append(read, n)
			if ref["key"] != n {
				t.Errorf("the console's %s reads key %v of %s", n, ref["key"], penpotTokenSecret)
			}
			if ref["optional"] != true {
				t.Errorf("the console's %s must read %s optionally: without penpot (the local profile) the pod would never start", n, penpotTokenSecret)
			}
			continue
		}
		switch n {
		case "PENPOT_TOKEN", "PENPOT_TEAM_ID":
			t.Errorf("the console's %s does not come from %s, so the token the Job publishes never reaches it", n, penpotTokenSecret)
		case "PENPOT_URL":
			url = true
			if v := fmt.Sprint(penpotAt(e, "value")); v != penpotScriptDefault(t, script, "PENPOT_URL") {
				t.Errorf("the console's PENPOT_URL %s is not the URL the token is published for (%s)", v, penpotScriptDefault(t, script, "PENPOT_URL"))
			}
		}
	}
	sort.Strings(read)
	if want := []string{"PENPOT_TEAM_ID", "PENPOT_TOKEN"}; !slices.Equal(read, want) {
		t.Errorf("the console reads %v from %s, want exactly %v (PENPOT_URL is its own literal, so the Secret's writer cannot redirect the console's Penpot calls)", read, penpotTokenSecret, want)
	}
	if !url {
		t.Error("the console's env has no PENPOT_URL literal; it would read the URL from a Secret a Job writes, or fall back to the public hostname behind the edge proxy")
	}
	if got := penpotScriptDefault(t, script, "CONSOLE"); got != penpotConsoleDeploy {
		t.Errorf("run.sh restarts Deployment %s, the console is %s", got, penpotConsoleDeploy)
	}
	label := regexp.MustCompile(`get pods -l ([a-z.\-/]+)=([a-z\-]+)`).FindStringSubmatch(script)
	podLabels, _ := penpotAt(deploy, "spec", "template", "metadata", "labels").(map[string]any)
	if label == nil || podLabels[label[1]] != label[2] {
		t.Errorf("run.sh finds console pods by %v, but they are labelled %v: it would never see them and never restart the console", label, podLabels)
	}
}

type penpotFramingRoute struct {
	Spec struct {
		Hostnames []string `yaml:"hostnames"`
		Rules     []struct {
			Matches []struct {
				Path struct{ Type, Value string } `yaml:"path"`
			} `yaml:"matches"`
			Filters []struct {
				Type                   string `yaml:"type"`
				ResponseHeaderModifier *struct {
					Remove []string                       `yaml:"remove"`
					Set    []struct{ Name, Value string } `yaml:"set"`
					Add    []struct{ Name, Value string } `yaml:"add"`
				} `yaml:"responseHeaderModifier"`
			} `yaml:"filters"`
			BackendRefs []struct{ Name string } `yaml:"backendRefs"`
		} `yaml:"rules"`
	} `yaml:"spec"`
}

func penpotStagedFile(t *testing.T, dir string, spec v1alpha1.BuildCustomizationSpec, file string) []byte {
	t.Helper()
	staged, cleanup, err := StageStack(dir, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	raw, err := os.ReadFile(filepath.Join(staged, file))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "{{") {
		t.Fatalf("%s: unrendered template left", file)
	}
	return raw
}

// Penpot's nginx sends X-Frame-Options: SAMEORIGIN, so the console could not
// frame Penpot's viewer. Every rule of the penpot route that serves content
// drops it and sends frame-ancestors naming exactly Penpot itself and the
// console's public origin — wherever the platform runs, since the host is
// rewritten at seed time.
func TestPenpotRouteLetsOnlyTheConsoleFrameIt(t *testing.T) {
	pkgs := filepath.Join(stackRoot(t), "packages")
	for _, spec := range []v1alpha1.BuildCustomizationSpec{
		{Protocol: "https", Host: "adhar.localtest.me", Port: "8443"},
		{Protocol: "https", Host: "dev.adhar.io", Port: "443"},
	} {
		spec.Normalize()
		var console struct {
			Spec struct {
				Hostnames []string `yaml:"hostnames"`
			} `yaml:"spec"`
		}
		if err := yaml.Unmarshal(penpotStagedFile(t, filepath.Join(pkgs, "core", "adhar-console", "manifests"), spec, "httproute.yaml"), &console); err != nil || len(console.Spec.Hostnames) != 1 {
			t.Fatalf("%s: cannot read the console's hostname (%v)", spec.Host, err)
		}
		origin := spec.Protocol + "://" + console.Spec.Hostnames[0] + spec.PortSuffix

		var r penpotFramingRoute
		if err := yaml.Unmarshal(penpotStagedFile(t, filepath.Join(pkgs, "application", "penpot", "manifests"), spec, "httproute.yaml"), &r); err != nil {
			t.Fatal(err)
		}
		served := 0
		for i, rule := range r.Spec.Rules {
			if len(rule.BackendRefs) == 0 {
				continue
			}
			served++
			var mods int
			for _, f := range rule.Filters {
				if f.Type != "ResponseHeaderModifier" || f.ResponseHeaderModifier == nil {
					continue
				}
				mods++
				m := f.ResponseHeaderModifier
				removes := false
				for _, h := range m.Remove {
					removes = removes || strings.EqualFold(h, "X-Frame-Options")
				}
				if !removes {
					t.Errorf("%s rule %d (%v): X-Frame-Options is not removed; Penpot's SAMEORIGIN blocks the console's frame", spec.Host, i, rule.Matches)
				}
				for _, h := range m.Add {
					if strings.EqualFold(h.Name, "X-Frame-Options") || strings.EqualFold(h.Name, "Content-Security-Policy") {
						t.Errorf("%s rule %d: `add` %s appends a second header instead of replacing it", spec.Host, i, h.Name)
					}
				}
				var ancestors []string
				csps := 0
				for _, h := range m.Set {
					if !strings.EqualFold(h.Name, "Content-Security-Policy") {
						continue
					}
					csps++
					for _, d := range strings.Split(h.Value, ";") {
						if f := strings.Fields(d); len(f) > 0 && strings.EqualFold(f[0], "frame-ancestors") {
							ancestors = append(ancestors, f[1:]...)
						}
					}
				}
				sort.Strings(ancestors)
				want := []string{"'self'", origin}
				sort.Strings(want)
				if csps != 1 || !slices.Equal(ancestors, want) {
					t.Errorf("%s rule %d (%v): frame-ancestors %q, want exactly %q — the console's origin, and nothing else", spec.Host, i, rule.Matches, ancestors, want)
				}
			}
			if mods != 1 {
				t.Errorf("%s rule %d (%v) to %s has %d ResponseHeaderModifier filters, want 1", spec.Host, i, rule.Matches, rule.BackendRefs[0].Name, mods)
			}
		}
		if served < 3 {
			t.Errorf("%s: only %d penpot rules serve content; the route changed shape, check this guard", spec.Host, served)
		}
	}
}

// ---------------------------------------------------------------------------
// The Job itself, against an in-process Penpot and a kubectl stand-in.
// ---------------------------------------------------------------------------

type penpotMock struct {
	mu          sync.Mutex
	seq         int
	profiles    map[string]*penpotMockProfile // by email
	pending     map[string]*penpotMockProfile // prepared registrations, by token
	sessions    map[string]string             // session token -> email
	tokens      map[string]*penpotMockToken   // token value -> token
	teams       []*penpotMockTeam
	calls       []string
	problems    []string
	expirations int  // create-access-token calls that asked for an expiry
	failTeams   bool // get-teams answers 500
	// What only a person can fix, and what a retry fixes.
	refuseLogin        string // login-with-password answers 400 with this code
	refuseRegistration string // prepare-register-profile answers 400 with this code
	unverified         bool   // a registered profile cannot sign in (email verification on)
	outage             string // this command answers 503
	notPenpot          bool   // every call answers 404, as a URL that is not Penpot's API would
}

type penpotMockProfile struct{ ID, Email, Password, Fullname string }

type penpotMockToken struct {
	ID, Name, Value, Email string
	Refused                bool // signed with a key Penpot no longer has
}

type penpotMockTeam struct {
	ID, Name string
	Default  bool
	Members  []string
}

func newPenpotMock() *penpotMock {
	return &penpotMock{profiles: map[string]*penpotMockProfile{}, pending: map[string]*penpotMockProfile{},
		sessions: map[string]string{}, tokens: map[string]*penpotMockToken{}}
}

func (m *penpotMock) id() string {
	m.seq++
	return fmt.Sprintf("71a41502-a5cb-81d9-8008-%012d", m.seq)
}

func (m *penpotMock) who(r *http.Request) string {
	if c, err := r.Cookie("auth-token"); err == nil && c.Value != "" {
		return m.sessions[c.Value]
	}
	a := regexp.MustCompile(`(?i)^(Token|Bearer)\s+(.*)`).FindStringSubmatch(r.Header.Get("Authorization"))
	if a == nil {
		return ""
	}
	if strings.EqualFold(a[1], "token") {
		if tk := m.tokens[a[2]]; tk != nil && !tk.Refused {
			return tk.Email
		}
		return ""
	}
	return m.sessions[a[2]]
}

func (m *penpotMock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	reply := func(status int, body any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if body != nil {
			_ = json.NewEncoder(w).Encode(body)
		}
	}
	fail := func(status int, typ, code string) { reply(status, map[string]string{"type": typ, "code": code}) }
	const prefix = "/api/main/methods/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		fail(404, "not-found", "object-not-found")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, prefix)
	m.calls = append(m.calls, name)
	if r.Method != http.MethodPost {
		m.problems = append(m.problems, name+": "+r.Method)
	}
	if !strings.Contains(r.Header.Get("Accept"), "application/json") || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		m.problems = append(m.problems, name+": not a JSON request (Penpot answers transit unless Accept is JSON, and ignores a non-JSON body)")
	}
	if name == m.outage {
		fail(503, "internal", "unavailable")
		return
	}
	if m.notPenpot {
		fail(404, "not-found", "object-not-found")
		return
	}
	params := map[string]any{}
	_ = json.NewDecoder(r.Body).Decode(&params)
	str := func(k string) string { s, _ := params[k].(string); return s }
	who := m.who(r)
	session := func(email string) {
		tok := "session-" + m.id()
		m.sessions[tok] = email
		http.SetCookie(w, &http.Cookie{Name: "auth-token", Value: tok, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, Expires: time.Now().Add(7 * 24 * time.Hour)})
	}
	profileJSON := func(p *penpotMockProfile) map[string]any {
		return map[string]any{"id": p.ID, "email": p.Email, "fullname": p.Fullname, "isActive": true}
	}

	switch name {
	case "get-profile":
		if p := m.profiles[who]; p != nil {
			reply(200, profileJSON(p))
			return
		}
		reply(200, map[string]any{"id": "00000000-0000-0000-0000-000000000000", "fullname": "Anonymous User"})
		return
	case "login-with-password":
		if m.refuseLogin != "" {
			fail(400, "restriction", m.refuseLogin)
			return
		}
		// An inactive (unverified) profile is answered like a wrong password.
		p := m.profiles[strings.ToLower(str("email"))]
		if p == nil || p.Password != str("password") || m.unverified {
			fail(400, "validation", "wrong-credentials")
			return
		}
		session(p.Email)
		reply(200, profileJSON(p))
		return
	case "prepare-register-profile":
		if m.refuseRegistration != "" {
			fail(400, "restriction", m.refuseRegistration)
			return
		}
		if str("fullname") == "" || str("email") == "" || str("password") == "" {
			fail(400, "validation", "params-validation")
			return
		}
		if m.profiles[strings.ToLower(str("email"))] != nil {
			fail(400, "validation", "email-already-exists")
			return
		}
		tok := "prepared-" + m.id()
		m.pending[tok] = &penpotMockProfile{Email: strings.ToLower(str("email")), Password: str("password"), Fullname: str("fullname")}
		reply(200, map[string]any{"token": tok})
		return
	case "register-profile":
		p := m.pending[str("token")]
		if p == nil {
			fail(400, "validation", "invalid-token")
			return
		}
		delete(m.pending, str("token"))
		p.ID = m.id()
		m.profiles[p.Email] = p
		m.teams = append(m.teams, &penpotMockTeam{ID: m.id(), Name: "Default", Default: true, Members: []string{p.Email}})
		session(p.Email)
		reply(200, profileJSON(p))
		return
	}

	if who == "" {
		fail(401, "authentication", "authentication-required")
		return
	}
	switch name {
	case "get-teams":
		if m.failTeams {
			fail(500, "internal", "unexpected")
			return
		}
		out := []map[string]any{}
		for _, tm := range m.teams {
			if slices.Contains(tm.Members, who) {
				out = append(out, map[string]any{"id": tm.ID, "name": tm.Name, "isDefault": tm.Default})
			}
		}
		reply(200, out)
	case "create-team":
		if str("name") == "" {
			fail(400, "validation", "params-validation")
			return
		}
		tm := &penpotMockTeam{ID: m.id(), Name: str("name"), Members: []string{who}}
		m.teams = append(m.teams, tm)
		reply(200, map[string]any{"id": tm.ID, "name": tm.Name, "isDefault": false})
	case "create-access-token":
		if str("name") == "" {
			fail(400, "validation", "params-validation")
			return
		}
		if _, ok := params["expiration"]; ok {
			m.expirations++
		}
		tk := &penpotMockToken{ID: m.id(), Name: str("name"), Value: "eyJhbGciOiJBMjU2S1ciLCJlbmMiOiJBMjU2R0NNIn0." + m.id(), Email: who}
		m.tokens[tk.Value] = tk
		reply(200, map[string]any{"id": tk.ID, "name": tk.Name, "token": tk.Value, "expiresAt": nil})
	case "get-access-tokens":
		out := []map[string]any{}
		for _, tk := range m.tokens {
			if tk.Email == who {
				out = append(out, map[string]any{"id": tk.ID, "name": tk.Name, "expiresAt": nil})
			}
		}
		reply(200, out)
	case "delete-access-token":
		for v, tk := range m.tokens {
			if tk.ID == str("id") && tk.Email == who {
				delete(m.tokens, v)
			}
		}
		reply(204, nil)
	default:
		fail(404, "not-found", "object-not-found")
	}
}

func (m *penpotMock) take() (calls, problems []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	calls, problems = m.calls, m.problems
	m.calls, m.problems = nil, nil
	return calls, problems
}

// kubectl for run.sh outside a cluster: Secrets are JSON files in $STUB_STATE,
// the console Deployment is deployment-<name> (JSON) while it exists, its pods
// are pods.json (listing them fails while pods.fail exists), every call is
// appended to calls.log and every restart to restarts (and moves the pods'
// start time to now).
const penpotKubectlStub = `#!/usr/bin/env bash
set -u
state=${STUB_STATE:?}
echo "$*" >>"$state/calls.log"
rest=()
while [ $# -gt 0 ]; do
  case "$1" in
    -n) shift 2 ;;
    *) rest+=("$1"); shift ;;
  esac
done
set -- "${rest[@]}"
case "$1 $2" in
  "get secret")
    if [ -f "$state/secret-$3.json" ]; then cat "$state/secret-$3.json"; exit 0; fi
    echo "Error from server (NotFound): secrets \"$3\" not found" >&2; exit 1 ;;
  "apply --server-side")
    obj=$(cat); name=$(printf '%s' "$obj" | jq -r .metadata.name)
    printf '%s' "$obj" | jq . >"$state/secret-$name.json"; echo "secret/$name serverside-applied"; exit 0 ;;
  "get deployment")
    [ -f "$state/deployment-$3" ] && { cat "$state/deployment-$3"; exit 0; }
    echo "Error from server (NotFound): deployments.apps \"$3\" not found" >&2; exit 1 ;;
  "rollout status") exit 0 ;;
  "get pods")
    if [ -f "$state/pods.fail" ]; then
      echo "Error from server (Timeout): the server was unable to return a response in the time allotted" >&2; exit 1
    fi
    cat "$state/pods.json"; exit 0 ;;
  "rollout restart")
    sleep 1
    now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    echo "$now" >>"$state/restarts"
    jq --arg t "$now" '.items |= map(.status.startTime = $t)' "$state/pods.json" >"$state/pods.next" && mv "$state/pods.next" "$state/pods.json"
    exit 0 ;;
esac
echo "stub kubectl: unsupported: $*" >&2
exit 2
`

const (
	penpotRobotEmail    = "adhar-console@adhar.local"
	penpotRobotPassword = "N0tTheRealOneButLongEnough4Tests"
)

// penpotJob runs the shipped run.sh outside a cluster: against an in-process
// Penpot (mock), with the kubectl stand-in keeping its state in a directory.
type penpotJob struct {
	t                       *testing.T
	bash, dir, state        string
	script, stub            string
	consoleFile, secretFile string
	mock                    *penpotMock
	url                     string
	printed                 []string
}

func newPenpotJob(t *testing.T) *penpotJob {
	t.Helper()
	bash := ""
	for _, tool := range []string{"bash", "curl", "jq"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not available", tool)
		}
		if tool == "bash" {
			bash = p
		}
	}
	dir := t.TempDir()
	j := &penpotJob{t: t, bash: bash, dir: dir, state: filepath.Join(dir, "state"),
		script: filepath.Join(dir, "run.sh"), stub: filepath.Join(dir, "kubectl"), mock: newPenpotMock()}
	j.consoleFile = filepath.Join(j.state, "deployment-"+penpotConsoleDeploy)
	j.secretFile = filepath.Join(j.state, "secret-"+penpotTokenSecret+".json")
	if err := os.MkdirAll(j.state, 0o755); err != nil {
		t.Fatal(err)
	}
	j.write(j.script, penpotTokenScript(t), 0o644)
	j.write(j.stub, penpotKubectlStub, 0o755)
	j.write(filepath.Join(j.state, "pods.json"), `{"items":[{"metadata":{"name":"adhar-console-1"},"status":{"phase":"Running","startTime":"2026-01-01T00:00:00Z"}}]}`, 0o644)
	srv := httptest.NewServer(j.mock)
	t.Cleanup(srv.Close)
	j.url = srv.URL
	return j
}

func (j *penpotJob) write(path, body string, mode os.FileMode) {
	j.t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		j.t.Fatal(err)
	}
}

// console serves a console Deployment that reads a key of each named Secret
// (env secretKeyRef) and nothing else.
func (j *penpotJob) console(secrets ...string) {
	env := []map[string]any{}
	for _, n := range secrets {
		env = append(env, map[string]any{"name": strings.ToUpper(strings.ReplaceAll(n, "-", "_")),
			"valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": n, "key": "token", "optional": true}}})
	}
	j.serveConsole(map[string]any{"kind": "Deployment", "metadata": map[string]any{"name": penpotConsoleDeploy},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []map[string]any{{"name": "adhar-console", "env": env}}}}}})
}

// serveConsole serves d as the console Deployment.
func (j *penpotJob) serveConsole(d map[string]any) {
	j.t.Helper()
	b, err := json.Marshal(d)
	if err != nil {
		j.t.Fatal(err)
	}
	j.write(j.consoleFile, string(b), 0o644)
}

func (j *penpotJob) run(pass string) error {
	cmd := exec.Command(j.bash, j.script)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + j.dir,
		"PENPOT_URL=" + j.url, "ROBOT_EMAIL=" + penpotRobotEmail, "ROBOT_PASSWORD=" + pass,
		"KUBECTL=" + j.stub, "STUB_STATE=" + j.state, "NAMESPACE=adhar-system",
		"WAIT_TRIES=3", "WAIT_SECONDS=0", "ROLLOUT_TIMEOUT=1s",
	}
	out, err := cmd.CombinedOutput()
	j.printed = append(j.printed, string(out))
	j.t.Logf("run.sh:\n%s", out)
	return err
}

// said is what the last run printed.
func (j *penpotJob) said() string { return j.printed[len(j.printed)-1] }

func (j *penpotJob) published() map[string]string {
	j.t.Helper()
	raw, err := os.ReadFile(j.secretFile)
	if err != nil {
		j.t.Fatalf("no Secret %s was written: %v", penpotTokenSecret, err)
	}
	var s struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		j.t.Fatal(err)
	}
	out := map[string]string{}
	for k, v := range s.Data {
		b, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			j.t.Fatalf("%s: %v", k, err)
		}
		out[k] = string(b)
	}
	return out
}

func (j *penpotJob) restarts() int {
	raw, _ := os.ReadFile(filepath.Join(j.state, "restarts"))
	return len(strings.Fields(string(raw)))
}

func penpotCalled(calls []string, name string) int {
	n := 0
	for _, c := range calls {
		if c == name {
			n++
		}
	}
	return n
}

func TestPenpotConsoleTokenJobMintsReusesAndRotates(t *testing.T) {
	j := newPenpotJob(t)
	mock, state, secretFile := j.mock, j.state, j.secretFile
	run, published, restarts, write, called := j.run, j.published, j.restarts, j.write, penpotCalled
	const robot, password = penpotRobotEmail, penpotRobotPassword
	check := func(step string, problems []string) {
		for _, p := range problems {
			t.Errorf("%s: %s", step, p)
		}
	}
	// A console whose manifest predates the Secret: restarting it would load nothing.
	j.console("plane-api-token", "strapi-api-token")

	// 1. A fresh cluster: register the robot, mint, create the team, publish.
	//    The console's manifest does not read the Secret yet, so it is not
	//    restarted: the rollout that adds the reference loads it.
	if err := run(password); err != nil {
		t.Fatalf("fresh run failed: %v", err)
	}
	calls, problems := mock.take()
	check("fresh", problems)
	// login-with-password twice: the first answers wrong-credentials (no such
	// profile yet), the second signs the freshly registered robot in.
	for c, want := range map[string]int{"login-with-password": 2, "prepare-register-profile": 1, "register-profile": 1, "create-access-token": 1, "create-team": 1} {
		if called(calls, c) != want {
			t.Errorf("fresh: %s called %d times, want %d (calls %v)", c, called(calls, c), want, calls)
		}
	}
	mock.mu.Lock()
	expirations := mock.expirations
	mock.mu.Unlock()
	if expirations != 0 {
		t.Error("fresh: create-access-token asked for an expiry; the console's token must not expire under it")
	}
	sec := published()
	var keys []string
	for k := range sec {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if !slices.Equal(keys, penpotTokenContract) {
		t.Errorf("fresh: %s carries %v, want exactly %v", penpotTokenSecret, keys, penpotTokenContract)
	}
	if sec["PENPOT_URL"] != j.url {
		t.Errorf("fresh: PENPOT_URL = %q, want the URL the Job reached Penpot at (%q)", sec["PENPOT_URL"], j.url)
	}
	mock.mu.Lock()
	first := mock.tokens[sec["PENPOT_TOKEN"]]
	var team *penpotMockTeam
	for _, tm := range mock.teams {
		if tm.ID == sec["PENPOT_TEAM_ID"] {
			team = tm
		}
	}
	mock.mu.Unlock()
	if first == nil || first.Email != robot {
		t.Fatalf("fresh: PENPOT_TOKEN is not a token Penpot issued to %s", robot)
	}
	if team == nil || team.Default || team.Name != "Adhar" || !slices.Contains(team.Members, robot) {
		t.Errorf("fresh: PENPOT_TEAM_ID %q is not the robot's \"Adhar\" team (%+v)", sec["PENPOT_TEAM_ID"], team)
	}
	if restarts() != 0 {
		t.Errorf("fresh: the console was restarted %d times though its manifest does not load %s", restarts(), penpotTokenSecret)
	}
	firstBytes, _ := os.ReadFile(secretFile)

	// 2. The console as install.yaml ships it reads the Secret, but its running
	//    pod started before the Secret existed: restart it, once, and touch
	//    nothing else.
	j.serveConsole(penpotConsoleDeployment(t))
	for _, step := range []string{"console loads it", "steady state"} {
		if err := run(password); err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		calls, problems = mock.take()
		check(step, problems)
		if !slices.Equal(calls, []string{"get-profile", "get-teams"}) {
			t.Errorf("%s: calls %v, want only the readiness probe and the token check", step, calls)
		}
		if b, _ := os.ReadFile(secretFile); string(b) != string(firstBytes) {
			t.Errorf("%s: the Secret was rewritten while its token still works", step)
		}
		if restarts() != 1 {
			t.Errorf("%s: %d console restarts in total, want exactly 1 (the pod that predates the Secret)", step, restarts())
		}
	}
	// Right after a rollout the replaced pod is still Running while it drains;
	// it is leaving, and must not trigger a second restart.
	draining := fmt.Sprintf(`{"items":[`+
		`{"metadata":{"name":"adhar-console-old","deletionTimestamp":"2026-01-01T00:10:00Z"},"status":{"phase":"Running","startTime":"2026-01-01T00:00:00Z"}},`+
		`{"metadata":{"name":"adhar-console-new"},"status":{"phase":"Running","startTime":%q}}]}`,
		time.Now().UTC().Add(time.Second).Format(time.RFC3339))
	write(filepath.Join(state, "pods.json"), draining, 0o644)
	if err := run(password); err != nil {
		t.Fatalf("draining pod: %v", err)
	}
	mock.take()
	if restarts() != 1 {
		t.Errorf("draining pod: the console was restarted again because its replaced pod was still draining (%d restarts)", restarts())
	}
	// The decision rests on the pod list. One that FAILS is an outage: the
	// Job fails and is retried, it never reads as "no pod" and restarts. One
	// that answers with no running pod needs no restart: a pod that has not
	// started yet reads the Secret when it does.
	write(filepath.Join(state, "pods.fail"), "", 0o644)
	if err := run(password); err == nil {
		t.Error("pod list fails: the Job succeeded without knowing which console pods run; it must fail and be retried")
	}
	if err := os.Remove(filepath.Join(state, "pods.fail")); err != nil {
		t.Fatal(err)
	}
	mock.take()
	if restarts() != 1 {
		t.Errorf("pod list fails: the console was restarted on a pod list that never answered (%d restarts)", restarts())
	}
	write(filepath.Join(state, "pods.json"), `{"items":[]}`, 0o644)
	if err := run(password); err != nil {
		t.Fatalf("no running pod: %v", err)
	}
	mock.take()
	if restarts() != 1 {
		t.Errorf("no running pod: the console was restarted though no pod of it had started (%d restarts)", restarts())
	}
	write(filepath.Join(state, "pods.json"), draining, 0o644)

	// 3. Penpot errors on the token check: not a reason to replace the token.
	mock.mu.Lock()
	mock.failTeams = true
	mock.mu.Unlock()
	if err := run(password); err == nil {
		t.Error("penpot 500: the Job succeeded; it must fail (and be retried) rather than decide anything")
	}
	mock.mu.Lock()
	mock.failTeams = false
	mock.mu.Unlock()
	calls, _ = mock.take()
	if called(calls, "login-with-password")+called(calls, "create-access-token") != 0 {
		t.Errorf("penpot 500: the Job replaced a token over an error that is not about the token (calls %v)", calls)
	}
	if b, _ := os.ReadFile(secretFile); string(b) != string(firstBytes) {
		t.Error("penpot 500: the Secret was rewritten")
	}

	// 4. The console is not deployed (another profile): publish, never restart.
	if err := os.Remove(j.consoleFile); err != nil {
		t.Fatal(err)
	}
	if err := run(password); err != nil {
		t.Fatalf("no console: %v", err)
	}
	mock.take()
	if restarts() != 1 {
		t.Error("no console: a restart was attempted")
	}
	j.serveConsole(penpotConsoleDeployment(t))

	// 5. Penpot refuses the token (PENPOT_SECRET_KEY replaced): sign in
	//    (no second registration), mint, keep the team, revoke the old one,
	//    restart the console once.
	mock.mu.Lock()
	first.Refused = true
	mock.mu.Unlock()
	time.Sleep(1100 * time.Millisecond) // the Secret's change time must move past the console's last start
	if err := run(password); err != nil {
		t.Fatalf("rotation failed: %v", err)
	}
	calls, problems = mock.take()
	check("rotation", problems)
	if called(calls, "prepare-register-profile") != 0 || called(calls, "login-with-password") != 1 || called(calls, "create-access-token") != 1 {
		t.Errorf("rotation: calls %v, want one sign-in and one new token, no registration", calls)
	}
	rotated := published()
	mock.mu.Lock()
	live, superseded := mock.tokens[rotated["PENPOT_TOKEN"]] != nil, mock.tokens[first.Value] != nil
	mock.mu.Unlock()
	if rotated["PENPOT_TOKEN"] == sec["PENPOT_TOKEN"] || !live {
		t.Error("rotation: PENPOT_TOKEN was not replaced by a live token")
	}
	if rotated["PENPOT_TEAM_ID"] != sec["PENPOT_TEAM_ID"] {
		t.Errorf("rotation: the team moved from %s to %s; the console's links point into the old one", sec["PENPOT_TEAM_ID"], rotated["PENPOT_TEAM_ID"])
	}
	if superseded {
		t.Error("rotation: the superseded token was not revoked")
	}
	if restarts() != 2 {
		t.Errorf("rotation: %d console restarts in total, want 2", restarts())
	}

	// 6. The robot's password Secret was regenerated after it registered: no
	//    way to sign in. Say how to recover, touch nothing — and do NOT fail:
	//    a failed PostSync hook fails the sync, which the ApplicationSet
	//    replays forever on the same revision, so no later penpot push lands.
	mock.mu.Lock()
	mock.tokens[rotated["PENPOT_TOKEN"]].Refused = true
	mock.mu.Unlock()
	before, _ := os.ReadFile(secretFile)
	if err := run("A-Different-Password-0123456789x"); err != nil {
		t.Errorf("lockout: the Job failed (%v); a problem only a person can fix must not fail the PostSync hook", err)
	}
	calls, _ = mock.take()
	if called(calls, "create-access-token") != 0 || called(calls, "register-profile") != 0 {
		t.Errorf("lockout: calls %v; nothing may be minted or registered", calls)
	}
	if after, _ := os.ReadFile(secretFile); string(after) != string(before) || restarts() != 2 {
		t.Error("lockout: the Secret or the console was touched")
	}
	if said := j.said(); !strings.Contains(said, "ACTION REQUIRED") || !strings.Contains(said, "UPDATE profile SET email") {
		t.Error("lockout: the log does not say, loudly, how to recover")
	}

	// Nothing secret is ever printed.
	all := strings.Join(j.printed, "\n")
	for _, secret := range []string{password, sec["PENPOT_TOKEN"], rotated["PENPOT_TOKEN"]} {
		if strings.Contains(all, secret) {
			t.Error("run.sh printed a password or a token")
		}
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	for v := range mock.sessions {
		if strings.Contains(all, v) {
			t.Error("run.sh printed a session token")
		}
	}
}

// A problem no retry can fix must not fail the PostSync hook: a failed hook
// fails the sync, the ApplicationSet retries a failed sync forever
// (retry.limit -1) by replaying the SAME revision, and so the penpot push that
// fixes the problem — and every one after it — never deploys. The Job says
// what to do ("ACTION REQUIRED") and ends successfully, publishing nothing.
// An outage is different: Penpot will answer again, so the Job fails and its
// restarts retry.
func TestPenpotConsoleTokenJobLeavesWhatNeedsAPersonToAPerson(t *testing.T) {
	for _, tc := range []struct {
		name   string
		penpot func(*penpotMock)
		outage bool   // the Job must fail, to be retried
		says   string // what the log must point at
	}{
		{"registration refused", func(m *penpotMock) { m.refuseRegistration = "registration-disabled" }, false, "enable-registration"},
		{"registered, but cannot sign in", func(m *penpotMock) { m.unverified = true }, false, "disable-email-verification"},
		{"sign-in refused", func(m *penpotMock) { m.refuseLogin = "profile-blocked" }, false, "login-with-password answered http 400 (profile-blocked)"},
		{"registration outage", func(m *penpotMock) { m.outage = "prepare-register-profile" }, true, "prepare-register-profile answered http 503"},
		{"sign-in outage", func(m *penpotMock) { m.outage = "login-with-password" }, true, "login-with-password answered http 503"},
		// The wait for the API: a 4xx through all of it is a URL that is not
		// Penpot's; a 5xx is Penpot not up yet.
		{"the URL is not Penpot's API", func(m *penpotMock) { m.notPenpot = true }, false, "answered http 404 (object-not-found)"},
		{"Penpot never comes up", func(m *penpotMock) { m.outage = "get-profile" }, true, "answered http 503 (unavailable)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := newPenpotJob(t)
			j.console(penpotTokenSecret)
			tc.penpot(j.mock)
			err := j.run(penpotRobotPassword)
			switch {
			case tc.outage && err == nil:
				t.Error("the Job succeeded through an outage; it must fail so its restarts retry")
			case !tc.outage && err != nil:
				t.Errorf("the Job failed (%v) on a problem no retry can fix: the failed hook would wedge every later penpot sync", err)
			case !tc.outage && !strings.Contains(j.said(), "ACTION REQUIRED"):
				t.Error("the log does not say, loudly, that a person has to act")
			}
			if !strings.Contains(j.said(), tc.says) {
				t.Errorf("the log does not mention %q", tc.says)
			}
			calls, problems := j.mock.take()
			for _, p := range problems {
				t.Error(p)
			}
			if n := penpotCalled(calls, "create-access-token"); n != 0 {
				t.Errorf("a token was minted (%d calls) though the robot never signed in", n)
			}
			if _, err := os.Stat(j.secretFile); err == nil {
				t.Errorf("Secret %s was published", penpotTokenSecret)
			}
			if j.restarts() != 0 {
				t.Error("the console was restarted")
			}
		})
	}
}

func penpotSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
