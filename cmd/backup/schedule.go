package backup

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var (
	scheduleCmd = &cobra.Command{
		Use:   "schedule",
		Short: "Manage Velero backup schedules",
		Long:  "Create, list, and manage automated Velero backup schedules (velero.io/v1 Schedule)",
		RunE:  runSchedule,
	}

	// Schedule-specific flags
	scheduleName     string
	scheduleInterval string
	scheduleTime     string
	scheduleCron     string
	scheduleEnabled  bool
)

func init() {
	scheduleCmd.Flags().StringVarP(&scheduleName, "name", "n", "", "Schedule name")
	scheduleCmd.Flags().StringVarP(&scheduleInterval, "interval", "i", "daily", "Backup interval: hourly, daily, weekly, monthly")
	scheduleCmd.Flags().StringVarP(&scheduleTime, "time", "t", "02:00", "Backup time (24-hour HH:MM)")
	scheduleCmd.Flags().BoolVar(&scheduleEnabled, "enabled", true, "Enable/disable schedule")

	scheduleCmd.AddCommand(createScheduleCmd)
	scheduleCmd.AddCommand(listScheduleCmd)
	scheduleCmd.AddCommand(deleteScheduleCmd)
	scheduleCmd.AddCommand(enableScheduleCmd)
	scheduleCmd.AddCommand(disableScheduleCmd)
}

func runSchedule(cmd *cobra.Command, args []string) error {
	fmt.Println("⏰ Adhar Velero Backup Scheduling")
	fmt.Println("")
	fmt.Println("Available commands:")
	fmt.Println("  create    - Create a new Velero Schedule")
	fmt.Println("  list      - List existing Velero Schedules")
	fmt.Println("  delete    - Delete a Schedule")
	fmt.Println("  enable    - Enable (unpause) a Schedule")
	fmt.Println("  disable   - Disable (pause) a Schedule")
	fmt.Println("")
	fmt.Println("Use 'adhar backup schedule <command> --help' for more information")
	return nil
}

var createScheduleCmd = &cobra.Command{
	Use:   "create [schedule-name]",
	Short: "Create a new Velero backup Schedule",
	Long:  "Create an automated Velero Schedule from an interval/time or an explicit --cron expression",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runCreateSchedule,
}

func init() {
	createScheduleCmd.Flags().StringVarP(&scheduleInterval, "interval", "i", "daily", "Backup interval: hourly, daily, weekly, monthly")
	createScheduleCmd.Flags().StringVarP(&scheduleTime, "time", "t", "02:00", "Backup time (24-hour HH:MM)")
	createScheduleCmd.Flags().StringVar(&scheduleCron, "cron", "", "Explicit cron expression (overrides --interval/--time)")
	createScheduleCmd.Flags().BoolVar(&scheduleEnabled, "enabled", true, "Create the schedule unpaused")
}

func runCreateSchedule(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		scheduleName = args[0]
	}
	if scheduleName == "" {
		scheduleName = fmt.Sprintf("adhar-schedule-%s", time.Now().Format("2006-01-02-150405"))
	}

	cron := scheduleCron
	if cron == "" {
		c, err := toCron(scheduleInterval, scheduleTime)
		if err != nil {
			return err
		}
		cron = c
	}

	fmt.Printf("⏰ Creating Velero schedule: %s (cron: %q)\n", scheduleName, cron)

	dyn, err := getDynamicClient()
	if err != nil {
		return unreachable(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "velero.io/v1",
		"kind":       "Schedule",
		"metadata": map[string]interface{}{
			"name":      scheduleName,
			"namespace": veleroNamespace,
			"labels":    map[string]interface{}{"adhar.io/managed-by": "adhar-cli"},
		},
		"spec": map[string]interface{}{
			"schedule": cron,
			"paused":   !scheduleEnabled,
			"template": map[string]interface{}{},
		},
	}}

	if _, err := dyn.Resource(scheduleGVR).Namespace(veleroNamespace).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		if crdMissing(err) {
			return fmt.Errorf("Velero Schedule CRD not installed (velero not present in the cluster)")
		}
		return fmt.Errorf("failed to create schedule %q: %w", scheduleName, err)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Schedule %q created (cron %q, paused=%t)", scheduleName, cron, !scheduleEnabled)))
	return nil
}

// toCron converts a simple interval+time into a cron expression.
func toCron(interval, hhmm string) (string, error) {
	var hour, min int
	if _, err := fmt.Sscanf(hhmm, "%d:%d", &hour, &min); err != nil {
		return "", fmt.Errorf("invalid --time %q, expected HH:MM", hhmm)
	}
	switch strings.ToLower(interval) {
	case "hourly":
		return fmt.Sprintf("%d * * * *", min), nil
	case "daily":
		return fmt.Sprintf("%d %d * * *", min, hour), nil
	case "weekly":
		return fmt.Sprintf("%d %d * * 0", min, hour), nil
	case "monthly":
		return fmt.Sprintf("%d %d 1 * *", min, hour), nil
	default:
		return "", fmt.Errorf("invalid --interval %q (hourly|daily|weekly|monthly)", interval)
	}
}

var listScheduleCmd = &cobra.Command{
	Use:   "list",
	Short: "List Velero backup Schedules",
	Long:  "List all Velero Schedules with their cron and last-backup status",
	RunE:  runListSchedules,
}

func runListSchedules(cmd *cobra.Command, args []string) error {
	fmt.Println(helpers.TitleStyle.Render("📋 Velero Backup Schedules"))

	dyn, err := getDynamicClient()
	if err != nil {
		return unreachable(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	list, err := dyn.Resource(scheduleGVR).Namespace(veleroNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		if crdMissing(err) {
			return fmt.Errorf("Velero Schedule CRD not installed (velero not present in the cluster)")
		}
		return fmt.Errorf("failed to list schedules: %w", err)
	}

	if len(list.Items) == 0 {
		fmt.Println(helpers.CreateMuted("   No schedules configured"))
		fmt.Println(helpers.CreateMuted("   Use 'adhar backup schedule create' to create one"))
		return nil
	}

	items := list.Items
	sort.Slice(items, func(i, j int) bool { return items[i].GetName() < items[j].GetName() })

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%-34s %-16s %-10s %-12s %s\n", "NAME", "CRON", "PAUSED", "PHASE", "LAST BACKUP"))
	b.WriteString(strings.Repeat("─", 92) + "\n")
	for _, s := range items {
		cron := nestedString(s.Object, "spec", "schedule")
		paused, _, _ := unstructured.NestedBool(s.Object, "spec", "paused")
		phase := nestedString(s.Object, "status", "phase")
		last := nestedString(s.Object, "status", "lastBackup")
		if last == "" {
			last = "-"
		}
		b.WriteString(fmt.Sprintf("%-34s %-16s %-10t %-12s %s\n",
			truncate(s.GetName(), 32), truncate(cron, 14), paused, phase, last))
	}
	fmt.Print(b.String())
	return nil
}

var deleteScheduleCmd = &cobra.Command{
	Use:   "delete [schedule-name]",
	Short: "Delete a Velero Schedule",
	Long:  "Delete a Velero Schedule and stop automated backups",
	Args:  cobra.ExactArgs(1),
	RunE:  runDeleteSchedule,
}

func runDeleteSchedule(cmd *cobra.Command, args []string) error {
	name := args[0]
	fmt.Printf("🗑️  Deleting schedule: %s\n", name)

	dyn, err := getDynamicClient()
	if err != nil {
		return unreachable(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := dyn.Resource(scheduleGVR).Namespace(veleroNamespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		if crdMissing(err) {
			return fmt.Errorf("Velero Schedule CRD not installed (velero not present in the cluster)")
		}
		return fmt.Errorf("failed to delete schedule %q: %w", name, err)
	}
	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Schedule %q deleted", name)))
	return nil
}

var enableScheduleCmd = &cobra.Command{
	Use:   "enable [schedule-name]",
	Short: "Enable (unpause) a Velero Schedule",
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return setSchedulePaused(args[0], false) },
}

var disableScheduleCmd = &cobra.Command{
	Use:   "disable [schedule-name]",
	Short: "Disable (pause) a Velero Schedule",
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return setSchedulePaused(args[0], true) },
}

// setSchedulePaused toggles spec.paused on a Velero Schedule.
func setSchedulePaused(name string, paused bool) error {
	verb := "Enabling"
	if paused {
		verb = "Disabling"
	}
	fmt.Printf("⏯️  %s schedule: %s\n", verb, name)

	dyn, err := getDynamicClient()
	if err != nil {
		return unreachable(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	obj, err := dyn.Resource(scheduleGVR).Namespace(veleroNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if crdMissing(err) {
			return fmt.Errorf("Velero Schedule CRD not installed (velero not present in the cluster)")
		}
		return fmt.Errorf("failed to get schedule %q: %w", name, err)
	}
	if err := unstructured.SetNestedField(obj.Object, paused, "spec", "paused"); err != nil {
		return fmt.Errorf("failed to set paused field: %w", err)
	}
	if _, err := dyn.Resource(scheduleGVR).Namespace(veleroNamespace).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to update schedule %q: %w", name, err)
	}
	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Schedule %q paused=%t", name, paused)))
	return nil
}
