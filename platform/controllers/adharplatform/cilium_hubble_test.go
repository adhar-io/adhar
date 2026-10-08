package adharplatform

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"adhar-io/adhar/api/v1alpha1"
)

// Renaming the Cilium cluster must not rename what Hubble's TLS depends on.
//
// hubble-relay derives the name it expects on the agent's certificate from its
// own config — `hubble-peer.<cluster-name>.hubble-grpc.cilium.io` — while the
// certificate itself is PRE-GENERATED in the embedded manifest with the SAN
// `*.adhar-mgmt.hubble-grpc.cilium.io`. A string replace can rewrite the
// expectation but cannot reissue the certificate, so rewriting both left the
// relay asking for a name the agent could never present:
//
//	x509: certificate is valid for *.adhar-mgmt.hubble-grpc.cilium.io,
//	not hubble-peer.adhar-prod.hubble-grpc.cilium.io
//
// Measured on a live AWS cluster named `adhar-prod` (2026-10-08): hubble-relay
// crash-looped six times in its first ten minutes while the rest of the platform
// looked healthy.
func TestRenamingTheClusterLeavesHubbleTLSAlone(t *testing.T) {
	manifest, err := ciliumFS.ReadFile("resources/cilium/install.yaml")
	if err != nil {
		t.Fatalf("reading the embedded Cilium manifest: %v", err)
	}

	platform := &v1alpha1.AdharPlatform{
		ObjectMeta: metav1.ObjectMeta{Name: "adhar", Namespace: "adhar-system"},
		Spec: v1alpha1.AdharPlatformSpec{
			ClusterMesh: &v1alpha1.ClusterMeshSpec{Name: "adhar-prod", ID: 2},
		},
	}

	out := string(rewriteCiliumClusterIdentity(context.Background(), manifest, platform))

	// The agent's own identity DOES change: that is the point of the rewrite.
	if !strings.Contains(out, `cluster-name: "adhar-prod"`) {
		t.Error("cilium-config did not get the platform's cluster name")
	}
	if !strings.Contains(out, `cluster-id: "2"`) {
		t.Error("cilium-config did not get the platform's cluster id")
	}

	// hubble-relay-config must NOT: its value has to keep matching the baked
	// certificate's SAN.
	if strings.Contains(out, "cluster-name: adhar-prod") {
		t.Error("hubble-relay-config was renamed; the relay will expect a TLS name the agent cannot present, " +
			"and hubble-relay crash-loops for the life of the cluster")
	}
	if !strings.Contains(out, "cluster-name: adhar-mgmt") {
		t.Error("hubble-relay-config lost its cluster name entirely")
	}
}

// The relay's configured name and the certificate's SAN are one agreement, and
// it lives across two objects in the same file. If someone regenerates the
// manifest with a different PKI name, this is what says so.
func TestHubbleRelayConfigMatchesTheBakedCertificate(t *testing.T) {
	manifest, err := ciliumFS.ReadFile("resources/cilium/install.yaml")
	if err != nil {
		t.Fatalf("reading the embedded Cilium manifest: %v", err)
	}
	text := string(manifest)

	// The relay's expectation, as shipped.
	const relayName = "cluster-name: " + v1alpha1.DefaultClusterMeshName
	if !strings.Contains(text, relayName) {
		t.Fatalf("hubble-relay-config does not carry %q; the Hubble PKI agreement has moved", relayName)
	}

	// And the quoted form, which is cilium-config — the two must be
	// distinguishable, because the rewrite relies on exactly that.
	quoted := `cluster-name: "` + v1alpha1.DefaultClusterMeshName + `"`
	if !strings.Contains(text, quoted) {
		t.Fatalf("cilium-config does not carry %q", quoted)
	}
	// Exactly one unquoted occurrence: the rewrite's whole safety rests on the
	// unquoted form being hubble-relay-config and nothing else. A second one
	// would silently stop getting the cluster name.
	if n := strings.Count(text, relayName); n != 1 {
		t.Errorf("the manifest has %d unquoted `cluster-name: %s` values, want exactly 1 (hubble-relay-config); "+
			"anything else that needs the real cluster name is now being skipped",
			n, v1alpha1.DefaultClusterMeshName)
	}

	// The certificate the relay has to verify against.
	if !strings.Contains(text, "name: hubble-server-certs") {
		t.Error("the manifest no longer ships hubble-server-certs; if Hubble TLS is now generated in-cluster, " +
			"the rewrite exception above is obsolete and should go")
	}
}
