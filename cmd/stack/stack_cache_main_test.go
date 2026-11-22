package stack

import (
	"os"
	"testing"

	"adhar-io/adhar/platform/stack"
)

// TestMain points embedded-stack extraction at a throw-away directory. Commands
// under test resolve the embedded stack, and extracting into the real user
// cache used to prune the extraction a concurrently running `adhar up` was
// seeding from (2026-11-22).
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "adhar-stack-cache-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv(stack.CacheEnv, dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
