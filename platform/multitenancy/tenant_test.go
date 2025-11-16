package multitenancy

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestTenantManager_CreateTenant(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	tm := NewTenantManager(client)

	config := TenantConfig{
		Name:        "test-tenant",
		DisplayName: "Test Tenant",
		Labels: map[string]string{
			"environment": "test",
		},
		ResourceQuotas: ResourceQuotaConfig{
			CPU:               "10",
			Memory:            "20Gi",
			Storage:           "100Gi",
			PersistentVolumes: 5,
			Services:          10,
			Pods:              20,
			Secrets:           50,
			ConfigMaps:        50,
		},
		Admins:     []string{"admin@example.com"},
		Developers: []string{"dev@example.com"},
		Viewers:    []string{"viewer@example.com"},
	}

	err := tm.CreateTenant(ctx, config)
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	// Verify namespace was created
	ns, err := client.CoreV1().Namespaces().Get(ctx, "test-tenant", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get created namespace: %v", err)
	}

	if ns.Labels["adhar.io/tenant"] != "test-tenant" {
		t.Errorf("Expected tenant label, got: %v", ns.Labels)
	}

	// Verify resource quota was created
	quota, err := client.CoreV1().ResourceQuotas("test-tenant").Get(ctx, "tenant-quota", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get resource quota: %v", err)
	}

	if quota.Spec.Hard.Cpu().String() != "10" {
		t.Errorf("Expected CPU quota to be 10, got: %s", quota.Spec.Hard.Cpu().String())
	}

	// Verify limit range was created
	limitRange, err := client.CoreV1().LimitRanges("test-tenant").Get(ctx, "tenant-limits", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get limit range: %v", err)
	}

	if len(limitRange.Spec.Limits) == 0 {
		t.Error("Expected limit range to have limits")
	}

	// Verify roles were created
	roles, err := client.RbacV1().Roles("test-tenant").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("Failed to list roles: %v", err)
	}

	expectedRoles := []string{"tenant-admin", "tenant-developer", "tenant-viewer"}
	if len(roles.Items) != len(expectedRoles) {
		t.Errorf("Expected %d roles, got %d", len(expectedRoles), len(roles.Items))
	}

	// Verify role bindings were created
	bindings, err := client.RbacV1().RoleBindings("test-tenant").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("Failed to list role bindings: %v", err)
	}

	if len(bindings.Items) != 3 { // 1 admin + 1 dev + 1 viewer
		t.Errorf("Expected 3 role bindings, got %d", len(bindings.Items))
	}
}

func TestTenantManager_DeleteTenant(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	tm := NewTenantManager(client)

	// Create a tenant first
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-tenant",
			Labels: map[string]string{
				"adhar.io/tenant": "test-tenant",
			},
		},
	}
	_, err := client.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create test namespace: %v", err)
	}

	// Delete the tenant
	err = tm.DeleteTenant(ctx, "test-tenant")
	if err != nil {
		t.Fatalf("Failed to delete tenant: %v", err)
	}

	// Verify namespace was deleted
	_, err = client.CoreV1().Namespaces().Get(ctx, "test-tenant", metav1.GetOptions{})
	if err == nil {
		t.Error("Expected namespace to be deleted")
	}
}

func TestTenantManager_ListTenants(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	tm := NewTenantManager(client)

	// Create multiple tenants
	tenants := []string{"tenant-1", "tenant-2", "tenant-3"}
	for _, name := range tenants {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				Labels: map[string]string{
					"adhar.io/tenant": name,
				},
			},
		}
		_, err := client.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("Failed to create tenant %s: %v", name, err)
		}
	}

	// Create a non-tenant namespace
	regularNs := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "regular-namespace",
		},
	}
	_, err := client.CoreV1().Namespaces().Create(ctx, regularNs, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create regular namespace: %v", err)
	}

	// List tenants
	listed, err := tm.ListTenants(ctx)
	if err != nil {
		t.Fatalf("Failed to list tenants: %v", err)
	}

	if len(listed) != len(tenants) {
		t.Errorf("Expected %d tenants, got %d", len(tenants), len(listed))
	}

	// Verify all tenant names are present
	for _, tenant := range tenants {
		found := false
		for _, listed := range listed {
			if listed == tenant {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Tenant %s not found in list", tenant)
		}
	}
}

func TestTenantManager_GetTenantQuota(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	tm := NewTenantManager(client)

	config := TenantConfig{
		Name: "quota-test-tenant",
		ResourceQuotas: ResourceQuotaConfig{
			CPU:    "5",
			Memory: "10Gi",
		},
	}

	err := tm.CreateTenant(ctx, config)
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	quota, err := tm.GetTenantQuota(ctx, "quota-test-tenant")
	if err != nil {
		t.Fatalf("Failed to get tenant quota: %v", err)
	}

	if quota.Name != "tenant-quota" {
		t.Errorf("Expected quota name to be 'tenant-quota', got: %s", quota.Name)
	}

	if quota.Spec.Hard.Cpu().String() != "5" {
		t.Errorf("Expected CPU quota to be 5, got: %s", quota.Spec.Hard.Cpu().String())
	}
}

func TestTenantManager_UpdateTenantQuota(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	tm := NewTenantManager(client)

	// Create tenant with initial quotas
	config := TenantConfig{
		Name: "update-quota-tenant",
		ResourceQuotas: ResourceQuotaConfig{
			CPU:    "5",
			Memory: "10Gi",
		},
	}

	err := tm.CreateTenant(ctx, config)
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	// Update quotas
	newQuotas := ResourceQuotaConfig{
		CPU:    "10",
		Memory: "20Gi",
	}

	err = tm.UpdateTenantQuota(ctx, "update-quota-tenant", newQuotas)
	if err != nil {
		t.Fatalf("Failed to update tenant quota: %v", err)
	}

	// Verify quotas were updated
	quota, err := tm.GetTenantQuota(ctx, "update-quota-tenant")
	if err != nil {
		t.Fatalf("Failed to get updated quota: %v", err)
	}

	if quota.Spec.Hard.Cpu().String() != "10" {
		t.Errorf("Expected CPU quota to be 10, got: %s", quota.Spec.Hard.Cpu().String())
	}

	if quota.Spec.Hard.Memory().String() != "20Gi" {
		t.Errorf("Expected memory quota to be 20Gi, got: %s", quota.Spec.Hard.Memory().String())
	}
}

func TestTenantConfig_Validation(t *testing.T) {
	tests := []struct {
		name    string
		config  TenantConfig
		wantErr bool
	}{
		{
			name: "valid config",
			config: TenantConfig{
				Name:        "valid-tenant",
				DisplayName: "Valid Tenant",
				ResourceQuotas: ResourceQuotaConfig{
					CPU:    "10",
					Memory: "20Gi",
				},
			},
			wantErr: false,
		},
		{
			name: "valid config with all fields",
			config: TenantConfig{
				Name:        "complete-tenant",
				DisplayName: "Complete Tenant",
				ResourceQuotas: ResourceQuotaConfig{
					CPU:               "10",
					Memory:            "20Gi",
					Storage:           "100Gi",
					PersistentVolumes: 5,
					Services:          10,
					Pods:              50,
					Secrets:           100,
					ConfigMaps:        100,
				},
				Admins:                []string{"admin@example.com"},
				Developers:            []string{"dev@example.com"},
				Viewers:               []string{"viewer@example.com"},
				EnableNetworkPolicies: true,
			},
			wantErr: false,
		},
	}

	ctx := context.Background()
	client := fake.NewSimpleClientset()
	tm := NewTenantManager(client)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tm.CreateTenant(ctx, tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("CreateTenant() error = %v, wantErr %v", err, tt.wantErr)
			}

			// Cleanup
			if err == nil {
				tm.DeleteTenant(ctx, tt.config.Name)
			}
		})
	}
}
