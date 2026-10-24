package adharplatform

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"adhar-io/adhar/api/v1alpha1"
)

// Renaming the Cilium cluster must rename it EVERYWHERE Hubble looks.
//
// hubble-relay dials the hubble-peer service as
// `hubble-peer.<relay cluster-name>.hubble-grpc.cilium.io` and, since Cilium
// 1.20, each agent as `<node>.<agent cluster-name>.hubble-grpc.cilium.io`. With
// the agent renamed but the relay left at adhar-mgmt (the 2026-10-08
// workaround for the then-baked certificate) every peer failed verification on
// `adhar-prod` (2026-10-24): "No connection to peer", relay NOT_SERVING, and
// the console stuck on "Data streams are reconnecting". The certificates are
// now issued per cluster (ensureHubbleCerts), so both names follow the cluster.
func TestRenamingTheClusterRenamesHubbleToo(t *testing.T) {
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

	if !strings.Contains(out, `cluster-name: "adhar-prod"`) {
		t.Error("cilium-config did not get the platform's cluster name")
	}
	if !strings.Contains(out, `cluster-id: "2"`) {
		t.Error("cilium-config did not get the platform's cluster id")
	}
	if !strings.Contains(out, "\n    cluster-name: adhar-prod\n") {
		t.Error("hubble-relay-config kept the management cluster's name; the relay will dial " +
			"hubble-peer.adhar-mgmt.hubble-grpc.cilium.io against a certificate issued for adhar-prod")
	}
	if strings.Contains(out, v1alpha1.DefaultClusterMeshName) {
		t.Errorf("the rewritten manifest still mentions %q somewhere", v1alpha1.DefaultClusterMeshName)
	}
}

// The default cluster is the manifest as rendered: nothing to rewrite.
func TestDefaultClusterIdentityIsUntouched(t *testing.T) {
	manifest, err := ciliumFS.ReadFile("resources/cilium/install.yaml")
	if err != nil {
		t.Fatalf("reading the embedded Cilium manifest: %v", err)
	}
	platform := &v1alpha1.AdharPlatform{ObjectMeta: metav1.ObjectMeta{Name: "adhar", Namespace: "adhar-system"}}
	out := rewriteCiliumClusterIdentity(context.Background(), manifest, platform)
	if string(out) != string(manifest) {
		t.Error("the default (adhar-mgmt / 1) identity rewrote the manifest")
	}
}

// The rewrite relies on telling cilium-config (quoted) and hubble-relay-config
// (unquoted, indented) apart. If someone regenerates the manifest and either
// form moves, this is what says so.
func TestCiliumManifestCarriesBothClusterNameForms(t *testing.T) {
	manifest, err := ciliumFS.ReadFile("resources/cilium/install.yaml")
	if err != nil {
		t.Fatalf("reading the embedded Cilium manifest: %v", err)
	}
	text := string(manifest)

	quoted := `cluster-name: "` + v1alpha1.DefaultClusterMeshName + `"`
	if n := strings.Count(text, quoted); n != 1 {
		t.Errorf("cilium-config: %d occurrences of %q, want 1", n, quoted)
	}
	relay := "\n    cluster-name: " + v1alpha1.DefaultClusterMeshName + "\n"
	if n := strings.Count(text, relay); n != 1 {
		t.Errorf("hubble-relay-config: %d occurrences of the unquoted cluster-name, want 1", n)
	}
}
