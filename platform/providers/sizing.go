package provider

import (
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"

	"adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/types"
)

// Machine sizing: ONE table of defaults per provider, and one resolution rule.
//
// Before this table existed the same decision was taken in up to five places per
// cloud, each with its own literal, and they disagreed: AWS created workers as
// r6i.xlarge, re-created them on scale-up as t3.medium, reported them as
// t3.medium, sized its quota preflight for t3.medium and ran EKS on t3.large;
// Azure's AKS default (Standard_D4s_v5) was dead code behind a provider default
// (Standard_D2s_v3) that always won; GKE's 100 GB disk default was likewise
// unreachable behind a 20 GB one. `adhar cluster create` and `adhar config
// create` had a third set of their own.
//
// Every path that needs a size — create, `adhar cluster scale`, the adhar-controlplane
// node autoscaler, the quota preflight, managed node pools — now resolves it the
// same way:
//
//	control plane: environment clusterConfig `controlPlaneMachineType`
//	               → provider `controlPlaneMachineType`
//	               → the provider's legacy single size key (vmSize, machine_type,
//	                 droplet_size, size), which used to size EVERY node
//	               → MachineDefaults.ControlPlaneMachineType
//
//	worker:        the node group's own type (clusterConfig `machineType`/`nodeSize`,
//	               or `nodeGroups[].machineType`)
//	               → on scale-up, the type the group's existing members run
//	               → provider `workerMachineType`
//	               → the legacy single size key
//	               → MachineDefaults.WorkerMachineType (ManagedWorkerMachineType in
//	                 managed mode when it differs)
//
// The legacy key keeps its old meaning on purpose: a config that sets only
// `vmSize: Standard_E4bds_v5` built that size for every node and still does.
// Only a config that names NO size at all sees the table, and the table's
// control-plane entries are the only values that moved (2 vCPU → 4 vCPU), for
// the reason given on ControlPlaneMachineType below. ResolveMachineType logs
// whenever a default is used, so the choice is never silent.

// Config keys that separate the control-plane size from the worker size at the
// PROVIDER level (providers.<name>.config). The environment-level spellings
// (clusterConfig `controlPlaneMachineType`, `machineType`/`nodeSize`/
// `workerMachineType`) already existed and still win.
const (
	KeyControlPlaneMachineType = "controlPlaneMachineType"
	KeyWorkerMachineType       = "workerMachineType"
)

// MachineDefaults is what a provider builds when configuration names nothing.
type MachineDefaults struct {
	// Region is used only by code paths that have no configured region at all
	// (`adhar config create`, a provider constructed without one). A config file
	// must name its region; validation requires it.
	Region string

	// ControlPlaneMachineType sizes the kubeadm control plane (compute mode).
	//
	// Every entry is at least 4 vCPU and 16 GiB, non-burstable. Evidence, from
	// live clusters:
	//   - Azure 2026-10-11: a 2-core control plane serving 616 CRDs ran
	//     kube-apiserver at 1383m with 5.2 GiB RSS and crash-looped
	//     kube-controller-manager, kube-scheduler and the CCM on leader-election
	//     lease renewal. Nothing left to tune; the fix was 4+ vCPU.
	//   - AWS: with 8 GiB the API server failed TLS handshakes during the first
	//     sync of ~75 applications, and SSH to the node timed out with it.
	//   - Burstable (t3/B-series) control planes throttle etcd once credits run
	//     out, which looks like a network fault rather than a CPU one.
	// The previous defaults were all 2 vCPU (r6i.large, Standard_D2s_v3,
	// e2-standard-2, s-2vcpu-2gb, g4s.kube.medium); every shipped config already
	// names its control-plane size explicitly or through the legacy size key, so
	// none of them changes.
	ControlPlaneMachineType string

	// WorkerMachineType sizes workers in compute mode (and managed mode unless
	// ManagedWorkerMachineType says otherwise).
	WorkerMachineType string

	// ManagedWorkerMachineType sizes managed node pools where the cloud uses a
	// different size catalogue for them (Civo: kube sizes, not instance sizes).
	// Empty means WorkerMachineType.
	ManagedWorkerMachineType string

	// WorkerCount is the starting worker count when neither clusterConfig
	// `nodeCount` nor `autoscaling.minWorkers` says otherwise. Two: the smallest
	// shape that is a cluster — one worker means every drain or replacement has
	// nowhere to put the pods, and it hits the kubelet's 110-pod ceiling long
	// before it runs out of CPU. The autoscaler grows from there.
	WorkerCount int

	// DiskSizeGB is each node's OS disk. 256 GiB where the provider lets us
	// choose: the containerd image cache alone measured ~35 GiB, and the
	// platform's default StorageClass is node-local, so PersistentVolumes live
	// on this disk too (~90 claims vs 7–8 block-volume attach slots per VM).
	// 0 = the size is implied by the machine type (DigitalOcean, Civo).
	DiskSizeGB int32

	// DiskType is the OS disk's type. Empty = implied by the machine type.
	DiskType string
}

// ManagedWorker returns the managed-mode worker size.
func (d MachineDefaults) ManagedWorker() string {
	if d.ManagedWorkerMachineType != "" {
		return d.ManagedWorkerMachineType
	}
	return d.WorkerMachineType
}

// machineDefaults is THE defaults table. Keep each value's reason beside it.
var machineDefaults = map[string]MachineDefaults{
	"aws": {
		Region: "us-east-1",
		// m6i.xlarge: 4 vCPU / 16 GiB, the cheapest non-burstable shape that
		// meets the control-plane floor above (was r6i.large, 2 vCPU).
		ControlPlaneMachineType: "m6i.xlarge",
		// r6i.xlarge: 4 vCPU / 32 GiB. The shape that carried the full catalogue
		// on four workers; JVM-heavy packages bind on memory before CPU. Was
		// already the compute-mode worker default; EKS (t3.large, burstable
		// 2 vCPU / 8 GiB), scale-up (t3.medium) and the preflight (t3.medium)
		// now agree with it.
		WorkerMachineType: "r6i.xlarge",
		WorkerCount:       2,
		DiskSizeGB:        256,
		DiskType:          "gp3", // gp3: same price as gp2 with a 3000 IOPS floor
	},
	"azure": {
		Region: "eastus",
		// Standard_D4s_v5: 4 vCPU / 16 GiB, non-burstable (was Standard_D2s_v3).
		ControlPlaneMachineType: "Standard_D4s_v5",
		// Standard_E4bds_v5: 4 vCPU / 32 GiB, 8 data-disk slots — the size every
		// verified Azure bring-up used. Was Standard_D2s_v3 (2 vCPU / 8 GiB),
		// inherited from the provider-wide size; AKS's own default
		// (Standard_D4s_v5) was unreachable behind it.
		WorkerMachineType: "Standard_E4bds_v5",
		WorkerCount:       2,
		DiskSizeGB:        256,
		// Standard_LRS kept: changing a disk type changes the bill of every
		// config that does not name one. Premium_LRS (what the shipped configs
		// set) is the faster choice for etcd.
		DiskType: "Standard_LRS",
	},
	"gcp": {
		Region: "us-central1",
		// e2-standard-4: 4 vCPU / 16 GiB (was e2-standard-2).
		ControlPlaneMachineType: "e2-standard-4",
		// e2-standard-4 for workers too (was e2-standard-2 in compute mode and
		// e2-standard-2 in GKE, behind a dead e2-standard-4 constant). A new GCP
		// project's CPUS_ALL_REGIONS quota is 12: 1 control plane + 2 workers at
		// 4 vCPU fits it exactly, an 8-vCPU worker default would not boot.
		WorkerMachineType: "e2-standard-4",
		WorkerCount:       2,
		// 256 GiB (was 20 in compute mode and in GKE; GKE's 100 was dead code):
		// 20 GB cannot even hold the image cache, and the node taints itself
		// disk-pressure and stops scheduling.
		DiskSizeGB: 256,
		DiskType:   "pd-standard",
	},
	"digitalocean": {
		Region: "nyc1",
		// s-8vcpu-16gb for both roles (was s-2vcpu-2gb for both). The droplet
		// catalogue has no non-premium 4 vCPU / 16 GiB slug; this is the size
		// every verified DigitalOcean bring-up used, and the production profile
		// settles at 6–8 of them.
		ControlPlaneMachineType: "s-8vcpu-16gb",
		WorkerMachineType:       "s-8vcpu-16gb",
		WorkerCount:             2,
		// Disk size and type are fixed by the droplet slug.
	},
	"civo": {
		Region: "LON1",
		// g3.large: 4 vCPU / 8 GB instance, the control-plane size every shipped
		// Civo config pins (was g4s.kube.medium, a 2 vCPU KUBERNETES size used
		// for an instance).
		ControlPlaneMachineType: "g3.large",
		// g3.xlarge: 6 vCPU / 16 GB for compute-mode workers.
		WorkerMachineType: "g3.xlarge",
		// g4s.kube.large: 4 vCPU / 8 GB for managed (k3s) pools — the size the
		// shipped example uses. Managed pools take KUBE sizes only.
		ManagedWorkerMachineType: "g4s.kube.large",
		WorkerCount:              2,
	},
	// custom (bring-your-own hosts, or clusterMode: provided) and kind create no
	// machines of a chosen size; only the counts apply.
	"custom": {Region: "on-premises"},
	"kind":   {Region: "local"},
}

// CanonicalProviderName folds the spellings the CLI accepts onto the table's keys.
func CanonicalProviderName(name string) string {
	switch n := strings.ToLower(strings.TrimSpace(name)); n {
	case "do":
		return "digitalocean"
	case "google":
		return "gcp"
	default:
		return n
	}
}

// MachineDefaultsFor returns the provider's defaults (zero value for an unknown
// provider, which makes every resolution fall through to "" — the provider then
// reports the missing size rather than inventing one).
func MachineDefaultsFor(providerName string) MachineDefaults {
	return machineDefaults[CanonicalProviderName(providerName)]
}

// DefaultWorkerCount is the starting worker count for a provider. Kind (and the
// custom/provided case) keep the historical rule — none outside production —
// because Kind's control plane runs untainted and needs no separate worker, and
// a provided cluster creates nothing.
func DefaultWorkerCount(providerName string, production bool) int {
	d := MachineDefaultsFor(providerName)
	if d.WorkerCount > 0 {
		return d.WorkerCount
	}
	if production {
		return 2
	}
	return 0
}

// SizeSource names where a resolved size came from, for the log line.
type SizeSource string

const (
	SizeFromNodeGroup       SizeSource = "node group"
	SizeFromExistingMembers SizeSource = "existing workers"
	SizeFromProvider        SizeSource = "provider config"
	SizeFromLegacyKey       SizeSource = "provider size key"
	SizeFromDefault         SizeSource = "built-in default"
)

// ResolveMachineType returns the first non-empty candidate. candidates are
// (value, source) pairs in precedence order; the default is the last resort and
// is logged, so a cluster never silently gets a size nobody chose.
func ResolveMachineType(role, providerName string, def string, candidates ...SizeCandidate) string {
	for _, c := range candidates {
		if v := strings.TrimSpace(c.Value); v != "" {
			return v
		}
	}
	if def != "" {
		log.Printf("%s %s machine type not configured; using the built-in default %s (set %s to choose)",
			providerName, role, def, roleKey(role))
	}
	return def
}

// SizeCandidate is one place a size may come from.
type SizeCandidate struct {
	Value  string
	Source SizeSource
}

func roleKey(role string) string {
	if role == RoleControlPlane {
		return KeyControlPlaneMachineType
	}
	return KeyWorkerMachineType + " (or clusterConfig machineType)"
}

// Roles for ResolveMachineType.
const (
	RoleControlPlane = "control-plane"
	RoleWorker       = "worker"
)

// ControlPlaneSize resolves the control-plane machine type for a provider.
func ControlPlaneSize(providerName, fromSpec, fromProvider, fromLegacy string) string {
	return ResolveMachineType(RoleControlPlane, providerName, MachineDefaultsFor(providerName).ControlPlaneMachineType,
		SizeCandidate{fromSpec, SizeFromNodeGroup},
		SizeCandidate{fromProvider, SizeFromProvider},
		SizeCandidate{fromLegacy, SizeFromLegacyKey})
}

// WorkerSize resolves a worker machine type for a provider. fromGroup is the node
// group's own type, fromMembers the type the group's existing members run (scale
// paths only; "" at create).
func WorkerSize(providerName string, managed bool, fromGroup, fromMembers, fromProvider, fromLegacy string) string {
	d := MachineDefaultsFor(providerName)
	def := d.WorkerMachineType
	if managed {
		def = d.ManagedWorker()
	}
	return ResolveMachineType(RoleWorker, providerName, def,
		SizeCandidate{fromGroup, SizeFromNodeGroup},
		SizeCandidate{fromMembers, SizeFromExistingMembers},
		SizeCandidate{fromProvider, SizeFromProvider},
		SizeCandidate{fromLegacy, SizeFromLegacyKey})
}

// ConfigString reads a string setting from a provider map, looking at the top
// level first (where environment clusterConfig values land) and then in the
// nested `config:` section, matching keys ignoring case and separators — the
// same rule clusterConfig uses, so controlPlaneMachineType,
// control_plane_machine_type and ControlPlaneMachineType are one key.
func ConfigString(m map[string]interface{}, keys ...string) string {
	if m == nil {
		return ""
	}
	want := map[string]bool{}
	for _, k := range keys {
		want[normalizeClusterConfigKey(k)] = true
	}
	lookup := func(section map[string]interface{}) string {
		for k, v := range section {
			if !want[normalizeClusterConfigKey(k)] {
				continue
			}
			if s := scalarToString(v); s != "" {
				return s
			}
		}
		return ""
	}
	if v := lookup(m); v != "" {
		return v
	}
	if section, ok := m["config"].(map[string]interface{}); ok {
		return lookup(section)
	}
	return ""
}

// ConfigStringList reads a list of strings (YAML sequence or comma-separated
// string) the same way ConfigString reads a scalar.
func ConfigStringList(m map[string]interface{}, keys ...string) []string {
	if m == nil {
		return nil
	}
	want := map[string]bool{}
	for _, k := range keys {
		want[normalizeClusterConfigKey(k)] = true
	}
	lookup := func(section map[string]interface{}) []string {
		for k, v := range section {
			if !want[normalizeClusterConfigKey(k)] {
				continue
			}
			var out []string
			switch t := v.(type) {
			case []interface{}:
				for _, item := range t {
					if s := scalarToString(item); s != "" {
						out = append(out, s)
					}
				}
			case []string:
				for _, s := range t {
					if s = strings.TrimSpace(s); s != "" {
						out = append(out, s)
					}
				}
			case string:
				for _, s := range strings.Split(t, ",") {
					if s = strings.TrimSpace(s); s != "" {
						out = append(out, s)
					}
				}
			}
			if len(out) > 0 {
				return out
			}
		}
		return nil
	}
	if v := lookup(m); len(v) > 0 {
		return v
	}
	if section, ok := m["config"].(map[string]interface{}); ok {
		return lookup(section)
	}
	return nil
}

// ConfigInt reads a positive integer setting the way ConfigString reads a
// string, accepting every scalar a YAML or JSON decoder produces (YAML gives
// `diskSizeGb: 256` as an int, a JSON round trip as a float64, clusterConfig as
// a string). Returns 0 when absent or not a positive integer.
func ConfigInt(m map[string]interface{}, keys ...string) int {
	n, err := strconv.Atoi(ConfigString(m, keys...))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func scalarToString(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case int:
		return strconv.Itoa(t)
	case int32:
		return strconv.FormatInt(int64(t), 10)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		return ""
	}
}

// vcpuPatterns estimate a machine type's vCPU count from its NAME, per cloud.
// Only used to warn; an unrecognised name yields 0 and no warning.
var vcpuPatterns = map[string][]*regexp.Regexp{
	// Azure: Standard_D4s_v5, Standard_E4bds_v5, Standard_B2s
	"azure": {regexp.MustCompile(`(?i)^standard_[a-z]+?(\d+)`)},
	// GCP: e2-standard-4, n2-highmem-8, c3-standard-22
	"gcp": {regexp.MustCompile(`-(\d+)$`)},
	// DigitalOcean: s-8vcpu-16gb, g-4vcpu-16gb, c-8, m-2vcpu-16gb
	"digitalocean": {regexp.MustCompile(`-(\d+)vcpu-`), regexp.MustCompile(`^c2?-(\d+)$`)},
}

// awsSizeVCPU maps the AWS size suffix to vCPUs for the common (Intel/AMD/Graviton
// general, compute and memory) families.
var awsSizeVCPU = map[string]int{
	"nano": 2, "micro": 2, "small": 2, "medium": 2, "large": 2, "xlarge": 4,
	"2xlarge": 8, "4xlarge": 16, "8xlarge": 32, "12xlarge": 48, "16xlarge": 64,
}

// civoSizeVCPU covers the Civo sizes the docs and shipped configs use.
var civoSizeVCPU = map[string]int{
	"g3.xsmall": 1, "g3.small": 1, "g3.medium": 2, "g3.large": 4, "g3.xlarge": 6, "g3.2xlarge": 8,
	"g4s.kube.xsmall": 1, "g4s.kube.small": 1, "g4s.kube.medium": 2, "g4s.kube.large": 4, "g4s.kube.xlarge": 6,
	"g4c.kube.small": 8, "g4c.kube.medium": 16, "g4c.kube.large": 32,
}

// EstimateVCPU returns the vCPU count a machine type's name implies, or 0 when
// the name is not one this function recognises.
func EstimateVCPU(providerName, machineType string) int {
	mt := strings.TrimSpace(machineType)
	switch CanonicalProviderName(providerName) {
	case "aws":
		if i := strings.LastIndex(mt, "."); i >= 0 {
			return awsSizeVCPU[strings.ToLower(mt[i+1:])]
		}
		return 0
	case "civo":
		return civoSizeVCPU[strings.ToLower(mt)]
	}
	for _, re := range vcpuPatterns[CanonicalProviderName(providerName)] {
		if m := re.FindStringSubmatch(mt); len(m) == 2 {
			if n, err := strconv.Atoi(m[1]); err == nil {
				return n
			}
		}
	}
	return 0
}

// MinControlPlaneVCPU is the floor the control-plane collapse of 2026-10-11 set.
const MinControlPlaneVCPU = 4

// ControlPlaneSizeWarning returns advice when a control-plane size is known to be
// below the floor, or "" when it is fine or unrecognised. Advice only: an
// operator may knowingly run a smaller control plane for a trimmed profile.
func ControlPlaneSizeWarning(providerName, machineType string) string {
	n := EstimateVCPU(providerName, machineType)
	if n == 0 || n >= MinControlPlaneVCPU {
		return ""
	}
	return fmt.Sprintf("control plane %s has %d vCPU; below %d vCPU the full platform's ~600 CRDs keep kube-apiserver "+
		"pegged and kube-controller-manager/kube-scheduler crash-loop on lease renewal — set %s to a %d+ vCPU size",
		machineType, n, MinControlPlaneVCPU, KeyControlPlaneMachineType, MinControlPlaneVCPU)
}

// legacySizeKeys is, per provider, the single provider-level size key that
// predates the control-plane/worker split, and whether the provider reads it
// from the top level too (GCP flattens its `config:` section into the top
// level, where environment clusterConfig values also land — so an environment
// `machineType` sizes a GCP control plane that nothing else names).
var legacySizeKeys = map[string]struct {
	keys     []string
	topLevel bool
}{
	"azure":        {keys: []string{"vmSize", "machineType"}},
	"gcp":          {keys: []string{"machineType", "machine_type"}, topLevel: true},
	"digitalocean": {keys: []string{"droplet_size"}},
	"civo":         {keys: []string{"size"}},
}

// LegacySize returns the provider's legacy single size from a provider map,
// read exactly as that provider reads it.
func LegacySize(providerName string, m map[string]interface{}) string {
	l, ok := legacySizeKeys[CanonicalProviderName(providerName)]
	if !ok {
		return ""
	}
	if l.topLevel {
		return ConfigString(m, l.keys...)
	}
	section, _ := m["config"].(map[string]interface{})
	return ConfigString(map[string]interface{}{"config": section}, l.keys...)
}

// EffectiveSizes is what an environment will build, resolved the way the
// provider resolves it. Used by `adhar config validate`, the provisioning
// warnings and the tests that pin every shipped config's result.
type EffectiveSizes struct {
	ControlPlane string // "" in managed mode and for kind/custom
	Worker       string
	Workers      int
	Managed      bool
}

// ResolveEffectiveSizes resolves an environment's control-plane and primary
// worker size and starting count.
func ResolveEffectiveSizes(envConfig *config.ResolvedEnvironmentConfig) (EffectiveSizes, error) {
	spec, err := buildClusterSpec(envConfig, "preview")
	if err != nil {
		return EffectiveSizes{}, err
	}
	m := buildProviderConfig(envConfig)
	ptype := envProviderType(envConfig)
	managed := false
	if mode, err := ParseClusterMode(m); err == nil {
		managed = ClusterModeIsManaged(mode)
	}
	legacy := LegacySize(ptype, m)
	out := EffectiveSizes{Workers: spec.NodeGroups[0].Replicas, Managed: managed}
	if !managed {
		out.ControlPlane = ControlPlaneSize(ptype, spec.ControlPlane.InstanceType, ConfigString(m, KeyControlPlaneMachineType), legacy)
	}
	out.Worker = WorkerSize(ptype, managed, spec.NodeGroups[0].InstanceType, "", ConfigString(m, KeyWorkerMachineType), legacy)
	return out, nil
}

// SizingWarnings returns non-fatal advice about an environment's sizes.
func SizingWarnings(envName string, envConfig *config.ResolvedEnvironmentConfig) []string {
	sizes, err := ResolveEffectiveSizes(envConfig)
	if err != nil {
		return nil
	}
	var out []string
	if w := ControlPlaneSizeWarning(envProviderType(envConfig), sizes.ControlPlane); w != "" {
		out = append(out, fmt.Sprintf("environment %s: %s", envName, w))
	}
	// Azure reads disk settings from providers.azure.config only. An
	// environment-level diskSizeGb has never reached it; say so instead of
	// letting the file claim a size the disks do not have.
	if CanonicalProviderName(envProviderType(envConfig)) == "azure" && envConfig.ProviderConfig != nil {
		envDisk := clusterConfigString(envConfig, "diskSizeGb", "diskSize")
		provDisk := ConfigString(map[string]interface{}{"config": envConfig.ProviderConfig.Config}, "diskSizeGb", "diskSize")
		if envDisk != "" && provDisk != "" && envDisk != provDisk {
			out = append(out, fmt.Sprintf("environment %s: clusterConfig diskSizeGb=%s is ignored on Azure; the OS disks are "+
				"providers.%s.config.diskSizeGb=%s (Azure reads disk settings from the provider block only)",
				envName, envDisk, envConfig.ResolvedProvider, provDisk))
		}
	}
	return out
}

// PreviewClusterSpec is the cluster spec `adhar up` would build for an
// environment, and ProviderMap the provider map it would construct the provider
// with. Exported so each provider's tests can push a real config file through
// that provider's OWN parsing and size resolution.
func PreviewClusterSpec(envConfig *config.ResolvedEnvironmentConfig) (*types.ClusterSpec, error) {
	return buildClusterSpec(envConfig, "preview")
}

// ProviderMap — see PreviewClusterSpec.
func ProviderMap(envConfig *config.ResolvedEnvironmentConfig) map[string]interface{} {
	return buildProviderConfig(envConfig)
}
