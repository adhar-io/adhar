package config

import (
	"fmt"
	"os"

	"adhar-io/adhar/globals"
	platformconfig "adhar-io/adhar/platform/config"

	"github.com/spf13/cobra"
)

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Scaffold a new configuration file",
	Long: `Scaffold a starter Adhar config.yaml for a provider, based on an
environment template. The generated file passes 'adhar config validate' and can
be edited further.

Examples:
  adhar config create                                  # local Kind starter
  adhar config create --provider=aws --region=us-east-1
  adhar config create --provider=civo --name=./civo.yaml --template=nonprod-defaults`,
	RunE: runCreate,
}

var (
	createProvider string
	createRegion   string
	createName     string
	createTemplate string
	createForce    bool
)

func init() {
	createCmd.Flags().StringVarP(&createProvider, "provider", "p", "kind", "Cloud provider (kind, aws, azure, gcp, digitalocean, civo, custom)")
	createCmd.Flags().StringVarP(&createRegion, "region", "r", "", "Cloud region")
	createCmd.Flags().StringVarP(&createName, "name", "n", "", "Output file path (default ./config.yaml or --file)")
	createCmd.Flags().StringVarP(&createTemplate, "template", "t", "nonprod-defaults", "Environment template name to scaffold")
	createCmd.Flags().BoolVar(&createForce, "force", false, "Overwrite an existing file")
}

func runCreate(cmd *cobra.Command, args []string) error {
	outPath := createName
	if outPath == "" {
		outPath = configFile
	}
	if outPath == "" {
		outPath = "./config.yaml"
	}

	if _, err := os.Stat(outPath); err == nil && !createForce {
		return fmt.Errorf("%s already exists (use --force to overwrite)", outPath)
	}

	region := createRegion
	if region == "" {
		if createProvider == "kind" {
			region = "local"
		} else {
			region = "us-east-1"
		}
	}

	cfg := starterConfig(createProvider, region, createTemplate)

	// Validate what we scaffolded before persisting so we never write an invalid
	// starter file.
	if err := platformconfig.ValidateConfig(cfg); err != nil {
		return fmt.Errorf("generated configuration failed validation: %w", err)
	}
	if err := platformconfig.SaveConfig(cfg, outPath); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}

	fmt.Printf("📝 Created starter configuration: %s\n", outPath)
	fmt.Printf("   provider=%s  region=%s  template=%s\n", createProvider, region, createTemplate)
	fmt.Println("   Validate it any time with: adhar config validate " + outPath)
	return nil
}

// starterConfig builds a minimal, schema-valid configuration for the given
// provider using a single environment template and one dev environment.
func starterConfig(provider, region, templateName string) *platformconfig.Config {
	return &platformconfig.Config{
		GlobalSettings: platformconfig.GlobalSettingsConfig{
			AdharContext: "adhar-mgmt",
			// The LOCAL default, not a real domain. Every URL, the DNS zone
			// external-dns manages and the wildcard certificate all derive from
			// this one value, so a starter file must not name a domain the reader
			// does not own — they set their own here before the first cloud run.
			DefaultHost:      globals.DefaultHostName,
			DefaultHttpPort:  80,
			DefaultHttpsPort: 8443,
			EnableHAMode:     false,
			Email:            "you@example.com",
		},
		Providers: map[string]platformconfig.ConfigProviderConfig{
			provider: {
				Type:    provider,
				Region:  region,
				Primary: true,
			},
		},
		EnvironmentTemplates: map[string]platformconfig.EnvironmentTemplateConfig{
			templateName: {
				ClusterConfig: []platformconfig.KeyValueConfig{
					{Key: "autoScale", Value: "true"},
					{Key: "minNodes", Value: "1"},
					{Key: "maxNodes", Value: "3"},
				},
				CoreServices: map[string]platformconfig.ServiceConfig{
					"argocd": {},
				},
			},
		},
		Environments: map[string]platformconfig.EnvironmentConfig{
			"dev": {
				Type:     platformconfig.EnvironmentTypeNonProduction,
				Provider: provider,
				Template: templateName,
				ClusterConfig: []platformconfig.KeyValueConfig{
					{Key: "name", Value: "adhar-dev"},
					{Key: "nodeCount", Value: "1"},
				},
			},
		},
	}
}
