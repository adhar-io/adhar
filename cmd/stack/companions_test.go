/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package stack

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Enabling volcano alone runs Volcano with no queue binding and no Volcano run
// integrity: any team can name any queue. The two elements are one package, so the
// command switches them together in both directions.
func TestVolcanoAndItsBatchPoliciesAreSwitchedTogether(t *testing.T) {
	t.Parallel()
	known := map[string]element{
		"volcano":             {Name: "volcano"},
		"adhar-batch-volcano": {Name: "adhar-batch-volcano"},
		"harbor":              {Name: "harbor"},
	}
	for _, tc := range []struct {
		in, all, added []string
	}{
		{[]string{"volcano"}, []string{"volcano", "adhar-batch-volcano"}, []string{"adhar-batch-volcano"}},
		{[]string{"adhar-batch-volcano"}, []string{"adhar-batch-volcano", "volcano"}, []string{"volcano"}},
		{[]string{"volcano", "adhar-batch-volcano"}, []string{"volcano", "adhar-batch-volcano"}, nil},
		{[]string{"harbor"}, []string{"harbor"}, nil},
	} {
		all, added := withCompanions(tc.in, known)
		if !reflect.DeepEqual(all, tc.all) || !reflect.DeepEqual(added, tc.added) {
			t.Errorf("withCompanions(%v) = %v, %v; want %v, %v", tc.in, all, added, tc.all, tc.added)
		}
	}
	// A stack that does not wire the companion still toggles the package.
	all, added := withCompanions([]string{"volcano"}, map[string]element{"volcano": {Name: "volcano"}})
	if !reflect.DeepEqual(all, []string{"volcano"}) || added != nil {
		t.Errorf("an unwired companion was added: %v, %v", all, added)
	}
}

// The shipped profiles must wire both halves and keep them in the same state, or
// the command's rule and the repository disagree.
func TestTheShippedProfilesKeepCompanionsInStep(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"local", "production"} {
		p, err := profileFor(name, filepath.Join("..", "..", "platform", "stack"))
		if err != nil {
			t.Fatal(err)
		}
		elements, err := readElements(p.appsetFile)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		state := map[string]*bool{}
		for _, e := range elements {
			on := e.on()
			state[e.Name] = &on
		}
		for pkg, cs := range companions {
			for _, c := range cs {
				a, b := state[pkg], state[c]
				if a == nil || b == nil {
					t.Errorf("%s profile does not wire both %s and its companion %s", name, pkg, c)
					continue
				}
				if *a != *b {
					t.Errorf("%s profile has %s enabled=%v but its companion %s enabled=%v", name, pkg, *a, c, *b)
				}
			}
		}
	}
}

// The command itself must carry the companion into every file it edits: the unit
// above proves the expansion, this proves runToggle uses it. It edits a copy of
// the shipped profiles, never the repository.
func TestEnablingVolcanoEditsBothElementsInEveryFile(t *testing.T) {
	src := filepath.Join("..", "..", "platform", "stack")
	dir := t.TempDir()
	files := []string{
		"adhar-appset-local.yaml",
		"adhar-appset-production.yaml",
		filepath.Join("environments", "local", "config.yaml"),
		filepath.Join("environments", "prod", "config.yaml"),
	}
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(src, f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldDir, oldProfile, oldYes := flagStackDir, flagProfile, flagYes
	flagStackDir, flagProfile, flagYes = dir, "local", true
	t.Cleanup(func() { flagStackDir, flagProfile, flagYes = oldDir, oldProfile, oldYes })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	if err := runToggle(cmd, []string{"volcano"}, true); err != nil {
		t.Fatalf("adhar stack enable volcano: %v", err)
	}
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		for _, pkg := range []string{"volcano", "adhar-batch-volcano"} {
			if !elementEnabledIn(string(b), pkg) {
				t.Errorf("after `adhar stack enable volcano`, %s still has %s off", f, pkg)
			}
		}
	}
}

// elementEnabledIn reads one element's enabled: line the way setEnabled finds it.
func elementEnabledIn(content, pkg string) bool {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		m := elementNameRe.FindStringSubmatch(line)
		if m == nil || m[2] != pkg {
			continue
		}
		for j := i + 1; j < len(lines) && !elementNameRe.MatchString(lines[j]); j++ {
			if em := enabledRe.FindStringSubmatch(lines[j]); em != nil {
				return em[2] == "true"
			}
		}
	}
	return false
}
