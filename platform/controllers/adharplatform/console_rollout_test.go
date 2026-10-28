package adharplatform

import (
	"fmt"
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
// proves each guard rejects the configuration that actually shipped.

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
// "is the running console older than the Secret?" must compare in a way the
// alpine/k8s image can evaluate, against the NEWEST pod.
func TestConsoleRestartHooksCompareTimestampsWithoutDate(t *testing.T) {
	root := filepath.Join(stackRoot(t), "packages")
	found := map[string]bool{}
	walkStackFiles(t, root, func(path, body string) {
		if !restartsTheConsole(body) || !strings.Contains(body, "POD_TS") {
			return
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		found[rel] = true
		for _, p := range timestampComparisonProblems(body) {
			t.Errorf("%s %s", rel, p)
		}
	})
	for _, want := range []string{
		"core/adhar-console/manifests/oidc-reload.yaml",
		"application/plane/manifests/console-reload.yaml",
		"application/strapi/manifests/console-token.yaml",
	} {
		if !found[want] {
			t.Errorf("%s no longer restarts the console by comparing timestamps, so this guard stopped covering it — find where that logic went and point the guard there", want)
		}
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
	fixedHook := `POD_TS=$(kubectl -n "$NS" get pods -l app=console \
           -o jsonpath='{range .items[*]}{.status.startTime}{"\n"}{end}' 2>/dev/null \
           | sort | tail -n 1 || true)
if [ -n "$POD_TS" ] && [ "${POD_TS//[!0-9]/}" -gt "${SECRET_TS//[!0-9]/}" ]; then`
	if got := timestampComparisonProblems(fixedHook); len(got) != 0 {
		t.Errorf("the fixed comparison should pass, got %v", got)
	}
	if !restartsTheConsole(`NS=adhar-system; DEPLOY=adhar-console; SECRET=strapi-api-token`) ||
		!restartsTheConsole("DEPLOY=adhar-console\n") ||
		restartsTheConsole("DEPLOY=adhar-console-shell\n") {
		t.Error("restartsTheConsole no longer recognises the hooks' DEPLOY assignments")
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
	consoleDeployAssignment = regexp.MustCompile(`\bDEPLOY=["']?adhar-console["']?(\s|;|$)`)
	dateParsesAKubeStamp    = regexp.MustCompile(`date\s+(-d|--date)[ =]?"?\$\{?(POD|SECRET)_TS`)
)

func restartsTheConsole(body string) bool {
	return consoleDeployAssignment.MatchString(body) || strings.Contains(body, "deployment/adhar-console")
}

// timestampComparisonProblems lists the ways a hook's "is the running pod newer
// than the Secret?" test can misfire.
func timestampComparisonProblems(body string) []string {
	var out []string
	if dateParsesAKubeStamp.MatchString(body) {
		out = append(out, "parses a Kubernetes timestamp with `date -d`: busybox date (alpine/k8s) rejects RFC 3339 (\"invalid date\"), the test errors and the hook restarts the console on every sync")
	}
	if strings.Contains(body, ".items[0].status.startTime") {
		out = append(out, "reads .items[0].status.startTime: pods list in NAME order, so mid-rollout that can be the pod being replaced rather than the newest")
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
