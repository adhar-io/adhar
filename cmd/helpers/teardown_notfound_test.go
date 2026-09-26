package helpers

import (
	"errors"
	"strings"
	"testing"
)

// A teardown may only treat "not found" as "already gone" when every configured
// provider actually answered. This is the guard for a real incident: `adhar down
// -f config.yaml --env production` reported "✓ Successfully tore down Adhar
// platform! Cloud resources for production have been removed" while five GCE
// instances, 79 disks, a VPC, 11 firewall rules and a load balancer kept running,
// because that file configures only `kind` and the GCP project was never queried.
func TestNotFoundIsOnlyConclusiveWhenEveryProviderAnswered(t *testing.T) {
	cases := []struct {
		name       string
		err        *NotFoundError
		conclusive bool
	}{
		{
			name:       "every provider answered and none had it",
			err:        &NotFoundError{Name: "production", Searched: []string{"gcp", "kind"}},
			conclusive: true,
		},
		{
			name: "a provider could not be consulted",
			err: &NotFoundError{
				Name:     "production",
				Searched: []string{"kind"},
				Failures: []LookupFailure{{Provider: "gcp", Reason: "credentials not found"}},
			},
			conclusive: false,
		},
		{
			name:       "nothing could be searched at all",
			err:        &NotFoundError{Name: "production"},
			conclusive: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Conclusive(); got != tc.conclusive {
				t.Fatalf("Conclusive() = %v, want %v", got, tc.conclusive)
			}
		})
	}
}

// The message has to name what was searched, so a wrong config file is visible
// without a trip to the cloud console.
func TestNotFoundErrorNamesWhatWasSearched(t *testing.T) {
	err := &NotFoundError{
		Name:     "production",
		Searched: []string{"kind"},
		Failures: []LookupFailure{{Provider: "gcp", Reason: "no credentials"}},
	}
	msg := err.Error()
	for _, want := range []string{`"production"`, "searched: kind", "gcp could not be consulted", "no credentials"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q is missing %q", msg, want)
		}
	}
}

// errors.As must reach it through a wrap, which is how the down command inspects it.
func TestNotFoundErrorIsUnwrappable(t *testing.T) {
	var nf *NotFoundError
	if !errors.As(error(&NotFoundError{Name: "x", Searched: []string{"kind"}}), &nf) {
		t.Fatal("errors.As did not match *NotFoundError")
	}
	if nf.Name != "x" {
		t.Fatalf("Name = %q, want %q", nf.Name, "x")
	}
}
