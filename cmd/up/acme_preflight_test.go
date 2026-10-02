package up

import (
	"context"
	"strings"
	"testing"
)

// parentDomains drives the CAA half of the check. Let's Encrypt walks UP the
// tree reading CAA, so a parent that fails to resolve blocks issuance for every
// name beneath it — which is exactly what happened on the first real GCP
// deployment: cloud.adhar.io resolved perfectly while adhar.io had been
// delegated to nameservers holding no zone for it, so CAA lookups returned
// SERVFAIL and Let's Encrypt refused with a message naming CAA, not delegation.
func TestParentDomainsStopsBeforeThePublicSuffix(t *testing.T) {
	cases := map[string][]string{
		"cloud.adhar.io":  {"adhar.io"},
		"a.b.example.com": {"b.example.com", "example.com"},
		"example.com":     nil,
		"cloud.adhar.io.": {"adhar.io"},
	}
	for host, want := range cases {
		got := parentDomains(host)
		if len(got) != len(want) {
			t.Errorf("parentDomains(%q) = %v, want %v", host, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("parentDomains(%q)[%d] = %q, want %q", host, i, got[i], want[i])
			}
		}
	}
}

// firstLabel is the label the operator adds at the registrar, so the remedy
// text names the right record.
func TestFirstLabelNamesTheRecordToCreate(t *testing.T) {
	for host, want := range map[string]string{
		"cloud.adhar.io":   "cloud",
		"platform.acme.io": "platform",
		"example.com":      "example",
	} {
		if got := firstLabel(host); got != want {
			t.Errorf("firstLabel(%q) = %q, want %q", host, got, want)
		}
	}
}

// An empty host must not be reported as a blocker: local builds have no real
// hostname and never request a public certificate.
func TestACMEPreflightIgnoresAnEmptyHost(t *testing.T) {
	if b := checkACMEDNS01Ready(context.Background(), "", "digitalocean"); b != nil {
		t.Errorf("an empty host must not be treated as a blocker, got %+v", b)
	}
}

// registrableDomain is what separates a real fault from a normal shape. An
// intermediate label may legitimately have no zone of its own — cloud.google.com
// is a record inside google.com — so requiring a delegation at every level would
// wrongly flag ordinary setups. A registrable domain that does not resolve is
// always broken.
func TestRegistrableDomain(t *testing.T) {
	for host, want := range map[string]string{
		"cloud.adhar.io":  "adhar.io",
		"a.b.example.com": "example.com",
		"example.com":     "example.com",
		"localhost":       "",
	} {
		if got := registrableDomain(host); got != want {
			t.Errorf("registrableDomain(%q) = %q, want %q", host, got, want)
		}
	}
}

// The advice must be built from the CONFIGURED host, never from a domain baked
// into the binary: two operators on two domains must each be told about their own.
func TestDelegationAdviceIsDerivedFromTheConfiguredHost(t *testing.T) {
	t.Parallel()
	cases := []struct {
		host, registrable string
		nameservers       []string
		wants             []string
		forbids           []string
	}{
		{
			host:        "cloud.adhar.io",
			registrable: "adhar.io",
			nameservers: []string{"ns-cloud-e1.googledomains.com", "ns-cloud-e2.googledomains.com"},
			wants:       []string{"adhar.io", `"cloud"`, "ns-cloud-e1.googledomains.com", "ns-cloud-e2.googledomains.com", "cloud.adhar.io"},
		},
		{
			host:        "platform.example.co",
			registrable: "example.co",
			nameservers: []string{"ns1.digitalocean.com"},
			wants:       []string{"example.co", `"platform"`, "ns1.digitalocean.com"},
			// Another operator's domain must never appear in their advice.
			forbids: []string{"adhar.io", "googledomains"},
		},
		{
			// Nameservers unknown (the zone does not exist yet): the advice must
			// still be actionable rather than naming a made-up server.
			host:        "idp.internal.dev",
			registrable: "internal.dev",
			nameservers: nil,
			wants:       []string{"internal.dev", `"idp"`, "cloud DNS zone"},
		},
	}
	for _, c := range cases {
		got := delegationFix(c.host, c.registrable, c.nameservers)
		for _, want := range c.wants {
			if !strings.Contains(got, want) {
				t.Errorf("advice for %s is missing %q:\n%s", c.host, want, got)
			}
		}
		for _, forbid := range c.forbids {
			if strings.Contains(got, forbid) {
				t.Errorf("advice for %s leaked %q:\n%s", c.host, forbid, got)
			}
		}
		// The ordering warning matters: switching nameservers before the delegation
		// records exist takes the platform offline for the propagation window.
		if !strings.Contains(got, "BEFORE") {
			t.Errorf("advice for %s does not warn about the order of operations:\n%s", c.host, got)
		}
	}
}

// The nameservers answering for the platform host must belong to the configured
// DNS provider. Nothing checked this, and it is what left a DigitalOcean cluster
// with no working URL at all: `do.adhar.io` had no delegation, so queries went to
// the registrar (GoDaddy) while external-dns wrote the platform's records into a
// DigitalOcean zone. The two older checks both passed — a zone did serve the host
// (adhar.io), and the registrable domain did resolve — and nothing resolved.
func TestProviderNameserverMarkCoversTheSupportedBackends(t *testing.T) {
	for _, tc := range []struct {
		provider string
		ns       string
		want     bool
	}{
		{"digitalocean", "ns1.digitalocean.com", true},
		{"digitalocean", "ns59.domaincontrol.com", false}, // the real failure
		{"aws", "ns-1234.awsdns-56.org", true},
		{"route53", "ns-7.awsdns-01.co.uk", true},
		{"gcp", "ns-cloud-c1.googledomains.com", true},
		{"azure", "ns1-05.azure-dns.com", true},
		{"cloudflare", "amy.ns.cloudflare.com", true},
	} {
		t.Run(tc.provider+"/"+tc.ns, func(t *testing.T) {
			mark, known := providerNameserverMark(tc.provider)
			if !known {
				t.Fatalf("provider %q should have a known nameserver mark", tc.provider)
			}
			if got := anyNameserverMatches([]string{tc.ns}, mark); got != tc.want {
				t.Errorf("anyNameserverMatches(%q, %q) = %v, want %v", tc.ns, mark, got, tc.want)
			}
		})
	}
}

// Where there is no stable marker the check must be SKIPPED, not guessed —
// a false alarm on a working cluster is worse than no check.
func TestUnknownDNSProvidersSkipTheNameserverCheck(t *testing.T) {
	// civo is deliberately NOT here any more: it became a first-class DNS-01
	// provider when the webhook solver shipped, so its delegation IS checked.
	for _, provider := range []string{"", "none", "something-else"} {
		if _, known := providerNameserverMark(provider); known {
			t.Errorf("provider %q must not claim a nameserver mark", provider)
		}
	}
}

// A zone mid-migration lists both the old and the new nameservers. That is a
// working state, so one match is enough.
func TestOneMatchingNameserverIsEnough(t *testing.T) {
	mark, _ := providerNameserverMark("digitalocean")
	mixed := []string{"ns59.domaincontrol.com", "ns1.digitalocean.com"}
	if !anyNameserverMatches(mixed, mark) {
		t.Error("a zone listing both the old and new nameservers is mid-migration, not broken")
	}
}

// Case must not matter: nameservers come back from DNS in whatever case the zone
// published them.
func TestNameserverMatchIsCaseInsensitive(t *testing.T) {
	mark, _ := providerNameserverMark("digitalocean")
	if !anyNameserverMatches([]string{"NS1.DigitalOcean.COM"}, mark) {
		t.Error("matching must be case-insensitive")
	}
}

// The remedy must name the exact nameservers where the provider has a fixed set.
// "point at the nameservers your cloud DNS zone lists" is correct and useless: the
// reader is looking at a registrar form with boxes to fill in, and the tool
// already knows what goes in them.
func TestRemedyNamesExactNameserversWhereItCan(t *testing.T) {
	for _, tc := range []struct {
		provider string
		want     string
	}{
		{"digitalocean", "ns1.digitalocean.com"},
		{"civo", "ns0.civo.com"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			got := subdomainDelegationFix("do.example.com", "example.com", tc.provider)
			if !strings.Contains(got, tc.want) {
				t.Errorf("remedy for %s should name %s:\n%s", tc.provider, tc.want, got)
			}
			if strings.Contains(got, "nameservers your") {
				t.Errorf("remedy for %s fell back to the vague wording:\n%s", tc.provider, got)
			}
		})
	}

	// Route 53, Cloud DNS, Azure DNS and Cloudflare assign nameservers PER ZONE, so
	// there is nothing truthful to print — the generic wording is correct there and
	// a plausible-looking guess would be worse than vague.
	for _, provider := range []string{"aws", "gcp", "azure", "cloudflare"} {
		got := subdomainDelegationFix("do.example.com", "example.com", provider)
		if !strings.Contains(got, "nameservers your") {
			t.Errorf("%s assigns per-zone nameservers; the remedy must not invent any:\n%s", provider, got)
		}
	}
}

// The remedy for a missing SUBDOMAIN delegation must not talk about switching
// nameservers. The apex is fine and stays where it is; mentioning a migration
// sent the reader looking for work they are not doing.
func TestSubdomainRemedyDoesNotMentionSwitchingNameservers(t *testing.T) {
	got := subdomainDelegationFix("do.adhar.io", "adhar.io", "digitalocean")
	for _, unwanted := range []string{"BEFORE switching", "switching the nameservers"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("the subdomain remedy must not mention a nameserver switch:\n%s", got)
		}
	}
	if !strings.Contains(got, "only change") {
		t.Errorf("the remedy should say it is the only change:\n%s", got)
	}
}
