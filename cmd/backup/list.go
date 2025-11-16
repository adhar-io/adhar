package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var (
	listCmd = &cobra.Command{
		Use:   "list",
		Short: "List existing backups",
		Long:  "List all available backups with details including size, creation date, and status",
		RunE:  runListBackups,
	}

	// List-specific flags
	showDetails bool
	sortBy      string
	limit       int
)

func init() {
	listCmd.Flags().BoolVarP(&showDetails, "detailed", "", false, "Show detailed backup information")
	listCmd.Flags().StringVarP(&sortBy, "sort", "s", "date", "Sort by: date, size, or name")
	listCmd.Flags().IntVarP(&limit, "limit", "l", 0, "Limit number of backups to show")
}

func runListBackups(cmd *cobra.Command, args []string) error {
	fmt.Println("📋 Available Backups")
	fmt.Println("")

	// Check if backup directory exists
	if _, err := os.Stat(backupDir); os.IsNotExist(err) {
		fmt.Printf("❌ Backup directory not found: %s\n", backupDir)
		return nil
	}

	// Get backup files
	backups, err := getBackupFiles()
	if err != nil {
		return fmt.Errorf("failed to list backups: %w", err)
	}

	if len(backups) == 0 {
		fmt.Println("📭 No backups found")
		return nil
	}

	// Sort backups
	sortBackups(backups, sortBy)

	// Apply limit
	if limit > 0 && len(backups) > limit {
		backups = backups[:limit]
	}

	// Display backups
	displayBackups(backups, showDetails)

	return nil
}

type BackupInfo struct {
	Name        string
	Path        string
	Size        int64
	Created     time.Time
	Type        string
	Status      string
	Description string
}

func getBackupFiles() ([]BackupInfo, error) {
	var backups []BackupInfo

	err := filepath.Walk(backupDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories and hidden files
		if info.IsDir() || strings.HasPrefix(info.Name(), ".") {
			return nil
		}

		// Check if it's a backup file
		if isBackupFile(info.Name()) {
			backup := BackupInfo{
				Name:    info.Name(),
				Path:    path,
				Size:    info.Size(),
				Created: info.ModTime(),
				Type:    inferBackupType(info.Name()),
				Status:  "valid", // TODO: Implement actual validation
			}
			backups = append(backups, backup)
		}

		return nil
	})

	return backups, err
}

func isBackupFile(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	return ext == ".tar.gz" || ext == ".zip" || ext == ".backup"
}

func inferBackupType(filename string) string {
	if strings.Contains(filename, "full") {
		return "full"
	}
	if strings.Contains(filename, "incremental") {
		return "incremental"
	}
	return "unknown"
}

func sortBackups(backups []BackupInfo, sortBy string) {
	switch sortBy {
	case "date":
		sort.Slice(backups, func(i, j int) bool {
			return backups[i].Created.After(backups[j].Created)
		})
	case "size":
		sort.Slice(backups, func(i, j int) bool {
			return backups[i].Size > backups[j].Size
		})
	case "name":
		sort.Slice(backups, func(i, j int) bool {
			return backups[i].Name < backups[j].Name
		})
	}
}

func displayBackups(backups []BackupInfo, detailed bool) {
	if detailed {
		fmt.Printf("%-40s %-12s %-15s %-20s %-10s\n", "NAME", "TYPE", "SIZE", "CREATED", "STATUS")
		fmt.Println(strings.Repeat("-", 100))
	} else {
		fmt.Printf("%-40s %-12s %-15s %-20s\n", "NAME", "TYPE", "SIZE", "CREATED")
		fmt.Println(strings.Repeat("-", 90))
	}

	for _, backup := range backups {
		sizeStr := formatSize(backup.Size)
		dateStr := backup.Created.Format("2006-01-02 15:04:05")

		if detailed {
			fmt.Printf("%-40s %-12s %-15s %-20s %-10s\n",
				truncateString(backup.Name, 38),
				backup.Type,
				sizeStr,
				dateStr,
				backup.Status)
		} else {
			fmt.Printf("%-40s %-12s %-15s %-20s\n",
				truncateString(backup.Name, 38),
				backup.Type,
				sizeStr,
				dateStr)
		}
	}
}

func formatSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "KMGTPE"[exp])
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
