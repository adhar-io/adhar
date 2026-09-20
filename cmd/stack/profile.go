/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package stack

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"adhar-io/adhar/cmd/helpers"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"
)

// A profile is one enablement of the catalogue. The two files below must agree —
// a parity test in the controller package enforces it — so every edit touches both
// or neither.
type profile struct {
	name       string
	appsetFile string
	envFile    string
}

func profileFor(name, stackDir string) (profile, error) {
	switch name {
	case "local":
		return profile{
			name:       "local",
			appsetFile: filepath.Join(stackDir, "adhar-appset-local.yaml"),
			envFile:    filepath.Join(stackDir, "environments", "local", "config.yaml"),
		}, nil
	case "production":
		return profile{
			name:       "production",
			appsetFile: filepath.Join(stackDir, "adhar-appset-production.yaml"),
			envFile:    filepath.Join(stackDir, "environments", "production", "config.yaml"),
		}, nil
	}
	return profile{}, fmt.Errorf("unknown profile %q — expected local or production", name)
}

// element is one wired package: its identity plus whether this profile turns it on.
type element struct {
	Name         string `json:"name"`
	Enabled      string `json:"enabled"`
	Namespace    string `json:"namespace"`
	Category     string `json:"category"`
	ManifestPath string `json:"manifestPath"`
	Plane        string `json:"plane,omitempty"`
}

func (e element) on() bool { return e.Enabled == "true" }

// readElements parses the ApplicationSet's list generator.
func readElements(appsetFile string) ([]element, error) {
	data, err := os.ReadFile(appsetFile) // #nosec G304 -- a path built from --stack-dir and a known profile name
	if err != nil {
		return nil, fmt.Errorf("reading the ApplicationSet (run from the adhar repository root, or pass --stack-dir): %w", err)
	}
	var doc struct {
		Spec struct {
			Generators []struct {
				List struct {
					Elements []element `json:"elements"`
				} `json:"list"`
			} `json:"generators"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", appsetFile, err)
	}
	var out []element
	for _, g := range doc.Spec.Generators {
		out = append(out, g.List.Elements...)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s declares no packages", appsetFile)
	}
	return out, nil
}

// detectProfile works out which profile this cluster is running.
//
// The in-cluster ApplicationSet's name carries it (`helm-charts-production`), which
// is better than guessing from the kube context: a developer with several contexts
// would otherwise edit the wrong profile's files and only find out when the diff
// looked nothing like what they asked for. With no cluster reachable it falls back
// to production, which is the one an operator is nearly always editing.
func detectProfile(ctx context.Context, stackDir string) (profile, string, error) {
	if flagProfile != "" {
		p, err := profileFor(flagProfile, stackDir)
		return p, "--profile", err
	}

	name, source := "production", "default (no cluster reachable)"
	if cfg, err := helpers.GetKubeConfig(); err == nil {
		if dc, err := dynamic.NewForConfig(cfg); err == nil {
			gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applicationsets"}
			if list, err := dc.Resource(gvr).Namespace(flagNamespace).List(ctx, metav1.ListOptions{}); err == nil {
				for _, item := range list.Items {
					n := item.GetName()
					switch {
					case strings.HasSuffix(n, "-production"):
						name, source = "production", "cluster ApplicationSet "+n
					case strings.HasSuffix(n, "-local"):
						name, source = "local", "cluster ApplicationSet "+n
					}
				}
			}
		}
	}
	p, err := profileFor(name, stackDir)
	return p, source, err
}

// ---------------------------------------------------------------------------
// Editing enablement without destroying the files
// ---------------------------------------------------------------------------

// These files are documentation as much as configuration: nearly every entry
// carries a comment saying why it is on or off, which package it conflicts with,
// and what it costs. Round-tripping them through a YAML marshaller would delete
// every one of those comments and reflow the rest, turning a one-line change into
// an unreviewable diff. So the edit is line-based and surgical: find the element,
// find its `enabled:` line, change that value, touch nothing else.

var (
	elementNameRe = regexp.MustCompile(`^(\s*)-\s+name:\s*"?([A-Za-z0-9._-]+)"?\s*$`)
	enabledRe     = regexp.MustCompile(`^(\s*)enabled:\s*"?(true|false)"?\s*$`)
)

// setEnabled rewrites one package's enablement in one file.
//
// Returns whether the file changed; a package that is already in the requested
// state is not an error, because `adhar stack enable x` twice should be a no-op
// rather than a failure.
func setEnabled(path, pkg string, on bool) (changed bool, err error) {
	data, err := os.ReadFile(path) // #nosec G304 -- a path built from --stack-dir and a known profile name
	if err != nil {
		return false, err
	}
	lines := strings.Split(string(data), "\n")
	want := "false"
	if on {
		want = "true"
	}

	found := false
	for i, line := range lines {
		m := elementNameRe.FindStringSubmatch(line)
		if m == nil || m[2] != pkg {
			continue
		}
		// Scan this element's own lines only: stopping at the next `- name:` keeps
		// the edit inside the entry, so a package whose `enabled:` line is missing
		// does not silently take its neighbour's.
		for j := i + 1; j < len(lines); j++ {
			if elementNameRe.MatchString(lines[j]) {
				break
			}
			em := enabledRe.FindStringSubmatch(lines[j])
			if em == nil {
				continue
			}
			found = true
			if em[2] == want {
				return false, nil
			}
			lines[j] = fmt.Sprintf(`%senabled: "%s"`, em[1], want)
			changed = true
			break
		}
		if found {
			break
		}
	}
	if !found {
		return false, fmt.Errorf("no package named %q in %s", pkg, filepath.Base(path))
	}
	if !changed {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil { // #nosec G306 -- a tracked source file in the user's checkout
		return false, err
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// Invariants the CLI must not let you break
// ---------------------------------------------------------------------------

// exclusiveGroups are packages that claim the SAME cluster-scoped object in the
// shared adhar-system namespace. Enabling both leaves two Applications fighting
// over one object, each reverting the other, both reporting Degraded and neither
// saying why — so enabling one is refused while another member is on. The list
// mirrors platform/stack/packages/CONFLICTS.md.
//
// NOT in this list, though a reader might expect it: supply-chain-policies and
// supply-chain-policies-enforce. The audit and enforce packs are the same rules at
// different failureActions, and their ClusterPolicy names differ by an `-enforce`
// suffix, so they coexist by design and production ships both — the audit findings
// are what predict which workloads the enforce pack would block.
var exclusiveGroups = []struct {
	members []string
	why     string
}{
	{[]string{"vault", "openbao"}, "both claim ClusterSecretStore/vault and Service/vault"},
	{[]string{"vllm-cpu", "vllm-gpu"}, "both Deployments are named vllm and share one Service"},
}

// advisoryGroups are "choose one" by platform policy rather than by object
// collision: nothing breaks if both run, but the platform's consumers are wired to
// one of them, so the other is dead weight. These WARN and never refuse — the
// difference matters, because a refusal for a non-collision would stop an operator
// doing something legitimate.
var advisoryGroups = []struct {
	members []string
	why     string
}{
	{[]string{"minio", "rustfs"}, "both are S3 endpoints, but every consumer reads RustFS's root-creds; MinIO is the opt-in alternative"},
}

// dependencies are packages that do not work alone. agentgateway serves adhar-ai's
// LLM and MCP endpoints, so enabling it by itself produces a data plane with
// nothing behind it.
var dependencies = map[string][]string{
	"agentgateway": {"adhar-ai"},
	"adhar-ai":     {"agentgateway"},
	"vllm-cpu":     {"vllm"},
	"vllm-gpu":     {"vllm"},
}

// conflictsWith returns the enabled packages that must be turned off before pkg can
// be turned on. Hard collisions only.
func conflictsWith(pkg string, elements []element) []string {
	on := map[string]bool{}
	for _, e := range elements {
		on[e.Name] = e.on()
	}
	var out []string
	for _, g := range exclusiveGroups {
		if !contains(g.members, pkg) {
			continue
		}
		for _, m := range g.members {
			if m != pkg && on[m] {
				out = append(out, m)
			}
		}
	}
	return out
}

// missingDependencies returns the packages pkg needs that are not enabled.
func missingDependencies(pkg string, elements []element) []string {
	on := map[string]bool{}
	for _, e := range elements {
		on[e.Name] = e.on()
	}
	var out []string
	for _, dep := range dependencies[pkg] {
		if !on[dep] {
			out = append(out, dep)
		}
	}
	return out
}

// dependentsOf returns the enabled packages that depend on pkg, so disabling it can
// say what it will break instead of leaving them half-working.
func dependentsOf(pkg string, elements []element) []string {
	on := map[string]bool{}
	for _, e := range elements {
		on[e.Name] = e.on()
	}
	var out []string
	for candidate, deps := range dependencies {
		if candidate == pkg || !on[candidate] {
			continue
		}
		if contains(deps, pkg) {
			out = append(out, candidate)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// advisoriesFor returns the enabled packages that make pkg redundant, or that pkg
// makes redundant. Informational: the caller warns and proceeds.
func advisoriesFor(pkg string, elements []element) []string {
	on := map[string]bool{}
	for _, e := range elements {
		on[e.Name] = e.on()
	}
	var out []string
	for _, g := range advisoryGroups {
		if !contains(g.members, pkg) {
			continue
		}
		for _, m := range g.members {
			if m != pkg && on[m] {
				out = append(out, m)
			}
		}
	}
	return out
}
