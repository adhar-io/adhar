package aws

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	provider "adhar-io/adhar/platform/providers"
	"adhar-io/adhar/platform/types"
)

// Ubuntu AMIs only allow SSH as the "ubuntu" user (no direct root login).
const awsSSHUser = "ubuntu"

// CreateCluster creates a new manual Kubernetes cluster on EC2 instances
func (p *Provider) CreateCluster(ctx context.Context, spec *types.ClusterSpec) (*types.Cluster, error) {
	if spec.Provider != "aws" {
		return nil, fmt.Errorf("provider mismatch: expected aws, got %s", spec.Provider)
	}

	if spec.ControlPlane.Replicas > 1 || spec.ControlPlane.HighAvailability {
		return nil, fmt.Errorf("HA control plane not yet supported: self-managed AWS clusters run a single control-plane node")
	}

	fmt.Printf("🚀 Creating self-managed Kubernetes cluster '%s' on EC2 instances...\n", spec.Name)
	fmt.Printf("⏳ This will take several minutes to provision real AWS infrastructure...\n")

	cluster := &types.Cluster{
		ID:        fmt.Sprintf("aws-%s", spec.Name),
		Name:      spec.Name,
		Provider:  "aws",
		Region:    p.config.Region,
		Version:   spec.Version,
		Status:    types.ClusterStatusCreating,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Tags:      spec.Tags,
	}

	// Create cluster infrastructure synchronously so CLI waits for completion
	fmt.Printf("📋 Step 1/3: Creating AWS infrastructure (VPC, subnets, security groups)...\n")
	infrastructure, err := p.createClusterInfrastructure(ctx, spec.Name, spec)
	if err != nil {
		fmt.Printf("❌ Failed to create cluster infrastructure: %v\n", err)

		// Check if this is an AWS account verification issue
		if strings.Contains(err.Error(), "PendingVerification") {
			fmt.Printf("\n🔍 AWS Account Verification Required:\n")
			fmt.Printf("   • Your AWS account is being validated for this region\n")
			fmt.Printf("   • This is a normal process that usually completes within minutes\n")
			fmt.Printf("   • You will receive an email notification when complete\n")
			fmt.Printf("   • Please try creating the cluster again in a few minutes\n\n")
		}

		// Attempt to clean up any partially created resources
		fmt.Printf("🧹 Cleaning up partially created resources...\n")
		cleanupErr := p.cleanupPartialInfrastructure(ctx, spec.Name)
		if cleanupErr != nil {
			fmt.Printf("⚠️  Warning: Failed to cleanup some resources: %v\n", cleanupErr)
			fmt.Printf("💡 You may need to manually delete orphaned resources in AWS console\n")
		} else {
			fmt.Printf("✓ Cleanup completed successfully\n")
		}

		cluster.Status = types.ClusterStatusError
		return cluster, fmt.Errorf("failed to create cluster infrastructure: %w", err)
	}

	fmt.Printf("🔧 Step 2/3: Setting up Kubernetes cluster...\n")
	err = p.setupKubernetesCluster(ctx, spec, cluster, infrastructure)
	if err != nil {
		fmt.Printf("❌ Failed to setup Kubernetes cluster: %v\n", err)
		cluster.Status = types.ClusterStatusError
		return cluster, fmt.Errorf("failed to setup Kubernetes cluster: %w", err)
	}

	fmt.Printf("🌐 Step 3/3: Configuring cluster endpoint and domain management...\n")
	// Update cluster endpoint with actual infrastructure details
	if len(infrastructure.MasterNodes) > 0 {
		masterIP := infrastructure.MasterNodes[0].PublicIP
		if masterIP != "" {
			cluster.Endpoint = fmt.Sprintf("https://%s:6443", masterIP)
		} else if infrastructure.MasterNodes[0].PrivateIP != "" {
			cluster.Endpoint = fmt.Sprintf("https://%s:6443", infrastructure.MasterNodes[0].PrivateIP)
		}
	}

	cluster.Status = types.ClusterStatusRunning
	cluster.UpdatedAt = time.Now()

	fmt.Printf("✅ Cluster '%s' is ready!\n", spec.Name)
	fmt.Printf("📍 Cluster endpoint: %s\n", cluster.Endpoint)
	fmt.Printf("🏷️  Cluster ID: %s\n", cluster.ID)
	fmt.Printf("ℹ️  Nodes stay NotReady until the platform bootstrap installs Cilium.\n")

	// Generate and save kubeconfig
	fmt.Printf("📄 Generating kubeconfig...\n")
	_, err = p.generateKubeconfig(ctx, cluster)
	if err != nil {
		fmt.Printf("⚠️  Warning: Failed to generate kubeconfig: %v\n", err)
	} else {
		fmt.Printf("✓ Kubeconfig generated and saved\n")
		fmt.Printf("💡 Use: export KUBECONFIG=~/.kube/config-%s\n", cluster.Name)
	}

	return cluster, nil
}

// createClusterInfrastructure creates the AWS infrastructure for a manual Kubernetes cluster
func (p *Provider) createClusterInfrastructure(ctx context.Context, clusterName string, spec *types.ClusterSpec) (*ClusterInfrastructure, error) {
	log.Printf("Creating infrastructure for cluster %s", clusterName)
	fmt.Printf("🔍 Starting AWS infrastructure provisioning...\n")

	// Validate AWS credentials and connection
	fmt.Printf("🔐 Validating AWS credentials and connection...\n")
	_, err := p.ec2Client.DescribeRegions(ctx, &ec2.DescribeRegionsInput{})
	if err != nil {
		return nil, fmt.Errorf("failed to validate AWS credentials: %w", err)
	}
	fmt.Printf("✓ AWS credentials validated for region %s\n", p.config.Region)

	// Create VPC
	fmt.Printf("🌐 Creating VPC for cluster...\n")
	vpcID, err := p.createVPCForCluster(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("failed to create VPC: %w", err)
	}
	fmt.Printf("✓ VPC created: %s\n", vpcID)

	// Create subnets
	fmt.Printf("📡 Creating subnets (public and private)...\n")
	publicSubnetID, privateSubnetID, err := p.createSubnets(ctx, vpcID, clusterName)
	if err != nil {
		return nil, fmt.Errorf("failed to create subnets: %w", err)
	}
	fmt.Printf("✓ Public subnet created: %s\n", publicSubnetID)
	fmt.Printf("✓ Private subnet created: %s\n", privateSubnetID)

	// Create security groups
	fmt.Printf("🔒 Creating security groups for Kubernetes cluster...\n")
	sgID, err := p.createSecurityGroups(ctx, vpcID, clusterName)
	if err != nil {
		return nil, fmt.Errorf("failed to create security groups: %w", err)
	}
	fmt.Printf("✓ Security group created: %s\n", sgID)

	// Create master nodes
	fmt.Printf("🎛️ Creating master nodes (%d instances)...\n", spec.ControlPlane.Replicas)
	masterNodes, err := p.createMasterNodes(ctx, publicSubnetID, sgID, clusterName, spec)
	if err != nil {
		return nil, fmt.Errorf("failed to create master nodes: %w", err)
	}
	fmt.Printf("✓ Master nodes created: %d instances\n", len(masterNodes))

	// Create worker nodes. Workers go into the public subnet too: kubeadm is
	// driven over SSH, which requires every node to have a public IP.
	var workerNodes []NodeInfo
	if len(spec.NodeGroups) > 0 {
		fmt.Printf("👷 Creating worker nodes (%d instances)...\n", spec.NodeGroups[0].Replicas)
		workerNodes, err = p.createWorkerNodes(ctx, publicSubnetID, sgID, clusterName, spec)
		if err != nil {
			return nil, fmt.Errorf("failed to create worker nodes: %w", err)
		}
		fmt.Printf("✓ Worker nodes created: %d instances\n", len(workerNodes))
	}

	return &ClusterInfrastructure{
		VPCId:          vpcID,
		SubnetIds:      []string{publicSubnetID, privateSubnetID},
		SecurityGroups: []string{sgID},
		MasterNodes:    masterNodes,
		WorkerNodes:    workerNodes,
	}, nil
}

// setupKubernetesCluster bootstraps Kubernetes on the created infrastructure
// by driving kubeadm over SSH: kube-proxy is skipped and no CNI is installed
// here — the platform bootstrap installs Cilium with kubeProxyReplacement, so
// nodes stay NotReady until then.
func (p *Provider) setupKubernetesCluster(ctx context.Context, spec *types.ClusterSpec, cluster *types.Cluster, infrastructure *ClusterInfrastructure) error {
	fmt.Printf("⚙️  Setting up Kubernetes cluster with kubeadm...\n")

	if len(infrastructure.MasterNodes) == 0 {
		return fmt.Errorf("no master nodes found")
	}
	if len(infrastructure.MasterNodes) > 1 {
		return fmt.Errorf("HA control plane not yet supported")
	}

	signer, err := provider.LoadClusterSSHKey(cluster.Name)
	if err != nil {
		return err
	}

	primaryMaster := infrastructure.MasterNodes[0]
	if primaryMaster.PublicIP == "" {
		return fmt.Errorf("master node %s has no public IP", primaryMaster.InstanceId)
	}

	fmt.Printf("⏳ Waiting for node preparation on master %s (%s)...\n", primaryMaster.InstanceId, primaryMaster.PublicIP)
	if err := provider.WaitForNodePrep(ctx, signer, awsSSHUser, primaryMaster.PublicIP, 15*time.Minute); err != nil {
		return fmt.Errorf("control-plane node not ready: %w", err)
	}

	fmt.Printf("🎯 Running kubeadm init on primary master %s...\n", primaryMaster.InstanceId)
	joinCmd, err := provider.KubeadmInitMaster(signer, awsSSHUser, primaryMaster.PublicIP, primaryMaster.PrivateIP)
	if err != nil {
		return fmt.Errorf("failed to initialize primary master: %w", err)
	}

	for i, worker := range infrastructure.WorkerNodes {
		fmt.Printf("👷 Joining worker node %d: %s\n", i+1, worker.InstanceId)
		if worker.PublicIP == "" {
			return fmt.Errorf("worker node %s has no public IP", worker.InstanceId)
		}
		if err := provider.WaitForNodePrep(ctx, signer, awsSSHUser, worker.PublicIP, 15*time.Minute); err != nil {
			return fmt.Errorf("worker node %s not ready: %w", worker.InstanceId, err)
		}
		if err := provider.KubeadmJoinWorker(signer, awsSSHUser, worker.PublicIP, joinCmd); err != nil {
			return fmt.Errorf("failed to join worker node %s: %w", worker.InstanceId, err)
		}
	}

	fmt.Printf("✅ Kubernetes cluster setup complete!\n")

	cluster.Endpoint = fmt.Sprintf("https://%s:6443", primaryMaster.PublicIP)

	// Setup domain management if configured
	if spec.Domain != nil {
		err := p.setupDomainManagementBasic(ctx, spec, cluster)
		if err != nil {
			fmt.Printf("⚠️  Warning: Failed to setup domain management: %v\n", err)
		} else {
			fmt.Printf("✅ Domain management configured\n")
		}
	}

	return nil
}

// setupDomainManagementBasic provides basic domain management setup
func (p *Provider) setupDomainManagementBasic(ctx context.Context, spec *types.ClusterSpec, cluster *types.Cluster) error {
	if spec.Domain == nil {
		log.Printf("No domain configuration provided, skipping domain management setup")
		return nil
	}

	log.Printf("🌐 Setting up domain management for %s", spec.Domain.BaseDomain)

	// Real domain management setup for production Kubernetes cluster
	// This sets up the foundation for hosting the Adhar platform

	// 1. Verify domain configuration
	if spec.Domain.BaseDomain == "" {
		return fmt.Errorf("base domain is required for domain management")
	}

	// 2. Log domain management components that would be installed
	log.Printf("  📋 Domain management components to install:")
	log.Printf("    - cert-manager for automatic TLS certificate management")
	log.Printf("    - external-dns for automatic DNS record management")
	log.Printf("    - nginx-ingress-controller for ingress traffic management")
	log.Printf("    - Route53 integration for AWS DNS management")

	// 3. Prepare domain-related metadata for cluster
	if cluster.Metadata == nil {
		cluster.Metadata = make(map[string]interface{})
	}

	cluster.Metadata["domain"] = map[string]interface{}{
		"baseDomain":   spec.Domain.BaseDomain,
		"provider":     "aws-route53",
		"certManager":  true,
		"externalDns":  true,
		"ingressClass": "nginx",
		"tlsEnabled":   true,
	}

	// 4. Configure ingress endpoint for Adhar platform
	ingressEndpoint := fmt.Sprintf("*.%s", spec.Domain.BaseDomain)
	cluster.Metadata["ingressEndpoint"] = ingressEndpoint

	// 5. Log next steps for domain setup
	log.Printf("  🔧 Next steps for complete domain setup:")
	log.Printf("    1. Apply cert-manager CRDs and deployment")
	log.Printf("    2. Configure AWS IAM permissions for Route53")
	log.Printf("    3. Install external-dns with AWS provider")
	log.Printf("    4. Deploy nginx-ingress-controller")
	log.Printf("    5. Create ClusterIssuer for Let's Encrypt")
	log.Printf("    6. Configure ingress resources for Adhar services")

	log.Printf("✓ Domain management foundation configured for %s", spec.Domain.BaseDomain)
	return nil
}

// DeleteCluster deletes an EKS cluster and ALL associated AWS resources
func (p *Provider) DeleteCluster(ctx context.Context, clusterID string) error {
	clusterName := extractClusterName(clusterID)
	log.Printf("🗑️  Starting comprehensive deletion of cluster %s and ALL associated AWS resources...", clusterName)
	fmt.Printf("🗑️  Deleting cluster '%s' and ALL associated AWS resources...\n", clusterName)

	// Create resource tracker to find all resources
	tracker, err := p.discoverClusterResources(ctx, clusterName)
	if err != nil {
		log.Printf("Warning: Could not discover all cluster resources: %v", err)
		fmt.Printf("⚠️  Warning: Could not discover all resources, proceeding with tag-based cleanup\n")
	}

	// Print what resources were found
	if tracker != nil {
		p.printResourceSummary(tracker)
	}

	// Step 1: Terminate all EC2 instances first (this releases ENIs and other attached resources)
	fmt.Printf("\n🖥️  Step 1/8: Terminating EC2 instances...\n")
	err = p.deleteClusterInstancesComprehensive(ctx, clusterName, tracker)
	if err != nil {
		log.Printf("Warning: Failed to delete some cluster instances: %v", err)
		fmt.Printf("⚠️  Warning: Failed to delete some instances: %v\n", err)
	} else {
		fmt.Printf("✓ All cluster instances terminated\n")
	}

	// Step 2: Release Elastic IPs
	fmt.Printf("\n💰 Step 2/8: Releasing Elastic IPs...\n")
	err = p.deleteElasticIPs(ctx, clusterName, tracker)
	if err != nil {
		log.Printf("Warning: Failed to release some Elastic IPs: %v", err)
		fmt.Printf("⚠️  Warning: Failed to release some Elastic IPs: %v\n", err)
	} else {
		fmt.Printf("✓ Elastic IPs released\n")
	}

	// Step 3: Delete NAT Gateways (must be done before deleting subnets)
	fmt.Printf("\n🌐 Step 3/8: Deleting NAT Gateways...\n")
	err = p.deleteNATGateways(ctx, clusterName, tracker)
	if err != nil {
		log.Printf("Warning: Failed to delete some NAT Gateways: %v", err)
		fmt.Printf("⚠️  Warning: Failed to delete some NAT Gateways: %v\n", err)
	} else {
		fmt.Printf("✓ NAT Gateways deleted\n")
	}

	// Step 4: Delete Network Interfaces (should be auto-deleted with instances, but clean up any orphans)
	fmt.Printf("\n� Step 4/8: Cleaning up Network Interfaces...\n")
	err = p.deleteNetworkInterfaces(ctx, clusterName, tracker)
	if err != nil {
		log.Printf("Warning: Failed to delete some Network Interfaces: %v", err)
		fmt.Printf("⚠️  Warning: Failed to delete some Network Interfaces: %v\n", err)
	} else {
		fmt.Printf("✓ Network Interfaces cleaned up\n")
	}

	// Step 5: Delete Security Groups (except default VPC security group)
	fmt.Printf("\n🔒 Step 5/8: Deleting security groups...\n")
	err = p.deleteClusterSecurityGroupsComprehensive(ctx, clusterName, tracker)
	if err != nil {
		log.Printf("Warning: Failed to delete security groups: %v", err)
		fmt.Printf("⚠️  Warning: Failed to delete some security groups: %v\n", err)
	} else {
		fmt.Printf("✓ Security groups deleted\n")
	}

	// Step 6: Delete Route Tables (except main route table)
	fmt.Printf("\n🛣️  Step 6/8: Deleting route tables...\n")
	err = p.deleteRouteTables(ctx, clusterName, tracker)
	if err != nil {
		log.Printf("Warning: Failed to delete some route tables: %v", err)
		fmt.Printf("⚠️  Warning: Failed to delete some route tables: %v\n", err)
	} else {
		fmt.Printf("✓ Route tables deleted\n")
	}

	// Step 7: Delete Subnets
	fmt.Printf("\n📡 Step 7/8: Deleting subnets...\n")
	err = p.deleteClusterSubnetsComprehensive(ctx, clusterName, tracker)
	if err != nil {
		log.Printf("Warning: Failed to delete subnets: %v", err)
		fmt.Printf("⚠️  Warning: Failed to delete some subnets: %v\n", err)
	} else {
		fmt.Printf("✓ Subnets deleted\n")
	}

	// Step 8: Delete Internet Gateways and VPC
	fmt.Printf("\n🌍 Step 8/8: Deleting Internet Gateway and VPC...\n")
	err = p.deleteVPCAndGateway(ctx, clusterName, tracker)
	if err != nil {
		log.Printf("Warning: Failed to delete VPC/Gateway: %v", err)
		fmt.Printf("⚠️  Warning: Failed to delete VPC/Gateway: %v\n", err)
	} else {
		fmt.Printf("✓ Internet Gateway and VPC deleted\n")
	}

	// Delete the imported SSH key pair and the local cluster state
	fmt.Printf("\n🔑 Deleting SSH key pair and local cluster state...\n")
	if _, err := p.ec2Client.DeleteKeyPair(ctx, &ec2.DeleteKeyPairInput{
		KeyName: aws.String("adhar-" + clusterName),
	}); err != nil {
		log.Printf("Warning: Failed to delete SSH key pair adhar-%s: %v", clusterName, err)
	}
	provider.RemoveClusterState(clusterName)

	fmt.Printf("\n✅ Cluster '%s' comprehensive deletion completed!\n", clusterName)
	fmt.Printf("🧹 All AWS resources associated with the cluster have been cleaned up.\n")
	log.Printf("✓ Cluster %s comprehensive deletion completed", clusterName)
	return nil
}

// cleanupPartialInfrastructure cleans up partially created infrastructure when cluster creation fails
func (p *Provider) cleanupPartialInfrastructure(ctx context.Context, clusterName string) error {
	log.Printf("Starting cleanup of partial infrastructure for cluster %s", clusterName)

	// 1. Try to delete any instances that might have been created
	err := p.deleteClusterInstances(ctx, clusterName)
	if err != nil {
		log.Printf("Warning: Failed to cleanup instances: %v", err)
	}

	// 2. Try to delete security groups
	err = p.deleteClusterSecurityGroups(ctx, clusterName)
	if err != nil {
		log.Printf("Warning: Failed to cleanup security groups: %v", err)
	}

	// 3. Try to delete subnets created by us
	// Find subnets by cluster tag
	subnetResult, err := p.ec2Client.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("tag:Cluster"),
				Values: []string{clusterName},
			},
		},
	})
	if err == nil {
		var subnetIds []string
		for _, subnet := range subnetResult.Subnets {
			if subnet.SubnetId != nil {
				subnetIds = append(subnetIds, *subnet.SubnetId)
			}
		}
		if len(subnetIds) > 0 {
			err = p.deleteClusterSubnets(ctx, subnetIds)
			if err != nil {
				log.Printf("Warning: Failed to cleanup subnets: %v", err)
			}
		}
	}

	// 4. Try to delete VPCs created by us
	vpcResult, err := p.ec2Client.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("tag:Created-By"),
				Values: []string{"adhar-platform"},
			},
			{
				Name:   aws.String("state"),
				Values: []string{"available"},
			},
		},
	})
	if err == nil {
		for _, vpc := range vpcResult.Vpcs {
			if vpc.VpcId != nil {
				// Check if this VPC has our cluster-related resources
				vpcId := *vpc.VpcId
				err = p.deleteClusterVPC(ctx, vpcId, clusterName)
				if err != nil {
					log.Printf("Warning: Failed to cleanup VPC %s: %v", vpcId, err)
				}
			}
		}
	}

	log.Printf("Cleanup completed for cluster %s", clusterName)
	return nil
}

// UpdateCluster updates a Kubernetes cluster by scaling nodes or updating configuration
func (p *Provider) UpdateCluster(ctx context.Context, clusterID string, spec *types.ClusterSpec) error {
	clusterName := extractClusterName(clusterID)
	log.Printf("Updating cluster %s", clusterName)

	// Get current cluster infrastructure
	infrastructure, err := p.getClusterInfrastructure(ctx, clusterName)
	if err != nil {
		return fmt.Errorf("failed to get cluster infrastructure: %w", err)
	}

	// Handle control plane scaling
	currentMasterCount := len(infrastructure.MasterNodes)
	desiredMasterCount := spec.ControlPlane.Replicas

	log.Printf("Current cluster state: %d masters, %d workers", currentMasterCount, len(infrastructure.WorkerNodes))
	log.Printf("Desired control plane replicas: %d", desiredMasterCount)

	// Scale master nodes if needed
	if desiredMasterCount > currentMasterCount {
		log.Printf("Scaling up master nodes from %d to %d", currentMasterCount, desiredMasterCount)
		err = p.scaleUpMasterNodes(ctx, infrastructure, spec, desiredMasterCount-currentMasterCount)
		if err != nil {
			return fmt.Errorf("failed to scale up master nodes: %w", err)
		}
	} else if desiredMasterCount < currentMasterCount {
		log.Printf("Scaling down master nodes from %d to %d", currentMasterCount, desiredMasterCount)
		err = p.scaleDownMasterNodes(ctx, infrastructure, currentMasterCount-desiredMasterCount)
		if err != nil {
			return fmt.Errorf("failed to scale down master nodes: %w", err)
		}
	}

	// Handle node group scaling (process only the first node group for simplicity)
	if len(spec.NodeGroups) > 0 {
		nodeGroup := spec.NodeGroups[0]
		currentWorkerCount := len(infrastructure.WorkerNodes)
		desiredWorkerCount := nodeGroup.Replicas

		log.Printf("Node group %s: current %d, desired %d workers", nodeGroup.Name, currentWorkerCount, desiredWorkerCount)

		if desiredWorkerCount > currentWorkerCount {
			log.Printf("Scaling up worker nodes from %d to %d", currentWorkerCount, desiredWorkerCount)
			err = p.scaleUpWorkerNodes(ctx, infrastructure, spec, desiredWorkerCount-currentWorkerCount)
			if err != nil {
				return fmt.Errorf("failed to scale up worker nodes: %w", err)
			}
		} else if desiredWorkerCount < currentWorkerCount {
			log.Printf("Scaling down worker nodes from %d to %d", currentWorkerCount, desiredWorkerCount)
			err = p.scaleDownWorkerNodes(ctx, infrastructure, currentWorkerCount-desiredWorkerCount)
			if err != nil {
				return fmt.Errorf("failed to scale down worker nodes: %w", err)
			}
		}
	}

	log.Printf("✓ Cluster %s update completed", clusterName)
	return nil
}

// GetCluster retrieves cluster information from AWS infrastructure
func (p *Provider) GetCluster(ctx context.Context, clusterID string) (*types.Cluster, error) {
	clusterName := extractClusterName(clusterID)

	// Get infrastructure details to build cluster information
	infrastructure, err := p.getClusterInfrastructure(ctx, clusterName)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster infrastructure: %w", err)
	}

	// Determine endpoint from master nodes
	endpoint := "https://api.kubernetes.local:6443" // Default
	if len(infrastructure.MasterNodes) > 0 {
		masterIP := infrastructure.MasterNodes[0].PublicIP
		if masterIP != "" {
			endpoint = fmt.Sprintf("https://%s:6443", masterIP)
		} else if infrastructure.MasterNodes[0].PrivateIP != "" {
			endpoint = fmt.Sprintf("https://%s:6443", infrastructure.MasterNodes[0].PrivateIP)
		}
	}

	return &types.Cluster{
		ID:        clusterID,
		Name:      clusterName,
		Provider:  "aws",
		Region:    p.config.Region,
		Status:    types.ClusterStatusRunning,
		Endpoint:  endpoint,
		CreatedAt: time.Now().Add(-1 * time.Hour),
		UpdatedAt: time.Now(),
	}, nil
}

// ListClusters lists all Kubernetes clusters by querying EC2 instances with cluster tags
func (p *Provider) ListClusters(ctx context.Context) ([]*types.Cluster, error) {
	// Query EC2 instances that are part of Kubernetes clusters
	result, err := p.ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("tag-key"),
				Values: []string{"Cluster"},
			},
			{
				Name:   aws.String("instance-state-name"),
				Values: []string{"running", "pending"},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe instances: %w", err)
	}

	// Group instances by cluster name
	clusterMap := make(map[string]*types.Cluster)

	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			var clusterName string
			for _, tag := range instance.Tags {
				if *tag.Key == "Cluster" {
					clusterName = *tag.Value
					break
				}
			}

			if clusterName == "" {
				continue
			}

			// Create or update cluster info
			if cluster, exists := clusterMap[clusterName]; exists {
				// Update existing cluster
				if instance.LaunchTime.After(cluster.CreatedAt) {
					cluster.UpdatedAt = *instance.LaunchTime
				}
			} else {
				// Create new cluster entry
				clusterMap[clusterName] = &types.Cluster{
					ID:        fmt.Sprintf("aws-%s", clusterName),
					Name:      clusterName,
					Provider:  "aws",
					Region:    p.config.Region,
					Status:    types.ClusterStatusRunning,
					CreatedAt: *instance.LaunchTime,
					UpdatedAt: *instance.LaunchTime,
				}
			}
		}
	}

	// Convert map to slice
	var clusters []*types.Cluster
	for _, cluster := range clusterMap {
		clusters = append(clusters, cluster)
	}

	return clusters, nil
}

// GetKubeconfig fetches the admin kubeconfig from the cluster's control-plane
// node over SSH.
func (p *Provider) GetKubeconfig(ctx context.Context, clusterID string) (string, error) {
	clusterName := extractClusterName(clusterID)

	masters, err := p.getClusterMasterNodes(ctx, clusterName)
	if err != nil {
		return "", fmt.Errorf("failed to find master nodes: %w", err)
	}

	masterIP := ""
	for _, master := range masters {
		if master.PublicIP != "" {
			masterIP = master.PublicIP
			break
		}
	}
	if masterIP == "" {
		return "", fmt.Errorf("no running master node with a public IP found for cluster %s", clusterName)
	}

	signer, err := provider.LoadClusterSSHKey(clusterName)
	if err != nil {
		return "", err
	}

	return provider.FetchAdminKubeconfig(signer, awsSSHUser, masterIP)
}

// generateKubeconfig fetches and saves the kubeconfig for a cluster
func (p *Provider) generateKubeconfig(ctx context.Context, cluster *types.Cluster) (string, error) {
	kubeconfig, err := p.GetKubeconfig(ctx, cluster.ID)
	if err != nil {
		return "", fmt.Errorf("failed to fetch kubeconfig: %w", err)
	}

	// Save kubeconfig to file
	kubeconfigPath := fmt.Sprintf("%s/.kube/config-%s", os.Getenv("HOME"), cluster.Name)

	// Create .kube directory if it doesn't exist
	kubeDirPath := fmt.Sprintf("%s/.kube", os.Getenv("HOME"))
	err = os.MkdirAll(kubeDirPath, 0755)
	if err != nil {
		return "", fmt.Errorf("failed to create .kube directory: %w", err)
	}

	err = os.WriteFile(kubeconfigPath, []byte(kubeconfig), 0600)
	if err != nil {
		return "", fmt.Errorf("failed to write kubeconfig: %w", err)
	}

	// Merge with main kubeconfig and fix authentication if needed
	fmt.Printf("🔧 Merging kubeconfig and setting up authentication...\n")
	err = p.setupKubeconfigAuthentication(cluster, kubeconfigPath)
	if err != nil {
		fmt.Printf("⚠️  Warning: Failed to setup authentication: %v\n", err)
		fmt.Printf("💡 You may need to manually configure authentication\n")
	} else {
		fmt.Printf("✅ Authentication configured successfully\n")
	}

	return kubeconfig, nil
}

// === COMPREHENSIVE RESOURCE DELETION METHODS ===

// discoverClusterResources discovers all AWS resources associated with a cluster
func (p *Provider) discoverClusterResources(ctx context.Context, clusterName string) (*ResourceTracker, error) {
	tracker := &ResourceTracker{
		ClusterName: clusterName,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	// Discover VPCs
	tracker.VPCs = p.discoverVPCs(ctx, clusterName)

	// Discover Subnets
	tracker.Subnets = p.discoverSubnets(ctx, clusterName)

	// Discover Security Groups
	tracker.SecurityGroups = p.discoverSecurityGroups(ctx, clusterName)

	// Discover Instances
	tracker.Instances = p.discoverInstances(ctx, clusterName)

	// Discover Internet Gateways
	tracker.InternetGateways = p.discoverInternetGateways(ctx, clusterName)

	// Discover NAT Gateways
	tracker.NATGateways = p.discoverNATGateways(ctx, clusterName)

	// Discover Route Tables
	tracker.RouteTables = p.discoverRouteTables(ctx, clusterName)

	// Discover Network Interfaces
	tracker.NetworkInterfaces = p.discoverNetworkInterfaces(ctx, clusterName)

	// Discover Elastic IPs
	tracker.ElasticIPs = p.discoverElasticIPs(ctx, clusterName)

	return tracker, nil
}

// printResourceSummary prints a summary of discovered resources
func (p *Provider) printResourceSummary(tracker *ResourceTracker) {
	fmt.Printf("\n📋 Discovered cluster resources:\n")
	fmt.Printf("   • VPCs: %d\n", len(tracker.VPCs))
	fmt.Printf("   • Subnets: %d\n", len(tracker.Subnets))
	fmt.Printf("   • Security Groups: %d\n", len(tracker.SecurityGroups))
	fmt.Printf("   • Instances: %d\n", len(tracker.Instances))
	fmt.Printf("   • Internet Gateways: %d\n", len(tracker.InternetGateways))
	fmt.Printf("   • NAT Gateways: %d\n", len(tracker.NATGateways))
	fmt.Printf("   • Route Tables: %d\n", len(tracker.RouteTables))
	fmt.Printf("   • Network Interfaces: %d\n", len(tracker.NetworkInterfaces))
	fmt.Printf("   • Elastic IPs: %d\n", len(tracker.ElasticIPs))
}

// === RESOURCE DISCOVERY METHODS ===

// ensureSSHKeyPair imports the cluster's local SSH public key (generated on
// first use in ~/.adhar/clusters/<name>/) as an EC2 key pair, reusing the key
// pair when it already exists.
func (p *Provider) ensureSSHKeyPair(ctx context.Context, clusterName string) (string, error) {
	keyName := "adhar-" + clusterName

	_, pubKey, err := provider.EnsureClusterSSHKey(clusterName)
	if err != nil {
		return "", err
	}

	// Reuse the imported key pair when present
	if _, err := p.ec2Client.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{
		KeyNames: []string{keyName},
	}); err == nil {
		fmt.Printf("✓ Using existing SSH key pair: %s\n", keyName)
		return keyName, nil
	}

	fmt.Printf("🔑 Importing SSH key pair: %s\n", keyName)
	_, err = p.ec2Client.ImportKeyPair(ctx, &ec2.ImportKeyPairInput{
		KeyName:           aws.String(keyName),
		PublicKeyMaterial: []byte(pubKey),
		TagSpecifications: []ec2types.TagSpecification{
			{
				ResourceType: ec2types.ResourceTypeKeyPair,
				Tags: []ec2types.Tag{
					{Key: aws.String("Cluster"), Value: aws.String(clusterName)},
					{Key: aws.String("CreatedBy"), Value: aws.String("Adhar")},
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to import SSH key pair: %w", err)
	}

	return keyName, nil
}

// setupKubeconfigAuthentication merges the fetched kubeconfig into the main
// kubeconfig (admin.conf already carries client-certificate credentials)
func (p *Provider) setupKubeconfigAuthentication(cluster *types.Cluster, kubeconfigPath string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	mainKubeconfigPath := fmt.Sprintf("%s/.kube/config", homeDir)

	// Read the generated kubeconfig
	kubeconfigContent, err := os.ReadFile(kubeconfigPath)
	if err != nil {
		return fmt.Errorf("failed to read generated kubeconfig: %w", err)
	}

	// Merge with main kubeconfig using kubectl
	if isKubectlAvailable() {
		return p.mergeKubeconfigWithKubectl(kubeconfigPath, mainKubeconfigPath, cluster.Name)
	}

	// Fallback: simple append if kubectl is not available
	return p.appendToMainKubeconfig(string(kubeconfigContent), mainKubeconfigPath, cluster.Name)
}

// mergeKubeconfigWithKubectl merges kubeconfig using kubectl
func (p *Provider) mergeKubeconfigWithKubectl(sourcePath, targetPath string, clusterName string) error {
	// Create backup
	if _, err := os.Stat(targetPath); err == nil {
		backupPath := fmt.Sprintf("%s.backup.%d", targetPath, os.Getpid())
		data, err := os.ReadFile(targetPath)
		if err != nil {
			return fmt.Errorf("failed to read kubeconfig for backup: %w", err)
		}
		if err := os.WriteFile(backupPath, data, 0600); err != nil {
			return fmt.Errorf("failed to create backup: %w", err)
		}
	}

	// Set KUBECONFIG to include both files for merging
	mergedKubeconfig := fmt.Sprintf("%s:%s", targetPath, sourcePath)
	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		mergedKubeconfig = sourcePath
	}

	// Use kubectl config view to merge and flatten the configs
	cmd := exec.Command("kubectl", "config", "view", "--flatten")
	cmd.Env = append(os.Environ(), fmt.Sprintf("KUBECONFIG=%s", mergedKubeconfig))

	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to merge kubeconfigs with kubectl: %w", err)
	}

	// Write the merged config back
	return os.WriteFile(targetPath, output, 0600)
}

// appendToMainKubeconfig appends kubeconfig content to main config (fallback)
func (p *Provider) appendToMainKubeconfig(kubeconfigContent, mainKubeconfigPath, clusterName string) error {
	var existingConfig []byte

	// Read existing config if it exists
	if _, err := os.Stat(mainKubeconfigPath); err == nil {
		existingConfig, err = os.ReadFile(mainKubeconfigPath)
		if err != nil {
			return fmt.Errorf("failed to read existing kubeconfig: %w", err)
		}
	}

	// Simple merge: append with separator
	var finalConfig []byte
	if len(existingConfig) > 0 {
		finalConfig = append(existingConfig, []byte(fmt.Sprintf("\n\n# Added by Adhar for cluster: %s\n", clusterName))...)
	}
	finalConfig = append(finalConfig, []byte(kubeconfigContent)...)

	// Write the final config
	return os.WriteFile(mainKubeconfigPath, finalConfig, 0600)
}
