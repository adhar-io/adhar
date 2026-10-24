package adharplatform

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// SeededEnabledAnnotation records, on the ApplicationSet pushed to Gitea, which
// packages the CLI's own profile file enabled at that push. It is the merge
// base for the next push.
//
// Two writers set a package's `enabled` flag in the packages repo: the CLI,
// which pushes the profile file (adhar-appset-*.yaml) on `adhar up` and
// `adhar upgrade`, and the console's Marketplace, which commits a one-line flip
// to applicationset.yaml. Pushing the local file verbatim reverted every
// Marketplace toggle (2026-10-15 and 2026-10-23). Taking Gitea's flags
// verbatim would instead ignore `adhar stack enable/disable`, which edits the
// local file. A three-way merge keeps both: a flag the Marketplace changed
// survives a push unless the local file changed the same flag too — the
// explicit local edit is the newer intent.
//
// The base cannot be "the file at the last CLI commit": once a push has merged
// a Marketplace flag in, that commit no longer says what the local file
// wanted, and the next push would read the kept flag as a local change and
// revert it. So the push records the local file's own enabled set here, and a
// Marketplace commit — a textual flip of one `enabled:` line — leaves it alone.
const SeededEnabledAnnotation = "adhar.io/seeded-enabled"

var (
	appSetElementNameRe = regexp.MustCompile(`^(\s*)-\s+name:\s*"?([A-Za-z0-9._-]+)"?\s*$`)
	appSetEnabledRe     = regexp.MustCompile(`^(\s*)enabled:\s*"?(true|false)"?\s*$`)
	seededEnabledRe     = regexp.MustCompile(`^\s*` + regexp.QuoteMeta(SeededEnabledAnnotation) + `:\s*"?([^"]*)"?\s*$`)
)

// appSetEnabledFlags maps each list element's name to its `enabled` value,
// scanning the text the same way the Marketplace and `adhar stack` edit it.
func appSetEnabledFlags(data []byte) map[string]bool {
	flags := map[string]bool{}
	current := ""
	for _, line := range strings.Split(string(data), "\n") {
		if m := appSetElementNameRe.FindStringSubmatch(line); m != nil {
			current = m[2]
			continue
		}
		if current == "" {
			continue
		}
		if m := appSetEnabledRe.FindStringSubmatch(line); m != nil {
			flags[current] = m[2] == "true"
			current = ""
		}
	}
	return flags
}

// seededEnabled reads SeededEnabledAnnotation; ok is false when it is absent.
func seededEnabled(data []byte) (map[string]bool, bool) {
	for _, line := range strings.Split(string(data), "\n") {
		m := seededEnabledRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		set := map[string]bool{}
		for _, n := range strings.Split(m[1], ",") {
			if n = strings.TrimSpace(n); n != "" {
				set[n] = true
			}
		}
		return set, true
	}
	return nil, false
}

// MergeMarketplaceToggles returns the ApplicationSet to push: the local
// profile file, with every flag the Marketplace changed in Gitea carried over,
// and SeededEnabledAnnotation set to the local file's own enabled set.
//
// head is Gitea's current applicationset.yaml (nil on a fresh repo). The base
// comes from head's annotation; fallbackBase — the file at the last commit
// that was not a Marketplace toggle — serves repos pushed before the annotation
// existed. With neither, the local file wins, as it always did. kept names the
// packages whose Gitea flag was preserved, for the caller to report.
func MergeMarketplaceToggles(local, head, fallbackBase []byte) (merged []byte, kept []string) {
	localFlags := appSetEnabledFlags(local)

	var base map[string]bool
	haveBase := false
	if len(head) > 0 {
		if set, ok := seededEnabled(head); ok {
			base, haveBase = map[string]bool{}, true
			for name := range localFlags {
				base[name] = set[name]
			}
		} else if len(fallbackBase) > 0 {
			base, haveBase = appSetEnabledFlags(fallbackBase), true
		}
	}

	out := local
	if haveBase {
		headFlags := appSetEnabledFlags(head)
		want := map[string]bool{}
		for name, l := range localFlags {
			b, inBase := base[name]
			h, inHead := headFlags[name]
			if inBase && inHead && l == b && h != b {
				want[name] = h
				kept = append(kept, name)
			}
		}
		sort.Strings(kept)
		out = setAppSetFlags(local, want)
	}
	return withSeededEnabled(out, localFlags), kept
}

// setAppSetFlags rewrites the `enabled:` line of each named element.
func setAppSetFlags(data []byte, want map[string]bool) []byte {
	if len(want) == 0 {
		return data
	}
	lines := strings.Split(string(data), "\n")
	current := ""
	for i, line := range lines {
		if m := appSetElementNameRe.FindStringSubmatch(line); m != nil {
			current = m[2]
			continue
		}
		v, ok := want[current]
		if !ok {
			continue
		}
		if m := appSetEnabledRe.FindStringSubmatch(line); m != nil {
			lines[i] = fmt.Sprintf(`%senabled: "%t"`, m[1], v)
			current = ""
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

// withSeededEnabled stamps SeededEnabledAnnotation into the ApplicationSet's
// metadata, replacing any earlier value.
func withSeededEnabled(data []byte, localFlags map[string]bool) []byte {
	var names []string
	for name, on := range localFlags {
		if on {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	entry := fmt.Sprintf(`    %s: "%s"`, SeededEnabledAnnotation, strings.Join(names, ","))

	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if seededEnabledRe.MatchString(line) {
			lines[i] = entry
			return []byte(strings.Join(lines, "\n"))
		}
	}
	for i, line := range lines {
		if line != "metadata:" {
			continue
		}
		// An existing top-level annotations block takes the entry; otherwise
		// one is opened directly under metadata.
		for j := i + 1; j < len(lines) && strings.HasPrefix(lines[j], "  "); j++ {
			if lines[j] == "  annotations:" {
				return []byte(strings.Join(insertLine(lines, j+1, entry), "\n"))
			}
		}
		return []byte(strings.Join(insertLine(insertLine(lines, i+1, entry), i+1, "  annotations:"), "\n"))
	}
	return data
}

func insertLine(lines []string, at int, line string) []string {
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:at]...)
	out = append(out, line)
	return append(out, lines[at:]...)
}
