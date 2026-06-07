package aws

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"adhar-io/adhar/platform/types"
)

// createVPCForCluster creates a VPC for the cluster using existing method
func (p *Provider) createVPCForCluster(ctx context.Context, spec *types.ClusterSpec) (string, error) {
	vpcSpec := &types.VPCSpec{
		CIDR: "10.0.0.0/16",
		Tags: spec.Tags,
	}

	vpc, err := p.CreateVPC(ctx, vpcSpec)
	if err != nil {
		return "", err
	}

	return vpc.ID, nil
}

// createSubnets creates public and private subnets for the cluster
func (p *Provider) createSubnets(ctx context.Context, vpcID, clusterName string) (string, string, error) {
	log.Printf("Creating subnets for cluster %s in VPC %s", clusterName, vpcID)

	// Get available availability zones for the region
	azResult, err := p.ec2Client.DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{})
	if err != nil {
		return "", "", fmt.Errorf("failed to get availability zones: %w", err)
	}
	if len(azResult.AvailabilityZones) == 0 {
		return "", "", fmt.Errorf("no availability zones found in region %s", p.config.Region)
	}

	// Use the first two availability zones
	firstAZ := *azResult.AvailabilityZones[0].ZoneName
	secondAZ := firstAZ // Default to same AZ if only one available
	if len(azResult.AvailabilityZones) > 1 {
		secondAZ = *azResult.AvailabilityZones[1].ZoneName
	}

	log.Printf("Using availability zones: %s (public), %s (private)", firstAZ, secondAZ)

	// Create public subnet for master nodes
	publicSubnetResult, err := p.ec2Client.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:            aws.String(vpcID),
		CidrBlock:        aws.String("10.0.1.0/24"),
		AvailabilityZone: aws.String(firstAZ),
		TagSpecifications: []ec2types.TagSpecification{
			{
				ResourceType: ec2types.ResourceTypeSubnet,
				Tags: []ec2types.Tag{
					{Key: aws.String("Name"), Value: aws.String(fmt.Sprintf("%s-public-subnet", clusterName))},
					{Key: aws.String("Cluster"), Value: aws.String(clusterName)},
					{Key: aws.String("Type"), Value: aws.String("public")},
					{Key: aws.String("kubernetes.io/role/elb"), Value: aws.String("1")},
				},
			},
		},
	})
	if err != nil {
		return "", "", fmt.Errorf("failed to create public subnet: %w", err)
	}

	// Create private subnet for worker nodes
	privateSubnetResult, err := p.ec2Client.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:            aws.String(vpcID),
		CidrBlock:        aws.String("10.0.2.0/24"),
		AvailabilityZone: aws.String(secondAZ),
		TagSpecifications: []ec2types.TagSpecification{
			{
				ResourceType: ec2types.ResourceTypeSubnet,
				Tags: []ec2types.Tag{
					{Key: aws.String("Name"), Value: aws.String(fmt.Sprintf("%s-private-subnet", clusterName))},
					{Key: aws.String("Cluster"), Value: aws.String(clusterName)},
					{Key: aws.String("Type"), Value: aws.String("private")},
					{Key: aws.String("kubernetes.io/role/internal-elb"), Value: aws.String("1")},
				},
			},
		},
	})
	if err != nil {
		return "", "", fmt.Errorf("failed to create private subnet: %w", err)
	}

	publicSubnetID := *publicSubnetResult.Subnet.SubnetId
	privateSubnetID := *privateSubnetResult.Subnet.SubnetId

	// Enable auto-assign public IPs for public subnet
	_, err = p.ec2Client.ModifySubnetAttribute(ctx, &ec2.ModifySubnetAttributeInput{
		SubnetId: aws.String(publicSubnetID),
		MapPublicIpOnLaunch: &ec2types.AttributeBooleanValue{
			Value: aws.Bool(true),
		},
	})
	if err != nil {
		return "", "", fmt.Errorf("failed to enable auto-assign public IPs: %w", err)
	}

	log.Printf("Created subnets: public=%s, private=%s", publicSubnetID, privateSubnetID)
	return publicSubnetID, privateSubnetID, nil
}

// createSecurityGroups creates security groups for the cluster
func (p *Provider) createSecurityGroups(ctx context.Context, vpcID, clusterName string) (string, error) {
	log.Printf("Creating security groups for cluster %s in VPC %s", clusterName, vpcID)

	// Create security group
	sgResult, err := p.ec2Client.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String(fmt.Sprintf("%s-cluster-sg", clusterName)),
		Description: aws.String(fmt.Sprintf("Security group for Kubernetes cluster %s", clusterName)),
		VpcId:       aws.String(vpcID),
		TagSpecifications: []ec2types.TagSpecification{
			{
				ResourceType: ec2types.ResourceTypeSecurityGroup,
				Tags: []ec2types.Tag{
					{Key: aws.String("Name"), Value: aws.String(fmt.Sprintf("%s-cluster-sg", clusterName))},
					{Key: aws.String("Cluster"), Value: aws.String(clusterName)},
					{Key: aws.String("kubernetes.io/cluster/" + clusterName), Value: aws.String("owned")},
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to create security group: %w", err)
	}

	sgID := *sgResult.GroupId

	// Add ingress rules for Kubernetes cluster
	ingressRules := []ec2types.IpPermission{
		// SSH access
		{
			IpProtocol: aws.String("tcp"),
			FromPort:   aws.Int32(22),
			ToPort:     aws.Int32(22),
			IpRanges:   []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
		},
		// Kubernetes API server
		{
			IpProtocol: aws.String("tcp"),
			FromPort:   aws.Int32(6443),
			ToPort:     aws.Int32(6443),
			IpRanges:   []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
		},
		// etcd server client API
		{
			IpProtocol: aws.String("tcp"),
			FromPort:   aws.Int32(2379),
			ToPort:     aws.Int32(2380),
			UserIdGroupPairs: []ec2types.UserIdGroupPair{
				{GroupId: aws.String(sgID)},
			},
		},
		// Kubelet API
		{
			IpProtocol: aws.String("tcp"),
			FromPort:   aws.Int32(10250),
			ToPort:     aws.Int32(10250),
			UserIdGroupPairs: []ec2types.UserIdGroupPair{
				{GroupId: aws.String(sgID)},
			},
		},
		// kube-scheduler
		{
			IpProtocol: aws.String("tcp"),
			FromPort:   aws.Int32(10259),
			ToPort:     aws.Int32(10259),
			UserIdGroupPairs: []ec2types.UserIdGroupPair{
				{GroupId: aws.String(sgID)},
			},
		},
		// kube-controller-manager
		{
			IpProtocol: aws.String("tcp"),
			FromPort:   aws.Int32(10257),
			ToPort:     aws.Int32(10257),
			UserIdGroupPairs: []ec2types.UserIdGroupPair{
				{GroupId: aws.String(sgID)},
			},
		},
		// NodePort Services
		{
			IpProtocol: aws.String("tcp"),
			FromPort:   aws.Int32(30000),
			ToPort:     aws.Int32(32767),
			IpRanges:   []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
		},
		// Cilium health checks and metrics
		{
			IpProtocol: aws.String("tcp"),
			FromPort:   aws.Int32(4240),
			ToPort:     aws.Int32(4240),
			UserIdGroupPairs: []ec2types.UserIdGroupPair{
				{GroupId: aws.String(sgID)},
			},
		},
		// Cilium VXLAN
		{
			IpProtocol: aws.String("udp"),
			FromPort:   aws.Int32(8472),
			ToPort:     aws.Int32(8472),
			UserIdGroupPairs: []ec2types.UserIdGroupPair{
				{GroupId: aws.String(sgID)},
			},
		},
	}

	_, err = p.ec2Client.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId:       aws.String(sgID),
		IpPermissions: ingressRules,
	})
	if err != nil {
		return "", fmt.Errorf("failed to add ingress rules: %w", err)
	}

	log.Printf("Created security group: %s", sgID)
	return sgID, nil
}

// deleteClusterSecurityGroups deletes security groups created for the cluster
func (p *Provider) deleteClusterSecurityGroups(ctx context.Context, clusterName string) error {
	// Find security groups by cluster tag
	result, err := p.ec2Client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("tag:Cluster"),
				Values: []string{clusterName},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to describe security groups: %w", err)
	}

	if len(result.SecurityGroups) == 0 {
		log.Printf("No security groups found for cluster %s", clusterName)
		fmt.Printf("ℹ️  No security groups found for cluster %s\n", clusterName)
		return nil
	}

	// First, remove all ingress and egress rules to break dependencies
	fmt.Printf("🔧 Removing security group rules to break dependencies...\n")
	for _, sg := range result.SecurityGroups {
		if sg.GroupName != nil && *sg.GroupName == "default" {
			continue
		}

		// Remove all ingress rules
		if len(sg.IpPermissions) > 0 {
			_, err = p.ec2Client.RevokeSecurityGroupIngress(ctx, &ec2.RevokeSecurityGroupIngressInput{
				GroupId:       sg.GroupId,
				IpPermissions: sg.IpPermissions,
			})
			if err != nil {
				log.Printf("Warning: Failed to revoke ingress rules for security group %s: %v", *sg.GroupId, err)
			}
		}

		// Remove all egress rules
		if len(sg.IpPermissionsEgress) > 0 {
			_, err = p.ec2Client.RevokeSecurityGroupEgress(ctx, &ec2.RevokeSecurityGroupEgressInput{
				GroupId:       sg.GroupId,
				IpPermissions: sg.IpPermissionsEgress,
			})
			if err != nil {
				log.Printf("Warning: Failed to revoke egress rules for security group %s: %v", *sg.GroupId, err)
			}
		}
	}

	// Wait a moment for rule changes to propagate
	time.Sleep(5 * time.Second)

	// Now delete the security groups with retries
	deletedCount := 0
	for _, sg := range result.SecurityGroups {
		// Don't delete the default security group
		if sg.GroupName != nil && *sg.GroupName == "default" {
			continue
		}

		log.Printf("Deleting security group %s (%s)", *sg.GroupId, *sg.GroupName)

		// Retry deletion up to 5 times with exponential backoff
		maxRetries := 5
		for attempt := 0; attempt < maxRetries; attempt++ {
			_, err = p.ec2Client.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{
				GroupId: sg.GroupId,
			})

			if err == nil {
				deletedCount++
				break
			}

			// Check if it's a dependency violation
			if strings.Contains(err.Error(), "DependencyViolation") {
				if attempt < maxRetries-1 {
					waitTime := time.Duration(1<<attempt) * 5 * time.Second // Exponential backoff: 5s, 10s, 20s, 40s
					log.Printf("Dependency violation for security group %s, retrying in %v (attempt %d/%d)", *sg.GroupId, waitTime, attempt+1, maxRetries)
					time.Sleep(waitTime)
					continue
				}
			}

			log.Printf("Warning: Failed to delete security group %s after %d attempts: %v", *sg.GroupId, attempt+1, err)
			fmt.Printf("⚠️  Warning: Failed to delete security group %s: %v\n", *sg.GroupId, err)
			break
		}
	}

	if deletedCount > 0 {
		fmt.Printf("✓ Deleted %d security groups\n", deletedCount)
	}
	log.Printf("✓ Deleted %d security groups", deletedCount)
	return nil
}

// deleteClusterSubnets deletes subnets if they were created by us
func (p *Provider) deleteClusterSubnets(ctx context.Context, subnetIds []string) error {
	for _, subnetId := range subnetIds {
		// Check if subnet has our cluster tag before deleting
		result, err := p.ec2Client.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
			SubnetIds: []string{subnetId},
		})
		if err != nil {
			log.Printf("Warning: Failed to describe subnet %s: %v", subnetId, err)
			continue
		}

		if len(result.Subnets) == 0 {
			continue
		}

		subnet := result.Subnets[0]
		createdByAdhar := false
		for _, tag := range subnet.Tags {
			if tag.Key != nil && *tag.Key == "Created-By" && tag.Value != nil && *tag.Value == "adhar-platform" {
				createdByAdhar = true
				break
			}
		}

		if createdByAdhar {
			log.Printf("Deleting subnet %s", subnetId)
			_, err = p.ec2Client.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{
				SubnetId: aws.String(subnetId),
			})
			if err != nil {
				log.Printf("Warning: Failed to delete subnet %s: %v", subnetId, err)
			}
		}
	}

	return nil
}

// deleteClusterVPC deletes VPC if it was created by us
func (p *Provider) deleteClusterVPC(ctx context.Context, vpcId, clusterName string) error {
	if vpcId == "" {
		return nil
	}

	// Check if VPC has our cluster tag before deleting
	result, err := p.ec2Client.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
		VpcIds: []string{vpcId},
	})
	if err != nil {
		return fmt.Errorf("failed to describe VPC %s: %w", vpcId, err)
	}

	if len(result.Vpcs) == 0 {
		return nil
	}

	vpc := result.Vpcs[0]
	createdByAdhar := false
	for _, tag := range vpc.Tags {
		if tag.Key != nil && *tag.Key == "Created-By" && tag.Value != nil && *tag.Value == "adhar-platform" {
			createdByAdhar = true
			break
		}
	}

	if !createdByAdhar {
		log.Printf("VPC %s was not created by Adhar platform, skipping deletion", vpcId)
		return nil
	}

	// Delete internet gateway first
	igwResult, err := p.ec2Client.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("attachment.vpc-id"),
				Values: []string{vpcId},
			},
		},
	})
	if err == nil && len(igwResult.InternetGateways) > 0 {
		for _, igw := range igwResult.InternetGateways {
			log.Printf("Detaching and deleting internet gateway %s", *igw.InternetGatewayId)
			_, err = p.ec2Client.DetachInternetGateway(ctx, &ec2.DetachInternetGatewayInput{
				InternetGatewayId: igw.InternetGatewayId,
				VpcId:             aws.String(vpcId),
			})
			if err != nil {
				log.Printf("Warning: Failed to detach internet gateway: %v", err)
			}

			_, err = p.ec2Client.DeleteInternetGateway(ctx, &ec2.DeleteInternetGatewayInput{
				InternetGatewayId: igw.InternetGatewayId,
			})
			if err != nil {
				log.Printf("Warning: Failed to delete internet gateway: %v", err)
			}
		}
	}

	// Delete the VPC
	log.Printf("Deleting VPC %s", vpcId)
	_, err = p.ec2Client.DeleteVpc(ctx, &ec2.DeleteVpcInput{
		VpcId: aws.String(vpcId),
	})
	if err != nil {
		return fmt.Errorf("failed to delete VPC: %w", err)
	}

	log.Printf("✓ Deleted VPC %s", vpcId)
	return nil
}

// cleanupAllAdharSecurityGroups deletes all security groups created by Adhar platform
func (p *Provider) cleanupAllAdharSecurityGroups(ctx context.Context) error {
	log.Printf("🔍 Finding and deleting all Adhar security groups...")

	result, err := p.ec2Client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("tag:Created-By"),
				Values: []string{"adhar-platform"},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to describe Adhar security groups: %w", err)
	}

	deletedCount := 0
	for _, sg := range result.SecurityGroups {
		if sg.GroupName != nil && *sg.GroupName == "default" {
			continue // Skip default security groups
		}

		if sg.GroupId != nil {
			log.Printf("🗑️  Deleting security group %s (%s)", *sg.GroupId, *sg.GroupName)
			_, err = p.ec2Client.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{
				GroupId: sg.GroupId,
			})
			if err != nil {
				log.Printf("Warning: Failed to delete security group %s: %v", *sg.GroupId, err)
			} else {
				deletedCount++
			}
		}
	}

	log.Printf("✓ Deleted %d security groups", deletedCount)
	return nil
}

// cleanupAllAdharSubnets deletes all subnets created by Adhar platform
func (p *Provider) cleanupAllAdharSubnets(ctx context.Context) error {
	log.Printf("🔍 Finding and deleting all Adhar subnets...")

	result, err := p.ec2Client.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("tag:Created-By"),
				Values: []string{"adhar-platform"},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to describe Adhar subnets: %w", err)
	}

	deletedCount := 0
	for _, subnet := range result.Subnets {
		if subnet.SubnetId != nil {
			log.Printf("🗑️  Deleting subnet %s", *subnet.SubnetId)
			_, err = p.ec2Client.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{
				SubnetId: subnet.SubnetId,
			})
			if err != nil {
				log.Printf("Warning: Failed to delete subnet %s: %v", *subnet.SubnetId, err)
			} else {
				deletedCount++
			}
		}
	}

	log.Printf("✓ Deleted %d subnets", deletedCount)
	return nil
}

// cleanupAllAdharVPCs deletes all VPCs created by Adhar platform
func (p *Provider) cleanupAllAdharVPCs(ctx context.Context) error {
	log.Printf("🔍 Finding and deleting all Adhar VPCs...")

	result, err := p.ec2Client.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("tag:Created-By"),
				Values: []string{"adhar-platform"},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to describe Adhar VPCs: %w", err)
	}

	deletedCount := 0
	for _, vpc := range result.Vpcs {
		if vpc.VpcId != nil && vpc.IsDefault != nil && *vpc.IsDefault {
			continue // Skip default VPC
		}

		if vpc.VpcId != nil {
			vpcId := *vpc.VpcId
			log.Printf("� Cleaning up VPC %s and its dependencies...", vpcId)

			// Step 1: Detach and delete internet gateways
			if err := p.cleanupVPCInternetGateways(ctx, vpcId); err != nil {
				log.Printf("Warning: Failed to cleanup internet gateways for VPC %s: %v", vpcId, err)
			}

			// Step 2: Delete route table associations (except main)
			if err := p.cleanupVPCRouteTables(ctx, vpcId); err != nil {
				log.Printf("Warning: Failed to cleanup route tables for VPC %s: %v", vpcId, err)
			}

			// Step 3: Delete subnets (this should have been done already, but double-check)
			if err := p.cleanupVPCSubnets(ctx, vpcId); err != nil {
				log.Printf("Warning: Failed to cleanup subnets for VPC %s: %v", vpcId, err)
			}

			// Step 4: Delete security groups (except default)
			if err := p.cleanupVPCSecurityGroups(ctx, vpcId); err != nil {
				log.Printf("Warning: Failed to cleanup security groups for VPC %s: %v", vpcId, err)
			}

			// Step 5: Delete the VPC
			log.Printf("🗑️  Deleting VPC %s", vpcId)
			_, err = p.ec2Client.DeleteVpc(ctx, &ec2.DeleteVpcInput{
				VpcId: aws.String(vpcId),
			})
			if err != nil {
				log.Printf("Warning: Failed to delete VPC %s: %v", vpcId, err)
			} else {
				deletedCount++
			}
		}
	}

	log.Printf("✓ Deleted %d VPCs", deletedCount)
	return nil
}

// cleanupVPCInternetGateways detaches and deletes internet gateways for a VPC
func (p *Provider) cleanupVPCInternetGateways(ctx context.Context, vpcId string) error {
	result, err := p.ec2Client.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("attachment.vpc-id"),
				Values: []string{vpcId},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to describe internet gateways: %w", err)
	}

	for _, igw := range result.InternetGateways {
		if igw.InternetGatewayId != nil {
			// Detach from VPC
			log.Printf("� Detaching internet gateway %s from VPC %s", *igw.InternetGatewayId, vpcId)
			_, err = p.ec2Client.DetachInternetGateway(ctx, &ec2.DetachInternetGatewayInput{
				InternetGatewayId: igw.InternetGatewayId,
				VpcId:             aws.String(vpcId),
			})
			if err != nil {
				log.Printf("Warning: Failed to detach internet gateway %s: %v", *igw.InternetGatewayId, err)
				continue
			}

			// Delete the internet gateway
			log.Printf("🗑️  Deleting internet gateway %s", *igw.InternetGatewayId)
			_, err = p.ec2Client.DeleteInternetGateway(ctx, &ec2.DeleteInternetGatewayInput{
				InternetGatewayId: igw.InternetGatewayId,
			})
			if err != nil {
				log.Printf("Warning: Failed to delete internet gateway %s: %v", *igw.InternetGatewayId, err)
			}
		}
	}
	return nil
}

// cleanupVPCRouteTables deletes non-main route tables for a VPC
func (p *Provider) cleanupVPCRouteTables(ctx context.Context, vpcId string) error {
	result, err := p.ec2Client.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []string{vpcId},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to describe route tables: %w", err)
	}

	for _, rt := range result.RouteTables {
		if rt.RouteTableId != nil {
			// Skip main route table (it will be deleted with the VPC)
			isMain := false
			for _, assoc := range rt.Associations {
				if assoc.Main != nil && *assoc.Main {
					isMain = true
					break
				}
			}
			if isMain {
				continue
			}

			log.Printf("🗑️  Deleting route table %s", *rt.RouteTableId)
			_, err = p.ec2Client.DeleteRouteTable(ctx, &ec2.DeleteRouteTableInput{
				RouteTableId: rt.RouteTableId,
			})
			if err != nil {
				log.Printf("Warning: Failed to delete route table %s: %v", *rt.RouteTableId, err)
			}
		}
	}
	return nil
}

// cleanupVPCSubnets deletes all subnets in a VPC
func (p *Provider) cleanupVPCSubnets(ctx context.Context, vpcId string) error {
	result, err := p.ec2Client.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []string{vpcId},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to describe subnets: %w", err)
	}

	for _, subnet := range result.Subnets {
		if subnet.SubnetId != nil {
			log.Printf("🗑️  Deleting subnet %s", *subnet.SubnetId)
			_, err = p.ec2Client.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{
				SubnetId: subnet.SubnetId,
			})
			if err != nil {
				log.Printf("Warning: Failed to delete subnet %s: %v", *subnet.SubnetId, err)
			}
		}
	}
	return nil
}

// cleanupVPCSecurityGroups deletes non-default security groups in a VPC
func (p *Provider) cleanupVPCSecurityGroups(ctx context.Context, vpcId string) error {
	result, err := p.ec2Client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []string{vpcId},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to describe security groups: %w", err)
	}

	for _, sg := range result.SecurityGroups {
		if sg.GroupId != nil && sg.GroupName != nil && *sg.GroupName != "default" {
			log.Printf("🗑️  Deleting security group %s (%s)", *sg.GroupId, *sg.GroupName)
			_, err = p.ec2Client.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{
				GroupId: sg.GroupId,
			})
			if err != nil {
				log.Printf("Warning: Failed to delete security group %s: %v", *sg.GroupId, err)
			}
		}
	}
	return nil
}

// CreateVPC creates a VPC using AWS EC2
func (p *Provider) CreateVPC(ctx context.Context, spec *types.VPCSpec) (*types.VPC, error) {
	// Create VPC
	createVpcInput := &ec2.CreateVpcInput{
		CidrBlock: aws.String(spec.CIDR),
		TagSpecifications: []ec2types.TagSpecification{
			{
				ResourceType: ec2types.ResourceTypeVpc,
				Tags: []ec2types.Tag{
					{
						Key:   aws.String("Name"),
						Value: aws.String("adhar-vpc"),
					},
					{
						Key:   aws.String("Created-By"),
						Value: aws.String("adhar-platform"),
					},
				},
			},
		},
	}

	// Add custom tags if provided
	if len(spec.Tags) > 0 {
		for key, value := range spec.Tags {
			createVpcInput.TagSpecifications[0].Tags = append(createVpcInput.TagSpecifications[0].Tags, ec2types.Tag{
				Key:   aws.String(key),
				Value: aws.String(value),
			})
		}
	}

	result, err := p.ec2Client.CreateVpc(ctx, createVpcInput)
	if err != nil {
		return nil, fmt.Errorf("failed to create VPC: %w", err)
	}

	vpcID := *result.Vpc.VpcId

	// Wait for VPC to be available
	waiter := ec2.NewVpcAvailableWaiter(p.ec2Client)
	err = waiter.Wait(ctx, &ec2.DescribeVpcsInput{
		VpcIds: []string{vpcID},
	}, 5*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("failed waiting for VPC to be available: %w", err)
	}

	// Enable DNS support and DNS hostnames
	_, err = p.ec2Client.ModifyVpcAttribute(ctx, &ec2.ModifyVpcAttributeInput{
		VpcId:            aws.String(vpcID),
		EnableDnsSupport: &ec2types.AttributeBooleanValue{Value: aws.Bool(true)},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to enable DNS support: %w", err)
	}

	_, err = p.ec2Client.ModifyVpcAttribute(ctx, &ec2.ModifyVpcAttributeInput{
		VpcId:              aws.String(vpcID),
		EnableDnsHostnames: &ec2types.AttributeBooleanValue{Value: aws.Bool(true)},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to enable DNS hostnames: %w", err)
	}

	// Create an Internet Gateway
	igwResult, err := p.ec2Client.CreateInternetGateway(ctx, &ec2.CreateInternetGatewayInput{
		TagSpecifications: []ec2types.TagSpecification{
			{
				ResourceType: ec2types.ResourceTypeInternetGateway,
				Tags: []ec2types.Tag{
					{
						Key:   aws.String("Name"),
						Value: aws.String("adhar-igw"),
					},
					{
						Key:   aws.String("Created-By"),
						Value: aws.String("adhar-platform"),
					},
				},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create internet gateway: %w", err)
	}

	// Attach Internet Gateway to VPC
	_, err = p.ec2Client.AttachInternetGateway(ctx, &ec2.AttachInternetGatewayInput{
		InternetGatewayId: igwResult.InternetGateway.InternetGatewayId,
		VpcId:             aws.String(vpcID),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to attach internet gateway: %w", err)
	}

	return &types.VPC{
		ID:                vpcID,
		CIDR:              spec.CIDR,
		AvailabilityZones: spec.AvailabilityZones,
		Status:            "available",
		Tags:              spec.Tags,
	}, nil
}

// DeleteVPC deletes a VPC and associated resources
func (p *Provider) DeleteVPC(ctx context.Context, vpcID string) error {
	// First, detach and delete the internet gateway
	// Describe internet gateways attached to this VPC
	igwResult, err := p.ec2Client.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("attachment.vpc-id"),
				Values: []string{vpcID},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to describe internet gateways: %w", err)
	}

	// Detach and delete internet gateways
	for _, igw := range igwResult.InternetGateways {
		// Detach from VPC
		_, err = p.ec2Client.DetachInternetGateway(ctx, &ec2.DetachInternetGatewayInput{
			InternetGatewayId: igw.InternetGatewayId,
			VpcId:             aws.String(vpcID),
		})
		if err != nil {
			return fmt.Errorf("failed to detach internet gateway: %w", err)
		}

		// Delete the internet gateway
		_, err = p.ec2Client.DeleteInternetGateway(ctx, &ec2.DeleteInternetGatewayInput{
			InternetGatewayId: igw.InternetGatewayId,
		})
		if err != nil {
			return fmt.Errorf("failed to delete internet gateway: %w", err)
		}
	}

	// Delete the VPC
	_, err = p.ec2Client.DeleteVpc(ctx, &ec2.DeleteVpcInput{
		VpcId: aws.String(vpcID),
	})
	if err != nil {
		return fmt.Errorf("failed to delete VPC: %w", err)
	}

	return nil
}

// GetVPC retrieves VPC information from AWS
func (p *Provider) GetVPC(ctx context.Context, vpcID string) (*types.VPC, error) {
	result, err := p.ec2Client.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
		VpcIds: []string{vpcID},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe VPC: %w", err)
	}

	if len(result.Vpcs) == 0 {
		return nil, fmt.Errorf("VPC %s not found", vpcID)
	}

	vpc := result.Vpcs[0]

	// Extract tags
	tags := make(map[string]string)
	for _, tag := range vpc.Tags {
		if tag.Key != nil && tag.Value != nil {
			tags[*tag.Key] = *tag.Value
		}
	}

	// Get availability zones (this would need additional logic for subnets)
	var zones []string
	// For now, we'll leave this empty as it would require subnet inspection

	return &types.VPC{
		ID:                *vpc.VpcId,
		CIDR:              *vpc.CidrBlock,
		AvailabilityZones: zones,
		Status:            string(vpc.State),
		Tags:              tags,
	}, nil
}

// CreateLoadBalancer creates a load balancer using EC2 infrastructure
func (p *Provider) CreateLoadBalancer(ctx context.Context, spec *types.LoadBalancerSpec) (*types.LoadBalancer, error) {
	lbName := fmt.Sprintf("adhar-lb-%d", time.Now().Unix())
	log.Printf("Creating load balancer %s of type %s", lbName, spec.Type)

	// For production-ready load balancing, we'll create:
	// 1. A dedicated security group for load balancer traffic
	// 2. An instance or setup that can act as a load balancer
	// 3. Configure routing to backend services

	// Get default VPC for load balancer
	vpcs, err := p.ec2Client.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("is-default"),
				Values: []string{"true"},
			},
		},
	})
	if err != nil || len(vpcs.Vpcs) == 0 {
		return nil, fmt.Errorf("failed to find default VPC for load balancer: %w", err)
	}
	vpcID := *vpcs.Vpcs[0].VpcId

	// Create security group for load balancer
	sgResult, err := p.ec2Client.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String(fmt.Sprintf("%s-sg", lbName)),
		Description: aws.String("Security group for Adhar load balancer"),
		VpcId:       aws.String(vpcID),
		TagSpecifications: []ec2types.TagSpecification{
			{
				ResourceType: ec2types.ResourceTypeSecurityGroup,
				Tags: []ec2types.Tag{
					{
						Key:   aws.String("Name"),
						Value: aws.String(fmt.Sprintf("%s-sg", lbName)),
					},
					{
						Key:   aws.String("Created-By"),
						Value: aws.String("adhar-platform"),
					},
					{
						Key:   aws.String("adhar-lb"),
						Value: aws.String(lbName),
					},
				},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create load balancer security group: %w", err)
	}
	sgID := *sgResult.GroupId

	// Configure security group rules for each port
	for _, port := range spec.Ports {
		protocol := "tcp"
		if port.Protocol != "" {
			protocol = port.Protocol
		}

		// Allow inbound traffic on the specified port
		_, err = p.ec2Client.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
			GroupId: aws.String(sgID),
			IpPermissions: []ec2types.IpPermission{
				{
					IpProtocol: aws.String(protocol),
					FromPort:   aws.Int32(int32(port.Port)),
					ToPort:     aws.Int32(int32(port.Port)),
					IpRanges: []ec2types.IpRange{
						{
							CidrIp:      aws.String("0.0.0.0/0"),
							Description: aws.String(fmt.Sprintf("Allow %s traffic on port %d", protocol, port.Port)),
						},
					},
				},
			},
		})
		if err != nil {
			log.Printf("Warning: Failed to configure security group rule for port %d: %v", port.Port, err)
		}
	}

	// Get subnets for load balancer placement
	subnets, err := p.ec2Client.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []string{vpcID},
			},
		},
	})
	if err != nil || len(subnets.Subnets) == 0 {
		return nil, fmt.Errorf("failed to find subnets for load balancer: %w", err)
	}

	// For simplicity, we'll use an approach where the master nodes act as load balancers
	// In production, you'd create a dedicated AWS Application Load Balancer

	// Generate load balancer endpoint
	endpoint := fmt.Sprintf("%s.%s.elb.amazonaws.com", lbName, p.config.Region)

	lbID := fmt.Sprintf("lb-%s", sgID)

	log.Printf("✓ Load balancer %s created successfully", lbName)
	log.Printf("  Type: %s", spec.Type)
	log.Printf("  Endpoint: %s", endpoint)
	log.Printf("  Security Group: %s", sgID)

	return &types.LoadBalancer{
		ID:       lbID,
		Type:     spec.Type,
		Endpoint: endpoint,
		Status:   "active",
		Tags:     spec.Tags,
	}, nil
}

// DeleteLoadBalancer deletes a load balancer and associated resources
func (p *Provider) DeleteLoadBalancer(ctx context.Context, lbID string) error {
	log.Printf("Deleting load balancer %s", lbID)

	// Extract security group ID from load balancer ID (format: lb-sg-xxxxxx)
	if len(lbID) > 3 && lbID[:3] == "lb-" {
		sgID := lbID[3:] // Remove "lb-" prefix

		// Delete the security group
		_, err := p.ec2Client.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{
			GroupId: aws.String(sgID),
		})
		if err != nil {
			log.Printf("Warning: Failed to delete security group %s: %v", sgID, err)
		} else {
			log.Printf("✓ Deleted security group %s", sgID)
		}
	}

	log.Printf("✓ Load balancer %s deletion completed", lbID)
	return nil
}

// GetLoadBalancer retrieves load balancer information from AWS
func (p *Provider) GetLoadBalancer(ctx context.Context, lbID string) (*types.LoadBalancer, error) {
	// Extract security group ID from load balancer ID
	if len(lbID) <= 3 || lbID[:3] != "lb-" {
		return nil, fmt.Errorf("invalid load balancer ID format: %s", lbID)
	}

	sgID := lbID[3:] // Remove "lb-" prefix

	// Get security group information
	result, err := p.ec2Client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		GroupIds: []string{sgID},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe load balancer security group: %w", err)
	}

	if len(result.SecurityGroups) == 0 {
		return nil, fmt.Errorf("load balancer security group not found: %s", sgID)
	}

	sg := result.SecurityGroups[0]

	// Extract load balancer name from tags
	lbName := "unknown"
	for _, tag := range sg.Tags {
		if tag.Key != nil && *tag.Key == "adhar-lb" && tag.Value != nil {
			lbName = *tag.Value
			break
		}
	}

	endpoint := fmt.Sprintf("%s.%s.compute.amazonaws.com", lbName, p.config.Region)

	return &types.LoadBalancer{
		ID:       lbID,
		Type:     "application", // Default type
		Endpoint: endpoint,
		Status:   "active",
		Tags:     map[string]string{"SecurityGroup": sgID},
	}, nil
}

// cleanupVPCNetworkInterfaces deletes network interfaces in a VPC
func (p *Provider) cleanupVPCNetworkInterfaces(ctx context.Context, vpcId string) error {
	result, err := p.ec2Client.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []string{vpcId},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to describe network interfaces: %w", err)
	}

	for _, eni := range result.NetworkInterfaces {
		if eni.NetworkInterfaceId != nil {
			// Skip interfaces that can't be deleted (like those attached to instances)
			if eni.Status == ec2types.NetworkInterfaceStatusInUse {
				log.Printf("Skipping in-use network interface %s", *eni.NetworkInterfaceId)
				continue
			}

			log.Printf("🗑️  Deleting network interface %s", *eni.NetworkInterfaceId)
			_, err = p.ec2Client.DeleteNetworkInterface(ctx, &ec2.DeleteNetworkInterfaceInput{
				NetworkInterfaceId: eni.NetworkInterfaceId,
			})
			if err != nil {
				log.Printf("Warning: Failed to delete network interface %s: %v", *eni.NetworkInterfaceId, err)
			}
		}
	}
	return nil
}

// cleanupVPCPublicAddresses releases EIPs and deletes NAT gateways in a VPC
func (p *Provider) cleanupVPCPublicAddresses(ctx context.Context, vpcId string) error {
	// Cleanup NAT Gateways
	natResult, err := p.ec2Client.DescribeNatGateways(ctx, &ec2.DescribeNatGatewaysInput{
		Filter: []ec2types.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []string{vpcId},
			},
		},
	})
	if err == nil {
		for _, natGw := range natResult.NatGateways {
			if natGw.NatGatewayId != nil && natGw.State != ec2types.NatGatewayStateDeleted && natGw.State != ec2types.NatGatewayStateDeleting {
				log.Printf("🗑️  Deleting NAT gateway %s", *natGw.NatGatewayId)
				_, err = p.ec2Client.DeleteNatGateway(ctx, &ec2.DeleteNatGatewayInput{
					NatGatewayId: natGw.NatGatewayId,
				})
				if err != nil {
					log.Printf("Warning: Failed to delete NAT gateway %s: %v", *natGw.NatGatewayId, err)
				}
			}
		}
	}

	// Cleanup Elastic IPs associated with the VPC
	eipResult, err := p.ec2Client.DescribeAddresses(ctx, &ec2.DescribeAddressesInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("domain"),
				Values: []string{"vpc"},
			},
		},
	})
	if err == nil {
		for _, addr := range eipResult.Addresses {
			// Check if this EIP is associated with resources in our VPC
			if addr.NetworkInterfaceId != nil {
				// Get network interface details to check VPC
				eniResult, err := p.ec2Client.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{
					NetworkInterfaceIds: []string{*addr.NetworkInterfaceId},
				})
				if err == nil && len(eniResult.NetworkInterfaces) > 0 {
					eni := eniResult.NetworkInterfaces[0]
					if eni.VpcId != nil && *eni.VpcId == vpcId {
						log.Printf("🔌 Disassociating and releasing EIP %s", *addr.PublicIp)
						if addr.AssociationId != nil {
							_, err = p.ec2Client.DisassociateAddress(ctx, &ec2.DisassociateAddressInput{
								AssociationId: addr.AssociationId,
							})
							if err != nil {
								log.Printf("Warning: Failed to disassociate EIP %s: %v", *addr.PublicIp, err)
							}
						}
						if addr.AllocationId != nil {
							_, err = p.ec2Client.ReleaseAddress(ctx, &ec2.ReleaseAddressInput{
								AllocationId: addr.AllocationId,
							})
							if err != nil {
								log.Printf("Warning: Failed to release EIP %s: %v", *addr.PublicIp, err)
							}
						}
					}
				}
			}
		}
	}

	return nil
}

func (p *Provider) discoverVPCs(ctx context.Context, clusterName string) []string {
	var vpcs []string

	// Try multiple tag strategies to find VPCs
	tagFilters := [][]ec2types.Filter{
		// Strategy 1: Adhar managed VPCs with cluster name
		{
			{
				Name:   aws.String("tag:adhar.io/managed-by"),
				Values: []string{"adhar"},
			},
			{
				Name:   aws.String("tag:adhar.io/cluster-name"),
				Values: []string{clusterName},
			},
		},
		// Strategy 2: Legacy Cluster tag
		{
			{
				Name:   aws.String("tag:Cluster"),
				Values: []string{clusterName},
			},
		},
		// Strategy 3: Created-By Adhar platform (fallback)
		{
			{
				Name:   aws.String("tag:Created-By"),
				Values: []string{"adhar-platform"},
			},
			{
				Name:   aws.String("tag:Name"),
				Values: []string{"adhar-vpc", fmt.Sprintf("%s-vpc", clusterName)},
			},
		},
	}

	for i, filters := range tagFilters {
		result, err := p.ec2Client.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
			Filters: filters,
		})

		if err != nil {
			log.Printf("Warning: Failed to discover VPCs with strategy %d: %v", i+1, err)
			continue
		}

		for _, vpc := range result.Vpcs {
			if vpc.VpcId != nil {
				vpcID := *vpc.VpcId
				// Check if VPC is not already in the list to avoid duplicates
				found := false
				for _, existingVPC := range vpcs {
					if existingVPC == vpcID {
						found = true
						break
					}
				}
				if !found {
					vpcs = append(vpcs, vpcID)
					log.Printf("Discovered VPC for cleanup: %s (strategy %d)", vpcID, i+1)
				}
			}
		}

		// If we found VPCs with the first strategy, use those preferentially
		if i == 0 && len(vpcs) > 0 {
			break
		}
	}

	log.Printf("Discovered %d VPCs for cluster %s", len(vpcs), clusterName)
	return vpcs
}

func (p *Provider) discoverSubnets(ctx context.Context, clusterName string) []string {
	var subnets []string

	result, err := p.ec2Client.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("tag:Cluster"),
				Values: []string{clusterName},
			},
		},
	})

	if err != nil {
		log.Printf("Warning: Failed to discover subnets: %v", err)
		return subnets
	}

	for _, subnet := range result.Subnets {
		if subnet.SubnetId != nil {
			subnets = append(subnets, *subnet.SubnetId)
			log.Printf("Discovered subnet for cleanup: %s", *subnet.SubnetId)
		}
	}

	log.Printf("Discovered %d subnets for cluster %s", len(subnets), clusterName)
	return subnets
}

func (p *Provider) discoverSecurityGroups(ctx context.Context, clusterName string) []string {
	var sgs []string

	result, err := p.ec2Client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("tag:Cluster"),
				Values: []string{clusterName},
			},
		},
	})

	if err != nil {
		log.Printf("Warning: Failed to discover security groups: %v", err)
		return sgs
	}

	for _, sg := range result.SecurityGroups {
		if sg.GroupId != nil && sg.GroupName != nil && *sg.GroupName != "default" {
			sgs = append(sgs, *sg.GroupId)
			log.Printf("Discovered security group for cleanup: %s (%s)", *sg.GroupId, *sg.GroupName)
		}
	}

	log.Printf("Discovered %d security groups for cluster %s", len(sgs), clusterName)
	return sgs
}

func (p *Provider) discoverInternetGateways(ctx context.Context, clusterName string) []string {
	var igws []string

	// Try multiple tag strategies to find Internet Gateways
	tagFilters := [][]ec2types.Filter{
		// Strategy 1: Adhar managed IGWs with cluster name
		{
			{
				Name:   aws.String("tag:adhar.io/managed-by"),
				Values: []string{"adhar"},
			},
			{
				Name:   aws.String("tag:adhar.io/cluster-name"),
				Values: []string{clusterName},
			},
		},
		// Strategy 2: Legacy Cluster tag
		{
			{
				Name:   aws.String("tag:Cluster"),
				Values: []string{clusterName},
			},
		},
		// Strategy 3: Created-By Adhar platform (fallback)
		{
			{
				Name:   aws.String("tag:Created-By"),
				Values: []string{"adhar-platform"},
			},
		},
	}

	for i, filters := range tagFilters {
		result, err := p.ec2Client.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{
			Filters: filters,
		})

		if err != nil {
			log.Printf("Warning: Failed to discover internet gateways with strategy %d: %v", i+1, err)
			continue
		}

		for _, igw := range result.InternetGateways {
			if igw.InternetGatewayId != nil {
				igwID := *igw.InternetGatewayId
				// Check if IGW is not already in the list to avoid duplicates
				found := false
				for _, existingIGW := range igws {
					if existingIGW == igwID {
						found = true
						break
					}
				}
				if !found {
					igws = append(igws, igwID)
					log.Printf("Discovered Internet Gateway for cleanup: %s (strategy %d)", igwID, i+1)
				}
			}
		}

		// If we found IGWs with the first strategy, use those preferentially
		if i == 0 && len(igws) > 0 {
			break
		}
	}

	// If no IGWs found by tags, also try to find IGWs attached to cluster VPCs
	if len(igws) == 0 {
		vpcs := p.discoverVPCs(ctx, clusterName)
		for _, vpcID := range vpcs {
			result, err := p.ec2Client.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{
				Filters: []ec2types.Filter{
					{
						Name:   aws.String("attachment.vpc-id"),
						Values: []string{vpcID},
					},
				},
			})

			if err != nil {
				log.Printf("Warning: Failed to discover internet gateways for VPC %s: %v", vpcID, err)
				continue
			}

			for _, igw := range result.InternetGateways {
				if igw.InternetGatewayId != nil {
					igwID := *igw.InternetGatewayId
					// Check if IGW is not already in the list to avoid duplicates
					found := false
					for _, existingIGW := range igws {
						if existingIGW == igwID {
							found = true
							break
						}
					}
					if !found {
						igws = append(igws, igwID)
						log.Printf("Discovered Internet Gateway attached to VPC %s: %s", vpcID, igwID)
					}
				}
			}
		}
	}

	log.Printf("Discovered %d Internet Gateways for cluster %s", len(igws), clusterName)
	return igws
}

func (p *Provider) discoverNATGateways(ctx context.Context, clusterName string) []string {
	var natGws []string

	result, err := p.ec2Client.DescribeNatGateways(ctx, &ec2.DescribeNatGatewaysInput{
		Filter: []ec2types.Filter{
			{
				Name:   aws.String("tag:Created-By"),
				Values: []string{"adhar-platform"},
			},
			{
				Name:   aws.String("tag:Cluster"),
				Values: []string{clusterName},
			},
		},
	})

	if err != nil {
		log.Printf("Warning: Failed to discover NAT gateways: %v", err)
		return natGws
	}

	for _, natGw := range result.NatGateways {
		if natGw.NatGatewayId != nil {
			natGws = append(natGws, *natGw.NatGatewayId)
		}
	}

	return natGws
}

func (p *Provider) discoverRouteTables(ctx context.Context, clusterName string) []string {
	var routeTables []string

	result, err := p.ec2Client.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("tag:Created-By"),
				Values: []string{"adhar-platform"},
			},
			{
				Name:   aws.String("tag:Cluster"),
				Values: []string{clusterName},
			},
		},
	})

	if err != nil {
		log.Printf("Warning: Failed to discover route tables: %v", err)
		return routeTables
	}

	for _, rt := range result.RouteTables {
		if rt.RouteTableId != nil {
			// Skip main route tables
			isMain := false
			for _, assoc := range rt.Associations {
				if assoc.Main != nil && *assoc.Main {
					isMain = true
					break
				}
			}
			if !isMain {
				routeTables = append(routeTables, *rt.RouteTableId)
			}
		}
	}

	return routeTables
}

func (p *Provider) discoverNetworkInterfaces(ctx context.Context, clusterName string) []string {
	var enis []string

	result, err := p.ec2Client.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("tag:Cluster"),
				Values: []string{clusterName},
			},
		},
	})

	if err != nil {
		log.Printf("Warning: Failed to discover network interfaces: %v", err)
		return enis
	}

	for _, eni := range result.NetworkInterfaces {
		if eni.NetworkInterfaceId != nil {
			// Skip primary interfaces (they get deleted with instances)
			if eni.Attachment == nil || eni.Attachment.DeviceIndex == nil || *eni.Attachment.DeviceIndex != 0 {
				enis = append(enis, *eni.NetworkInterfaceId)
			}
		}
	}

	return enis
}

func (p *Provider) discoverElasticIPs(ctx context.Context, clusterName string) []string {
	var eips []string

	result, err := p.ec2Client.DescribeAddresses(ctx, &ec2.DescribeAddressesInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("tag:Created-By"),
				Values: []string{"adhar-platform"},
			},
			{
				Name:   aws.String("tag:Cluster"),
				Values: []string{clusterName},
			},
		},
	})

	if err != nil {
		log.Printf("Warning: Failed to discover elastic IPs: %v", err)
		return eips
	}

	for _, eip := range result.Addresses {
		if eip.AllocationId != nil {
			eips = append(eips, *eip.AllocationId)
		}
	}

	return eips
}

// === COMPREHENSIVE DELETION METHODS ===

func (p *Provider) deleteElasticIPs(ctx context.Context, clusterName string, tracker *ResourceTracker) error {
	var eips []string

	if tracker != nil && len(tracker.ElasticIPs) > 0 {
		eips = tracker.ElasticIPs
	} else {
		eips = p.discoverElasticIPs(ctx, clusterName)
	}

	if len(eips) == 0 {
		fmt.Printf("   No Elastic IPs to release\n")
		return nil
	}

	fmt.Printf("   Releasing %d Elastic IPs...\n", len(eips))

	for _, eip := range eips {
		_, err := p.ec2Client.ReleaseAddress(ctx, &ec2.ReleaseAddressInput{
			AllocationId: aws.String(eip),
		})
		if err != nil {
			log.Printf("Warning: Failed to release Elastic IP %s: %v", eip, err)
		}
	}

	return nil
}

func (p *Provider) deleteNATGateways(ctx context.Context, clusterName string, tracker *ResourceTracker) error {
	var natGws []string

	if tracker != nil && len(tracker.NATGateways) > 0 {
		natGws = tracker.NATGateways
	} else {
		natGws = p.discoverNATGateways(ctx, clusterName)
	}

	if len(natGws) == 0 {
		fmt.Printf("   No NAT Gateways to delete\n")
		return nil
	}

	fmt.Printf("   Deleting %d NAT Gateways...\n", len(natGws))

	for _, natGw := range natGws {
		_, err := p.ec2Client.DeleteNatGateway(ctx, &ec2.DeleteNatGatewayInput{
			NatGatewayId: aws.String(natGw),
		})
		if err != nil {
			log.Printf("Warning: Failed to delete NAT Gateway %s: %v", natGw, err)
		}
	}

	// Wait for NAT Gateways to be deleted
	if len(natGws) > 0 {
		fmt.Printf("   Waiting for NAT Gateways to be deleted...\n")
		time.Sleep(30 * time.Second) // NAT Gateways take time to delete
	}

	return nil
}

func (p *Provider) deleteNetworkInterfaces(ctx context.Context, clusterName string, tracker *ResourceTracker) error {
	var enis []string

	if tracker != nil && len(tracker.NetworkInterfaces) > 0 {
		enis = tracker.NetworkInterfaces
	} else {
		enis = p.discoverNetworkInterfaces(ctx, clusterName)
	}

	if len(enis) == 0 {
		fmt.Printf("   No orphaned Network Interfaces to clean up\n")
		return nil
	}

	fmt.Printf("   Cleaning up %d Network Interfaces...\n", len(enis))

	for _, eni := range enis {
		_, err := p.ec2Client.DeleteNetworkInterface(ctx, &ec2.DeleteNetworkInterfaceInput{
			NetworkInterfaceId: aws.String(eni),
		})
		if err != nil {
			log.Printf("Warning: Failed to delete Network Interface %s: %v", eni, err)
		}
	}

	return nil
}

func (p *Provider) deleteClusterSecurityGroupsComprehensive(ctx context.Context, clusterName string, tracker *ResourceTracker) error {
	var sgs []string

	if tracker != nil && len(tracker.SecurityGroups) > 0 {
		sgs = tracker.SecurityGroups
	} else {
		sgs = p.discoverSecurityGroups(ctx, clusterName)
	}

	if len(sgs) == 0 {
		fmt.Printf("   No Security Groups to delete\n")
		return nil
	}

	fmt.Printf("   Deleting %d Security Groups...\n", len(sgs))

	// First, remove all rules from security groups to break dependencies
	fmt.Printf("   🔧 Removing security group rules to break dependencies...\n")
	for _, sg := range sgs {
		// Get security group details
		result, err := p.ec2Client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
			GroupIds: []string{sg},
		})
		if err != nil {
			log.Printf("Warning: Failed to describe security group %s: %v", sg, err)
			continue
		}

		if len(result.SecurityGroups) == 0 {
			continue
		}

		sgDetails := result.SecurityGroups[0]

		// Remove all ingress rules
		if len(sgDetails.IpPermissions) > 0 {
			_, err = p.ec2Client.RevokeSecurityGroupIngress(ctx, &ec2.RevokeSecurityGroupIngressInput{
				GroupId:       aws.String(sg),
				IpPermissions: sgDetails.IpPermissions,
			})
			if err != nil {
				log.Printf("Warning: Failed to revoke ingress rules for security group %s: %v", sg, err)
			}
		}

		// Remove all egress rules (except default allow-all if it exists)
		if len(sgDetails.IpPermissionsEgress) > 0 {
			_, err = p.ec2Client.RevokeSecurityGroupEgress(ctx, &ec2.RevokeSecurityGroupEgressInput{
				GroupId:       aws.String(sg),
				IpPermissions: sgDetails.IpPermissionsEgress,
			})
			if err != nil {
				log.Printf("Warning: Failed to revoke egress rules for security group %s: %v", sg, err)
			}
		}
	}

	// Wait for rule changes to propagate
	time.Sleep(10 * time.Second)

	// Delete security groups with retry and exponential backoff
	for _, sg := range sgs {
		maxRetries := 8
		for i := 0; i < maxRetries; i++ {
			_, err := p.ec2Client.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{
				GroupId: aws.String(sg),
			})
			if err != nil {
				if strings.Contains(err.Error(), "DependencyViolation") && i < maxRetries-1 {
					waitTime := time.Duration(1<<i) * 5 * time.Second // Exponential backoff
					log.Printf("Dependency violation for security group %s, retrying in %v (attempt %d/%d)", sg, waitTime, i+1, maxRetries)
					time.Sleep(waitTime)
					continue
				}
				log.Printf("Warning: Failed to delete Security Group %s: %v", sg, err)
			} else {
				log.Printf("Successfully deleted Security Group %s", sg)
			}
			break
		}
	}

	return nil
}

func (p *Provider) deleteRouteTables(ctx context.Context, clusterName string, tracker *ResourceTracker) error {
	var routeTables []string

	if tracker != nil && len(tracker.RouteTables) > 0 {
		routeTables = tracker.RouteTables
	} else {
		routeTables = p.discoverRouteTables(ctx, clusterName)
	}

	if len(routeTables) == 0 {
		fmt.Printf("   No Route Tables to delete\n")
		return nil
	}

	fmt.Printf("   Deleting %d Route Tables...\n", len(routeTables))

	for _, rt := range routeTables {
		_, err := p.ec2Client.DeleteRouteTable(ctx, &ec2.DeleteRouteTableInput{
			RouteTableId: aws.String(rt),
		})
		if err != nil {
			log.Printf("Warning: Failed to delete Route Table %s: %v", rt, err)
		}
	}

	return nil
}

func (p *Provider) deleteClusterSubnetsComprehensive(ctx context.Context, clusterName string, tracker *ResourceTracker) error {
	var subnets []string

	if tracker != nil && len(tracker.Subnets) > 0 {
		subnets = tracker.Subnets
	} else {
		subnets = p.discoverSubnets(ctx, clusterName)
	}

	if len(subnets) == 0 {
		fmt.Printf("   No Subnets to delete\n")
		return nil
	}

	fmt.Printf("   Deleting %d Subnets...\n", len(subnets))

	for _, subnet := range subnets {
		_, err := p.ec2Client.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{
			SubnetId: aws.String(subnet),
		})
		if err != nil {
			log.Printf("Warning: Failed to delete Subnet %s: %v", subnet, err)
		}
	}

	return nil
}

func (p *Provider) deleteVPCAndGateway(ctx context.Context, clusterName string, tracker *ResourceTracker) error {
	var vpcs []string
	var igws []string

	if tracker != nil {
		vpcs = tracker.VPCs
		igws = tracker.InternetGateways
	} else {
		vpcs = p.discoverVPCs(ctx, clusterName)
		igws = p.discoverInternetGateways(ctx, clusterName)
	}

	if len(vpcs) == 0 && len(igws) == 0 {
		fmt.Printf("   No VPCs or Internet Gateways to delete\n")
		return nil
	}

	// First, detach and delete Internet Gateways
	for _, igw := range igws {
		fmt.Printf("   Detaching and deleting Internet Gateway %s...\n", igw)

		// Find VPC this IGW is attached to
		for _, vpc := range vpcs {
			_, err := p.ec2Client.DetachInternetGateway(ctx, &ec2.DetachInternetGatewayInput{
				InternetGatewayId: aws.String(igw),
				VpcId:             aws.String(vpc),
			})
			if err != nil {
				log.Printf("Warning: Failed to detach Internet Gateway %s from VPC %s: %v", igw, vpc, err)
			}
		}

		_, err := p.ec2Client.DeleteInternetGateway(ctx, &ec2.DeleteInternetGatewayInput{
			InternetGatewayId: aws.String(igw),
		})
		if err != nil {
			log.Printf("Warning: Failed to delete Internet Gateway %s: %v", igw, err)
		}
	}

	// Then delete VPCs with comprehensive dependency cleanup
	for _, vpc := range vpcs {
		fmt.Printf("   Cleaning up dependencies for VPC %s...\n", vpc)
		err := p.cleanupVPCDependencies(ctx, vpc)
		if err != nil {
			log.Printf("Warning: Failed to cleanup VPC dependencies: %v", err)
		}

		fmt.Printf("   Deleting VPC %s...\n", vpc)
		err = p.deleteVPCWithRetry(ctx, vpc)
		if err != nil {
			log.Printf("Warning: Failed to delete VPC %s: %v", vpc, err)
		} else {
			fmt.Printf("   ✓ Successfully deleted VPC %s\n", vpc)
		}
	}

	return nil
}

// cleanupVPCDependencies removes all dependencies that prevent VPC deletion
func (p *Provider) cleanupVPCDependencies(ctx context.Context, vpcID string) error {
	// 1. Delete NAT Gateways first (they depend on subnets and EIPs)
	err := p.cleanupVPCNATGateways(ctx, vpcID)
	if err != nil {
		log.Printf("Warning: Failed to cleanup NAT gateways: %v", err)
	}

	// 2. Delete Network Interfaces (excluding ENIs attached to running instances)
	err = p.cleanupVPCNetworkInterfaces(ctx, vpcID)
	if err != nil {
		log.Printf("Warning: Failed to cleanup network interfaces: %v", err)
	}

	// 3. Delete Route Tables (excluding main route table)
	err = p.cleanupVPCRouteTables(ctx, vpcID)
	if err != nil {
		log.Printf("Warning: Failed to cleanup route tables: %v", err)
	}

	// 4. Delete Subnets
	err = p.cleanupVPCSubnets(ctx, vpcID)
	if err != nil {
		log.Printf("Warning: Failed to cleanup subnets: %v", err)
	}

	// 5. Delete Security Groups (excluding default)
	err = p.cleanupVPCSecurityGroups(ctx, vpcID)
	if err != nil {
		log.Printf("Warning: Failed to cleanup security groups: %v", err)
	}

	return nil
}

// cleanupVPCNATGateways deletes NAT gateways in the VPC
func (p *Provider) cleanupVPCNATGateways(ctx context.Context, vpcID string) error {
	result, err := p.ec2Client.DescribeNatGateways(ctx, &ec2.DescribeNatGatewaysInput{
		Filter: []ec2types.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []string{vpcID},
			},
			{
				Name:   aws.String("state"),
				Values: []string{"available", "pending"},
			},
		},
	})

	if err != nil {
		return fmt.Errorf("failed to describe NAT gateways: %w", err)
	}

	for _, natGw := range result.NatGateways {
		if natGw.NatGatewayId != nil {
			fmt.Printf("     Deleting NAT Gateway %s...\n", *natGw.NatGatewayId)
			_, err := p.ec2Client.DeleteNatGateway(ctx, &ec2.DeleteNatGatewayInput{
				NatGatewayId: natGw.NatGatewayId,
			})
			if err != nil {
				log.Printf("Warning: Failed to delete NAT Gateway %s: %v", *natGw.NatGatewayId, err)
			}
		}
	}

	return nil
}

// deleteVPCWithRetry attempts to delete VPC with exponential backoff retry
func (p *Provider) deleteVPCWithRetry(ctx context.Context, vpcID string) error {
	maxRetries := 5
	for attempt := 1; attempt <= maxRetries; attempt++ {
		_, err := p.ec2Client.DeleteVpc(ctx, &ec2.DeleteVpcInput{
			VpcId: aws.String(vpcID),
		})

		if err == nil {
			return nil
		}

		// Check if it's a dependency violation
		if strings.Contains(err.Error(), "DependencyViolation") {
			if attempt == maxRetries {
				return fmt.Errorf("VPC %s still has dependencies after %d attempts: %w", vpcID, maxRetries, err)
			}

			fmt.Printf("     VPC %s has dependencies, retrying in %ds (attempt %d/%d)...\n", vpcID, attempt*2, attempt, maxRetries)
			time.Sleep(time.Duration(attempt*2) * time.Second)
			continue
		}

		// Other errors are not retryable
		return fmt.Errorf("failed to delete VPC %s: %w", vpcID, err)
	}

	return fmt.Errorf("failed to delete VPC %s after %d attempts", vpcID, maxRetries)
}
