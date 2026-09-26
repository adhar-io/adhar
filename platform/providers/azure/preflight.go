package azure

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"

	provider "adhar-io/adhar/platform/providers"
	"adhar-io/adhar/platform/types"
)

// requiredNamespaces are the resource providers a kubeadm-on-VMs cluster cannot be
// built without.
//
// A fresh subscription has these UNREGISTERED, and the failure that causes is one
// of the least helpful in Azure: the create fails with
// `MissingSubscriptionRegistration` naming the namespace, not the VM you asked for,
// and only after the resource group and network already exist. Measured on
// subscription b8e6d308 (2026-09-26): Microsoft.Compute and Microsoft.Storage were
// both NotRegistered while Microsoft.Network was fine.
var requiredNamespaces = []string{
	"Microsoft.Compute",
	"Microsoft.Network",
	"Microsoft.Storage",
}

// Preflight proves this subscription can build the cluster before anything exists.
//
// The checks are ordered by how early they would otherwise bite, and each one
// reports the remedy rather than only the symptom.
func (p *Provider) Preflight(ctx context.Context, spec *types.ClusterSpec) []provider.Check {
	var checks []provider.Check

	// 1. Can we talk to the subscription at all, and is the resource group there?
	//    Everything else is meaningless if not.
	if p.resourceGroupClient != nil && p.config.ResourceGroup != "" {
		if _, err := p.resourceGroupClient.Get(ctx, p.config.ResourceGroup, nil); err != nil {
			return append(checks, provider.Check{
				Name:   "resource group " + p.config.ResourceGroup,
				Status: provider.CheckFail,
				Detail: err.Error(),
				Fix: fmt.Sprintf("Create it, or point `resourceGroup` at one that exists: "+
					"az group create --name %s --location %s", p.config.ResourceGroup, p.config.Location),
			})
		}
		checks = append(checks, provider.Check{
			Name:   "resource group " + p.config.ResourceGroup,
			Status: provider.CheckPass,
			Detail: "exists and is readable",
		})
	}

	checks = append(checks, p.preflightNamespaces(ctx)...)
	checks = append(checks, p.preflightVMSize(ctx, spec))
	checks = append(checks, p.preflightQuota(ctx, spec))
	return checks
}

// preflightNamespaces reports any resource provider that is not registered.
//
// Registration is idempotent, takes a few minutes, and is nobody's idea of an
// interesting prerequisite — which is exactly why it should be checked rather than
// discovered halfway through a create.
func (p *Provider) preflightNamespaces(ctx context.Context) []provider.Check {
	if p.providersClient == nil {
		return nil
	}
	var out []provider.Check
	for _, ns := range requiredNamespaces {
		resp, err := p.providersClient.Get(ctx, ns, nil)
		if err != nil {
			out = append(out, provider.Check{
				Name:   "resource provider " + ns,
				Status: provider.CheckWarn,
				Detail: "could not read its registration state: " + err.Error(),
				Fix:    provider.ExplainAccessError(err),
			})
			continue
		}
		state := ""
		if resp.RegistrationState != nil {
			state = *resp.RegistrationState
		}
		switch {
		case strings.EqualFold(state, "Registered"):
			out = append(out, provider.Check{
				Name:   "resource provider " + ns,
				Status: provider.CheckPass,
				Detail: "Registered",
			})
		case strings.EqualFold(state, "Registering"):
			// Not a failure: it finishes on its own, usually within minutes.
			out = append(out, provider.Check{
				Name:   "resource provider " + ns,
				Status: provider.CheckWarn,
				Detail: "still Registering — it will finish on its own",
				Fix:    "Wait for it: az provider show -n " + ns + " --query registrationState",
			})
		default:
			out = append(out, provider.Check{
				Name:   "resource provider " + ns,
				Status: provider.CheckFail,
				Detail: fmt.Sprintf("%s — no resource of this type can be created", state),
				Fix:    "az provider register --namespace " + ns + "   (takes a few minutes)",
			})
		}
	}
	return out
}

// preflightVMSize catches a size that does not exist in this region — otherwise a
// create fails only when the first VM is requested, with the network already built.
func (p *Provider) preflightVMSize(ctx context.Context, spec *types.ClusterSpec) provider.Check {
	size := plannedVMSize(spec, p.config.VMSize)
	if size == "" {
		return provider.Check{
			Name:   "VM size",
			Status: provider.CheckWarn,
			Detail: "no size configured; the provider default will be used",
		}
	}
	cores := p.vmSizeCores(ctx, p.config.Location, size)
	if cores == 0 {
		return provider.Check{
			Name:   "VM size " + size,
			Status: provider.CheckFail,
			Detail: fmt.Sprintf("%s is not offered in %s", size, p.config.Location),
			Fix: fmt.Sprintf("Pick one this region has: "+
				"az vm list-skus --location %s --resource-type virtualMachines -o table",
				p.config.Location),
		}
	}

	// Supported is not the same as AVAILABLE.
	//
	// VirtualMachineSizes.List (above) reports what the region supports, and it
	// said Standard_D4s_v3 was fine in malaysiawest. The create then failed at the
	// first VM with
	//   RESPONSE 409 SkuNotAvailable: Following SKUs have failed for Capacity
	//   Restrictions: Standard_D4s_v3
	// after the network, security group, public IP and NIC already existed
	// (2026-09-26). Only ResourceSKUs carries the per-subscription restrictions
	// that cause that, so it is the list worth checking.
	if restriction := p.skuRestriction(ctx, size); restriction != "" {
		return provider.Check{
			Name:   "VM size " + size,
			Status: provider.CheckFail,
			Detail: fmt.Sprintf("%s is supported in %s but NOT available to this "+
				"subscription: %s", size, p.config.Location, restriction),
			Fix: fmt.Sprintf("Choose a size with no restrictions: "+
				"az vm list-skus --location %s --resource-type virtualMachines "+
				"--query \"[?!restrictions && capabilities[?name=='vCPUs' && value=='%d']].name\" -o tsv",
				p.config.Location, cores),
		}
	}

	return provider.Check{
		Name:   "VM size " + size,
		Status: provider.CheckPass,
		Detail: fmt.Sprintf("available to this subscription in %s, %d vCPU each",
			p.config.Location, cores),
	}
}

// skuRestriction returns a human description of why this subscription cannot
// launch a size in this location, or "" when it can.
//
// Restrictions are the difference between "the region offers this" and "you may
// use it", and they are the reason a create can fail on a size the sizes list
// happily reported.
func (p *Provider) skuRestriction(ctx context.Context, size string) string {
	if p.resourceSKUsClient == nil {
		return ""
	}
	filter := fmt.Sprintf("location eq '%s'", p.config.Location)
	pager := p.resourceSKUsClient.NewListPager(&armcompute.ResourceSKUsClientListOptions{
		Filter: &filter,
	})
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			// Unreadable is not a reason to block: the check below would then be
			// stricter than the create itself.
			return ""
		}
		for _, sku := range page.Value {
			if sku == nil || sku.Name == nil || !strings.EqualFold(*sku.Name, size) {
				continue
			}
			if sku.ResourceType == nil || !strings.EqualFold(*sku.ResourceType, "virtualMachines") {
				continue
			}
			var reasons []string
			for _, r := range sku.Restrictions {
				if r == nil {
					continue
				}
				reason := ""
				if r.ReasonCode != nil {
					reason = string(*r.ReasonCode)
				}
				scope := ""
				if r.Type != nil {
					scope = string(*r.Type)
				}
				if reason != "" || scope != "" {
					reasons = append(reasons, strings.TrimSpace(reason+" "+scope))
				}
			}
			if len(reasons) > 0 {
				return strings.Join(reasons, "; ")
			}
			return ""
		}
	}
	return ""
}

// preflightQuota compares the subscription's vCPU limits against the cluster at
// FULL size — control plane plus every node group's autoscaling MAXIMUM.
//
// checkQuota (called during the create) sizes against the STARTING replica count,
// which is the right question for "can I build this now". It is the wrong question
// for "will this platform work": the cluster boots on its floor, the catalogue needs
// more, the autoscaler asks, and the request is refused by a limit nobody checked.
// Azure enforces a per-family limit AND a region-wide total, and a create can fail
// on either, so both are reported.
func (p *Provider) preflightQuota(ctx context.Context, spec *types.ClusterSpec) provider.Check {
	size := plannedVMSize(spec, p.config.VMSize)
	cores := p.vmSizeCores(ctx, p.config.Location, size)
	if cores == 0 {
		// preflightVMSize already reported this; do not fail twice for one cause.
		return provider.Check{
			Name:   "vCPU quota",
			Status: provider.CheckWarn,
			Detail: "skipped: the VM size could not be resolved",
		}
	}
	nodes := maxNodeCount(spec)
	need := nodes * cores
	raise := fmt.Sprintf("Raise BOTH \"Total Regional vCPUs\" and \"%s\" for %s "+
		"(Azure portal → Subscriptions → Usage + quotas). Check with: "+
		"az vm list-usage --location %s -o table",
		vmSizeFamilyQuotaName(size), p.config.Location, p.config.Location)

	if p.usageClient == nil {
		return provider.QuotaCheck("vCPU quota", 0, float64(need), "vCPU", raise)
	}
	var usages []*armcompute.Usage
	pager := p.usageClient.NewListPager(p.config.Location, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			c := provider.QuotaCheck("vCPU quota", 0, float64(need), "vCPU", raise)
			c.Detail = "could not read quotas: " + err.Error()
			return c
		}
		usages = append(usages, page.Value...)
	}

	// The tightest of the two limits that apply is what actually stops a create.
	var limit int64 = -1
	var limiting string
	for _, u := range usages {
		if u == nil || u.Name == nil || u.Name.LocalizedValue == nil {
			continue
		}
		name := *u.Name.LocalizedValue
		relevant := strings.Contains(name, "Total Regional vCPUs") ||
			(vmSizeFamilyQuotaName(size) != "" && strings.EqualFold(name, vmSizeFamilyQuotaName(size)))
		if !relevant {
			continue
		}
		if u.Limit == nil {
			continue
		}
		if limit < 0 || *u.Limit < limit {
			limit = *u.Limit
			limiting = name
		}
	}
	if limit < 0 {
		return provider.QuotaCheck("vCPU quota", 0, float64(need), "vCPU", raise)
	}
	c := provider.QuotaCheck("vCPU quota", float64(limit), float64(need), "vCPU", raise)
	c.Name = "vCPU quota (" + limiting + ")"
	c.Detail = fmt.Sprintf("%s: limit %d, cluster needs %d at full size (%d × %s at %d vCPU)",
		limiting, limit, need, nodes, size, cores)
	if limit < int64(need) {
		c.Status = provider.CheckFail
	}
	return c
}

// maxNodeCount is the cluster at FULL size: the control plane plus each node
// group's autoscaling maximum, falling back to its replica count when autoscaling
// is not configured.
func maxNodeCount(spec *types.ClusterSpec) int {
	if spec == nil {
		return 0
	}
	masters := spec.ControlPlane.Replicas
	if masters <= 0 {
		masters = 1
	}
	workers := 0
	for _, ng := range spec.NodeGroups {
		n := ng.Replicas
		if m := ng.AutoScaling.MaxReplicas; m > n {
			n = m
		}
		if n > 0 {
			workers += n
		}
	}
	return masters + workers
}
