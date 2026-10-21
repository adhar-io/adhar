package azure

import provider "adhar-io/adhar/platform/providers"

// Day-2 control-plane steps (the API server's OIDC trust) need it.
var _ provider.ComputeSSHUserProvider = (*Provider)(nil)

// ComputeSSHUser is the login user on this provider's compute-mode instances
// (provider.ComputeSSHUserProvider), for day-2 control-plane steps.
func (p *Provider) ComputeSSHUser() string { return azureSSHUser }
