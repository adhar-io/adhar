package adharplatform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// cilium-agent refuses to start on an api-rate-limit it cannot parse, and with
// no agent no pod on the node gets a network — so the value is checked here
// against the keys and value forms Cilium 1.20 accepts (pkg/rate/api_limiter.go
// parseUserConfig / parseRate), not left to a live cluster.
func TestCiliumEndpointCreateRateLimitIsRaisedAndParses(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("resources", "cilium", "install.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^  api-rate-limit: (".*")$`).FindStringSubmatch(string(b))
	if m == nil {
		t.Fatal("cilium-config sets no api-rate-limit: endpoint creation runs at Cilium's 0.5/s default and " +
			"self-throttles on a loaded node (2026-10-21)")
	}
	var raw string
	if err := json.Unmarshal([]byte(m[1]), &raw); err != nil {
		t.Fatalf("api-rate-limit is not a quoted string: %v", err)
	}
	var calls map[string]string
	if err := json.Unmarshal([]byte(raw), &calls); err != nil {
		t.Fatalf("api-rate-limit is not a JSON object of API call -> settings: %v", err)
	}
	known := map[string]string{
		"rate-limit": "rate", "rate-burst": "int", "min-wait-duration": "duration", "max-wait-duration": "duration",
		"estimated-processing-duration": "duration", "auto-adjust": "bool", "parallel-requests": "int",
		"min-parallel-requests": "int", "max-parallel-requests": "int", "mean-over": "int", "log": "bool",
		"delayed-adjustment-factor": "float", "max-adjustment-factor": "float", "skip-initial": "int",
	}
	if _, ok := calls["endpoint-create"]; !ok {
		t.Fatal("api-rate-limit does not configure endpoint-create, the call every new pod makes")
	}
	got := map[string]string{}
	for call, s := range calls {
		switch call {
		case "endpoint-create", "endpoint-delete", "endpoint-get", "endpoint-patch", "endpoint-list":
		default:
			t.Errorf("unknown API call %q", call)
		}
		for _, kv := range strings.Split(s, ",") {
			k, v, found := strings.Cut(kv, ":")
			kind, isKnown := known[k]
			if !found || !isKnown {
				t.Errorf("%s: %q is not a setting Cilium parses", call, kv)
				continue
			}
			if call == "endpoint-create" {
				got[k] = v
			}
			var perr error
			switch kind {
			case "int":
				_, perr = strconv.Atoi(v)
			case "float":
				_, perr = strconv.ParseFloat(v, 64)
			case "bool":
				_, perr = strconv.ParseBool(v)
			case "duration":
				_, perr = time.ParseDuration(v)
			case "rate":
				n, unit, ok := strings.Cut(v, "/")
				if _, err := strconv.ParseFloat(n, 64); err != nil || !ok {
					perr = err
				} else if _, err := strconv.Atoi(unit); err == nil {
					t.Errorf("%s: rate %q has no duration suffix; Cilium rejects it", call, v)
				}
			}
			if perr != nil {
				t.Errorf("%s: %s=%q does not parse: %v", call, k, v, perr)
			}
		}
	}
	if n, _ := strconv.Atoi(got["parallel-requests"]); n < 8 {
		t.Errorf("endpoint-create parallel-requests=%q; Cilium's default of 4 is what stalled pod starts", got["parallel-requests"])
	}
	if d, _ := time.ParseDuration(got["estimated-processing-duration"]); d < 4*time.Second {
		t.Errorf("estimated-processing-duration=%q: below what a loaded node measures (~6s), auto-adjust throttles toward it",
			got["estimated-processing-duration"])
	}
	if d, _ := time.ParseDuration(got["max-wait-duration"]); d >= 4*time.Minute {
		t.Errorf("max-wait-duration=%q must stay inside kubelet's 4-minute sandbox timeout", got["max-wait-duration"])
	}

	// The generator's values file must carry the same setting, or the next
	// hack/cilium/generate-manifests.sh run silently drops it.
	v, err := os.ReadFile(filepath.Join("..", "..", "..", "hack", "cilium", "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(v), "apiRateLimit: '"+raw+"'") {
		t.Error("hack/cilium/values.yaml apiRateLimit disagrees with the manifest; regenerating would revert it")
	}
}
