package gcp

import (
	"os"
	"strings"
	"testing"
)

// gcpFunc returns one Provider method's source with comments stripped, so prose
// describing a past mistake is not mistaken for the mistake.
func gcpFunc(t *testing.T, name string) string {
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
	src := code.String()
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
// p.clusters is populated from ~/.adhar/state/gcp/clusters.json — a file the
// CLI has and an in-cluster pod never does. Reading it directly meant the node
// autoscaler, which runs in the cluster, could never add a worker: it decided
// ScaleUp correctly and then failed every cooldown with "cluster not found".
// getClusterInfrastructure already falls back to discovering the instances by
// the provider's naming convention; the scale path has to go through it.
func TestScalingResolvesTheClusterFromTheCloud(t *testing.T) {
	for _, name := range []string{"scaleWorkers", "GetNodeGroup"} {
		body := gcpFunc(t, name)
		if strings.Contains(body, "p.clusters[clusterID]") {
			t.Errorf("%s reads the local state map; an in-cluster caller has no state file, so scaling always fails with \"not found\"", name)
		}
		if !strings.Contains(body, "p.getClusterInfrastructure(ctx, ") {
			t.Errorf("%s must resolve the cluster through getClusterInfrastructure, which rediscovers from GCP", name)
		}
	}
}
