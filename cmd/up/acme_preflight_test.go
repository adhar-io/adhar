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
	if b := checkACMEDNS01Ready(context.Background(), ""); b != nil {
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
