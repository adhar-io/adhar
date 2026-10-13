package adharplatform

import (
	"os"
	"strings"
	"testing"

	"adhar-io/adhar/api/v1alpha1"
	"adhar-io/adhar/globals"
	"adhar-io/adhar/platform/utils/files"
)

// A scaffolded app is published at <app>.<env>.<host>, and the Gateway must
// terminate TLS for every environment in the chain.
//
// A TLS wildcard matches exactly ONE label, so the base `*.<host>` listener
// does NOT cover `my-service.dev.<host>`. Without a listener per environment the
// route resolves and then serves a certificate for the wrong name — the same
// class of fault as a missing DNS record and considerably harder to read, since
// the URL answers.
//
// cert-manager's gateway support derives adhar-cert's dnsNames from these
// listener hostnames, so a listener is the only thing needed for the
// certificate to cover an environment. That is also why this test checks the
// listeners rather than the Certificate: the listeners are the source.
func TestTheGatewayTerminatesTLSForEveryEnvironmentHostname(t *testing.T) {
	raw, err := gatewayFS.ReadFile("resources/gateway/gateway-cloud.yaml")
	if err != nil {
		t.Fatalf("reading the cloud gateway: %v", err)
	}
	const host = "example.test"
	out, err := files.ApplyTemplate(raw, v1alpha1.BuildCustomizationSpec{Host: host, DNSProvider: "digitalocean"})
	if err != nil {
		t.Fatalf("rendering the cloud gateway: %v", err)
	}
	rendered := string(out)

	// The base wildcard must stay: the platform's own services (argocd, gitea,
	// console) are published directly under <host> with no environment.
	if !strings.Contains(rendered, `hostname: "*.`+host+`"`) {
		t.Error("the base *.<host> listener is gone; every platform service URL terminates on it")
	}

	envs := globals.PlatformEnvironments()
	if len(envs) == 0 {
		t.Fatal("no canonical environments; this guard is checking nothing")
	}
	for _, env := range envs {
		want := `hostname: "*.` + env + "." + host + `"`
		if !strings.Contains(rendered, want) {
			t.Errorf("the cloud Gateway has no listener for %s; an app published at "+
				"<app>.%s.%s would serve the wrong certificate, because *.%s covers one label only",
				want, env, host, host)
		}
	}
}

// The scaffolder must put the environment IN the hostname.
//
// Without it every environment of every app competes for one name:
// `my-service.<host>` cannot be both the dev copy and the prod copy, so the
// promotion chain has nowhere to put the other two. The ml template's own
// comment says the same thing — "give each environment its own HTTPRoute
// hostname, since three namespaces cannot share one".
//
// Asserted on the source with comments stripped, so the prose above cannot
// satisfy it.
func TestTheScaffoldedHostnameCarriesTheEnvironment(t *testing.T) {
	b, err := os.ReadFile("../../../cmd/apps/scaffold.go")
	if err != nil {
		t.Fatalf("reading the scaffolder: %v", err)
	}
	code := withoutComments(string(b))

	if !strings.Contains(code, `values["hostname"] = appName + "." + env + "." + base`) {
		t.Error("the scaffolder no longer builds the hostname as <app>.<env>.<base>; a new app then " +
			"takes a name that every other environment of the same app also wants")
	}
	if strings.Contains(code, `values["hostname"] = appName + "." + base`) {
		t.Error("the scaffolder still has the environment-less hostname form")
	}

	// And the environment must be one the Gateway actually serves, or the app
	// gets a URL that answers with the wrong certificate.
	d, err := os.ReadFile("../../../cmd/apps/deploy.go")
	if err != nil {
		t.Fatal(err)
	}
	dcode := withoutComments(string(d))
	if !strings.Contains(dcode, "globals.PlatformEnvironments()") {
		t.Error("the deploy command does not validate --env against the canonical environments, so a " +
			"typo reaches the hostname and the Gateway has no listener for it")
	}
	if !strings.Contains(dcode, "globals.DefaultEnvironment()") {
		t.Error("the deploy command has no default environment; nothing is promoted into dev, so an " +
			"app that has never been promoted belongs there")
	}
}
