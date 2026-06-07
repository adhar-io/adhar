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
	"sort"
	"strconv"
	"strings"

	platformconfig "adhar-io/adhar/platform/config"

	"github.com/spf13/cobra"
)

var getCmd = &cobra.Command{
	Use:   "get [key]",
	Short: "Get a platform configuration value",
	Long: `Get a value from the platform global settings.

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
  adhar config get globalSettings.defaultHost
  adhar config get defaultHttpsPort`,
	Args: cobra.ExactArgs(1),
	RunE: runGet,
}

func runGet(cmd *cobra.Command, args []string) error {
	key := normalizeKey(args[0])

	cfg, err := platformconfig.LoadConfig(resolveConfigPath())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	value, err := getGlobalSetting(&cfg.GlobalSettings, key)
	if err != nil {
		return err
	}
	fmt.Println(value)
	return nil
}

// normalizeKey strips an optional "globalSettings." prefix and lowercases the
// leading character for a forgiving lookup.
func normalizeKey(key string) string {
	return strings.TrimPrefix(key, "globalSettings.")
}

// globalSettingKeys lists the settable keys (without prefix) for validation and
// error messages.
var globalSettingKeys = []string{
	"adharContext",
	"defaultHost",
	"defaultHttpPort",
	"defaultHttpsPort",
	"enableHAMode",
	"email",
	"productionProvider",
	"nonProductionProvider",
}

func getGlobalSetting(gs *platformconfig.GlobalSettingsConfig, key string) (string, error) {
	switch key {
	case "adharContext":
		return gs.AdharContext, nil
	case "defaultHost":
		return gs.DefaultHost, nil
	case "defaultHttpPort":
		return strconv.Itoa(gs.DefaultHttpPort), nil
	case "defaultHttpsPort":
		return strconv.Itoa(gs.DefaultHttpsPort), nil
	case "enableHAMode":
		return strconv.FormatBool(gs.EnableHAMode), nil
	case "email":
		return gs.Email, nil
	case "productionProvider":
		return gs.ProductionProvider, nil
	case "nonProductionProvider":
		return gs.NonProductionProvider, nil
	default:
		keys := append([]string{}, globalSettingKeys...)
		sort.Strings(keys)
		return "", fmt.Errorf("unknown config key %q (supported: %s)", key, strings.Join(keys, ", "))
	}
}
