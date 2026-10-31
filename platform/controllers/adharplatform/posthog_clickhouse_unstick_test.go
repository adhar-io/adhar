package adharplatform

// The ClickHouse operator bundled with PostHog (Altinity 0.19.0, pinned by the
// chart) can stop reconciling the `posthog` CHI and never come back to it, and
// Argo CD then holds the whole posthog sync at wave 0. It has done so two ways:
// a FAILED Service reconcile it never retried (2026-10-09), and a reconcile the
// API server cut short, whose FAILED status write died with it, so the CHI read
// InProgress with its StatefulSet Ready (2026-10-30) — which the hook, looking
// only for FAILED, watched for twenty minutes and left alone. These guards run
// the shipped script against a kubectl stand-in whose clock moves only when the
// script sleeps.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	"k8s.io/client-go/util/jsonpath"
)

// clickhouseUnstickDocs decodes clickhouse-unstick.yaml.
func clickhouseUnstickDocs(t *testing.T) []map[string]any {
	t.Helper()
	dec := yaml.NewDecoder(strings.NewReader(readStackFile(t, "application/posthog/manifests/clickhouse-unstick.yaml")))
	var docs []map[string]any
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc != nil {
			docs = append(docs, doc)
		}
	}
	return docs
}

// clickhouseUnstickScript returns the hook Job and the script it runs.
func clickhouseUnstickScript(t *testing.T) (map[string]any, string) {
	t.Helper()
	for _, doc := range clickhouseUnstickDocs(t) {
		if doc["kind"] != "Job" {
			continue
		}
		pod := doc["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
		container := pod["containers"].([]any)[0].(map[string]any)
		return doc, toString(container["args"].([]any)[0])
	}
	t.Fatal("no Job in clickhouse-unstick.yaml")
	return nil, ""
}

// The unstick hook must run IN the CHI's wave — a PostSync hook would be gated
// on the very CHI it repairs.
func TestPostHogUnstickHookRunsAlongsideTheClickHouseInstallation(t *testing.T) {
	install := readStackFile(t, "application/posthog/manifests/install.yaml")
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
	job, _ := clickhouseUnstickScript(t)
	ann, _ := job["metadata"].(map[string]any)["annotations"].(map[string]any)
	if ann["argocd.argoproj.io/hook"] != "Sync" {
		t.Errorf("the unstick Job is a %v hook; only a Sync hook runs while the wave is still open", ann["argocd.argoproj.io/hook"])
	}
	if w, _ := strconv.Atoi(toString(ann["argocd.argoproj.io/sync-wave"])); w > chiWave {
		t.Errorf("the unstick Job runs at wave %d, after the ClickHouseInstallation's wave %d — it would wait on the thing it repairs", w, chiWave)
	}
}

// The stand-in can only show what the script would do; the Role decides
// whether the cluster lets it. A verb missing here is a 403 in the Job's log
// and an operator that is never restarted.
func TestPostHogUnstickHookMayDoWhatItsScriptDoes(t *testing.T) {
	granted := map[string]bool{}
	for _, doc := range clickhouseUnstickDocs(t) {
		if doc["kind"] != "Role" {
			continue
		}
		for _, raw := range toSlice(doc["rules"]) {
			rule, _ := raw.(map[string]any)
			for _, g := range toSlice(rule["apiGroups"]) {
				for _, r := range toSlice(rule["resources"]) {
					for _, v := range toSlice(rule["verbs"]) {
						granted[toString(g)+"/"+toString(r)+":"+toString(v)] = true
					}
				}
			}
		}
	}
	for _, need := range []string{
		"clickhouse.altinity.com/clickhouseinstallations:get", // reading the CHI's status
		"apps/statefulsets:list",                              // "every StatefulSet is Ready"
		"/pods:list", "/pods:delete",                          // kubectl delete pod -l
	} {
		if !granted[need] {
			t.Errorf("the unstick Role does not grant %s, which its script needs", need)
		}
	}
}

// The stand-in below answers with what kubectl WOULD print, so the two jsonpath
// expressions the script's decisions rest on are checked here, with the
// package kubectl itself prints through (missing keys allowed, as kubectl
// does): a CHI with no error yet, and a StatefulSet still starting, have no
// such field at all.
func TestPostHogUnstickHookReadsWhatItThinksItReads(t *testing.T) {
	_, script := clickhouseUnstickScript(t)
	for _, tc := range []struct {
		name, pattern string
		object        map[string]any
		want          string
	}{
		{
			name:    "the CHI's status",
			pattern: `get clickhouseinstallation "\$CHI" \\\n\s*-o jsonpath='([^']+)'`,
			object: map[string]any{"status": map[string]any{
				"status": "InProgress", "taskID": "68e64bfd", "action": "Create StatefulSet adhar-system/chi-posthog-posthog-0-0 - started",
			}},
			want: "InProgress|68e64bfd|Create StatefulSet adhar-system/chi-posthog-posthog-0-0 - started|",
		},
		{
			name:    "the readiness of the CHI's StatefulSets",
			pattern: `get statefulset -l "clickhouse.altinity.com/chi=\$CHI" \\\n\s*-o jsonpath='([^']+)'`,
			object: map[string]any{"items": []any{
				map[string]any{"spec": map[string]any{"replicas": 1}, "status": map[string]any{"replicas": 1, "readyReplicas": 1}},
				map[string]any{"spec": map[string]any{"replicas": 1}, "status": map[string]any{"replicas": 1}},
			}},
			want: "1/1 1/ ",
		},
	} {
		m := regexp.MustCompile(tc.pattern).FindStringSubmatch(script)
		if m == nil {
			t.Errorf("%s: the unstick script no longer reads it with a jsonpath (looked for %s)", tc.name, tc.pattern)
			continue
		}
		jp := jsonpath.New(tc.name).AllowMissingKeys(true)
		if err := jp.Parse(m[1]); err != nil {
			t.Errorf("%s: the jsonpath does not parse: %v", tc.name, err)
			continue
		}
		var out strings.Builder
		if err := jp.Execute(&out, tc.object); err != nil {
			t.Errorf("%s: executing the jsonpath: %v", tc.name, err)
			continue
		}
		if out.String() != tc.want {
			t.Errorf("%s: the jsonpath prints %q, want %q", tc.name, out.String(), tc.want)
		}
	}
}

// The kubectl stand-in, and the three other commands the script runs, as shell
// FUNCTIONS put in front of it: a function shadows a command of the same name,
// and calling one costs no exec — the script makes four calls a tick for up to
// 120 ticks. The CHI is files in $STUB_STATE (status, task, action, error; a
// file holding "@tick" reads as a new value every tick); sts holds the
// StatefulSets' "<replicas>/<ready> "; chi.fail fails every read like an
// unreachable API server; from the clock in `horizon` on, the CHI reads
// Completed, which ends a run once its outcome is decided; and every operator
// pod deletion appends the clock to restarts, then runs on-restart, if present.
const clickhouseUnstickStandIn = `
__get() { __v=; if [ -f "$STUB_STATE/$1" ]; then IFS= read -r __v <"$STUB_STATE/$1"; fi; }
__clock() { read -r __c <"$STUB_STATE/clock"; }
__field() {
  __get "$1"
  if [ "$__v" = "@tick" ]; then __clock; __v="tick-$__c"; fi
}
kubectl() {
  echo "$*" >>"$STUB_STATE/calls"
  case "$*" in
    *"get clickhouseinstallation posthog -o jsonpath={.status.status}{\"|\"}{.status.taskID}{\"|\"}{.status.action}{\"|\"}{.status.error}")
      __clock; __get horizon
      if [ -n "$__v" ] && [ "$__c" -ge "$__v" ]; then printf 'Completed|||'; return 0; fi
      if [ -f "$STUB_STATE/chi.fail" ]; then echo "The connection to the server was refused" >&2; return 1; fi
      __field status; __s=$__v; __field task; __t=$__v; __field action; __a=$__v; __field error
      printf '%s|%s|%s|%s' "$__s" "$__t" "$__a" "$__v" ;;
    *"get statefulset -l clickhouse.altinity.com/chi=posthog -o jsonpath="*)
      __get sts; printf '%s' "$__v" ;;
    *"delete pod -l app=clickhouse-operator"*)
      __clock; echo "$__c" >>"$STUB_STATE/restarts"
      if [ -f "$STUB_STATE/on-restart" ]; then . "$STUB_STATE/on-restart"; fi ;;
    *) echo "stub kubectl: unsupported: $*" >&2; return 2 ;;
  esac
}
sleep() { __clock; echo $((__c + $1)) >"$STUB_STATE/clock"; }
date() { echo 00:00:00; }
cut() { while IFS= read -r __l; do printf '%s\n' "$__l"; done; }
`

type unstickRun struct {
	restarts []int    // simulated seconds at which the operator pod was deleted
	calls    []string // every kubectl invocation
	output   string
	err      error
}

// runClickHouseUnstick runs the shipped script under shell with the CHI in
// `state` (file name -> content).
func runClickHouseUnstick(t *testing.T, shell, script string, state map[string]string) unstickRun {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{"clock": "0", "calls": "", "script": clickhouseUnstickStandIn + script}
	for name, body := range state {
		files[name] = body
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, filepath.Join(dir, "script"))
	cmd.Env = append(os.Environ(), "STUB_STATE="+dir)
	out, err := cmd.CombinedOutput()
	run := unstickRun{output: string(out), err: err}
	if b, readErr := os.ReadFile(filepath.Join(dir, "restarts")); readErr == nil {
		for _, f := range strings.Fields(string(b)) {
			n, convErr := strconv.Atoi(f)
			if convErr != nil {
				t.Fatalf("restarts log holds %q", f)
			}
			run.restarts = append(run.restarts, n)
		}
	}
	if b, readErr := os.ReadFile(filepath.Join(dir, "calls")); readErr == nil {
		run.calls = strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	return run
}

// The decision itself: restart a stuck operator, at most twice, and never a
// working one — under each distinct POSIX shell available, since the image's
// /bin/sh need not be bash.
func TestTheClickHouseUnstickHookRestartsTheOperatorOnlyWhenItIsStuck(t *testing.T) {
	_, script := clickhouseUnstickScript(t)
	var shells []string
	seen := map[string]bool{}
	for _, name := range []string{"sh", "dash"} {
		p, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		if real, err := filepath.EvalSymlinks(p); err == nil && !seen[real] {
			seen[real] = true
			shells = append(shells, p)
		}
	}
	if len(shells) == 0 {
		t.Skip("no POSIX shell available")
	}

	// The 2026-10-30 CHI, as the API-server restart left it.
	cutShort := map[string]string{
		"status": "InProgress",
		"task":   "68e64bfd-03a9-4bed-ab36-054072e5a1ea",
		"action": "Create StatefulSet adhar-system/chi-posthog-posthog-0-0 - started",
		"sts":    "1/1 ",
	}
	// The 2026-10-09 CHI: a FAILED Service reconcile, before any StatefulSet.
	failed := map[string]string{
		"status": "InProgress",
		"task":   "5f0c1d2e-0000-4000-8000-000000000001",
		"action": "FAILED to reconcile Service: clickhouse-posthog CHI: posthog",
		"error":  `Service "clickhouse-posthog" is invalid: failed to allocate IP 10.96.16.107: provided IP is already allocated`,
	}
	with := func(base map[string]string, changes ...string) map[string]string {
		out := map[string]string{}
		for k, v := range base {
			out[k] = v
		}
		for i := 0; i+1 < len(changes); i += 2 {
			out[changes[i]] = changes[i+1]
		}
		return out
	}

	// Each horizon is past the point where one restart too many would land:
	// the third at 900 s (or 270 s for FAILED), the first at 300 s.
	for _, tc := range []struct {
		name     string
		state    map[string]string
		restarts int
		// The first restart lands in [from, until) simulated seconds.
		from, until int
	}{
		{"a reconcile the API server cut short", with(cutShort, "horizon", "960"), 2, 300, 330},
		{"the same reconcile, cleared by the first restart",
			with(cutShort, "on-restart", `echo Completed >"$STUB_STATE/status"`), 1, 300, 330},
		{"a FAILED Service reconcile that was never retried", with(failed, "horizon", "400"), 2, 90, 120},
		{"a reconcile still waiting on its StatefulSet", with(cutShort, "sts", "1/ ", "horizon", "660"), 0, 0, 0},
		{"a reconcile that has created no StatefulSet yet", with(cutShort, "sts", "", "horizon", "660"), 0, 0, 0},
		{"one StatefulSet of two still starting", with(cutShort, "sts", "1/1 1/ ", "horizon", "660"), 0, 0, 0},
		{"a reconcile that keeps moving", with(cutShort, "action", "@tick", "horizon", "660"), 0, 0, 0},
		{"a new reconcile task every pass", with(cutShort, "task", "@tick", "horizon", "660"), 0, 0, 0},
		{"a CHI being torn down", with(cutShort, "status", "Terminating", "horizon", "660"), 0, 0, 0},
		{"a Completed CHI", with(cutShort, "status", "Completed"), 0, 0, 0},
		// No horizon: the whole thirty minutes, to the loop's own end.
		{"an API server that never answers", with(cutShort, "chi.fail", ""), 0, 0, 0},
	} {
		for _, shell := range shells {
			t.Run(tc.name+"/"+filepath.Base(shell), func(t *testing.T) {
				t.Parallel()
				run := runClickHouseUnstick(t, shell, script, tc.state)
				if run.err != nil {
					t.Fatalf("the script exited with %v — a failed hook fails the posthog sync:\n%s", run.err, run.output)
				}
				if strings.Contains(run.output, "stub kubectl: unsupported") {
					t.Fatalf("the script made a kubectl call the stand-in does not know:\n%s", run.output)
				}
				if len(run.restarts) != tc.restarts {
					t.Fatalf("the operator was restarted %d time(s) (at %v s), want %d:\n%s",
						len(run.restarts), run.restarts, tc.restarts, run.output)
				}
				if tc.restarts > 0 && (run.restarts[0] < tc.from || run.restarts[0] >= tc.until) {
					t.Errorf("the first restart came at %d s, want within [%d, %d)", run.restarts[0], tc.from, tc.until)
				}
				for _, call := range run.calls {
					if strings.Contains(call, "rollout") || strings.Contains(call, "patch") {
						t.Errorf("the script changed the Argo CD-tracked operator Deployment (%q); it must delete the pod", call)
					}
				}
			})
		}
	}
}
