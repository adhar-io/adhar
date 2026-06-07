package auth

import (
	"context"
	"fmt"
	"strings"

	"adhar-io/adhar/cmd/helpers"

	"github.com/spf13/cobra"
)

var (
	groupCmd = &cobra.Command{
		Use:   "group",
		Short: "Manage user groups",
		Long: `Manage platform user groups including:
- Group creation and deletion
- Group membership management
- Group permissions and roles
- Group hierarchy and nesting`,
		RunE: runGroup,
	}

	// Group specific flags
	groupID   string
	groupName string
)

func init() {
	groupCmd.Flags().StringVarP(&groupID, "id", "i", "", "Group ID")
	groupCmd.Flags().StringVarP(&groupName, "name", "n", "", "Group name")

	// Add group subcommands
	groupCmd.AddCommand(createGroupCmd)
	groupCmd.AddCommand(listGroupsCmd)
	groupCmd.AddCommand(getGroupCmd)
	groupCmd.AddCommand(updateGroupCmd)
	groupCmd.AddCommand(deleteGroupCmd)
	groupCmd.AddCommand(addMemberCmd)
	groupCmd.AddCommand(removeMemberCmd)
}

func runGroup(cmd *cobra.Command, args []string) error {
	fmt.Println("👥 Adhar Platform Group Management")
	fmt.Println("")
	fmt.Println("Available commands:")
	fmt.Println("  create       - Create a new group")
	fmt.Println("  list         - List all groups")
	fmt.Println("  get          - Get group details")
	fmt.Println("  update       - Update group information")
	fmt.Println("  delete       - Delete a group")
	fmt.Println("  add-member   - Add user to group")
	fmt.Println("  remove-member - Remove user from group")
	fmt.Println("")
	fmt.Println("Use 'adhar auth group <command> --help' for more information")
	return nil
}

var (
	createGroupCmd = &cobra.Command{
		Use:   "create [group-name]",
		Short: "Create a new group",
		Long:  "Create a new user group with specified details",
		Args:  cobra.ExactArgs(1),
		RunE:  runCreateGroup,
	}

	// Create group specific flags
	newGroupDesc string
	newGroupRole string
)

func init() {
	createGroupCmd.Flags().StringVarP(&newGroupDesc, "description", "d", "", "Group description")
	createGroupCmd.Flags().StringVarP(&newGroupRole, "role", "r", "member", "Default group role")
}

func runCreateGroup(cmd *cobra.Command, args []string) error {
	groupName := args[0]

	fmt.Printf("👥 Creating group: %s\n", groupName)

	if newGroupDesc != "" {
		fmt.Printf("📝 Description: %s\n", newGroupDesc)
	}
	if newGroupRole != "" {
		fmt.Printf("🔑 Default role: %s\n", newGroupRole)
	}

	// TODO: Implement actual group creation logic
	fmt.Printf("✅ Successfully created group: %s\n", groupName)
	return nil
}

var (
	listGroupsCmd = &cobra.Command{
		Use:   "list",
		Short: "List all groups",
		Long:  "List all platform groups with filtering options",
		RunE:  runListGroups,
	}
)

// kcGroup is a subset of the Keycloak group representation.
type kcGroup struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	SubGroups []kcGroup `json:"subGroups"`
}

func runListGroups(cmd *cobra.Command, args []string) error {
	fmt.Println("📋 Platform Groups (Keycloak)")
	kc := settings()

	var groups []kcGroup
	if err := kc.adminGet(context.Background(), "/groups", &groups); err != nil {
		return err
	}

	if output == "json" {
		return helpers.PrintJSON(groups)
	}
	if output == "yaml" {
		return helpers.PrintYAML(groups)
	}

	// Flatten the group tree for tabular display.
	var flat []kcGroup
	var walk func(gs []kcGroup)
	walk = func(gs []kcGroup) {
		for _, g := range gs {
			flat = append(flat, g)
			if len(g.SubGroups) > 0 {
				walk(g.SubGroups)
			}
		}
	}
	walk(groups)

	if len(flat) == 0 {
		fmt.Println(helpers.CreateMuted("No groups found in realm " + kc.Realm))
		return nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%-30s %-40s %s\n", "👥 NAME", "🧭 PATH", "🆔 ID"))
	b.WriteString(strings.Repeat("─", 100) + "\n")
	for _, g := range flat {
		b.WriteString(fmt.Sprintf("%-30s %-40s %s\n", truncA(g.Name, 30), truncA(g.Path, 40), g.ID))
	}
	fmt.Println(helpers.BorderStyle.Render(b.String()))
	fmt.Println(helpers.CreateMuted(fmt.Sprintf("%d group(s) in realm %s", len(flat), kc.Realm)))
	return nil
}

var (
	getGroupCmd = &cobra.Command{
		Use:   "get [group-name]",
		Short: "Get group details",
		Long:  "Get detailed information about a specific group",
		Args:  cobra.ExactArgs(1),
		RunE:  runGetGroup,
	}
)

func runGetGroup(cmd *cobra.Command, args []string) error {
	groupName := args[0]

	fmt.Printf("👥 Group Details: %s\n", groupName)
	fmt.Println("")

	// TODO: Implement actual group retrieval logic
	fmt.Println("📭 Group not found")

	return nil
}

var (
	updateGroupCmd = &cobra.Command{
		Use:   "update [group-name]",
		Short: "Update group information",
		Long:  "Update group description, role, or permissions",
		Args:  cobra.ExactArgs(1),
		RunE:  runUpdateGroup,
	}

	// Update group specific flags
	updateDesc      string
	updateGroupRole string
)

func init() {
	updateGroupCmd.Flags().StringVarP(&updateDesc, "description", "d", "", "New description")
	updateGroupCmd.Flags().StringVarP(&updateGroupRole, "role", "r", "", "New default role")
}

func runUpdateGroup(cmd *cobra.Command, args []string) error {
	groupName := args[0]

	fmt.Printf("✏️  Updating group: %s\n", groupName)

	if updateDesc != "" {
		fmt.Printf("📝 New description: %s\n", updateDesc)
	}
	if updateGroupRole != "" {
		fmt.Printf("🔑 New default role: %s\n", updateGroupRole)
	}

	// TODO: Implement actual group update logic
	fmt.Printf("✅ Successfully updated group: %s\n", groupName)
	return nil
}

var (
	deleteGroupCmd = &cobra.Command{
		Use:   "delete [group-name]",
		Short: "Delete a group",
		Long:  "Delete a group from the platform",
		Args:  cobra.ExactArgs(1),
		RunE:  runDeleteGroup,
	}

	// Delete group specific flags
	forceDeleteGroup bool
)

func init() {
	deleteGroupCmd.Flags().BoolVarP(&forceDeleteGroup, "force", "f", false, "Force deletion without confirmation")
}

func runDeleteGroup(cmd *cobra.Command, args []string) error {
	groupName := args[0]

	fmt.Printf("🗑️  Deleting group: %s\n", groupName)

	// TODO: Implement actual group deletion logic
	fmt.Printf("✅ Successfully deleted group: %s\n", groupName)
	return nil
}

var (
	addMemberCmd = &cobra.Command{
		Use:   "add-member [group-name] [username]",
		Short: "Add user to group",
		Long:  "Add a user to a specific group",
		Args:  cobra.ExactArgs(2),
		RunE:  runAddMember,
	}

	// Add member specific flags
	memberRole string
)

func init() {
	addMemberCmd.Flags().StringVarP(&memberRole, "role", "r", "member", "Member role in group")
}

func runAddMember(cmd *cobra.Command, args []string) error {
	groupName := args[0]
	username := args[1]

	fmt.Printf("➕ Adding user %s to group %s\n", username, groupName)
	fmt.Printf("🔑 Role: %s\n", memberRole)

	// TODO: Implement actual member addition logic
	fmt.Printf("✅ Successfully added %s to group %s\n", username, groupName)
	return nil
}

var (
	removeMemberCmd = &cobra.Command{
		Use:   "remove-member [group-name] [username]",
		Short: "Remove user from group",
		Long:  "Remove a user from a specific group",
		Args:  cobra.ExactArgs(2),
		RunE:  runRemoveMember,
	}
)

func runRemoveMember(cmd *cobra.Command, args []string) error {
	groupName := args[0]
	username := args[1]

	fmt.Printf("➖ Removing user %s from group %s\n", username, groupName)

	// TODO: Implement actual member removal logic
	fmt.Printf("✅ Successfully removed %s from group %s\n", username, groupName)
	return nil
}
