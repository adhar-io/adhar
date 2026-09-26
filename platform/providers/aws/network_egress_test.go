package aws

import (
	"os"
	"strings"
	"testing"
)

// A VPC that attaches an internet gateway but never routes to it is the worst
// kind of broken: the instances come up, they hold public IPs, and every
// outbound connection fails.
//
// CreateVPC used to do exactly that — CreateInternetGateway then
// AttachInternetGateway then return — so `MapPublicIpOnLaunch` on the public
// subnet made it look finished while kubeadm node prep could not reach a single
// package mirror or container registry. Found by reading the provider before the
// first live AWS run (2026-09-26), which is the cheapest place to find it.
//
// This is a source-level guard on purpose: the alternative is a live AWS account.
func TestCreateVPCRoutesEgressToTheInternetGateway(t *testing.T) {
	src, err := os.ReadFile("provider_network.go")
	if err != nil {
		t.Fatalf("reading provider_network.go: %v", err)
	}
	body := string(src)

	if !strings.Contains(body, "routeVPCEgressToGateway") {
		t.Fatal("CreateVPC must call routeVPCEgressToGateway: attaching an internet " +
			"gateway without a 0.0.0.0/0 route leaves every node without egress")
	}
	if !strings.Contains(body, "CreateRoute(") {
		t.Fatal("no ec2 CreateRoute call remains; the default route to the internet " +
			"gateway is what makes node prep able to reach package mirrors")
	}
	if !strings.Contains(body, `DestinationCidrBlock: aws.String("0.0.0.0/0")`) {
		t.Fatal(`the default route must be 0.0.0.0/0`)
	}
	// The attach and the route must both be in CreateVPC, in that order.
	// `p.` prefixed so this matches the CALL SITE and not the function
	// definition, which appears earlier in the file.
	attach := strings.Index(body, "p.ec2Client.AttachInternetGateway(ctx")
	route := strings.Index(body, "p.routeVPCEgressToGateway(ctx")
	if attach < 0 || route < 0 || route < attach {
		t.Fatalf("the egress route must be created after the gateway is attached "+
			"(attach at %d, route at %d)", attach, route)
	}
}

// Re-running `adhar up` after a partial failure must not fail on a route that is
// already there.
func TestEgressRouteToleratesAnExistingRoute(t *testing.T) {
	src, err := os.ReadFile("provider_network.go")
	if err != nil {
		t.Fatalf("reading provider_network.go: %v", err)
	}
	if !strings.Contains(string(src), "RouteAlreadyExists") {
		t.Fatal("routeVPCEgressToGateway must treat RouteAlreadyExists as success, " +
			"or a retried create fails on its own previous work")
	}
}
