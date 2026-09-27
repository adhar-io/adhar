package aws

import "testing"

// The provider sent no block device mapping, so every node inherited the AMI's
// default root volume — 8 GB on these Ubuntu images — and `diskSizeGb` in the
// config file reached nothing. On a live Singapore cluster that disk was 89% full
// within minutes and the kubelet set node.kubernetes.io/disk-pressure, tainting
// the node and halting scheduling. Two things share it: the containerd image
// cache (~35 GiB here) and, since the default StorageClass became node-local,
// every PersistentVolume.
func TestRootVolumeSizeIsConfiguredNotInheritedFromTheAMI(t *testing.T) {
	cfg, err := parseProviderConfig(map[string]interface{}{
		"region": "ap-southeast-1", "diskSizeGb": 256, "diskType": "gp3",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.DiskSizeGB != 256 {
		t.Errorf("diskSizeGb = %d, want 256", cfg.DiskSizeGB)
	}
	if cfg.DiskType != "gp3" {
		t.Errorf("diskType = %q, want gp3", cfg.DiskType)
	}
}

// YAML gives numbers as int, not string. A string-only assertion is what made
// the equivalent Azure key a no-op for weeks.
func TestRootVolumeSizeAcceptsEveryScalarSpelling(t *testing.T) {
	for name, raw := range map[string]interface{}{
		"int": 256, "int64": int64(256), "float64": float64(256), "string": "256",
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := parseProviderConfig(map[string]interface{}{"region": "ap-southeast-1", "diskSizeGb": raw})
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if cfg.DiskSizeGB != 256 {
				t.Errorf("from %T: got %d, want 256", raw, cfg.DiskSizeGB)
			}
		})
	}
}

// Key spellings must match what an operator would reasonably write, because the
// environment's clusterConfig already matches keys ignoring case and separators.
func TestRootVolumeSizeAcceptsAlternateKeys(t *testing.T) {
	for _, key := range []string{"diskSizeGb", "diskSizeGB", "diskSize", "disk_size_gb", "rootVolumeSize"} {
		cfg, err := parseProviderConfig(map[string]interface{}{"region": "ap-southeast-1", key: 128})
		if err != nil {
			t.Fatalf("parse %s: %v", key, err)
		}
		if cfg.DiskSizeGB != 128 {
			t.Errorf("key %q was ignored", key)
		}
	}
}

// Unset must mean the platform's default, never the AMI's 8 GB.
func TestRootVolumeFallsBackToThePlatformDefaultNotTheAMIs(t *testing.T) {
	cfg, err := parseProviderConfig(map[string]interface{}{"region": "ap-southeast-1"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.DiskSizeGB != 0 {
		t.Errorf("unset should stay 0 in config, got %d", cfg.DiskSizeGB)
	}
	if defaultRootVolumeSizeGB < 100 {
		t.Errorf("the default root volume (%d GiB) is too small for the image cache plus node-local volumes",
			defaultRootVolumeSizeGB)
	}
}
