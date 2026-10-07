package adharplatform

import (
	"strings"
	"testing"
)

// The Corefile this edits is cluster DNS for everything, so the surgery is a
// pure function and these are its guard rails.
//
// Why it exists: a cloud cluster with no cloud-controller-manager (Civo compute
// mode) never gets a LoadBalancer address, so nothing publishes an A record and
// `*.<host>` is NXDOMAIN inside the cluster too. Every oauth2-proxy does OIDC
// Discovery against https://keycloak.<host>/... at STARTUP and exits when that
// lookup fails. Measured on a live bring-up (2026-10-06): twenty-odd SSO
// proxies in CrashLoopBackOff on an otherwise healthy platform, fixed by
// exactly this rewrite — verified by hand before it was code.
const stockCorefile = `.:53 {
    errors
    health {
       lameduck 5s
    }
    ready
    kubernetes cluster.local in-addr.arpa ip6.arpa {
       pods insecure
       fallthrough in-addr.arpa ip6.arpa
       ttl 30
    }
    prometheus :9153
    forward . /etc/resolv.conf {
       max_concurrent 1000
    }
    cache 30
    loop
    reload
    loadbalance
}
`

func TestInsertGatewayRewriteTargetsTheGatewayBeforeForward(t *testing.T) {
	out, changed := insertGatewayRewrite(stockCorefile, "hub.adhar.io", gatewayServiceFQDN)
	if !changed {
		t.Fatal("no rewrite inserted: the platform's own hostnames stay NXDOMAIN and every SSO proxy crash-loops")
	}
	if !strings.Contains(out, gatewayServiceFQDN) {
		t.Errorf("the rewrite does not target the gateway Service (%s)", gatewayServiceFQDN)
	}
	// The host's dots must be escaped, or the regex matches far too much.
	if !strings.Contains(out, `name regex (.*)\.hub\.adhar\.io `) {
		t.Errorf("host dots are not escaped in the regex:\n%s", out)
	}
	// Order matters: `rewrite stop` has to run BEFORE the query is forwarded.
	rw := strings.Index(out, "rewrite stop")
	fwd := strings.Index(out, "forward . /etc/resolv.conf")
	if rw < 0 || fwd < 0 || rw > fwd {
		t.Errorf("the rewrite must come before `forward` (rewrite at %d, forward at %d)", rw, fwd)
	}
	// Everything the cluster already relied on must survive verbatim.
	for _, keep := range []string{"kubernetes cluster.local", "forward . /etc/resolv.conf", "prometheus :9153", "loadbalance"} {
		if !strings.Contains(out, keep) {
			t.Errorf("the edit dropped %q from the Corefile — that is cluster DNS for everything", keep)
		}
	}
	if n := strings.Count(out, "forward . /etc/resolv.conf"); n != 1 {
		t.Errorf("forward appears %d times; the block was duplicated", n)
	}
}

func TestInsertGatewayRewriteIsIdempotentAndCautious(t *testing.T) {
	once, _ := insertGatewayRewrite(stockCorefile, "hub.adhar.io", gatewayServiceFQDN)
	twice, changed := insertGatewayRewrite(once, "hub.adhar.io", gatewayServiceFQDN)
	if changed || twice != once {
		t.Error("a second pass changed the Corefile again; every reconcile would stack another rewrite")
	}
	if _, changed := insertGatewayRewrite(stockCorefile, "", gatewayServiceFQDN); changed {
		t.Error("an empty host must not produce a rewrite")
	}
	// An unrecognised layout is left alone rather than guessed at.
	if _, changed := insertGatewayRewrite(".:53 {\n    errors\n}\n", "hub.adhar.io", gatewayServiceFQDN); changed {
		t.Error("a Corefile with no forward directive must be left untouched")
	}
}
