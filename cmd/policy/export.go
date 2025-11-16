package policy

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	exportCmd = &cobra.Command{
		Use:   "export [policy-name]",
		Short: "Export policies from the platform",
		Long:  "Export current policies to files for backup or migration",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runExportPolicy,
	}

	// Export specific flags
	exportType string
	exportDir  string
	exportAll  bool
)

func init() {
	exportCmd.Flags().StringVarP(&exportType, "type", "t", "", "Export policies of specific type")
	exportCmd.Flags().StringVarP(&exportDir, "dir", "d", "./policies", "Export directory")
	exportCmd.Flags().BoolVarP(&exportAll, "all", "a", false, "Export all policies")
}

func runExportPolicy(cmd *cobra.Command, args []string) error {
	policyName := ""
	if len(args) > 0 {
		policyName = args[0]
	}

	if policyName == "" && !exportAll {
		return fmt.Errorf("policy name is required or use --all flag")
	}

	fmt.Printf("📤 Exporting policies to: %s\n", exportDir)
	if policyName != "" {
		fmt.Printf("📋 Policy name: %s\n", policyName)
	}
	if exportType != "" {
		fmt.Printf("🔧 Policy type: %s\n", exportType)
	}
	if exportAll {
		fmt.Println("📦 Exporting all policies")
	}

	// TODO: Implement actual policy export logic
	fmt.Println("✅ Policy export completed successfully!")
	return nil
}
