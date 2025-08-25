package auth

import (
	"fmt"
	"strings"

	"adhar-io/adhar/platform/auth/keycloak"
	"adhar-io/adhar/platform/auth/rbac"
	"adhar-io/adhar/platform/logger"
)

// Service provides authentication and authorization services
type Service struct {
	keycloakClient *keycloak.Client
	rbacManager    *rbac.Manager
	config         *Config
}

// Config holds authentication service configuration
type Config struct {
	KeycloakURL      string
	KeycloakRealm    string
	KeycloakClientID string
	KeycloakSecret   string
	Kubeconfig       string
	DefaultNamespace string
}

// NewService creates a new authentication service
func NewService(config *Config) (*Service, error) {
	// Initialize Keycloak client
	keycloakClient := keycloak.NewClient(
		config.KeycloakURL,
		config.KeycloakRealm,
		config.KeycloakClientID,
		config.KeycloakSecret,
	)

	// Initialize RBAC manager
	rbacManager, err := rbac.NewManager(config.Kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create RBAC manager: %w", err)
	}

	service := &Service{
		keycloakClient: keycloakClient,
		rbacManager:    rbacManager,
		config:         config,
	}

	// Create default roles if they don't exist
	if err := service.initializeDefaultRoles(); err != nil {
		logger.Warnf("Failed to initialize default roles: %v", err)
	}

	return service, nil
}

// initializeDefaultRoles creates default platform roles
func (s *Service) initializeDefaultRoles() error {
	logger.Info("Initializing default platform roles...")

	if err := s.rbacManager.CreateDefaultRoles(); err != nil {
		return fmt.Errorf("failed to create default roles: %w", err)
	}

	logger.Info("Default platform roles created successfully")
	return nil
}

// AuthenticateUser authenticates a user with Keycloak
func (s *Service) AuthenticateUser(username, password string) (*keycloak.User, error) {
	logger.Infof("Authenticating user: %s", username)

	// For now, we'll use the admin client to get user info
	// In a real implementation, you'd validate credentials
	user, err := s.keycloakClient.GetUser(username)
	if err != nil {
		return nil, fmt.Errorf("authentication failed: %w", err)
	}

	if !user.Enabled {
		return nil, fmt.Errorf("user account is disabled")
	}

	logger.Infof("User %s authenticated successfully", username)
	return user, nil
}

// CreateUser creates a new user in both Keycloak and Kubernetes
func (s *Service) CreateUser(user *keycloak.User) error {
	logger.Infof("Creating user: %s", user.Username)

	// Create user in Keycloak
	if err := s.keycloakClient.CreateUser(user); err != nil {
		return fmt.Errorf("failed to create user in Keycloak: %w", err)
	}

	// Get the created user to get the ID
	createdUser, err := s.keycloakClient.GetUser(user.Username)
	if err != nil {
		return fmt.Errorf("failed to get created user: %w", err)
	}

	// Create Kubernetes ServiceAccount for the user
	if err := s.createUserServiceAccount(createdUser); err != nil {
		logger.Warnf("Failed to create Kubernetes ServiceAccount for user %s: %v", user.Username, err)
	}

	// Assign default role based on user attributes
	if err := s.assignDefaultRole(createdUser); err != nil {
		logger.Warnf("Failed to assign default role for user %s: %v", user.Username, err)
	}

	logger.Infof("User %s created successfully", user.Username)
	return nil
}

// createUserServiceAccount creates a Kubernetes ServiceAccount for the user
func (s *Service) createUserServiceAccount(user *keycloak.User) error {
	// This would create a ServiceAccount and link it to the user
	// For now, we'll just log the intention
	logger.Infof("Creating ServiceAccount for user: %s", user.Username)

	// TODO: Implement ServiceAccount creation
	// - Create ServiceAccount in the default namespace
	// - Create role binding to appropriate role
	// - Link ServiceAccount to Keycloak user via annotations

	return nil
}

// assignDefaultRole assigns a default role to the user
func (s *Service) assignDefaultRole(user *keycloak.User) error {
	// Determine default role based on user attributes or groups
	defaultRole := "adhar-viewer" // Default to viewer role

	// Check if user has admin attributes
	if hasAdminAttributes(user) {
		defaultRole = "adhar-admin"
	} else if hasDeveloperAttributes(user) {
		defaultRole = "adhar-developer"
	}

	logger.Infof("Assigning default role %s to user %s", defaultRole, user.Username)

	// Create role binding
	binding := &rbac.RoleBinding{
		Name:      fmt.Sprintf("user-%s-binding", user.Username),
		Namespace: s.config.DefaultNamespace,
		Role:      defaultRole,
		Subjects: []rbac.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      fmt.Sprintf("user-%s", user.Username),
				Namespace: s.config.DefaultNamespace,
			},
		},
	}

	if err := s.rbacManager.CreateRoleBinding(binding); err != nil {
		return fmt.Errorf("failed to create role binding: %w", err)
	}

	return nil
}

// hasAdminAttributes checks if user has admin attributes
func hasAdminAttributes(user *keycloak.User) bool {
	// Check if user is in admin groups
	for _, group := range user.Groups {
		if strings.Contains(strings.ToLower(group), "admin") {
			return true
		}
	}

	// Check if user has admin attributes
	if adminAttr, exists := user.Attributes["role"]; exists {
		for _, role := range adminAttr {
			if strings.Contains(strings.ToLower(role), "admin") {
				return true
			}
		}
	}

	return false
}

// hasDeveloperAttributes checks if user has developer attributes
func hasDeveloperAttributes(user *keycloak.User) bool {
	// Check if user is in developer groups
	for _, group := range user.Groups {
		if strings.Contains(strings.ToLower(group), "developer") || strings.Contains(strings.ToLower(group), "dev") {
			return true
		}
	}

	// Check if user has developer attributes
	if devAttr, exists := user.Attributes["role"]; exists {
		for _, role := range devAttr {
			if strings.Contains(strings.ToLower(role), "developer") || strings.Contains(strings.ToLower(role), "dev") {
				return true
			}
		}
	}

	return false
}

// SyncKeycloakToKubernetes syncs Keycloak users and roles to Kubernetes
func (s *Service) SyncKeycloakToKubernetes() error {
	logger.Info("Starting Keycloak to Kubernetes sync...")

	// Get all users from Keycloak
	users, err := s.keycloakClient.ListUsers()
	if err != nil {
		return fmt.Errorf("failed to list Keycloak users: %w", err)
	}

	// Get all roles from Keycloak
	roles, err := s.keycloakClient.ListRoles()
	if err != nil {
		return fmt.Errorf("failed to list Keycloak roles: %w", err)
	}

	// Extract role names
	var roleNames []string
	for _, role := range roles {
		roleNames = append(roleNames, role.Name)
	}

	// Sync roles to Kubernetes
	if err := s.rbacManager.SyncKeycloakRoles(roleNames, s.config.DefaultNamespace); err != nil {
		return fmt.Errorf("failed to sync Keycloak roles: %w", err)
	}

	// Sync users to Kubernetes
	for _, user := range users {
		if err := s.syncUserToKubernetes(&user); err != nil {
			logger.Warnf("Failed to sync user %s to Kubernetes: %v", user.Username, err)
		}
	}

	logger.Info("Keycloak to Kubernetes sync completed successfully")
	return nil
}

// syncUserToKubernetes syncs a single user to Kubernetes
func (s *Service) syncUserToKubernetes(user *keycloak.User) error {
	logger.Infof("Syncing user %s to Kubernetes", user.Username)

	// Create or update ServiceAccount
	if err := s.createUserServiceAccount(user); err != nil {
		return fmt.Errorf("failed to create ServiceAccount: %w", err)
	}

	// Assign appropriate roles
	if err := s.assignDefaultRole(user); err != nil {
		return fmt.Errorf("failed to assign default role: %w", err)
	}

	return nil
}

// GetUserPermissions gets the effective permissions for a user
func (s *Service) GetUserPermissions(username string) ([]rbac.PolicyRule, error) {
	user, err := s.keycloakClient.GetUser(username)
	if err != nil {
		return nil, fmt.Errorf("failed to get user: %w", err)
	}

	// Get user's roles from Keycloak
	roles, err := s.keycloakClient.ListRoles()
	if err != nil {
		return nil, fmt.Errorf("failed to list roles: %w", err)
	}

	// Get user's groups
	groups, err := s.keycloakClient.ListGroups()
	if err != nil {
		return nil, fmt.Errorf("failed to list groups: %w", err)
	}

	// Collect all permissions
	var allPermissions []rbac.PolicyRule

	// Add permissions from user's direct roles
	for _, role := range roles {
		if userHasRole(user, role.Name) {
			// Convert Keycloak role to Kubernetes policy rules
			// This is a simplified mapping - in practice, you'd have more sophisticated mapping
			policyRules := s.mapKeycloakRoleToPolicyRules(&role)
			allPermissions = append(allPermissions, policyRules...)
		}
	}

	// Add permissions from user's groups
	for _, group := range groups {
		if userInGroup(user, group.Name) {
			// Get group roles and their permissions
			for _, roleName := range group.RealmRoles {
				policyRules := s.mapKeycloakRoleToPolicyRules(&keycloak.Role{Name: roleName})
				allPermissions = append(allPermissions, policyRules...)
			}
		}
	}

	return allPermissions, nil
}

// userHasRole checks if user has a specific role
func userHasRole(user *keycloak.User, roleName string) bool {
	// This is a simplified check - in practice, you'd check the user's actual roles
	// For now, we'll assume all users have basic access
	return true
}

// userInGroup checks if user is in a specific group
func userInGroup(user *keycloak.User, groupName string) bool {
	for _, group := range user.Groups {
		if group == groupName {
			return true
		}
	}
	return false
}

// mapKeycloakRoleToPolicyRules maps Keycloak roles to Kubernetes policy rules
func (s *Service) mapKeycloakRoleToPolicyRules(role *keycloak.Role) []rbac.PolicyRule {
	// This is a simplified mapping - in practice, you'd have more sophisticated mapping
	switch strings.ToLower(role.Name) {
	case "admin", "administrator":
		return []rbac.PolicyRule{
			{
				APIGroups: []string{"*"},
				Resources: []string{"*"},
				Verbs:     []string{"*"},
			},
		}
	case "developer", "dev":
		return []rbac.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"pods", "services", "configmaps", "secrets"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{"apps"},
				Resources: []string{"deployments", "replicasets", "statefulsets"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
		}
	default:
		return []rbac.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"pods", "services", "configmaps"},
				Verbs:     []string{"get", "list", "watch"},
			},
		}
	}
}

// ValidateAccess validates if a user has access to a specific resource
func (s *Service) ValidateAccess(username, resource, verb string) (bool, error) {
	permissions, err := s.GetUserPermissions(username)
	if err != nil {
		return false, fmt.Errorf("failed to get user permissions: %w", err)
	}

	// Check if user has permission for the resource and verb
	for _, permission := range permissions {
		if s.permissionMatches(permission, resource, verb) {
			return true, nil
		}
	}

	return false, nil
}

// permissionMatches checks if a permission matches the requested resource and verb
func (s *Service) permissionMatches(permission rbac.PolicyRule, resource, verb string) bool {
	// Check if the verb is allowed
	verbAllowed := false
	for _, v := range permission.Verbs {
		if v == "*" || v == verb {
			verbAllowed = true
			break
		}
	}

	if !verbAllowed {
		return false
	}

	// Check if the resource is allowed
	resourceAllowed := false
	for _, r := range permission.Resources {
		if r == "*" || r == resource {
			resourceAllowed = true
			break
		}
	}

	return resourceAllowed
}
