package config

import (
	"fmt"
	"sort"
	"strings"

	platformconfig "adhar-io/adhar/platform/config"

	"github.com/spf13/cobra"
)

var listTemplatesCmd = &cobra.Command{
	Use:   "list-templates",
	Short: "List available environment templates",
	Long: `List the environment templates defined in the loaded config.yaml
(environmentTemplates). These are the reusable bases that environments inherit
from via their 'template:' reference.

Examples:
  adhar config list-templates
  adhar config list-templates --file ./config.yaml`,
	RunE: runListTemplates,
}

func runListTemplates(cmd *cobra.Command, args []string) error {
	cfg, err := platformconfig.LoadConfig(resolveConfigPath())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if len(cfg.EnvironmentTemplates) == 0 {
		fmt.Println("📭 No environment templates defined in the configuration")
		return nil
	}

	// Which environments reference each template.
	usedBy := map[string][]string{}
	for envName, env := range cfg.Environments {
		if env.Template != "" {
			usedBy[env.Template] = append(usedBy[env.Template], envName)
		}
	}

	names := make([]string, 0, len(cfg.EnvironmentTemplates))
	for name := range cfg.EnvironmentTemplates {
		names = append(names, name)
	}
	sort.Strings(names)

	fmt.Printf("📋 Environment templates (%d):\n\n", len(names))
	for _, name := range names {
		tpl := cfg.EnvironmentTemplates[name]
		fmt.Printf("• %s\n", name)
		fmt.Printf("    cluster settings: %d  •  core services: %d  •  addons: %d\n",
			len(tpl.ClusterConfig), len(tpl.CoreServices), len(tpl.Addons))
		if envs := usedBy[name]; len(envs) > 0 {
			sort.Strings(envs)
			fmt.Printf("    used by: %s\n", strings.Join(envs, ", "))
		}
	}
	return nil
}
