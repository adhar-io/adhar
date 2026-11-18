package adharplatform

// Phase 3 of the BatchJob composition (design 0025 PLAT-3.3/3.4): the business
// calendar (D5), the JobRepository's retention (D4) and the definition-time refusals
// of the fields the platform, rather than the composition, acts on (`smoke`, D6;
// `dependsOn`, §12). Rendered in-process with the same template language, and through
// `crossplane render` when Docker is available. Each guard was mutation-tested: the
// behaviour reverted in the composition, the test seen failing, the fix restored.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const calCalendarText = "name: settlement-days\ntimeZone: Asia/Singapore\nweekendDays: [SATURDAY, SUNDAY]\nholidays: [\"2026-12-25\"]\nbusinessDays: [\"2026-12-26\"]\n"

// calCalendar is a calendar ConfigMap; label false leaves the calendar label off.
func calCalendar(namespace, name, text string, label bool) map[string]any {
	md := map[string]any{"name": name, "namespace": namespace}
	if label {
		md["labels"] = map[string]any{"adhar.io/batch-calendar": "true"}
	}
	return map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": md, "data": map[string]any{"calendar.yaml": text}}
}

// calXR is the scheduled single-mode fixture (namespace prod) naming a calendar.
func calXR(t *testing.T, calendar string) map[string]any {
	t.Helper()
	return bjWithParams(bjXR(t, "xr-scheduled.yaml"), map[string]any{
		"schedule": map[string]any{"cron": "30 1 * * *", "timeZone": "Asia/Singapore", "calendar": calendar},
	})
}

// calPodSpecs are every pod template of a run object, by role.
func calPodSpecs(run map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for role, pod := range vcPods(run) {
		out[role], _ = pod["spec"].(map[string]any)
	}
	return out
}

func calRequirements(docs []map[string]any) map[string]any {
	for _, d := range docs {
		if d["kind"] == "ExtraResources" {
			r, _ := d["requirements"].(map[string]any)
			return r
		}
	}
	return nil
}

// D5: the job's namespace first, then adhar-system; a ConfigMap that is not labelled
// as a calendar, or holds no calendar.yaml, is not one.
func TestBatchJobCalendarIsLookedUpInTheNamespaceThenThePlatform(t *testing.T) {
	platform := calCalendar("adhar-system", "settlement-days", "name: settlement-days\nholidays: [\"2026-01-01\"]\n", true)
	local := calCalendar("prod", "settlement-days", calCalendarText, true)

	reqs := calRequirements(bjMustRender(t, calXR(t, "settlement-days"), append(bjCluster(t), platform), nil))
	for key, ns := range map[string]string{"calendarLocal": "prod", "calendarPlatform": "adhar-system"} {
		r, _ := reqs[key].(map[string]any)
		if r["kind"] != "ConfigMap" || r["matchName"] != "settlement-days" || r["namespace"] != ns {
			t.Errorf("requirement %s = %v, want ConfigMap %s/settlement-days", key, r, ns)
		}
	}
	if r := calRequirements(bjMustRender(t, bjXR(t, "xr-scheduled.yaml"), bjCluster(t), nil)); r["calendarLocal"] != nil || r["calendarPlatform"] != nil {
		t.Error("a job that names no calendar asks for one")
	}

	for _, c := range []struct {
		name    string
		cluster []map[string]any
		from    string
	}{
		{"only the platform's", append(bjCluster(t), platform), "adhar-system/settlement-days"},
		{"the namespace's wins", append(bjCluster(t), platform, local), "prod/settlement-days"},
		{"an unlabelled namespace ConfigMap is not a calendar", append(bjCluster(t), platform, calCalendar("prod", "settlement-days", calCalendarText, false)), "adhar-system/settlement-days"},
		{"a namespace calendar with no file is not one", append(bjCluster(t), platform, calCalendar("prod", "settlement-days", "  \n", true)), "adhar-system/settlement-days"},
	} {
		t.Run(c.name, func(t *testing.T) {
			docs := bjMustRender(t, calXR(t, "settlement-days"), c.cluster, nil)
			if got := bjStatus(t, docs)["calendar"]; got != c.from {
				t.Errorf("status.calendar = %v, want %s", got, c.from)
			}
			if got := dig(bjMustResource(t, docs, "calendar"), "metadata", "annotations", "adhar.io/batch-calendar-source"); got != c.from {
				t.Errorf("the copy says it came from %v, want %s", got, c.from)
			}
			if p := bjCondition(t, docs, "PrerequisitesReady"); p["status"] != "True" || !strings.Contains(fmt.Sprint(p["message"]), "calendar settlement-days from "+c.from) {
				t.Errorf("PrerequisitesReady = %v", p)
			}
		})
	}
}

// The copy is the source, byte for byte, and every run mounts it read-only: the
// clock's, a person's from the template, and every pod of a partitioned run.
func TestBatchJobCalendarIsCopiedAndMountedIntoEveryRun(t *testing.T) {
	cluster := append(bjCluster(t), calCalendar("prod", "settlement-days", calCalendarText, true))
	docs := bjMustRender(t, calXR(t, "settlement-days"), cluster, nil)
	cm := bjMustResource(t, docs, "calendar")
	if cm["kind"] != "ConfigMap" || dig(cm, "metadata", "name") != "importer-calendar" || dig(cm, "metadata", "namespace") != "prod" {
		t.Fatalf("the copy is %v %v/%v", cm["kind"], dig(cm, "metadata", "namespace"), dig(cm, "metadata", "name"))
	}
	if got := dig(cm, "data", "calendar.yaml"); got != calCalendarText {
		t.Errorf("the copy holds %q, want the source's %q", got, calCalendarText)
	}
	// Labelled as the job's, never as a calendar: it must not resolve as one.
	if _, set := dig(cm, "metadata", "labels").(map[string]any)["adhar.io/batch-calendar"]; set {
		t.Error("the copy is labelled adhar.io/batch-calendar, so it would itself resolve as a calendar")
	}
	if dig(cm, "metadata", "labels", "adhar.io/batch-job") != "importer" || dig(cm, "metadata", "labels", "adhar.io/team") != "payments" {
		t.Errorf("the copy lacks the job's hierarchy labels: %v", dig(cm, "metadata", "labels"))
	}

	check := func(t *testing.T, what string, run map[string]any) {
		t.Helper()
		specs := calPodSpecs(run)
		if len(specs) == 0 {
			t.Fatalf("%s has no pod template", what)
		}
		for role, spec := range specs {
			var vol map[string]any
			for _, v := range spec["volumes"].([]any) {
				if m := v.(map[string]any); m["name"] == "calendar" {
					vol = m
				}
			}
			if dig(vol, "configMap", "name") != "importer-calendar" && dig(vol, "configMap", "name") != "settlement-calendar" {
				t.Errorf("%s/%s: volume calendar = %v, want the copy", what, role, vol)
			}
			// defaultMode is what the API server writes in: the template must already
			// carry it, or every run differs from its template.
			if fmt.Sprint(dig(vol, "configMap", "defaultMode")) != "420" {
				t.Errorf("%s/%s: the calendar volume has defaultMode %v", what, role, dig(vol, "configMap", "defaultMode"))
			}
			// Optional, so a calendar that never resolved (no copy) is a run that exits 2
			// on the missing file (D5), not one parked in FailedMount for six hours.
			if dig(vol, "configMap", "optional") != true {
				t.Errorf("%s/%s: the calendar volume is not optional: a run started before the copy exists never starts", what, role)
			}
			items, _ := dig(vol, "configMap", "items").([]any)
			if len(items) != 1 || dig(items[0].(map[string]any), "key") != "calendar.yaml" || dig(items[0].(map[string]any), "path") != "calendar.yaml" {
				t.Errorf("%s/%s: calendar items = %v", what, role, items)
			}
			c := spec["containers"].([]any)[0].(map[string]any)
			mounted := false
			for _, m := range c["volumeMounts"].([]any) {
				mm := m.(map[string]any)
				if mm["name"] == "calendar" {
					mounted = mm["mountPath"] == "/etc/adhar/batch/calendar" && mm["readOnly"] == true
				}
			}
			if !mounted {
				t.Errorf("%s/%s: the calendar is not mounted read-only at /etc/adhar/batch/calendar: %v", what, role, c["volumeMounts"])
			}
			if env := bjEnv(t, c); env["ADHAR_BATCH_CALENDAR"] != "/etc/adhar/batch/calendar/calendar.yaml" {
				t.Errorf("%s/%s: ADHAR_BATCH_CALENDAR = %v", what, role, env["ADHAR_BATCH_CALENDAR"])
			}
		}
	}
	check(t, "the run template", bjRunTemplate(t, docs))
	check(t, "the CronJob", dig(bjMustResource(t, docs, "schedule-native"), "spec", "jobTemplate").(map[string]any))

	// A gang: both tasks (the manager decides; the workers need the same pod spec as
	// the template the integrity policy compares).
	gangXR := bjWithParams(vcXR(t, "xr-partitioned-scheduled.yaml"), map[string]any{
		"schedule": map[string]any{"cron": "0 2 * * *", "timeZone": "Asia/Singapore", "calendar": "weekdays"},
	})
	gang := vcMustRender(t, gangXR, append(vcCluster(t), calCalendar("adhar-system", "weekdays", "name: weekdays\n", true)), nil)
	run, kind := vcTemplate(t, gang)
	if kind != "VolcanoJob" || len(calPodSpecs(run)) != 2 {
		t.Fatalf("the gang fixture rendered a %s with %d pod templates", kind, len(calPodSpecs(run)))
	}
	check(t, "the gang template", run)
	check(t, "the Volcano CronJob", dig(bjMustResource(t, gang, "schedule-volcano"), "spec", "jobTemplate").(map[string]any))

	// No calendar: no volume, no mount, no variable; the template is Phase 2's.
	plain := bjRunTemplate(t, bjMustRender(t, bjXR(t, "xr-scheduled.yaml"), bjCluster(t), nil))
	if fmt.Sprint(dig(plain, "spec", "template", "spec", "volumes")) != "[map[emptyDir:map[sizeLimit:1Gi] name:tmp]]" {
		t.Errorf("a job without a calendar mounts %v", dig(plain, "spec", "template", "spec", "volumes"))
	}
	if _, set := bjEnv(t, bjContainer(t, plain))["ADHAR_BATCH_CALENDAR"]; set {
		t.Error("a job without a calendar sets ADHAR_BATCH_CALENDAR")
	}

	// Statusless: ready once observed.
	observed := bjObserved(t, "observed-importer.yaml")
	observed["calendar"] = map[string]any{"resource": map[string]any{"metadata": map[string]any{"uid": "c1"}}}
	if a := dig(bjMustResource(t, bjMustRender(t, calXR(t, "settlement-days"), cluster, observed), "calendar"), "metadata", "annotations", "gotemplating.fn.crossplane.io/ready"); a != "True" {
		t.Errorf("the observed copy is ready=%v, and function-auto-ready would hold the XR unready", a)
	}
	if a := dig(cm, "metadata", "annotations", "gotemplating.fn.crossplane.io/ready"); a != "False" {
		t.Errorf("the copy is ready=%v before it exists", a)
	}
}

// An unresolvable calendar is a missing prerequisite (D5): the schedule is suspended
// and the condition names both places it looked. A copy already made is kept as it
// is, because deleting it would leave a run in flight without the file it mounts.
func TestBatchJobUnresolvableCalendarSuspendsTheScheduleAndSaysSo(t *testing.T) {
	docs := bjMustRender(t, calXR(t, "settlement-days"), bjCluster(t), nil)
	p := bjCondition(t, docs, "PrerequisitesReady")
	if p["status"] != "False" || p["reason"] != "CalendarUnresolvable" {
		t.Errorf("PrerequisitesReady = %v", p)
	}
	for _, w := range []string{"settlement-days", "prod", "adhar-system", `adhar.io/batch-calendar "true"`} {
		if !strings.Contains(fmt.Sprint(p["message"]), w) {
			t.Errorf("PrerequisitesReady does not say %s: %v", w, p["message"])
		}
	}
	if dig(bjMustResource(t, docs, "schedule-native"), "spec", "suspend") != true {
		t.Error("the schedule keeps firing runs that cannot mount their calendar")
	}
	if bjResource(docs, "calendar") != nil {
		t.Error("a copy was composed of a calendar that does not exist")
	}
	if _, set := bjStatus(t, docs)["calendar"]; set {
		t.Error("status.calendar names a calendar that did not resolve")
	}
	// The template still mounts it, so it does not change shape while the source is away.
	if env := bjEnv(t, bjContainer(t, bjRunTemplate(t, docs))); env["ADHAR_BATCH_CALENDAR"] == nil {
		t.Error("the run template dropped its calendar while the calendar is missing")
	}
	// ...but optionally: the copy it names does not exist, and a run started from this
	// template must reach the runner (exit 2, D5) rather than wait on a mount.
	for _, v := range dig(bjRunTemplate(t, docs), "spec", "template", "spec", "volumes").([]any) {
		if m := v.(map[string]any); m["name"] == "calendar" && dig(m, "configMap", "optional") != true {
			t.Errorf("the never-resolved calendar is mounted non-optionally: %v", m)
		}
	}

	// Gone after a copy was made: the copy is kept as observed.
	observed := map[string]any{"calendar": map[string]any{"resource": map[string]any{
		"metadata": map[string]any{"uid": "c1", "annotations": map[string]any{"adhar.io/batch-calendar-source": "prod/settlement-days"}},
		"data":     map[string]any{"calendar.yaml": calCalendarText},
	}}}
	kept := bjMustResource(t, bjMustRender(t, calXR(t, "settlement-days"), bjCluster(t), observed), "calendar")
	if dig(kept, "data", "calendar.yaml") != calCalendarText || dig(kept, "metadata", "annotations", "adhar.io/batch-calendar-source") != "prod/settlement-days" {
		t.Errorf("the kept copy is %v", kept)
	}

	// Another prerequisite missing first keeps its own reason.
	docs = bjMustRender(t, calXR(t, "settlement-days"), bjCluster(t, "adhar-batch-critical"), nil)
	if p := bjCondition(t, docs, "PrerequisitesReady"); p["reason"] != "PriorityClassMissing" || !strings.Contains(fmt.Sprint(p["message"]), "calendar settlement-days cannot be resolved") {
		t.Errorf("PrerequisitesReady with two prerequisites missing = %v", p)
	}
}

// D4: every run carries the retention, defaults included, in every shape and from
// every trigger; it is composition-owned, never client-settable.
func TestBatchJobRunsCarryTheirRetention(t *testing.T) {
	retention := func(t *testing.T, what string, run map[string]any, days, keep string) {
		t.Helper()
		for role, pod := range vcPods(run) {
			_, env := vcContainerEnv(t, pod)
			if env["ADHAR_BATCH_RETENTION_DAYS"] != days || env["ADHAR_BATCH_RETENTION_KEEP_LAST"] != keep {
				t.Errorf("%s/%s: retention = %v/%v, want %s/%s", what, role, env["ADHAR_BATCH_RETENTION_DAYS"], env["ADHAR_BATCH_RETENTION_KEEP_LAST"], days, keep)
			}
		}
	}
	docs := bjMustRender(t, bjXR(t, "xr-scheduled.yaml"), bjCluster(t), nil)
	retention(t, "default template", bjRunTemplate(t, docs), "90", "30")
	retention(t, "default CronJob", dig(bjMustResource(t, docs, "schedule-native"), "spec", "jobTemplate").(map[string]any), "90", "30")

	set := map[string]any{"retention": map[string]any{"days": 14, "keepLast": 5}}
	docs = bjMustRender(t, bjWithParams(bjXR(t, "xr-scheduled.yaml"), set), bjCluster(t), nil)
	retention(t, "explicit template", bjRunTemplate(t, docs), "14", "5")
	for _, c := range []struct {
		name    string
		cluster func(t *testing.T) []map[string]any
		kind    string
	}{{"indexed", vcNoVolcano, "IndexedJob"}, {"gang", func(t *testing.T) []map[string]any { return vcCluster(t) }, "VolcanoJob"}} {
		run, kind := vcTemplate(t, vcMustRender(t, bjWithParams(vcXR(t, "xr-partitioned.yaml"), set), c.cluster(t), nil))
		if kind != c.kind {
			t.Fatalf("%s rendered a %s", c.name, kind)
		}
		retention(t, c.name, run, "14", "5")
	}
	// The env a definition may set never reaches it.
	if _, err := bjRender(t, bjWithParams(bjXR(t, "xr-ondemand.yaml"), map[string]any{"env": map[string]any{"ADHAR_BATCH_RETENTION_DAYS": "1"}}), bjCluster(t), nil); err == nil || !strings.Contains(err.Error(), "owned by the runner") {
		t.Errorf("env ADHAR_BATCH_RETENTION_DAYS on a definition was not refused: %v", err)
	}
}

// What the gate and the observer would refuse at night is refused at definition.
func TestBatchJobPhaseThreeRefusals(t *testing.T) {
	sched := bjXR(t, "xr-scheduled.yaml") // declares businessDate, source and dryRun
	for _, c := range []struct {
		name string
		set  map[string]any
		want string
	}{
		{"smoke names an undeclared parameter", map[string]any{"smoke": map[string]any{"parameters": map[string]any{"businessDate": "2026-11-03", "region": "sg"}}}, "smoke.parameters names region"},
		{"dependsOn itself", map[string]any{"dependsOn": []any{map[string]any{"batchJob": "importer"}}}, "names importer itself"},
		{"dependsOn itself, namespace spelled out", map[string]any{"dependsOn": []any{map[string]any{"batchJob": "importer", "namespace": "prod"}}}, "names importer itself"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := bjRender(t, bjWithParams(sched, c.set), bjCluster(t), nil); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("rendered without the expected refusal %q: %v", c.want, err)
			}
		})
	}
	// A same-date dependency needs a businessDate the observer can set.
	noDate := bjWithParams(sched, map[string]any{
		"parameters": []any{map[string]any{"name": "source"}},
		"dependsOn":  []any{map[string]any{"batchJob": "extract"}},
	})
	if _, err := bjRender(t, noDate, bjCluster(t), nil); err == nil || !strings.Contains(err.Error(), "declares no businessDate parameter") {
		t.Errorf("a same-date dependency without businessDate was accepted: %v", err)
	}
	for name, set := range map[string]map[string]any{
		"a dependency on a job elsewhere": {"dependsOn": []any{map[string]any{"batchJob": "importer", "namespace": "test"}, map[string]any{"batchJob": "extract"}}},
		"declared smoke parameters":       {"smoke": map[string]any{"parameters": map[string]any{"businessDate": "2026-11-03", "dryRun": "true"}}},
		"finishBy and its zone":           {"sla": map[string]any{"maxDuration": "3h", "finishBy": "06:30", "timeZone": "Asia/Singapore"}},
	} {
		if _, err := bjRender(t, bjWithParams(sched, set), bjCluster(t), nil); err != nil {
			t.Errorf("%s was refused: %v", name, err)
		}
	}
	// sameBusinessDate: false still starts the run FOR a business date: the observer
	// (createDependencyRun) refuses to start one without a date to carry, so the
	// definition is refused rather than accepted and never run.
	latest := bjWithParams(sched, map[string]any{
		"parameters": []any{map[string]any{"name": "source"}},
		"dependsOn":  []any{map[string]any{"batchJob": "extract", "sameBusinessDate": false}},
	})
	if _, err := bjRender(t, latest, bjCluster(t), nil); err == nil || !strings.Contains(err.Error(), "declares no businessDate parameter") {
		t.Errorf("a dependency on a job with no business-date parameter was accepted: %v", err)
	}
}

const calFixtureDir = "testdata/batchjob-calendar"

// calGoldens: the Phase-3 fixture natively, and a gang (Volcano CronJob) following
// the platform's calendar. ADHAR_UPDATE_GOLDEN=1 rewrites them; read the diff first.
var calGoldens = []struct {
	golden string
	render func(t *testing.T) []map[string]any
}{
	{"golden-native-calendar.yaml", func(t *testing.T) []map[string]any {
		return bjMustRender(t, readYAMLDocs(t, filepath.Join(calFixtureDir, "xr-calendar.yaml"))[0],
			append(bjCluster(t), readYAMLDocs(t, filepath.Join(calFixtureDir, "calendars.yaml"))...), nil)
	}},
	{"golden-gang-calendar.yaml", func(t *testing.T) []map[string]any {
		xr := bjWithParams(vcXR(t, "xr-partitioned-scheduled.yaml"), map[string]any{
			"schedule":  map[string]any{"cron": "0 2 * * *", "timeZone": "Asia/Singapore", "calendar": "weekdays"},
			"retention": map[string]any{"days": 180},
		})
		return vcMustRender(t, xr, append(vcCluster(t), calCalendar("adhar-system", "weekdays", "name: weekdays\nweekendDays: [SATURDAY, SUNDAY]\n", true)), nil)
	}},
}

func TestBatchJobCalendarGoldenRenders(t *testing.T) {
	for _, g := range calGoldens {
		t.Run(g.golden, func(t *testing.T) {
			got := vcGolden(t, g.render(t))
			path := filepath.Join(calFixtureDir, g.golden)
			if os.Getenv("ADHAR_UPDATE_GOLDEN") == "1" {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (ADHAR_UPDATE_GOLDEN=1 writes it)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s differs from the render (ADHAR_UPDATE_GOLDEN=1 rewrites it; read the diff first):\n%s", g.golden, lineDiff(string(want), string(got)))
			}
		})
	}
}

// The Phase-3 fixture is what a person would apply: every field it sets is one the
// strict XRD defines, down to the nested ones.
func TestThePhaseThreeFixtureUsesOnlyDeclaredFields(t *testing.T) {
	x := readYAMLDocs(t, filepath.Join(controlPlaneDir(t), "xrd/batchjob.xrd.yaml"))[0]
	schema := dig(dig(x, "spec", "versions").([]any)[0].(map[string]any), "schema", "openAPIV3Schema", "properties", "spec", "properties", "parameters").(map[string]any)
	var walk func(path string, v any, s map[string]any)
	walk = func(path string, v any, s map[string]any) {
		if s["x-kubernetes-preserve-unknown-fields"] == true {
			return
		}
		switch val := v.(type) {
		case map[string]any:
			props, _ := s["properties"].(map[string]any)
			extra, _ := s["additionalProperties"].(map[string]any)
			for k, sub := range val {
				ss, ok := props[k].(map[string]any)
				if !ok && extra != nil {
					ss, ok = extra, true
				}
				if !ok {
					t.Errorf("xr-calendar.yaml sets %s.%s, which the XRD does not define", path, k)
					continue
				}
				walk(path+"."+k, sub, ss)
			}
		case []any:
			items, _ := s["items"].(map[string]any)
			for i, e := range val {
				walk(fmt.Sprintf("%s[%d]", path, i), e, items)
			}
		}
	}
	walk("parameters", dig(readYAMLDocs(t, filepath.Join(calFixtureDir, "xr-calendar.yaml"))[0], "spec", "parameters"), schema)
}

// The platform calendar the design names ships with core/adhar-batch, in the shape
// the composition resolves.
func TestThePlatformShipsTheWeekdaysCalendar(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	var found map[string]any
	for _, d := range readYAMLDocs(t, filepath.Join(root, "platform/stack/packages/core/adhar-batch/manifests/calendars.yaml")) {
		if d["kind"] == "ConfigMap" && dig(d, "metadata", "name") == "weekdays" {
			found = d
		}
	}
	if found == nil {
		t.Fatal("core/adhar-batch ships no `weekdays` calendar (D5)")
	}
	if dig(found, "metadata", "namespace") != "adhar-system" || dig(found, "metadata", "labels", "adhar.io/batch-calendar") != "true" {
		t.Errorf("weekdays is %v/%v labelled %v; the composition looks in adhar-system for the calendar label", dig(found, "metadata", "namespace"), dig(found, "metadata", "name"), dig(found, "metadata", "labels"))
	}
	var cal map[string]any
	if err := yaml.Unmarshal([]byte(fmt.Sprint(dig(found, "data", "calendar.yaml"))), &cal); err != nil {
		t.Fatalf("weekdays' calendar.yaml does not parse: %v", err)
	}
	if cal["name"] != "weekdays" || fmt.Sprint(cal["weekendDays"]) != "[SATURDAY SUNDAY]" {
		t.Errorf("weekdays = %v", cal)
	}
	// And it resolves.
	docs := bjMustRender(t, calXR(t, "weekdays"), append(bjCluster(t), found), nil)
	if bjStatus(t, docs)["calendar"] != "adhar-system/weekdays" {
		t.Errorf("the shipped weekdays does not resolve: %v", bjCondition(t, docs, "PrerequisitesReady"))
	}
}

// The real function containers: the calendar is found, copied verbatim and mounted
// by the engine Crossplane runs, not only by this file's template harness.
func TestBatchJobCalendarRendersWithCrossplane(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: skipping `crossplane render`, which starts the function containers")
	}
	bin, err := exec.LookPath("crossplane")
	if err != nil {
		t.Skip("the crossplane CLI is not installed; the in-process renders still ran")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("Docker is not available; `crossplane render` runs each function in a container")
	}
	dir := t.TempDir()
	write := func(name string, docs ...map[string]any) string {
		var parts []string
		for _, d := range docs {
			y, _ := yaml.Marshal(d)
			parts = append(parts, string(y))
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(strings.Join(parts, "---\n")), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	xr := bjWithParams(calXR(t, "settlement-days"), map[string]any{"retention": map[string]any{"days": 14, "keepLast": 5}})
	cluster := append(bjCluster(t), calCalendar("adhar-system", "settlement-days", calCalendarText, true))
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, "render", write("xr.yaml", xr), filepath.Join(controlPlaneDir(t), bjComposition), filepath.Join(bjFixtureDir, "functions.yaml"),
		"--required-resources="+write("required.yaml", cluster...))
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := runRender(cmd); err != nil {
		t.Fatalf("crossplane render: %v\n%s", err, stderr.String())
	}
	var copyCM, runCM, status map[string]any
	for _, d := range strings.Split(stdout.String(), "\n---") {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(d), &m); err != nil || len(m) == 0 {
			continue
		}
		switch dig(m, "metadata", "annotations", "crossplane.io/composition-resource-name") {
		case "calendar":
			copyCM = m
		case "run-template":
			runCM = m
		}
		if m["kind"] == "BatchJob" {
			status, _ = m["status"].(map[string]any)
		}
	}
	if copyCM == nil || dig(copyCM, "data", "calendar.yaml") != calCalendarText {
		t.Fatalf("crossplane render composed the calendar copy %v", copyCM)
	}
	if status["calendar"] != "adhar-system/settlement-days" {
		t.Errorf("status.calendar through crossplane render = %v", status["calendar"])
	}
	var run map[string]any
	if err := yaml.Unmarshal([]byte(fmt.Sprint(dig(runCM, "data", "template.yaml"))), &run); err != nil {
		t.Fatal(err)
	}
	env := bjEnv(t, bjContainer(t, run))
	for k, v := range map[string]string{"ADHAR_BATCH_CALENDAR": "/etc/adhar/batch/calendar/calendar.yaml", "ADHAR_BATCH_RETENTION_DAYS": "14", "ADHAR_BATCH_RETENTION_KEEP_LAST": "5"} {
		if env[k] != v {
			t.Errorf("through crossplane render %s = %v, want %s", k, env[k], v)
		}
	}
	// The same object the in-process render builds.
	inProcess := bjRunTemplate(t, bjMustRender(t, xr, cluster, nil))
	a, _ := json.Marshal(run)
	b, _ := json.Marshal(inProcess)
	if string(a) != string(b) {
		t.Errorf("the run template differs between crossplane render and the in-process render:\n%s\n%s", a, b)
	}
}
