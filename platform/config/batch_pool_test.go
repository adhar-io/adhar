package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"adhar-io/adhar/globals"
)

// A batch node pool (design 0025 D14) is declared once, as `batchPool`, and is
// refused where it would build something the pool's rules cannot serve.
func TestBatchPoolValidation(t *testing.T) {
	cases := []struct {
		name  string
		pool  *BatchPoolConfig
		group *NodeGroupConfig
		field string // "" = valid
	}{
		{name: "valid", pool: &BatchPoolConfig{MachineType: "c6i.2xlarge", MinNodes: 1, MaxNodes: 4}},
		{name: "fixed size", pool: &BatchPoolConfig{MinNodes: 2, MaxNodes: 2}},
		// A pool at zero would not exist when a run's pod is admitted, so the run
		// would carry no toleration and could never use the node its demand bought.
		{name: "empty pool", pool: &BatchPoolConfig{MinNodes: 0, MaxNodes: 3}, field: "batchPool.minNodes"},
		{name: "ceiling below floor", pool: &BatchPoolConfig{MinNodes: 3, MaxNodes: 2}, field: "batchPool.maxNodes"},
		// The pool's nodes are recognised by their machine names; a hand-written
		// group called `batch` would come up with the pool's taint and no bounds.
		{name: "nodeGroups named batch", group: &NodeGroupConfig{Name: "batch", Count: 1}, field: "reserved for the batch node pool"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validCloudConfig("aws", "ap-southeast-1")
			e := cfg.Environments["dev"]
			e.BatchPool = tc.pool
			if tc.group != nil {
				e.NodeGroups = []NodeGroupConfig{*tc.group}
			}
			cfg.Environments["dev"] = e
			err := ValidateConfig(cfg)
			switch {
			case tc.field == "" && err != nil:
				t.Errorf("valid pool refused: %v", err)
			case tc.field != "" && (err == nil || !strings.Contains(err.Error(), tc.field)):
				t.Errorf("want an error naming %q, got %v", tc.field, err)
			}
		})
	}
	// The template's block is checked too.
	cfg := validCloudConfig("aws", "ap-southeast-1")
	tm := cfg.EnvironmentTemplates["t"]
	tm.BatchPool = &BatchPoolConfig{MinNodes: 0, MaxNodes: 1}
	cfg.EnvironmentTemplates["t"] = tm
	if err := ValidateConfig(cfg); err == nil || !strings.Contains(err.Error(), "environmentTemplates.t.batchPool.minNodes") {
		t.Errorf("template pool not validated: %v", err)
	}
}

// The pool resolves like nodeGroups: the environment's block, else the template's.
func TestBatchPoolResolvesFromTheTemplate(t *testing.T) {
	cfg := validCloudConfig("aws", "ap-southeast-1")
	tm := cfg.EnvironmentTemplates["t"]
	tm.BatchPool = &BatchPoolConfig{MinNodes: 1, MaxNodes: 3}
	cfg.EnvironmentTemplates["t"] = tm
	if err := cfg.ResolveEnvironments(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.ResolvedEnvironments["dev"].BatchPool; got == nil || got.MaxNodes != 3 {
		t.Errorf("template pool not inherited: %+v", got)
	}
	e := cfg.Environments["dev"]
	e.BatchPool = &BatchPoolConfig{MachineType: "c6i.2xlarge", MinNodes: 2, MaxNodes: 6}
	cfg.Environments["dev"] = e
	cfg.ResolvedEnvironments = nil
	if err := cfg.ResolveEnvironments(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.ResolvedEnvironments["dev"].BatchPool; got == nil || got.MaxNodes != 6 || got.MachineType != "c6i.2xlarge" {
		t.Errorf("the environment's pool must replace the template's: %+v", got)
	}
}

// The pool is a node group carrying exactly the label and taint the Kyverno
// policy and the autoscaler key on.
func TestBatchPoolIsTheReservedGroupWithTheLabelAndTaint(t *testing.T) {
	g, ok := (&BatchPoolConfig{MachineType: "m", MinNodes: 2, MaxNodes: 5}).BatchPoolNodeGroup()
	if !ok || g.Name != globals.BatchNodeGroupName || g.Count != 2 || g.MachineType != "m" {
		t.Fatalf("pool group %+v", g)
	}
	if g.Labels[globals.BatchNodePoolLabel] != globals.BatchNodePoolValue {
		t.Errorf("labels %v", g.Labels)
	}
	if len(g.Taints) != 1 || g.Taints[0] != (TaintConfig{Key: globals.BatchNodePoolTaintKey, Value: globals.BatchNodePoolTaintValue, Effect: "NoSchedule"}) {
		t.Errorf("taints %v", g.Taints)
	}
	if _, ok := (*BatchPoolConfig)(nil).BatchPoolNodeGroup(); ok {
		t.Error("no pool must build no group")
	}
}

// The YAML spelling loads (mapstructure tags), and the schema and the annotated
// example both describe it.
func TestBatchPoolLoadsFromYAMLAndIsDocumented(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`globalSettings: {adharContext: c, defaultHost: h, defaultHttpPort: 80, defaultHttpsPort: 443, email: e@x}
providers:
  aws: {type: aws, region: ap-southeast-1, primary: true}
environmentTemplates:
  t:
    clusterConfig: [{key: autoScale, value: "true"}]
    coreServices: {argocd: {chart: {name: argo-cd}}}
environments:
  prod:
    type: production
    provider: aws
    template: t
    clusterConfig: [{key: name, value: adhar-prod}]
    batchPool: {machineType: c6i.2xlarge, minNodes: 1, maxNodes: 4}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Environments["prod"].BatchPool; got == nil || got.MinNodes != 1 || got.MaxNodes != 4 || got.MachineType != "c6i.2xlarge" {
		t.Errorf("batchPool not loaded: %+v", got)
	}

	root := repoRoot(t)
	for file, wants := range map[string][]string{
		"config.schema.json":   {`"batchPool": {`, `"$ref": "#/definitions/batchPool"`, `"enum": ["workers", "batch"]`},
		"examples/config.yaml": {"#       batchPool:", "#         minNodes: 1", "#         maxNodes: 4"},
	} {
		raw, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range wants {
			if !strings.Contains(string(raw), w) {
				t.Errorf("%s does not document the batch pool (%q)", file, w)
			}
		}
	}
}

// A batchPool where the platform builds no node groups is refused, not ignored:
// accepted, it built nothing while the node autoscaler was told a pool existed
// (and the custom provider's update path then failed on a `batch` group it
// cannot size). A template's pool inherited by such an environment is refused
// at the environment that inherits it.
func TestBatchPoolIsRefusedWhereNoNodeGroupIsBuilt(t *testing.T) {
	pool := &BatchPoolConfig{MinNodes: 1, MaxNodes: 2}
	for _, tc := range []struct {
		name, providerType, clusterMode, region string
		want                                    string // "" = accepted
	}{
		{"kind", "kind", "", "", "kind does not"},
		{"custom", "custom", "", "", "custom does not"},
		{"provided", "aws", "provided", "", "not built on a provided cluster"},
		{"provided, any case", "gcp", " Provided ", "", "not built on a provided cluster"},
		{"compute", "aws", "compute", "ap-southeast-1", ""},
		{"managed", "civo", "managed", "LON1", ""},
		{"default mode", "digitalocean", "", "sgp1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, inherited := range []bool{false, true} {
				cfg := validCloudConfig(tc.providerType, tc.region)
				p := cfg.Providers[tc.providerType]
				p.ClusterMode = tc.clusterMode
				cfg.Providers[tc.providerType] = p
				if inherited {
					tm := cfg.EnvironmentTemplates["t"]
					tm.BatchPool = pool
					cfg.EnvironmentTemplates["t"] = tm
				} else {
					e := cfg.Environments["dev"]
					e.BatchPool = pool
					cfg.Environments["dev"] = e
				}
				err := ValidateConfig(cfg)
				if tc.want == "" {
					if err != nil && strings.Contains(err.Error(), "batchPool") {
						t.Errorf("inherited=%v: a pool on %s/%q refused: %v", inherited, tc.providerType, tc.clusterMode, err)
					}
					continue
				}
				if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "batchPool") {
					t.Errorf("inherited=%v: want a batchPool error containing %q, got %v", inherited, tc.want, err)
				}
				if inherited && err != nil && !strings.Contains(err.Error(), "inherited by environments.dev") {
					t.Errorf("an inherited pool's error does not name the environment: %v", err)
				}
			}
		})
	}
}
