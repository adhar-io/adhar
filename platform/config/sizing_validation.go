package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Validation of everything that sizes and places infrastructure: regions,
// worker counts, autoscaling ranges, disk sizes and additional node groups.
//
// Each of these used to be read with a "parse, and on failure use the default"
// helper, so `nodeCount: two` built the default worker count, `minWorkers: 5,
// maxWorkers: 2` reached the autoscaler, and `region: us-east-1` on Azure failed
// twenty minutes into a run. They are now refused by `adhar config validate` and
// by every command that loads the file.

// regionFormats is the SHAPE of a region name per provider. A format, not a
// list: the clouds add regions faster than a list in this repository is
// updated (the list in providers/factory.go has neither Azure's centralindia
// nor Civo's mum1, both of which run clusters), so a list would refuse valid
// regions. A format still catches the common mistake — another cloud's region,
// or a zone where a region belongs.
var regionFormats = map[string]struct {
	re      *regexp.Regexp
	example string
}{
	// us-east-1, ap-southeast-1, us-gov-west-1, il-central-1
	"aws": {regexp.MustCompile(`^[a-z]{2}(-gov|-iso[a-z]?)?-[a-z]+-\d+$`), "ap-southeast-1"},
	// us-central1, asia-southeast1, northamerica-northeast2 (NOT a zone like us-central1-a)
	"gcp": {regexp.MustCompile(`^[a-z]+-[a-z]+\d+$`), "asia-southeast1"},
	// centralindia, eastus2, or the display form "East US" the provider normalises
	"azure": {regexp.MustCompile(`^[A-Za-z][A-Za-z0-9 ]*$`), "centralindia"},
	// blr1, nyc3
	"digitalocean": {regexp.MustCompile(`^[a-z]{3}\d$`), "blr1"},
	// LON1, mum1
	"civo": {regexp.MustCompile(`^[A-Za-z]{3}\d$`), "LON1"},
}

// gcpZoneFormat is a GCP zone: <region>-<letter>.
var gcpZoneFormat = regexp.MustCompile(`^([a-z]+-[a-z]+\d+)-[a-z]$`)

// dns1123Label is a Kubernetes/VM-name-safe label.
var dns1123Label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// maxNodeGroupNameLength keeps <cluster>-worker-<group>-<n> inside the VM-name
// limits of every cloud (Azure: 64, GCP: 63) for cluster names the platform
// generates.
const maxNodeGroupNameLength = 20

// validTaintEffects are the three effects Kubernetes accepts.
var validTaintEffects = map[string]bool{"NoSchedule": true, "PreferNoSchedule": true, "NoExecute": true}

// validateRegion checks a region's format for a provider type.
func (v *SchemaValidator) validateRegion(field, providerType, region string) {
	region = strings.TrimSpace(region)
	if region == "" {
		return
	}
	f, ok := regionFormats[providerType]
	if !ok || f.re.MatchString(region) {
		return
	}
	hint := ""
	if providerType == "gcp" && gcpZoneFormat.MatchString(region) {
		hint = fmt.Sprintf(" — %q is a ZONE; put the region (%s) here and the zone under config.zone", region, gcpZoneFormat.FindStringSubmatch(region)[1])
	}
	v.addError(field, region, fmt.Sprintf("not a %s region name (expected something like %q)%s", providerType, f.example, hint))
}

// validateProviderSizing checks the sizing keys a provider block may carry.
func (v *SchemaValidator) validateProviderSizing(name string, provider ConfigProviderConfig) {
	v.validateRegion(fmt.Sprintf("providers.%s.region", name), provider.Type, provider.Region)
	section := provider.Config
	if section == nil {
		return
	}
	for _, key := range []string{"controlPlaneMachineType", "workerMachineType"} {
		if raw, ok := lookupFolded(section, key); ok {
			if s, isString := raw.(string); !isString || strings.TrimSpace(s) == "" {
				v.addError(fmt.Sprintf("providers.%s.config.%s", name, key), raw, "must be a non-empty machine type")
			}
		}
	}
	for _, key := range []string{"diskSizeGb", "disk_size_gb"} {
		if raw, ok := lookupFolded(section, key); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(fmt.Sprint(raw))); err != nil || n <= 0 {
				v.addError(fmt.Sprintf("providers.%s.config.%s", name, key), raw, "must be a positive whole number of GiB")
			}
		}
	}
	if provider.Type == "gcp" {
		if raw, ok := lookupFolded(section, "zone"); ok {
			zone := strings.TrimSpace(fmt.Sprint(raw))
			m := gcpZoneFormat.FindStringSubmatch(zone)
			switch {
			case zone == "":
				// empty is reported by validateGCPConfig
			case m == nil:
				v.addError(fmt.Sprintf("providers.%s.config.zone", name), zone, "not a GCP zone name (expected <region>-<letter>, e.g. \"asia-southeast1-a\")")
			case provider.Region != "" && m[1] != provider.Region:
				v.addError(fmt.Sprintf("providers.%s.config.zone", name), zone,
					fmt.Sprintf("zone is in region %s but the provider region is %s; instances would be created outside the region's network", m[1], provider.Region))
			}
		}
	}
}

// lookupFolded finds a key ignoring case and separators — viper lower-cases
// nested map keys, so `diskSizeGb` arrives as `disksizegb`.
func lookupFolded(m map[string]interface{}, key string) (interface{}, bool) {
	want := foldKey(key)
	for k, v := range m {
		if foldKey(k) == want {
			return v, true
		}
	}
	return nil, false
}

func foldKey(k string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(k) {
		switch r {
		case '_', '-', '.', ' ':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// countKeys are clusterConfig keys that hold a node count.
var countKeys = map[string]bool{"nodecount": true, "workerreplicas": true, "numnodes": true, "controlplanereplicas": true}

// sizeKeys are clusterConfig keys that hold a disk size in GiB.
var sizeKeys = map[string]bool{"disksizegb": true, "disksize": true, "rootvolumesize": true}

// machineKeys are clusterConfig keys that hold a machine type.
var machineKeys = map[string]bool{"machinetype": true, "nodesize": true, "instancetype": true, "nodeinstancetype": true,
	"workermachinetype": true, "controlplanemachinetype": true, "controlplaneinstancetype": true, "mastermachinetype": true}

// validateClusterConfigSizing checks the numeric and machine-type entries of a
// clusterConfig list.
func (v *SchemaValidator) validateClusterConfigSizing(field string, kvs []KeyValueConfig) {
	for i, kv := range kvs {
		key := foldKey(kv.Key)
		path := fmt.Sprintf("%s[%d] (%s)", field, i, kv.Key)
		value := strings.TrimSpace(kv.Value)
		switch {
		case countKeys[key]:
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 {
				v.addError(path, kv.Value, "must be a whole number of nodes, 0 or more")
			}
			if err == nil && key == "controlplanereplicas" && n > 1 {
				v.addError(path, kv.Value, "only a single control-plane node is supported (HA control planes are not implemented)")
			}
		case sizeKeys[key]:
			if n, err := strconv.Atoi(value); err != nil || n <= 0 {
				v.addError(path, kv.Value, "must be a positive whole number of GiB")
			}
		case machineKeys[key]:
			if value == "" {
				v.addError(path, kv.Value, "must name a machine type")
			}
		}
	}
}

// validateAutoscaling checks one autoscaling block.
func (v *SchemaValidator) validateAutoscaling(field string, as *AutoscalingConfig) {
	if as == nil {
		return
	}
	if as.MinWorkers < 0 {
		v.addError(field+".minWorkers", as.MinWorkers, "must be 0 or more")
	}
	if as.MaxWorkers < 0 {
		v.addError(field+".maxWorkers", as.MaxWorkers, "must be 0 or more")
	}
	if as.Enabled && as.MaxWorkers > 0 && as.MinWorkers > as.MaxWorkers {
		v.addError(field, fmt.Sprintf("minWorkers=%d maxWorkers=%d", as.MinWorkers, as.MaxWorkers),
			"minWorkers is greater than maxWorkers; the autoscaler could satisfy neither bound")
	}
	if as.Enabled && as.MaxWorkers == 0 {
		v.addError(field+".maxWorkers", as.MaxWorkers, "autoscaling is enabled but maxWorkers is 0; set the spend ceiling (or disable autoscaling)")
	}
	if as.NodeGroup != "" && !dns1123Label.MatchString(as.NodeGroup) {
		v.addError(field+".nodeGroup", as.NodeGroup, "must be a lower-case DNS-1123 label")
	}
}

// validateNodeGroups checks a nodeGroups list.
func (v *SchemaValidator) validateNodeGroups(field string, groups []NodeGroupConfig) {
	seen := map[string]bool{}
	for i, g := range groups {
		path := fmt.Sprintf("%s[%d]", field, i)
		switch {
		case g.Name == "":
			v.addError(path+".name", g.Name, "a node group needs a name")
		case g.Name == PrimaryNodeGroupName:
			v.addError(path+".name", g.Name, fmt.Sprintf("%q is the primary node group; size it with clusterConfig machineType/nodeCount and the autoscaling block, and use nodeGroups only for ADDITIONAL groups", PrimaryNodeGroupName))
		case !dns1123Label.MatchString(g.Name) || len(g.Name) > maxNodeGroupNameLength:
			v.addError(path+".name", g.Name, fmt.Sprintf("must be a lower-case DNS-1123 label of at most %d characters (it becomes part of every VM name)", maxNodeGroupNameLength))
		case seen[g.Name]:
			v.addError(path+".name", g.Name, "duplicate node group name")
		}
		seen[g.Name] = true
		if g.Count < 1 {
			v.addError(path+".count", g.Count, "an additional node group needs at least 1 node")
		}
		for j, t := range g.Taints {
			tp := fmt.Sprintf("%s.taints[%d]", path, j)
			if t.Key == "" {
				v.addError(tp+".key", t.Key, "a taint needs a key")
			}
			if !validTaintEffects[t.Effect] {
				v.addError(tp+".effect", t.Effect, "must be NoSchedule, PreferNoSchedule or NoExecute")
			}
		}
	}
}

// validateSizing runs every sizing check over a whole configuration.
func (v *SchemaValidator) validateSizing(config *Config) {
	for name, provider := range config.Providers {
		v.validateProviderSizing(name, provider)
	}
	for name, tmpl := range config.EnvironmentTemplates {
		base := fmt.Sprintf("environmentTemplates.%s", name)
		v.validateClusterConfigSizing(base+".clusterConfig", tmpl.ClusterConfig)
		v.validateAutoscaling(base+".autoscaling", tmpl.Autoscaling)
		v.validateNodeGroups(base+".nodeGroups", tmpl.NodeGroups)
	}
	for name, env := range config.Environments {
		base := fmt.Sprintf("environments.%s", name)
		v.validateClusterConfigSizing(base+".clusterConfig", env.ClusterConfig)
		v.validateAutoscaling(base+".autoscaling", env.Autoscaling)
		v.validateNodeGroups(base+".nodeGroups", env.NodeGroups)
		providerType := ""
		if p, ok := config.Providers[env.Provider]; ok {
			providerType = p.Type
		} else if env.Provider == "" && len(config.Providers) == 1 {
			for _, p := range config.Providers {
				providerType = p.Type
			}
		}
		v.validateRegion(base+".region", providerType, env.Region)
	}
}
