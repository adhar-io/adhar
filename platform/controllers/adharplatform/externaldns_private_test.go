package adharplatform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// external-dns must never publish an RFC1918 address into the platform's public
// zone, and the reason is the combination of two of its own settings.
//
// Its sources report whatever address the object carries, and on a cluster with
// no cloud load balancer that can be the nodes' own private IPs — a Gateway in
// Cilium's host-network mode reports 192.168.x.y. With --policy=upsert-only it
// never retracts a record. So one reconcile is enough to leave a public zone
// answering with addresses no client can route, which is the worst failure shape
// available: DNS resolves, nothing connects, and the URL looks merely broken.
//
// Measured on a live Civo compute cluster (2026-10-06): 140 A records for
// *.hub.adhar.io pointing at 192.168.1.2-5, which had to be deleted through the
// provider's API because external-dns would not remove them.
func TestExternalDNSNeverPublishesPrivateAddresses(t *testing.T) {
	path := filepath.Join(stackPackagesDir(t), "application/external-dns/manifests/deployment.yaml.tmpl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)

	// The whole of RFC1918. Leaving one out leaves the hole open for clusters
	// that happen to use that range.
	for _, net := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
		if !strings.Contains(src, "--exclude-target-net="+net) {
			t.Errorf("external-dns can still publish %s addresses into the public zone", net)
		}
	}

	// The exclusions only matter because nothing retracts a bad record; if the
	// policy ever becomes `sync`, this test's reasoning should be revisited
	// rather than silently relied upon.
	if !strings.Contains(src, "--policy=upsert-only") {
		t.Log("note: --policy is no longer upsert-only; re-read why these exclusions exist")
	}
}
