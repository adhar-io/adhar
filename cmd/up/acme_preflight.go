package up

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// acmeDNS01Blocker describes why a publicly trusted certificate cannot be
// issued for a host, in terms the operator can act on.
type acmeDNS01Blocker struct {
	Reason string
	Fix    string
}

// checkACMEDNS01Ready reports whether Let's Encrypt will be able to issue a
// certificate for host, or why it will not.
//
// This exists because the failure is otherwise SILENT and slow. cert-manager
// creates the Certificate, the ACME order goes to "invalid", and the Gateway
// quietly keeps serving the self-signed fallback — no bootstrap output says
// anything is wrong, and the first sign of trouble is a browser warning or, far
// worse, an in-cluster client refusing the certificate hours later. Both
// conditions below were hit on a real deployment: the platform host resolved
// fine while the PARENT zone had been delegated to nameservers that held no
// zone for it, so every CAA lookup returned SERVFAIL and issuance could never
// succeed.
//
// A nil return means nothing stands in the way. Errors resolving are reported as
// blockers rather than swallowed: not being able to answer the question is
// itself worth telling the operator about.
func checkACMEDNS01Ready(ctx context.Context, host string) *acmeDNS01Blocker {
	if host == "" {
		return nil
	}
	resolver := &net.Resolver{}
	lookup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// 1. SOME level must serve a zone for this name. The host does not need a
	//    delegation of its own — records may simply live in the parent zone, and
	//    requiring NS on the host itself would wrongly flag a perfectly ordinary
	//    setup like cloud.google.com, which is a record inside google.com.
	served := false
	var serving []string // the nameservers that DO answer for the platform host
	for _, name := range append([]string{host}, parentDomains(host)...) {
		if ns, err := resolver.LookupNS(lookup, name); err == nil && len(ns) > 0 {
			served = true
			for _, n := range ns {
				serving = append(serving, strings.TrimSuffix(n.Host, "."))
			}
			break
		}
	}
	if !served {
		return &acmeDNS01Blocker{
			Reason: fmt.Sprintf("no nameserver serves %s or any parent of it", host),
			Fix: fmt.Sprintf("create the DNS zone for %s at your cloud provider, then add its nameservers as NS records for %q on the parent domain at your registrar",
				host, firstLabel(host)),
		}
	}

	// 2. The REGISTRABLE domain must resolve, because Let's Encrypt walks up the
	//    tree reading CAA and stops at nothing. A registrable domain that fails
	//    to answer is always broken — unlike an intermediate label, which
	//    legitimately has no zone of its own. This is the condition that bit a
	//    real deployment: cloud.adhar.io resolved perfectly while adhar.io had
	//    been delegated to nameservers holding no zone for it, so every CAA
	//    lookup failed and the ACME order went to "invalid" with an error naming
	//    CAA rather than delegation.
	if reg := registrableDomain(host); reg != "" && reg != host {
		if _, err := resolver.LookupNS(lookup, reg); err != nil {
			return &acmeDNS01Blocker{
				Reason: fmt.Sprintf("%s does not resolve (%v), so Let's Encrypt cannot read CAA records for it", reg, err),
				// Name the exact records, derived from the configured host: the
				// generic advice ("fix your DNS") is true and useless, and the
				// operator is looking at a registrar form with fields to fill in.
				// The split-zone layout below is the one that works — the apex
				// served wherever the domain is registered, and ONLY the platform
				// label delegated to the cloud zone that external-dns writes into.
				Fix: delegationFix(host, reg, serving),
			}
		}
	}
	return nil
}

// registrableDomain is the last two labels of host — the domain someone
// registered. Intermediate labels may legitimately have no zone; a registrable
// domain that does not resolve is always a fault.
func registrableDomain(host string) string {
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	if len(labels) < 2 {
		return ""
	}
	return strings.Join(labels[len(labels)-2:], ".")
}

// parentDomains lists the ancestors of host that Let's Encrypt consults for CAA,
// stopping before the public suffix (a two-label tail such as example.com or
// example.co.uk is where the useful check ends).
func parentDomains(host string) []string {
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	var out []string
	for i := 1; i < len(labels)-1; i++ {
		out = append(out, strings.Join(labels[i:], "."))
	}
	return out
}

// firstLabel is the subdomain label an operator delegates at the registrar:
// "cloud" for cloud.example.com.
func firstLabel(host string) string {
	return strings.SplitN(strings.TrimSuffix(host, "."), ".", 2)[0]
}

// delegationFix spells out the split-zone DNS the platform needs, for whatever
// host the operator configured.
//
// Everything here is derived: the platform host, its registrable domain and the
// nameservers that already answer for the platform zone. Nothing about the
// platform's own domain is assumed, because the host comes from
// globalSettings.defaultHost and may be anything.
func delegationFix(host, registrable string, platformNameservers []string) string {
	label := firstLabel(host)
	ns := "the nameservers your cloud DNS zone lists"
	if len(platformNameservers) > 0 {
		ns = strings.Join(platformNameservers, ", ")
	}
	return fmt.Sprintf(
		"serve %s at your registrar (its default nameservers are fine), and inside that zone delegate ONLY %q to the platform zone: add NS records for %q pointing at %s. "+
			"Add those NS records BEFORE switching the nameservers, so %s keeps resolving through the change. "+
			"Nothing about the platform zone changes — external-dns and the DNS-01 solver keep writing into it",
		registrable, label, label, ns, host)
}
