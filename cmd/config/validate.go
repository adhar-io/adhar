package config

import (
	"adhar-io/adhar/cmd/helpers"
	"fmt"
	"sort"

	platformconfig "adhar-io/adhar/platform/config"
	pfactory "adhar-io/adhar/platform/providers"

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
	helpers.Printf("▸ Validating configuration: %s\n", path)

	// LoadConfig runs the SchemaValidator + provider validation and returns a
	// descriptive error listing every problem when the config is invalid.
	cfg, err := platformconfig.LoadConfig(path)
	if err != nil {
		return fmt.Errorf("✖ invalid configuration: %w", err)
	}

	helpers.Println("● Configuration is valid")
	printSizing(cfg)
	return nil
}

// printSizing shows what each environment will build — sizes resolved exactly as
// the provider resolves them, so a default is visible before it is billed — and
// any advice about those sizes. Advice never fails validation.
func printSizing(cfg *platformconfig.Config) {
	if err := cfg.ResolveEnvironments(); err != nil {
		return
	}
	names := make([]string, 0, len(cfg.ResolvedEnvironments))
	for name := range cfg.ResolvedEnvironments {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		env := cfg.ResolvedEnvironments[name]
		sizes, err := pfactory.ResolveEffectiveSizes(env)
		if err != nil || sizes.Worker == "" {
			continue
		}
		cp := sizes.ControlPlane
		switch {
		case sizes.Managed:
			cp = "managed by the cloud"
		case cp == "":
			cp = "n/a"
		}
		helpers.Printf("  %s (%s): control plane %s, %d × %s workers\n", name, env.ResolvedProvider, cp, sizes.Workers, sizes.Worker)
		for _, w := range pfactory.SizingWarnings(name, env) {
			helpers.Printf("  ⚠ %s\n", w)
		}
	}
}
