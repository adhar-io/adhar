package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
)

// clusterCmd represents the cluster command
var clusterCmd = &cobra.Command{
	Use:   "cluster",
	Short: "Manage Adhar management cluster",
	Long: `Manage the Adhar management cluster lifecycle including provisioning, 
health checks, backup, and day-2 operations.

The management cluster is the foundation of the Adhar platform and hosts
the control plane services including ArgoCD, Crossplane, and other core components.`,
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

// clusterBootstrapCmd represents the cluster bootstrap command
var clusterBootstrapCmd = &cobra.Command{
	Use:   "bootstrap",
	Short: "Bootstrap a new management cluster",
	Long: `Bootstrap a new production-grade management cluster with industry best practices.

This command creates a highly available Kubernetes cluster with:
- Cilium CNI for advanced networking
- Production-grade security configurations
- Monitoring and observability stack
- Automated backup and disaster recovery
- Day-2 operations tooling

The bootstrap process is idempotent and can be safely re-run.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runClusterBootstrap(cmd, args)
	},
}

// clusterStatusCmd represents the cluster status command
var clusterStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check management cluster status",
	Long: `Check the health and status of the management cluster including:
- Node health and resource utilization
- System pod status
- Networking (Cilium) health
- Storage and backup status
- Security policy compliance`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runClusterStatus(cmd, args)
	},
}

// clusterBackupCmd represents the cluster backup command
var clusterBackupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Backup management cluster state",
	Long: `Create a comprehensive backup of the management cluster including:
- etcd snapshot
- Kubernetes certificates and secrets
- Persistent volume snapshots
- Configuration files
- Custom resources and applications`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runClusterBackup(cmd, args)
	},
}

// clusterCleanupCmd represents the cluster cleanup command
var clusterCleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Clean up management cluster resources",
	Long: `Perform cleanup operations on the management cluster:
- Remove failed or completed pods
- Clean up unused container images
- Purge old backup files
- Optimize resource usage`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runClusterCleanup(cmd, args)
	},
}

func init() {
	rootCmd.AddCommand(clusterCmd)
	clusterCmd.AddCommand(clusterBootstrapCmd)
	clusterCmd.AddCommand(clusterStatusCmd)
	clusterCmd.AddCommand(clusterBackupCmd)
	clusterCmd.AddCommand(clusterCleanupCmd)

	// Bootstrap command flags
	clusterBootstrapCmd.Flags().StringP("config", "c", "", "Path to cluster configuration file")
	clusterBootstrapCmd.Flags().Bool("force", false, "Force bootstrap even if cluster already exists")
	clusterBootstrapCmd.Flags().Bool("dry-run", false, "Show what would be done without making changes")

	// Status command flags
	clusterStatusCmd.Flags().Bool("verbose", false, "Show detailed status information")
	clusterStatusCmd.Flags().Bool("json", false, "Output status in JSON format")

	// Backup command flags
	clusterBackupCmd.Flags().StringP("output", "o", "/var/lib/adhar/backups", "Backup output directory")
	clusterBackupCmd.Flags().Bool("etcd-only", false, "Backup only etcd data")

	// Cleanup command flags
	clusterCleanupCmd.Flags().Bool("force", false, "Force cleanup without confirmation")
	clusterCleanupCmd.Flags().Bool("dry-run", false, "Show what would be cleaned up without making changes")
}

// runClusterBootstrap handles the cluster bootstrap command
func runClusterBootstrap(cmd *cobra.Command, args []string) error {
	fmt.Println("🚀 Starting management cluster bootstrap...")

	// TODO: Port cluster management to new provider system
	return fmt.Errorf("cluster management commands are temporarily disabled during provider system migration")
}

// runClusterStatus handles the cluster status command
func runClusterStatus(cmd *cobra.Command, args []string) error {
	verbose, _ := cmd.Flags().GetBool("verbose")
	jsonOutput, _ := cmd.Flags().GetBool("json")

	// Get scripts directory
	pwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %w", err)
	}

	scriptsDir := filepath.Join(pwd, "scripts", "management-cluster")
	day2Script := filepath.Join(scriptsDir, "day2-ops.sh")

	// Check if day2 script exists
	if _, err := os.Stat(day2Script); os.IsNotExist(err) {
		return fmt.Errorf("day-2 operations script not found at %s", day2Script)
	}

	fmt.Println("📊 Checking management cluster status...")

	// Execute health check
	var cmdArgs []string
	if jsonOutput {
		cmdArgs = []string{day2Script, "health", "--json"}
	} else if verbose {
		cmdArgs = []string{day2Script, "health", "--verbose"}
	} else {
		cmdArgs = []string{day2Script, "health"}
	}

	return executeScript(cmdArgs, scriptsDir)
}

// runClusterBackup handles the cluster backup command
func runClusterBackup(cmd *cobra.Command, args []string) error {
	outputDir, _ := cmd.Flags().GetString("output")
	etcdOnly, _ := cmd.Flags().GetBool("etcd-only")

	// Get scripts directory
	pwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %w", err)
	}

	scriptsDir := filepath.Join(pwd, "scripts", "management-cluster")
	day2Script := filepath.Join(scriptsDir, "day2-ops.sh")

	// Check if day2 script exists
	if _, err := os.Stat(day2Script); os.IsNotExist(err) {
		return fmt.Errorf("day-2 operations script not found at %s", day2Script)
	}

	fmt.Println("💾 Creating management cluster backup...")

	// Prepare command arguments
	var cmdArgs []string
	if etcdOnly {
		cmdArgs = []string{day2Script, "backup", "--etcd-only", "--output", outputDir}
	} else {
		cmdArgs = []string{day2Script, "backup", "--output", outputDir}
	}

	return executeScript(cmdArgs, scriptsDir)
}

// runClusterCleanup handles the cluster cleanup command
func runClusterCleanup(cmd *cobra.Command, args []string) error {
	force, _ := cmd.Flags().GetBool("force")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	// Get scripts directory
	pwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %w", err)
	}

	scriptsDir := filepath.Join(pwd, "scripts", "management-cluster")
	day2Script := filepath.Join(scriptsDir, "day2-ops.sh")

	// Check if day2 script exists
	if _, err := os.Stat(day2Script); os.IsNotExist(err) {
		return fmt.Errorf("day-2 operations script not found at %s", day2Script)
	}

	fmt.Println("🧹 Cleaning up management cluster resources...")

	// Prepare command arguments
	var cmdArgs []string
	if dryRun {
		cmdArgs = []string{day2Script, "cleanup", "--dry-run"}
	} else if force {
		cmdArgs = []string{day2Script, "cleanup", "--force"}
	} else {
		cmdArgs = []string{day2Script, "cleanup"}
	}

	return executeScript(cmdArgs, scriptsDir)
}

// executeScript executes a script with the given arguments
func executeScript(cmdArgs []string, workingDir string) error {
	cmd := exec.Command("bash", cmdArgs...)
	cmd.Dir = workingDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("script execution failed: %w", err)
	}

	return nil
}
