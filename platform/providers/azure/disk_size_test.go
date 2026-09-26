package azure

import "testing"

// Disk size must come from configuration, and its default must fit this platform.
//
// It was never parsed at all: every node got 50 GiB however large a value the file
// asked for. The platform pulls ~70 images and the containerd cache alone measured
// 35 GiB on a worker, which took the node over the kubelet's disk-eviction
// threshold. The resulting `node.kubernetes.io/disk-pressure:NoSchedule` taint then
// stopped EVERY pod from scheduling — 80 of them, on a node with CPU to spare
// (Azure, 2026-09-26). A full disk presents as a capacity problem that adding CPU
// does not fix.
func TestDiskSizeIsConfigurable(t *testing.T) {
	for _, key := range []string{"diskSizeGb", "diskSizeGB", "disk_size_gb", "DISK-SIZE-GB"} {
		cfg, err := parseProviderConfig(map[string]interface{}{
			"config": map[string]interface{}{"subscriptionid": "s", key: "250"},
		})
		if err != nil {
			t.Fatalf("parseProviderConfig(%s): %v", key, err)
		}
		if cfg.DiskSizeGB != 250 {
			t.Errorf("spelling %q gave DiskSizeGB=%d, want 250", key, cfg.DiskSizeGB)
		}
	}
}

// A value that is not a positive number must be ignored rather than producing a
// zero-sized disk.
func TestDiskSizeIgnoresNonsense(t *testing.T) {
	for _, v := range []string{"", "lots", "-5", "0"} {
		cfg, err := parseProviderConfig(map[string]interface{}{
			"config": map[string]interface{}{"subscriptionid": "s", "diskSizeGb": v},
		})
		if err != nil {
			t.Fatalf("parseProviderConfig(%q): %v", v, err)
		}
		if cfg.DiskSizeGB != 0 {
			t.Errorf("%q set DiskSizeGB=%d, want it left unset for the default", v, cfg.DiskSizeGB)
		}
	}
}

// The default must be large enough for the image cache, and NewProvider is where it
// is applied.
func TestDefaultDiskSizeFitsTheImageCache(t *testing.T) {
	p, err := NewProvider(&Config{SubscriptionID: "s", Location: "l", UseAzureCLI: true})
	if err != nil {
		t.Skipf("NewProvider needs credentials here: %v", err)
	}
	if p.config.DiskSizeGB < 100 {
		t.Errorf("default DiskSizeGB=%d; 50 was measured too small (35 GiB of images alone)", p.config.DiskSizeGB)
	}
}
