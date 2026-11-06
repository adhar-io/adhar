package adharplatform

// Coder reads its Gitea external-auth provider, Secret coder-gitea-oauth,
// through envFrom, once, when its pod starts: gitea-external-auth.yaml writes
// the Secret and oidc-reload.yaml restarts Coder when the running pod predates
// it. Two defects lined up so that Coder ran on credentials Gitea no longer
// knew:
//
//   - The writer decides "already registered?" from the Secret it GETs with
//     curl, and the API server pretty-prints JSON for a curl User-Agent
//     (`"key": "value"`). Its sed wanted `"key":"value"`, found no client id,
//     and so on EVERY sync deleted the Gitea application, registered a new
//     one and replaced the Secret with a PUT.
//   - A PUT keeps the creationTimestamp, and that was all the hook compared
//     the pod's start with, so the replaced Secret always looked older than
//     the running pod: Coder was never restarted onto the new credentials and
//     every "Login with Gitea" from a workspace (so every git push) failed
//     against the deleted application.
//
// The writer now reads either spelling and writes only when it registers an
// application; the hook takes the Secret's LAST write, which the API server
// records in managedFields. Both halves run here as they ship: the writer
// against a curl stand-in that answers the Secret GET the way the API server
// answers curl, the hook against a kubectl stand-in that answers every
// `-o jsonpath=` with what kubectl prints for it (k8s.io/client-go/util/jsonpath
// with missing keys allowed, as kubectl does). Then the two are joined: the
// Secret the writer PUTs is the one the hook reads and the next sync's writer
// finds.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/util/jsonpath"
)

const (
	coderReloadManifest = "application/coder/manifests/oidc-reload.yaml"
	coderGiteaManifest  = "application/coder/manifests/gitea-external-auth.yaml"
	coderGiteaAppName   = "Coder Cloud Dev Environments"
)

// coderGiteaWriterStandIn replaces curl, sleep and the ServiceAccount token
// read for gitea-external-auth.yaml's run.sh. $STUB_STATE holds what the
// servers answer: secret.code and secret.json the GET of the Secret, apps.json
// the admin's Gitea OAuth2 applications, post.code the POST that creates the
// Secret (409 when it exists). Every request is appended to calls; the Secret
// bodies POSTed and PUT are kept in post.json and put.json.
const coderGiteaWriterStandIn = `
cat() {
  case "$1" in
    */serviceaccount/token) printf 'sa-token' ;;
    *) command cat "$@" ;;
  esac
}
sleep() { :; }
curl() {
  __m=GET; __d=; __w=; __o=; __u=; __f=
  while [ $# -gt 0 ]; do
    case "$1" in
      -X) __m=$2; shift 2 ;;
      -d) __d=$2; shift 2 ;;
      -w) __w=$2; shift 2 ;;
      -o) __o=$2; shift 2 ;;
      -H|--max-time|--cacert) shift 2 ;;
      -sf|-fs|-f) __f=1; shift ;;
      -s) shift ;;
      http://*|https://*) __u=$1; shift ;;
      *) echo "stub curl: unsupported argument $1" >&2; return 2 ;;
    esac
  done
  echo "$__m $__u" >>"$STUB_STATE/calls"
  case "$__m $__u" in
    "GET "*/api/v1/version) __c=200; __b='{"version":"1.24.6"}' ;;
    "GET "*/api/v1/namespaces/adhar-system/secrets/coder-gitea-oauth)
      __c=$(command cat "$STUB_STATE/secret.code"); __b=$(command cat "$STUB_STATE/secret.json") ;;
    "GET "*/api/v1/user/applications/oauth2) __c=200; __b=$(command cat "$STUB_STATE/apps.json") ;;
    "PATCH "*/api/v1/user/applications/oauth2/*) __c=200; __b='{}' ;;
    "DELETE "*/api/v1/user/applications/oauth2/*) __c=204; __b= ;;
    "POST "*/api/v1/user/applications/oauth2)
      __c=201; __b='{"id":8,"name":"Coder Cloud Dev Environments","client_id":"new-client-id","client_secret":"new-client-secret","confidential_client":true}' ;;
    "POST "*/api/v1/namespaces/adhar-system/secrets)
      printf '%s' "$__d" >"$STUB_STATE/post.json"; __c=$(command cat "$STUB_STATE/post.code"); __b=$__d ;;
    "PUT "*/api/v1/namespaces/adhar-system/secrets/coder-gitea-oauth)
      printf '%s' "$__d" >"$STUB_STATE/put.json"; __c=200; __b=$__d ;;
    *) echo "stub curl: unsupported request $__m $__u" >&2; return 2 ;;
  esac
  if [ -n "$__f" ] && [ "$__c" -ge 400 ]; then return 22; fi
  if [ -n "$__o" ]; then printf '%s' "$__b" >"$STUB_STATE/out"; else printf '%s' "$__b"; fi
  if [ -n "$__w" ]; then printf '%s' "$__c"; fi
  return 0
}
`

// coderReloadStandIn replaces kubectl and sleep for oidc-reload.yaml's script.
// $STUB_STATE/jsonpath/<n> holds each jsonpath the script asks for, and
// $STUB_STATE/<object>/<n> what kubectl prints for jsonpath n on that object
// (<object> is secret.<name> or pods). An object with no directory does not
// exist; <object>.fail makes every read of it fail like an unreachable API
// server. Each rollout restart appends its Deployment to restarts.
const coderReloadStandIn = `
sleep() { :; }
kubectl() {
  printf '%s\n' "$*" >>"$STUB_STATE/calls"
  local out="" ignore="" obj="" f="" jp=""
  local -a pos=()
  while [ $# -gt 0 ]; do
    case "$1" in
      -n|-l|-c) shift 2 ;;
      -o) out=$2; shift 2 ;;
      --ignore-not-found) ignore=1; shift ;;
      --field-selector=*|--timeout=*) shift ;;
      -*) echo "stub kubectl: unsupported flag $1" >&2; return 2 ;;
      *) pos+=("$1"); shift ;;
    esac
  done
  case "${pos[*]}" in
    "rollout restart deployment "*) echo "${pos[3]}" >>"$STUB_STATE/restarts"; echo "deployment.apps/${pos[3]} restarted"; return 0 ;;
    "rollout status deployment "*) echo "deployment \"${pos[3]}\" successfully rolled out"; return 0 ;;
    "get secret "*) obj="secret.${pos[2]}" ;;
    "get pods") obj=pods ;;
    *) echo "stub kubectl: unsupported: ${pos[*]}" >&2; return 2 ;;
  esac
  if [ -e "$STUB_STATE/$obj.fail" ]; then
    echo "Unable to connect to the server: net/http: request canceled (Client.Timeout exceeded while awaiting headers)" >&2
    return 1
  fi
  if [ ! -d "$STUB_STATE/$obj" ]; then
    if [ -n "$ignore" ]; then return 0; fi
    echo "Error from server (NotFound): $obj not found" >&2
    return 1
  fi
  case "$out" in
    jsonpath=*) jp=${out#jsonpath=} ;;
    *) echo "stub kubectl: unsupported output '$out'" >&2; return 2 ;;
  esac
  local expr=""
  for f in "$STUB_STATE"/jsonpath/*; do
    IFS= read -r expr <"$f" || true
    if [ "$expr" = "$jp" ] && [ -e "$STUB_STATE/$obj/${f##*/}" ]; then
      command cat "$STUB_STATE/$obj/${f##*/}"
      return 0
    fi
  done
  echo "stub kubectl: no answer for $obj -o jsonpath=$jp" >&2
  return 2
}
`

// distinctShells returns the shells named that exist here, one per binary.
func distinctShells(names ...string) []string {
	var out []string
	seen := map[string]bool{}
	for _, name := range names {
		p, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		if real, err := filepath.EvalSymlinks(p); err == nil && !seen[real] {
			seen[real] = true
			out = append(out, p)
		}
	}
	return out
}

func writeStubFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func runStubbedScript(t *testing.T, shell, path string, env []string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, path)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// kubectlPrints is what `kubectl get … -o jsonpath=<expr>` prints for obj:
// kubectl runs this package over the object with missing keys allowed
// (--allow-missing-template-keys defaults to true).
func kubectlPrints(expr string, obj any) (string, error) {
	jp := jsonpath.New("kubectl").AllowMissingKeys(true)
	if err := jp.Parse(expr); err != nil {
		return "", err
	}
	var b strings.Builder
	if err := jp.Execute(&b, obj); err != nil {
		return "", err
	}
	return b.String(), nil
}

// apiServerJSON is how the API server answers a GET from curl: pretty-printed
// (k8s.io/apiserver negotiation.isPrettyPrint matches the curl User-Agent),
// or compact for any other client.
func apiServerJSON(t *testing.T, obj any, pretty bool) string {
	t.Helper()
	var b []byte
	var err error
	if pretty {
		b, err = json.MarshalIndent(obj, "", "  ")
	} else {
		b, err = json.Marshal(obj)
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

type coderGiteaWriterRun struct {
	output    string
	err       error
	calls     []string
	post, put map[string]any // the Secret bodies sent, nil when none was
}

// runCoderGiteaWriter runs the writer's run.sh under shell. secret is what the
// API server holds (nil: no Secret yet), apps the Gitea applications.
func runCoderGiteaWriter(t *testing.T, shell string, secret map[string]any, pretty bool, apps []map[string]any) coderGiteaWriterRun {
	t.Helper()
	script := configMapData(t, coderGiteaManifest, "coder-gitea-oauth-app")["run.sh"]
	if !strings.Contains(script, "CODER_EXTERNAL_AUTH_0_CLIENT_ID") {
		t.Fatal("ConfigMap coder-gitea-oauth-app no longer carries the registration script; point this guard at where it went")
	}
	env, _ := containerEnv(t, readStackFile(t, coderGiteaManifest), "Job", "register")
	if apps == nil {
		apps = []map[string]any{}
	}
	appsJSON, _ := json.Marshal(apps)
	files := map[string]string{"calls": "", "apps.json": string(appsJSON), "script": coderGiteaWriterStandIn + script}
	if secret == nil {
		files["secret.code"], files["post.code"] = "404", "201"
		files["secret.json"] = apiServerJSON(t, map[string]any{"kind": "Status", "apiVersion": "v1", "metadata": map[string]any{},
			"status": "Failure", "message": `secrets "coder-gitea-oauth" not found`, "reason": "NotFound",
			"details": map[string]any{"name": "coder-gitea-oauth", "kind": "secrets"}, "code": 404}, pretty)
	} else {
		files["secret.code"], files["post.code"] = "200", "409"
		files["secret.json"] = apiServerJSON(t, secret, pretty)
	}
	dir := t.TempDir()
	writeStubFiles(t, dir, files)
	vars := []string{"STUB_STATE=" + dir, "GITEA_TOKEN=gitea-admin-token"}
	for k, v := range env {
		if v != "" {
			vars = append(vars, k+"="+v)
		}
	}
	run := coderGiteaWriterRun{}
	run.output, run.err = runStubbedScript(t, shell, filepath.Join(dir, "script"), vars)
	if b, err := os.ReadFile(filepath.Join(dir, "calls")); err == nil {
		run.calls = strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	for name, into := range map[string]*map[string]any{"post.json": &run.post, "put.json": &run.put} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		if err := json.Unmarshal(b, into); err != nil {
			t.Fatalf("the writer sent a Secret body that is not JSON (%v):\n%s", err, b)
		}
	}
	return run
}

type coderReloadRun struct {
	restarts int
	output   string
	err      error
}

// runCoderReload runs the reload hook's script under shell against objects
// ("secret.<name>" or "pods" -> the object kubectl would read); the objects
// named in failing cannot be read at all.
func runCoderReload(t *testing.T, shell string, objects map[string]any, failing ...string) coderReloadRun {
	t.Helper()
	job := stackObject(t, coderReloadManifest, "Job", "coder-oidc-reload")
	container := sliceOfMaps(nestedMap(job, "spec", "template", "spec")["containers"])[0]
	if got := strings.Join(toStrings(container["command"]), " "); got != "/bin/bash -c" {
		t.Fatalf("the coder-oidc-reload Job runs %q, not `/bin/bash -c <script>`; point this guard at how it runs now", got)
	}
	script := toString(toSlice(container["args"])[0])
	exprs := map[string]bool{}
	for _, m := range regexp.MustCompile(`jsonpath='([^']*)'`).FindAllStringSubmatch(script, -1) {
		exprs[m[1]] = true
	}
	files := map[string]string{"calls": "", "script": coderReloadStandIn + script}
	for name := range objects {
		files[name+"/.exists"] = ""
	}
	i := 0
	for expr := range exprs {
		i++
		files["jsonpath/"+strconv.Itoa(i)] = expr
		for name, obj := range objects {
			if out, err := kubectlPrints(expr, obj); err == nil {
				files[name+"/"+strconv.Itoa(i)] = out
			}
		}
	}
	for _, name := range failing {
		files[name+".fail"] = ""
	}
	dir := t.TempDir()
	writeStubFiles(t, dir, files)
	run := coderReloadRun{}
	run.output, run.err = runStubbedScript(t, shell, filepath.Join(dir, "script"), []string{"STUB_STATE=" + dir})
	if b, err := os.ReadFile(filepath.Join(dir, "restarts")); err == nil {
		run.restarts = len(strings.Fields(string(b)))
	}
	return run
}

// coderSecret is a Secret as the API server returns it; writes are the times
// in its managedFields (the API server's record of every write).
func coderSecret(name, created, clientID string, writes ...string) map[string]any {
	meta := map[string]any{"name": name, "namespace": "adhar-system", "creationTimestamp": created}
	if len(writes) > 0 {
		var fields []any
		for i, at := range writes {
			fields = append(fields, map[string]any{"manager": []string{"curl", "kubectl-label", "kubectl-annotate"}[i%3],
				"operation": "Update", "apiVersion": "v1", "time": at, "fieldsType": "FieldsV1",
				"fieldsV1": map[string]any{"f:data": map[string]any{".": map[string]any{}}}})
		}
		meta["managedFields"] = fields
	}
	data := map[string]any{"clientSecret": "czNjcjN0", "CODER_EXTERNAL_AUTH_0_CLIENT_SECRET": "Z2l0ZWE="}
	if clientID != "" {
		data["CODER_EXTERNAL_AUTH_0_CLIENT_ID"] = base64.StdEncoding.EncodeToString([]byte(clientID))
	}
	return map[string]any{"kind": "Secret", "apiVersion": "v1", "metadata": meta, "type": "Opaque", "data": data}
}

// coderPods is the PodList `kubectl get pods` reads; each pod is "name=startTime"
// ("name=" for a pod that has not started).
func coderPods(pods ...string) map[string]any {
	var items []any
	for _, p := range pods {
		name, started, _ := strings.Cut(p, "=")
		status := map[string]any{"phase": "Pending"}
		if started != "" {
			status = map[string]any{"phase": "Running", "startTime": started}
		}
		items = append(items, map[string]any{"metadata": map[string]any{"name": name,
			"labels": map[string]any{"app.kubernetes.io/instance": "coder", "app.kubernetes.io/name": "coder"}}, "status": status})
	}
	return map[string]any{"kind": "List", "apiVersion": "v1", "items": items}
}

func decodedClientID(t *testing.T, body map[string]any) string {
	t.Helper()
	data, _ := body["data"].(map[string]any)
	b, err := base64.StdEncoding.DecodeString(toString(data["CODER_EXTERNAL_AUTH_0_CLIENT_ID"]))
	if err != nil {
		t.Fatalf("the Secret's client id is not base64: %v", err)
	}
	return string(b)
}

func TestCoderRestartsOntoAReRegisteredGiteaApp(t *testing.T) {
	writers := distinctShells("sh", "dash")
	readers := distinctShells("bash", "/bin/bash")
	if len(writers) == 0 || len(readers) == 0 {
		t.Skip("no POSIX shell or no bash here")
	}

	// The writer runs the ConfigMap's script; nothing else counts.
	job := stackObject(t, coderGiteaManifest, "Job", "coder-gitea-oauth-app")
	pod := nestedMap(job, "spec", "template", "spec")
	mounted := false
	for _, v := range sliceOfMaps(pod["volumes"]) {
		mounted = mounted || toString(nestedMap(v, "configMap")["name"]) == "coder-gitea-oauth-app"
	}
	if command := strings.Join(toStrings(sliceOfMaps(pod["containers"])[0]["command"]), " "); !mounted || !strings.HasSuffix(command, "/run.sh") {
		t.Fatalf("the coder-gitea-oauth-app Job no longer runs ConfigMap coder-gitea-oauth-app's run.sh (command %q); point this guard at its script", command)
	}

	registered := []map[string]any{{"id": 7, "name": coderGiteaAppName, "client_id": "client-1", "confidential_client": true,
		"redirect_uris": []string{"https://coder.adhar.localtest.me:8443/external-auth/gitea/callback"}}}
	t.Run("the writer registers and writes only when it must", func(t *testing.T) {
		for _, shell := range writers {
			for _, pretty := range []bool{true, false} {
				form := map[bool]string{true: "pretty JSON, as the API server answers curl", false: "compact JSON"}[pretty]
				t.Run(filepath.Base(shell)+"/"+form, func(t *testing.T) {
					t.Parallel()
					check := func(name string, run coderGiteaWriterRun) {
						t.Helper()
						if run.err != nil || strings.Contains(run.output, "stub curl: unsupported") {
							t.Fatalf("%s: the writer exited with %v:\n%s", name, run.err, run.output)
						}
					}

					first := runCoderGiteaWriter(t, shell, nil, pretty, nil)
					check("no Secret yet", first)
					if first.post == nil || first.put != nil || decodedClientID(t, first.post) != "new-client-id" {
						t.Errorf("no Secret yet: want one POST of the new application's credentials, got POST %v PUT %v:\n%s", first.post != nil, first.put != nil, first.output)
					}

					gone := runCoderGiteaWriter(t, shell, coderSecret("coder-gitea-oauth", "2026-10-27T21:00:00Z", "old-client-id"), pretty, nil)
					check("application gone", gone)
					if gone.put == nil || decodedClientID(t, gone.put) != "new-client-id" {
						t.Errorf("the Secret names an application Gitea no longer has: want the Secret replaced (PUT) with the new application's credentials, got PUT %v:\n%s", gone.put != nil, gone.output)
					}

					steady := runCoderGiteaWriter(t, shell, coderSecret("coder-gitea-oauth", "2026-10-27T21:00:00Z", "client-1"), pretty, registered)
					check("already registered", steady)
					for _, call := range steady.calls {
						writesSecret := strings.Contains(call, "/secrets") && !strings.HasPrefix(call, "GET ")
						if writesSecret || strings.HasPrefix(call, "POST ") || strings.HasPrefix(call, "DELETE ") {
							t.Errorf("already registered (Secret and application agree): the writer made %q — it registers and writes only when the application is gone, or every sync replaces everyone's Gitea credentials and restarts Coder:\n%s", call, steady.output)
						}
					}
					if !strings.Contains(strings.Join(steady.calls, "\n"), "PATCH ") {
						t.Errorf("already registered: the writer no longer keeps the application consent-free (no PATCH):\n%s", steady.output)
					}
				})
			}
		}
	})

	const (
		oidcCreated  = "2026-10-27T18:35:46Z"
		giteaCreated = "2026-10-27T21:00:00Z"
		reRegistered = "2026-10-28T01:00:00Z"
		beforeAll    = "2026-10-27T18:00:00Z"
		beforeGitea  = "2026-10-27T20:00:00Z"
		between      = "2026-10-27T23:28:57Z" // after the Gitea Secret was created, before it was replaced
		afterAll     = "2026-10-28T02:00:00Z"
	)
	oidc := coderSecret("coder-oidc", oidcCreated, "")
	gitea := func(writes ...string) map[string]any {
		return coderSecret("coder-gitea-oauth", giteaCreated, "client-1", writes...)
	}
	for _, tc := range []struct {
		name     string
		gitea    map[string]any // nil: no Secret
		pods     map[string]any
		failing  []string
		restarts int
	}{
		{"replaced in place (a PUT keeps creationTimestamp) after the pod started", gitea(giteaCreated, reRegistered), coderPods("coder-6c8d-abcde=" + between), nil, 1},
		{"the latest write wins, whoever made it", gitea(giteaCreated, giteaCreated, reRegistered), coderPods("coder-6c8d-abcde=" + between), nil, 1},
		{"written once, before the pod started", gitea(giteaCreated), coderPods("coder-6c8d-abcde=" + between), nil, 0},
		{"replaced, and the pod started after it", gitea(giteaCreated, reRegistered), coderPods("coder-6c8d-abcde=" + afterAll), nil, 0},
		{"no managedFields: the creation counts", gitea(), coderPods("coder-6c8d-abcde=" + between), nil, 0},
		{"no managedFields, the pod started before the Gitea Secret", gitea(), coderPods("coder-6c8d-abcde=" + beforeGitea), nil, 1},
		{"no Gitea Secret: the OIDC Secret alone decides", nil, coderPods("coder-6c8d-abcde=" + beforeGitea), nil, 0},
		{"no Gitea Secret, the pod started before the OIDC Secret", nil, coderPods("coder-6c8d-abcde=" + beforeAll), nil, 1},
		{"a Gitea Secret that cannot be read ends the hook", gitea(giteaCreated, reRegistered), coderPods("coder-6c8d-abcde=" + beforeAll), []string{"secret.coder-gitea-oauth"}, 0},
		{"a pod list that fails ends the hook", gitea(giteaCreated, reRegistered), coderPods("coder-6c8d-abcde=" + beforeAll), []string{"pods"}, 0},
		{"a pod list that answers empty restarts", gitea(giteaCreated), coderPods(), nil, 1},
		{"mid-rollout: the newest pod decides", gitea(giteaCreated, reRegistered), coderPods("coder-5d-aaaaa="+afterAll, "coder-7f-zzzzz="+between, "coder-7f-yyyyy="), nil, 0},
		{"mid-rollout, every pod older than the replacement", gitea(giteaCreated, reRegistered), coderPods("coder-5d-aaaaa="+between, "coder-7f-zzzzz=", "coder-7f-yyyyy="+beforeAll), nil, 1},
	} {
		for _, shell := range readers {
			t.Run("the hook: "+tc.name+"/"+strings.ReplaceAll(strings.TrimPrefix(shell, "/"), "/", "_"), func(t *testing.T) {
				t.Parallel()
				objects := map[string]any{"secret.coder-oidc": oidc, "pods": tc.pods}
				if tc.gitea != nil {
					objects["secret.coder-gitea-oauth"] = tc.gitea
				}
				run := runCoderReload(t, shell, objects, tc.failing...)
				if run.err != nil || strings.Contains(run.output, "stub kubectl:") {
					t.Fatalf("the hook exited with %v — a failed hook fails the coder sync:\n%s", run.err, run.output)
				}
				if run.restarts != tc.restarts {
					t.Errorf("Coder restarted %d time(s), want %d:\n%s", run.restarts, tc.restarts, run.output)
				}
			})
		}
	}

	// Joined: the application is gone, the writer replaces the Secret, the
	// hook restarts Coder onto it, and the next sync changes nothing.
	t.Run("end to end", func(t *testing.T) {
		now := time.Now().UTC()
		stamp := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
		replaced := runCoderGiteaWriter(t, writers[0], coderSecret("coder-gitea-oauth", stamp(-2*time.Hour), "old-client-id", stamp(-2*time.Hour)), true, nil)
		if replaced.err != nil || replaced.put == nil {
			t.Fatalf("the writer did not replace the Secret of a deleted application (err %v):\n%s", replaced.err, replaced.output)
		}
		// What the API server holds after that PUT: the body, the original
		// creation, and the write recorded in managedFields.
		held := replaced.put
		meta, _ := held["metadata"].(map[string]any)
		meta["creationTimestamp"] = stamp(-2 * time.Hour)
		meta["managedFields"] = []any{map[string]any{"manager": "curl", "operation": "Update", "apiVersion": "v1", "time": stamp(0)}}
		objects := map[string]any{"secret.coder-oidc": coderSecret("coder-oidc", stamp(-3*time.Hour), ""), "secret.coder-gitea-oauth": held,
			"pods": coderPods("coder-6c8d-abcde=" + stamp(-time.Hour))}
		for _, shell := range readers {
			if run := runCoderReload(t, shell, objects); run.err != nil || run.restarts != 1 {
				t.Errorf("%s: after the Secret was replaced, Coder (started an hour before) restarted %d time(s), want 1 (err %v):\n%s", shell, run.restarts, run.err, run.output)
			}
		}
		// The next sync: the writer finds its own Secret (pretty-printed, as
		// curl gets it) naming an application Gitea has, and writes nothing;
		// Coder, restarted after the write, stays up.
		apps := []map[string]any{{"id": 8, "name": coderGiteaAppName, "client_id": decodedClientID(t, held), "confidential_client": true}}
		for _, shell := range writers {
			next := runCoderGiteaWriter(t, shell, held, true, apps)
			if next.err != nil || next.post != nil || next.put != nil {
				t.Errorf("%s: the sync after the replacement wrote the Secret again (POST %v, PUT %v, err %v) — Coder would restart on every sync:\n%s", shell, next.post != nil, next.put != nil, next.err, next.output)
			}
		}
		objects["pods"] = coderPods("coder-6c8d-fghij=" + stamp(time.Minute))
		for _, shell := range readers {
			if run := runCoderReload(t, shell, objects); run.err != nil || run.restarts != 0 {
				t.Errorf("%s: Coder started after the replacement was restarted %d time(s), want 0 (err %v):\n%s", shell, run.restarts, run.err, run.output)
			}
		}
	})
}
