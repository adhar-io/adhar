package up

import (
	"os"
	"path/filepath"

	"adhar-io/adhar/platform/logger"
	"adhar-io/adhar/platform/stack"
)

// resolveStack returns the platform stack `adhar up` seeds into the GitOps
// repositories. A -p directory that IS a stack (it has packages/) is used as
// given; any other -p value is an extra package location and leaves the choice
// to stack.Resolve: $ADHAR_STACK_DIR, else the stack built into the binary —
// in every directory, the adhar repository included, so what is seeded is what
// the binary ships.
func resolveStack() (stack.Source, error) {
	explicit := ""
	for _, p := range extraPackages {
		if fi, err := os.Stat(filepath.Join(p, "packages")); err == nil && fi.IsDir() {
			explicit = p
			break
		}
	}
	src, err := stack.Resolve(explicit)
	if err != nil {
		return stack.Source{}, err
	}
	logger.Debugf("platform stack: %s (%s)", src.Describe(), src.Dir)
	return src, nil
}
