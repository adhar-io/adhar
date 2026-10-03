package domain

import (
	"context"
	"errors"
	"testing"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
)

const heritage = "heritage=external-dns,external-dns/owner=%s,external-dns/resource=httproute/adhar-system/argocd"

// withLookup installs a fake resolver and a clean cache for one test.
func withLookup(t *testing.T, answers map[string][]string) *int {
	t.Helper()
	calls := 0
	prev := lookupTXT
	lookupTXT = func(_ context.Context, name string) ([]string, error) {
		calls++
		if v, ok := answers[name]; ok {
			return v, nil
		}
		return nil, errors.New("NXDOMAIN")
	}
	resetOwnerCache()
	t.Cleanup(func() { lookupTXT = prev; resetOwnerCache() })
	return &calls
}

func cloudSpec(host string) v1alpha1.BuildCustomizationSpec {
	s := v1alpha1.BuildCustomizationSpec{Protocol: "https", Host: host, Port: "443",
		Email: "ops@example.io", DNSProvider: "gcp", ClusterName: "prod"}
	s.Normalize()
	return s
}

// The zone's own recording wins over the derived id.
//
// This is the 2026-10-03 GCP outage: the zone held 110 TXT records owned by
// `adhar-prod` and `adhar-production` while the running external-dns called
// itself `adhar-cloud-adhar-io`. Owning nothing, it could neither update the 55
// A records pointing at two dead load-balancer IPs nor delete them
// (`--policy=upsert-only`), and reported "All records are already up to date"
// once a minute. Every platform URL was unreachable, and because the hostnames
// resolved elsewhere ACME could not validate either, so the gateway served a
// self-signed certificate on top.
func TestResolveTXTOwnerIDAdoptsTheOwnerAlreadyInTheZone(t *testing.T) {
	spec := cloudSpec("cloud.adhar.io")
	derived := spec.TXTOwnerID()
	if derived != "adhar-cloud-adhar-io" {
		t.Fatalf("precondition: derived owner is %q", derived)
	}

	withLookup(t, map[string][]string{
		"argocd.cloud.adhar.io": {"v=spf1 -all", fmtHeritage("adhar-prod")},
	})

	if got := ResolveTXTOwnerID(spec); got != "adhar-prod" {
		t.Errorf("owner must be adopted from the zone, got %q want %q — disagreeing with the "+
			"zone leaves every record unmanageable", got, "adhar-prod")
	}
}

// The newer external-dns TXT format writes the registry record at an `a-`
// prefixed name, and a zone can hold either shape.
func TestResolveTXTOwnerIDFindsThePrefixedRegistryRecord(t *testing.T) {
	spec := cloudSpec("cloud.adhar.io")
	withLookup(t, map[string][]string{
		"a-gitea.cloud.adhar.io": {fmtHeritage("adhar-production")},
	})
	if got := ResolveTXTOwnerID(spec); got != "adhar-production" {
		t.Errorf("got %q, want %q", got, "adhar-production")
	}
}

// A zone with no Adhar recording yet — a first bring-up — keeps the derived id,
// which is what seeds the zone.
func TestResolveTXTOwnerIDFallsBackWhenTheZoneSaysNothing(t *testing.T) {
	spec := cloudSpec("fresh.adhar.io")
	withLookup(t, map[string][]string{})
	if got := ResolveTXTOwnerID(spec); got != spec.TXTOwnerID() {
		t.Errorf("a zone with no recording must use the derived owner, got %q want %q",
			got, spec.TXTOwnerID())
	}
}

// Adopting an arbitrary owner would hand this platform another tool's records
// in a shared zone. Stale records are visible and recoverable; hijacked ones
// are neither, so a foreign owner is never adopted.
func TestResolveTXTOwnerIDNeverAdoptsAForeignOwner(t *testing.T) {
	spec := cloudSpec("shared.example.io")
	withLookup(t, map[string][]string{
		"argocd.shared.example.io": {fmtHeritage("some-other-platform")},
	})
	if got := ResolveTXTOwnerID(spec); got != spec.TXTOwnerID() {
		t.Errorf("a non-Adhar owner must not be adopted, got %q", got)
	}
}

// A TXT record that is not an external-dns registry record must not be mined
// for an owner.
func TestResolveTXTOwnerIDIgnoresUnrelatedTXTRecords(t *testing.T) {
	spec := cloudSpec("cloud.adhar.io")
	withLookup(t, map[string][]string{
		"argocd.cloud.adhar.io": {"google-site-verification=abc", "external-dns/owner=adhar-prod"},
	})
	if got := ResolveTXTOwnerID(spec); got != spec.TXTOwnerID() {
		t.Errorf("without a heritage marker the value is not a registry record, got %q", got)
	}
}

// The local platform must never touch the network.
//
// It runs external-dns with `--provider=inmemory`, where ownership dies with
// the process, so probing is pure latency in the path every `adhar up` takes.
func TestResolveTXTOwnerIDDoesNotProbeLocally(t *testing.T) {
	for _, spec := range []v1alpha1.BuildCustomizationSpec{
		func() v1alpha1.BuildCustomizationSpec {
			s := v1alpha1.BuildCustomizationSpec{Protocol: "https", Host: globals.DefaultHostName, Port: "8443"}
			s.Normalize()
			return s
		}(),
		// A real host but no DNS provider: external-dns is still inmemory.
		func() v1alpha1.BuildCustomizationSpec {
			s := v1alpha1.BuildCustomizationSpec{Protocol: "https", Host: "cloud.adhar.io", Port: "443"}
			s.Normalize()
			return s
		}(),
	} {
		calls := withLookup(t, map[string][]string{
			"argocd." + spec.Host: {fmtHeritage("adhar-prod")},
		})
		if got := ResolveTXTOwnerID(spec); got != spec.TXTOwnerID() {
			t.Errorf("host %q: got %q, want the derived %q", spec.Host, got, spec.TXTOwnerID())
		}
		if *calls != 0 {
			t.Errorf("host %q: made %d DNS lookups; the local path must make none", spec.Host, *calls)
		}
	}
}

// The probe is memoised: staging runs during bootstrap and again for
// `adhar upgrade --diff-only`, and the zone cannot change under us.
func TestResolveTXTOwnerIDIsMemoisedPerHost(t *testing.T) {
	spec := cloudSpec("cloud.adhar.io")
	calls := withLookup(t, map[string][]string{
		"argocd.cloud.adhar.io": {fmtHeritage("adhar-prod")},
	})
	for i := 0; i < 5; i++ {
		if got := ResolveTXTOwnerID(spec); got != "adhar-prod" {
			t.Fatalf("call %d returned %q", i, got)
		}
	}
	if *calls != 1 {
		t.Errorf("expected 1 lookup across 5 resolutions, made %d", *calls)
	}
}

func fmtHeritage(owner string) string {
	return "heritage=external-dns,external-dns/owner=" + owner +
		",external-dns/resource=httproute/adhar-system/argocd"
}
