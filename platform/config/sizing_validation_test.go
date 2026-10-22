package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validCloudConfig is a minimal valid configuration for one cloud provider.
func validCloudConfig(providerType, region string) *Config {
	return &Config{
		GlobalSettings: GlobalSettingsConfig{AdharContext: "c", DefaultHost: "h", DefaultHttpPort: 80, DefaultHttpsPort: 443, Email: "e@x"},
		Providers:      map[string]ConfigProviderConfig{providerType: {Type: providerType, Region: region, Primary: true}},
		EnvironmentTemplates: map[string]EnvironmentTemplateConfig{
			"t": {ClusterConfig: []KeyValueConfig{{Key: "autoScale", Value: "true"}}, CoreServices: map[string]ServiceConfig{"argocd": {}}},
		},
		Environments: map[string]EnvironmentConfig{
			"dev": {Type: EnvironmentTypeNonProduction, Provider: providerType, Template: "t",
				ClusterConfig: []KeyValueConfig{{Key: "name", Value: "adhar-dev"}}},
		},
	}
}

func TestSizingValidationRefusesWhatWouldBuildTheWrongThing(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		field  string // must appear in the error
	}{
		{"non-numeric nodeCount", func(c *Config) { addKV(c, "nodeCount", "two") }, "nodeCount"},
		{"negative nodeCount", func(c *Config) { addKV(c, "node_count", "-1") }, "node_count"},
		{"zero diskSizeGb", func(c *Config) { addKV(c, "diskSizeGb", "0") }, "diskSizeGb"},
		{"empty machineType", func(c *Config) { addKV(c, "machineType", " ") }, "machineType"},
		{"HA control plane", func(c *Config) { addKV(c, "controlPlaneReplicas", "3") }, "controlPlaneReplicas"},
		{"min > max", func(c *Config) { setAS(c, &AutoscalingConfig{Enabled: true, MinWorkers: 5, MaxWorkers: 2}) }, "greater than maxWorkers"},
		{"negative min", func(c *Config) { setAS(c, &AutoscalingConfig{MinWorkers: -1}) }, "minWorkers"},
		{"enabled with no ceiling", func(c *Config) { setAS(c, &AutoscalingConfig{Enabled: true, MinWorkers: 1}) }, "maxWorkers"},
		{"node group named workers", func(c *Config) { setNG(c, NodeGroupConfig{Name: "workers", Count: 1}) }, "primary node group"},
		{"node group bad name", func(c *Config) { setNG(c, NodeGroupConfig{Name: "GPU_pool", Count: 1}) }, "DNS-1123"},
		{"node group zero count", func(c *Config) { setNG(c, NodeGroupConfig{Name: "gpu", Count: 0}) }, "count"},
		{"duplicate node group", func(c *Config) {
			setNG(c, NodeGroupConfig{Name: "gpu", Count: 1}, NodeGroupConfig{Name: "gpu", Count: 1})
		}, "duplicate"},
		{"bad taint effect", func(c *Config) {
			setNG(c, NodeGroupConfig{Name: "gpu", Count: 1, Taints: []TaintConfig{{Key: "k", Effect: "NoScheduled"}}})
		}, "effect"},
		{"empty provider controlPlaneMachineType", func(c *Config) { setSection(c, "controlplanemachinetype", "") }, "controlPlaneMachineType"},
		{"provider diskSizeGb not a number", func(c *Config) { setSection(c, "disksizegb", "big") }, "diskSizeGb"},
		{"another cloud's region", func(c *Config) { setRegion(c, "centralindia") }, "region"},
		{"template autoscaling min > max", func(c *Config) {
			tm := c.EnvironmentTemplates["t"]
			tm.Autoscaling = &AutoscalingConfig{Enabled: true, MinWorkers: 9, MaxWorkers: 3}
			c.EnvironmentTemplates["t"] = tm
		}, "environmentTemplates.t.autoscaling"},
		{"environment region format", func(c *Config) {
			e := c.Environments["dev"]
			e.Region = "eu west"
			c.Environments["dev"] = e
		}, "environments.dev.region"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validCloudConfig("aws", "ap-southeast-1")
			if err := ValidateConfig(cfg); err != nil {
				t.Fatalf("baseline is invalid: %v", err)
			}
			tc.mutate(cfg)
			err := ValidateConfig(cfg)
			if err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Errorf("want an error naming %q, got %v", tc.field, err)
			}
		})
	}
}

func TestRegionFormatsPerProvider(t *testing.T) {
	for _, c := range []struct {
		provider, region string
		ok               bool
	}{
		{"aws", "ap-southeast-1", true}, {"aws", "us-gov-west-1", true}, {"aws", "eastus", false},
		{"gcp", "asia-southeast1", true}, {"gcp", "northamerica-northeast2", true}, {"gcp", "asia-southeast1-a", false},
		{"azure", "centralindia", true}, {"azure", "East US", true}, {"azure", "us-east-1", false},
		{"digitalocean", "blr1", true}, {"digitalocean", "nyc", false},
		{"civo", "LON1", true}, {"civo", "mum1", true}, {"civo", "london", false},
		{"kind", "anything goes", true}, {"custom", "on-premises", true},
	} {
		err := ValidateConfig(validCloudConfig(c.provider, c.region))
		if (err == nil) != c.ok {
			t.Errorf("%s region %q: valid=%v, want %v (%v)", c.provider, c.region, err == nil, c.ok, err)
		}
	}
	// A zone in the region slot says which value belongs where.
	err := ValidateConfig(validCloudConfig("gcp", "asia-southeast1-a"))
	if err == nil || !strings.Contains(err.Error(), "is a ZONE") {
		t.Errorf("want the zone hint, got %v", err)
	}
	cfg := validCloudConfig("gcp", "asia-southeast1")
	setSection(cfg, "zone", "us-central1-a")
	if err := ValidateConfig(cfg); err == nil || !strings.Contains(err.Error(), "zone is in region us-central1") {
		t.Errorf("a zone outside the region must be refused, got %v", err)
	}
}

func TestValidSizingIsAccepted(t *testing.T) {
	cfg := validCloudConfig("aws", "ap-southeast-1")
	addKV(cfg, "nodeCount", "3")
	addKV(cfg, "diskSizeGb", "256")
	addKV(cfg, "controlPlaneMachineType", "m6i.xlarge")
	setAS(cfg, &AutoscalingConfig{Enabled: true, MinWorkers: 2, MaxWorkers: 6, NodeGroup: "gpu"})
	setNG(cfg, NodeGroupConfig{Name: "gpu", MachineType: "g5.xlarge", Count: 1,
		Taints: []TaintConfig{{Key: "nvidia.com/gpu", Value: "true", Effect: "NoSchedule"}}})
	setSection(cfg, "workermachinetype", "r6i.xlarge")
	if err := ValidateConfig(cfg); err != nil {
		t.Errorf("valid sizing refused: %v", err)
	}
}

// nodeGroups resolve like autoscaling: the environment's list, else the template's.
func TestNodeGroupsResolveFromTheTemplate(t *testing.T) {
	cfg := validCloudConfig("aws", "ap-southeast-1")
	tm := cfg.EnvironmentTemplates["t"]
	tm.NodeGroups = []NodeGroupConfig{{Name: "highmem", Count: 1}}
	cfg.EnvironmentTemplates["t"] = tm
	if err := cfg.ResolveEnvironments(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.ResolvedEnvironments["dev"].NodeGroups; len(got) != 1 || got[0].Name != "highmem" {
		t.Errorf("template node groups not inherited: %+v", got)
	}
	setNG(cfg, NodeGroupConfig{Name: "gpu", Count: 2})
	cfg.ResolvedEnvironments = nil
	if err := cfg.ResolveEnvironments(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.ResolvedEnvironments["dev"].NodeGroups; len(got) != 1 || got[0].Name != "gpu" {
		t.Errorf("the environment's list must replace the template's: %+v", got)
	}
}

// nodeGroups and vpcConfig must survive the YAML → viper → struct path (viper
// lower-cases map keys, and a struct with no field for a key drops it).
func TestNodeGroupsAndVPCConfigLoadFromYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `globalSettings: {adharContext: c, defaultHost: h, defaultHttpPort: 80, defaultHttpsPort: 443, email: e@x}
providers:
  aws:
    type: aws
    region: ap-southeast-1
    primary: true
    vpcConfig: {cidr: 10.9.0.0/16, subnetCidrs: [10.9.1.0/24, 10.9.2.0/24]}
environmentTemplates:
  t: {clusterConfig: [{key: autoScale, value: "true"}], coreServices: {argocd: {chart: {name: argo-cd}}}}
environments:
  dev:
    type: non-production
    template: t
    clusterConfig: [{key: name, value: adhar-dev}]
    nodeGroups:
      - name: gpu
        machineType: g5.xlarge
        count: 1
        labels: {adhar.io/accelerator: nvidia}
        taints: [{key: nvidia.com/gpu, value: "true", effect: NoSchedule}]
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	ng := cfg.Environments["dev"].NodeGroups
	if len(ng) != 1 || ng[0].MachineType != "g5.xlarge" || ng[0].Count != 1 || len(ng[0].Taints) != 1 || ng[0].Taints[0].Effect != "NoSchedule" {
		t.Errorf("nodeGroups = %+v", ng)
	}
	aws := cfg.Providers["aws"]
	if v := aws.ToProviderMap()["vpcConfig"]; v == nil {
		t.Error("vpcConfig dropped on load: the AWS provider would build its hardcoded network")
	}
}

func addKV(c *Config, k, v string) {
	e := c.Environments["dev"]
	e.ClusterConfig = append(e.ClusterConfig, KeyValueConfig{Key: k, Value: v})
	c.Environments["dev"] = e
}

func setAS(c *Config, as *AutoscalingConfig) {
	e := c.Environments["dev"]
	e.Autoscaling = as
	c.Environments["dev"] = e
}

func setNG(c *Config, groups ...NodeGroupConfig) {
	e := c.Environments["dev"]
	e.NodeGroups = groups
	c.Environments["dev"] = e
}

func setSection(c *Config, k string, v interface{}) {
	for name, p := range c.Providers {
		if p.Config == nil {
			p.Config = map[string]interface{}{}
		}
		p.Config[k] = v
		c.Providers[name] = p
	}
}

func setRegion(c *Config, region string) {
	for name, p := range c.Providers {
		p.Region = region
		c.Providers[name] = p
	}
}
