package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
)

var (
	userCmd = &cobra.Command{
		Use:   "user",
		Short: "Manage users and accounts",
		Long: `Manage platform users including:
- User creation and deletion
- User profile management
- Password management
- Account status and permissions
- User search and filtering`,
		RunE: runUser,
	}

	// User specific flags
	userID     string
	userEmail  string
	userStatus string
	userRole   string
)

func init() {
	userCmd.Flags().StringVarP(&userID, "id", "i", "", "User ID")
	userCmd.Flags().StringVarP(&userEmail, "email", "e", "", "User email")
	userCmd.Flags().StringVarP(&userStatus, "status", "s", "", "User status (active, inactive, suspended)")
	userCmd.Flags().StringVarP(&userRole, "role", "r", "", "User role")

	// Add user subcommands
	userCmd.AddCommand(createUserCmd)
	userCmd.AddCommand(listUsersCmd)
	userCmd.AddCommand(getUserCmd)
	userCmd.AddCommand(updateUserCmd)
	userCmd.AddCommand(deleteUserCmd)
	userCmd.AddCommand(resetPasswordCmd)
}

func runUser(cmd *cobra.Command, args []string) error {
	fmt.Println("👥 Adhar Platform User Management")
	fmt.Println("")
	fmt.Println("Available commands:")
	fmt.Println("  create    - Create a new user")
	fmt.Println("  list      - List all users")
	fmt.Println("  get       - Get user details")
	fmt.Println("  update    - Update user information")
	fmt.Println("  delete    - Delete a user")
	fmt.Println("  reset-pwd - Reset user password")
	fmt.Println("")
	fmt.Println("Use 'adhar auth user <command> --help' for more information")
	return nil
}

var (
	createUserCmd = &cobra.Command{
		Use:   "create [username]",
		Short: "Create a new user",
		Long:  "Create a new user account with specified details",
		Args:  cobra.ExactArgs(1),
		RunE:  runCreateUser,
	}

	// Create user specific flags
	newUserEmail    string
	newUserPassword string
	newUserRole     string
	newUserGroup    string
)

func init() {
	createUserCmd.Flags().StringVarP(&newUserEmail, "email", "e", "", "User email address")
	createUserCmd.Flags().StringVarP(&newUserPassword, "password", "", "", "User password")
	createUserCmd.Flags().StringVarP(&newUserRole, "role", "r", "user", "User role (admin, user, developer)")
	createUserCmd.Flags().StringVarP(&newUserGroup, "group", "g", "", "User group")
}

func runCreateUser(cmd *cobra.Command, args []string) error {
	username := args[0]
	kc := settings()
	ctx := context.Background()

	fmt.Printf("👤 Creating user %q in realm %s\n", username, kc.Realm)

	body := map[string]interface{}{
		"username": username,
		"enabled":  true,
	}
	if newUserEmail != "" {
		body["email"] = newUserEmail
		body["emailVerified"] = false
	}
	if newUserPassword != "" {
		body["credentials"] = []map[string]interface{}{{
			"type":      "password",
			"value":     newUserPassword,
			"temporary": false,
		}}
	}

	location, err := kc.adminWrite(ctx, http.MethodPost, "/users", body)
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	userID := idFromLocation(location)

	// Optionally add the user to a group by name.
	if newUserGroup != "" {
		if userID == "" {
			if userID, err = kc.userIDByUsername(ctx, username); err != nil {
				return err
			}
		}
		groupID, gerr := kc.groupIDByName(ctx, newUserGroup)
		if gerr != nil {
			return fmt.Errorf("user created, but joining group failed: %w", gerr)
		}
		if _, gerr := kc.adminWrite(ctx, http.MethodPut, fmt.Sprintf("/users/%s/groups/%s", userID, groupID), nil); gerr != nil {
			return fmt.Errorf("user created, but joining group %q failed: %w", newUserGroup, gerr)
		}
		fmt.Printf("👥 Added to group: %s\n", newUserGroup)
	}

	// Optionally assign a realm role by name.
	if newUserRole != "" && newUserRole != "user" {
		if userID == "" {
			if userID, err = kc.userIDByUsername(ctx, username); err != nil {
				return err
			}
		}
		if rerr := kc.assignRealmRole(ctx, userID, newUserRole); rerr != nil {
			return fmt.Errorf("user created, but assigning role %q failed: %w", newUserRole, rerr)
		}
		fmt.Printf("🔑 Assigned realm role: %s\n", newUserRole)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Created user %s", username)))
	if newUserPassword == "" {
		fmt.Println(helpers.CreateMuted("   No password set — use `adhar auth user reset-pwd " + username + "` to set one."))
	}
	return nil
}

var (
	listUsersCmd = &cobra.Command{
		Use:   "list",
		Short: "List all users",
		Long:  "List all platform users with filtering options",
		RunE:  runListUsers,
	}

	// List users specific flags
	showDetails bool
	limit       int
)

func init() {
	listUsersCmd.Flags().BoolVarP(&showDetails, "detailed", "d", false, "Show detailed user information")
	listUsersCmd.Flags().IntVarP(&limit, "limit", "l", 0, "Limit number of users to show")
}

// kcUser is a subset of the Keycloak user representation.
type kcUser struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Email     string `json:"email"`
	Enabled   bool   `json:"enabled"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
}

func runListUsers(cmd *cobra.Command, args []string) error {
	fmt.Println("📋 Platform Users (Keycloak)")
	kc := settings()

	path := "/users"
	if limit > 0 {
		path = fmt.Sprintf("/users?max=%d", limit)
	}

	var users []kcUser
	if err := kc.adminGet(context.Background(), path, &users); err != nil {
		return err
	}

	if output == "json" {
		return helpers.PrintJSON(users)
	}
	if output == "yaml" {
		return helpers.PrintYAML(users)
	}

	if len(users) == 0 {
		fmt.Println(helpers.CreateMuted("No users found in realm " + kc.Realm))
		return nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%-24s %-30s %-9s %s\n", "👤 USERNAME", "📧 EMAIL", "✅ ENABLED", "🆔 ID"))
	b.WriteString(strings.Repeat("─", 100) + "\n")
	for _, u := range users {
		enabled := "yes"
		if !u.Enabled {
			enabled = "no"
		}
		b.WriteString(fmt.Sprintf("%-24s %-30s %-9s %s\n", truncA(u.Username, 24), truncA(u.Email, 30), enabled, u.ID))
	}
	fmt.Println(helpers.BorderStyle.Render(b.String()))
	fmt.Println(helpers.CreateMuted(fmt.Sprintf("%d user(s) in realm %s", len(users), kc.Realm)))
	return nil
}

// truncA shortens s to n runes, appending an ellipsis when truncated.
func truncA(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

var (
	getUserCmd = &cobra.Command{
		Use:   "get [username]",
		Short: "Get user details",
		Long:  "Get detailed information about a specific user",
		Args:  cobra.ExactArgs(1),
		RunE:  runGetUser,
	}
)

func runGetUser(cmd *cobra.Command, args []string) error {
	username := args[0]
	kc := settings()
	ctx := context.Background()

	id, err := kc.userIDByUsername(ctx, username)
	if err != nil {
		return err
	}

	var u kcUser
	if err := kc.adminGetOne(ctx, "/users/"+id, &u); err != nil {
		return err
	}

	if output == "json" {
		return helpers.PrintJSON(u)
	}
	if output == "yaml" {
		return helpers.PrintYAML(u)
	}

	fmt.Printf("👤 Username:  %s\n", u.Username)
	if u.Email != "" {
		fmt.Printf("📧 Email:     %s\n", u.Email)
	}
	if name := strings.TrimSpace(u.FirstName + " " + u.LastName); name != "" {
		fmt.Printf("🪪 Name:      %s\n", name)
	}
	fmt.Printf("✅ Enabled:   %t\n", u.Enabled)
	fmt.Printf("🆔 ID:        %s\n", u.ID)
	return nil
}

var (
	updateUserCmd = &cobra.Command{
		Use:   "update [username]",
		Short: "Update user information",
		Long:  "Update user profile, role, or status",
		Args:  cobra.ExactArgs(1),
		RunE:  runUpdateUser,
	}

	// Update user specific flags
	updateEmail  string
	updateRole   string
	updateStatus string
	updateGroup  string
)

func init() {
	updateUserCmd.Flags().StringVarP(&updateEmail, "email", "e", "", "New email address")
	updateUserCmd.Flags().StringVarP(&updateRole, "role", "r", "", "New role")
	updateUserCmd.Flags().StringVarP(&updateStatus, "status", "s", "", "New status")
	updateUserCmd.Flags().StringVarP(&updateGroup, "group", "g", "", "New group")
}

func runUpdateUser(cmd *cobra.Command, args []string) error {
	username := args[0]
	kc := settings()
	ctx := context.Background()

	id, err := kc.userIDByUsername(ctx, username)
	if err != nil {
		return err
	}

	// Fetch the current representation and patch the requested fields so we
	// don't clobber attributes Keycloak expects to round-trip.
	var current map[string]interface{}
	if err := kc.adminGetOne(ctx, "/users/"+id, &current); err != nil {
		return err
	}
	fmt.Printf("✏️  Updating user %q\n", username)
	if updateEmail != "" {
		current["email"] = updateEmail
	}
	switch updateStatus {
	case "active":
		current["enabled"] = true
	case "inactive", "suspended", "disabled":
		current["enabled"] = false
	case "":
		// no change
	default:
		return fmt.Errorf("invalid --status %q (active, inactive, suspended)", updateStatus)
	}

	if _, err := kc.adminWrite(ctx, http.MethodPut, "/users/"+id, current); err != nil {
		return fmt.Errorf("update user: %w", err)
	}

	if updateRole != "" {
		if err := kc.assignRealmRole(ctx, id, updateRole); err != nil {
			return fmt.Errorf("user updated, but assigning role %q failed: %w", updateRole, err)
		}
		fmt.Printf("🔑 Assigned realm role: %s\n", updateRole)
	}
	if updateGroup != "" {
		groupID, gerr := kc.groupIDByName(ctx, updateGroup)
		if gerr != nil {
			return fmt.Errorf("user updated, but joining group failed: %w", gerr)
		}
		if _, gerr := kc.adminWrite(ctx, http.MethodPut, fmt.Sprintf("/users/%s/groups/%s", id, groupID), nil); gerr != nil {
			return fmt.Errorf("user updated, but joining group %q failed: %w", updateGroup, gerr)
		}
		fmt.Printf("👥 Added to group: %s\n", updateGroup)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Updated user %s", username)))
	return nil
}

var (
	deleteUserCmd = &cobra.Command{
		Use:   "delete [username]",
		Short: "Delete a user",
		Long:  "Delete a user account from the platform",
		Args:  cobra.ExactArgs(1),
		RunE:  runDeleteUser,
	}

	// Delete user specific flags
	forceDelete bool
)

func init() {
	deleteUserCmd.Flags().BoolVarP(&forceDelete, "force", "f", false, "Force deletion without confirmation")
}

func runDeleteUser(cmd *cobra.Command, args []string) error {
	username := args[0]
	kc := settings()
	ctx := context.Background()

	id, err := kc.userIDByUsername(ctx, username)
	if err != nil {
		return err
	}
	if _, err := kc.adminWrite(ctx, http.MethodDelete, "/users/"+id, nil); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Deleted user %s", username)))
	return nil
}

var (
	resetPasswordCmd = &cobra.Command{
		Use:   "reset-pwd [username]",
		Short: "Reset user password",
		Long:  "Reset a user's password and send reset instructions",
		Args:  cobra.ExactArgs(1),
		RunE:  runResetPassword,
	}

	// Reset password specific flags
	sendEmail      bool
	resetPassword  string
	resetPrompt    bool
	resetTemporary bool
)

func init() {
	resetPasswordCmd.Flags().BoolVarP(&sendEmail, "send-email", "e", false, "Email a password-reset link (requires realm SMTP)")
	resetPasswordCmd.Flags().StringVar(&resetPassword, "set-password", "", "New password to set directly")
	resetPasswordCmd.Flags().BoolVar(&resetPrompt, "prompt", false, "Prompt for the new password")
	resetPasswordCmd.Flags().BoolVar(&resetTemporary, "temporary", false, "Force the user to change the password at next login")
}

func runResetPassword(cmd *cobra.Command, args []string) error {
	username := args[0]
	kc := settings()
	ctx := context.Background()

	id, err := kc.userIDByUsername(ctx, username)
	if err != nil {
		return err
	}

	// Two supported modes:
	//   --set-password (or prompt): set a new password directly.
	//   otherwise: trigger Keycloak's UPDATE_PASSWORD required action, and if
	//   --send-email is set, ask Keycloak to email the reset link.
	if resetPassword != "" || resetPrompt {
		pw := resetPassword
		if pw == "" {
			p, perr := promptPassword(fmt.Sprintf("New password for %s: ", username))
			if perr != nil {
				return perr
			}
			pw = p
		}
		body := map[string]interface{}{"type": "password", "value": pw, "temporary": resetTemporary}
		if _, err := kc.adminWrite(ctx, http.MethodPut, "/users/"+id+"/reset-password", body); err != nil {
			return fmt.Errorf("reset password: %w", err)
		}
		fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Password reset for user %s", username)))
		if resetTemporary {
			fmt.Println(helpers.CreateMuted("   Marked temporary — the user must change it at next login."))
		}
		return nil
	}

	// No password supplied: fall back to the "execute actions email" flow.
	if !sendEmail {
		return fmt.Errorf("provide --set-password / --prompt to set a password, or --send-email to email a reset link")
	}
	fmt.Println("📧 Requesting Keycloak to email a password-reset link...")
	if _, err := kc.adminWrite(ctx, http.MethodPut, "/users/"+id+"/execute-actions-email", []string{"UPDATE_PASSWORD"}); err != nil {
		return fmt.Errorf("send reset email (is SMTP configured in the realm?): %w", err)
	}
	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("✅ Reset email sent for user %s", username)))
	return nil
}
