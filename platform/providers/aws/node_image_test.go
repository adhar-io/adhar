package aws

import (
	"strings"
	"testing"
)

// The node image must track the latest Ubuntu LTS, and must be overridable.
//
// It was hardcoded to Ubuntu 22.04 (jammy) with no configuration path, while the
// GCP provider booted `ubuntu-2404-lts-amd64`. So the same platform ran on two
// distro releases depending on the cloud — different kernel, different containerd
// — and no cluster could be pinned to a known-good or hardened image.
func TestDefaultNodeImageIsLatestUbuntuLTS(t *testing.T) {
	if !strings.Contains(defaultImageNameFilter, "24.04") {
		t.Errorf("default image filter %q is not Ubuntu 24.04 LTS", defaultImageNameFilter)
	}
	if strings.Contains(defaultImageNameFilter, "22.04") || strings.Contains(defaultImageNameFilter, "jammy") {
		t.Errorf("default image filter %q still points at the old LTS", defaultImageNameFilter)
	}
	// Canonical has used both hvm-ssd and hvm-ssd-gp3 for noble; matching across
	// that segment keeps the lookup working when they change it again.
	if !strings.Contains(defaultImageNameFilter, "hvm-ssd*") {
		t.Errorf("default image filter %q should wildcard the storage-type segment", defaultImageNameFilter)
	}
	if canonicalOwnerID != "099720109477" {
		t.Errorf("owner %q is not Canonical", canonicalOwnerID)
	}
	if defaultImageArchitecture != "x86_64" {
		t.Errorf("architecture %q unexpected", defaultImageArchitecture)
	}
}

// Every part of image selection has to be reachable from configuration, in both
// the camelCase and snake_case spellings an operator might reasonably write.
func TestNodeImageIsConfigurable(t *testing.T) {
	cases := []struct {
		name  string
		cfg   map[string]interface{}
		check func(*testing.T, *Config)
	}{
		{
			name: "pinned ami, camelCase",
			cfg:  map[string]interface{}{"region": "ap-south-1", "ami": "ami-0123456789abcdef0"},
			check: func(t *testing.T, c *Config) {
				if c.AMI != "ami-0123456789abcdef0" {
					t.Errorf("AMI = %q", c.AMI)
				}
			},
		},
		{
			name: "pinned ami, snake_case alias",
			cfg:  map[string]interface{}{"region": "ap-south-1", "image_id": "ami-aaa"},
			check: func(t *testing.T, c *Config) {
				if c.AMI != "ami-aaa" {
					t.Errorf("AMI = %q", c.AMI)
				}
			},
		},
		{
			name: "name filter, owner and arch",
			cfg: map[string]interface{}{
				"region":            "ap-south-1",
				"imageNameFilter":   "my/hardened-*",
				"imageOwner":        "111122223333",
				"imageArchitecture": "arm64",
			},
			check: func(t *testing.T, c *Config) {
				if c.ImageNameFilter != "my/hardened-*" {
					t.Errorf("ImageNameFilter = %q", c.ImageNameFilter)
				}
				if c.ImageOwner != "111122223333" {
					t.Errorf("ImageOwner = %q", c.ImageOwner)
				}
				if c.ImageArchitecture != "arm64" {
					t.Errorf("ImageArchitecture = %q", c.ImageArchitecture)
				}
			},
		},
		{
			name: "nothing set leaves the defaults to apply",
			cfg:  map[string]interface{}{"region": "ap-south-1"},
			check: func(t *testing.T, c *Config) {
				if c.AMI != "" || c.ImageNameFilter != "" || c.ImageOwner != "" || c.ImageArchitecture != "" {
					t.Errorf("expected all image fields empty, got %+v", c)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := parseProviderConfig(tc.cfg)
			if err != nil {
				t.Fatalf("parseProviderConfig: %v", err)
			}
			tc.check(t, cfg)
		})
	}
}
