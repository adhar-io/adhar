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
	"fmt"
	"strconv"

	"adhar-io/adhar/cmd/helpers"
	platformconfig "adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var setCmd = &cobra.Command{
	Use:   "set [key] [value]",
	Short: "Set a platform configuration value",
	Long: `Set a value in the platform global settings and write it back to the
on-disk config file. The configuration is validated before being written.

Supported keys:
  globalSettings.adharContext
  globalSettings.defaultHost
  globalSettings.defaultHttpPort
  globalSettings.defaultHttpsPort
  globalSettings.enableHAMode
  globalSettings.email
  globalSettings.productionProvider
  globalSettings.nonProductionProvider

Examples:
  adhar config set globalSettings.defaultHost platform.example.com
  adhar config set defaultHttpsPort 9443
  adhar config set enableHAMode true`,
	Args: cobra.ExactArgs(2),
	RunE: runSet,
}

func runSet(cmd *cobra.Command, args []string) error {
	key := normalizeKey(args[0])
	value := args[1]

	path := resolveConfigPath()

	cfg, err := platformconfig.LoadConfig(path)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if err := setGlobalSetting(&cfg.GlobalSettings, key, value); err != nil {
		return err
	}

	// Validate the mutated config before persisting it.
	if err := platformconfig.ValidateConfig(cfg); err != nil {
		return fmt.Errorf("validation failed, not writing config: %w", err)
	}

	if err := platformconfig.SaveConfig(cfg, path); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	logger.Info("⚙️ Updated configuration at " + path)
	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("Set %s = %s", key, value)))
	return nil
}

func setGlobalSetting(gs *platformconfig.GlobalSettingsConfig, key, value string) error {
	switch key {
	case "adharContext":
		gs.AdharContext = value
	case "defaultHost":
		gs.DefaultHost = value
	case "defaultHttpPort":
		port, err := parsePort(value)
		if err != nil {
			return err
		}
		gs.DefaultHttpPort = port
	case "defaultHttpsPort":
		port, err := parsePort(value)
		if err != nil {
			return err
		}
		gs.DefaultHttpsPort = port
	case "enableHAMode":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid boolean value %q for %s", value, key)
		}
		gs.EnableHAMode = b
	case "email":
		gs.Email = value
	case "productionProvider":
		gs.ProductionProvider = value
	case "nonProductionProvider":
		gs.NonProductionProvider = value
	default:
		return fmt.Errorf("unknown or read-only config key %q", key)
	}
	return nil
}

func parsePort(value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid port %q: must be an integer", value)
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid port %d: must be between 1 and 65535", port)
	}
	return port, nil
}
