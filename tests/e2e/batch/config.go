package batch

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Config is the suite's knobs (NEW-1). The suite is OPT-IN: it scaffolds a JVM
// application and waits for app-ci to build it with kpack, which a Kind runner
// pays for in tens of minutes, and `make e2e` runs every package under
// tests/e2e — this one first, alphabetically, before bootstrap brings a
// cluster up. Unset, it skips with the reason.
//
//	ADHAR_E2E_BATCH=1                  run the suite
//	ADHAR_E2E_SKIP_UP=1                use the running platform (shared with bootstrap)
//	ADHAR_E2E_KEEP=1                   leave the cluster and the app behind
//	ADHAR_E2E_BATCH_APP=<name>         application name (default batch-e2e-<base36 minutes>)
//	ADHAR_E2E_BATCH_REUSE=1            the app already exists: skip the scaffold (needs SKIP_UP)
//	ADHAR_E2E_BATCH_TEMPLATE=<id>      template to scaffold (default: the first that exists of
//	                                   springboot-batch-job, the design's name, and springboot-batch-service)
//	ADHAR_E2E_BATCH_SCHEDULE=<cron>    the test environment's schedule (default */2 * * * *)
//	ADHAR_E2E_BATCH_ITEMS=<n>          rows the interrupted runs read (default 50000)
//	ADHAR_E2E_BATCH_TIMEOUT            overall budget (default 150m)
//	ADHAR_E2E_UP_TIMEOUT, ADHAR_E2E_APPS_TIMEOUT   `adhar up`, as bootstrap reads them
//	ADHAR_E2E_BATCH_BUILD_TIMEOUT      scaffold → image built → BatchJobs Ready (default 45m)
//	ADHAR_E2E_BATCH_RUN_TIMEOUT        one run, start to end (default 15m)
//	ADHAR_E2E_BATCH_SCHEDULE_TIMEOUT   the scheduled runs in test (default 20m)
type Config struct {
	Enabled, SkipUp, Keep, Reuse bool
	App                          string
	// Tag names this suite instance (base36 minutes): the runs it builds
	// itself carry it, so a rerun against a reused app meets no old name.
	Tag                   string
	Templates             []string
	Schedule              string
	Items                 int
	Overall, Up, Apps     time.Duration
	Build, Run, Scheduled time.Duration
	// DateBase is the first business date this suite instance uses; every
	// manual identity is a distinct day after it, so a rerun against a reused
	// app does not meet the identities the last one completed.
	DateBase time.Time
}

// TemplateCandidates are tried in order: §9.1 names the template
// springboot-batch-job; adhar-templates ships it as springboot-batch-service
// (2026-11-14).
var TemplateCandidates = []string{"springboot-batch-job", "springboot-batch-service"}

// batchJobName is the composition's rule for a BatchJob name (§6.4): a DNS-1035
// label of at most 40 characters, since run names add `-<hash8>-r<n>`.
var batchJobName = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)

// LoadConfig reads the knobs. A knob that is set and cannot be read is an
// error rather than its default, so a typo does not silently shorten a budget.
func LoadConfig(getenv func(string) string, now time.Time) (Config, error) {
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
	minutes := now.Unix() / 60
	c := Config{
		Enabled: on("ADHAR_E2E_BATCH"), SkipUp: on("ADHAR_E2E_SKIP_UP"), Keep: on("ADHAR_E2E_KEEP"), Reuse: on("ADHAR_E2E_BATCH_REUSE"),
		App:       getenv("ADHAR_E2E_BATCH_APP"),
		Tag:       strconv.FormatInt(minutes, 36),
		Templates: TemplateCandidates,
		Schedule:  "*/2 * * * *",
		Items:     50000,
		Overall:   dur("ADHAR_E2E_BATCH_TIMEOUT", 150*time.Minute),
		Up:        dur("ADHAR_E2E_UP_TIMEOUT", 60*time.Minute),
		Build:     dur("ADHAR_E2E_BATCH_BUILD_TIMEOUT", 45*time.Minute),
		Run:       dur("ADHAR_E2E_BATCH_RUN_TIMEOUT", 15*time.Minute),
		Scheduled: dur("ADHAR_E2E_BATCH_SCHEDULE_TIMEOUT", 20*time.Minute),
		DateBase:  time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(minutes%36000)),
	}
	c.Apps = dur("ADHAR_E2E_APPS_TIMEOUT", max(c.Up-20*time.Minute, 15*time.Minute))
	if c.App == "" {
		c.App = "batch-e2e-" + c.Tag
	}
	if t := strings.TrimSpace(getenv("ADHAR_E2E_BATCH_TEMPLATE")); t != "" {
		c.Templates = []string{t}
	}
	if s := strings.TrimSpace(getenv("ADHAR_E2E_BATCH_SCHEDULE")); s != "" {
		c.Schedule = s
	}
	if v := getenv("ADHAR_E2E_BATCH_ITEMS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1000 {
			errs = append(errs, fmt.Sprintf("ADHAR_E2E_BATCH_ITEMS=%q: at least 1000, or a run ends before it can be interrupted", v))
		} else {
			c.Items = n
		}
	}
	if len(c.App) > 40 || !batchJobName.MatchString(c.App) {
		errs = append(errs, fmt.Sprintf("ADHAR_E2E_BATCH_APP=%q is not a BatchJob name (a DNS-1035 label of at most 40 characters)", c.App))
	}
	if c.Reuse && getenv("ADHAR_E2E_BATCH_APP") == "" {
		errs = append(errs, "ADHAR_E2E_BATCH_REUSE needs ADHAR_E2E_BATCH_APP: the app to reuse")
	}
	if c.Reuse && !c.SkipUp {
		errs = append(errs, "ADHAR_E2E_BATCH_REUSE needs ADHAR_E2E_SKIP_UP: without it `adhar up --recreate` destroys the app to reuse")
	}
	if len(errs) > 0 {
		return c, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return c, nil
}

// Date is the nth business date this suite instance owns.
func (c Config) Date(n int) string {
	return c.DateBase.AddDate(0, 0, n).Format(time.DateOnly)
}
