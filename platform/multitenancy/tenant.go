package multitenancy

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// TenantManager handles multi-tenancy operations
type TenantManager struct {
	k8sClient kubernetes.Interface
}

// TenantConfig defines tenant configuration
type TenantConfig struct {
	Name        string
	DisplayName string
	Labels      map[string]string
	Annotations map[string]string

	// Resource quotas
	ResourceQuotas ResourceQuotaConfig

	// Network policies
	EnableNetworkPolicies bool
	AllowedNamespaces     []string

	// RBAC
	Admins     []string
	Developers []string
	Viewers    []string
}

// ResourceQuotaConfig defines resource limits for a tenant
type ResourceQuotaConfig struct {
	CPU               string
	Memory            string
	Storage           string
	PersistentVolumes int
	Services          int
	Pods              int
	Secrets           int
	ConfigMaps        int
}

// NewTenantManager creates a new tenant manager
func NewTenantManager(k8sClient kubernetes.Interface) *TenantManager {
	return &TenantManager{
		k8sClient: k8sClient,
	}
}

// CreateTenant creates a new tenant with namespace, RBAC, and resource quotas
func (tm *TenantManager) CreateTenant(ctx context.Context, config TenantConfig) error {
	// 1. Create namespace
	if err := tm.createNamespace(ctx, config); err != nil {
		return fmt.Errorf("failed to create namespace: %w", err)
	}

	// 2. Create resource quota
	if err := tm.createResourceQuota(ctx, config); err != nil {
		return fmt.Errorf("failed to create resource quota: %w", err)
	}

	// 3. Create limit range
	if err := tm.createLimitRange(ctx, config); err != nil {
		return fmt.Errorf("failed to create limit range: %w", err)
	}

	// 4. Create RBAC roles and bindings
	if err := tm.createRBAC(ctx, config); err != nil {
		return fmt.Errorf("failed to create RBAC: %w", err)
	}

	// 5. Create network policies if enabled
	if config.EnableNetworkPolicies {
		if err := tm.createNetworkPolicies(ctx, config); err != nil {
			return fmt.Errorf("failed to create network policies: %w", err)
		}
	}

	return nil
}

// createNamespace creates the tenant namespace
func (tm *TenantManager) createNamespace(ctx context.Context, config TenantConfig) error {
	labels := config.Labels
	if labels == nil {
		labels = make(map[string]string)
	}
	labels["adhar.io/tenant"] = config.Name
	labels["adhar.io/managed-by"] = "adhar-control-plane"

	annotations := config.Annotations
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations["adhar.io/display-name"] = config.DisplayName

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:        config.Name,
			Labels:      labels,
			Annotations: annotations,
		},
	}

	_, err := tm.k8sClient.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	return err
}

// createResourceQuota creates resource quotas for the tenant
func (tm *TenantManager) createResourceQuota(ctx context.Context, config TenantConfig) error {
	quota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tenant-quota",
			Namespace: config.Name,
			Labels: map[string]string{
				"adhar.io/tenant": config.Name,
			},
		},
		Spec: corev1.ResourceQuotaSpec{
			Hard: corev1.ResourceList{},
		},
	}

	// Add CPU quota
	if config.ResourceQuotas.CPU != "" {
		cpuQuantity, err := resource.ParseQuantity(config.ResourceQuotas.CPU)
		if err == nil {
			quota.Spec.Hard[corev1.ResourceRequestsCPU] = cpuQuantity
			quota.Spec.Hard[corev1.ResourceLimitsCPU] = cpuQuantity
		}
	}

	// Add memory quota
	if config.ResourceQuotas.Memory != "" {
		memQuantity, err := resource.ParseQuantity(config.ResourceQuotas.Memory)
		if err == nil {
			quota.Spec.Hard[corev1.ResourceRequestsMemory] = memQuantity
			quota.Spec.Hard[corev1.ResourceLimitsMemory] = memQuantity
		}
	}

	// Add storage quota
	if config.ResourceQuotas.Storage != "" {
		storageQuantity, err := resource.ParseQuantity(config.ResourceQuotas.Storage)
		if err == nil {
			quota.Spec.Hard[corev1.ResourceRequestsStorage] = storageQuantity
		}
	}

	// Add object counts
	if config.ResourceQuotas.PersistentVolumes > 0 {
		quota.Spec.Hard[corev1.ResourcePersistentVolumeClaims] = *resource.NewQuantity(int64(config.ResourceQuotas.PersistentVolumes), resource.DecimalSI)
	}

	if config.ResourceQuotas.Services > 0 {
		quota.Spec.Hard[corev1.ResourceServices] = *resource.NewQuantity(int64(config.ResourceQuotas.Services), resource.DecimalSI)
	}

	if config.ResourceQuotas.Pods > 0 {
		quota.Spec.Hard[corev1.ResourcePods] = *resource.NewQuantity(int64(config.ResourceQuotas.Pods), resource.DecimalSI)
	}

	if config.ResourceQuotas.Secrets > 0 {
		quota.Spec.Hard[corev1.ResourceSecrets] = *resource.NewQuantity(int64(config.ResourceQuotas.Secrets), resource.DecimalSI)
	}

	if config.ResourceQuotas.ConfigMaps > 0 {
		quota.Spec.Hard[corev1.ResourceConfigMaps] = *resource.NewQuantity(int64(config.ResourceQuotas.ConfigMaps), resource.DecimalSI)
	}

	_, err := tm.k8sClient.CoreV1().ResourceQuotas(config.Name).Create(ctx, quota, metav1.CreateOptions{})
	return err
}

// createLimitRange creates default resource limits for pods
func (tm *TenantManager) createLimitRange(ctx context.Context, config TenantConfig) error {
	limitRange := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tenant-limits",
			Namespace: config.Name,
			Labels: map[string]string{
				"adhar.io/tenant": config.Name,
			},
		},
		Spec: corev1.LimitRangeSpec{
			Limits: []corev1.LimitRangeItem{
				{
					Type: corev1.LimitTypeContainer,
					Default: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("512Mi"),
					},
					DefaultRequest: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
					Max: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
					},
					Min: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("50m"),
						corev1.ResourceMemory: resource.MustParse("64Mi"),
					},
				},
			},
		},
	}

	_, err := tm.k8sClient.CoreV1().LimitRanges(config.Name).Create(ctx, limitRange, metav1.CreateOptions{})
	return err
}

// createRBAC creates RBAC roles and bindings for the tenant
func (tm *TenantManager) createRBAC(ctx context.Context, config TenantConfig) error {
	// Create admin role
	adminRole := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tenant-admin",
			Namespace: config.Name,
			Labels: map[string]string{
				"adhar.io/tenant": config.Name,
			},
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"*"},
				Resources: []string{"*"},
				Verbs:     []string{"*"},
			},
		},
	}

	_, err := tm.k8sClient.RbacV1().Roles(config.Name).Create(ctx, adminRole, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	// Create developer role
	devRole := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tenant-developer",
			Namespace: config.Name,
			Labels: map[string]string{
				"adhar.io/tenant": config.Name,
			},
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"", "apps", "batch"},
				Resources: []string{"pods", "deployments", "services", "configmaps", "secrets", "jobs", "cronjobs"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"pods/log", "pods/exec"},
				Verbs:     []string{"get", "create"},
			},
		},
	}

	_, err = tm.k8sClient.RbacV1().Roles(config.Name).Create(ctx, devRole, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	// Create viewer role
	viewerRole := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tenant-viewer",
			Namespace: config.Name,
			Labels: map[string]string{
				"adhar.io/tenant": config.Name,
			},
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"*"},
				Resources: []string{"*"},
				Verbs:     []string{"get", "list", "watch"},
			},
		},
	}

	_, err = tm.k8sClient.RbacV1().Roles(config.Name).Create(ctx, viewerRole, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	// Create role bindings for admins
	for _, admin := range config.Admins {
		binding := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("admin-%s", admin),
				Namespace: config.Name,
				Labels: map[string]string{
					"adhar.io/tenant": config.Name,
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Role",
				Name:     "tenant-admin",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind: "User",
					Name: admin,
				},
			},
		}

		_, err := tm.k8sClient.RbacV1().RoleBindings(config.Name).Create(ctx, binding, metav1.CreateOptions{})
		if err != nil {
			return err
		}
	}

	// Create role bindings for developers
	for _, dev := range config.Developers {
		binding := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("developer-%s", dev),
				Namespace: config.Name,
				Labels: map[string]string{
					"adhar.io/tenant": config.Name,
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Role",
				Name:     "tenant-developer",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind: "User",
					Name: dev,
				},
			},
		}

		_, err := tm.k8sClient.RbacV1().RoleBindings(config.Name).Create(ctx, binding, metav1.CreateOptions{})
		if err != nil {
			return err
		}
	}

	// Create role bindings for viewers
	for _, viewer := range config.Viewers {
		binding := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("viewer-%s", viewer),
				Namespace: config.Name,
				Labels: map[string]string{
					"adhar.io/tenant": config.Name,
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Role",
				Name:     "tenant-viewer",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind: "User",
					Name: viewer,
				},
			},
		}

		_, err := tm.k8sClient.RbacV1().RoleBindings(config.Name).Create(ctx, binding, metav1.CreateOptions{})
		if err != nil {
			return err
		}
	}

	return nil
}

// createNetworkPolicies creates network isolation policies
func (tm *TenantManager) createNetworkPolicies(ctx context.Context, config TenantConfig) error {
	// This is a placeholder - in production, you'd use networking.k8s.io/v1.NetworkPolicy
	// to create default deny all ingress and egress policies
	// For brevity, showing the concept

	return nil
}

// DeleteTenant removes a tenant and all its resources
func (tm *TenantManager) DeleteTenant(ctx context.Context, tenantName string) error {
	return tm.k8sClient.CoreV1().Namespaces().Delete(ctx, tenantName, metav1.DeleteOptions{})
}

// ListTenants returns all tenants
func (tm *TenantManager) ListTenants(ctx context.Context) ([]string, error) {
	namespaces, err := tm.k8sClient.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: "adhar.io/tenant",
	})
	if err != nil {
		return nil, err
	}

	tenants := make([]string, 0, len(namespaces.Items))
	for _, ns := range namespaces.Items {
		tenants = append(tenants, ns.Name)
	}

	return tenants, nil
}

// GetTenantQuota returns the resource quota for a tenant
func (tm *TenantManager) GetTenantQuota(ctx context.Context, tenantName string) (*corev1.ResourceQuota, error) {
	return tm.k8sClient.CoreV1().ResourceQuotas(tenantName).Get(ctx, "tenant-quota", metav1.GetOptions{})
}

// UpdateTenantQuota updates resource quotas for a tenant
func (tm *TenantManager) UpdateTenantQuota(ctx context.Context, tenantName string, quotas ResourceQuotaConfig) error {
	quota, err := tm.GetTenantQuota(ctx, tenantName)
	if err != nil {
		return err
	}

	// Update quotas
	if quotas.CPU != "" {
		cpuQuantity, err := resource.ParseQuantity(quotas.CPU)
		if err == nil {
			quota.Spec.Hard[corev1.ResourceRequestsCPU] = cpuQuantity
			quota.Spec.Hard[corev1.ResourceLimitsCPU] = cpuQuantity
		}
	}

	if quotas.Memory != "" {
		memQuantity, err := resource.ParseQuantity(quotas.Memory)
		if err == nil {
			quota.Spec.Hard[corev1.ResourceRequestsMemory] = memQuantity
			quota.Spec.Hard[corev1.ResourceLimitsMemory] = memQuantity
		}
	}

	_, err = tm.k8sClient.CoreV1().ResourceQuotas(tenantName).Update(ctx, quota, metav1.UpdateOptions{})
	return err
}
