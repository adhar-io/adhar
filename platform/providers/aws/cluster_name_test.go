package aws

import "testing"

// This provider's cluster IDs carry an "aws-" prefix ("aws-dev") while the EC2
// instances it creates are tagged with the BARE name ("Cluster=dev"). Callers
// hold whichever they happen to have: ScaleNodeGroup passed the prefixed ID
// straight into the tag filter, so it matched no instances, MasterNodes came back
// empty, and the node autoscaler failed on every tick with
//
//	cannot scale cluster aws-dev: control-plane public IP unknown
//
// …while 31 pods sat Pending for capacity and the cluster could not grow past its
// two starting workers. getClusterInfrastructure now normalises, so all ten call
// sites are correct whichever form they pass.
func TestExtractClusterNameAcceptsBothSpellings(t *testing.T) {
	for in, want := range map[string]string{
		"aws-dev":        "dev",
		"dev":            "dev",
		"aws-production": "production",
		"production":     "production",
		// A name that merely starts with the letters must not be truncated.
		"awsome": "awsome",
		"aws":    "aws",
		"":       "",
	} {
		if got := extractClusterName(in); got != want {
			t.Errorf("extractClusterName(%q) = %q, want %q", in, got, want)
		}
	}
}

// Normalising must be idempotent, or a doubly-prefixed id would survive.
func TestExtractClusterNameIsIdempotent(t *testing.T) {
	once := extractClusterName("aws-dev")
	if twice := extractClusterName(once); twice != once {
		t.Errorf("not idempotent: %q then %q", once, twice)
	}
}
