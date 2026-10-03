package domain

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
)

// The external-dns registry owner id is the one piece of DNS state that cannot
// be derived safely from configuration alone: it is recorded IN THE ZONE, and
// whoever disagrees with that recording loses every record.
//
// external-dns writes a TXT record beside each A record saying who owns it, and
// refuses to modify a record owned by someone else. The package also runs
// `--policy=upsert-only`, so it never deletes either. An owner id that does not
// match what the zone already says therefore produces a platform where every
// hostname resolves to a PREVIOUS cluster's load-balancer IP while external-dns
// logs "All records are already up to date" once a minute — nothing is wrong
// from its point of view, because it manages nothing.
//
// Measured on the 2026-10-03 GCP zone: 55 A records across two dead IPs and 110
// TXT records owned by `adhar-prod` (86) and `adhar-production` (24), against a
// live external-dns whose owner was `adhar-cloud-adhar-io`. Not one record was
// owned by the running platform, so not one could be corrected. Both the dead
// IPs and the stale cert followed from that: with the hostnames pointing
// elsewhere, ACME could not validate, so the gateway kept serving a self-signed
// certificate.
//
// Deriving the id from the HOST rather than the cluster name (see
// BuildCustomizationSpec.TXTOwnerID) stops a cluster rename from orphaning a
// zone, but it cannot repair a zone that was already stamped with a different
// id — changing the derivation IS a rename, which is what produced the state
// above. So the zone's own recording wins: if the records already name an Adhar
// owner, adopt it. That makes the invariant self-maintaining rather than
// dependent on the derivation never changing again.

// ownerProbeNames are hostnames the platform publishes on every profile, tried
// in order. Only one has to answer; the first Adhar owner found is the zone's.
var ownerProbeNames = []string{"argocd", "gitea", "console", "grafana", "keycloak"}

// ownerProbeTimeout bounds the whole probe. This runs in the bootstrap path, so
// an unreachable resolver must cost seconds, not minutes — and falling back to
// the derived id is always correct for a zone that has no recording yet.
const ownerProbeTimeout = 3 * time.Second

type ownerCacheEntry struct {
	id   string
	once sync.Once
}

var (
	ownerCacheMu sync.Mutex
	ownerCache   = map[string]*ownerCacheEntry{}
)

// lookupTXT is indirected so tests can answer without a resolver.
var lookupTXT = func(ctx context.Context, name string) ([]string, error) {
	return net.DefaultResolver.LookupTXT(ctx, name)
}

// ResolveTXTOwnerID returns the external-dns owner id to run with: the one the
// zone already records, when it records an Adhar owner, and otherwise the id
// derived from the spec.
//
// Memoised per host — stageStack runs once per bootstrap and again for
// `adhar upgrade --diff-only`, and the answer cannot change under us.
func ResolveTXTOwnerID(spec v1alpha1.BuildCustomizationSpec) string {
	desired := spec.TXTOwnerID()

	host := strings.Trim(strings.TrimSpace(strings.ToLower(spec.Host)), ".")
	// No real zone to consult: a local platform runs external-dns with
	// `--provider=inmemory`, where ownership is a fiction that dies with the
	// process, and probing localtest.me would be a pointless network round trip.
	if host == "" || host == globals.DefaultHostName || spec.ExternalDNSProvider() == "inmemory" {
		return desired
	}

	ownerCacheMu.Lock()
	entry, ok := ownerCache[host]
	if !ok {
		entry = &ownerCacheEntry{}
		ownerCache[host] = entry
	}
	ownerCacheMu.Unlock()

	entry.once.Do(func() {
		entry.id = probeZoneOwner(host, desired)
	})
	return entry.id
}

func probeZoneOwner(host, desired string) string {
	ctx, cancel := context.WithTimeout(context.Background(), ownerProbeTimeout)
	defer cancel()

	for _, p := range ownerProbeNames {
		// external-dns has written the registry TXT at the record's own name
		// historically and at an `a-` prefixed name since the newer format, and a
		// zone can hold both. Either answers the question.
		for _, name := range []string{p + "." + host, "a-" + p + "." + host} {
			txts, err := lookupTXT(ctx, name)
			if err != nil {
				if ctx.Err() != nil {
					return desired
				}
				continue
			}
			if owner, found := externalDNSOwner(txts); found && isAdharOwner(owner) {
				return owner
			}
		}
	}
	return desired
}

// externalDNSOwner extracts the owner from an external-dns heritage TXT value.
func externalDNSOwner(txts []string) (string, bool) {
	const key = "external-dns/owner="
	for _, txt := range txts {
		if !strings.Contains(txt, "heritage=external-dns") {
			continue
		}
		for _, field := range strings.Split(txt, ",") {
			field = strings.TrimSpace(field)
			if v, ok := strings.CutPrefix(field, key); ok {
				if v = strings.Trim(strings.TrimSpace(v), `"`); v != "" {
					return v, true
				}
			}
		}
	}
	return "", false
}

// isAdharOwner reports whether an owner id was written by an Adhar platform.
//
// Adopting an arbitrary owner would let this platform take over another tool's
// records in a shared zone, which is a worse failure than the one being fixed:
// stale records are visible and recoverable, hijacked ones are neither. Every
// id this project has ever produced is "adhar" or "adhar-<something>".
func isAdharOwner(owner string) bool {
	return owner == "adhar" || strings.HasPrefix(owner, "adhar-")
}

// resetOwnerCache clears the memoised answers. For tests only.
func resetOwnerCache() {
	ownerCacheMu.Lock()
	defer ownerCacheMu.Unlock()
	ownerCache = map[string]*ownerCacheEntry{}
}
