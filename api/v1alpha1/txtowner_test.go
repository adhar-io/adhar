package v1alpha1

import "testing"

// The external-dns owner id must survive a cluster rename or recreate.
//
// external-dns writes a TXT record beside every A record saying who owns it, and
// refuses to modify a record owned by someone else. While the owner was
// "adhar-"+ClusterName, replacing a cluster changed the owner and ORPHANED every
// record in the zone: the new cluster could neither update them (not its
// records) nor delete them (the package runs `--policy=upsert-only`).
//
// Measured on the 2026-10-03 GCP cluster: 42 A records still pointing at the
// previous cluster's dead load-balancer IP, 108 TXT records all owned by
// `adhar-production`, against a live external-dns whose owner was `adhar-prod`.
// Every platform URL resolved to a dead IP while external-dns logged
// "All records are already up to date" once a minute. The single exception
// proved the mechanism: `kubeflow.cloud.adhar.io` was a NEW hostname with no
// prior TXT, so it was created correctly at the live IP.
func TestTXTOwnerIDIsStableAcrossClusterRenames(t *testing.T) {
	const host = "cloud.adhar.io"
	want := BuildCustomizationSpec{Host: host, ClusterName: "prod"}.TXTOwnerID()

	for _, cluster := range []string{"prod", "production", "adhar", "adhar-prod", ""} {
		got := BuildCustomizationSpec{Host: host, ClusterName: cluster}.TXTOwnerID()
		if got != want {
			t.Errorf("cluster %q changed the owner id to %q (want %q) — renaming or recreating "+
				"a cluster must not orphan the zone's records", cluster, got, want)
		}
	}
}

// Two platforms on DIFFERENT hosts must still own their records separately —
// that is the case per-owner separation actually protects.
func TestTXTOwnerIDSeparatesDifferentHosts(t *testing.T) {
	a := BuildCustomizationSpec{Host: "cloud.adhar.io"}.TXTOwnerID()
	b := BuildCustomizationSpec{Host: "platform.adhar.io"}.TXTOwnerID()
	if a == b {
		t.Errorf("different hosts must get different owner ids, both got %q", a)
	}
}

// One platform must not resolve to two owners because the host was spelled with
// different separators.
func TestTXTOwnerIDNormalisesSeparators(t *testing.T) {
	dotted := BuildCustomizationSpec{Host: "cloud.adhar.io"}.TXTOwnerID()
	dashed := BuildCustomizationSpec{Host: "cloud-adhar-io"}.TXTOwnerID()
	if dotted != dashed {
		t.Errorf("separator shape must not change ownership: %q vs %q", dotted, dashed)
	}
	if upper := (BuildCustomizationSpec{Host: "Cloud.Adhar.IO"}).TXTOwnerID(); upper != dotted {
		t.Errorf("case must not change ownership: %q vs %q", upper, dotted)
	}
}

// The id has to be a usable DNS/TXT value: lower-case alphanumerics and dashes,
// no leading or trailing separator.
func TestTXTOwnerIDIsLabelSafe(t *testing.T) {
	got := BuildCustomizationSpec{Host: "  .cloud.adhar.io.  "}.TXTOwnerID()
	if got == "" || got[len(got)-1] == '-' {
		t.Errorf("owner id must be trimmed and non-empty, got %q", got)
	}
	for _, r := range got {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
		if !ok {
			t.Errorf("owner id %q contains an unsafe character %q", got, r)
		}
	}
}

// With no host there is nothing stable to key on, so it falls back to the old
// behaviour rather than returning a constant that every platform would share.
func TestTXTOwnerIDFallsBackWithoutAHost(t *testing.T) {
	if got := (BuildCustomizationSpec{ClusterName: "prod"}).TXTOwnerID(); got != "adhar-prod" {
		t.Errorf("without a host the cluster name is the only key available, got %q", got)
	}
	if got := (BuildCustomizationSpec{}).TXTOwnerID(); got != "adhar" {
		t.Errorf("with neither, got %q, want %q", got, "adhar")
	}
}
