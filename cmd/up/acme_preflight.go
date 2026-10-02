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
func checkACMEDNS01Ready(ctx context.Context, host, dnsProvider string) *acmeDNS01Blocker {
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
	servingZone := ""    // and the name they answer for, which matters in check 3
	for _, name := range append([]string{host}, parentDomains(host)...) {
		if ns, err := resolver.LookupNS(lookup, name); err == nil && len(ns) > 0 {
			served = true
			servingZone = name
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
	// 3. The nameservers answering for the platform host must belong to the
	//    configured DNS provider.
	//
	//    This is what the two checks above cannot see, and it is what left a real
	//    cluster with no working URL at all. `do.adhar.io` had no delegation; check 1
	//    walked UP to `adhar.io`, found the registrar's nameservers and concluded a
	//    zone served the host, and check 2 confirmed the registrable domain resolved.
	//    Both were true — and nothing resolved, because external-dns was writing the
	//    platform's records into a DigitalOcean zone while every query for the host
	//    went to GoDaddy. Records in a zone nobody is asked about.
	//
	//    Comparing the ANSWERING nameservers against the provider catches it at any
	//    level: hosting the whole apex at the provider still passes, because then the
	//    registrable domain's own nameservers carry the provider's mark.
	if mark, known := providerNameserverMark(dnsProvider); known && len(serving) > 0 {
		if !anyNameserverMatches(serving, mark) {
			return &acmeDNS01Blocker{
				Reason: fmt.Sprintf(
					"queries for %s are answered by %s (zone %q), which is not %s DNS — external-dns writes the platform's records into your %s zone, so nothing ever asks the nameservers that hold them",
					host, strings.Join(serving, ", "), servingZone, dnsProvider, dnsProvider),
				Fix: subdomainDelegationFix(host, registrableDomain(host), dnsProvider),
			}
		}
	}

	return nil
}

// subdomainDelegationFix is the remedy for the ONE missing delegation: the
// registrable domain already resolves at the registrar, and only the platform's
// own label is not pointed at the cloud zone.
//
// It is separate from delegationFix because that one is written for a broken APEX
// and ends with "add those NS records BEFORE switching the nameservers, so the
// host keeps resolving through the change". There is no nameserver switch here —
// the apex is fine and stays exactly where it is — so that sentence sent the
// reader looking for a migration they are not performing. One action, named
// precisely, is the whole message.
func subdomainDelegationFix(host, registrable, dnsProvider string) string {
	label := firstLabel(host)
	target := "the nameservers your " + dnsProvider + " DNS zone lists"
	if ns, ok := providerNameservers(dnsProvider); ok {
		target = strings.Join(ns, ", ")
	}
	return fmt.Sprintf(
		"in the %s zone at your registrar, add NS records for the name %q pointing at %s. "+
			"That is the only change: %s stays where it is, and the %s zone already holds the platform's records",
		registrable, label, target, registrable, dnsProvider)
}

// providerNameserverMark is a substring every nameserver of that provider's
// hosted zones carries. The second return is false where there is no stable mark,
// and the check is then skipped rather than guessed at.
//
// A substring rather than an exact set because the hostnames vary per zone —
// Route 53 hands out ns-1234.awsdns-56.org, Cloud DNS ns-cloud-c1.googledomains.com
// — while the vendor marker is stable.
func providerNameserverMark(dnsProvider string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(dnsProvider)) {
	case "digitalocean", "do":
		return "digitalocean.com", true
	case "aws", "route53":
		return "awsdns", true
	case "gcp", "google", "clouddns":
		return "googledomains.com", true
	case "azure":
		return "azure-dns", true
	case "cloudflare":
		return "ns.cloudflare.com", true
	case "civo":
		// Civo serves zones from ns0/ns1.civo.com. Checked now that Civo is a
		// first-class DNS-01 provider (a webhook solver ships with cert-manager):
		// a Civo zone that is not delegated fails issuance as surely as any other.
		return "civo.com", true
	default:
		// Includes "" and "none": nothing is claimed about the zone, so there is
		// nothing to verify.
		return "", false
	}
}

// providerNameservers are the EXACT nameservers to delegate to, for the providers
// that use one fixed set for every zone.
//
// It matters because the remedy is a form at a registrar with boxes to fill in.
// "point at the nameservers your cloud DNS zone lists" is correct and useless: it
// sends the reader back to a console to look up something this already knows.
// DigitalOcean and Civo publish a fixed set, so name them.
//
// AWS, GCP, Azure and Cloudflare assign nameservers PER ZONE — Route 53 hands out
// ns-331.awsdns-41.com for one zone and something else for the next — so there is
// nothing truthful to print, and those keep the generic wording rather than a
// plausible-looking guess.
func providerNameservers(dnsProvider string) ([]string, bool) {
	switch strings.ToLower(strings.TrimSpace(dnsProvider)) {
	case "digitalocean", "do":
		return []string{"ns1.digitalocean.com", "ns2.digitalocean.com", "ns3.digitalocean.com"}, true
	case "civo":
		return []string{"ns0.civo.com", "ns1.civo.com"}, true
	default:
		return nil, false
	}
}

// anyNameserverMatches reports whether at least one answering nameserver carries
// the provider's mark. One is enough: a zone mid-migration can list both the old
// and the new set, and that is a working state rather than a fault.
func anyNameserverMatches(serving []string, mark string) bool {
	for _, ns := range serving {
		if strings.Contains(strings.ToLower(ns), mark) {
			return true
		}
	}
	return false
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
