package digitalocean

import (
	"testing"

	provider "adhar-io/adhar/platform/providers"
)

func TestComputeK8sMinor(t *testing.T) {
	cases := map[string]string{
		"":        provider.KubeadmDefaultK8sMinor,
		"1.31":    "1.31",
		"1.31.4":  "1.31",
		"v1.30.2": "1.30",
		"v1.29":   "1.29",
		"junk":    provider.KubeadmDefaultK8sMinor,
	}
	for in, want := range cases {
		if got := provider.K8sMinorFromVersion(in); got != want {
			t.Errorf("provider.K8sMinorFromVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestComputeClusterNameAndTag(t *testing.T) {
	if got := computeClusterName("adhar-cluster-demo"); got != "demo" {
		t.Errorf("computeClusterName(tag) = %q, want demo", got)
	}
	if got := computeClusterName("demo"); got != "demo" {
		t.Errorf("computeClusterName(name) = %q, want demo", got)
	}
	if got := computeClusterTag("demo"); got != "adhar-cluster-demo" {
		t.Errorf("computeClusterTag = %q", got)
	}
	// Tagging must be stable when a tag is passed back in.
	if got := computeClusterTag("adhar-cluster-demo"); got != "adhar-cluster-demo" {
		t.Errorf("computeClusterTag(tag) = %q", got)
	}
}

func TestLastLines(t *testing.T) {
	if got := provider.LastLines("a\nb\nc\nd", 2); got != "c\nd" {
		t.Errorf("lastLines = %q", got)
	}
	if got := provider.LastLines("a", 5); got != "a" {
		t.Errorf("lastLines short = %q", got)
	}
}
