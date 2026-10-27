package adharplatform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// The image-scan gate excuses exactly two finding classes, both in layers a
// buildpack wrote, and each must stay narrow, justified and time-boxed.
//
// (1) Buildpack-built images carry Go binaries the platform did not write: the
// CNB launcher and one exec.d helper per buildpack. They read environment
// variables, print a config line and exit — they never serve HTTP or perform a
// TLS handshake, so the Go stdlib server-side CVEs are unreachable in them. They
// are also unfixable: Paketo pins an exact Go patch in go.mod and Go 1.27.2 was
// published two days after the newest buildpack release.
//
// Without the exception the gate refused a React app image whose only findings
// were 12 instances of those three CVEs, all HIGH, zero CRITICAL. With it, that
// image passes and the Java service still fails on nine CRITICAL dependency
// vulnerabilities — which is the behaviour this test pins.
//
// (2) node-engine installs the npm CLI next to node in the Node.js LAUNCH layer.
// The image runs node, never npm, and no Node release bundles an npm free of the
// findings (test-func, 2026-10-27: five HIGH, all under node-engine's npm). These
// entries are scoped by exact PATH to that npm, so the same packages anywhere
// the application put them are still gated; TestTheNpmExceptionCannot…, below,
// pins that.
func TestTheScanGateExcusesOnlyUnreachableBuildpackCVEs(t *testing.T) {
	path := filepath.Join(stackRoot(t), "packages", "application", "adhar-supply-chain",
		"manifests", "30-scan-verify-pipeline.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	raw := string(b)

	// The gate itself must not be weakened. Since scan governance the image
	// task MEASURES (one JSON report, every severity) and its evaluate step
	// JUDGES; standalone — every pipeline but app-ci — it must still fail on
	// HIGH/CRITICAL with nothing but the buildpack exceptions applied.
	for _, forbidden := range []string{"--exit-code 0", "--severity CRITICAL\""} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("the scan gate uses %q; the exception must be a narrow allow-list, never a "+
				"blanket downgrade of what the gate reports", forbidden)
		}
	}
	// --ignore-unfixed is a project POLICY now, off unless a reviewer turns it
	// on — never a hardcoded flag on the scan.
	if strings.Count(raw, "--ignore-unfixed") != 1 ||
		!strings.Contains(raw, `[ "$(params.ignoreUnfixed)" = "true" ] && UNFIXED="--ignore-unfixed"`) ||
		!strings.Contains(raw, `{name: ignoreUnfixed, type: string, default: "false"}`) {
		t.Error("--ignore-unfixed must only come from the ignoreUnfixed param, which defaults to false")
	}
	for _, want := range []string{
		`{name: severity, type: string, default: "HIGH,CRITICAL"}`,
		`{name: blocking, type: string, default: "true"}`,
		`if [ "$VERDICT" = fail ] && [ "$(params.blocking)" = "true" ]; then`,
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("the scan no longer fails the pipeline by default (missing %q); a gate that only "+
				"reports is a dashboard", want)
		}
	}
	if !strings.Contains(raw, "IGNORE=/etc/trivy/ignore.yaml") || !strings.Contains(raw, `--ignorefile "$IGNORE"`) {
		t.Fatal("the scan does not read the exception file, so the buildpack CVEs still fail every " +
			"buildpack-built image and nobody can clear them")
	}

	// Parse the exception list out of the ConfigMap.
	type entry struct {
		ID        string   `yaml:"id"`
		PURLs     []string `yaml:"purls"`
		Paths     []string `yaml:"paths"`
		Statement string   `yaml:"statement"`
		ExpiredAt string   `yaml:"expired_at"`
	}
	var found bool
	for _, doc := range strings.Split(raw, "\n---\n") {
		var cm struct {
			Kind     string                `yaml:"kind"`
			Metadata struct{ Name string } `yaml:"metadata"`
			Data     map[string]string     `yaml:"data"`
		}
		if yaml.Unmarshal([]byte(doc), &cm) != nil || cm.Kind != "ConfigMap" {
			continue
		}
		if cm.Metadata.Name != "trivy-buildpack-exceptions" {
			continue
		}
		found = true
		var ign struct {
			Vulnerabilities []entry `yaml:"vulnerabilities"`
		}
		if err := yaml.Unmarshal([]byte(cm.Data["ignore.yaml"]), &ign); err != nil {
			t.Fatalf("the exception file is not valid YAML (%v); trivy would ignore it silently and "+
				"the gate would keep failing with no clue why", err)
		}
		if len(ign.Vulnerabilities) == 0 {
			t.Fatal("the exception file lists nothing; this guard is checking nothing")
		}
		for _, v := range ign.Vulnerabilities {
			// Scoped: never a bare CVE id, which would excuse it everywhere.
			if len(v.PURLs) == 0 && len(v.Paths) == 0 {
				t.Errorf("%s is excused with no purl or path scope, so it is suppressed anywhere it "+
					"appears, including in application code", v.ID)
			}
			// A path scope may only name node-engine's bundled npm (class 2),
			// exactly: Trivy matches `paths` as doublestar globs, so a `*` or
			// `**` could reach the application's own node_modules.
			for _, p := range v.Paths {
				if !strings.HasPrefix(p, npmBundledInNodeEngine) || strings.ContainsAny(p, "*?[]{}\\") ||
					strings.Contains(p, "..") || !strings.HasSuffix(p, "/package.json") {
					t.Errorf("%s is scoped to path %q; a path exception may only name one package.json "+
						"under %s — the npm CLI node-engine bundles, which the image never runs — "+
						"exactly, with no glob", v.ID, p, npmBundledInNodeEngine)
				}
			}
			// Only Go packages that ship INSIDE buildpack binaries may be
			// excused. x/net is here because the bellsoft-liberica helper
			// vendors it and Trivy reports it as its own package, so a
			// stdlib-only scope leaves one finding and the gate stays red.
			allowed := []string{"pkg:golang/stdlib", "pkg:golang/golang.org/x/net"}
			for _, p := range v.PURLs {
				ok := false
				for _, a := range allowed {
					if strings.HasPrefix(p, a) {
						ok = true
					}
				}
				if !ok {
					t.Errorf("%s is scoped to purl %q; only %v may be excused here, because those are "+
						"the Go packages compiled into buildpack-provided binaries", v.ID, p, allowed)
				}
			}
			// Justified, so a reader knows why it was accepted.
			if len(strings.TrimSpace(v.Statement)) < 40 {
				t.Errorf("%s has no real justification (%q); an unexplained exception is "+
					"indistinguishable from someone silencing a finding", v.ID, v.Statement)
			}
			// Time-boxed, and the box must not already be open-ended.
			if v.ExpiredAt == "" {
				t.Errorf("%s never expires; the upstream fix is days away and an exception with no "+
					"end date outlives the reason it was granted", v.ID)
				continue
			}
			exp, perr := time.Parse("2006-01-02", v.ExpiredAt)
			if perr != nil {
				t.Errorf("%s has an unparseable expired_at %q; trivy drops a malformed entry and the "+
					"suppression silently stops working", v.ID, v.ExpiredAt)
				continue
			}
			if exp.After(time.Now().AddDate(0, 6, 0)) {
				t.Errorf("%s is excused until %s, more than six months out; that is not a time-box",
					v.ID, v.ExpiredAt)
			}
		}
	}
	if !found {
		t.Fatal("no trivy-buildpack-exceptions ConfigMap; the scan mounts one, so the task would fail " +
			"to start")
	}
}

// npmBundledInNodeEngine is where paketo's node-engine puts the npm CLI it ships
// with node, inside the Node.js launch layer of every Node image.
const npmBundledInNodeEngine = "layers/paketo-buildpacks_node-engine/node/lib/node_modules/npm/node_modules/"

// The npm exception (class 2) excuses npm's bundled copy and nothing else.
//
// Trivy 0.58.1 (pkg/result/ignore.go) excuses a finding when an entry's id is
// the finding's AND one of its `paths` matches — as a doublestar glob — either
// the result's target or the finding's PkgPath. The findings below are the five
// that refused test-func (harbor …/library/test-func@sha256:68b5adbc…, Trivy
// report in its cosign `vuln` attestation); the copies at the other paths are
// what an application's OWN dependency looks like in the same image — npm-install
// puts node_modules in its launch-modules layer, a hand-built image in
// /workspace — plus corepack's, which node-engine also ships. Run against Trivy
// itself on 2026-10-28: the image went from 5 HIGH to 0, and every copy outside
// npm (15 findings) stayed HIGH.
func TestTheNpmExceptionCannotExcuseTheApplicationsOwnPackages(t *testing.T) {
	path := filepath.Join(stackRoot(t), "packages", "application", "adhar-supply-chain",
		"manifests", "30-scan-verify-pipeline.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	type entry struct {
		ID    string   `yaml:"id"`
		Paths []string `yaml:"paths"`
		PURLs []string `yaml:"purls"`
	}
	var entries []entry
	for _, doc := range strings.Split(string(b), "\n---\n") {
		var cm struct {
			Kind     string                `yaml:"kind"`
			Metadata struct{ Name string } `yaml:"metadata"`
			Data     map[string]string     `yaml:"data"`
		}
		if yaml.Unmarshal([]byte(doc), &cm) != nil || cm.Kind != "ConfigMap" || cm.Metadata.Name != "trivy-buildpack-exceptions" {
			continue
		}
		var ign struct {
			Vulnerabilities []entry `yaml:"vulnerabilities"`
		}
		if err := yaml.Unmarshal([]byte(cm.Data["ignore.yaml"]), &ign); err != nil {
			t.Fatal(err)
		}
		entries = ign.Vulnerabilities
	}
	// Trivy's rule: the id matches, AND (no paths, or one matches the target or
	// the PkgPath), AND (no purls, or one matches the package's). Paths are
	// compared exactly, which is what a doublestar pattern without
	// metacharacters does (the test above refuses any other); a purl matches on
	// type and name, and on version only when it names one.
	purlMatches := func(pattern, purl string) bool {
		name := func(s string) (string, string) {
			s = strings.TrimPrefix(s, "pkg:")
			n, v, _ := strings.Cut(s, "@")
			return n, v
		}
		pn, pv := name(pattern)
		fn, fv := name(purl)
		return pn == fn && (pv == "" || pv == fv)
	}
	excused := func(id, target, pkgPath, purl string) bool {
		for _, e := range entries {
			if e.ID != id {
				continue
			}
			pathOK := len(e.Paths) == 0
			for _, p := range e.Paths {
				pathOK = pathOK || p == target || p == pkgPath
			}
			purlOK := len(e.PURLs) == 0
			for _, p := range e.PURLs {
				purlOK = purlOK || purlMatches(p, purl)
			}
			if pathOK && purlOK {
				return true
			}
		}
		return false
	}
	// id → the package and installed version Trivy reported.
	findings := map[string][2]string{
		"CVE-2026-102276": {"brace-expansion", "5.0.9"},
		"CVE-2026-102278": {"brace-expansion", "5.0.9"},
		"CVE-2026-109869": {"undici", "6.28.0"},
		"CVE-2026-19534":  {"undici", "6.28.0"},
		"CVE-2026-93748":  {"http-cache-semantics", "4.2.0"},
	}
	for id, pkg := range findings {
		purl := "pkg:npm/" + pkg[0] + "@" + pkg[1]
		npm := npmBundledInNodeEngine + pkg[0] + "/package.json"
		// Only checked while an entry for the id exists: when the exception is
		// retired, the gate is meant to fail again, not this test.
		hasEntry := false
		for _, e := range entries {
			hasEntry = hasEntry || e.ID == id
		}
		if hasEntry && !excused(id, "Node.js", npm, purl) {
			t.Errorf("%s in node-engine's npm (%s) is not excused by its own entry; the scope does not "+
				"match the PkgPath Trivy reports, so the exception does nothing", id, npm)
		}
		for _, own := range []string{
			"layers/paketo-buildpacks_npm-install/launch-modules/node_modules/" + pkg[0] + "/package.json",
			"workspace/node_modules/" + pkg[0] + "/package.json",
			"layers/paketo-buildpacks_node-engine/node/lib/node_modules/corepack/node_modules/" + pkg[0] + "/package.json",
			npmBundledInNodeEngine + "some-dependency/node_modules/" + pkg[0] + "/package.json",
		} {
			if excused(id, "Node.js", own, purl) {
				t.Errorf("%s at %s is excused; only node-engine's own npm copy may be — the application's "+
					"dependencies with the same name must still fail the gate", id, own)
			}
		}
	}
}
