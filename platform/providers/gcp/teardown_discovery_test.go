package gcp

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"google.golang.org/api/googleapi"
)

// The 2026-10-21 GCP teardown failed on "subnet … is already being used by
// adhar-worker-workers-4": a worker the in-cluster autoscaler added was never in
// the local tracker, and discovery only ran when there was NO tracker.
func TestTeardownMergesInstancesTheLocalStateNeverSaw(t *testing.T) {
	local := &ResourceTracker{
		Instances:     []string{"adhar-master-1", "adhar-worker-workers-1"},
		FirewallRules: []string{"adhar-allow-web"},
		Subnets:       []string{"adhar-subnet"},
		Networks:      []string{"adhar-vpc"},
	}
	cloud := &ResourceTracker{
		Instances:     []string{"adhar-master-1", "adhar-worker-workers-1", "adhar-worker-workers-4"},
		FirewallRules: []string{"adhar-allow-web", "adhar-allow-internal"},
		Subnets:       []string{"adhar-subnet"},
		Networks:      []string{"adhar-vpc"},
		Zone:          "asia-southeast1-a",
	}
	mergeTracker(local, cloud)
	if got := strings.Join(local.Instances, ","); got != "adhar-master-1,adhar-worker-workers-1,adhar-worker-workers-4" {
		t.Errorf("instances to delete: %s", got)
	}
	if got := strings.Join(local.FirewallRules, ","); got != "adhar-allow-web,adhar-allow-internal" {
		t.Errorf("firewall rules to delete: %s", got)
	}
	if len(local.Subnets) != 1 || len(local.Networks) != 1 {
		t.Errorf("merge duplicated the network: %v %v", local.Subnets, local.Networks)
	}
	if local.Zone != "asia-southeast1-a" {
		t.Errorf("zone not filled from discovery: %q", local.Zone)
	}
}

func TestDeleteClusterAsksTheProjectEvenWithLocalState(t *testing.T) {
	b, err := os.ReadFile("provider.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	start := strings.Index(src, "func (p *Provider) DeleteCluster(")
	end := strings.Index(src[start:], "\n}\n")
	body := src[start : start+end]
	discover := strings.Index(body, "p.discoverClusterResources(")
	merge := strings.Index(body, "mergeTracker(tracker, discovered)")
	instances := strings.Index(body, "range tracker.Instances")
	if discover < 0 || merge < 0 || instances < merge {
		t.Error("DeleteCluster does not merge what the project holds into the local tracker before deleting instances")
	}
	if strings.Contains(body[:discover], "if !exists {\n\t\tlog.Printf(\"No local state") {
		t.Error("discovery is still gated on the tracker being missing")
	}
}

func TestOnlyThisClustersNodesAreClaimed(t *testing.T) {
	cases := []struct {
		instance string
		tags     []string
		want     bool
	}{
		{"adhar-worker-workers-4", []string{"adhar-node"}, true},
		{"adhar-worker-workers-4", nil, true},
		{"adhar-master-1", nil, true},
		{"renamed-by-hand", []string{"adhar-node"}, true},
		// Another cluster whose name starts with ours must never be deleted.
		{"adhar-dev-master-1", []string{"adhar-dev-node"}, false},
		{"adhar-dev-worker-workers-1", nil, false},
		{"adhar-bastion", nil, false},
		// A name that looks like ours but tagged for another cluster.
		{"adhar-worker-x-1", []string{"other-node"}, false},
	}
	for _, c := range cases {
		if got := instanceBelongsToCluster("adhar", c.instance, c.tags); got != c.want {
			t.Errorf("instanceBelongsToCluster(adhar, %s, %v) = %t, want %t", c.instance, c.tags, got, c.want)
		}
	}
}

// A retried `adhar down` failed on every firewall rule the first attempt had
// already removed: 404 is the outcome a delete asks for.
func TestAlreadyDeletedIsNotAFailure(t *testing.T) {
	if !isNotFound(fmt.Errorf("wrapped: %w", &googleapi.Error{Code: 404})) {
		t.Error("a wrapped googleapi 404 is not recognised")
	}
	if !isNotFound(errors.New("googleapi: Error 404: The resource 'projects/p/global/firewalls/adhar-allow-web' was not found")) {
		t.Error("the 404 message the compute client returns is not recognised")
	}
	if isNotFound(&googleapi.Error{Code: 400}) || isNotFound(errors.New("RESOURCE_IN_USE_BY_ANOTHER_RESOURCE")) || isNotFound(nil) {
		t.Error("a real failure is treated as already deleted")
	}
	b, err := os.ReadFile("provider.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, fn := range []string{"deleteInstance", "deleteFirewallRule", "deleteExternalIP", "deleteSubnet", "deleteNetwork"} {
		i := strings.Index(src, "func (p *Provider) "+fn+"(")
		j := strings.Index(src[i:], "\n}\n")
		if !strings.Contains(src[i:i+j], "if isNotFound(err) {\n\t\treturn nil") {
			t.Errorf("%s still reports an already-deleted resource as a failure", fn)
		}
	}
}
