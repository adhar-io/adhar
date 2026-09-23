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
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/cmd/version"

	"github.com/spf13/cobra"
	"k8s.io/client-go/discovery"
)

// ADR-0014 rule 4: the catalogue carries verification state, so "any package can
// be turned on at any time" is a checkable claim. This command is the only thing
// that writes it. The evidence is Argo CD health on a real cluster — sync status
// is ignored on purpose (rule 2): a package can sit OutOfSync on a monorepo push
// and be entirely healthy.

var (
	verifyWrite bool
	verifyCmd   = &cobra.Command{
		Use:   "verify",
		Short: "Record each package's verification state from live Argo CD health",
		Long: `Compares the packages this profile enables against Argo CD health and reports
which are verified (Healthy), known-broken (enabled but not Healthy) and
unverified (never enabled on this profile).

With --write, the result is recorded into each package's adhar-package.yaml as
its ` + "`verification:`" + ` block — the marketplace's evidence that the package works
on this Adhar and Kubernetes version. Only that block is touched; a package
already recorded as verified is not downgraded to unverified because it is
disabled on the current profile.`,
		Example: `  adhar stack verify
  adhar stack verify --write            # update every contract in the checkout
  adhar stack verify --write --profile production`,
		RunE: runVerify,
	}
)

func init() {
	verifyCmd.Flags().BoolVar(&verifyWrite, "write", false, "Write the verification block into each package's adhar-package.yaml")
	StackCmd.AddCommand(verifyCmd)
}

// verdict is what one package's contract will carry.
type verdict struct {
	Status string
	Reason string
}

// Evaluate turns declared enablement and live Argo CD state into a verdict.
//
// Healthy is the only pass. Progressing is not a failure — the package may be
// mid-rollout — so it is reported as unverified rather than known-broken; an
// operator who wants to record it waits for it to settle and runs again.
func Evaluate(enabled, present bool, health, message string) verdict {
	switch {
	case !enabled || !present:
		return verdict{Status: "unverified"}
	case health == "Healthy":
		return verdict{Status: "verified"}
	case health == "Progressing" || health == "":
		return verdict{Status: "unverified"}
	default:
		reason := health
		if message != "" {
			reason = health + ": " + firstLine(message)
		}
		return verdict{Status: "known-broken", Reason: reason}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return strings.TrimSpace(s)
}

func runVerify(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	rows, p, source, err := gather(ctx)
	if err != nil {
		return err
	}
	if len(liveApplications(ctx)) == 0 {
		return fmt.Errorf("no Argo CD Applications reachable — verification needs a running cluster (profile detected from %s)", source)
	}
	elements, err := readElements(p.appsetFile)
	if err != nil {
		return err
	}
	kubeVersion := serverVersion(ctx)
	today := time.Now().UTC().Format("2006-01-02")

	counts := map[string]int{}
	var written, skipped int
	// Elements that share a contract (the audit and enforce policy packs) write
	// the same file; a broken verdict must not be papered over by its healthy
	// sibling, whichever order they come in.
	broken := map[string]bool{}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	for _, r := range rows {
		v := Evaluate(r.Enabled, r.Present, r.Health, r.Message)
		counts[v.Status]++
		fmt.Printf("  %-14s %-42s %s\n", v.Status, r.Name, v.Reason)
		if !verifyWrite {
			continue
		}
		path := contractPath(flagStackDir, elements, r.Name)
		if path == "" {
			skipped++
			continue
		}
		if broken[path] && v.Status != "known-broken" {
			continue
		}
		if v.Status == "known-broken" {
			broken[path] = true
		}
		block := verification{
			Status:            v.Status,
			Profile:           p.name,
			PlatformVersion:   platformVersion(flagStackDir),
			KubernetesVersion: kubeVersion,
			VerifiedAt:        today,
			Reason:            v.Reason,
		}
		changed, err := writeVerification(path, block)
		if err != nil {
			return fmt.Errorf("%s: %w", r.Name, err)
		}
		if changed {
			written++
		}
	}
	fmt.Printf("\n%d verified · %d known-broken · %d unverified  (profile %s, from %s)\n",
		counts["verified"], counts["known-broken"], counts["unverified"], p.name, source)
	if verifyWrite {
		fmt.Printf("%d contract(s) updated", written)
		if skipped > 0 {
			fmt.Printf(", %d without a contract file", skipped)
		}
		fmt.Println()
	}
	return nil
}

// platformVersion is the Adhar version the verified stack belongs to. A release
// binary knows it; a dev build (`v0.0.1-dev`) does not, and the cluster does not
// record it either (the controller runs `:latest`), so fall back to the tag of
// the checkout whose stack is being verified — that is the thing the evidence is
// about. Empty when neither is known, never a placeholder.
func platformVersion(stackDir string) string {
	v := version.Version
	if v != "" && !strings.Contains(v, "-dev") && !strings.HasPrefix(strings.TrimPrefix(v, "v"), "0.0.1") {
		return v
	}
	// Walk up from the stack directory: the stack is a subtree of the platform
	// repository, and a stray inner .git (the seeding step leaves one behind on
	// some runs) must not be mistaken for the repository whose tag we want.
	dir, err := filepath.Abs(stackDir)
	if err != nil {
		return ""
	}
	for {
		if out, err := exec.Command("git", "-C", dir, "describe", "--tags", "--abbrev=0", "--match", "v*").Output(); err == nil {
			if tag := strings.TrimSpace(string(out)); tag != "" {
				return tag
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func serverVersion(ctx context.Context) string {
	cfg, err := helpers.GetKubeConfig()
	if err != nil {
		return ""
	}
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return ""
	}
	info, err := dc.ServerVersion()
	if err != nil {
		return ""
	}
	_ = ctx
	return info.GitVersion
}

// contractPath resolves a package's adhar-package.yaml through the
// ApplicationSet's manifest path, not the element name: several elements
// (vllm-cpu/-gpu, the audit and enforce policy packs, argo-workflows' dev
// overlay) point INTO a package — `ai/vllm/manifests/cpu` — and share the
// contract at its root. The search walks up from the manifest directory and
// stops at the packages tree, so an element can never borrow a neighbour's.
func contractPath(stackDir string, elements []element, name string) string {
	for _, e := range elements {
		if e.Name != name {
			continue
		}
		root := filepath.Clean(filepath.Join(stackDir, "packages"))
		dir := filepath.Clean(filepath.Join(root, e.ManifestPath))
		for strings.HasPrefix(dir, root+string(filepath.Separator)) {
			p := filepath.Join(dir, "adhar-package.yaml")
			if _, err := os.Stat(p); err == nil {
				return p
			}
			dir = filepath.Dir(dir)
		}
		return ""
	}
	return ""
}

// verification is the block written into a contract.
type verification struct {
	Status, Profile, PlatformVersion, KubernetesVersion, VerifiedAt, Reason string
}

func (v verification) render() []string {
	lines := []string{
		"verification:",
		"  status: " + v.Status,
	}
	if v.Status == "unverified" {
		return lines
	}
	lines = append(lines,
		"  profile: "+v.Profile,
		"  verifiedAt: \""+v.VerifiedAt+"\"",
	)
	if v.PlatformVersion != "" {
		lines = append(lines, "  platformVersion: \""+v.PlatformVersion+"\"")
	}
	if v.KubernetesVersion != "" {
		lines = append(lines, "  kubernetesVersion: \""+v.KubernetesVersion+"\"")
	}
	if v.Reason != "" {
		lines = append(lines, "  reason: "+yamlQuote(v.Reason))
	}
	return lines
}

func yamlQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

var verificationKeyRe = regexp.MustCompile(`^verification:\s*$`)

// writeVerification replaces (or appends) the top-level `verification:` block.
//
// Like setEnabled it is line-based: the contract files carry hand-written
// comments beside nearly every field, and a marshaller round-trip would throw
// them away. The block is the run of indented lines after the key; everything
// else is copied through untouched.
//
// An `unverified` verdict never overwrites a recorded `verified` or `known-broken`
// block: being disabled on today's profile is not evidence against yesterday's
// observation on another.
func writeVerification(path string, v verification) (bool, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- resolved from the stack's own ApplicationSet
	if err != nil {
		return false, err
	}
	lines := strings.Split(string(data), "\n")

	start, end := -1, -1
	for i, l := range lines {
		if verificationKeyRe.MatchString(l) {
			start = i
			end = len(lines)
			for j := i + 1; j < len(lines); j++ {
				t := lines[j]
				// Indented lines are the block's; blanks may be. A top-level
				// comment or key is the next thing in the file.
				if t == "" || strings.HasPrefix(t, " ") {
					continue
				}
				end = j
				break
			}
			break
		}
	}

	if start >= 0 && v.Status == "unverified" {
		for _, l := range lines[start:end] {
			if strings.Contains(l, "status: verified") || strings.Contains(l, "status: known-broken") {
				return false, nil
			}
		}
	}

	block := v.render()
	var out []string
	if start >= 0 {
		// Keep trailing blank lines that belonged to the old block's end so the
		// file's spacing does not drift on every run.
		trailing := 0
		for k := end - 1; k > start && lines[k] == ""; k-- {
			trailing++
		}
		out = append(out, lines[:start]...)
		out = append(out, block...)
		for i := 0; i < trailing; i++ {
			out = append(out, "")
		}
		out = append(out, lines[end:]...)
	} else {
		out = append(out, lines...)
		for len(out) > 0 && out[len(out)-1] == "" {
			out = out[:len(out)-1]
		}
		out = append(out, block...)
		out = append(out, "")
	}
	next := strings.Join(out, "\n")
	if next == string(data) {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(next), 0o644); err != nil { // #nosec G306 -- a tracked source file in the user's checkout
		return false, err
	}
	return true, nil
}
