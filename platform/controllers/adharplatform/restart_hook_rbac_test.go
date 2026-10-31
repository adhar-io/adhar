package adharplatform

// What a reload/restart hook may do, and to whom. The hooks that restart an
// app when a late Secret arrives are small shell scripts with a Role beside
// them, and both halves have gone wrong in ways a sync never reports:
//
//   - `kubectl rollout status` without `watch` on the Deployment logs "Failed
//     to watch … unknown" and only notices the rollout on its backoff relists
//     (grafana, coder and the AI runtime, 2026-10-30).
//   - Grafana's hook chose its pod by `app.kubernetes.io/name=grafana`, which
//     also selects OnCall's Grafana — whose start time then answered for the
//     platform's Grafana.
//   - plane-instance-setup could get, create and patch every Secret in
//     adhar-system to write one.
//
// The guards below read the stack; TestHookAuthorityOnARealAPIServer
// (hook_authority_apiserver_test.go) checks the same Roles against a real API
// server.

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// manifestDocs splits a manifest body into documents, skipping any that do not
// parse (Go-template files).
func manifestDocs(body string) []map[string]any {
	var out []map[string]any
	for _, chunk := range strings.Split(body, "\n---") {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(chunk), &m); err != nil || m == nil {
			continue
		}
		out = append(out, m)
	}
	return out
}

// hookJob is a Job (or CronJob) and the script text it runs: its inline
// command and args, plus every ConfigMap in the same file it mounts.
type hookJob struct {
	serviceAccount string
	script         string
	env            map[string]string
}

func hookJobs(docs []map[string]any) []hookJob {
	configMaps := map[string]string{}
	for _, d := range docs {
		if d["kind"] != "ConfigMap" {
			continue
		}
		var data []string
		for _, v := range nestedMap(d, "data") {
			data = append(data, toString(v))
		}
		configMaps[toString(metaName(d))] = strings.Join(data, "\n")
	}
	var out []hookJob
	for _, d := range docs {
		var pod map[string]any
		switch d["kind"] {
		case "Job":
			pod = nestedMap(d, "spec", "template", "spec")
		case "CronJob":
			pod = nestedMap(d, "spec", "jobTemplate", "spec", "template", "spec")
		default:
			continue
		}
		job := hookJob{serviceAccount: toString(pod["serviceAccountName"]), env: map[string]string{}}
		var script []string
		for _, c := range append(sliceOfMaps(pod["initContainers"]), sliceOfMaps(pod["containers"])...) {
			for _, field := range []string{"command", "args"} {
				for _, a := range toSlice(c[field]) {
					script = append(script, toString(a))
				}
			}
			for _, e := range sliceOfMaps(c["env"]) {
				if v, ok := e["value"].(string); ok {
					job.env[toString(e["name"])] = v
				}
			}
		}
		for _, v := range sliceOfMaps(pod["volumes"]) {
			if cm := nestedMap(v, "configMap"); cm != nil {
				script = append(script, configMaps[toString(cm["name"])])
			}
		}
		job.script = withoutComments(strings.Join(script, "\n"))
		out = append(out, job)
	}
	return out
}

// deploymentGrant is what the Roles bound to one ServiceAccount (in the same
// file) allow on apps/deployments: the verbs, and the names when every rule
// that grants them names its Deployments (nil = any Deployment).
type deploymentGrant struct {
	verbs map[string]bool
	names map[string]bool
}

func deploymentGrantFor(docs []map[string]any, serviceAccount string) (deploymentGrant, bool) {
	roles := map[string]bool{}
	for _, d := range docs {
		if d["kind"] != "RoleBinding" {
			continue
		}
		for _, s := range sliceOfMaps(d["subjects"]) {
			if s["kind"] == "ServiceAccount" && s["name"] == serviceAccount {
				roles[toString(nestedMap(d, "roleRef")["name"])] = true
			}
		}
	}
	grant := deploymentGrant{verbs: map[string]bool{}, names: map[string]bool{}}
	found, unnamed := false, false
	for _, d := range docs {
		if d["kind"] != "Role" || !roles[toString(metaName(d))] {
			continue
		}
		found = true
		for _, rule := range sliceOfMaps(d["rules"]) {
			if !listHas(toSlice(rule["apiGroups"]), "apps") || !listHas(toSlice(rule["resources"]), "deployments") {
				continue
			}
			for _, v := range toSlice(rule["verbs"]) {
				grant.verbs[toString(v)] = true
			}
			names := toSlice(rule["resourceNames"])
			if len(names) == 0 {
				unnamed = true
			}
			for _, n := range names {
				grant.names[toString(n)] = true
			}
		}
	}
	if unnamed {
		grant.names = nil
	}
	return grant, found
}

func listHas(list []any, want string) bool {
	for _, v := range list {
		if toString(v) == want {
			return true
		}
	}
	return false
}

var (
	// rollout status deployment "$DEPLOY" | deployment/${DEPLOY} | deploy/x
	rolloutStatusTarget = regexp.MustCompile(`rollout\s+status\s+"?(?:deployments?|deploy)(?:/|\s+"?)\$?\{?([\w.-]+)\}?"?`)
	rolloutRestart      = regexp.MustCompile(`rollout\s+restart\s+"?(?:deployments?|deploy)(?:/|\s+"?)\$?\{?([\w.-]+)\}?"?`)
)

// resolveName turns the target of a kubectl call into a Deployment name: a
// literal stays as it is; a variable is looked up in the container env, then
// in the script (`VAR=value`, `VAR=${OTHER:-value}`).
func resolveName(job hookJob, token string) (string, bool) {
	if !regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`).MatchString(token) {
		return token, true
	}
	if v, ok := job.env[token]; ok && v != "" {
		return v, true
	}
	m := regexp.MustCompile(`(?:^|[\s;])` + token + `=("?)([^\s;"]+)("?)`).FindStringSubmatch(job.script)
	if m == nil {
		return "", false
	}
	if d := regexp.MustCompile(`^\$\{\w+:-([\w.-]+)\}$`).FindStringSubmatch(m[2]); d != nil {
		return d[1], true
	}
	if strings.HasPrefix(m[2], "$") {
		return "", false
	}
	return m[2], true
}

// TestReloadHooksMayWatchTheRolloutTheyWaitFor: `kubectl rollout status` GETs
// the Deployment, then LISTs and WATCHes it by name. A Role without `watch`
// does not fail the hook — the wait logs "Failed to watch
// *unstructured.Unstructured: unknown" and sees the rollout only on its backoff
// relists — so nothing reports it. Every restart hook that waits on a rollout
// must hold get, list and watch (and patch, to restart it) on that Deployment.
func TestReloadHooksMayWatchTheRolloutTheyWaitFor(t *testing.T) {
	checked := map[string]bool{}
	walkRestartHooks(t, func(rel, body string) {
		docs := manifestDocs(body)
		for _, job := range hookJobs(docs) {
			targets := rolloutStatusTarget.FindAllStringSubmatch(job.script, -1)
			if len(targets) == 0 {
				continue
			}
			checked[rel] = true
			grant, ok := deploymentGrantFor(docs, job.serviceAccount)
			if !ok {
				t.Errorf("%s: the Job waits on a rollout as ServiceAccount %q, but no Role bound to it is in the same file to check", rel, job.serviceAccount)
				continue
			}
			need := []string{"get", "list", "watch"}
			if rolloutRestart.MatchString(job.script) {
				need = append(need, "patch")
			}
			for _, v := range need {
				if !grant.verbs[v] {
					t.Errorf("%s: ServiceAccount %s runs `kubectl rollout status` without %q on apps/deployments — the wait logs \"Failed to watch … unknown\" and only sees the rollout on its backoff relists", rel, job.serviceAccount, v)
				}
			}
			if grant.names == nil {
				continue
			}
			for _, m := range targets {
				name, ok := resolveName(job, m[1])
				if !ok {
					t.Errorf("%s: cannot tell which Deployment `rollout status … %s` waits on, so the guard cannot check the Role names it", rel, m[1])
					continue
				}
				if !grant.names[name] {
					t.Errorf("%s: the Role names Deployments %v but the hook waits on %s — the wait is refused", rel, sortedKeys(grant.names), name)
				}
			}
		}
	})
	for _, want := range []string{
		"core/adhar-console/manifests/oidc-reload.yaml",
		"observability/kube-prometheus/manifests/grafana-oidc-reload.yaml",
		"application/coder/manifests/oidc-reload.yaml",
		"ai/adhar-ai/manifests/oidc-reload.yaml",
		"data/rustfs/manifests/oidc.yaml",
		"application/penpot/manifests/console-token.yaml",
	} {
		if !checked[want] {
			t.Errorf("%s no longer waits on a rollout in a restart hook, so this guard stopped covering it — find where that logic went and point the guard there", want)
		}
	}
}

// podTemplate is one workload's pod labels, wherever the stack declares it.
type podTemplate struct {
	id     string // Kind/name
	file   string
	labels map[string]string
}

// stackPodTemplates collects the pod template labels of every workload in the
// stack that lands in adhar-system (an explicit other namespace is skipped).
func stackPodTemplates(t *testing.T) []podTemplate {
	t.Helper()
	root := filepath.Join(stackRoot(t), "packages")
	var out []podTemplate
	walkStackFiles(t, root, func(path, body string) {
		for _, d := range manifestDocs(body) {
			if ns := toString(nestedMap(d, "metadata")["namespace"]); ns != "" && ns != "adhar-system" {
				continue
			}
			var meta map[string]any
			switch d["kind"] {
			case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job":
				meta = nestedMap(d, "spec", "template", "metadata")
			case "CronJob":
				meta = nestedMap(d, "spec", "jobTemplate", "spec", "template", "metadata")
			case "Pod":
				meta = nestedMap(d, "metadata")
			default:
				continue
			}
			labels := map[string]string{}
			for k, v := range nestedMap(meta, "labels") {
				labels[k] = toString(v)
			}
			r, _ := filepath.Rel(root, path)
			out = append(out, podTemplate{id: fmt.Sprintf("%v/%v", d["kind"], metaName(d)), file: filepath.ToSlash(r), labels: labels})
		}
	})
	return out
}

var podSelectorFlag = regexp.MustCompile(`\b(?:get|delete)\s+pods?\b[^\n]*?\s(?:-l|--selector)(?:\s+|=)("?)([^\s"]+)`)

// equalitySelector parses `a=b,c==d`; ok is false for anything else (set-based
// terms, a variable).
func equalitySelector(s string) (map[string]string, bool) {
	out := map[string]string{}
	for _, term := range strings.Split(s, ",") {
		k, v, found := strings.Cut(strings.Replace(term, "==", "=", 1), "=")
		if !found || k == "" || strings.ContainsAny(term, "$!()") || strings.HasSuffix(k, "!") {
			return nil, false
		}
		out[k] = v
	}
	return out, true
}

func selects(selector, labels map[string]string) bool {
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}

// TestRestartHooksSelectOnlyTheirOwnPods: a reload hook decides "restart or
// not" from the pods its label selector returns (or deletes the pods it
// returns), so the selector must return ONE workload's pods — the one it
// restarts. Grafana's hook selected
// `app.kubernetes.io/name=grafana`, which OnCall's Grafana carries too: the
// newest start time could be OnCall's, and the platform's Grafana, started
// before its OAuth Secret, would never be restarted. Operator-created pods are
// not in the stack and cannot be checked here; everything the stack declares
// is.
func TestRestartHooksSelectOnlyTheirOwnPods(t *testing.T) {
	templates := stackPodTemplates(t)
	checked := map[string]bool{}
	walkRestartHooks(t, func(rel, body string) {
		for _, job := range hookJobs(manifestDocs(body)) {
			for _, cmd := range strings.Split(strings.ReplaceAll(job.script, "\\\n", " "), "\n") {
				m := podSelectorFlag.FindStringSubmatch(cmd)
				if m == nil {
					continue
				}
				sel, ok := equalitySelector(m[2])
				if !ok {
					t.Errorf("%s: cannot read the pod selector %q (only literal k=v terms), so the guard cannot check whose pods it lists — spell it out", rel, m[2])
					continue
				}
				checked[rel] = true
				matched := map[string][]string{}
				for _, tpl := range templates {
					if selects(sel, tpl.labels) {
						matched[tpl.id] = append(matched[tpl.id], tpl.file)
					}
				}
				t.Logf("%s: `%s` selects %v", rel, m[2], sortedIDs(matched))
				var target string
				if r := rolloutRestart.FindStringSubmatch(job.script); r != nil {
					if name, ok := resolveName(job, r[1]); ok {
						target = "Deployment/" + name
					}
				}
				switch {
				case target != "" && len(matched[target]) == 0:
					t.Errorf("%s: the pod selector %q does not select %s, the workload the hook restarts (it selects %v)", rel, m[2], target, sortedIDs(matched))
				case len(matched) > 1:
					t.Errorf("%s: the pod selector %q selects the pods of %d workloads %v — the hook then decides on another workload's pods; use the restarted workload's own selector", rel, m[2], len(matched), sortedIDs(matched))
				case len(matched) == 0 && target == "":
					t.Errorf("%s: the pod selector %q selects no workload the stack declares, so the guard cannot tell whose pods it lists", rel, m[2])
				}
			}
		}
	})
	for _, want := range []string{
		"core/adhar-console/manifests/oidc-reload.yaml",
		"observability/kube-prometheus/manifests/grafana-oidc-reload.yaml",
		"application/coder/manifests/oidc-reload.yaml",
		"ai/adhar-ai/manifests/oidc-reload.yaml",
		"application/posthog/manifests/clickhouse-unstick.yaml",
	} {
		if !checked[want] {
			t.Errorf("%s no longer selects pods in a restart hook, so this guard stopped covering it — find where that logic went and point the guard there", want)
		}
	}
}

func sortedIDs(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// TestPlaneInstanceSetupWritesOnlyItsOwnSecret: the setup Job reads the
// keycloak-clients Secret and writes the console's token Secret, and its Role
// granted get, create and patch on EVERY Secret in adhar-system to do it. The
// Role names exactly the two Secrets the Job's env names, and the script writes
// with server-side apply — a PATCH to the Secret's own URL, whose implied
// create is authorised by name — because a POST to the collection carries no
// name and could only be allowed on every Secret.
func TestPlaneInstanceSetupWritesOnlyItsOwnSecret(t *testing.T) {
	const rel = "application/plane/manifests/instance-setup.yaml"
	body := readStackFile(t, rel)
	env, _ := containerEnv(t, body, "Job", "setup")
	read, write := env["KEYCLOAK_CLIENTS_SECRET"], env["TOKEN_SECRET_NAME"]
	if read == "" || write == "" {
		t.Fatalf("the setup Job no longer names its Secrets in KEYCLOAK_CLIENTS_SECRET / TOKEN_SECRET_NAME (%q, %q); point this guard at where they went", read, write)
	}
	want := map[string]string{"get": read, "create": write, "patch": write}
	got := map[string][]string{}
	role := stackObject(t, rel, "Role", "plane-instance-setup")
	for _, rule := range sliceOfMaps(role["rules"]) {
		if !listHas(toSlice(rule["resources"]), "secrets") {
			continue
		}
		names := toSlice(rule["resourceNames"])
		for _, v := range toSlice(rule["verbs"]) {
			verb := toString(v)
			if len(names) == 0 {
				t.Errorf("Role plane-instance-setup grants %s on every Secret in adhar-system; name the Secret (resourceNames)", verb)
				continue
			}
			for _, n := range names {
				got[verb] = append(got[verb], toString(n))
			}
		}
	}
	for verb, names := range got {
		if w, ok := want[verb]; !ok || len(names) != 1 || names[0] != w {
			t.Errorf("Role plane-instance-setup grants %s on Secrets %v; the script needs %s only on %q", verb, names, verb, want[verb])
		}
	}
	for verb, name := range want {
		if len(got[verb]) == 0 {
			t.Errorf("Role plane-instance-setup does not grant %s on Secret %s, which the script needs", verb, name)
		}
	}

	code := withoutComments(body)
	if strings.Contains(code, `kube("POST"`) {
		t.Error(`the setup script still POSTs to the Secrets collection: a create with no name is authorised against every Secret, so the narrowed Role refuses it — write with server-side apply`)
	}
	for _, want := range []string{`application/apply-patch+yaml`, `?fieldManager=plane-instance-setup&force=true`, `kube("PATCH", f"{base}/{name}`} {
		if !strings.Contains(code, want) {
			t.Errorf("the setup script no longer writes %s with server-side apply (lost %q)", write, want)
		}
	}
}
