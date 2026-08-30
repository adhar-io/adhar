package config

import (
	"fmt"

	platformconfig "adhar-io/adhar/platform/config"

	"github.com/spf13/cobra"
)

var validateCmd = &cobra.Command{
	Use:   "validate [config-file]",
	Short: "Validate a configuration file",
	Long: `Validate an Adhar config.yaml against the platform schema (the same
SchemaValidator the platform applies on load) and the provider rules.

With no argument, validates the default/discovered config (or --file).

Examples:
  adhar config validate
  adhar config validate ./config.yaml
  adhar config validate --file ./config.yaml`,
	Args: cobra.MaximumNArgs(1),
	RunE: runValidate,
}

func runValidate(cmd *cobra.Command, args []string) error {
	path := resolveConfigPath()
	if len(args) == 1 {
		path = args[0]
	}
	fmt.Printf("🔍 Validating configuration: %s\n", path)

	// LoadConfig runs the SchemaValidator + provider validation and returns a
	// descriptive error listing every problem when the config is invalid.
	if _, err := platformconfig.LoadConfig(path); err != nil {
		return fmt.Errorf("❌ invalid configuration: %w", err)
	}

	fmt.Println("✅ Configuration is valid")
	return nil
}
