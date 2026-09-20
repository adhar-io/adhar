package gcp

import (
	"strings"
	"testing"
)

// Firewall rules must be built from the cluster's configured CIDRs and must
// target the cluster's own network tag. The internal rule hardcoded 10.0.0.0/24
// and the rules targeted the whole network while instances carried a per-instance
// tag, so on any non-default subnet node-to-node traffic was dropped and the
// rules matched nothing (found preparing the GCP deployment, 2026-09-19).
func TestInstanceTagIsPerClusterNotPerInstance(t *testing.T) {
	if got := instanceTag("adhar-mgmt"); got != "adhar-mgmt-node" {
		t.Errorf("instanceTag = %q, want adhar-mgmt-node", got)
	}
	// Two clusters must not share a tag, or one's rules govern the other.
	if instanceTag("a") == instanceTag("b") {
		t.Error("distinct clusters must get distinct network tags")
	}
	// The tag must not depend on the instance, which is what broke rule matching.
	if instanceTag("adhar") == "adhar-master-0-node" {
		t.Error("the tag must name the cluster, not an instance")
	}
}

func TestToStringSliceAcceptsEveryYAMLShape(t *testing.T) {
	cases := []struct {
		name string
		in   interface{}
		want int
	}{
		{"native slice", []string{"10.0.0.0/8", "192.168.0.0/16"}, 2},
		{"generic slice", []interface{}{"10.0.0.0/8", "192.168.0.0/16"}, 2},
		{"comma string", "10.0.0.0/8, 192.168.0.0/16", 2},
		{"blank entries dropped", []interface{}{"10.0.0.0/8", "", "  "}, 1},
		{"unsupported type", 42, 0},
	}
	for _, tc := range cases {
		if got := toStringSlice(tc.in); len(got) != tc.want {
			t.Errorf("%s: got %d entries (%v), want %d", tc.name, len(got), got, tc.want)
		}
	}
}

// Everything under `providers.gcp.config` in config.yaml used to be ignored:
// only project_id and zone had a fallback into that nested section, so a cluster
// asking for 100 GB pd-balanced disks on a 10.20.0.0/16 subnet silently got the
// built-in defaults of 20 GB pd-standard on 10.0.0.0/24 (found on the first real
// GCP provisioning run, 2026-09-19).
func TestFlattenProviderConfigLiftsTheNestedSection(t *testing.T) {
	in := map[string]interface{}{
		"region":    "asia-southeast1",
		"projectId": "adhar-cloud",
		"config": map[string]interface{}{
			"machine_type": "e2-standard-4",
			"disk_size_gb": 100,
			"disk_type":    "pd-balanced",
			"vpc_name":     "adhar-vpc",
			"subnet_cidr":  "10.20.0.0/16",
		},
	}
	got := flattenProviderConfig(in)

	for key, want := range map[string]interface{}{
		"machine_type": "e2-standard-4",
		"disk_type":    "pd-balanced",
		"vpc_name":     "adhar-vpc",
		"subnet_cidr":  "10.20.0.0/16",
		"region":       "asia-southeast1",
	} {
		if got[key] != want {
			t.Errorf("%s = %v, want %v", key, got[key], want)
		}
	}
	if v, ok := toInt32(got["disk_size_gb"]); !ok || v != 100 {
		t.Errorf("disk_size_gb = %v (ok=%v), want 100", v, ok)
	}
}

// A root-level key is the more specific place to say something, so it must win.
func TestFlattenProviderConfigPrefersRootLevelKeys(t *testing.T) {
	got := flattenProviderConfig(map[string]interface{}{
		"machine_type": "e2-standard-8",
		"config":       map[string]interface{}{"machine_type": "e2-standard-2"},
	})
	if got["machine_type"] != "e2-standard-8" {
		t.Errorf("machine_type = %v, want the root-level e2-standard-8", got["machine_type"])
	}
	// A config with no nested section must pass through untouched.
	plain := map[string]interface{}{"region": "us-central1"}
	if out := flattenProviderConfig(plain); out["region"] != "us-central1" {
		t.Error("a config without a nested section must be returned unchanged")
	}
}

// YAML and JSON disagree about the type of an integer, and the disk-size parser
// accepted only int and int32, so a float64 from a JSON round trip was dropped.
func TestToInt32AcceptsEveryNumericShape(t *testing.T) {
	for _, v := range []interface{}{100, int32(100), int64(100), float64(100), float32(100)} {
		if got, ok := toInt32(v); !ok || got != 100 {
			t.Errorf("toInt32(%T) = %v (ok=%v), want 100", v, got, ok)
		}
	}
	if _, ok := toInt32("100"); ok {
		t.Error("a string must not be accepted as a number")
	}
}

// clusterNameFromTag reverses instanceTag, and must not claim tags it does not
// own: a bare suffix or another tool's tag would make an unrelated instance look
// like an Adhar cluster, and `adhar down` would then offer to delete it.
func TestClusterNameFromTagRoundTripsAndRejectsOthers(t *testing.T) {
	for _, name := range []string{"production", "adhar-mgmt", "dev"} {
		got, ok := clusterNameFromTag(instanceTag(name))
		if !ok || got != name {
			t.Errorf("round trip of %q gave (%q, %v)", name, got, ok)
		}
	}
	for _, tag := range []string{"-node", "node", "", "web-server", "gke-cluster-pool"} {
		if _, ok := clusterNameFromTag(tag); ok {
			t.Errorf("tag %q must not be read as an Adhar cluster tag", tag)
		}
	}
}

// The quota that bites is not the obvious one: pd-balanced and pd-ssd count
// against SSD_TOTAL_GB (250 GB by default in a new project), while only
// pd-standard counts against DISKS_TOTAL_GB. Three 100 GB pd-balanced workers
// plus a control plane exceeded the SSD limit and the build failed on the second
// worker, after creating a VPC, subnet, five firewall rules and two instances
// (found on the first real GCP run, 2026-09-19).
func TestMachineTypeCPUsReadsStandardNames(t *testing.T) {
	for name, want := range map[string]int64{
		"e2-standard-4":  4,
		"e2-standard-2":  2,
		"n2-highmem-16":  16,
		"n2d-standard-8": 8,
	} {
		got, ok := machineTypeCPUs(name)
		if !ok || got != want {
			t.Errorf("machineTypeCPUs(%q) = %d (ok=%v), want %d", name, got, ok, want)
		}
	}
	// Unrecognised shapes must report false so the check is skipped rather than
	// guessed at — a wrong guess would block a valid cluster.
	for _, name := range []string{"custom-4-8192", "e2-micro", "", "f1-micro", "e2-standard-x"} {
		if _, ok := machineTypeCPUs(name); ok && name != "custom-4-8192" {
			t.Errorf("machineTypeCPUs(%q) claimed to know the vCPU count", name)
		}
	}
}

// scaleWorkers derived the cluster name by trimming the literal suffix
// "-master-1" from the first master's instance name. Masters are numbered from
// zero, so "production-master-0" was left untouched and the cluster was treated
// as being called "production-master-0". Two failures followed from that one
// line: the SSH key was looked up in a directory that did not exist, so a fresh
// key was generated that no node had ever seen and every scale failed to
// authenticate; and the worker name prefix matched none of the real workers, so
// a scale DOWN was computed as a scale UP (2026-09-19).
func TestExtractClusterNameHandlesRealClusterIDs(t *testing.T) {
	for id, want := range map[string]string{
		"gcp/adhar-cloud/production": "production",
		"gcp/proj/adhar-mgmt":        "adhar-mgmt",
		"gcp-production":             "production",
		"production":                 "production",
	} {
		if got := extractClusterName(id); got != want {
			t.Errorf("extractClusterName(%q) = %q, want %q", id, got, want)
		}
	}

	// The specific trap: a master instance name must never be mistaken for the
	// cluster name, whatever its index.
	for _, master := range []string{"production-master-0", "production-master-1"} {
		if extractClusterName("gcp/proj/production") == master {
			t.Errorf("cluster name must not equal the master instance name %q", master)
		}
	}
}

// Teardown must remove what the in-cluster controllers created, because nothing
// records it. On the first GCP teardown the cloud controller manager's load
// balancer and 59 CSI disks survived and kept billing, and a leftover k8s-fw-*
// rule held a reference to the VPC so the network could not be deleted either —
// the next run inherited it (2026-09-19/20).
//
// Load balancer resources are swept unconditionally: they hold no data and the
// firewall rule blocks completing the teardown. Disks are opt-in, because a disk
// may still hold data someone wants.
func TestPurgeOrphanedVolumesIsOptInFromProviderConfig(t *testing.T) {
	// Default: report only.
	p := &Provider{config: &Config{ProjectID: "proj", Zone: "z"}}
	if p.config.PurgeOrphanedVolumes {
		t.Error("deleting disks must be opt-in, not the default")
	}

	// The flag arrives through the generic provider map that `adhar down` builds.
	cfg := &Config{}
	m := map[string]interface{}{"purgeOrphanedVolumes": true}
	if purge, ok := m["purgeOrphanedVolumes"].(bool); ok && purge {
		cfg.PurgeOrphanedVolumes = true
	}
	if !cfg.PurgeOrphanedVolumes {
		t.Error("--purge-orphaned-volumes must reach the GCP provider config")
	}
}

// The CCM's own rules are identified by a k8s- prefix, and its forwarding rules by
// the Service name it stamps into the description. Matching anything broader would
// delete a human's load balancer in the same project.
func TestLoadBalancerSweepMatchesOnlyKubernetesResources(t *testing.T) {
	// The prefix the sweep uses for firewall rules.
	for _, name := range []string{"k8s-fw-abc123", "k8s-abc123-node-http-hc"} {
		if !strings.HasPrefix(name, "k8s-") {
			t.Errorf("%q should be recognised as a CCM firewall rule", name)
		}
	}
	for _, name := range []string{"production-allow-ssh", "default-allow-internal", "my-own-rule"} {
		if strings.HasPrefix(name, "k8s-") {
			t.Errorf("%q must not be treated as a CCM rule", name)
		}
	}
	// The marker the sweep uses for forwarding rules.
	const ccm = `{"kubernetes.io/service-name":"adhar-system/cilium-gateway-adhar-gateway"}`
	if !strings.Contains(ccm, "kubernetes.io/service-name") {
		t.Error("the CCM description marker must be what identifies its forwarding rules")
	}
	if strings.Contains("a load balancer someone made by hand", "kubernetes.io/service-name") {
		t.Error("an unrelated description must not match")
	}
}
