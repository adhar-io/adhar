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
// --cloud-provider=external into the kubelet's flags, and `csiStartupTaint`
// registers node.adhar.io/csi-not-ready:NoSchedule. Each has exactly one thing
// that can undo it, and both of those things are the cloud-controller-manager:
//
//	--cloud-provider=external
//	  -> the kubelet registers node.cloudprovider.kubernetes.io/uninitialized
//	  -> only a CCM removes that taint
//
//	csi-not-ready
//	  -> lifted once the CSI node plugin publishes a CSINode
//	  -> which needs .spec.providerID, which only a CCM sets
//
// So a provider that does not run a CCM must ask for neither, and one that does
// must ask for it on EVERY join path. Measured consequences of getting it wrong:
// on AWS the scale-up path passed false where create passed true and left 5
// nodes with two of them at 3% CPU and 27 unschedulable pods; on Civo the
// control-plane path passed a hardcoded true while compute mode installs no CCM,
// and a live mum1 bring-up never got past Cilium because the master kept the
// uninitialized taint and both workers kept csi-not-ready (2026-10-06).
//
// This test reads the provider sources so neither mismatch can come back. It has
// to do two things the first version did not, both of which let the Civo bug
// through while the test stayed green:
//
//   - RESOLVE CONSTANTS. Civo passes `civoComputeHasExternalCCM`, not a literal,
//     so a regex over `(true|false)` simply did not see those call sites.
//   - IGNORE COMMENTS. "installs a CCM" was inferred from the string
//     "cloud-controller-manager" appearing anywhere in cloud_integration.go —
//     including in a comment that says it installs NO cloud-controller-manager.
//
// The 5th and 6th arguments, positionally — NOT "the last two before the
// closing paren". The helper takes a variadic hostnameOverride after them, so
// AWS calls it with a 7th argument and anchoring on `)` read the wrong pair.
// The two values a flag can resolve to.
const (
	flagTrue  = "true"
	flagFalse = "false"
)

var ccmCallSite = regexp.MustCompile(
	`EnableExternalCloudProvider\(\s*[^,]+,\s*[^,]+,\s*[^,]+,\s*[^,]+,\s*([A-Za-z_][\w.]*|true|false)\s*,\s*([A-Za-z_][\w.]*|true|false)\s*[,)]`)

// stripGoComments removes line and block comments so prose about the code is
// never mistaken for the code.
func stripGoComments(src string) string {
	var out strings.Builder
	for i := 0; i < len(src); {
		switch {
		case strings.HasPrefix(src[i:], "//"):
			j := strings.IndexByte(src[i:], '\n')
			if j < 0 {
				return out.String()
			}
			out.WriteByte('\n')
			i += j + 1
		case strings.HasPrefix(src[i:], "/*"):
			j := strings.Index(src[i:], "*/")
			if j < 0 {
				return out.String()
			}
			i += j + 2
		default:
			out.WriteByte(src[i])
			i++
		}
	}
	return out.String()
}

// boolConsts collects `name = true|false` declarations so a call site written
// against a constant can be read like a literal one.
func boolConsts(sources map[string]string) map[string]string {
	decl := regexp.MustCompile(`(?m)^\s*(?:const\s+)?([A-Za-z_][\w]*)\s*(?:=|bool\s*=)\s*(true|false)\s*$`)
	out := map[string]string{}
	for _, text := range sources {
		for _, m := range decl.FindAllStringSubmatch(text, -1) {
			out[m[1]] = m[2]
		}
	}
	return out
}

func TestProvidersEnableTheExternalCCMOnEveryJoinPath(t *testing.T) {
	for _, dir := range []string{"aws", "azure", "gcp", "digitalocean", "civo"} {
		sources := map[string]string{}
		installsCCM := false

		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			code := stripGoComments(string(b))
			sources[path] = code
			// A provider installs a CCM when its integration steps REFERENCE one
			// in code — a manifest URL or a step name — not when a comment
			// mentions the words.
			if strings.Contains(path, "cloud_integration.go") &&
				(strings.Contains(code, "cloud-controller-manager") ||
					strings.Contains(code, "CCMManifestURL") ||
					strings.Contains(code, "cloudNodeManager")) {
				installsCCM = true
			}
			return nil
		})
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}

		consts := boolConsts(sources)
		resolve := func(arg string) (string, bool) {
			if arg == flagTrue || arg == flagFalse {
				return arg, true
			}
			v, ok := consts[arg]
			return v, ok
		}

		type site struct {
			path, externalCCM, csiTaint string
		}
		var sites []site
		for path, code := range sources {
			for _, m := range ccmCallSite.FindAllStringSubmatch(code, -1) {
				ccm, okCCM := resolve(m[1])
				taint, okTaint := resolve(m[2])
				if !okCCM || !okTaint {
					t.Errorf("%s: cannot resolve EnableExternalCloudProvider(%s, %s) to booleans; "+
						"this test must be able to read every join path", path, m[1], m[2])
					continue
				}
				sites = append(sites, site{path, ccm, taint})
			}
		}
		if len(sites) == 0 {
			continue
		}

		// Every join path in one provider must agree about the cloud. This is the
		// check that catches a control plane prepped differently from its workers.
		for _, s := range sites[1:] {
			if s.externalCCM != sites[0].externalCCM {
				t.Errorf("%s: join paths disagree about the external CCM (%s says %s, %s says %s). "+
					"Nodes prepped both ways cannot all be initialised, and the odd ones out stay tainted.",
					dir, sites[0].path, sites[0].externalCCM, s.path, s.externalCCM)
				break
			}
		}

		for _, s := range sites {
			// Applies to EVERY provider, whether or not it installs a CCM: the
			// taint has no other way out.
			if s.csiTaint == flagTrue && s.externalCCM != flagTrue {
				t.Errorf("%s: registers the CSI startup taint without the external CCM that lifts it — "+
					"the node joins Ready and stays empty forever", s.path)
			}
			if installsCCM && s.externalCCM != flagTrue {
				t.Errorf("%s: a join path passes externalCCM=%s, but %s installs a cloud-controller-manager. "+
					"The node will never get a providerID, so its CSI plugin cannot register and its "+
					"csi-not-ready taint is never lifted — it stays Ready and empty.", s.path, s.externalCCM, dir)
			}
		}
	}
}
