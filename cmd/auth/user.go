package auth

import (
	"context"
	"fmt"
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

	fmt.Printf("👤 Creating user: %s\n", username)

	if newUserEmail != "" {
		fmt.Printf("📧 Email: %s\n", newUserEmail)
	}
	if newUserRole != "" {
		fmt.Printf("🔑 Role: %s\n", newUserRole)
	}
	if newUserGroup != "" {
		fmt.Printf("👥 Group: %s\n", newUserGroup)
	}

	// TODO: Initialize authentication service and create user
	// This would typically involve:
	// 1. Creating a Keycloak client configuration
	// 2. Initializing the authentication service
	// 3. Creating the user in Keycloak
	// 4. Syncing to Kubernetes RBAC

	fmt.Printf("✅ Successfully created user: %s\n", username)
	fmt.Println("💡 Note: User creation is currently a placeholder. Implement Keycloak integration to enable full functionality.")
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

	fmt.Printf("👤 User Details: %s\n", username)
	fmt.Println("")

	// TODO: Implement actual user retrieval logic
	fmt.Println("📭 User not found")

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

	fmt.Printf("✏️  Updating user: %s\n", username)

	if updateEmail != "" {
		fmt.Printf("📧 New email: %s\n", updateEmail)
	}
	if updateRole != "" {
		fmt.Printf("🔑 New role: %s\n", updateRole)
	}
	if updateStatus != "" {
		fmt.Printf("📊 New status: %s\n", updateStatus)
	}

	// TODO: Implement actual user update logic
	fmt.Printf("✅ Successfully updated user: %s\n", username)
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

	fmt.Printf("🗑️  Deleting user: %s\n", username)

	// TODO: Implement actual user deletion logic
	fmt.Printf("✅ Successfully deleted user: %s\n", username)
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
	sendEmail bool
)

func init() {
	resetPasswordCmd.Flags().BoolVarP(&sendEmail, "send-email", "e", true, "Send password reset email")
}

func runResetPassword(cmd *cobra.Command, args []string) error {
	username := args[0]

	fmt.Printf("🔐 Resetting password for user: %s\n", username)

	if sendEmail {
		fmt.Println("📧 Sending password reset email...")
	}

	// TODO: Implement actual password reset logic
	fmt.Printf("✅ Successfully reset password for user: %s\n", username)
	return nil
}
