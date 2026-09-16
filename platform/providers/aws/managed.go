package aws

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	provider "adhar-io/adhar/platform/providers"
	"adhar-io/adhar/platform/types"
)

// Managed mode: Amazon EKS. The default (`clusterMode: compute`) provisions
// Kubernetes with kubeadm on EC2; `useManagedK8s: true` (or
// `clusterMode: eks`) hands the control plane to EKS. The VPC, subnets and
// security group come from the same helpers as compute mode, so `adhar down`
// tears both modes down through the same tag-based cleanup.

const (
	clusterModeEKS      = "eks"
	eksPollInterval     = 20 * time.Second
	eksCreateTimeout    = 30 * time.Minute
	eksDefaultNodeType  = "t3.large"
	eksDefaultNodeCount = 2
)

// isManagedMode reports whether the provider config selects EKS.
func (p *Provider) isManagedMode() bool {
	return provider.ClusterModeIsManaged(p.config.ClusterMode, clusterModeEKS)
}

// isManagedCluster probes EKS for the cluster so a compute-mode config can
// still operate on (and tear down) a cluster created in managed mode.
func (p *Provider) isManagedCluster(ctx context.Context, clusterID string) bool {
	if p.eksClient == nil {
		return false
	}
	_, err := p.describeEKS(ctx, extractClusterName(clusterID))
	return err == nil
}

func (p *Provider) describeEKS(ctx context.Context, name string) (*ekstypes.Cluster, error) {
	out, err := p.eksClient.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String(name)})
	if err != nil {
		return nil, err
	}
	return out.Cluster, nil
}

func isEKSNotFound(err error) bool {
	var nf *ekstypes.ResourceNotFoundException
	return errors.As(err, &nf)
}

// eksRoleNames returns the IAM role names for the control plane and the nodes.
func eksRoleNames(clusterName string) (clusterRole, nodeRole string) {
	return "adhar-" + clusterName + "-eks-cluster", "adhar-" + clusterName + "-eks-node"
}

var (
	eksClusterPolicies = []string{"arn:aws:iam::aws:policy/AmazonEKSClusterPolicy"}
	eksNodePolicies    = []string{
		"arn:aws:iam::aws:policy/AmazonEKSWorkerNodePolicy",
		"arn:aws:iam::aws:policy/AmazonEKS_CNI_Policy",
		"arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly",
		"arn:aws:iam::aws:policy/service-role/AmazonEBSCSIDriverPolicy",
	}
)

// ensureIAMRole creates (once) an IAM role trusted by service with the given
// managed policies attached, returning its ARN.
func (p *Provider) ensureIAMRole(ctx context.Context, name, service string, policies []string, clusterName string) (string, error) {
	trust := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"%s"},"Action":"sts:AssumeRole"}]}`, service)
	var arn string
	got, err := p.iamClient.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(name)})
	switch {
	case err == nil:
		arn = aws.ToString(got.Role.Arn)
	default:
		var nse *iamtypes.NoSuchEntityException
		if !errors.As(err, &nse) {
			return "", fmt.Errorf("IAM role %s: %w", name, err)
		}
		created, err := p.iamClient.CreateRole(ctx, &iam.CreateRoleInput{
			RoleName:                 aws.String(name),
			AssumeRolePolicyDocument: aws.String(trust),
			Tags: []iamtypes.Tag{
				{Key: aws.String("Cluster"), Value: aws.String(clusterName)},
				{Key: aws.String("ManagedBy"), Value: aws.String("adhar")},
			},
		})
		if err != nil {
			return "", fmt.Errorf("creating IAM role %s: %w", name, err)
		}
		arn = aws.ToString(created.Role.Arn)
	}
	for _, policy := range policies {
		if _, err := p.iamClient.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{RoleName: aws.String(name), PolicyArn: aws.String(policy)}); err != nil {
			return "", fmt.Errorf("attaching %s to %s: %w", policy, name, err)
		}
	}
	return arn, nil
}

// deleteIAMRole detaches every managed policy and deletes the role; a missing
// role is not an error.
func (p *Provider) deleteIAMRole(ctx context.Context, name string) error {
	attached, err := p.iamClient.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{RoleName: aws.String(name)})
	if err != nil {
		var nse *iamtypes.NoSuchEntityException
		if errors.As(err, &nse) {
			return nil
		}
		return err
	}
	for _, pol := range attached.AttachedPolicies {
		if _, err := p.iamClient.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{RoleName: aws.String(name), PolicyArn: pol.PolicyArn}); err != nil {
			return err
		}
	}
	_, err = p.iamClient.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(name)})
	return err
}

// waitEKSCluster polls until the cluster reaches status (or disappears when
// status is empty).
func (p *Provider) waitEKSCluster(ctx context.Context, name string, status ekstypes.ClusterStatus, timeout time.Duration) (*ekstypes.Cluster, error) {
	deadline := time.Now().Add(timeout)
	for {
		c, err := p.describeEKS(ctx, name)
		if err != nil {
			if status == "" && isEKSNotFound(err) {
				return nil, nil
			}
			if !isEKSNotFound(err) {
				return nil, err
			}
		} else {
			if c.Status == ekstypes.ClusterStatusFailed {
				return nil, fmt.Errorf("EKS cluster %s entered FAILED", name)
			}
			if status != "" && c.Status == status {
				return c, nil
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for EKS cluster %s to reach %q", name, status)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(eksPollInterval):
		}
	}
}

// waitEKSNodegroup polls until the node group is ACTIVE (or gone when
// status is empty).
func (p *Provider) waitEKSNodegroup(ctx context.Context, cluster, name string, status ekstypes.NodegroupStatus, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		out, err := p.eksClient.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{ClusterName: aws.String(cluster), NodegroupName: aws.String(name)})
		if err != nil {
			if status == "" && isEKSNotFound(err) {
				return nil
			}
			if !isEKSNotFound(err) {
				return err
			}
		} else {
			switch out.Nodegroup.Status {
			case ekstypes.NodegroupStatusCreateFailed, ekstypes.NodegroupStatusDegraded, ekstypes.NodegroupStatusDeleteFailed:
				return fmt.Errorf("EKS node group %s/%s is %s", cluster, name, out.Nodegroup.Status)
			case status:
				if status != "" {
					return nil
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for EKS node group %s/%s to reach %q", cluster, name, status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(eksPollInterval):
		}
	}
}

// clusterSubnets finds the cluster's tagged subnets (public first).
func (p *Provider) clusterSubnets(ctx context.Context, clusterName string) (public, private string, err error) {
	out, err := p.ec2Client.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{Filters: []ec2types.Filter{
		{Name: aws.String("tag:Cluster"), Values: []string{clusterName}},
	}})
	if err != nil {
		return "", "", err
	}
	for _, sn := range out.Subnets {
		for _, t := range sn.Tags {
			if aws.ToString(t.Key) == "Type" {
				switch aws.ToString(t.Value) {
				case "public":
					public = aws.ToString(sn.SubnetId)
				case "private":
					private = aws.ToString(sn.SubnetId)
				}
			}
		}
	}
	if public == "" {
		return "", "", fmt.Errorf("no public subnet tagged Cluster=%s", clusterName)
	}
	return public, private, nil
}

// createManagedCluster provisions the network, IAM roles, the EKS control
// plane, one managed node group per spec node group and the EBS CSI addon.
// Every step is idempotent so a failed run can be retried.
func (p *Provider) createManagedCluster(ctx context.Context, spec *types.ClusterSpec) (*types.Cluster, error) {
	name := spec.Name
	fmt.Printf("🚀 Creating managed EKS cluster '%s' in %s...\n", name, p.config.Region)

	vpcID, err := p.createVPCForCluster(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("failed to create VPC: %w", err)
	}
	publicSubnet, privateSubnet, err := p.createSubnets(ctx, vpcID, name)
	if err != nil {
		return nil, fmt.Errorf("failed to create subnets: %w", err)
	}
	sgID, err := p.createSecurityGroups(ctx, vpcID, name)
	if err != nil {
		return nil, fmt.Errorf("failed to create security groups: %w", err)
	}
	clusterRoleName, nodeRoleName := eksRoleNames(name)
	clusterRole, err := p.ensureIAMRole(ctx, clusterRoleName, "eks.amazonaws.com", eksClusterPolicies, name)
	if err != nil {
		return nil, err
	}
	nodeRole, err := p.ensureIAMRole(ctx, nodeRoleName, "ec2.amazonaws.com", eksNodePolicies, name)
	if err != nil {
		return nil, err
	}
	// IAM is eventually consistent: a role created a moment ago is not always
	// assumable by EKS yet.
	time.Sleep(10 * time.Second)

	existing, err := p.describeEKS(ctx, name)
	switch {
	case err == nil:
		log.Printf("EKS cluster %s already exists (%s); reusing", name, existing.Status)
	case isEKSNotFound(err):
		in := &eks.CreateClusterInput{
			Name:    aws.String(name),
			RoleArn: aws.String(clusterRole),
			ResourcesVpcConfig: &ekstypes.VpcConfigRequest{
				SubnetIds:             []string{publicSubnet, privateSubnet},
				SecurityGroupIds:      []string{sgID},
				EndpointPublicAccess:  aws.Bool(true),
				EndpointPrivateAccess: aws.Bool(true),
			},
			AccessConfig: &ekstypes.CreateAccessConfigRequest{AuthenticationMode: ekstypes.AuthenticationModeApiAndConfigMap},
			Tags:         map[string]string{"Cluster": name, "ManagedBy": "adhar"},
		}
		if v := provider.ManagedVersion(spec.Version); v != "" {
			in.Version = aws.String(v)
		}
		if _, err := p.eksClient.CreateCluster(ctx, in); err != nil {
			return nil, fmt.Errorf("creating EKS cluster %s: %w", name, err)
		}
	default:
		return nil, fmt.Errorf("describing EKS cluster %s: %w", name, err)
	}
	fmt.Printf("⏳ Waiting for the EKS control plane to become ACTIVE (this takes ~10 minutes)...\n")
	c, err := p.waitEKSCluster(ctx, name, ekstypes.ClusterStatusActive, eksCreateTimeout)
	if err != nil {
		return nil, err
	}

	groups := spec.NodeGroups
	if len(groups) == 0 {
		groups = []types.NodeGroupSpec{{Name: "workers", Replicas: eksDefaultNodeCount, InstanceType: eksDefaultNodeType}}
	}
	for _, ng := range groups {
		if err := p.ensureEKSNodegroup(ctx, name, nodeRole, publicSubnet, &ng); err != nil {
			return nil, err
		}
	}

	// The EBS CSI driver as an EKS addon: the node role carries
	// AmazonEBSCSIDriverPolicy, so no IRSA wiring is needed for it to work.
	if _, err := p.eksClient.CreateAddon(ctx, &eks.CreateAddonInput{
		ClusterName: aws.String(name), AddonName: aws.String("aws-ebs-csi-driver"),
		ResolveConflicts: ekstypes.ResolveConflictsOverwrite,
	}); err != nil {
		var inUse *ekstypes.ResourceInUseException
		if !errors.As(err, &inUse) {
			log.Printf("Warning: EBS CSI addon: %v", err)
		}
	}

	cluster := p.eksToCluster(c)
	cluster.Tags = spec.Tags
	fmt.Printf("✅ EKS cluster %s is ACTIVE at %s\n", name, cluster.Endpoint)
	return cluster, nil
}

// ensureEKSNodegroup creates a managed node group when it does not exist and
// waits for it to be ACTIVE.
func (p *Provider) ensureEKSNodegroup(ctx context.Context, clusterName, nodeRole, subnet string, ng *types.NodeGroupSpec) error {
	_, err := p.eksClient.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{ClusterName: aws.String(clusterName), NodegroupName: aws.String(ng.Name)})
	if err == nil {
		return p.waitEKSNodegroup(ctx, clusterName, ng.Name, ekstypes.NodegroupStatusActive, eksCreateTimeout)
	}
	if !isEKSNotFound(err) {
		return err
	}
	replicas := ng.Replicas
	if replicas <= 0 {
		replicas = eksDefaultNodeCount
	}
	minSize, maxSize := replicas, replicas
	if ng.AutoScaling.MaxReplicas > 0 {
		minSize, maxSize = ng.AutoScaling.MinReplicas, ng.AutoScaling.MaxReplicas
		if minSize <= 0 {
			minSize = 1
		}
		if replicas < minSize {
			replicas = minSize
		}
		if replicas > maxSize {
			replicas = maxSize
		}
	}
	instanceType := ng.InstanceType
	if instanceType == "" {
		instanceType = eksDefaultNodeType
	}
	in := &eks.CreateNodegroupInput{
		ClusterName:   aws.String(clusterName),
		NodegroupName: aws.String(ng.Name),
		NodeRole:      aws.String(nodeRole),
		Subnets:       []string{subnet},
		InstanceTypes: []string{instanceType},
		ScalingConfig: &ekstypes.NodegroupScalingConfig{MinSize: aws.Int32(int32(minSize)), MaxSize: aws.Int32(int32(maxSize)), DesiredSize: aws.Int32(int32(replicas))},
		Labels:        ng.Labels,
		Tags:          map[string]string{"Cluster": clusterName, "ManagedBy": "adhar", "NodeGroup": ng.Name},
	}
	for _, t := range ng.Taints {
		in.Taints = append(in.Taints, ekstypes.Taint{Key: aws.String(t.Key), Value: aws.String(t.Value), Effect: ekstypes.TaintEffect(strings.ToUpper(strings.ReplaceAll(t.Effect, "NoSchedule", "NO_SCHEDULE")))})
	}
	if _, err := p.eksClient.CreateNodegroup(ctx, in); err != nil {
		return fmt.Errorf("creating EKS node group %s: %w", ng.Name, err)
	}
	fmt.Printf("⏳ Waiting for node group %s (%d × %s)...\n", ng.Name, replicas, instanceType)
	return p.waitEKSNodegroup(ctx, clusterName, ng.Name, ekstypes.NodegroupStatusActive, eksCreateTimeout)
}

func (p *Provider) eksToCluster(c *ekstypes.Cluster) *types.Cluster {
	status := types.ClusterStatusUnknown
	switch c.Status {
	case ekstypes.ClusterStatusActive:
		status = types.ClusterStatusRunning
	case ekstypes.ClusterStatusCreating, ekstypes.ClusterStatusPending:
		status = types.ClusterStatusCreating
	case ekstypes.ClusterStatusUpdating:
		status = types.ClusterStatusUpdating
	case ekstypes.ClusterStatusDeleting:
		status = types.ClusterStatusDeleting
	case ekstypes.ClusterStatusFailed:
		status = types.ClusterStatusError
	}
	created := time.Now()
	if c.CreatedAt != nil {
		created = *c.CreatedAt
	}
	version := aws.ToString(c.Version)
	if version != "" && !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	meta := map[string]interface{}{"mode": clusterModeEKS, "arn": aws.ToString(c.Arn)}
	if c.ResourcesVpcConfig != nil {
		meta["vpcId"] = aws.ToString(c.ResourcesVpcConfig.VpcId)
	}
	return &types.Cluster{
		ID:        fmt.Sprintf("aws-%s", aws.ToString(c.Name)),
		Name:      aws.ToString(c.Name),
		Provider:  "aws",
		Region:    p.config.Region,
		Version:   version,
		Status:    status,
		Endpoint:  aws.ToString(c.Endpoint),
		CreatedAt: created,
		UpdatedAt: time.Now(),
		Tags:      c.Tags,
		Metadata:  meta,
	}
}

// managedKubeconfig renders the EKS kubeconfig: the CA and endpoint from the
// API, credentials through `aws eks get-token` (the AWS CLI must be on PATH
// wherever the kubeconfig is used, including on the machine running `adhar up`).
func (p *Provider) managedKubeconfig(ctx context.Context, name string) (string, error) {
	c, err := p.describeEKS(ctx, name)
	if err != nil {
		return "", fmt.Errorf("describing EKS cluster %s: %w", name, err)
	}
	if c.CertificateAuthority == nil || c.Endpoint == nil {
		return "", fmt.Errorf("EKS cluster %s has no endpoint yet (status %s)", name, c.Status)
	}
	args := []string{"eks", "get-token", "--cluster-name", name, "--region", p.config.Region, "--output", "json"}
	if p.config.Profile != "" {
		args = append(args, "--profile", p.config.Profile)
	}
	return provider.ExecKubeconfig(name, aws.ToString(c.Endpoint), aws.ToString(c.CertificateAuthority.Data), "aws", args, nil, false), nil
}

// deleteManagedControlPlane removes the node groups, the EKS cluster and the
// IAM roles; the caller then runs the shared tag-based network cleanup.
func (p *Provider) deleteManagedControlPlane(ctx context.Context, name string) error {
	groups, err := p.eksClient.ListNodegroups(ctx, &eks.ListNodegroupsInput{ClusterName: aws.String(name)})
	if err != nil && !isEKSNotFound(err) {
		return fmt.Errorf("listing node groups of %s: %w", name, err)
	}
	if groups != nil {
		for _, ng := range groups.Nodegroups {
			fmt.Printf("🗑️  Deleting EKS node group %s...\n", ng)
			if _, err := p.eksClient.DeleteNodegroup(ctx, &eks.DeleteNodegroupInput{ClusterName: aws.String(name), NodegroupName: aws.String(ng)}); err != nil && !isEKSNotFound(err) {
				return fmt.Errorf("deleting node group %s: %w", ng, err)
			}
		}
		for _, ng := range groups.Nodegroups {
			if err := p.waitEKSNodegroup(ctx, name, ng, "", eksCreateTimeout); err != nil {
				return err
			}
		}
	}
	fmt.Printf("🗑️  Deleting EKS control plane %s...\n", name)
	if _, err := p.eksClient.DeleteCluster(ctx, &eks.DeleteClusterInput{Name: aws.String(name)}); err != nil && !isEKSNotFound(err) {
		return fmt.Errorf("deleting EKS cluster %s: %w", name, err)
	}
	if _, err := p.waitEKSCluster(ctx, name, "", eksCreateTimeout); err != nil {
		return err
	}
	clusterRole, nodeRole := eksRoleNames(name)
	for _, role := range []string{clusterRole, nodeRole} {
		if err := p.deleteIAMRole(ctx, role); err != nil {
			log.Printf("Warning: IAM role %s: %v", role, err)
		}
	}
	return nil
}

func eksNodegroupToNodeGroup(ng *ekstypes.Nodegroup) *types.NodeGroup {
	status := types.NodeGroupStatusReady
	switch ng.Status {
	case ekstypes.NodegroupStatusCreating:
		status = types.NodeGroupStatusCreating
	case ekstypes.NodegroupStatusUpdating:
		status = types.NodeGroupStatusScaling
	case ekstypes.NodegroupStatusDeleting:
		status = types.NodeGroupStatusDeleting
	case ekstypes.NodegroupStatusCreateFailed, ekstypes.NodegroupStatusDegraded, ekstypes.NodegroupStatusDeleteFailed:
		status = types.NodeGroupStatusError
	}
	g := &types.NodeGroup{Name: aws.ToString(ng.NodegroupName), Status: status, Labels: ng.Labels, UpdatedAt: time.Now()}
	if ng.ScalingConfig != nil {
		g.Replicas = int(aws.ToInt32(ng.ScalingConfig.DesiredSize))
	}
	if len(ng.InstanceTypes) > 0 {
		g.InstanceType = ng.InstanceTypes[0]
	}
	if ng.CreatedAt != nil {
		g.CreatedAt = *ng.CreatedAt
	}
	return g
}

func (p *Provider) managedAddNodeGroup(ctx context.Context, clusterName string, ng *types.NodeGroupSpec) (*types.NodeGroup, error) {
	_, nodeRoleName := eksRoleNames(clusterName)
	nodeRole, err := p.ensureIAMRole(ctx, nodeRoleName, "ec2.amazonaws.com", eksNodePolicies, clusterName)
	if err != nil {
		return nil, err
	}
	publicSubnet, _, err := p.clusterSubnets(ctx, clusterName)
	if err != nil {
		return nil, err
	}
	if err := p.ensureEKSNodegroup(ctx, clusterName, nodeRole, publicSubnet, ng); err != nil {
		return nil, err
	}
	return p.managedGetNodeGroup(ctx, clusterName, ng.Name)
}

func (p *Provider) managedRemoveNodeGroup(ctx context.Context, clusterName, name string) error {
	if _, err := p.eksClient.DeleteNodegroup(ctx, &eks.DeleteNodegroupInput{ClusterName: aws.String(clusterName), NodegroupName: aws.String(name)}); err != nil {
		if isEKSNotFound(err) {
			return nil
		}
		return err
	}
	return p.waitEKSNodegroup(ctx, clusterName, name, "", eksCreateTimeout)
}

func (p *Provider) managedScaleNodeGroup(ctx context.Context, clusterName, name string, replicas int) error {
	out, err := p.eksClient.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{ClusterName: aws.String(clusterName), NodegroupName: aws.String(name)})
	if err != nil {
		return err
	}
	sc := out.Nodegroup.ScalingConfig
	minSize, maxSize := int32(replicas), int32(replicas)
	if sc != nil {
		minSize, maxSize = aws.ToInt32(sc.MinSize), aws.ToInt32(sc.MaxSize)
	}
	if int32(replicas) < minSize {
		minSize = int32(replicas)
	}
	if int32(replicas) > maxSize {
		maxSize = int32(replicas)
	}
	if minSize < 1 && replicas > 0 {
		minSize = 1
	}
	_, err = p.eksClient.UpdateNodegroupConfig(ctx, &eks.UpdateNodegroupConfigInput{
		ClusterName: aws.String(clusterName), NodegroupName: aws.String(name),
		ScalingConfig: &ekstypes.NodegroupScalingConfig{MinSize: aws.Int32(minSize), MaxSize: aws.Int32(maxSize), DesiredSize: aws.Int32(int32(replicas))},
	})
	if err != nil {
		return fmt.Errorf("scaling EKS node group %s: %w", name, err)
	}
	return p.waitEKSNodegroup(ctx, clusterName, name, ekstypes.NodegroupStatusActive, eksCreateTimeout)
}

func (p *Provider) managedGetNodeGroup(ctx context.Context, clusterName, name string) (*types.NodeGroup, error) {
	out, err := p.eksClient.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{ClusterName: aws.String(clusterName), NodegroupName: aws.String(name)})
	if err != nil {
		return nil, fmt.Errorf("node group %s: %w", name, err)
	}
	return eksNodegroupToNodeGroup(out.Nodegroup), nil
}

func (p *Provider) managedListNodeGroups(ctx context.Context, clusterName string) ([]*types.NodeGroup, error) {
	names, err := p.eksClient.ListNodegroups(ctx, &eks.ListNodegroupsInput{ClusterName: aws.String(clusterName)})
	if err != nil {
		return nil, err
	}
	var groups []*types.NodeGroup
	for _, n := range names.Nodegroups {
		g, err := p.managedGetNodeGroup(ctx, clusterName, n)
		if err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, nil
}

// managedUpgrade moves the control plane and then every node group to the
// requested minor version.
func (p *Provider) managedUpgrade(ctx context.Context, clusterName, version string) error {
	target := provider.ManagedVersion(version)
	if target == "" {
		return fmt.Errorf("a target version is required")
	}
	c, err := p.describeEKS(ctx, clusterName)
	if err != nil {
		return err
	}
	if aws.ToString(c.Version) != target {
		if _, err := p.eksClient.UpdateClusterVersion(ctx, &eks.UpdateClusterVersionInput{Name: aws.String(clusterName), Version: aws.String(target)}); err != nil {
			return fmt.Errorf("upgrading EKS control plane: %w", err)
		}
		if _, err := p.waitEKSCluster(ctx, clusterName, ekstypes.ClusterStatusActive, eksCreateTimeout); err != nil {
			return err
		}
	}
	names, err := p.eksClient.ListNodegroups(ctx, &eks.ListNodegroupsInput{ClusterName: aws.String(clusterName)})
	if err != nil {
		return err
	}
	for _, ng := range names.Nodegroups {
		if _, err := p.eksClient.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{ClusterName: aws.String(clusterName), NodegroupName: aws.String(ng), Version: aws.String(target)}); err != nil {
			return fmt.Errorf("upgrading node group %s: %w", ng, err)
		}
		if err := p.waitEKSNodegroup(ctx, clusterName, ng, ekstypes.NodegroupStatusActive, eksCreateTimeout); err != nil {
			return err
		}
	}
	return nil
}

// managedListClusters returns the adhar-tagged EKS clusters in the region.
func (p *Provider) managedListClusters(ctx context.Context) ([]*types.Cluster, error) {
	var clusters []*types.Cluster
	var next *string
	for {
		out, err := p.eksClient.ListClusters(ctx, &eks.ListClustersInput{NextToken: next})
		if err != nil {
			return nil, err
		}
		for _, n := range out.Clusters {
			c, err := p.describeEKS(ctx, n)
			if err != nil {
				continue
			}
			if c.Tags["ManagedBy"] != "adhar" {
				continue
			}
			clusters = append(clusters, p.eksToCluster(c))
		}
		if out.NextToken == nil {
			return clusters, nil
		}
		next = out.NextToken
	}
}

// managedHealth reports the EKS control-plane status.
func (p *Provider) managedHealth(ctx context.Context, clusterName string) (*types.HealthStatus, error) {
	c, err := p.describeEKS(ctx, clusterName)
	if err != nil {
		return &types.HealthStatus{Status: "unhealthy", Components: map[string]types.ComponentHealth{"cluster": {Status: "unhealthy", Message: err.Error()}}, LastCheck: time.Now()}, nil
	}
	status := "healthy"
	if c.Status != ekstypes.ClusterStatusActive {
		status = "degraded"
	}
	h := &types.HealthStatus{Status: status, Components: map[string]types.ComponentHealth{"control-plane": {Status: status, Message: "EKS " + string(c.Status)}}, LastCheck: time.Now()}
	if c.Health != nil {
		for _, issue := range c.Health.Issues {
			h.Status = "unhealthy"
			h.Components[string(issue.Code)] = types.ComponentHealth{Status: "unhealthy", Message: aws.ToString(issue.Message)}
		}
	}
	return h, nil
}
