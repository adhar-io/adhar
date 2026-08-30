package config

import (
	"encoding/json"
	"fmt"
	"os"

	platformconfig "adhar-io/adhar/platform/config"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"
)

var (
	exportFormat  string
	exportOutput  string
	exportResolve bool
)

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export the loaded platform configuration",
	Long: `Export the resolved Adhar platform configuration (the same config the
platform loads via LoadConfig) to stdout or a file, as YAML or JSON.

Examples:
  adhar config export
  adhar config export --format=json
  adhar config export --output=./backup.yaml
  adhar config export --resolve   # include fully-merged environments`,
	RunE: runExport,
}

func init() {
	exportCmd.Flags().StringVar(&exportFormat, "format", "yaml", "Output format (yaml, json)")
	exportCmd.Flags().StringVarP(&exportOutput, "output", "o", "", "Write to file instead of stdout")
	exportCmd.Flags().BoolVar(&exportResolve, "resolve", false, "Resolve environment templates before exporting")
}

func runExport(cmd *cobra.Command, args []string) error {
	path := resolveConfigPath()
	cfg, err := platformconfig.LoadConfig(path)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if exportResolve {
		if err := cfg.ResolveEnvironments(); err != nil {
			return fmt.Errorf("resolve environments: %w", err)
		}
	}

	var data []byte
	switch exportFormat {
	case "json":
		data, err = json.MarshalIndent(cfg, "", "  ")
	case "yaml", "":
		data, err = yaml.Marshal(cfg)
	default:
		return fmt.Errorf("unsupported --format %q (yaml, json)", exportFormat)
	}
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if exportOutput == "" {
		fmt.Print(string(data))
		if exportFormat == "json" {
			fmt.Println()
		}
		return nil
	}

	if err := os.WriteFile(exportOutput, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", exportOutput, err)
	}
	logger.Info("📤 Exported configuration from " + path + " to " + exportOutput)
	return nil
}
