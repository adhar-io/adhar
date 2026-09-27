package provider

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every node a provider joins must be told the same thing about its cloud.
//
// EnableExternalCloudProvider's `externalCCM` argument writes
// --cloud-provider=external into the kubelet's flags. When a provider installs a
// cloud-controller-manager, omitting it on ANY join path breaks a chain that
// gives no useful error anywhere along it:
//
//	no --cloud-provider=external
//	  -> the kubelet registers with no uninitialized taint, so the CCM ignores it
//	  -> .spec.providerID stays empty
//	  -> the CSI node plugin cannot identify its instance and crash-loops
//	     ("node providerID empty, cannot parse", IMDS having already timed out)
//	  -> no CSINode object is published
//	  -> the autoscaler never lifts node.adhar.io/csi-not-ready:NoSchedule
//
// What an operator sees at the end of that is an autoscaled node that is Ready
// and totally EMPTY while pods stay Pending. Measured on AWS: 5 nodes, two of
// them at 3% CPU, 27 unschedulable pods — because the scale-up path passed false
// where the create path passed true. Civo had the same mismatch, justified by a
// comment claiming it installed no CCM while its own integration installed one.
//
// This test reads the provider sources so the mismatch cannot come back.
func TestProvidersEnableTheExternalCCMOnEveryJoinPath(t *testing.T) {
	// A call site: EnableExternalCloudProvider(..., externalCCM, csiStartupTaint, ...)
	// The two booleans are the 5th and 6th arguments.
	call := regexp.MustCompile(`EnableExternalCloudProvider\([^)]*?,\s*(true|false),\s*(true|false)`)

	for _, dir := range []string{"aws", "azure", "gcp", "digitalocean", "civo"} {
		installsCCM := false
		var sources []string

		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			text := string(b)
			// A provider "installs a CCM" if its integration steps mention one.
			if strings.Contains(path, "cloud_integration.go") &&
				(strings.Contains(text, "cloud-controller-manager") || strings.Contains(text, "cloudNodeManager")) {
				installsCCM = true
			}
			if call.MatchString(text) {
				sources = append(sources, path)
			}
			return nil
		})
		if err != nil || !installsCCM || len(sources) == 0 {
			continue
		}

		for _, path := range sources {
			b, _ := os.ReadFile(path)
			for _, m := range call.FindAllStringSubmatch(string(b), -1) {
				externalCCM, csiTaint := m[1], m[2]
				if externalCCM != "true" {
					t.Errorf("%s: a join path passes externalCCM=%s, but %s installs a cloud-controller-manager. "+
						"The node will never get a providerID, so its CSI plugin cannot register and its "+
						"csi-not-ready taint is never lifted — it stays Ready and empty.", path, externalCCM, dir)
				}
				// The CSI startup taint is only ever lifted via a CSINode, which
				// needs the CCM. Registering it without the CCM strands the node.
				if csiTaint == "true" && externalCCM != "true" {
					t.Errorf("%s: registers the CSI startup taint without the external CCM that lifts it", path)
				}
			}
		}
	}
}
