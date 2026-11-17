// Command stack-bundle writes platform/stack/generated/nested-modules.tar.gz:
// the stack directories that carry their own go.mod, which go:embed leaves out.
// It runs from `go generate ./platform/stack/` — which `make build`, `make test`
// and the GoReleaser before-hook all invoke — so the archive is a build product
// and is never committed.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"adhar-io/adhar/platform/stack"
)

func main() {
	dir := "."
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	b, err := stack.BuildNestedModuleBundle(dir)
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, filepath.FromSlash(stack.NestedModuleBundle)), b, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "stack-bundle:", err)
		os.Exit(1)
	}
}
