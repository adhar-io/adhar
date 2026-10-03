package k8s

import (
	"os"
	"testing"

	"adhar-io/adhar/globals"
)

// Velero's namespace is DISCOVERED, never assumed.
//
// Every `adhar backup`/`adhar restore` subcommand used a
// `const veleroNamespace = "velero"` — Velero's upstream default. This platform
// installs every package into `adhar-system` (ADR-0011), so the commands failed
// against their own cluster with
//
//	✖ failed to create backup "adhar-verify-1": namespaces "velero" not found
//
// while Velero sat healthy in `adhar-system`. Replacing one hardcoded namespace
// with the other would just move the bug onto anyone running an upstream Velero.
func TestVeleroNamespaceHonoursTheOperatorOverride(t *testing.T) {
	// The override is checked FIRST, before any cluster call, so it is the one
	// path that is testable without a live API server — and it is the escape
	// hatch an operator needs when discovery cannot reach the cluster.
	t.Setenv(VeleroNamespaceEnv, "my-velero")
	resetVeleroCache()
	if got := VeleroNamespace(); got != "my-velero" {
		t.Errorf("$%s must win: got %q, want %q", VeleroNamespaceEnv, got, "my-velero")
	}
}

func TestVeleroNamespaceTrimsTheOverride(t *testing.T) {
	// An operator exporting the variable from a shell script easily leaves
	// whitespace on it, and a namespace with a trailing space is not a namespace.
	t.Setenv(VeleroNamespaceEnv, "  velero-prod\n")
	resetVeleroCache()
	if got := VeleroNamespace(); got != "velero-prod" {
		t.Errorf("override must be trimmed: got %q", got)
	}
}

// An empty override must NOT be treated as an answer — it has to fall through to
// discovery, or `export ADHAR_VELERO_NAMESPACE=` would silently break the
// commands it was meant to fix.
func TestVeleroNamespaceIgnoresAnEmptyOverride(t *testing.T) {
	t.Setenv(VeleroNamespaceEnv, "   ")
	resetVeleroCache()
	// With no reachable cluster, discovery falls back to the platform's own
	// namespace — the place Velero is SUPPOSED to be, so the error an operator
	// sees names somewhere real.
	if got := VeleroNamespace(); got != globals.AdharSystemNamespace {
		t.Errorf("empty override must fall through to discovery: got %q, want %q",
			got, globals.AdharSystemNamespace)
	}
}

// The fallback is the PLATFORM's namespace, not Velero's upstream default.
//
// This is the assertion that encodes the fix: when nothing can be discovered,
// guess where this platform actually installs Velero.
func TestVeleroNamespaceFallsBackToThePlatformNamespace(t *testing.T) {
	os.Unsetenv(VeleroNamespaceEnv)
	resetVeleroCache()
	got := VeleroNamespace()
	if got == veleroDefaultNamespace {
		t.Errorf("fallback must not be Velero's upstream default %q — this platform "+
			"installs into %q (ADR-0011), which is what made every backup command fail",
			veleroDefaultNamespace, globals.AdharSystemNamespace)
	}
	if got != globals.AdharSystemNamespace {
		t.Errorf("fallback: got %q, want %q", got, globals.AdharSystemNamespace)
	}
}
