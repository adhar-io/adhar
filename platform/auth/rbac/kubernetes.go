package rbac

import (
	"context"
	"fmt"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Manager handles Kubernetes RBAC operations
type Manager struct {
	clientset *kubernetes.Clientset
	config    *rest.Config
}

// RoleBinding represents a simplified role binding
type RoleBinding struct {
	Name      string
	Namespace string
	Role      string
	Subjects  []Subject
}

// Subject represents a role binding subject
type Subject struct {
	Kind      string
	Name      string
	Namespace string
}

// ClusterRole represents a simplified cluster role
type ClusterRole struct {
	Name        string
	Rules       []PolicyRule
	Annotations map[string]string
}

// PolicyRule represents a policy rule
type PolicyRule struct {
	APIGroups     []string
	Resources     []string
	ResourceNames []string
	Verbs         []string
}

// NewManager creates a new RBAC manager
func NewManager(kubeconfig string) (*Manager, error) {
	var config *rest.Config
	var err error

	if kubeconfig != "" {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		config, err = rest.InClusterConfig()
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get kubernetes config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	return &Manager{
		clientset: clientset,
		config:    config,
	}, nil
}

// CreateClusterRole creates a new cluster role
func (m *Manager) CreateClusterRole(role *ClusterRole) error {
	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:        role.Name,
			Annotations: role.Annotations,
		},
		Rules: convertPolicyRules(role.Rules),
	}

	_, err := m.clientset.RbacV1().ClusterRoles().Create(context.Background(), clusterRole, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create cluster role: %w", err)
	}

	return nil
}

// CreateRole creates a new role in a namespace
func (m *Manager) CreateRole(namespace string, role *ClusterRole) error {
	k8sRole := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:        role.Name,
			Namespace:   namespace,
			Annotations: role.Annotations,
		},
		Rules: convertPolicyRules(role.Rules),
	}

	_, err := m.clientset.RbacV1().Roles(namespace).Create(context.Background(), k8sRole, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create role: %w", err)
	}

	return nil
}

// CreateClusterRoleBinding creates a new cluster role binding
func (m *Manager) CreateClusterRoleBinding(binding *RoleBinding) error {
	clusterRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: binding.Name,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     binding.Role,
		},
		Subjects: convertSubjects(binding.Subjects),
	}

	_, err := m.clientset.RbacV1().ClusterRoleBindings().Create(context.Background(), clusterRoleBinding, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create cluster role binding: %w", err)
	}

	return nil
}

// CreateRoleBinding creates a new role binding in a namespace
func (m *Manager) CreateRoleBinding(binding *RoleBinding) error {
	roleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      binding.Name,
			Namespace: binding.Namespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     binding.Role,
		},
		Subjects: convertSubjects(binding.Subjects),
	}

	_, err := m.clientset.RbacV1().RoleBindings(binding.Namespace).Create(context.Background(), roleBinding, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create role binding: %w", err)
	}
	return nil
}

// DeleteClusterRole deletes a cluster role
func (m *Manager) DeleteClusterRole(name string) error {
	err := m.clientset.RbacV1().ClusterRoles().Delete(context.Background(), name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete cluster role: %w", err)
	}
	return nil
}

// DeleteRole deletes a role from a namespace
func (m *Manager) DeleteRole(namespace, name string) error {
	err := m.clientset.RbacV1().Roles(namespace).Delete(context.Background(), name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete role: %w", err)
	}
	return nil
}

// DeleteClusterRoleBinding deletes a cluster role binding
func (m *Manager) DeleteClusterRoleBinding(name string) error {
	err := m.clientset.RbacV1().ClusterRoleBindings().Delete(context.Background(), name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete cluster role binding: %w", err)
	}
	return nil
}

// DeleteRoleBinding deletes a role binding from a namespace
func (m *Manager) DeleteRoleBinding(namespace, name string) error {
	err := m.clientset.RbacV1().RoleBindings(namespace).Delete(context.Background(), name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete role binding: %w", err)
	}
	return nil
}

// ListClusterRoles lists all cluster roles
func (m *Manager) ListClusterRoles() ([]ClusterRole, error) {
	clusterRoles, err := m.clientset.RbacV1().ClusterRoles().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list cluster roles: %w", err)
	}

	var roles []ClusterRole
	for _, cr := range clusterRoles.Items {
		roles = append(roles, ClusterRole{
			Name:        cr.Name,
			Rules:       convertK8sPolicyRules(cr.Rules),
			Annotations: cr.Annotations,
		})
	}

	return roles, nil
}

// ListRoles lists all roles in a namespace
func (m *Manager) ListRoles(namespace string) ([]ClusterRole, error) {
	roles, err := m.clientset.RbacV1().Roles(namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list roles: %w", err)
	}

	var result []ClusterRole
	for _, r := range roles.Items {
		result = append(result, ClusterRole{
			Name:        r.Name,
			Rules:       convertK8sPolicyRules(r.Rules),
			Annotations: r.Annotations,
		})
	}

	return result, nil
}

// GetClusterRole gets a specific cluster role
func (m *Manager) GetClusterRole(name string) (*ClusterRole, error) {
	clusterRole, err := m.clientset.RbacV1().ClusterRoles().Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster role: %w", err)
	}

	return &ClusterRole{
		Name:        clusterRole.Name,
		Rules:       convertK8sPolicyRules(clusterRole.Rules),
		Annotations: clusterRole.Annotations,
	}, nil
}

// GetRole gets a specific role from a namespace
func (m *Manager) GetRole(namespace, name string) (*ClusterRole, error) {
	role, err := m.clientset.RbacV1().Roles(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get role: %w", err)
	}

	return &ClusterRole{
		Name:        role.Name,
		Rules:       convertK8sPolicyRules(role.Rules),
		Annotations: role.Annotations,
	}, nil
}

// CreateDefaultRoles creates default roles for common use cases
func (m *Manager) CreateDefaultRoles() error {
	defaultRoles := []ClusterRole{
		{
			Name: "adhar-admin",
			Rules: []PolicyRule{
				{
					APIGroups: []string{"*"},
					Resources: []string{"*"},
					Verbs:     []string{"*"},
				},
			},
			Annotations: map[string]string{
				"description": "Full access to Adhar platform",
			},
		},
		{
			Name: "adhar-developer",
			Rules: []PolicyRule{
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
				{
					APIGroups: []string{"networking.k8s.io"},
					Resources: []string{"ingresses", "networkpolicies"},
					Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
				},
			},
			Annotations: map[string]string{
				"description": "Developer access to application resources",
			},
		},
		{
			Name: "adhar-viewer",
			Rules: []PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"pods", "services", "configmaps"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{"apps"},
					Resources: []string{"deployments", "replicasets", "statefulsets"},
					Verbs:     []string{"get", "list", "watch"},
				},
			},
			Annotations: map[string]string{
				"description": "Read-only access to platform resources",
			},
		},
	}

	for _, role := range defaultRoles {
		if err := m.CreateClusterRole(&role); err != nil {
			return fmt.Errorf("failed to create default role %s: %w", role.Name, err)
		}
	}

	return nil
}

// SyncKeycloakRoles syncs Keycloak roles to Kubernetes RBAC
func (m *Manager) SyncKeycloakRoles(keycloakRoles []string, namespace string) error {
	for _, roleName := range keycloakRoles {
		// Create a basic role for each Keycloak role
		role := &ClusterRole{
			Name: fmt.Sprintf("keycloak-%s", strings.ToLower(roleName)),
			Rules: []PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"pods", "services", "configmaps"},
					Verbs:     []string{"get", "list", "watch"},
				},
			},
			Annotations: map[string]string{
				"source":        "keycloak",
				"keycloak-role": roleName,
			},
		}

		if namespace == "" {
			if err := m.CreateClusterRole(role); err != nil {
				return fmt.Errorf("failed to create cluster role for Keycloak role %s: %w", roleName, err)
			}
		} else {
			if err := m.CreateRole(namespace, role); err != nil {
				return fmt.Errorf("failed to create role for Keycloak role %s in namespace %s: %w", roleName, namespace, err)
			}
		}
	}

	return nil
}

// Helper functions to convert between our types and Kubernetes types

func convertPolicyRules(rules []PolicyRule) []rbacv1.PolicyRule {
	var k8sRules []rbacv1.PolicyRule
	for _, rule := range rules {
		k8sRules = append(k8sRules, rbacv1.PolicyRule{
			APIGroups:     rule.APIGroups,
			Resources:     rule.Resources,
			ResourceNames: rule.ResourceNames,
			Verbs:         rule.Verbs,
		})
	}
	return k8sRules
}

func convertK8sPolicyRules(rules []rbacv1.PolicyRule) []PolicyRule {
	var result []PolicyRule
	for _, rule := range rules {
		result = append(result, PolicyRule{
			APIGroups:     rule.APIGroups,
			Resources:     rule.Resources,
			ResourceNames: rule.ResourceNames,
			Verbs:         rule.Verbs,
		})
	}
	return result
}

func convertSubjects(subjects []Subject) []rbacv1.Subject {
	var k8sSubjects []rbacv1.Subject
	for _, subject := range subjects {
		k8sSubjects = append(k8sSubjects, rbacv1.Subject{
			Kind:      subject.Kind,
			Name:      subject.Name,
			Namespace: subject.Namespace,
		})
	}
	return k8sSubjects
}
