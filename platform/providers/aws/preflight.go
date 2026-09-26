package aws

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"

	provider "adhar-io/adhar/platform/providers"
	"adhar-io/adhar/platform/types"
)

// vCPUQuotaCode is "Running On-Demand Standard (A, C, D, H, I, M, R, T, Z)
// instances". It counts vCPUs, not instances, and it is the limit a kubeadm
// cluster of this shape actually hits.
const vCPUQuotaCode = "L-1216C47A"

// Preflight proves this account can build the cluster before anything is created.
//
// It probes READS AND WRITES. Reads alone are not enough, and writes alone are
// worse: on the account this was written against, an Organizations SCP allowed
// `ec2:RunInstances` while denying `ec2:DescribeImages` and `ec2:DescribeSubnets`,
// so a write-only probe reported a healthy account that could not resolve an AMI
// or find a subnet. Every write below uses DryRun, so nothing is created.
func (p *Provider) Preflight(ctx context.Context, spec *types.ClusterSpec) []provider.Check {
	var checks []provider.Check

	// NOTE: identity is not reported here — this Provider holds no STS client. The
	// permission results below are what actually gate a create anyway.

	// 2. The read calls a create cannot start without.
	reads := []struct {
		action string
		call   func() error
	}{
		{"ec2:DescribeAvailabilityZones", func() error {
			_, err := p.ec2Client.DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{})
			return err
		}},
		{"ec2:DescribeImages", func() error {
			_, err := p.ec2Client.DescribeImages(ctx, &ec2.DescribeImagesInput{
				Owners:     []string{canonicalOwnerID},
				Filters:    []ec2types.Filter{{Name: aws.String("state"), Values: []string{"available"}}},
				MaxResults: aws.Int32(5),
			})
			return err
		}},
		{"ec2:DescribeVpcs", func() error {
			_, err := p.ec2Client.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{MaxResults: aws.Int32(5)})
			return err
		}},
		{"ec2:DescribeSubnets", func() error {
			_, err := p.ec2Client.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{MaxResults: aws.Int32(5)})
			return err
		}},
		{"ec2:DescribeSecurityGroups", func() error {
			_, err := p.ec2Client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{MaxResults: aws.Int32(5)})
			return err
		}},
		{"ec2:DescribeRouteTables", func() error {
			_, err := p.ec2Client.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{MaxResults: aws.Int32(5)})
			return err
		}},
		{"ec2:DescribeKeyPairs", func() error {
			_, err := p.ec2Client.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{})
			return err
		}},
	}
	var denied []string
	var scpDenied bool
	for _, r := range reads {
		if err := r.call(); err != nil {
			denied = append(denied, r.action)
			if strings.Contains(err.Error(), "explicit deny in a service control policy") {
				scpDenied = true
			}
		}
	}

	// 3. The write calls, as dry-runs. AWS answers DryRunOperation when the call
	//    WOULD have been permitted, and UnauthorizedOperation when it would not, so
	//    this tests authorisation without creating anything.
	writes := []struct {
		action string
		call   func() error
	}{
		{"ec2:CreateVpc", func() error {
			_, err := p.ec2Client.CreateVpc(ctx, &ec2.CreateVpcInput{
				CidrBlock: aws.String("10.255.255.0/28"), DryRun: aws.Bool(true)})
			return err
		}},
		{"ec2:CreateSecurityGroup", func() error {
			_, err := p.ec2Client.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
				GroupName: aws.String("adhar-preflight"), Description: aws.String("preflight"),
				DryRun: aws.Bool(true)})
			return err
		}},
		{"ec2:CreateInternetGateway", func() error {
			_, err := p.ec2Client.CreateInternetGateway(ctx, &ec2.CreateInternetGatewayInput{
				DryRun: aws.Bool(true)})
			return err
		}},
		{"ec2:ImportKeyPair", func() error {
			_, err := p.ec2Client.ImportKeyPair(ctx, &ec2.ImportKeyPairInput{
				KeyName: aws.String("adhar-preflight"), PublicKeyMaterial: []byte("ssh-ed25519 AAAA preflight"),
				DryRun: aws.Bool(true)})
			return err
		}},
		{"ec2:RunInstances", func() error {
			_, err := p.ec2Client.RunInstances(ctx, &ec2.RunInstancesInput{
				MinCount: aws.Int32(1), MaxCount: aws.Int32(1),
				ImageId: aws.String("ami-00000000000000000"), DryRun: aws.Bool(true)})
			return err
		}},
		{"ec2:CreateVolume", func() error {
			_, err := p.ec2Client.CreateVolume(ctx, &ec2.CreateVolumeInput{
				AvailabilityZone: aws.String(p.config.Region + "a"), Size: aws.Int32(1),
				DryRun: aws.Bool(true)})
			return err
		}},
	}
	for _, w := range writes {
		err := w.call()
		if err == nil || isDryRunSuccess(err) {
			continue
		}
		// A malformed-parameter complaint means authorisation already passed.
		if isParameterComplaint(err) {
			continue
		}
		denied = append(denied, w.action)
		if strings.Contains(err.Error(), "explicit deny in a service control policy") {
			scpDenied = true
		}
	}

	if len(denied) > 0 {
		fix := "Add these to the provisioning policy; docs/AWS_PROVIDER.md §1 has the full document."
		if scpDenied {
			fix = "At least one of these is denied by an AWS Organizations SCP, which NO IAM " +
				"policy in this account can override — attaching AdministratorAccess will not " +
				"help. Amend the SCP from the organization's MANAGEMENT account, then re-test " +
				"repeatedly: while a policy is being edited, calls pass briefly and then go " +
				"back to denied."
		}
		checks = append(checks, provider.Check{
			Name:   "EC2 permissions",
			Status: provider.CheckFail,
			Detail: fmt.Sprintf("%d of %d actions denied: %s",
				len(denied), len(reads)+len(writes), strings.Join(denied, ", ")),
			Fix: fix,
		})
	} else {
		checks = append(checks, provider.Check{
			Name:   "EC2 permissions",
			Status: provider.CheckPass,
			Detail: fmt.Sprintf("all %d required actions permitted (reads and dry-run writes)",
				len(reads)+len(writes)),
		})
	}

	checks = append(checks, p.preflightQuota(ctx, spec))
	if c, ok := p.preflightInstanceType(ctx, spec); ok {
		checks = append(checks, c)
	}
	if c, ok := p.preflightLoadBalancer(ctx); ok {
		checks = append(checks, c)
	}
	return checks
}

// preflightQuota compares the region's vCPU limit against the cluster at FULL
// size. Sizing to the starting node count is the trap: the cluster boots, then the
// autoscaler tries to buy what the platform actually needs and cannot.
func (p *Provider) preflightQuota(ctx context.Context, spec *types.ClusterSpec) provider.Check {
	need := float64(neededVCPUs(spec))
	raise := fmt.Sprintf("Request an increase for %s (Running On-Demand Standard instances) "+
		"in %s. It is a support case, not instant.", vCPUQuotaCode, p.config.Region)

	if p.quotaClient == nil {
		return provider.QuotaCheck("EC2 vCPU quota", 0, need, "vCPU", raise)
	}
	out, err := p.quotaClient.GetServiceQuota(ctx, &servicequotas.GetServiceQuotaInput{
		ServiceCode: aws.String("ec2"), QuotaCode: aws.String(vCPUQuotaCode)})
	if err != nil {
		c := provider.QuotaCheck("EC2 vCPU quota", 0, need, "vCPU", raise)
		c.Detail = "could not read the limit: " + err.Error()
		if fix := provider.ExplainAccessError(err); fix != "" {
			c.Fix = fix
		}
		return c
	}
	limit := 0.0
	if out.Quota != nil && out.Quota.Value != nil {
		limit = *out.Quota.Value
	}
	return provider.QuotaCheck("EC2 vCPU quota", limit, need, "vCPU", raise)
}

// preflightInstanceType catches the machine type that does not exist, or is not
// offered in this region — a create that otherwise fails only once the first node
// is launched, after the VPC and everything else already exists.
func (p *Provider) preflightInstanceType(ctx context.Context, spec *types.ClusterSpec) (provider.Check, bool) {
	it := instanceTypeFor(spec)
	if it == "" {
		return provider.Check{}, false
	}
	out, err := p.ec2Client.DescribeInstanceTypeOfferings(ctx, &ec2.DescribeInstanceTypeOfferingsInput{
		LocationType: ec2types.LocationTypeAvailabilityZone,
		Filters: []ec2types.Filter{
			{Name: aws.String("instance-type"), Values: []string{it}},
		},
	})
	if err != nil {
		return provider.Check{
			Name:   "instance type " + it,
			Status: provider.CheckWarn,
			Detail: "could not confirm availability: " + err.Error(),
			Fix:    provider.ExplainAccessError(err),
		}, true
	}
	if len(out.InstanceTypeOfferings) == 0 {
		return provider.Check{
			Name:   "instance type " + it,
			Status: provider.CheckFail,
			Detail: fmt.Sprintf("%s is not offered in any availability zone of %s", it, p.config.Region),
			Fix:    "Pick a type this region offers, or move the environment to a region that has it.",
		}, true
	}
	zones := make([]string, 0, len(out.InstanceTypeOfferings))
	for _, o := range out.InstanceTypeOfferings {
		zones = append(zones, aws.ToString(o.Location))
	}
	return provider.Check{
		Name:   "instance type " + it,
		Status: provider.CheckPass,
		Detail: fmt.Sprintf("offered in %d zone(s): %s", len(zones), strings.Join(zones, ", ")),
	}, true
}

// preflightLoadBalancer checks the ELB access teardown needs. The in-cluster cloud
// controller manager creates the platform's load balancer, so `adhar up` never calls
// ELB — but `adhar down` must, and discovering that at teardown means the load
// balancer survives and keeps billing.
func (p *Provider) preflightLoadBalancer(ctx context.Context) (provider.Check, bool) {
	if p.elbv2Client == nil {
		return provider.Check{}, false
	}
	_, err := p.elbv2Client.DescribeLoadBalancers(ctx,
		&elasticloadbalancingv2.DescribeLoadBalancersInput{PageSize: aws.Int32(1)})
	if err != nil {
		return provider.Check{
			Name:   "ELB access (needed by teardown)",
			Status: provider.CheckWarn,
			Detail: err.Error(),
			Fix: "Without this, `adhar down` cannot remove the load balancer the " +
				"in-cluster controller creates, and it keeps billing after teardown. " +
				provider.ExplainAccessError(err),
		}, true
	}
	return provider.Check{
		Name:   "ELB access (needed by teardown)",
		Status: provider.CheckPass,
		Detail: "load balancers are listable",
	}, true
}

// isDryRunSuccess reports AWS's way of saying "this would have been allowed".
func isDryRunSuccess(err error) bool {
	return err != nil && strings.Contains(err.Error(), "DryRunOperation")
}

// isParameterComplaint reports an error about the REQUEST rather than the caller's
// rights — a made-up AMI id, for instance. Authorisation already succeeded, so for
// a permission probe it counts as a pass.
func isParameterComplaint(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, s := range []string{
		"InvalidAMIID", "InvalidParameterValue", "InvalidParameterCombination",
		"MissingParameter", "InvalidVpcID", "InvalidSubnetID", "InvalidGroup",
		"InvalidKeyPair", "InvalidRouteTableID", "InvalidInstanceID",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// instanceTypeFor returns the machine type the cluster will actually launch,
// preferring the worker node group because that is where the count multiplies.
func instanceTypeFor(spec *types.ClusterSpec) string {
	if spec == nil {
		return ""
	}
	for _, ng := range spec.NodeGroups {
		if ng.InstanceType != "" {
			return ng.InstanceType
		}
	}
	return spec.ControlPlane.InstanceType
}

// neededVCPUs is the cluster's vCPU appetite at FULL size: the control plane plus
// every node group at its autoscaling MAXIMUM, not at its starting replica count.
//
// Sizing against the starting count is how a cluster boots fine and then cannot
// grow: the platform's catalogue needs more capacity than a minimal floor provides,
// the autoscaler asks for it, and the request is refused by a quota nobody checked.
func neededVCPUs(spec *types.ClusterSpec) int {
	if spec == nil {
		return 0
	}
	total := 0
	cpRep := spec.ControlPlane.Replicas
	if cpRep == 0 {
		cpRep = 1
	}
	total += cpRep * vcpusForInstanceType(spec.ControlPlane.InstanceType)

	for _, ng := range spec.NodeGroups {
		count := ng.Replicas
		// AutoScalingSpec carries MaxReplicas; a zero means autoscaling was not
		// configured for this group, so the replica count stands.
		if m := ng.AutoScaling.MaxReplicas; m > count {
			count = m
		}
		if count == 0 {
			count = 1
		}
		total += count * vcpusForInstanceType(ng.InstanceType)
	}
	return total
}

// vcpusForInstanceType reads the vCPU count out of an EC2 type name.
//
// Parsed rather than looked up on purpose: this runs BEFORE permissions are
// confirmed, and DescribeInstanceTypes is one of the calls that may be denied — a
// quota check that needs the permission it is trying to warn about is no use. AWS
// type names are regular enough for this: the size suffix maps to a vCPU count, and
// `<n>xlarge` is n × 4.
func vcpusForInstanceType(t string) int {
	if t == "" {
		return 2 // a conservative floor rather than zero, so the total is never 0
	}
	size := t
	if i := strings.LastIndex(t, "."); i >= 0 {
		size = t[i+1:]
	}
	switch size {
	case "nano", "micro", "small":
		return 1
	case "medium":
		return 1
	case "large":
		return 2
	case "xlarge":
		return 4
	case "metal", "metal-16xl", "metal-24xl", "metal-32xl", "metal-48xl":
		return 96
	}
	if strings.HasSuffix(size, "xlarge") {
		n := 0
		if _, err := fmt.Sscanf(size, "%dxlarge", &n); err == nil && n > 0 {
			return n * 4
		}
	}
	return 2
}
