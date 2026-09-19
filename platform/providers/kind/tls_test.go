package kind

import "testing"

// A platform certificate cached on disk was reused whatever hostname the new
// cluster used, so a cloud platform on cloud.adhar.io served a certificate for
// adhar.localtest.me left over from a local run. The visible symptom was browser
// warnings; the damaging one was server-side, with Coder's OIDC discovery failing
// on "certificate is valid for adhar.localtest.me, not keycloak.cloud.adhar.io"
// so the workspace service never started (2026-09-19).
func TestCachedCertificateIsRejectedWhenItDoesNotCoverTheHost(t *testing.T) {
	local := []string{"adhar.localtest.me", "*.adhar.localtest.me"}
	cloud := []string{"cloud.adhar.io", "*.cloud.adhar.io"}

	cert, _, err := createSelfSignedCertificate(local)
	if err != nil {
		t.Fatalf("generating the local certificate: %v", err)
	}

	if !certificateCoversSANs(cert, local) {
		t.Error("a certificate must be accepted for the names it was generated for")
	}
	if certificateCoversSANs(cert, cloud) {
		t.Error("a local certificate must NOT be accepted for a cloud hostname — this is the reuse that broke Coder")
	}

	// A wildcard must satisfy a host beneath it, so a cluster is not forced to
	// regenerate on every apex/subdomain difference.
	if !certificateCoversSANs(cert, []string{"argocd.adhar.localtest.me"}) {
		t.Error("a wildcard SAN should cover a single label beneath it")
	}
	// Garbage must never be treated as usable.
	if certificateCoversSANs([]byte("not a certificate"), local) {
		t.Error("unparseable input must not be reported as covering anything")
	}
}
