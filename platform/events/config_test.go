package events

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The publisher is opt-in, so the ONE place that turns it on must name the
// address this package documents. A typo here is the whole bus silently off:
// the controller starts, reports itself healthy, and publishes nothing.
func TestTheManagerDeploymentEnablesTheBusAtTheDocumentedAddress(t *testing.T) {
	path := filepath.Join("..", "controllers", "resources", "manager", "manager.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the manager Deployment: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, EnvBridgeURL) {
		t.Errorf("the manager Deployment does not set %s, so the in-cluster controller publishes nothing", EnvBridgeURL)
	}
	if !strings.Contains(s, DefaultBridgeURL) {
		t.Errorf("the manager Deployment does not point %s at %s; Strimzi's `-bridge-service` "+
			"suffix is not a name that survives being retyped", EnvBridgeURL, DefaultBridgeURL)
	}
}

// SourceFor must never produce an empty or partial `source`: (source, id) is
// what makes a CloudEvent unique, so a blank one collapses every event from
// every platform into one identity for a deduplicating consumer.
func TestSourceIsAlwaysComplete(t *testing.T) {
	for _, cluster := range []string{"", "   ", "dev.adhar.io"} {
		got := SourceFor(cluster, "adharplatform-controller")
		if !strings.HasPrefix(got, "/adhar/") || strings.HasSuffix(got, "/") {
			t.Errorf("SourceFor(%q) = %q, which is not a complete source URI", cluster, got)
		}
		if strings.Contains(got, "//") {
			t.Errorf("SourceFor(%q) = %q, which has an empty path segment", cluster, got)
		}
	}
}

// With nothing in the environment the bus is OFF. This is the local default and
// it must not require a flag to get right.
func TestWithNoEnvironmentTheBusIsOff(t *testing.T) {
	t.Setenv(EnvBridgeURL, "")
	if opts := FromEnv("local", "c"); opts.BridgeURL != "" {
		t.Errorf("FromEnv produced a bridge URL %q from an empty environment", opts.BridgeURL)
	}
	t.Setenv(EnvBridgeURL, "http://bridge:8080/")
	if opts := FromEnv("local", "c"); opts.BridgeURL != "http://bridge:8080" {
		t.Errorf("a trailing slash survived into the bridge URL: %q", opts.BridgeURL)
	}
}
