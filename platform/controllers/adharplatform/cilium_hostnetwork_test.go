package adharplatform

import (
	"strings"
	"testing"
)

// Host-network mode needs THREE edits to agree, and the one that is easy to
// miss is the capability: with only the config key flipped, the Gateway reports
// Programmed with the node addresses and nothing listens on 443 — which is
// exactly what a live Civo compute cluster did on 2026-10-06. So this asserts
// all three, on the real embedded manifest.
func TestCiliumHostNetworkRewriteLandsAllThreeEdits(t *testing.T) {
	manifest, err := ciliumFS.ReadFile("resources/cilium/install.yaml")
	if err != nil {
		t.Fatalf("reading the embedded Cilium manifest: %v", err)
	}

	// The stock manifest must NOT already be in host-network mode: every other
	// provider depends on the Service path.
	if !strings.Contains(string(manifest), ciliumHostNetworkKeyOff) {
		t.Fatalf("the embedded manifest does not carry %q — host-network mode would be on for every provider",
			ciliumHostNetworkKeyOff)
	}

	patched, ok := rewriteCiliumGatewayHostNetwork(manifest)
	if !ok {
		t.Fatal("rewriteCiliumGatewayHostNetwork reported failure on the embedded manifest: " +
			"one of its three anchors has moved (config key, envoy starter args, envoy capabilities)")
	}
	out := string(patched)

	if !strings.Contains(out, ciliumHostNetworkKeyOn) {
		t.Error("cilium-config still has gateway-api-hostnetwork-enabled off")
	}
	if !strings.Contains(out, "--keep-cap-net-bind-service") {
		t.Error("cilium-envoy-starter is not told to keep NET_BIND_SERVICE, so it drops the capability before exec'ing Envoy")
	}
	if !strings.Contains(out, "NET_BIND_SERVICE") {
		t.Error("the cilium-envoy container has no NET_BIND_SERVICE capability, so Envoy cannot bind :80/:443")
	}

	// The capability must land on ENVOY ONLY. The same block appears five times
	// in the rendered chart (agent, two init containers, operator, envoy); the
	// agent getting it is a privilege the platform did not ask for.
	if n := strings.Count(out, "NET_BIND_SERVICE"); n != 1 {
		t.Errorf("NET_BIND_SERVICE appears %d times, want exactly 1 (the cilium-envoy container)", n)
	}
	envoyDoc := ""
	for _, doc := range strings.Split(out, "\n---\n") {
		if strings.Contains(doc, "k8s-app: cilium-envoy") && strings.Contains(doc, "kind: DaemonSet") {
			envoyDoc = doc
		}
	}
	if envoyDoc == "" {
		t.Fatal("no cilium-envoy DaemonSet in the patched manifest")
	}
	if !strings.Contains(envoyDoc, "NET_BIND_SERVICE") || !strings.Contains(envoyDoc, "--keep-cap-net-bind-service") {
		t.Error("the envoy edits did not land on the cilium-envoy DaemonSet")
	}

	// Idempotent: the reconciler runs this on every pass.
	again, ok := rewriteCiliumGatewayHostNetwork(patched)
	if !ok {
		t.Fatal("rewriting an already-patched manifest reported failure")
	}
	if string(again) != out {
		t.Error("rewriting an already-patched manifest changed it again (not idempotent)")
	}
}

// A missing anchor must be reported, not half-applied. Half-applied is the
// state that produces a Gateway which claims to be Programmed and serves
// nothing.
func TestCiliumHostNetworkRewriteRefusesAPartialPatch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		manifest string
	}{
		{
			name: "no cilium-config document",
			manifest: "apiVersion: apps/v1\nkind: DaemonSet\nmetadata:\n  labels:\n    k8s-app: cilium-envoy\n" +
				"spec:\n" + ciliumEnvoyStarterArgs + "          capabilities:\n" + ciliumEnvoyCaps,
		},
		{
			name:     "no cilium-envoy DaemonSet",
			manifest: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cilium-config\ndata:\n  " + ciliumHostNetworkKeyOff + "\n",
		},
		{
			name: "envoy capabilities block has moved",
			manifest: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cilium-config\ndata:\n  " +
				ciliumHostNetworkKeyOff + "\n\n---\napiVersion: apps/v1\nkind: DaemonSet\nmetadata:\n  labels:\n" +
				"    k8s-app: cilium-envoy\nspec:\n" + ciliumEnvoyStarterArgs,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, ok := rewriteCiliumGatewayHostNetwork([]byte(tc.manifest))
			if ok {
				t.Error("reported success with an anchor missing; a partial patch gives a Gateway that listens on nothing")
			}
			if string(out) != tc.manifest {
				t.Error("the manifest was modified even though the rewrite failed")
			}
		})
	}
}
