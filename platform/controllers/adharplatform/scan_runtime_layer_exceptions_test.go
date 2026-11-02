package adharplatform

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// Runtime-layer exceptions: build tools that arrive inside a runtime's LAUNCH
// layer and that the running application never executes —
//
//	class (2) the npm CLI node-engine bundles next to node, excused by
//	          Trivy's ignore.yaml at its exact path;
//	class (3) the pip inside the prebuilt CPython, excused by runtime-layers.yaml
//	          in trivy-scan's evaluate step, because Trivy reports it from pip's
//	          own SBOM with no path to scope by.
//
// Both refused every Node and Python template image (node-svc, deno-func,
// py-service on AWS, 2026-10-31; re-measured for every template on 2026-11-03).
// The user authorised exceptions ONLY where nothing removes the findings —
// no Node, npm, CPython or pip release does yet, and no buildpack setting keeps
// npm or pip out of the launch layer — and only narrow, justified and short.
// These tests hold each of those words:
//
//   - narrow: an exact id at an exact package location, never a severity, a
//     package type or a glob, and never a match on the application's own copy;
//   - justified: why the running image cannot reach it, and which upstream
//     release removes it;
//   - short: each expires no more than 30 days after the review recorded on
//     the ConfigMap, and once expired the finding fails the gate again.

const (
	runtimeLayerReviewAnnotation = "adhar.io/runtime-layer-exceptions-reviewed"
	runtimeLayerMaxDays          = 30
	// The only layer class (3) may excuse a finding in: the prebuilt CPython,
	// whose site-packages carry pip.
	cpythonLaunchLayer = "paketo-buildpacks/cpython/cpython"
)

// Pinned: excusing one more finding is a decision a reviewer must see here,
// not a line slipped into a ConfigMap.
var runtimeLayerExceptionIDs = map[string][]string{
	"npm (class 2, ignore.yaml)": {
		"CVE-2026-102276", "CVE-2026-102278", "CVE-2026-109383",
		"CVE-2026-109869", "CVE-2026-19534", "CVE-2026-87018",
	},
	"pip (class 3, runtime-layers.yaml)": {
		"CVE-2025-47273", "CVE-2026-114613", "CVE-2026-97687",
		"CVE-2026-97689", "GHSA-6v7p-g79w-8964",
	},
}

type npmRuntimeException struct {
	ID        string   `yaml:"id"`
	Paths     []string `yaml:"paths"`
	PURLs     []string `yaml:"purls"`
	Statement string   `yaml:"statement"`
	ExpiredAt string   `yaml:"expired_at"`
}

type layerRuntimeException struct {
	ID        string  `yaml:"id" json:"id"`
	PURL      string  `yaml:"purl" json:"purl"`
	Layer     string  `yaml:"layer" json:"layer"`
	PkgPath   *string `yaml:"pkgPath" json:"pkgPath"`
	Statement string  `yaml:"statement" json:"statement"`
	Upstream  string  `yaml:"upstream" json:"upstream"`
	ExpiredAt string  `yaml:"expired_at" json:"expired_at"`
}

type runtimeLayerExceptions struct {
	reviewed  time.Time
	npm       []npmRuntimeException   // ignore.yaml entries scoped by path
	layers    []layerRuntimeException // runtime-layers.yaml
	layersRaw string
}

func loadRuntimeLayerExceptions(t *testing.T) runtimeLayerExceptions {
	t.Helper()
	cm := findDoc(t, docsOf(t, supplyChain+"30-scan-verify-pipeline.yaml"), "ConfigMap", "trivy-buildpack-exceptions")
	var out runtimeLayerExceptions
	reviewed, _ := sgDig(cm, "metadata", "annotations", runtimeLayerReviewAnnotation).(string)
	r, err := time.Parse("2006-01-02", reviewed)
	if err != nil {
		t.Fatalf("trivy-buildpack-exceptions carries no parseable %s annotation (%q): nothing records when "+
			"the runtime-layer exceptions were last re-measured, so nothing bounds how long they live",
			runtimeLayerReviewAnnotation, reviewed)
	}
	out.reviewed = r

	ignore, _ := sgDig(cm, "data", "ignore.yaml").(string)
	var ign struct {
		Vulnerabilities []npmRuntimeException `yaml:"vulnerabilities"`
	}
	if err := yaml.Unmarshal([]byte(ignore), &ign); err != nil {
		t.Fatalf("ignore.yaml does not parse: %v", err)
	}
	for _, v := range ign.Vulnerabilities {
		if len(v.Paths) > 0 {
			out.npm = append(out.npm, v)
		}
	}

	out.layersRaw, _ = sgDig(cm, "data", "runtime-layers.yaml").(string)
	var rl struct {
		Exceptions []layerRuntimeException `yaml:"exceptions"`
	}
	if err := yaml.Unmarshal([]byte(out.layersRaw), &rl); err != nil {
		t.Fatalf("runtime-layers.yaml does not parse (%v); the evaluate step would apply nothing and every "+
			"Python image would fail on pip's vendored packages", err)
	}
	out.layers = rl.Exceptions
	return out
}

var (
	advisoryID = regexp.MustCompile(`^(CVE-\d{4}-\d{4,}|GHSA(-[23456789cfghjmpqrvwx]{4}){3})$`)
	// One package.json directly under npm's own node_modules: no nesting (a
	// dependency of a dependency is a different copy) and no glob.
	npmPackageJSON = regexp.MustCompile(`^(@[a-z0-9._-]+/)?[a-z0-9._-]+/package\.json$`)
	// pkg:pypi/<name>@<exact version>, nothing else: no qualifiers, no subpath,
	// no range.
	exactPypiPURL = regexp.MustCompile(`^pkg:pypi/[a-z0-9._-]+@[0-9][0-9A-Za-z.+!-]*$`)
)

// Every runtime-layer exception names exactly one finding at exactly one
// location, says why the running image cannot reach it and which upstream
// release ends it, and lapses within 30 days of the last review.
func TestRuntimeLayerExceptionsAreNarrowJustifiedAndShortLived(t *testing.T) {
	ex := loadRuntimeLayerExceptions(t)
	// A day of slack: "today" in the reviewer's time zone may be tomorrow in UTC.
	if ex.reviewed.After(time.Now().Add(24 * time.Hour)) {
		t.Errorf("%s is %s, in the future: a review cannot be dated ahead to stretch the 30-day box",
			runtimeLayerReviewAnnotation, ex.reviewed.Format("2006-01-02"))
	}
	limit := ex.reviewed.AddDate(0, 0, runtimeLayerMaxDays)
	box := func(id, expiredAt string) {
		exp, err := time.Parse("2006-01-02", expiredAt)
		if err != nil {
			t.Errorf("%s has no parseable expired_at (%q); an exception without an end outlives its reason", id, expiredAt)
			return
		}
		if exp.After(limit) {
			t.Errorf("%s is excused until %s, more than %d days after the review of %s; re-measure and "+
				"move the review date instead of stretching the box", id, expiredAt, runtimeLayerMaxDays,
				ex.reviewed.Format("2006-01-02"))
		}
		if !exp.After(ex.reviewed) {
			t.Errorf("%s expired (%s) before the review that granted it (%s)", id, expiredAt, ex.reviewed.Format("2006-01-02"))
		}
	}

	var npmIDs []string
	seenNpm := map[string]bool{}
	for _, v := range ex.npm {
		npmIDs = append(npmIDs, v.ID)
		if !advisoryID.MatchString(v.ID) {
			t.Errorf("%q is not one advisory id; an exception names exactly one finding", v.ID)
		}
		if len(v.Paths) != 1 || len(v.PURLs) != 0 {
			t.Errorf("%s has %d paths and %d purls; an npm exception names exactly ONE location, its path",
				v.ID, len(v.Paths), len(v.PURLs))
		}
		for _, p := range v.Paths {
			rest, ok := strings.CutPrefix(p, npmBundledInNodeEngine)
			if !ok || !npmPackageJSON.MatchString(rest) {
				t.Errorf("%s is scoped to %q; it may only name one package.json directly under %s — the "+
					"npm CLI node-engine bundles — with no glob and no nesting", v.ID, p, npmBundledInNodeEngine)
			}
			if seenNpm[v.ID+" "+p] {
				t.Errorf("%s at %s is listed twice", v.ID, p)
			}
			seenNpm[v.ID+" "+p] = true
		}
		s := strings.Join(strings.Fields(v.Statement), " ")
		if len(s) < 80 || !strings.Contains(s, "npm") || !(strings.Contains(s, "never") || strings.Contains(s, "not ")) {
			t.Errorf("%s: %q does not say why the running image cannot reach npm's copy", v.ID, s)
		}
		if !strings.Contains(s, "Fixed in") {
			t.Errorf("%s: the statement must name the upstream release that removes it (\"Fixed in …\")", v.ID)
		}
		box(v.ID, v.ExpiredAt)
	}

	var pipIDs []string
	seenLayer := map[string]bool{}
	for _, v := range ex.layers {
		pipIDs = append(pipIDs, v.ID)
		if !advisoryID.MatchString(v.ID) {
			t.Errorf("%q is not one advisory id; an exception names exactly one finding", v.ID)
		}
		if !exactPypiPURL.MatchString(v.PURL) {
			t.Errorf("%s is scoped to purl %q; a runtime-layer exception names one package at one exact "+
				"version, never a package name or a type", v.ID, v.PURL)
		}
		if v.Layer != cpythonLaunchLayer {
			t.Errorf("%s is scoped to layer %q; only %s — the prebuilt CPython that carries pip — may hold "+
				"an excused build tool, never an application layer", v.ID, v.Layer, cpythonLaunchLayer)
		}
		if v.PkgPath == nil || *v.PkgPath != "" {
			t.Errorf("%s must state pkgPath: \"\" — the finding comes from pip's own SBOM and has no file; a "+
				"finding WITH a path belongs in ignore.yaml, scoped by that path", v.ID)
		}
		key := v.ID + " " + v.PURL + " " + v.Layer
		if seenLayer[key] {
			t.Errorf("%s (%s in %s) is listed twice", v.ID, v.PURL, v.Layer)
		}
		seenLayer[key] = true
		s := strings.Join(strings.Fields(v.Statement), " ")
		if len(s) < 80 || !strings.Contains(s, "pip") || !(strings.Contains(s, "never") || strings.Contains(s, "not ")) {
			t.Errorf("%s: %q does not say why the running image cannot reach pip's copy", v.ID, s)
		}
		if u := strings.Join(strings.Fields(v.Upstream), " "); len(u) < 40 || !strings.Contains(u, "release") {
			t.Errorf("%s: upstream %q must name the release that removes it", v.ID, u)
		}
		box(v.ID, v.ExpiredAt)
	}

	for class, got := range map[string][]string{
		"npm (class 2, ignore.yaml)":         npmIDs,
		"pip (class 3, runtime-layers.yaml)": pipIDs,
	} {
		got = slices.Clone(got)
		slices.Sort(got)
		want := slices.Clone(runtimeLayerExceptionIDs[class])
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s excuses %v, the reviewed set is %v: add or drop one here, deliberately", class, got, want)
		}
	}

	// A pip finding scoped by purl in ignore.yaml would be excused in the
	// application's own layers too — exactly what runtime-layers.yaml avoids.
	cm := findDoc(t, docsOf(t, supplyChain+"30-scan-verify-pipeline.yaml"), "ConfigMap", "trivy-buildpack-exceptions")
	if ignore, _ := sgDig(cm, "data", "ignore.yaml").(string); strings.Contains(ignore, "pkg:pypi/") ||
		strings.Contains(ignore, "paketo-buildpacks_cpython") {
		t.Error("ignore.yaml excuses a Python package; Trivy can only scope those by purl, which reaches the " +
			"application's own copy — they belong in runtime-layers.yaml")
	}
}

// --- running the platform's own jq programs --------------------------------

// The DiffIDs a synthetic report uses, mapped to their CNB layers through the
// same io.buildpacks.lifecycle.metadata label the real image carries.
const (
	diffCPython    = "sha256:1f26cb3aea866fdb0de5f55551524e379dd0d9dc600724fc48cb62610f339699"
	diffPipInstall = "sha256:d54f92ed9ad5dd7bab6e2a3cca837ff0b61c4f1a960a32fa364d0bb7fea2d1a5"
	diffApp        = "sha256:5a825ea80e6fd5e94daaec8343b3041ba7adf0882f7ef10ecfa442ba6660bdb9"
)

func lifecycleLabel() string {
	b, _ := json.Marshal(map[string]any{
		"buildpacks": []any{
			map[string]any{"key": "paketo-buildpacks/cpython", "layers": map[string]any{
				"cpython": map[string]any{"sha": diffCPython, "launch": true, "build": true}}},
			map[string]any{"key": "paketo-buildpacks/pip-install", "layers": map[string]any{
				"packages": map[string]any{"sha": diffPipInstall, "launch": true}}},
		},
		"app": []any{map[string]any{"sha": diffApp}},
	})
	return string(b)
}

type synthFinding struct {
	id, name, version, purl, pkgPath, diffID string
}

func synthReport(withLabel bool, fs []synthFinding) map[string]any {
	var vulns []any
	for _, f := range fs {
		v := map[string]any{
			"VulnerabilityID": f.id, "PkgName": f.name, "InstalledVersion": f.version,
			"PkgIdentifier": map[string]any{"PURL": f.purl}, "Severity": "HIGH",
			"Layer": map[string]any{"DiffID": f.diffID},
		}
		if f.pkgPath != "" {
			v["PkgPath"] = f.pkgPath
		}
		vulns = append(vulns, v)
	}
	labels := map[string]any{}
	if withLabel {
		labels["io.buildpacks.lifecycle.metadata"] = lifecycleLabel()
	}
	return map[string]any{
		"Metadata": map[string]any{"ImageConfig": map[string]any{"config": map[string]any{"Labels": labels}}},
		"Results":  []any{map[string]any{"Target": "Python", "Class": "lang-pkgs", "Type": "python-pkg", "Vulnerabilities": vulns}},
	}
}

type scanOutcome struct {
	report  map[string]any
	verdict string
	waived  int
	failing int
	left    []string // "<id> <purl> <pkgPath> <diffID>" still judged
}

// applyRuntimeLayerExceptions runs runtime-layer-exceptions.jq and then
// evaluate.jq, both read from the manifests, exactly as trivy-scan's evaluate
// step chains them.
func applyRuntimeLayerExceptions(t *testing.T, rlJSON []byte, report map[string]any, now time.Time) scanOutcome {
	t.Helper()
	lib := findDoc(t, docsOf(t, supplyChain+"74-scan-governance.yaml"), "ConfigMap", "adhar-scan-lib")
	dir := t.TempDir()
	write := func(name string, body []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, body, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	prog, _ := sgDig(lib, "data", "runtime-layer-exceptions.jq").(string)
	eval, _ := sgDig(lib, "data", "evaluate.jq").(string)
	if strings.TrimSpace(prog) == "" || strings.TrimSpace(eval) == "" {
		t.Fatal("adhar-scan-lib lacks runtime-layer-exceptions.jq or evaluate.jq")
	}
	rep, _ := json.Marshal(report)
	in := write("report.json", rep)
	out, err := exec.Command("jq", "--slurpfile", "rl", write("rl.json", rlJSON),
		"--argjson", "now", strconv.FormatInt(now.Unix(), 10),
		"-f", write("rle.jq", []byte(prog)), in).CombinedOutput()
	if err != nil {
		t.Fatalf("runtime-layer-exceptions.jq failed: %v\n%s", err, out)
	}
	applied := write("applied.json", out)
	sum, err := exec.Command("jq", "-c", "-s", "--arg", "fail", "HIGH,CRITICAL", "--arg", "deny", "", "--arg", "warn", "",
		"-f", write("evaluate.jq", []byte(eval)), applied).CombinedOutput()
	if err != nil {
		t.Fatalf("evaluate.jq failed: %v\n%s", err, sum)
	}
	var res scanOutcome
	if err := json.Unmarshal(out, &res.report); err != nil {
		t.Fatalf("runtime-layer-exceptions.jq did not emit a report: %v\n%s", err, out)
	}
	var s struct {
		Verdict string `json:"verdict"`
		Waived  int    `json:"waived"`
		Failing struct {
			Vulnerabilities int `json:"vulnerabilities"`
		} `json:"failing"`
	}
	if err := json.Unmarshal(sum, &s); err != nil {
		t.Fatalf("evaluate.jq output: %v\n%s", err, sum)
	}
	res.verdict, res.waived, res.failing = s.Verdict, s.Waived, s.Failing.Vulnerabilities
	for _, r := range res.report["Results"].([]any) {
		vs, _ := r.(map[string]any)["Vulnerabilities"].([]any)
		for _, v := range vs {
			m := v.(map[string]any)
			p, _ := m["PkgPath"].(string)
			res.left = append(res.left, strings.Join([]string{m["VulnerabilityID"].(string),
				sgDig(m, "PkgIdentifier", "PURL").(string), p, sgDig(m, "Layer", "DiffID").(string)}, " "))
		}
	}
	return res
}

func runtimeLayersJSON(t *testing.T, ex runtimeLayerExceptions) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"exceptions": ex.layers})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func pipFinding(e layerRuntimeException, purl, pkgPath, diff string) synthFinding {
	name, version, _ := strings.Cut(strings.TrimPrefix(purl, "pkg:pypi/"), "@")
	return synthFinding{id: e.ID, name: name, version: version, purl: purl, pkgPath: pkgPath, diffID: diff}
}

// The pip copy is waived — still in the report, counted, judged as not failing
// — while the exception lives, and fails the gate again the moment it expires.
func TestAnExpiredRuntimeLayerExceptionFailsTheGateAgain(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed")
	}
	ex := loadRuntimeLayerExceptions(t)
	if len(ex.layers) == 0 {
		t.Fatal("runtime-layers.yaml lists nothing; this guard is checking nothing")
	}
	var fs []synthFinding
	for _, e := range ex.layers {
		fs = append(fs, pipFinding(e, e.PURL, "", diffCPython))
	}
	rl := runtimeLayersJSON(t, ex)

	live := applyRuntimeLayerExceptions(t, rl, synthReport(true, fs), ex.reviewed.Add(time.Hour))
	if live.verdict != "pass" || live.waived != len(fs) || live.failing != 0 {
		t.Fatalf("with every exception live, pip's vendored copies should all be waived: verdict=%s waived=%d "+
			"failing=%d left=%v", live.verdict, live.waived, live.failing, live.left)
	}
	for _, r := range live.report["Results"].([]any) {
		mod, _ := r.(map[string]any)["ExperimentalModifiedFindings"].([]any)
		for _, m := range mod {
			mf := m.(map[string]any)
			if mf["Status"] != "not_affected" || strings.TrimSpace(mf["Statement"].(string)) == "" ||
				!strings.Contains(mf["Source"].(string), "runtime-layers.yaml") || !strings.Contains(mf["Source"].(string), "expires") {
				t.Errorf("a waived finding must say what excused it and until when: %v", mf)
			}
			if sgDig(mf, "Finding", "VulnerabilityID") == nil {
				t.Errorf("a waived finding lost the finding itself: %v", mf)
			}
		}
	}

	for _, e := range ex.layers {
		exp, _ := time.Parse("2006-01-02", e.ExpiredAt)
		after := applyRuntimeLayerExceptions(t, rl, synthReport(true, []synthFinding{pipFinding(e, e.PURL, "", diffCPython)}), exp.Add(time.Second))
		if after.verdict != "fail" || after.failing != 1 || after.waived != 0 {
			t.Errorf("%s still excused after its expiry %s (verdict=%s waived=%d): an expired exception must "+
				"fail the gate again", e.ID, e.ExpiredAt, after.verdict, after.waived)
		}
	}

	// Without a date nothing is excused: an undated entry is not a forever entry.
	var undated []map[string]any
	for _, e := range ex.layers {
		b, _ := json.Marshal(e)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		delete(m, "expired_at")
		undated = append(undated, m)
	}
	rlUndated, _ := json.Marshal(map[string]any{"exceptions": undated})
	if got := applyRuntimeLayerExceptions(t, rlUndated, synthReport(true, fs), ex.reviewed); got.waived != 0 {
		t.Errorf("an entry with no expired_at excused %d finding(s); it must excuse nothing", got.waived)
	}

	// Class (2) is applied by Trivy itself, which drops an ignore entry whose
	// expired_at is before now (pkg/result/ignore.go, Prune) and matches the
	// rest on id AND a path equal to the finding's target or PkgPath. Checked
	// against trivy 0.58.1 on 2026-11-03: re-dating the two undici entries to
	// the day before brought both findings back and failed the gate. The same
	// rule, here, for every class (2) entry: excused at the review, judged
	// again the second its date passes.
	for _, v := range ex.npm {
		exp, err := time.Parse("2006-01-02", v.ExpiredAt)
		if err != nil {
			continue // the shape test reports it
		}
		for _, p := range v.Paths {
			if !trivyIgnores(ex.npm, v.ID, "Node.js", p, ex.reviewed) {
				t.Errorf("%s at %s is not excused at its own review; the entry does nothing", v.ID, p)
			}
			if trivyIgnores(ex.npm, v.ID, "Node.js", p, exp.Add(time.Second)) {
				t.Errorf("%s at %s is still excused after %s", v.ID, p, v.ExpiredAt)
			}
		}
	}
}

// trivyIgnores is Trivy 0.58.1's rule for a path-scoped ignore entry: pruned
// once expired_at is before now, otherwise matched on the id and on a path
// equal (no glob is allowed here) to the result's target or the PkgPath.
func trivyIgnores(entries []npmRuntimeException, id, target, pkgPath string, now time.Time) bool {
	for _, e := range entries {
		if exp, err := time.Parse("2006-01-02", e.ExpiredAt); err == nil && exp.Before(now) {
			continue
		}
		if e.ID == id && (slices.Contains(e.Paths, target) || slices.Contains(e.Paths, pkgPath)) {
			return true
		}
	}
	return false
}

// The same advisory on the same package anywhere the APPLICATION put it — its
// pip-install layer, its own layer, CPython's site-packages, another version,
// an image without the CNB label; its node_modules, a nested copy, corepack's —
// is still judged and still fails.
func TestNoRuntimeLayerExceptionExcusesAnApplicationDependency(t *testing.T) {
	ex := loadRuntimeLayerExceptions(t)
	for _, v := range ex.npm {
		for _, p := range v.Paths {
			pkg := strings.TrimSuffix(strings.TrimPrefix(p, npmBundledInNodeEngine), "/package.json")
			for _, own := range []string{
				"layers/paketo-buildpacks_npm-install/launch-modules/node_modules/" + pkg + "/package.json",
				"layers/paketo-buildpacks_npm-install/build-modules/node_modules/" + pkg + "/package.json",
				"workspace/source/node_modules/" + pkg + "/package.json",
				"layers/paketo-buildpacks_node-engine/node/lib/node_modules/corepack/node_modules/" + pkg + "/package.json",
				npmBundledInNodeEngine + "some-dependency/node_modules/" + pkg + "/package.json",
			} {
				if trivyIgnores(ex.npm, v.ID, "Node.js", own, ex.reviewed) {
					t.Errorf("%s at %s is excused; only node-engine's own npm copy may be", v.ID, own)
				}
			}
		}
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed")
	}
	rl := runtimeLayersJSON(t, ex)
	now := ex.reviewed.Add(time.Hour)
	for _, e := range ex.layers {
		name, version, _ := strings.Cut(strings.TrimPrefix(e.PURL, "pkg:pypi/"), "@")
		pip := pipFinding(e, e.PURL, "", diffCPython)
		own := map[string]synthFinding{
			"the application's pip-install layer": pipFinding(e, e.PURL,
				"layers/paketo-buildpacks_pip-install/packages/lib/python3.12/site-packages/"+name+"-"+version+".dist-info/METADATA", diffPipInstall),
			"an SBOM the application ships in its own layer": pipFinding(e, e.PURL, "", diffApp),
			"a package installed into CPython's own site-packages": pipFinding(e, e.PURL,
				"layers/paketo-buildpacks_cpython/cpython/lib/python3.12/site-packages/"+name+"-"+version+".dist-info/METADATA", diffCPython),
			"another version in pip's own place":      pipFinding(e, "pkg:pypi/"+name+"@"+version+".post1", "", diffCPython),
			"a layer the image's label does not know": pipFinding(e, e.PURL, "", "sha256:"+strings.Repeat("0", 64)),
		}
		for where, f := range own {
			got := applyRuntimeLayerExceptions(t, rl, synthReport(true, []synthFinding{pip, f}), now)
			if got.failing != 1 || got.waived != 1 || len(got.left) != 1 ||
				!strings.Contains(got.left[0], f.purl+" "+f.pkgPath+" "+f.diffID) {
				t.Errorf("%s (%s) in %s: want only pip's copy waived and this one still failing; got waived=%d "+
					"failing=%d left=%v", e.ID, f.purl, where, got.waived, got.failing, got.left)
			}
		}
		if got := applyRuntimeLayerExceptions(t, rl, synthReport(false, []synthFinding{pip}), now); got.waived != 0 || got.failing != 1 {
			t.Errorf("%s was excused in an image with no io.buildpacks.lifecycle.metadata label; with no way to "+
				"tell which layer holds it, it must be judged", e.ID)
		}
	}
}

// trivy-scan applies the runtime-layer exceptions itself, before any waiver
// and before judging, fails closed, and shows what it waived.
func TestTheImageScanAppliesRuntimeLayerExceptionsBeforeJudging(t *testing.T) {
	scan := findDoc(t, docsOf(t, supplyChain+"30-scan-verify-pipeline.yaml"), "Task", "trivy-scan")
	ev := stepScript(t, scan, "evaluate")
	rle := strings.Index(ev, "-f /lib/adhar-scan/runtime-layer-exceptions.jq")
	dw := strings.Index(ev, "-f /lib/adhar-scan/digest-waivers.jq")
	judge := strings.Index(ev, "-f /lib/adhar-scan/evaluate.jq")
	if rle < 0 || dw < 0 || judge < 0 || rle > dw || dw > judge {
		t.Errorf("evaluate must apply runtime-layer-exceptions.jq, then the digest-scoped waivers, then judge "+
			"(positions %d, %d, %d)", rle, dw, judge)
	}
	for _, want := range []string{
		"RL=/etc/trivy/runtime-layers.yaml",
		`yq -o=json '.' "$RL" > /work/rl.json`,
		`--argjson now "$(date +%s)"`,
		`mv /work/rl-applied.json "$R"`,
		"WARNING: runtime-layer exceptions could not be applied; none are in effect",
		"-f /lib/adhar-scan/waived.jq",
	} {
		if !strings.Contains(ev, want) {
			t.Errorf("trivy-scan/evaluate lacks %q", want)
		}
	}
	// The report is only replaced when the program succeeded: a failure must
	// leave every finding where it was (the gate gets stricter, never looser).
	if !regexp.MustCompile(`> /work/rl-applied\.json; then\s+mv /work/rl-applied\.json "\$R"\s+else\s+echo "WARNING: runtime-layer exceptions could not be applied`).MatchString(ev) {
		t.Error("the runtime-layer exceptions must replace the report only when they were applied successfully")
	}
	mounted := false
	for _, s := range sgDig(scan, "spec", "steps").([]any) {
		if sgDig(s, "name") != "evaluate" {
			continue
		}
		for _, m := range sgDig(s, "volumeMounts").([]any) {
			if sgDig(m, "name") == "trivy-exceptions" && sgDig(m, "mountPath") == "/etc/trivy" {
				mounted = true
			}
		}
	}
	if !mounted {
		t.Error("trivy-scan/evaluate does not mount trivy-buildpack-exceptions at /etc/trivy, so runtime-layers.yaml " +
			"is never read and every Python image fails on pip's vendored packages")
	}
	lib := findDoc(t, docsOf(t, supplyChain+"74-scan-governance.yaml"), "ConfigMap", "adhar-scan-lib")
	if e, _ := sgDig(lib, "data", "evaluate.jq").(string); !strings.Contains(e, "[$R[] | .ExperimentalModifiedFindings[]?] as $x") ||
		!strings.Contains(e, "waived: ($x | length)") {
		t.Error("evaluate.jq must count ExperimentalModifiedFindings as waived: an excused finding is reported, not hidden")
	}
}
