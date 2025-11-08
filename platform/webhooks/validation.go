package webhooks

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// ClusterValidator validates cluster composite resources
type ClusterValidator struct {
	decoder admission.Decoder
}

// ValidateCluster validates cluster specifications
func (v *ClusterValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	cluster := &unstructured.Unstructured{}

	err := v.decoder.Decode(req, cluster)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	// Extract spec
	spec, ok := cluster.Object["spec"].(map[string]interface{})
	if !ok {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("invalid spec"))
	}

	params, ok := spec["parameters"].(map[string]interface{})
	if !ok {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("invalid parameters"))
	}

	// Validate provider
	provider, ok := params["provider"].(string)
	if !ok || provider == "" {
		return admission.Denied("provider is required")
	}

	validProviders := []string{"AWS_EKS", "GCP_GKE", "AZURE_AKS", "DIGITALOCEAN_DOKS", "CIVO_K3S", "KIND"}
	if !contains(validProviders, provider) {
		return admission.Denied(fmt.Sprintf("invalid provider: %s. Must be one of: %s", provider, strings.Join(validProviders, ", ")))
	}

	// Validate region
	region, ok := params["region"].(string)
	if !ok || region == "" {
		return admission.Denied("region is required")
	}

	// Validate version
	version, ok := params["version"].(string)
	if !ok || version == "" {
		return admission.Denied("version is required")
	}

	// Validate node pools
	nodePools, ok := params["nodePools"].([]interface{})
	if !ok || len(nodePools) == 0 {
		return admission.Denied("at least one node pool is required")
	}

	for i, pool := range nodePools {
		poolMap, ok := pool.(map[string]interface{})
		if !ok {
			return admission.Denied(fmt.Sprintf("invalid node pool at index %d", i))
		}

		// Validate node pool name
		name, ok := poolMap["name"].(string)
		if !ok || name == "" {
			return admission.Denied(fmt.Sprintf("node pool at index %d must have a name", i))
		}

		// Validate instance type
		instanceType, ok := poolMap["instanceType"].(string)
		if !ok || instanceType == "" {
			return admission.Denied(fmt.Sprintf("node pool '%s' must have an instanceType", name))
		}

		// Validate count
		count, ok := poolMap["count"].(float64)
		if !ok || count < 1 {
			return admission.Denied(fmt.Sprintf("node pool '%s' must have a count >= 1", name))
		}

		// Validate min/max count if specified
		if minCount, ok := poolMap["minCount"].(float64); ok {
			if minCount < 1 {
				return admission.Denied(fmt.Sprintf("node pool '%s' minCount must be >= 1", name))
			}
			if minCount > count {
				return admission.Denied(fmt.Sprintf("node pool '%s' minCount cannot be greater than count", name))
			}
		}

		if maxCount, ok := poolMap["maxCount"].(float64); ok {
			if maxCount < count {
				return admission.Denied(fmt.Sprintf("node pool '%s' maxCount cannot be less than count", name))
			}
		}
	}

	// Provider-specific validation
	if providerSettings, ok := params["providerSettings"].(map[string]interface{}); ok {
		switch provider {
		case "AZURE_AKS":
			if azure, ok := providerSettings["azure"].(map[string]interface{}); ok {
				if _, ok := azure["resourceGroupName"].(string); !ok {
					return admission.Denied("Azure AKS requires providerSettings.azure.resourceGroupName")
				}
				if _, ok := azure["dnsPrefix"].(string); !ok {
					return admission.Denied("Azure AKS requires providerSettings.azure.dnsPrefix")
				}
			} else {
				return admission.Denied("Azure AKS requires providerSettings.azure configuration")
			}
		}
	}

	return admission.Allowed("cluster validation passed")
}

// DatabaseValidator validates database composite resources
type DatabaseValidator struct {
	decoder admission.Decoder
}

// ValidateDatabase validates database specifications
func (v *DatabaseValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	database := &unstructured.Unstructured{}

	err := v.decoder.Decode(req, database)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	spec, ok := database.Object["spec"].(map[string]interface{})
	if !ok {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("invalid spec"))
	}

	params, ok := spec["parameters"].(map[string]interface{})
	if !ok {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("invalid parameters"))
	}

	// Validate engine
	engine, ok := params["engine"].(string)
	if !ok || engine == "" {
		return admission.Denied("engine is required")
	}

	validEngines := []string{"postgresql", "mysql", "mariadb", "mongodb", "redis"}
	if !contains(validEngines, engine) {
		return admission.Denied(fmt.Sprintf("invalid engine: %s. Must be one of: %s", engine, strings.Join(validEngines, ", ")))
	}

	// Validate engine version
	engineVersion, ok := params["engineVersion"].(string)
	if !ok || engineVersion == "" {
		return admission.Denied("engineVersion is required")
	}

	// Validate storage size format
	if storageSize, ok := params["storageSize"].(string); ok {
		if !isValidStorageSize(storageSize) {
			return admission.Denied("storageSize must be in format like '20Gi', '100Gi', etc.")
		}
	}

	// Validate backup retention days
	if retentionDays, ok := params["backupRetentionDays"].(float64); ok {
		if retentionDays < 0 || retentionDays > 35 {
			return admission.Denied("backupRetentionDays must be between 0 and 35")
		}
	}

	// Validate credentials if master password is provided
	if credentials, ok := params["credentials"].(map[string]interface{}); ok {
		if passwordRef, ok := credentials["masterPasswordSecretRef"].(map[string]interface{}); ok {
			if _, ok := passwordRef["name"].(string); !ok {
				return admission.Denied("credentials.masterPasswordSecretRef.name is required")
			}
		}
	}

	return admission.Allowed("database validation passed")
}

// NetworkValidator validates network composite resources
type NetworkValidator struct {
	decoder admission.Decoder
}

// ValidateNetwork validates network specifications
func (v *NetworkValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	network := &unstructured.Unstructured{}

	err := v.decoder.Decode(req, network)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	spec, ok := network.Object["spec"].(map[string]interface{})
	if !ok {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("invalid spec"))
	}

	params, ok := spec["parameters"].(map[string]interface{})
	if !ok {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("invalid parameters"))
	}

	// Validate provider
	provider, ok := params["provider"].(string)
	if !ok || provider == "" {
		return admission.Denied("provider is required")
	}

	validProviders := []string{"AWS_VPC", "GCP_VPC", "AZURE_VNET", "DIGITALOCEAN_VPC", "CIVO_NETWORK"}
	if !contains(validProviders, provider) {
		return admission.Denied(fmt.Sprintf("invalid provider: %s. Must be one of: %s", provider, strings.Join(validProviders, ", ")))
	}

	// Validate CIDR block
	cidrBlock, ok := params["cidrBlock"].(string)
	if !ok || cidrBlock == "" {
		return admission.Denied("cidrBlock is required")
	}

	if !isValidCIDR(cidrBlock) {
		return admission.Denied(fmt.Sprintf("invalid CIDR block: %s", cidrBlock))
	}

	// Validate subnets
	subnets, ok := params["subnets"].([]interface{})
	if !ok || len(subnets) == 0 {
		return admission.Denied("at least one subnet is required")
	}

	for i, subnet := range subnets {
		subnetMap, ok := subnet.(map[string]interface{})
		if !ok {
			return admission.Denied(fmt.Sprintf("invalid subnet at index %d", i))
		}

		// Validate subnet name
		name, ok := subnetMap["name"].(string)
		if !ok || name == "" {
			return admission.Denied(fmt.Sprintf("subnet at index %d must have a name", i))
		}

		// Validate subnet CIDR
		subnetCIDR, ok := subnetMap["cidrBlock"].(string)
		if !ok || subnetCIDR == "" {
			return admission.Denied(fmt.Sprintf("subnet '%s' must have a cidrBlock", name))
		}

		if !isValidCIDR(subnetCIDR) {
			return admission.Denied(fmt.Sprintf("subnet '%s' has invalid CIDR block: %s", name, subnetCIDR))
		}

		// Validate subnet type
		if subnetType, ok := subnetMap["type"].(string); ok {
			validTypes := []string{"public", "private", "isolated"}
			if !contains(validTypes, subnetType) {
				return admission.Denied(fmt.Sprintf("subnet '%s' has invalid type: %s. Must be one of: %s", name, subnetType, strings.Join(validTypes, ", ")))
			}
		}

		// Validate availability zone
		if _, ok := subnetMap["availabilityZone"].(string); !ok {
			return admission.Denied(fmt.Sprintf("subnet '%s' must have an availabilityZone", name))
		}
	}

	return admission.Allowed("network validation passed")
}

// ApplicationValidator validates application composite resources
type ApplicationValidator struct {
	decoder admission.Decoder
}

// ValidateApplication validates application specifications
func (v *ApplicationValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	app := &unstructured.Unstructured{}

	err := v.decoder.Decode(req, app)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	spec, ok := app.Object["spec"].(map[string]interface{})
	if !ok {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("invalid spec"))
	}

	params, ok := spec["parameters"].(map[string]interface{})
	if !ok {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("invalid parameters"))
	}

	// Validate project
	project, ok := params["project"].(string)
	if !ok || project == "" {
		return admission.Denied("project is required")
	}

	// Validate source
	source, ok := params["source"].(map[string]interface{})
	if !ok {
		return admission.Denied("source is required")
	}

	repoURL, ok := source["repoURL"].(string)
	if !ok || repoURL == "" {
		return admission.Denied("source.repoURL is required")
	}

	// Validate source type (must have one of: path, chart)
	hasPath := false
	hasChart := false

	if path, ok := source["path"].(string); ok && path != "" {
		hasPath = true
	}

	if chart, ok := source["chart"].(string); ok && chart != "" {
		hasChart = true
	}

	if !hasPath && !hasChart {
		return admission.Denied("source must have either path or chart specified")
	}

	// Validate destination
	destination, ok := params["destination"].(map[string]interface{})
	if !ok {
		return admission.Denied("destination is required")
	}

	// Must have either server or name
	hasServer := false
	hasName := false

	if server, ok := destination["server"].(string); ok && server != "" {
		hasServer = true
	}

	if name, ok := destination["name"].(string); ok && name != "" {
		hasName = true
	}

	if !hasServer && !hasName {
		return admission.Denied("destination must have either server or name specified")
	}

	return admission.Allowed("application validation passed")
}

// Helper functions

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func isValidCIDR(cidr string) bool {
	cidrPattern := regexp.MustCompile(`^(\d{1,3}\.){3}\d{1,3}/\d{1,2}$`)
	return cidrPattern.MatchString(cidr)
}

func isValidStorageSize(size string) bool {
	sizePattern := regexp.MustCompile(`^\d+(Gi|Mi|Ti)$`)
	return sizePattern.MatchString(size)
}

// InjectDecoder injects the decoder into validators
func (v *ClusterValidator) InjectDecoder(d admission.Decoder) error {
	v.decoder = d
	return nil
}

func (v *DatabaseValidator) InjectDecoder(d admission.Decoder) error {
	v.decoder = d
	return nil
}

func (v *NetworkValidator) InjectDecoder(d admission.Decoder) error {
	v.decoder = d
	return nil
}

func (v *ApplicationValidator) InjectDecoder(d admission.Decoder) error {
	v.decoder = d
	return nil
}

var _ admission.Handler = &ClusterValidator{}
var _ admission.Handler = &DatabaseValidator{}
var _ admission.Handler = &NetworkValidator{}
var _ admission.Handler = &ApplicationValidator{}

// SetupWebhooks registers all validators with the webhook server
func SetupWebhooks(mgr runtime.Object) error {
	// This would be implemented in the actual webhook server setup
	// For now, this is a placeholder to show the structure
	return nil
}
