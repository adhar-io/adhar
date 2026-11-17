package adharplatform

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Namespaced managed resources — the `.m` API groups every composition here
// uses — have no spec.deletionPolicy. The API server does not prune it: the
// apply fails with "field not declared in schema", and because one failed
// composed resource fails the whole composition, the XR composes NOTHING. On a
// live cluster that left every Team unsynced (its Volcano queues), and the AI
// runtime without its knowledge store (the Vector's Helm release), while
// `crossplane render` — which checks no schema — showed both as fine
// (2026-11-18). Retention is spelled by withholding Delete from
// managementPolicies.
func TestNoNamespacedManagedResourceSetsDeletionPolicy(t *testing.T) {
	root := filepath.Join(stackRoot(t), "..", "controlplane", "configuration", "compositions")
	apiVersion := regexp.MustCompile(`^\s*(?:- )?apiVersion:\s*(\S+)`)
	deletion := regexp.MustCompile(`^\s*deletionPolicy:`)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".yaml") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		api := ""
		for i, line := range strings.Split(string(b), "\n") {
			if m := apiVersion.FindStringSubmatch(line); m != nil {
				api = m[1]
				continue
			}
			if deletion.MatchString(line) && strings.Contains(api, ".m.") {
				rel, _ := filepath.Rel(root, p)
				t.Errorf("%s:%d sets deletionPolicy on a %s resource; use managementPolicies without Delete", rel, i+1, api)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
