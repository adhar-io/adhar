package azure

import (
	"log"
	"strings"
	"testing"
)

// The provider's `config:` block must be read whatever spelling it uses.
//
// The map arrives with its keys already LOWER-CASED, so lookups for `resourceGroup`
// and `resource_group` could never match the `resourcegroup` that actually arrived.
// Five of seven settings were silently dropped on a real config (2026-09-26) and the
// cluster was built in a resource group nobody asked for, with a default VM size and
// default CIDRs — with no warning that the file had been ignored.
func TestConfigSectionKeysAreMatchedIgnoringCaseAndSeparators(t *testing.T) {
	// Exactly how the keys arrive in practice: lower-cased, no separators.
	asArrived := map[string]interface{}{
		"config": map[string]interface{}{
			"subscriptionid": "sub-1",
			"resourcegroup":  "adhar-rg",
			"location":       "malaysiawest",
			"vmsize":         "Standard_D4s_v3",
			"vnetcidr":       "10.30.0.0/16",
			"subnetcidr":     "10.30.1.0/24",
			"disktype":       "Premium_LRS",
		},
	}
	got, err := parseProviderConfig(asArrived)
	if err != nil {
		t.Fatalf("parseProviderConfig: %v", err)
	}
	for _, tc := range []struct{ field, want, have string }{
		{"SubscriptionID", "sub-1", got.SubscriptionID},
		{"ResourceGroup", "adhar-rg", got.ResourceGroup},
		{"Location", "malaysiawest", got.Location},
		{"VMSize", "Standard_D4s_v3", got.VMSize},
		{"VNetCIDR", "10.30.0.0/16", got.VNetCIDR},
		{"SubnetCIDR", "10.30.1.0/24", got.SubnetCIDR},
		{"DiskType", "Premium_LRS", got.DiskType},
	} {
		if tc.have != tc.want {
			t.Errorf("%s = %q, want %q — this key was silently dropped", tc.field, tc.have, tc.want)
		}
	}
}

// camelCase and snake_case must land in the same place.
func TestConfigSectionAcceptsEverySpelling(t *testing.T) {
	for _, keys := range []map[string]interface{}{
		{"resourceGroup": "rg1", "vmSize": "v1", "vnetCidr": "c1", "subnetCidr": "s1", "diskType": "d1"},
		{"resource_group": "rg1", "vm_size": "v1", "vnet_cidr": "c1", "subnet_cidr": "s1", "disk_type": "d1"},
		{"RESOURCE-GROUP": "rg1", "VM-SIZE": "v1", "VNET-CIDR": "c1", "SUBNET-CIDR": "s1", "DISK-TYPE": "d1"},
	} {
		got, err := parseProviderConfig(map[string]interface{}{"config": keys})
		if err != nil {
			t.Fatalf("parseProviderConfig: %v", err)
		}
		if got.ResourceGroup != "rg1" || got.VMSize != "v1" ||
			got.VNetCIDR != "c1" || got.SubnetCIDR != "s1" || got.DiskType != "d1" {
			t.Errorf("spelling %v was not honoured: %+v", keys, got)
		}
	}
}

func TestNormaliseKey(t *testing.T) {
	for in, want := range map[string]string{
		"machineType": "machinetype", "machine_type": "machinetype",
		"MACHINE-TYPE": "machinetype", "machine type": "machinetype",
		"vnet.cidr": "vnetcidr", "": "",
	} {
		if got := normaliseKey(in); got != want {
			t.Errorf("normaliseKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// A config file's numbers arrive as int, not string. A string-only lookup
// dropped every numeric setting and fell back to the built-in default while the
// log said the key had been read — `diskSizeGb: 256` silently stayed at the
// default, and on this platform the node disk is where node-local volumes live.
func TestNumericConfigValuesAreHonoured(t *testing.T) {
	for name, raw := range map[string]interface{}{
		"int":     256,
		"int64":   int64(256),
		"float64": float64(256),
		"string":  "256",
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := parseProviderConfig(map[string]interface{}{
				"config": map[string]interface{}{
					"subscriptionId": "sub", "resourceGroup": "rg", "location": "centralindia",
					"diskSizeGb": raw,
				},
			})
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if cfg.DiskSizeGB != 256 {
				t.Fatalf("diskSizeGb from %T = %d, want 256", raw, cfg.DiskSizeGB)
			}
		})
	}
}

// A key that IS acted on must not be reported as ignored: the warning sends you
// hunting for a bug in the setting that works.
func TestKnownKeysAreNotReportedAsIgnored(t *testing.T) {
	var logged strings.Builder
	old := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(old)

	if _, err := parseProviderConfig(map[string]interface{}{
		"config": map[string]interface{}{
			"subscriptionId": "sub", "resourceGroup": "rg", "location": "centralindia",
			"diskSizeGb": 256, "dnsResourceGroup": "dns-rg",
		},
	}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, k := range []string{"disksizegb", "dnsresourcegroup"} {
		if strings.Contains(logged.String(), `key "`+k+`" is not recognised`) {
			t.Errorf("%s is read by the provider but reported as ignored:\n%s", k, logged.String())
		}
	}
}
