package batch

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// VolcanoConfig is the Phase-2 canary's knobs (design 0025 §15 Phase 2,
// PLAT-2.8). The canary is OPT-IN, like the Phase-1 suite, and separately:
// ADHAR_E2E_VOLCANO alone runs Test_BatchOnVolcano, and `make
// e2e-batch-volcano` sets it beside ADHAR_E2E_BATCH so the whole batch suite
// runs on a Kind cluster that has Volcano (A2.9).
//
//	ADHAR_E2E_VOLCANO=1                  run the canary
//	ADHAR_E2E_VOLCANO_STACK=<dir>        where the harness stages its copy of platform/stack with
//	                                     Volcano switched on (default: a new temporary directory). The
//	                                     shipped profile is never edited; the copy is what `adhar up -p` seeds
//	ADHAR_E2E_VOLCANO_APP=<name>         the partitioned application (default batch-e2e-p<tag>)
//	ADHAR_E2E_VOLCANO_TEMPLATE=<id>      its template (default springboot-batch-partitioned, design §9.2)
//	ADHAR_E2E_VOLCANO_GANG=<n>           A2.1: the cluster is given room for n members, the gang has n+1 (default 3)
//	ADHAR_E2E_VOLCANO_HOLD=<d>           A2.1: how long an over-size gang must stay unstarted (default 90s)
//	ADHAR_E2E_VOLCANO_COMPLETE_WITHIN=<d> A2.3: the manager's end to the last worker's (default 30s)
//	ADHAR_E2E_VOLCANO_SCENARIO_TIMEOUT   one scheduling scenario, start to end (default 20m)
//	ADHAR_E2E_VOLCANO_NATIVE_TIMEOUT     A2.10: descriptor gone → every BatchJob native, and back (default 15m)
//	ADHAR_E2E_KUBE_VERSION=<v>           passed to `adhar up --kube-version` (default: the CLI's, v1.37.x)
//	ADHAR_E2E_VOLCANO_KUBE_MINOR=<n>     the lowest Kubernetes minor the canary accepts (default 37, design §4.6)
type VolcanoConfig struct {
	Enabled          bool
	Stack            string
	App              string
	Templates        []string
	Gang             int
	Hold             time.Duration
	CompleteWithin   time.Duration
	Scenario, Native time.Duration
	KubeVersion      string
	MinKubeMinor     int
}

// PartitionedTemplateCandidates: §9.2 names the Phase-2 template
// springboot-batch-partitioned (TPL-2.1).
var PartitionedTemplateCandidates = []string{"springboot-batch-partitioned"}

// LoadVolcanoConfig reads the canary's knobs; the Phase-1 Config supplies the
// tag the canary's own names share. A set knob that cannot be read is an error.
func LoadVolcanoConfig(getenv func(string) string, base Config) (VolcanoConfig, error) {
	on := func(k string) bool {
		v := strings.ToLower(strings.TrimSpace(getenv(k)))
		return v != "" && v != "0" && v != "false" && v != "no"
	}
	var errs []string
	dur := func(k string, def time.Duration) time.Duration {
		v := getenv(k)
		if v == "" {
			return def
		}
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			errs = append(errs, fmt.Sprintf("%s=%q is not a positive duration", k, v))
			return def
		}
		return d
	}
	count := func(k string, def, lo, hi int) int {
		v := getenv(k)
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < lo || n > hi {
			errs = append(errs, fmt.Sprintf("%s=%q is not a whole number from %d to %d", k, v, lo, hi))
			return def
		}
		return n
	}
	c := VolcanoConfig{
		Enabled:   on("ADHAR_E2E_VOLCANO"),
		Stack:     strings.TrimSpace(getenv("ADHAR_E2E_VOLCANO_STACK")),
		App:       strings.TrimSpace(getenv("ADHAR_E2E_VOLCANO_APP")),
		Templates: PartitionedTemplateCandidates,
		// Room for one gives a gang of two, the smallest that can be half-placed.
		Gang:           count("ADHAR_E2E_VOLCANO_GANG", 3, 1, 16),
		Hold:           dur("ADHAR_E2E_VOLCANO_HOLD", 90*time.Second),
		CompleteWithin: dur("ADHAR_E2E_VOLCANO_COMPLETE_WITHIN", 30*time.Second),
		Scenario:       dur("ADHAR_E2E_VOLCANO_SCENARIO_TIMEOUT", 20*time.Minute),
		Native:         dur("ADHAR_E2E_VOLCANO_NATIVE_TIMEOUT", 15*time.Minute),
		KubeVersion:    strings.TrimSpace(getenv("ADHAR_E2E_KUBE_VERSION")),
		MinKubeMinor:   count("ADHAR_E2E_VOLCANO_KUBE_MINOR", 37, 20, 99),
	}
	if c.App == "" {
		c.App = "batch-e2e-p" + base.Tag
	}
	if t := strings.TrimSpace(getenv("ADHAR_E2E_VOLCANO_TEMPLATE")); t != "" {
		c.Templates = []string{t}
	}
	if len(c.App) > 40 || !batchJobName.MatchString(c.App) {
		errs = append(errs, fmt.Sprintf("ADHAR_E2E_VOLCANO_APP=%q is not a BatchJob name (a DNS-1035 label of at most 40 characters)", c.App))
	}
	if c.App == base.App {
		errs = append(errs, fmt.Sprintf("ADHAR_E2E_VOLCANO_APP=%q is the Phase-1 app: the partitioned job needs its own", c.App))
	}
	if c.Stack != "" && base.SkipUp {
		errs = append(errs, "ADHAR_E2E_VOLCANO_STACK is the stack `adhar up` seeds, and ADHAR_E2E_SKIP_UP skips `adhar up`")
	}
	// The manager-ends-workers bound must be shorter than a member could run on
	// its own, or A2.3 would pass on workers that simply finished.
	if c.CompleteWithin >= c.Scenario {
		errs = append(errs, "ADHAR_E2E_VOLCANO_COMPLETE_WITHIN must be shorter than ADHAR_E2E_VOLCANO_SCENARIO_TIMEOUT")
	}
	if len(errs) > 0 {
		return c, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return c, nil
}

// UpArgs is `adhar up` for this run: the Phase-1 suite's, plus the staged stack
// when the canary installs Volcano, and the Kubernetes version when pinned.
func (c VolcanoConfig) UpArgs(apps time.Duration, stagedStack string) []string {
	args := []string{"up", "--recreate", "--apps-timeout", apps.String()}
	if stagedStack != "" {
		args = append(args, "-p", stagedStack)
	}
	if c.KubeVersion != "" {
		args = append(args, "--kube-version", c.KubeVersion)
	}
	return args
}
