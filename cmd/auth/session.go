package auth

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
)

// kcSession is a subset of the Keycloak user-session representation.
type kcSession struct {
	ID         string            `json:"id"`
	Username   string            `json:"username"`
	UserID     string            `json:"userId"`
	IPAddress  string            `json:"ipAddress"`
	Start      int64             `json:"start"`
	LastAccess int64             `json:"lastAccess"`
	Clients    map[string]string `json:"clients"`
}

// kcClientSessionStat is a subset of the client-session-stats response.
type kcClientSessionStat struct {
	ID       string `json:"id"`
	ClientID string `json:"clientId"`
	Active   string `json:"active"`
	Offline  string `json:"offline"`
}

// epochMillis renders a Keycloak millisecond timestamp as a local time string.
func epochMillis(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return time.UnixMilli(ms).Format("2006-01-02 15:04:05")
}

var (
	sessionCmd = &cobra.Command{
		Use:   "session",
		Short: "Manage user sessions",
		Long: `Manage platform user sessions including:
- Active session listing
- Session termination
- Session monitoring and analytics
- Security policy enforcement`,
		RunE: runSession,
	}

	// Session specific flags
	sessionID   string
	sessionUser string
)

func init() {
	sessionCmd.Flags().StringVarP(&sessionID, "id", "i", "", "Session ID")
	sessionCmd.Flags().StringVarP(&sessionUser, "user", "u", "", "Session user")

	// Add session subcommands
	sessionCmd.AddCommand(listSessionsCmd)
	sessionCmd.AddCommand(getSessionCmd)
	sessionCmd.AddCommand(terminateSessionCmd)
	sessionCmd.AddCommand(terminateAllSessionsCmd)
	sessionCmd.AddCommand(sessionStatsCmd)
}

func runSession(cmd *cobra.Command, args []string) error {
	fmt.Println("🖥️  Adhar Platform Session Management")
	fmt.Println("")
	fmt.Println("Available commands:")
	fmt.Println("  list           - List active sessions")
	fmt.Println("  get            - Get session details")
	fmt.Println("  terminate      - Terminate a session")
	fmt.Println("  terminate-all  - Terminate all sessions for a user")
	fmt.Println("  stats          - Session statistics")
	fmt.Println("")
	fmt.Println("Use 'adhar auth session <command> --help' for more information")
	return nil
}

var (
	listSessionsCmd = &cobra.Command{
		Use:   "list",
		Short: "List active sessions",
		Long:  "List all active user sessions with filtering options",
		RunE:  runListSessions,
	}

	// List sessions specific flags
	showExpiredSessions bool
	showSessionDetails  bool
)

func init() {
	listSessionsCmd.Flags().BoolVarP(&showExpiredSessions, "expired", "e", false, "Show expired sessions")
	listSessionsCmd.Flags().BoolVarP(&showSessionDetails, "detailed", "d", false, "Show detailed session information")
}

func runListSessions(cmd *cobra.Command, args []string) error {
	kc := settings()
	ctx := context.Background()

	// Per-user sessions when --user is given; otherwise a realm-wide summary of
	// active sessions per client (Keycloak has no single "all sessions" list).
	if sessionUser != "" {
		id, err := kc.userIDByUsername(ctx, sessionUser)
		if err != nil {
			return err
		}
		var sessions []kcSession
		if err := kc.adminGet(ctx, "/users/"+id+"/sessions", &sessions); err != nil {
			return err
		}
		if output == "json" {
			return helpers.PrintJSON(sessions)
		}
		if output == "yaml" {
			return helpers.PrintYAML(sessions)
		}
		fmt.Printf("📋 Active sessions for %s\n", sessionUser)
		if len(sessions) == 0 {
			fmt.Println(helpers.CreateMuted("No active sessions"))
			return nil
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("%-36s %-15s %-19s %s\n", "🆔 SESSION", "🌐 IP", "🕒 STARTED", "⏱️  LAST ACCESS"))
		b.WriteString(strings.Repeat("─", 100) + "\n")
		for _, s := range sessions {
			b.WriteString(fmt.Sprintf("%-36s %-15s %-19s %s\n", s.ID, s.IPAddress, epochMillis(s.Start), epochMillis(s.LastAccess)))
		}
		fmt.Println(helpers.BorderStyle.Render(b.String()))
		fmt.Println(helpers.CreateMuted(fmt.Sprintf("%d active session(s)", len(sessions))))
		return nil
	}

	stats, err := kc.clientSessionStats(ctx)
	if err != nil {
		return err
	}
	if output == "json" {
		return helpers.PrintJSON(stats)
	}
	if output == "yaml" {
		return helpers.PrintYAML(stats)
	}
	fmt.Println("📋 Active sessions by client (realm " + kc.Realm + ")")
	if len(stats) == 0 {
		fmt.Println(helpers.CreateMuted("No active sessions in the realm"))
		return nil
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%-30s %-10s %s\n", "🔌 CLIENT", "✅ ACTIVE", "💤 OFFLINE"))
	b.WriteString(strings.Repeat("─", 60) + "\n")
	total := 0
	for _, s := range stats {
		b.WriteString(fmt.Sprintf("%-30s %-10s %s\n", truncA(s.ClientID, 30), s.Active, s.Offline))
		if n, err := strconv.Atoi(s.Active); err == nil {
			total += n
		}
	}
	fmt.Println(helpers.BorderStyle.Render(b.String()))
	fmt.Println(helpers.CreateMuted(fmt.Sprintf("%d active session(s) across %d client(s) — filter with --user <name>", total, len(stats))))
	return nil
}

var (
	getSessionCmd = &cobra.Command{
		Use:   "get [session-id]",
		Short: "Get session details",
		Long:  "Get detailed information about a specific session",
		Args:  cobra.ExactArgs(1),
		RunE:  runGetSession,
	}
)

func runGetSession(cmd *cobra.Command, args []string) error {
	sessionID := args[0]
	kc := settings()
	ctx := context.Background()

	// Keycloak exposes session details only under a user, so --user is required
	// to look one up by id.
	if sessionUser == "" {
		return fmt.Errorf("session details require --user <username> (Keycloak scopes sessions to a user)")
	}
	id, err := kc.userIDByUsername(ctx, sessionUser)
	if err != nil {
		return err
	}
	var sessions []kcSession
	if err := kc.adminGet(ctx, "/users/"+id+"/sessions", &sessions); err != nil {
		return err
	}
	for _, s := range sessions {
		if s.ID == sessionID {
			if output == "json" {
				return helpers.PrintJSON(s)
			}
			if output == "yaml" {
				return helpers.PrintYAML(s)
			}
			fmt.Printf("🆔 Session:     %s\n", s.ID)
			fmt.Printf("👤 User:        %s\n", s.Username)
			fmt.Printf("🌐 IP:          %s\n", s.IPAddress)
			fmt.Printf("🕒 Started:     %s\n", epochMillis(s.Start))
			fmt.Printf("⏱️  Last access: %s\n", epochMillis(s.LastAccess))
			if len(s.Clients) > 0 {
				clients := make([]string, 0, len(s.Clients))
				for _, c := range s.Clients {
					clients = append(clients, c)
				}
				fmt.Printf("🔌 Clients:     %s\n", strings.Join(clients, ", "))
			}
			return nil
		}
	}
	return fmt.Errorf("session %q not found for user %s", sessionID, sessionUser)
}

var (
	terminateSessionCmd = &cobra.Command{
		Use:   "terminate [session-id]",
		Short: "Terminate a session",
		Long:  "Terminate a specific user session",
		Args:  cobra.ExactArgs(1),
		RunE:  runTerminateSession,
	}

	// Terminate session specific flags
	terminateReason string
)

func init() {
	terminateSessionCmd.Flags().StringVarP(&terminateReason, "reason", "r", "", "Reason for termination")
}

func runTerminateSession(cmd *cobra.Command, args []string) error {
	sessionID := args[0]
	kc := settings()
	ctx := context.Background()

	if _, err := kc.adminWrite(ctx, http.MethodDelete, "/sessions/"+sessionID, nil); err != nil {
		return fmt.Errorf("terminate session: %w", err)
	}
	if terminateReason != "" {
		fmt.Printf("📝 Reason: %s\n", terminateReason)
	}
	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Terminated session %s", sessionID)))
	return nil
}

var (
	terminateAllSessionsCmd = &cobra.Command{
		Use:   "terminate-all [username]",
		Short: "Terminate all sessions for a user",
		Long:  "Terminate all active sessions for a specific user",
		Args:  cobra.ExactArgs(1),
		RunE:  runTerminateAllSessions,
	}

	// Terminate all sessions specific flags
	terminateAllReason string
)

func init() {
	terminateAllSessionsCmd.Flags().StringVarP(&terminateAllReason, "reason", "r", "", "Reason for termination")
}

func runTerminateAllSessions(cmd *cobra.Command, args []string) error {
	username := args[0]
	kc := settings()
	ctx := context.Background()

	id, err := kc.userIDByUsername(ctx, username)
	if err != nil {
		return err
	}
	if _, err := kc.adminWrite(ctx, http.MethodPost, "/users/"+id+"/logout", nil); err != nil {
		return fmt.Errorf("terminate sessions: %w", err)
	}
	if terminateAllReason != "" {
		fmt.Printf("📝 Reason: %s\n", terminateAllReason)
	}
	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Terminated all sessions for user %s", username)))
	return nil
}

var (
	sessionStatsCmd = &cobra.Command{
		Use:   "stats",
		Short: "Session statistics",
		Long:  "Display session statistics and analytics",
		RunE:  runSessionStats,
	}
)

func runSessionStats(cmd *cobra.Command, args []string) error {
	kc := settings()
	ctx := context.Background()

	stats, err := kc.clientSessionStats(ctx)
	if err != nil {
		return err
	}
	if output == "json" {
		return helpers.PrintJSON(stats)
	}
	if output == "yaml" {
		return helpers.PrintYAML(stats)
	}

	activeTotal, offlineTotal := 0, 0
	for _, s := range stats {
		if n, err := strconv.Atoi(s.Active); err == nil {
			activeTotal += n
		}
		if n, err := strconv.Atoi(s.Offline); err == nil {
			offlineTotal += n
		}
	}
	fmt.Println("📊 Session Statistics (realm " + kc.Realm + ")")
	fmt.Printf("📈 Active sessions:  %d\n", activeTotal)
	fmt.Printf("💤 Offline sessions: %d\n", offlineTotal)
	fmt.Printf("🔌 Clients with sessions: %d\n", len(stats))
	return nil
}
