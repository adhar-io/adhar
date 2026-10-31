package adharplatform

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/intstr"
)

// "No healthy upstream" on the console (Azure, 2026-10-28) is what the Gateway's
// Envoy answers when the `console` Service has no ready endpoint, and the
// console runs ONE replica. Two defects made that routine rather than rare:
//
//   - The Deployment rolled out with `maxSurge: 0, maxUnavailable: 1`, so every
//     rollout stopped the only pod before its successor existed and the Service
//     stayed empty until the new pod passed /readyz (~15s quiet, up to ~3 min on
//     a busy cluster).
//   - The PostSync hooks that restart the console when a Secret it reads at
//     start-up appears compared timestamps with busybox `date -d`, which rejects
//     RFC 3339. The test errored, fell through, and the console was restarted on
//     EVERY sync of adhar-console, plane and strapi. One adhar-console sync
//     produced two outage windows 16s apart.
//
// These guards pin both halves, and TestTheConsoleRolloutGuardsCatchWhatShipped
// proves each guard rejects the configuration that actually shipped. The fix
// (v0.1.63) left three more holes, closed and pinned here too: headlamp and
// jupyterhub still compared with `date -d`; every one of those hooks read a
// pod list that FAILED as "no pod" and restarted; and the console imported
// penpot-api-token whole (envFrom), so the Job that writes it could set any of
// the console's variables. The same failed-answer-as-"missing" shape was in
// the hooks that restart Grafana, Coder and the AI runtime, in two forms: the
// pod list, and a `kubectl exec … test -n` whose exit status cannot tell a
// missing variable from a refused exec — Grafana's Role never granted
// pods/exec, so its hook restarted Grafana on every sync. Every reload hook is
// covered now, with no exceptions list.

const consoleManifest = "core/adhar-console/manifests/install.yaml"

// The server's own drain deadline after SIGTERM (SHUTDOWN_DEADLINE_MS default in
// the console's server.ts) — the grace period must outlast preStop + this.
const consoleDrainDeadlineSeconds = 25

// TestTheConsoleKeepsAnEndpointThroughARollout: a rollout must start the new
// pod before it stops the old one, and the old one must keep accepting until
// the Gateway has stopped routing to it.
func TestTheConsoleKeepsAnEndpointThroughARollout(t *testing.T) {
	dep := stackObject(t, consoleManifest, "Deployment", "adhar-console")
	for _, p := range rolloutGapProblems(dep) {
		t.Errorf("Deployment/adhar-console: %s — the console Service has no endpoint for the whole start of the new pod and the Gateway answers \"no healthy upstream\"", p)
	}

	podSpec := nestedMap(dep, "spec", "template", "spec")
	c := consoleContainer(t, podSpec)
	sleep, ok := nestedMap(c, "lifecycle", "preStop", "sleep")["seconds"].(float64)
	if !ok || sleep < 1 {
		t.Fatalf("container adhar-console has no preStop sleep: the server closes its listener on SIGTERM before Envoy has dropped the endpoint, so requests in that gap are refused (\"upstream connect error … connection refused\")")
	}
	grace := 30.0 // the Kubernetes default when unset
	if g, ok := podSpec["terminationGracePeriodSeconds"].(float64); ok {
		grace = g
	}
	drain := float64(consoleDrainDeadlineSeconds)
	for _, e := range sliceOfMaps(c["env"]) {
		if e["name"] == "SHUTDOWN_DEADLINE_MS" {
			ms, err := strconv.Atoi(fmt.Sprint(e["value"]))
			if err != nil {
				t.Fatalf("SHUTDOWN_DEADLINE_MS is not a number: %v", e["value"])
			}
			drain = float64(ms) / 1000
		}
	}
	if grace <= sleep+drain {
		t.Errorf("terminationGracePeriodSeconds %.0f does not outlast preStop %.0fs + the server's %.0fs drain deadline: the grace countdown includes the preStop, so the kubelet SIGKILLs a drain that was about to finish", grace, sleep, drain)
	}
}

// TestConsoleRestartHooksCompareTimestampsWithoutDate: every hook that decides
// "is the running pod older than the Secret?" must compare in a way the
// alpine/k8s image can evaluate, against the NEWEST pod. It covers EVERY
// reload/restart hook in the stack, not only the console's: headlamp and
// jupyterhub kept `date -d` after the console's hooks were fixed, and
// restarted their app on every sync.
func TestConsoleRestartHooksCompareTimestampsWithoutDate(t *testing.T) {
	found := map[string]bool{}
	walkRestartHooks(t, func(rel, body string) {
		if timestampAssignment.MatchString(body) {
			found[rel] = true
		}
		for _, p := range timestampComparisonProblems(body) {
			t.Errorf("%s %s", rel, p)
		}
	})
	for _, want := range []string{
		"core/adhar-console/manifests/oidc-reload.yaml",
		"application/plane/manifests/console-reload.yaml",
		"application/strapi/manifests/console-token.yaml",
		"observability/headlamp/manifests/oidc-reload.yaml",
		"data/jupyterhub/manifests/oidc-reload.yaml",
		"observability/kube-prometheus/manifests/grafana-oidc-reload.yaml",
		"application/coder/manifests/oidc-reload.yaml",
	} {
		if !found[want] {
			t.Errorf("%s no longer restarts by comparing timestamps, so this guard stopped covering it — find where that logic went and point the guard there", want)
		}
	}
}

// TestRestartHooksNeverRestartOnAFailedPodList: a hook restarts when no pod
// answers "I started after the Secret", so it has to know the pod list
// ANSWERED. `POD_TS=$(kubectl get pods … 2>/dev/null | sort | tail -n 1 ||
// true)` (v0.1.63) turned an API timeout into an empty list — "no pod is
// running" — and restarted the console. The list is read on its own, and a
// failure ends the hook; only a list that succeeded empty may restart. Every
// reload/restart hook in the stack, with no exceptions: the Grafana, Coder and
// AI-runtime hooks sat on an allow-list here until they were fixed.
func TestRestartHooksNeverRestartOnAFailedPodList(t *testing.T) {
	checked := map[string]bool{}
	walkRestartHooks(t, func(rel, body string) {
		if podList.MatchString(body) {
			checked[rel] = true
		}
		for _, p := range podListProblems(body) {
			t.Errorf("%s %s", rel, p)
		}
	})
	for _, want := range []string{
		"core/adhar-console/manifests/oidc-reload.yaml",
		"observability/kube-prometheus/manifests/grafana-oidc-reload.yaml",
		"application/coder/manifests/oidc-reload.yaml",
		"ai/adhar-ai/manifests/oidc-reload.yaml",
	} {
		if !checked[want] {
			t.Errorf("%s no longer lists pods in a restart hook, so this guard stopped covering it — find where that logic went and point the guard there", want)
		}
	}
}

// TestRestartHooksNeverReadAFailedExecAsMissing: a hook that asks the running
// pod whether it has its env must tell "the variable is empty" from "the
// question was never answered". `if kubectl exec "$POD" -- sh -c 'test -n
// "$X"'; then …nothing to do…; fi` cannot: a refused exec (no pods/exec), a
// timeout or a shell-less image all exit non-zero exactly like a failed
// `test`, and the hook restarted. Grafana's Role never granted pods/exec, so
// its hook restarted Grafana on EVERY sync (2026-10-30). The answer is printed
// and captured, and an exec that fails ends the hook.
func TestRestartHooksNeverReadAFailedExecAsMissing(t *testing.T) {
	asks := 0
	walkRestartHooks(t, func(rel, body string) {
		if execCommand.MatchString(withoutComments(body)) {
			asks++
		}
		for _, p := range execAnswerProblems(body) {
			t.Errorf("%s %s", rel, p)
		}
	})
	if asks == 0 {
		t.Error("no restart hook asks a pod for its env any more, so this guard covers nothing — the AI runtime's oidc-reload.yaml did; find where that logic went")
	}
}

// TestTheConsoleEnvFromsOnlyItsOwnSecrets: envFrom imports EVERY key of a
// Secret, and a later source overrides an earlier one. The console imported
// penpot-api-token that way (v0.1.63), and the penpot-console-token Job's
// ServiceAccount may patch that Secret — so it could replace
// AUTH_COOKIE_SECRET, AUTH_CLIENT_SECRET or DATABASE_URL from
// console-env-vars. What another package publishes for the console is read
// key by key (env valueFrom.secretKeyRef); envFrom is for the console
// package's own Secrets.
func TestTheConsoleEnvFromsOnlyItsOwnSecrets(t *testing.T) {
	own := consolePackageEnvSources(t)
	for _, want := range []string{"Secret/console-env-vars", "Secret/gitea-credentials"} {
		if !own[want] {
			t.Fatalf("the console package no longer produces %s, so this guard cannot tell the console's own Secrets apart: point consolePackageEnvSources at where it went", want)
		}
	}
	dep := stackObject(t, consoleManifest, "Deployment", "adhar-console")
	for _, p := range consoleEnvFromProblems(dep, own) {
		t.Errorf("Deployment/adhar-console: %s", p)
	}
}

// TestTheConsoleRolloutGuardsCatchWhatShipped feeds the guards the exact
// configuration that caused the outages, so a guard that silently stops
// matching fails here rather than passing everything.
func TestTheConsoleRolloutGuardsCatchWhatShipped(t *testing.T) {
	deployment := func(strategy map[string]any) map[string]any {
		spec := map[string]any{"replicas": float64(1)}
		if strategy != nil {
			spec["strategy"] = strategy
		}
		return map[string]any{"spec": spec}
	}
	rolling := func(surge, unavailable any) map[string]any {
		ru := map[string]any{}
		if surge != nil {
			ru["maxSurge"] = surge
		}
		if unavailable != nil {
			ru["maxUnavailable"] = unavailable
		}
		return map[string]any{"type": "RollingUpdate", "rollingUpdate": ru}
	}
	for _, tc := range []struct {
		name    string
		dep     map[string]any
		wantGap bool
	}{
		{"as shipped: maxSurge 0, maxUnavailable 1", deployment(rolling(float64(0), float64(1))), true},
		{"Recreate", deployment(map[string]any{"type": "Recreate"}), true},
		// Both fenceposts resolve to zero, and the controller then allows one
		// unavailable pod ("surge might not work due to quota").
		{"0% surge rounds the 25% unavailable down to the 1-unavailable fencepost", deployment(rolling("0%", "25%")), true},
		{"the fix: maxSurge 1, maxUnavailable 0", deployment(rolling(float64(1), float64(0))), false},
		{"Kubernetes defaults (25%/25%) round to surge 1, unavailable 0", deployment(nil), false},
		{"only maxSurge given: maxUnavailable defaults to 25%, rounds down to 0", deployment(rolling(float64(1), nil)), false},
	} {
		if got := len(rolloutGapProblems(tc.dep)) > 0; got != tc.wantGap {
			t.Errorf("%s: rollout gap reported = %v, want %v (%v)", tc.name, got, tc.wantGap, rolloutGapProblems(tc.dep))
		}
	}

	shippedHook := `SECRET_TS=$(kubectl -n "$NS" get secret "$SECRET" -o jsonpath='{.metadata.creationTimestamp}')
POD_TS=$(kubectl -n "$NS" get pods -l app=console \
           -o jsonpath='{.items[0].status.startTime}' 2>/dev/null || true)
if [ -n "$POD_TS" ] && [ "$(date -d "$POD_TS" +%s)" -gt "$(date -d "$SECRET_TS" +%s)" ]; then`
	if got := timestampComparisonProblems(shippedHook); len(got) != 2 {
		t.Errorf("the shipped hook comparison should report the date -d parse AND the items[0] pick, got %d: %v", len(got), got)
	}
	// Any `date` reading a *_TS variable, however it is spelled.
	for _, call := range []string{
		`[ "$(date +%s -d "${SECRET_TS}")" -lt 1 ]`,
		`POD_S=$(date --date="$POD_TS" +%s)`,
		`date -u -D '%Y-%m-%dT%H:%M:%SZ' -d "$STARTED_TS" +%s`,
	} {
		if got := timestampComparisonProblems(call); len(got) != 1 {
			t.Errorf("the date guard misses %q (got %v)", call, got)
		}
	}
	// The v0.1.63 comparison: right about dates, wrong about a failed list.
	fixedComparison := `POD_TS=$(kubectl -n "$NS" get pods -l app=console \
           -o jsonpath='{range .items[*]}{.status.startTime}{"\n"}{end}' 2>/dev/null \
           | sort | tail -n 1 || true)
if [ -n "$POD_TS" ] && [ "${POD_TS//[!0-9]/}" -gt "${SECRET_TS//[!0-9]/}" ]; then`
	if got := timestampComparisonProblems(fixedComparison); len(got) != 0 {
		t.Errorf("the fixed comparison should pass the date guard, got %v", got)
	}
	for name, hook := range map[string]string{
		"v0.1.63, swallowed after the pipe": fixedComparison,
		"exec-style hooks, swallowed": `POD=$(kubectl -n "$NS" get pods -l app.kubernetes.io/name=coder \
        -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)`,
		"piped with no `|| true`, so pipefail decides": `POD_TS=$(kubectl get pods -l app=console -o jsonpath='{.items[*].status.startTime}' | sort | tail -n 1)`,
		"`|| :` swallows as well":                      `kubectl -n "$NS" get pods -l app=console -o json >"$work/pods.json" || :`,
		"`|| X=\"\"` carries on just the same":         `STARTED=$(kubectl -n "$NS" get pods -l app=console -o jsonpath='{.items[*].status.startTime}') || STARTED=""`,
		"`|| echo` inside the substitution":            `STARTED=$(kubectl -n "$NS" get pods -l app=console -o jsonpath='{.items[*].status.startTime}' || echo "")`,
	} {
		if got := podListProblems(hook); len(got) != 1 {
			t.Errorf("the pod-list guard should report %s once, got %v", name, got)
		}
	}
	for name, hook := range map[string]string{
		"read on its own, a failure ends the hook": `if ! STARTED=$(kubectl -n "$NS" get pods -l app=console \
                 -o jsonpath='{range .items[*]}{.status.startTime}{"\n"}{end}'); then
  echo "cannot list the console pods; leaving the console as-is (retries next sync)"
  exit 0
fi
POD_TS=$(printf '%s\n' "$STARTED" | sort | tail -n 1)`,
		"penpot: to a file, a failure fails the Job": `$KUBECTL -n "$NS" get pods -l app=console --field-selector=status.phase=Running -o json >"$work/pods.json" \
      || fail "cannot list the $CONSOLE pods"`,
		"a pipe inside the jsonpath is not a shell pipe": `if ! P=$(kubectl get pods -o jsonpath='{.items[?(@.status.phase=="Running")].metadata.name}' -o go-template='{{.x | len}}'); then exit 0; fi`,
		"`|| { …; exit 0; }` ends the hook":              `kubectl -n "$NS" get pods -l app=console -o json >"$work/pods.json" || { echo "cannot list the console pods"; exit 0; }`,
	} {
		if got := podListProblems(hook); len(got) != 0 {
			t.Errorf("the pod-list guard should accept %s, got %v", name, got)
		}
	}
	// The exec-style hooks as they shipped, and the ways to keep the mistake.
	for name, hook := range map[string]string{
		"grafana, as shipped (its Role never granted pods/exec)": `if [ -n "$POD" ]; then
  if kubectl -n "$NS" exec "$POD" -c grafana -- \
       sh -c 'test -n "$GF_AUTH_GENERIC_OAUTH_CLIENT_SECRET"' 2>/dev/null; then
    echo "Grafana already has the OAuth client secret; nothing to do."`,
		"the AI runtime, as shipped": `if kubectl -n "$NS" exec "$POD" -- \
     sh -c 'test -n "$ADHAR_AI_OIDC_CLIENT_SECRET"' 2>/dev/null; then`,
		"`if !` on the exit status: a missing variable never restarts": `if ! kubectl -n "$NS" exec "$POD" -- sh -c 'test -n "$X"'; then exit 0; fi`,
		"captured, but the failure swallowed":                          `HELD=$(kubectl -n "$NS" exec "$POD" -- sh -c 'echo held' || true)`,
		"captured in an && chain":                                      `[ -n "$POD" ] && HELD=$(kubectl -n "$NS" exec "$POD" -- sh -c 'echo held')`,
		"through $KUBECTL":                                             `if $KUBECTL -n "$NS" exec "$POD" -- test -n "$X"; then exit 0; fi`,
	} {
		if got := execAnswerProblems(hook); len(got) != 1 {
			t.Errorf("the exec guard should report %s once, got %v", name, got)
		}
	}
	for name, hook := range map[string]string{
		"the fix: the printed answer, a failed exec ends the hook": `if ! HELD=$(kubectl -n "$NS" exec "$POD" -- \
              sh -c 'if [ -n "${ADHAR_AI_OIDC_CLIENT_SECRET:-}" ]; then echo held; else echo missing; fi'); then
  echo "cannot ask $POD for its env"; exit 0
fi`,
		"a command quoted in a message for a person": `needs_person "free the address: kubectl -n $NS exec penpot-postgresql-0 -- psql -c \"UPDATE profile …\""`,
		"a comment":                   `# kubectl -n "$NS" exec "$POD" -- sh -c 'test -n "$X"'`,
		"RBAC naming the subresource": `    resources: ["pods/exec"]`,
	} {
		if got := execAnswerProblems(hook); len(got) != 0 {
			t.Errorf("the exec guard should accept %s, got %v", name, got)
		}
	}

	for body, want := range map[string]bool{
		`kubectl -n "$NS" rollout restart deployment "$DEPLOY"`:   true,
		`kubectl -n "$NS" rollout restart "deployment/${DEPLOY}"`: true,
		`kubectl -n "$NS" delete pod -l app=minio`:                true,
		`kubectl -n "$NS" rollout status deployment "$DEPLOY"`:    false,
	} {
		if got := restartHook.MatchString(body); got != want {
			t.Errorf("restartHook.MatchString(%q) = %v, want %v", body, got, want)
		}
	}

	// The console Deployment as v0.1.63 shipped it: penpot-api-token imported
	// whole, after the console's own Secrets.
	own := map[string]bool{"Secret/console-env-vars": true, "Secret/gitea-credentials": true}
	console := func(envFrom ...map[string]any) map[string]any {
		refs := []any{}
		for _, r := range envFrom {
			refs = append(refs, r)
		}
		return map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []any{map[string]any{"name": "adhar-console", "envFrom": refs}}}}}}
	}
	secret := func(name string) map[string]any {
		return map[string]any{"secretRef": map[string]any{"name": name, "optional": true}}
	}
	if got := consoleEnvFromProblems(console(secret("console-env-vars"), secret("gitea-credentials"), secret("penpot-api-token")), own); len(got) != 1 {
		t.Errorf("the envFrom guard should report penpot-api-token, got %v", got)
	}
	if got := consoleEnvFromProblems(console(map[string]any{"configMapRef": map[string]any{"name": "someone-elses"}}), own); len(got) != 1 {
		t.Errorf("the envFrom guard should report another package's ConfigMap, got %v", got)
	}
	if got := consoleEnvFromProblems(console(secret("console-env-vars"), secret("gitea-credentials")), own); len(got) != 0 {
		t.Errorf("the envFrom guard should accept the console's own Secrets, got %v", got)
	}
}

// rolloutGapProblems resolves a Deployment's strategy the way the Deployment
// controller does — the 25% defaults, surge rounded up, unavailable rounded
// down, and the "both zero → one unavailable" fencepost (ResolveFenceposts) —
// and reports a strategy that can leave the Service with no ready pod.
func rolloutGapProblems(dep map[string]any) []string {
	spec, _ := dep["spec"].(map[string]any)
	replicas := 1
	if r, ok := spec["replicas"].(float64); ok {
		replicas = int(r)
	}
	strategy, _ := spec["strategy"].(map[string]any)
	if t, _ := strategy["type"].(string); t == "Recreate" {
		return []string{"strategy Recreate stops every pod before it starts any new one"}
	}
	ru, _ := strategy["rollingUpdate"].(map[string]any)
	scaled := func(v any, roundUp bool) (int, error) {
		val := intstr.FromString("25%")
		switch x := v.(type) {
		case nil:
		case float64:
			val = intstr.FromInt32(int32(x))
		case string:
			val = intstr.Parse(x)
		default:
			return 0, fmt.Errorf("unexpected %T %v", v, v)
		}
		return intstr.GetScaledValueFromIntOrPercent(&val, replicas, roundUp)
	}
	surge, err := scaled(ru["maxSurge"], true)
	if err != nil {
		return []string{"maxSurge: " + err.Error()}
	}
	unavailable, err := scaled(ru["maxUnavailable"], false)
	if err != nil {
		return []string{"maxUnavailable: " + err.Error()}
	}
	if surge == 0 && unavailable == 0 {
		unavailable = 1
	}
	if unavailable >= replicas {
		return []string{fmt.Sprintf("maxSurge/maxUnavailable resolve to %d/%d for %d replica(s), so a rollout stops every serving pod before a new one is Ready", surge, unavailable, replicas)}
	}
	return nil
}

var (
	// A hook that restarts something: `rollout restart`, or deleting pods.
	restartHook = regexp.MustCompile(`\brollout\s+restart\b|\bdelete\s+pods?\b`)
	// POD_TS=…, SECRET_TS=… — the hook compares timestamps.
	timestampAssignment = regexp.MustCompile(`\b\w+_TS=`)
	// Any `date` call reading a *_TS variable on the same command.
	dateReadsATimestamp = regexp.MustCompile(`\bdate\b[^\n;|]*\$\{?\w*_TS`)
	// A pod list, and what turns its failure into an empty answer: an `||`
	// that carries on (`|| true`, `|| :`, `|| X=""`, `|| echo`) instead of
	// ending the hook (exit, fail, return, or a { …; exit 0; } group).
	podList      = regexp.MustCompile(`\bget\s+pods?\b`)
	orElse       = regexp.MustCompile(`\|\|\s*`)
	endsTheHook  = regexp.MustCompile(`^(exit|fail|return)\b|^\{.*\b(exit|fail|return)\b`)
	shellPipe    = regexp.MustCompile(`(^|[^|])\|([^|]|$)`)
	singleQuoted = regexp.MustCompile(`'[^']*'`)
	// A `kubectl exec` that starts a command — at the start of a line, inside
	// $( ), after an operator or if/then/else — and not one quoted inside a
	// message telling a person what to run.
	execCommand = regexp.MustCompile(`(?m)(?:^\s*|\$\(\s*|[;&|]\s*|\bif\s+(?:!\s*)?|\bthen\s+|\belse\s+)(?:kubectl|\$\{?KUBECTL\}?)\s(?:[^\n;|&]*\s)?exec\s`)
	// if ! X=$(kubectl … exec …); then — the pod's answer is captured, and an
	// exec that fails takes the then-branch instead of reading as an answer.
	execAnswerCaptured = regexp.MustCompile(`^\s*if\s+!\s*\w+=\$\(\s*(?:kubectl|\$\{?KUBECTL\}?)\s`)
)

// execAnswerProblems lists the `kubectl exec` calls in a hook whose exit
// status is taken as the pod's answer: a refused exec (no pods/exec), an API
// timeout or an image with no shell exits non-zero exactly like the `test` it
// runs, so the hook takes "no answer" for "missing" and restarts. Accepted is
// `if ! X=$(kubectl exec …); then <end the hook>; fi`, deciding on what the pod
// printed. Commands continued over `\` lines count as one; comments and quoted
// remote scripts are ignored.
func execAnswerProblems(body string) []string {
	var out []string
	for _, cmd := range strings.Split(strings.ReplaceAll(withoutComments(body), "\\\n", " "), "\n") {
		if !execCommand.MatchString(cmd) {
			continue
		}
		if execAnswerCaptured.MatchString(cmd) && !swallowsAFailure(singleQuoted.ReplaceAllString(cmd, "''")) {
			continue
		}
		out = append(out, fmt.Sprintf("reads the exit status of `kubectl exec` as the pod's answer (`%s`): a refused or timed-out exec, or an image with no shell, exits non-zero exactly like the test it runs, so the hook takes \"no answer\" for \"missing\" and restarts — capture what the pod prints (`if ! X=$(kubectl exec …); then exit 0; fi`) and decide on that", strings.Join(strings.Fields(cmd), " ")))
	}
	return out
}

// walkRestartHooks calls fn for every reload/restart hook in the stack, with
// its path relative to packages/.
func walkRestartHooks(t *testing.T, fn func(rel, body string)) {
	t.Helper()
	root := filepath.Join(stackRoot(t), "packages")
	walkStackFiles(t, root, func(path, body string) {
		if !restartHook.MatchString(body) {
			return
		}
		rel, _ := filepath.Rel(root, path)
		fn(filepath.ToSlash(rel), body)
	})
}

// timestampComparisonProblems lists the ways a hook's "is the running pod newer
// than the Secret?" test can misfire.
func timestampComparisonProblems(body string) []string {
	var out []string
	if dateReadsATimestamp.MatchString(body) {
		out = append(out, "parses a Kubernetes timestamp with `date`: busybox date (alpine/k8s) rejects RFC 3339 (\"invalid date\"), the test errors and the hook restarts on every sync — compare the stamps' digits instead")
	}
	if strings.Contains(body, ".items[0].status.startTime") {
		out = append(out, "reads .items[0].status.startTime: pods list in NAME order, so mid-rollout that can be the pod being replaced rather than the newest")
	}
	return out
}

// podListProblems lists the pod lists in a hook whose failure is turned into
// an answer: an `||` after the list that does not end the hook (`|| true`,
// `|| :`, `|| STARTED=""` …), or a pipe straight out of it — the decision
// after either cannot tell "no pod" from "no answer". Commands continued over
// `\` lines count as one; quoted jsonpath is ignored.
func podListProblems(body string) []string {
	var out []string
	for _, cmd := range strings.Split(strings.ReplaceAll(body, "\\\n", " "), "\n") {
		at := podList.FindStringIndex(cmd)
		if at == nil || strings.HasPrefix(strings.TrimSpace(cmd), "#") {
			continue
		}
		rest := singleQuoted.ReplaceAllString(cmd[at[0]:], "''")
		show := strings.Join(strings.Fields(cmd), " ")
		switch {
		case swallowsAFailure(rest):
			out = append(out, fmt.Sprintf("swallows a failed pod list (`%s`): an API timeout reads as \"no pod\" and the hook restarts — read the list on its own and end the hook when it fails", show))
		case shellPipe.MatchString(rest):
			out = append(out, fmt.Sprintf("pipes the pod list straight into another command (`%s`): its failure is then the pipeline's to report, or not — read the list on its own and end the hook when it fails", show))
		}
	}
	return out
}

// swallowsAFailure: an `||` in the rest of a pod-list command that carries on
// rather than ending the hook.
func swallowsAFailure(rest string) bool {
	for _, at := range orElse.FindAllStringIndex(rest, -1) {
		if !endsTheHook.MatchString(rest[at[1]:]) {
			return true
		}
	}
	return false
}

// consolePackageEnvSources: the Secrets and ConfigMaps the console package
// itself produces ("Secret/<name>", "ConfigMap/<name>"), from ExternalSecret
// targets and plain objects in core/adhar-console/manifests.
func consolePackageEnvSources(t *testing.T) map[string]bool {
	t.Helper()
	dir := filepath.Join(stackRoot(t), "packages", "core", "adhar-console", "manifests")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	own := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		for _, d := range yamlDocs(t, filepath.Join(dir, e.Name())) {
			switch d["kind"] {
			case "ExternalSecret":
				name := nestedMap(d, "spec", "target")["name"]
				if name == nil {
					name = metaName(d)
				}
				own[fmt.Sprintf("Secret/%v", name)] = true
			case "Secret", "ConfigMap":
				own[fmt.Sprintf("%v/%v", d["kind"], metaName(d))] = true
			}
		}
	}
	return own
}

// consoleEnvFromProblems lists the envFrom sources of a console Deployment
// that the console package does not produce itself.
func consoleEnvFromProblems(dep map[string]any, own map[string]bool) []string {
	pod := nestedMap(dep, "spec", "template", "spec")
	var out []string
	for _, field := range []string{"initContainers", "containers"} {
		for _, c := range sliceOfMaps(pod[field]) {
			for _, from := range sliceOfMaps(c["envFrom"]) {
				for _, src := range []struct{ kind, ref, keyRef string }{
					{"Secret", "secretRef", "secretKeyRef"},
					{"ConfigMap", "configMapRef", "configMapKeyRef"},
				} {
					ref, ok := from[src.ref].(map[string]any)
					if !ok || own[fmt.Sprintf("%s/%v", src.kind, ref["name"])] {
						continue
					}
					out = append(out, fmt.Sprintf("container %v imports %s %v whole (envFrom), and the console package does not produce it: whatever can write it could set any of the console's variables — AUTH_COOKIE_SECRET, AUTH_CLIENT_SECRET, DATABASE_URL. Read the keys the console needs with env valueFrom.%s (optional)", c["name"], src.kind, ref["name"], src.keyRef))
				}
			}
		}
	}
	return out
}

func sliceOfMaps(v any) []map[string]any {
	items, _ := v.([]any)
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func consoleContainer(t *testing.T, podSpec map[string]any) map[string]any {
	t.Helper()
	for _, c := range sliceOfMaps(podSpec["containers"]) {
		if c["name"] == "adhar-console" {
			return c
		}
	}
	t.Fatal("Deployment/adhar-console has no container named adhar-console")
	return nil
}
