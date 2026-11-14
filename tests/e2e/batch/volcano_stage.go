package batch

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"sigs.k8s.io/yaml"
)

// The Kind + Volcano variant of the batch suite (A2.9). Volcano is disabled in
// every shipped profile until the production gate (design 0025 §15), so the
// harness never edits platform/stack: it copies the tree, switches the Volcano
// elements on in the COPY's local profile, and `adhar up -p <copy>` seeds Gitea
// from the copy (cmd/up resolves the stack directory from the first -p
// directory; the controller re-applies the ApplicationSet from there).

// VolcanoElements are the ApplicationSet elements a cluster needs before the
// BatchJob composition takes the Volcano path: the scheduler package, and
// core/adhar-batch's Volcano directory (queue binding and the Volcano pod
// standards, design §4.3, §10.3), which is enabled exactly where `volcano` is.
// The packages they REQUIRE (their contracts' non-optional dependencies) are
// added by VolcanoStackElements.
var VolcanoElements = []string{"volcano", "adhar-batch-volcano"}

var (
	stageElementName = regexp.MustCompile(`^(\s*)-\s+name:\s*"?([A-Za-z0-9._-]+)"?\s*$`)
	stageEnabled     = regexp.MustCompile(`^(\s*)enabled:\s*"?(true|false)"?\s*$`)
)

// localProfileFiles are the two files of the local profile; a parity test
// keeps them in step, so the copy changes both.
var localProfileFiles = []string{"adhar-appset-local.yaml", filepath.Join("environments", "local", "config.yaml")}

// VolcanoStackElements is VolcanoElements plus every package they need, read
// from the packages' own contracts (adhar-package.yaml `dependencies`, the
// optional ones left out), transitively.
func VolcanoStackElements(stack string) ([]string, error) {
	type dep struct {
		Name     string `json:"name"`
		Category string `json:"category"`
		Optional bool   `json:"optional"`
	}
	contract := map[string]string{
		"volcano":             filepath.Join("core", "volcano"),
		"adhar-batch-volcano": filepath.Join("core", "adhar-batch"),
	}
	seen := map[string]bool{}
	var out []string
	queue := append([]string(nil), VolcanoElements...)
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
		dir, ok := contract[name]
		if !ok {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(stack, "packages", dir, "adhar-package.yaml"))
		if err != nil {
			return nil, fmt.Errorf("the %s contract: %w", name, err)
		}
		var doc struct {
			Dependencies []dep `json:"dependencies"`
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("the %s contract: %w", name, err)
		}
		for _, d := range doc.Dependencies {
			if d.Optional || d.Name == "" {
				continue
			}
			if d.Category != "" {
				contract[d.Name] = filepath.Join(d.Category, d.Name)
			}
			queue = append(queue, d.Name)
		}
	}
	return out, nil
}

// StageVolcanoStack copies the stack at src to dst (which must not exist yet,
// or be empty) and enables names in the copy's local profile. src is only
// read. It returns the elements it switched on, in order.
func StageVolcanoStack(src, dst string, names []string) ([]string, error) {
	src, err := filepath.Abs(src)
	if err != nil {
		return nil, err
	}
	dst, err = filepath.Abs(dst)
	if err != nil {
		return nil, err
	}
	if rel, err := filepath.Rel(src, dst); err == nil && (rel == "." || !strings.HasPrefix(rel, "..")) {
		return nil, fmt.Errorf("the staged stack %s must be outside %s, which it copies", dst, src)
	}
	if entries, err := os.ReadDir(dst); err == nil && len(entries) > 0 {
		return nil, fmt.Errorf("the staged stack %s is not empty; remove it or name another", dst)
	}
	if _, err := os.Stat(filepath.Join(src, localProfileFiles[0])); err != nil {
		return nil, fmt.Errorf("%s is not the platform stack: %w", src, err)
	}
	if err := copyTree(src, dst); err != nil {
		return nil, fmt.Errorf("copying %s to %s: %w", src, dst, err)
	}
	var changed []string
	for _, f := range localProfileFiles {
		for _, n := range names {
			did, err := enableElement(filepath.Join(dst, f), n)
			if err != nil {
				return nil, err
			}
			if did && f == localProfileFiles[0] {
				changed = append(changed, n)
			}
		}
	}
	return changed, nil
}

// enableElement sets `enabled: "true"` on one element of one profile file,
// line by line inside that element, as `adhar stack enable` edits it.
func enableElement(path, name string) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	lines := strings.Split(string(raw), "\n")
	for i, line := range lines {
		m := stageElementName.FindStringSubmatch(line)
		if m == nil || m[2] != name {
			continue
		}
		for j := i + 1; j < len(lines) && !stageElementName.MatchString(lines[j]); j++ {
			em := stageEnabled.FindStringSubmatch(lines[j])
			if em == nil {
				continue
			}
			if em[2] == "true" {
				return false, nil
			}
			lines[j] = em[1] + `enabled: "true"`
			return true, os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
		}
		return false, fmt.Errorf("%s: element %q has no enabled line", path, name)
	}
	hint := ""
	if name == "adhar-batch-volcano" {
		hint = " (the appsets gain it with design 0025 stage 2 FU-2)"
	}
	return false, fmt.Errorf("%s wires no element %q%s", path, name, hint)
}

// copyTree copies regular files, directories and symlinks, keeping modes.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case info.Mode().IsRegular():
			return copyFile(path, target, info.Mode().Perm())
		}
		return nil // sockets and devices are not part of a stack
	})
}

func copyFile(from, to string, mode os.FileMode) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// VolcanoProfileProblems reads a (staged) local ApplicationSet and says what
// keeps it from being the canary's profile: the batch chain on, as Phase 1
// needs, and every element names on.
func VolcanoProfileProblems(appset []byte, names []string) ([]string, error) {
	enabled, declared, err := appsetElements(appset)
	if err != nil {
		return nil, err
	}
	var problems []string
	for _, n := range LocalBatchNeeds {
		if !enabled[n] {
			problems = append(problems, n+" is not enabled, and the batch chain needs it")
		}
	}
	for _, n := range names {
		switch {
		case !declared[n]:
			problems = append(problems, n+" is not wired in the ApplicationSet")
		case !enabled[n]:
			problems = append(problems, n+" is not enabled, so the canary would run without Volcano")
		}
	}
	return problems, nil
}

func appsetElements(appset []byte) (enabled, declared map[string]bool, err error) {
	var doc struct {
		Spec struct {
			Generators []struct {
				List struct {
					Elements []struct {
						Name    string `json:"name"`
						Enabled string `json:"enabled"`
					} `json:"elements"`
				} `json:"list"`
			} `json:"generators"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(appset, &doc); err != nil {
		return nil, nil, err
	}
	enabled, declared = map[string]bool{}, map[string]bool{}
	for _, g := range doc.Spec.Generators {
		for _, e := range g.List.Elements {
			declared[e.Name] = true
			enabled[e.Name] = enabled[e.Name] || e.Enabled == "true"
		}
	}
	if len(declared) == 0 {
		return nil, nil, fmt.Errorf("no list elements: not the platform ApplicationSet")
	}
	return enabled, declared, nil
}
