package config

// ToProviderMap converts a ConfigProviderConfig to a map[string]interface{}
// that includes all authentication fields and the nested config section
func (c *ConfigProviderConfig) ToProviderMap() map[string]interface{} {
	result := make(map[string]interface{})

	// Add all authentication and provider-level fields
	result["type"] = c.Type
	result["region"] = c.Region
	result["primary"] = c.Primary

	// Common authentication fields
	if c.CredentialsFile != "" {
		result["credentials_file"] = c.CredentialsFile
	}
	result["useEnvironment"] = c.UseEnvironment

	// AWS authentication
	if c.AccessKeyID != "" {
		result["accessKeyId"] = c.AccessKeyID
	}
	if c.SecretAccessKey != "" {
		result["secretAccessKey"] = c.SecretAccessKey
	}
	if c.SessionToken != "" {
		result["sessionToken"] = c.SessionToken
	}
	if c.Profile != "" {
		result["profile"] = c.Profile
	}
	result["useInstanceRole"] = c.UseInstanceRole

	// Azure authentication
	if c.ClientID != "" {
		result["clientId"] = c.ClientID
	}
	if c.ClientSecret != "" {
		result["clientSecret"] = c.ClientSecret
	}
	if c.TenantID != "" {
		result["tenantId"] = c.TenantID
	}
	if c.CertificatePath != "" {
		result["certificatePath"] = c.CertificatePath
	}
	result["useManagedIdentity"] = c.UseManagedIdentity
	result["useAzureCLI"] = c.UseAzureCLI

	// GCP authentication
	if c.ProjectID != "" {
		result["projectId"] = c.ProjectID
	}
	if c.ServiceAccountKeyFile != "" {
		result["serviceAccountKeyFile"] = c.ServiceAccountKeyFile
	}
	if c.ServiceAccountKey != "" {
		result["serviceAccountKey"] = c.ServiceAccountKey
	}
	if c.ImpersonateServiceAccount != "" {
		result["impersonateServiceAccount"] = c.ImpersonateServiceAccount
	}
	result["useApplicationDefault"] = c.UseApplicationDefault
	result["useComputeMetadata"] = c.UseComputeMetadata

	// DigitalOcean & Civo authentication
	if c.Token != "" {
		result["token"] = c.Token
	}

	// Add the nested config section
	if c.Config != nil {
		result["config"] = c.Config
	}

	return result
}
