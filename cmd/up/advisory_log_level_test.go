package up

import (
	"os"
	"regexp"
	"testing"
)

// The checklist shows WARN lines to the operator. Three advisories used to be
// printed there on every cloud `adhar up` although none asked anything of the
// operator: control-plane sizing (that belongs to `adhar config validate`), the
// capacity estimate (the node autoscaler acts on it), and an environment that
// repeats the host's first label (its apps are routed as <app>-<env>.<host>, so
// nothing is unreachable). They are debug-level now.
func TestProvisioningAdvisoriesAreNotWarnings(t *testing.T) {
	cases := []struct{ file, pattern string }{
		{"bootstrap.go", `logger\.Warnf\("Capacity:`},
		{"bootstrap.go", `logger\.Warnf\("Environment %q (has no public hostname|shares the host)`},
		{"../../platform/providers/provider.go", `range SizingWarnings\([^)]*\) \{\s*logger\.Warnf`},
	}
	for _, c := range cases {
		src, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatal(err)
		}
		if regexp.MustCompile(c.pattern).Match(src) {
			t.Errorf("%s logs an advisory at WARN (%s); it must stay at debug level during adhar up", c.file, c.pattern)
		}
	}
}
