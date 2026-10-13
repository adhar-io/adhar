package adharplatform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// The image-scan gate excuses exactly one finding class, and it must stay
// narrow, justified and time-boxed.
//
// Buildpack-built images carry Go binaries the platform did not write: the CNB
// launcher and one exec.d helper per buildpack. They read environment variables,
// print a config line and exit — they never serve HTTP or perform a TLS
// handshake, so the Go stdlib server-side CVEs are unreachable in them. They are
// also unfixable: Paketo pins an exact Go patch in go.mod and Go 1.27.2 was
// published two days after the newest buildpack release.
//
// Without the exception the gate refused a React app image whose only findings
// were 12 instances of those three CVEs, all HIGH, zero CRITICAL. With it, that
// image passes and the Java service still fails on nine CRITICAL dependency
// vulnerabilities — which is the behaviour this test pins.
func TestTheScanGateExcusesOnlyUnreachableBuildpackCVEs(t *testing.T) {
	path := filepath.Join(stackRoot(t), "packages", "application", "adhar-supply-chain",
		"manifests", "30-scan-verify-pipeline.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	raw := string(b)

	// The gate itself must not be weakened.
	for _, forbidden := range []string{"--ignore-unfixed", "--exit-code 0", "--severity CRITICAL\""} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("the scan gate uses %q; the exception must be a narrow allow-list, never a "+
				"blanket downgrade of what the gate reports", forbidden)
		}
	}
	if !strings.Contains(raw, "--exit-code 1") {
		t.Error("the scan no longer fails the pipeline; a gate that only reports is a dashboard")
	}
	if !strings.Contains(raw, "--ignorefile /etc/trivy/ignore.yaml") {
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
			for _, p := range v.PURLs {
				if !strings.HasPrefix(p, "pkg:golang/stdlib") {
					t.Errorf("%s is scoped to purl %q; only the Go standard library in "+
						"buildpack-provided binaries is excused here", v.ID, p)
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
