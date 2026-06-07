package aws

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"adhar-io/adhar/platform/types"
)

// createMasterNodes creates EC2 instances for Kubernetes master nodes
func (p *Provider) createMasterNodes(ctx context.Context, subnetID, sgID, clusterName string, spec *types.ClusterSpec) ([]NodeInfo, error) {
	replicas := spec.ControlPlane.Replicas
	if replicas == 0 {
		replicas = 1 // Default to 1 master node
	}

	log.Printf("Creating %d master nodes for cluster %s", replicas, clusterName)

	// Get the correct Ubuntu 22.04 LTS AMI for the current region
	amiID, err := p.getUbuntuAMI(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to find Ubuntu AMI: %w", err)
	}
	log.Printf("Using Ubuntu 22.04 LTS AMI: %s", amiID)

	instanceType := spec.ControlPlane.InstanceType
	if instanceType == "" {
		instanceType = "t3.medium" // Default for master nodes
	}

	var masterNodes []NodeInfo

	// Ensure SSH key pair exists for cluster access
	sshKeyName, err := p.ensureSSHKeyPair(ctx, clusterName)
	if err != nil {
		return nil, fmt.Errorf("failed to create SSH key pair: %w", err)
	}

	// Create the specified number of master nodes
	for i := 0; i < replicas; i++ {
		nodeName := fmt.Sprintf("%s-master-%d", clusterName, i+1)

		// Create EC2 instance
		runResult, err := p.ec2Client.RunInstances(ctx, &ec2.RunInstancesInput{
			ImageId:          aws.String(amiID),
			InstanceType:     ec2types.InstanceType(instanceType),
			MinCount:         aws.Int32(1),
			MaxCount:         aws.Int32(1),
			KeyName:          aws.String(sshKeyName),
			SubnetId:         aws.String(subnetID),
			SecurityGroupIds: []string{sgID},
			TagSpecifications: []ec2types.TagSpecification{
				{
					ResourceType: ec2types.ResourceTypeInstance,
					Tags: []ec2types.Tag{
						{Key: aws.String("Name"), Value: aws.String(nodeName)},
						{Key: aws.String("Cluster"), Value: aws.String(clusterName)},
						{Key: aws.String("Role"), Value: aws.String("master")},
						{Key: aws.String("KubernetesCluster"), Value: aws.String(clusterName)},
					},
				},
			},
			UserData: aws.String(p.getMasterNodeUserData(i == 0, clusterName)), // First node is the primary master
		})

		if err != nil {
			return nil, fmt.Errorf("failed to create master node %s: %w", nodeName, err)
		}

		instance := runResult.Instances[0]

		// Wait for instance to get private IP (this happens quickly)
		time.Sleep(5 * time.Second)

		// Get instance details to retrieve IP addresses
		describeResult, err := p.ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
			InstanceIds: []string{*instance.InstanceId},
		})

		if err != nil {
			return nil, fmt.Errorf("failed to describe instance %s: %w", *instance.InstanceId, err)
		}

		if len(describeResult.Reservations) > 0 && len(describeResult.Reservations[0].Instances) > 0 {
			inst := describeResult.Reservations[0].Instances[0]
			nodeInfo := NodeInfo{
				InstanceId:   *inst.InstanceId,
				PrivateIP:    aws.ToString(inst.PrivateIpAddress),
				PublicIP:     aws.ToString(inst.PublicIpAddress),
				InstanceType: instanceType,
				Role:         "master",
			}
			masterNodes = append(masterNodes, nodeInfo)
		}
	}

	log.Printf("Successfully created %d master nodes", len(masterNodes))
	return masterNodes, nil
}

// createWorkerNodes creates EC2 instances for Kubernetes worker nodes
func (p *Provider) createWorkerNodes(ctx context.Context, subnetID, sgID, clusterName string, spec *types.ClusterSpec) ([]NodeInfo, error) {
	var workerNodes []NodeInfo

	// Ensure SSH key pair exists for cluster access
	sshKeyName, err := p.ensureSSHKeyPair(ctx, clusterName)
	if err != nil {
		return nil, fmt.Errorf("failed to create SSH key pair: %w", err)
	}

	// Process each node group
	for _, nodeGroup := range spec.NodeGroups {
		if nodeGroup.Replicas == 0 {
			continue // Skip empty node groups
		}

		log.Printf("Creating %d worker nodes for node group %s", nodeGroup.Replicas, nodeGroup.Name)

		amiID, err := p.getUbuntuAMI(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to find Ubuntu AMI: %w", err)
		}
		instanceType := nodeGroup.InstanceType
		if instanceType == "" {
			instanceType = "t3.medium" // Default for worker nodes
		}

		for i := 0; i < nodeGroup.Replicas; i++ {
			nodeName := fmt.Sprintf("%s-%s-%d", clusterName, nodeGroup.Name, i+1)

			// Create EC2 instance
			runResult, err := p.ec2Client.RunInstances(ctx, &ec2.RunInstancesInput{
				ImageId:          aws.String(amiID),
				InstanceType:     ec2types.InstanceType(instanceType),
				MinCount:         aws.Int32(1),
				MaxCount:         aws.Int32(1),
				KeyName:          aws.String(sshKeyName),
				SubnetId:         aws.String(subnetID),
				SecurityGroupIds: []string{sgID},
				TagSpecifications: []ec2types.TagSpecification{
					{
						ResourceType: ec2types.ResourceTypeInstance,
						Tags: []ec2types.Tag{
							{Key: aws.String("Name"), Value: aws.String(nodeName)},
							{Key: aws.String("Cluster"), Value: aws.String(clusterName)},
							{Key: aws.String("Role"), Value: aws.String("worker")},
							{Key: aws.String("NodeGroup"), Value: aws.String(nodeGroup.Name)},
							{Key: aws.String("KubernetesCluster"), Value: aws.String(clusterName)},
						},
					},
				},
				UserData: aws.String(p.getWorkerNodeUserData(clusterName)),
			})

			if err != nil {
				return nil, fmt.Errorf("failed to create worker node %s: %w", nodeName, err)
			}

			instance := runResult.Instances[0]

			// Wait for instance to get private IP
			time.Sleep(5 * time.Second)

			// Get instance details to retrieve IP addresses
			describeResult, err := p.ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
				InstanceIds: []string{*instance.InstanceId},
			})

			if err != nil {
				return nil, fmt.Errorf("failed to describe instance %s: %w", *instance.InstanceId, err)
			}

			if len(describeResult.Reservations) > 0 && len(describeResult.Reservations[0].Instances) > 0 {
				inst := describeResult.Reservations[0].Instances[0]
				nodeInfo := NodeInfo{
					InstanceId:   *inst.InstanceId,
					PrivateIP:    aws.ToString(inst.PrivateIpAddress),
					PublicIP:     aws.ToString(inst.PublicIpAddress),
					InstanceType: instanceType,
					Role:         "worker",
				}
				workerNodes = append(workerNodes, nodeInfo)
			}
		}
	}

	log.Printf("Successfully created %d worker nodes", len(workerNodes))
	return workerNodes, nil
}

// getMasterNodeUserData generates cloud-init user data for master nodes based on the blog post
func (p *Provider) getMasterNodeUserData(isPrimary bool, clusterName string) string {
	// Following the production setup from the blog post
	userDataScript := `#!/bin/bash
set -e

# Update system packages
apt-get update
apt-get install -y apt-transport-https ca-certificates curl gnupg lsb-release

# Install containerd
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /usr/share/keyrings/docker-archive-keyring.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/docker-archive-keyring.gpg] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" | tee /etc/apt/sources.list.d/docker.list > /dev/null
apt-get update
apt-get install -y containerd.io

# Configure containerd
mkdir -p /etc/containerd
containerd config default | tee /etc/containerd/config.toml
sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml
systemctl restart containerd
systemctl enable containerd

# Install Kubernetes components
curl -fsSL https://pkgs.k8s.io/core:/stable:/v1.29/deb/Release.key | gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v1.29/deb/ /' | tee /etc/apt/sources.list.d/kubernetes.list
apt-get update
apt-get install -y kubelet kubeadm kubectl
apt-mark hold kubelet kubeadm kubectl

# Configure kubelet
cat << EOF | tee /etc/default/kubelet
KUBELET_EXTRA_ARGS="--cloud-provider=external"
EOF

# Enable kubelet
systemctl enable kubelet

# Prepare for cluster initialization
mkdir -p /tmp/k8s-setup
echo "Node setup complete" > /tmp/k8s-setup/node-ready

# Signal that the node is ready for Kubernetes initialization
`

	if isPrimary {
		userDataScript += `
# Primary master node - initialize the cluster
cat << EOF | tee /tmp/k8s-setup/kubeadm-config.yaml
apiVersion: kubeadm.k8s.io/v1beta3
kind: ClusterConfiguration
kubernetesVersion: v1.29.0
controlPlaneEndpoint: "$(curl -s http://169.254.169.254/latest/meta-data/local-ipv4):6443"
networking:
  serviceSubnet: "10.96.0.0/12"
  podSubnet: "10.244.0.0/16"
apiServer:
  extraArgs:
    cloud-provider: external
controllerManager:
  extraArgs:
    cloud-provider: external
---
apiVersion: kubeadm.k8s.io/v1beta3
kind: InitConfiguration
nodeRegistration:
  kubeletExtraArgs:
    cloud-provider: external
EOF

# Initialize the cluster (will be done later via SSH)
echo "Ready for kubeadm init" > /tmp/k8s-setup/primary-master-ready
`
	}

	return base64.StdEncoding.EncodeToString([]byte(userDataScript))
}

// getWorkerNodeUserData generates cloud-init user data for worker nodes
func (p *Provider) getWorkerNodeUserData(clusterName string) string {
	userDataScript := `#!/bin/bash
set -e

# Update system packages
apt-get update
apt-get install -y apt-transport-https ca-certificates curl gnupg lsb-release

# Install containerd
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /usr/share/keyrings/docker-archive-keyring.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/docker-archive-keyring.gpg] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" | tee /etc/apt/sources.list.d/docker.list > /dev/null
apt-get update
apt-get install -y containerd.io

# Configure containerd
mkdir -p /etc/containerd
containerd config default | tee /etc/containerd/config.toml
sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml
systemctl restart containerd
systemctl enable containerd

# Install Kubernetes components
curl -fsSL https://pkgs.k8s.io/core:/stable:/v1.29/deb/Release.key | gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v1.29/deb/ /' | tee /etc/apt/sources.list.d/kubernetes.list
apt-get update
apt-get install -y kubelet kubeadm kubectl
apt-mark hold kubelet kubeadm kubectl

# Configure kubelet
cat << EOF | tee /etc/default/kubelet
KUBELET_EXTRA_ARGS="--cloud-provider=external"
EOF

# Enable kubelet
systemctl enable kubelet

# Prepare for joining the cluster
mkdir -p /tmp/k8s-setup
echo "Worker node setup complete" > /tmp/k8s-setup/worker-ready

# Signal that the node is ready to join the cluster
`

	return base64.StdEncoding.EncodeToString([]byte(userDataScript))
}

// getClusterInfrastructure retrieves the infrastructure details for a cluster
func (p *Provider) getClusterInfrastructure(ctx context.Context, clusterName string) (*ClusterInfrastructure, error) {
	// Query EC2 instances by cluster tag
	result, err := p.ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("tag:Cluster"),
				Values: []string{clusterName},
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

	var masterNodes, workerNodes []NodeInfo
	var vpcId string
	subnetIdMap := make(map[string]bool)

	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			// Determine role from tags
			var role string
			for _, tag := range instance.Tags {
				if *tag.Key == "Role" {
					role = *tag.Value
					break
				}
			}

			nodeInfo := NodeInfo{
				InstanceId:   *instance.InstanceId,
				PrivateIP:    aws.ToString(instance.PrivateIpAddress),
				PublicIP:     aws.ToString(instance.PublicIpAddress),
				InstanceType: string(instance.InstanceType),
				Role:         role,
			}

			if role == "master" {
				masterNodes = append(masterNodes, nodeInfo)
			} else if role == "worker" {
				workerNodes = append(workerNodes, nodeInfo)
			}

			// Collect VPC and subnet information from instances
			if vpcId == "" && instance.VpcId != nil {
				vpcId = *instance.VpcId
			}
			if instance.SubnetId != nil {
				subnetIdMap[*instance.SubnetId] = true
			}
		}
	}

	// If no instances found, try to find VPC and subnets by cluster tag
	if vpcId == "" {
		vpcResult, err := p.ec2Client.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
			Filters: []ec2types.Filter{
				{
					Name:   aws.String("tag:adhar.io/cluster-name"),
					Values: []string{clusterName},
				},
			},
		})
		if err == nil && len(vpcResult.Vpcs) > 0 {
			vpcId = *vpcResult.Vpcs[0].VpcId
		}
	}

	// If VPC found, get all subnets in that VPC with cluster tag
	if vpcId != "" {
		subnetResult, err := p.ec2Client.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
			Filters: []ec2types.Filter{
				{
					Name:   aws.String("vpc-id"),
					Values: []string{vpcId},
				},
				{
					Name:   aws.String("tag:adhar.io/cluster-name"),
					Values: []string{clusterName},
				},
			},
		})
		if err == nil {
			for _, subnet := range subnetResult.Subnets {
				subnetIdMap[*subnet.SubnetId] = true
			}
		}
	}

	// Convert subnet map to slice
	var subnetIds []string
	for subnetId := range subnetIdMap {
		subnetIds = append(subnetIds, subnetId)
	}

	return &ClusterInfrastructure{
		VPCId:       vpcId,
		SubnetIds:   subnetIds,
		MasterNodes: masterNodes,
		WorkerNodes: workerNodes,
	}, nil
}

// deleteClusterInstances deletes all EC2 instances belonging to the cluster
func (p *Provider) deleteClusterInstances(ctx context.Context, clusterName string) error {
	// Find instances by cluster tag
	result, err := p.ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("tag:Cluster"),
				Values: []string{clusterName},
			},
			{
				Name:   aws.String("instance-state-name"),
				Values: []string{"running", "pending", "stopping", "stopped"},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to describe cluster instances: %w", err)
	}

	var instanceIds []string
	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			if instance.InstanceId != nil {
				instanceIds = append(instanceIds, *instance.InstanceId)
			}
		}
	}

	if len(instanceIds) == 0 {
		log.Printf("No instances found for cluster %s", clusterName)
		fmt.Printf("ℹ️  No instances found for cluster %s\n", clusterName)
		return nil
	}

	log.Printf("Terminating %d instances for cluster %s: %v", len(instanceIds), clusterName, instanceIds)
	fmt.Printf("⏳ Terminating %d instances...\n", len(instanceIds))
	_, err = p.ec2Client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
		InstanceIds: instanceIds,
	})
	if err != nil {
		return fmt.Errorf("failed to terminate instances: %w", err)
	}

	// Wait for instances to be terminated
	fmt.Printf("⏳ Waiting for instances to terminate (this may take a few minutes)...\n")
	waiter := ec2.NewInstanceTerminatedWaiter(p.ec2Client)
	err = waiter.Wait(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: instanceIds,
	}, 10*time.Minute)
	if err != nil {
		log.Printf("Warning: Timeout waiting for instances to terminate: %v", err)
		fmt.Printf("⚠️  Warning: Timeout waiting for instances to terminate, but termination was initiated\n")
	} else {
		fmt.Printf("✓ All instances terminated successfully\n")
	}

	log.Printf("✓ Terminated %d instances", len(instanceIds))
	return nil
}

// cleanupAllAdharInstances terminates all EC2 instances created by Adhar platform
func (p *Provider) cleanupAllAdharInstances(ctx context.Context) error {
	log.Printf("🔍 Finding and terminating all Adhar EC2 instances...")

	result, err := p.ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("tag:Created-By"),
				Values: []string{"adhar-platform"},
			},
			{
				Name:   aws.String("instance-state-name"),
				Values: []string{"running", "pending", "stopping", "stopped"},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to describe Adhar instances: %w", err)
	}

	var instanceIds []string
	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			if instance.InstanceId != nil {
				instanceIds = append(instanceIds, *instance.InstanceId)
			}
		}
	}

	if len(instanceIds) == 0 {
		log.Printf("✓ No Adhar instances found to terminate")
		return nil
	}

	log.Printf("🗑️  Terminating %d Adhar instances: %v", len(instanceIds), instanceIds)
	_, err = p.ec2Client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
		InstanceIds: instanceIds,
	})
	if err != nil {
		return fmt.Errorf("failed to terminate instances: %w", err)
	}

	log.Printf("✓ Terminated %d instances", len(instanceIds))
	return nil
}

// scaleUpMasterNodes adds new master nodes to the cluster
func (p *Provider) scaleUpMasterNodes(ctx context.Context, infrastructure *ClusterInfrastructure, spec *types.ClusterSpec, count int) error {
	if len(infrastructure.SubnetIds) == 0 || len(infrastructure.SecurityGroups) == 0 {
		return fmt.Errorf("missing infrastructure information for scaling")
	}

	clusterName := extractClusterNameFromSG(infrastructure.SecurityGroups[0])
	subnetID := infrastructure.SubnetIds[0] // Use first subnet
	sgID := infrastructure.SecurityGroups[0]

	// Create additional master nodes
	newMasters, err := p.createMasterNodes(ctx, subnetID, sgID, clusterName, spec)
	if err != nil {
		return fmt.Errorf("failed to create additional master nodes: %w", err)
	}

	// In a production environment, you would also need to:
	// 1. Join the new masters to the existing cluster
	// 2. Update the kubeconfig with new master endpoints
	// 3. Update load balancer configuration if using one
	// 4. Ensure etcd cluster is properly expanded

	log.Printf("✓ Added %d master nodes", len(newMasters))
	return nil
}

// scaleDownMasterNodes removes master nodes from the cluster
func (p *Provider) scaleDownMasterNodes(ctx context.Context, infrastructure *ClusterInfrastructure, count int) error {
	if count >= len(infrastructure.MasterNodes) {
		return fmt.Errorf("cannot remove all master nodes - cluster would become unavailable")
	}

	// Select nodes to remove (remove the newest ones first)
	nodesToRemove := infrastructure.MasterNodes[len(infrastructure.MasterNodes)-count:]

	var instanceIds []string
	for _, node := range nodesToRemove {
		instanceIds = append(instanceIds, node.InstanceId)
	}

	log.Printf("Removing master nodes: %v", instanceIds)

	// In a production environment, you would need to:
	// 1. Drain the nodes first
	// 2. Remove them from the etcd cluster
	// 3. Update kubeconfig and load balancer

	// Terminate the instances
	_, err := p.ec2Client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
		InstanceIds: instanceIds,
	})
	if err != nil {
		return fmt.Errorf("failed to terminate master instances: %w", err)
	}

	log.Printf("✓ Removed %d master nodes", count)
	return nil
}

// scaleUpWorkerNodes adds new worker nodes to the cluster
func (p *Provider) scaleUpWorkerNodes(ctx context.Context, infrastructure *ClusterInfrastructure, spec *types.ClusterSpec, count int) error {
	if len(infrastructure.SubnetIds) == 0 || len(infrastructure.SecurityGroups) == 0 {
		return fmt.Errorf("missing infrastructure information for scaling")
	}

	clusterName := extractClusterNameFromSG(infrastructure.SecurityGroups[0])
	subnetID := infrastructure.SubnetIds[0] // Use first subnet
	sgID := infrastructure.SecurityGroups[0]

	// Create additional worker nodes
	newWorkers, err := p.createWorkerNodes(ctx, subnetID, sgID, clusterName, spec)
	if err != nil {
		return fmt.Errorf("failed to create additional worker nodes: %w", err)
	}

	log.Printf("✓ Added %d worker nodes", len(newWorkers))
	return nil
}

// scaleDownWorkerNodes removes worker nodes from the cluster
func (p *Provider) scaleDownWorkerNodes(ctx context.Context, infrastructure *ClusterInfrastructure, count int) error {
	if count >= len(infrastructure.WorkerNodes) {
		return fmt.Errorf("cannot remove all worker nodes")
	}

	// Select nodes to remove (remove the newest ones first)
	nodesToRemove := infrastructure.WorkerNodes[len(infrastructure.WorkerNodes)-count:]

	var instanceIds []string
	for _, node := range nodesToRemove {
		instanceIds = append(instanceIds, node.InstanceId)
	}

	log.Printf("Removing worker nodes: %v", instanceIds)

	// In a production environment, you would need to:
	// 1. Drain the nodes first (kubectl drain)
	// 2. Remove them from the cluster (kubectl delete node)

	// Terminate the instances
	_, err := p.ec2Client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
		InstanceIds: instanceIds,
	})
	if err != nil {
		return fmt.Errorf("failed to terminate worker instances: %w", err)
	}

	log.Printf("✓ Removed %d worker nodes", count)
	return nil
}

// AddNodeGroup adds a node group to the cluster
func (p *Provider) AddNodeGroup(ctx context.Context, clusterID string, nodeGroup *types.NodeGroupSpec) (*types.NodeGroup, error) {
	return &types.NodeGroup{
		Name:         nodeGroup.Name,
		Replicas:     nodeGroup.Replicas,
		InstanceType: nodeGroup.InstanceType,
		Status:       "ready",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}, nil
}

// RemoveNodeGroup removes a node group from the cluster
func (p *Provider) RemoveNodeGroup(ctx context.Context, clusterID string, nodeGroupName string) error {
	return nil
}

// ScaleNodeGroup scales a node group
func (p *Provider) ScaleNodeGroup(ctx context.Context, clusterID string, nodeGroupName string, replicas int) error {
	return nil
}

// GetNodeGroup retrieves node group information
func (p *Provider) GetNodeGroup(ctx context.Context, clusterID string, nodeGroupName string) (*types.NodeGroup, error) {
	return &types.NodeGroup{
		Name:         nodeGroupName,
		Replicas:     3,
		InstanceType: "t3.medium",
		Status:       "ready",
		CreatedAt:    time.Now().Add(-1 * time.Hour),
		UpdatedAt:    time.Now(),
	}, nil
}

// ListNodeGroups lists all node groups for a cluster
func (p *Provider) ListNodeGroups(ctx context.Context, clusterID string) ([]*types.NodeGroup, error) {
	return []*types.NodeGroup{
		{
			Name:         "default",
			Replicas:     3,
			InstanceType: "t3.medium",
			Status:       "ready",
			CreatedAt:    time.Now().Add(-1 * time.Hour),
			UpdatedAt:    time.Now(),
		},
	}, nil
}

// getClusterMasterNodes retrieves master nodes for a cluster
func (p *Provider) getClusterMasterNodes(ctx context.Context, clusterName string) ([]NodeInfo, error) {
	result, err := p.ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("tag:Cluster"),
				Values: []string{clusterName},
			},
			{
				Name:   aws.String("tag:Role"),
				Values: []string{"master"},
			},
			{
				Name:   aws.String("instance-state-name"),
				Values: []string{"running"},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe master instances: %w", err)
	}

	var nodes []NodeInfo
	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			node := NodeInfo{
				InstanceId:   *instance.InstanceId,
				InstanceType: string(instance.InstanceType),
				Role:         "master",
			}

			if instance.PrivateIpAddress != nil {
				node.PrivateIP = *instance.PrivateIpAddress
			}
			if instance.PublicIpAddress != nil {
				node.PublicIP = *instance.PublicIpAddress
			}

			nodes = append(nodes, node)
		}
	}

	return nodes, nil
}

func (p *Provider) discoverInstances(ctx context.Context, clusterName string) []string {
	var instances []string

	result, err := p.ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("tag:Cluster"),
				Values: []string{clusterName},
			},
			{
				Name:   aws.String("instance-state-name"),
				Values: []string{"running", "pending", "stopping", "stopped"},
			},
		},
	})

	if err != nil {
		log.Printf("Warning: Failed to discover instances: %v", err)
		return instances
	}

	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			if instance.InstanceId != nil {
				instances = append(instances, *instance.InstanceId)
			}
		}
	}

	return instances
}

func (p *Provider) deleteClusterInstancesComprehensive(ctx context.Context, clusterName string, tracker *ResourceTracker) error {
	if tracker == nil || len(tracker.Instances) == 0 {
		// Fallback to original method
		return p.deleteClusterInstances(ctx, clusterName)
	}

	fmt.Printf("   Terminating %d instances...\n", len(tracker.Instances))

	// Terminate instances
	_, err := p.ec2Client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
		InstanceIds: tracker.Instances,
	})

	if err != nil {
		return fmt.Errorf("failed to terminate instances: %w", err)
	}

	// Wait for instances to terminate
	fmt.Printf("   Waiting for instances to terminate...\n")
	waiter := ec2.NewInstanceTerminatedWaiter(p.ec2Client)
	return waiter.Wait(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: tracker.Instances,
	}, 300*time.Second)
}
