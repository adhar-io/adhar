/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the file at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package config

import (
	"encoding/json"
	"fmt"

	platformconfig "adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"
)

var viewOutput string

var viewCmd = &cobra.Command{
	Use:   "view",
	Short: "View the resolved platform configuration",
	Long: `View the resolved Adhar platform configuration loaded from the on-disk
config.yaml (and environment overrides).

Examples:
  adhar config view
  adhar config view --output=json
  adhar config view --file=./config.yaml`,
	RunE: runView,
}

func init() {
	viewCmd.Flags().StringVarP(&viewOutput, "output", "o", "yaml", "Output format (yaml, json)")
}

func runView(cmd *cobra.Command, args []string) error {
	path := resolveConfigPath()
	logger.Info("⚙️ Loading configuration from " + path)

	cfg, err := platformconfig.LoadConfig(path)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Resolve environments so the view reflects fully-merged settings.
	if err := cfg.ResolveEnvironments(); err != nil {
		return fmt.Errorf("resolve environments: %w", err)
	}

	switch viewOutput {
	case "json":
		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal json: %w", err)
		}
		fmt.Println(string(out))
	default:
		out, err := yaml.Marshal(cfg)
		if err != nil {
			return fmt.Errorf("marshal yaml: %w", err)
		}
		fmt.Print(string(out))
	}
	return nil
}

// resolveConfigPath returns the config file path from the --file flag or the
// platform's default discovery logic.
func resolveConfigPath() string {
	if configFile != "" {
		return configFile
	}
	return platformconfig.GetConfigPath()
}
