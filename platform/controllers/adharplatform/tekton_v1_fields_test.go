package adharplatform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// tekton.dev/v1 names a Step's and a Sidecar's resources `computeResources`.
// A `resources:` there is rejected by Tekton's webhook — and in a GitOps
// package that does not just break one Task: Argo CD's server-side diff fails
// for the WHOLE application, which then syncs nothing (adhar-supply-chain,
// 2026-10-22).
func TestTektonV1StepsAndSidecarsUseComputeResources(t *testing.T) {
	root := stackPackagesDir(t)
	checked := 0
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return err
		}
		b, _ := os.ReadFile(path)
		if !strings.Contains(string(b), "tekton.dev/v1") {
			return nil
		}
		for _, raw := range strings.Split(string(b), "\n---\n") {
			var doc map[string]any
			if yaml.Unmarshal([]byte(raw), &doc) != nil || doc == nil || doc["apiVersion"] != "tekton.dev/v1" {
				continue
			}
			if doc["kind"] != "Task" {
				continue
			}
			checked++
			for _, list := range []string{"steps", "sidecars"} {
				items, _ := dig(doc, "spec", list).([]any)
				for _, it := range items {
					m, _ := it.(map[string]any)
					if _, bad := m["resources"]; bad {
						t.Errorf("%s: Task %v %s %v uses `resources`; tekton.dev/v1 calls it computeResources",
							filepath.Base(path), dig(doc, "metadata", "name"), strings.TrimSuffix(list, "s"), m["name"])
					}
				}
			}
		}
		return nil
	})
	if checked == 0 {
		t.Fatal("no tekton.dev/v1 Tasks found; this test checks nothing")
	}
}
