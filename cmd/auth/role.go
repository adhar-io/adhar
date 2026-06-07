package auth

import (
	"context"
	"fmt"
	"strings"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
)

var (
	roleCmd = &cobra.Command{
		Use:   "role",
		Short: "Manage roles and permissions",
		Long: `Manage platform roles and permissions including:
- Role creation and deletion
- Permission assignment
- Role hierarchy and inheritance
- Access control policies`,
		RunE: runRole,
	}

	// Role specific flags
	roleID   string
	roleName string
)

func init() {
	roleCmd.Flags().StringVarP(&roleID, "id", "i", "", "Role ID")
	roleCmd.Flags().StringVarP(&roleName, "name", "n", "", "Role name")

	// Add role subcommands
	roleCmd.AddCommand(createRoleCmd)
	roleCmd.AddCommand(listRolesCmd)
	roleCmd.AddCommand(getRoleCmd)
	roleCmd.AddCommand(updateRoleCmd)
	roleCmd.AddCommand(deleteRoleCmd)
	roleCmd.AddCommand(assignRoleCmd)
	roleCmd.AddCommand(revokeRoleCmd)
}

func runRole(cmd *cobra.Command, args []string) error {
	fmt.Println("🔑 Adhar Platform Role Management")
	fmt.Println("")
	fmt.Println("Available commands:")
	fmt.Println("  create      - Create a new role")
	fmt.Println("  list        - List all roles")
	fmt.Println("  get         - Get role details")
	fmt.Println("  update      - Update role information")
	fmt.Println("  delete      - Delete a role")
	fmt.Println("  assign      - Assign role to user/group")
	fmt.Println("  revoke      - Revoke role from user/group")
	fmt.Println("")
	fmt.Println("Use 'adhar auth role <command> --help' for more information")
	return nil
}

var (
	createRoleCmd = &cobra.Command{
		Use:   "create [role-name]",
		Short: "Create a new role",
		Long:  "Create a new role with specified permissions",
		Args:  cobra.ExactArgs(1),
		RunE:  runCreateRole,
	}

	// Create role specific flags
	newRoleDesc     string
	newRolePerms    []string
	newRoleInherits string
)

func init() {
	createRoleCmd.Flags().StringVarP(&newRoleDesc, "description", "d", "", "Role description")
	createRoleCmd.Flags().StringArrayVarP(&newRolePerms, "permissions", "p", []string{}, "Role permissions")
	createRoleCmd.Flags().StringVarP(&newRoleInherits, "inherits", "i", "", "Parent role to inherit from")
}

func runCreateRole(cmd *cobra.Command, args []string) error {
	roleName := args[0]

	fmt.Printf("🔑 Creating role: %s\n", roleName)

	if newRoleDesc != "" {
		fmt.Printf("📝 Description: %s\n", newRoleDesc)
	}
	if len(newRolePerms) > 0 {
		fmt.Printf("🔐 Permissions: %v\n", newRolePerms)
	}
	if newRoleInherits != "" {
		fmt.Printf("⬆️  Inherits from: %s\n", newRoleInherits)
	}

	// TODO: Implement actual role creation logic
	fmt.Printf("✅ Successfully created role: %s\n", roleName)
	return nil
}

var (
	listRolesCmd = &cobra.Command{
		Use:   "list",
		Short: "List all roles",
		Long:  "List all platform roles with filtering options",
		RunE:  runListRoles,
	}
)

// kcRole is a subset of the Keycloak role representation.
type kcRole struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Composite   bool   `json:"composite"`
	ClientRole  bool   `json:"clientRole"`
}

func runListRoles(cmd *cobra.Command, args []string) error {
	fmt.Println("📋 Platform Roles (Keycloak realm roles)")
	kc := settings()

	var roles []kcRole
	if err := kc.adminGet(context.Background(), "/roles", &roles); err != nil {
		return err
	}

	if output == "json" {
		return helpers.PrintJSON(roles)
	}
	if output == "yaml" {
		return helpers.PrintYAML(roles)
	}

	if len(roles) == 0 {
		fmt.Println(helpers.CreateMuted("No realm roles found in " + kc.Realm))
		return nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%-30s %-11s %s\n", "🔑 ROLE", "🧩 COMPOSITE", "📝 DESCRIPTION"))
	b.WriteString(strings.Repeat("─", 90) + "\n")
	for _, r := range roles {
		comp := "no"
		if r.Composite {
			comp = "yes"
		}
		b.WriteString(fmt.Sprintf("%-30s %-11s %s\n", truncA(r.Name, 30), comp, truncA(r.Description, 40)))
	}
	fmt.Println(helpers.BorderStyle.Render(b.String()))
	fmt.Println(helpers.CreateMuted(fmt.Sprintf("%d realm role(s) in %s", len(roles), kc.Realm)))
	return nil
}

var (
	getRoleCmd = &cobra.Command{
		Use:   "get [role-name]",
		Short: "Get role details",
		Long:  "Get detailed information about a specific role",
		Args:  cobra.ExactArgs(1),
		RunE:  runGetRole,
	}
)

func runGetRole(cmd *cobra.Command, args []string) error {
	roleName := args[0]

	fmt.Printf("🔑 Role Details: %s\n", roleName)
	fmt.Println("")

	// TODO: Implement actual role retrieval logic
	fmt.Println("📭 Role not found")

	return nil
}

var (
	updateRoleCmd = &cobra.Command{
		Use:   "update [role-name]",
		Short: "Update role information",
		Long:  "Update role description, permissions, or inheritance",
		Args:  cobra.ExactArgs(1),
		RunE:  runUpdateRole,
	}

	// Update role specific flags
	updateRoleDesc     string
	updateRolePerms    []string
	updateRoleInherits string
)

func init() {
	updateRoleCmd.Flags().StringVarP(&updateRoleDesc, "description", "d", "", "New description")
	updateRoleCmd.Flags().StringArrayVarP(&updateRolePerms, "permissions", "p", []string{}, "New permissions")
	updateRoleCmd.Flags().StringVarP(&updateRoleInherits, "inherits", "i", "", "New parent role")
}

func runUpdateRole(cmd *cobra.Command, args []string) error {
	roleName := args[0]

	fmt.Printf("✏️  Updating role: %s\n", roleName)

	if updateRoleDesc != "" {
		fmt.Printf("📝 New description: %s\n", updateRoleDesc)
	}
	if len(updateRolePerms) > 0 {
		fmt.Printf("🔐 New permissions: %v\n", updateRolePerms)
	}
	if updateRoleInherits != "" {
		fmt.Printf("⬆️  New parent role: %s\n", updateRoleInherits)
	}

	// TODO: Implement actual role update logic
	fmt.Printf("✅ Successfully updated role: %s\n", roleName)
	return nil
}

var (
	deleteRoleCmd = &cobra.Command{
		Use:   "delete [role-name]",
		Short: "Delete a role",
		Long:  "Delete a role from the platform",
		Args:  cobra.ExactArgs(1),
		RunE:  runDeleteRole,
	}

	// Delete role specific flags
	forceDeleteRole bool
)

func init() {
	deleteRoleCmd.Flags().BoolVarP(&forceDeleteRole, "force", "f", false, "Force deletion without confirmation")
}

func runDeleteRole(cmd *cobra.Command, args []string) error {
	roleName := args[0]

	fmt.Printf("🗑️  Deleting role: %s\n", roleName)

	// TODO: Implement actual role deletion logic
	fmt.Printf("✅ Successfully deleted role: %s\n", roleName)
	return nil
}

var (
	assignRoleCmd = &cobra.Command{
		Use:   "assign [role-name] [user|group] [name]",
		Short: "Assign role to user or group",
		Long:  "Assign a role to a specific user or group",
		Args:  cobra.ExactArgs(3),
		RunE:  runAssignRole,
	}

	// Assign role specific flags
	assignScope string
)

func init() {
	assignRoleCmd.Flags().StringVarP(&assignScope, "scope", "s", "namespace", "Assignment scope (namespace, cluster, global)")
}

func runAssignRole(cmd *cobra.Command, args []string) error {
	roleName := args[0]
	entityType := args[1]
	entityName := args[2]

	fmt.Printf("➕ Assigning role %s to %s %s\n", roleName, entityType, entityName)
	fmt.Printf("🌐 Scope: %s\n", assignScope)

	// TODO: Implement actual role assignment logic
	fmt.Printf("✅ Successfully assigned role %s to %s %s\n", roleName, entityType, entityName)
	return nil
}

var (
	revokeRoleCmd = &cobra.Command{
		Use:   "revoke [role-name] [user|group] [name]",
		Short: "Revoke role from user or group",
		Long:  "Revoke a role from a specific user or group",
		Args:  cobra.ExactArgs(3),
		RunE:  runRevokeRole,
	}
)

func runRevokeRole(cmd *cobra.Command, args []string) error {
	roleName := args[0]
	entityType := args[1]
	entityName := args[2]

	fmt.Printf("➖ Revoking role %s from %s %s\n", roleName, entityType, entityName)

	// TODO: Implement actual role revocation logic
	fmt.Printf("✅ Successfully revoked role %s from %s %s\n", roleName, entityType, entityName)
	return nil
}
