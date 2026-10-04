package azure

import (
	"os"
	"strings"
	"testing"

	"adhar-io/adhar/platform/types"
)

// azureSource returns provider.go with comments stripped, so prose describing a
// past mistake is not mistaken for the mistake.
func azureSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("provider.go")
	if err != nil {
		t.Fatal(err)
	}
	var code strings.Builder
	for _, line := range strings.Split(string(b), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		code.WriteString(line + "\n")
	}
	return code.String()
}

func azureFunc(t *testing.T, name string) string {
	t.Helper()
	src := azureSource(t)
	i := strings.Index(src, "func (p *Provider) "+name+"(")
	if i < 0 {
		t.Fatalf("%s has moved; this guard needs updating", name)
	}
	body := src[i:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}
	return body
}

// Scaling must not depend on the CLI's local state file.
//
// ScaleNodeGroup read p.clusters[clusterID] and p.resourceTrackers[clusterID]
// directly. Those come from ~/.adhar/state/<cloud>/clusters.json — a file the
// CLI has and an in-cluster pod never does (the controller runs with HOME=/tmp).
// So the node autoscaler, its main caller, could not scale a cloud cluster at
// all: it decided correctly that a worker was needed and then failed every
// three-minute cooldown with
//
//	"autoscaling action failed" error="cluster azure-adhar not found"
//
// while 52 pods stayed Pending and keycloak-db — the platform's critical path,
// which every SSO consumer waits on — never started (2026-10-04). It had only
// ever been verified by running the controller OUT of cluster, where the state
// file exists, which is exactly why it looked fine.
func TestScaleNodeGroupResolvesTheClusterFromTheCloud(t *testing.T) {
	body := azureFunc(t, "ScaleNodeGroup")
	if strings.Contains(body, "p.clusters[clusterID]") {
		t.Error("ScaleNodeGroup must not read the local state map — an in-cluster caller has no " +
			"state file, so scaling would always fail with \"not found\"")
	}
	if strings.Contains(body, "p.resourceTrackers[clusterID]") {
		t.Error("ScaleNodeGroup must resolve the tracker through trackerFor, which rediscovers " +
			"from Azure when state has no record")
	}
	for _, want := range []string{"p.clusterFor(ctx, clusterID)", "p.trackerFor(ctx, clusterID)"} {
		if !strings.Contains(body, want) {
			t.Errorf("ScaleNodeGroup must call %s", want)
		}
	}
}

// A new worker must match the RUNNING control plane.
//
// Discovery cannot see inside a VM, so a discovered cluster carries no version.
// It used to carry a hardcoded "v1.29.0", which is not a default but a wrong
// answer: preparing a worker from it would have installed 1.29 packages into a
// v1.37.1 cluster and skewed the new kubelet from the API server it joined.
func TestDiscoveredClusterCarriesNoInventedVersion(t *testing.T) {
	src := azureSource(t)
	if strings.Contains(src, `"v1.29.0"`) {
		t.Error("a discovered cluster must not be given an invented Kubernetes version — nothing " +
			"about a VM listing reveals what Kubernetes is inside it")
	}
	// And the scaling path must read it from the control plane instead.
	if !strings.Contains(azureFunc(t, "ScaleNodeGroup"), "scaleKubernetesVersion(") {
		t.Error("ScaleNodeGroup must resolve the version via scaleKubernetesVersion, which reads " +
			"the running control plane when none is remembered")
	}
}

// A remembered version is used as-is; an empty one must not be silently
// defaulted, because the fallback requires reaching the master.
func TestScaleKubernetesVersionPrefersTheRememberedVersion(t *testing.T) {
	got, err := scaleKubernetesVersion(&types.Cluster{Version: "v1.37.1"}, nil, "10.0.0.1")
	if err != nil {
		t.Fatalf("a remembered version must need no SSH: %v", err)
	}
	if got != "v1.37.1" {
		t.Errorf("got %q, want %q", got, "v1.37.1")
	}
}

// With no remembered version and no reachable master, scaling must FAIL rather
// than guess — a guessed minor is a skewed kubelet.
func TestScaleKubernetesVersionRefusesToGuess(t *testing.T) {
	_, err := scaleKubernetesVersion(&types.Cluster{}, nil, "")
	if err == nil {
		t.Fatal("an unknown version must be an error, not a default")
	}
	if !strings.Contains(err.Error(), "cannot be prepared safely") {
		t.Errorf("the error must say why it refuses, got: %v", err)
	}
}

// An authentication failure must never be reported as a missing cluster.
//
// ListClusters logged the discovery error and returned an empty list, so an
// expired credential surfaced downstream as `cluster adhar not found` — which
// reads as "someone deleted it" and sent an hour of debugging at the wrong
// problem. The real cause was AADSTS7000215.
func TestClusterLookupDoesNotFlattenAuthFailuresIntoNotFound(t *testing.T) {
	list := azureFunc(t, "ListClusters")
	if !strings.Contains(list, "return nil, fmt.Errorf(") {
		t.Error("ListClusters must return the discovery error when state supplied nothing — " +
			"otherwise an unreadable subscription is indistinguishable from an empty one")
	}
	cf := azureFunc(t, "clusterFor")
	if !strings.Contains(cf, "could not be read from Azure") {
		t.Error("clusterFor must distinguish 'could not read the subscription' from 'not found'")
	}
}

// A discovered cluster must carry its API endpoint, or it can be listed but
// never scaled: ScaleNodeGroup needs a master IP to SSH to.
func TestDiscoveredClusterCarriesItsEndpoint(t *testing.T) {
	src := azureSource(t)
	if !strings.Contains(src, "Endpoint: endpoint") && !strings.Contains(src, "Endpoint:   endpoint") {
		t.Error("discovery must populate Endpoint from the master VM's public IP")
	}
	if !strings.Contains(src, "masterPublicIPFor(") {
		t.Error("expected the master public-IP lookup used to build the endpoint")
	}
}
