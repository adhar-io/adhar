package aws

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	provider "adhar-io/adhar/platform/providers"
	"adhar-io/adhar/platform/types"
)

// nodeUserData returns the base64-encoded node-preparation script (EC2
// requires user data base64-encoded). Masters and workers run the same
// script; kubeadm is driven over SSH afterwards.
func nodeUserData(spec *types.ClusterSpec) string {
	script := provider.KubeadmNodePrepScript(provider.K8sMinorFromVersion(spec.Version))
	return base64.StdEncoding.EncodeToString([]byte(script))
}

// waitForInstanceIPs waits until an instance is running with both private and
// public IPs assigned (kubeadm is driven over SSH via the public IP).
func (p *Provider) waitForInstanceIPs(ctx context.Context, instanceID string) (*ec2types.Instance, error) {
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for {
		result, err := p.ec2Client.DescribeInstances(waitCtx, &ec2.DescribeInstancesInput{
			InstanceIds: []string{instanceID},
		})
		if err == nil && len(result.Reservations) > 0 && len(result.Reservations[0].Instances) > 0 {
			inst := result.Reservations[0].Instances[0]
			if inst.State != nil && inst.State.Name == ec2types.InstanceStateNameRunning &&
				aws.ToString(inst.PrivateIpAddress) != "" && aws.ToString(inst.PublicIpAddress) != "" {
				return &inst, nil
			}
		}
		select {
		case <-waitCtx.Done():
			return nil, fmt.Errorf("instance %s did not become running with a public IP in time", instanceID)
		case <-time.After(5 * time.Second):
		}
	}
}

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
		// r6i.large (2 vCPU, 16 GiB), not the previous t3.medium. t3 is BURSTABLE:
		// once the control plane exhausts its CPU credits the node is throttled, and
		// a throttled etcd degrades the whole cluster in ways that look like a
		// network problem rather than a CPU one. t3.medium's 4 GiB is also thin for
		// etcd plus the API server on a cluster with ~75 applications' worth of
		// objects — measured: with 8 GiB the API server started failing TLS
		// handshakes during the first sync of 75 applications, and SSH to the node
		// timed out with it. 16 GiB is the shape verified on Azure, and memory does
		// not count against the region's vCPU quota. Override with clusterConfig
		// `controlPlaneMachineType`.
		instanceType = "r6i.large"
	}

	var masterNodes []NodeInfo

	userData := nodeUserData(spec)

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
			ImageId:             aws.String(amiID),
			InstanceType:        ec2types.InstanceType(instanceType),
			MinCount:            aws.Int32(1),
			MaxCount:            aws.Int32(1),
			KeyName:             aws.String(sshKeyName),
			SubnetId:            aws.String(subnetID),
			SecurityGroupIds:    []string{sgID},
			BlockDeviceMappings: p.rootBlockDevice(ctx, amiID),
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
			UserData: aws.String(userData),
		})

		if err != nil {
			return nil, fmt.Errorf("failed to create master node %s: %w", nodeName, err)
		}

		inst, err := p.waitForInstanceIPs(ctx, *runResult.Instances[0].InstanceId)
		if err != nil {
			return nil, fmt.Errorf("master node %s: %w", nodeName, err)
		}

		masterNodes = append(masterNodes, NodeInfo{
			InstanceId:     *inst.InstanceId,
			PrivateIP:      aws.ToString(inst.PrivateIpAddress),
			PublicIP:       aws.ToString(inst.PublicIpAddress),
			PrivateDNSName: aws.ToString(inst.PrivateDnsName),
			InstanceType:   instanceType,
			Role:           "master",
		})
	}

	log.Printf("Successfully created %d master nodes", len(masterNodes))
	return masterNodes, nil
}

// createWorkerNodes creates EC2 instances for Kubernetes worker nodes
func (p *Provider) createWorkerNodes(ctx context.Context, subnetID, sgID, clusterName string, spec *types.ClusterSpec) ([]NodeInfo, error) {
	var workerNodes []NodeInfo

	userData := nodeUserData(spec)

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
			// A worker with 4 GiB cannot hold this platform's share of ~75 apps, and
			// burstable CPU makes the autoscaler's utilisation readings meaningless.
			// r6i.xlarge (4 vCPU, 32 GiB) is the shape verified on Azure, where 4 of
			// them carried the full catalogue.
			instanceType = "r6i.xlarge"
		}

		for i := 0; i < nodeGroup.Replicas; i++ {
			nodeName := fmt.Sprintf("%s-%s-%d", clusterName, nodeGroup.Name, i+1)
			node, err := p.runWorkerInstance(ctx, workerInstanceSpec{
				Name: nodeName, ClusterName: clusterName, NodeGroup: nodeGroup.Name, InstanceType: instanceType,
				AMI: amiID, KeyName: sshKeyName, SubnetID: subnetID, SecurityGroupID: sgID, UserData: userData,
			})
			if err != nil {
				return nil, err
			}
			workerNodes = append(workerNodes, *node)
		}
	}

	log.Printf("Successfully created %d worker nodes", len(workerNodes))
	return workerNodes, nil
}

// workerInstanceSpec is everything one worker EC2 instance needs. One place, so
// the instance a scale-up adds is indistinguishable from one `adhar up` made.
type workerInstanceSpec struct {
	Name, ClusterName, NodeGroup, InstanceType string
	AMI, KeyName, SubnetID, SecurityGroupID    string
	UserData                                   string
}

// rootBlockDevice describes the node's root EBS volume.
//
// This provider used to send no block device mapping at all, so every node
// inherited the AMI's default root volume — 8 GB on the Ubuntu images it
// selects — and `diskSizeGb` in the configuration file reached nothing. On a
// live Singapore cluster that filled to 89% within minutes of boot and the
// kubelet set node.kubernetes.io/disk-pressure, which taints the node and stops
// scheduling: a capacity failure that looks nothing like a disk one.
//
// Two things share this disk: the containerd image cache (~35 GiB for this
// platform's images) and, since the default StorageClass became node-local,
// every PersistentVolume.
//
// deviceName must be the AMI's own root device (/dev/sda1 on these images) —
// naming any other device adds a second, unused volume and leaves the root at
// its default size.
func (p *Provider) rootBlockDevice(ctx context.Context, amiID string) []ec2types.BlockDeviceMapping {
	size := p.config.DiskSizeGB
	if size <= 0 {
		size = defaultRootVolumeSizeGB
	}
	volType := ec2types.VolumeTypeGp3
	if t := strings.TrimSpace(p.config.DiskType); t != "" {
		volType = ec2types.VolumeType(t)
	}

	deviceName := "/dev/sda1"
	if out, err := p.ec2Client.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{amiID}}); err == nil &&
		len(out.Images) > 0 && aws.ToString(out.Images[0].RootDeviceName) != "" {
		deviceName = aws.ToString(out.Images[0].RootDeviceName)
	} else if err != nil {
		log.Printf("WARNING: could not read the root device name for %s (%v); assuming %s", amiID, err, deviceName)
	}

	return []ec2types.BlockDeviceMapping{{
		DeviceName: aws.String(deviceName),
		Ebs: &ec2types.EbsBlockDevice{
			VolumeSize: aws.Int32(size),
			VolumeType: volType,
			// The volume must go when the instance does, or teardown leaves a paid
			// orphan behind for every node the cluster ever had.
			DeleteOnTermination: aws.Bool(true),
			Encrypted:           aws.Bool(true),
		},
	}}
}

// runWorkerInstance launches one tagged worker instance and waits for its IPs.
func (p *Provider) runWorkerInstance(ctx context.Context, w workerInstanceSpec) (*NodeInfo, error) {
	runResult, err := p.ec2Client.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId:             aws.String(w.AMI),
		InstanceType:        ec2types.InstanceType(w.InstanceType),
		MinCount:            aws.Int32(1),
		MaxCount:            aws.Int32(1),
		KeyName:             aws.String(w.KeyName),
		SubnetId:            aws.String(w.SubnetID),
		SecurityGroupIds:    []string{w.SecurityGroupID},
		BlockDeviceMappings: p.rootBlockDevice(ctx, w.AMI),
		TagSpecifications: []ec2types.TagSpecification{
			{
				ResourceType: ec2types.ResourceTypeInstance,
				Tags: []ec2types.Tag{
					{Key: aws.String("Name"), Value: aws.String(w.Name)},
					{Key: aws.String("Cluster"), Value: aws.String(w.ClusterName)},
					{Key: aws.String("Role"), Value: aws.String("worker")},
					{Key: aws.String("NodeGroup"), Value: aws.String(w.NodeGroup)},
					{Key: aws.String("KubernetesCluster"), Value: aws.String(w.ClusterName)},
				},
			},
		},
		UserData: aws.String(w.UserData),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create worker node %s: %w", w.Name, err)
	}
	inst, err := p.waitForInstanceIPs(ctx, *runResult.Instances[0].InstanceId)
	if err != nil {
		return nil, fmt.Errorf("worker node %s: %w", w.Name, err)
	}
	return &NodeInfo{
		InstanceId:     *inst.InstanceId,
		PrivateIP:      aws.ToString(inst.PrivateIpAddress),
		PublicIP:       aws.ToString(inst.PublicIpAddress),
		PrivateDNSName: aws.ToString(inst.PrivateDnsName),
		InstanceType:   w.InstanceType,
		Role:           "worker",
	}, nil
}

// getClusterInfrastructure retrieves the infrastructure details for a cluster
func (p *Provider) getClusterInfrastructure(ctx context.Context, clusterName string) (*ClusterInfrastructure, error) {
	// Accept either spelling of the name. Instances are tagged with the BARE
	// cluster name ("dev"), but this provider's own cluster IDs carry an "aws-"
	// prefix ("aws-dev") and callers pass whichever they happen to hold —
	// ScaleNodeGroup passed the prefixed ID, so the tag filter matched nothing,
	// MasterNodes came back empty, and the node autoscaler failed every tick with
	// "cannot scale cluster aws-dev: control-plane public IP unknown" while 31
	// pods sat Pending for capacity. Normalising here fixes all ten call sites at
	// once, and makes it impossible for a new caller to get it wrong.
	clusterName = extractClusterName(clusterName)

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
	securityGroupMap := make(map[string]bool)

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
				InstanceId:     *instance.InstanceId,
				PrivateIP:      aws.ToString(instance.PrivateIpAddress),
				PublicIP:       aws.ToString(instance.PublicIpAddress),
				PrivateDNSName: aws.ToString(instance.PrivateDnsName),
				InstanceType:   string(instance.InstanceType),
				Role:           role,
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
			// Security groups were never collected here at all, so
			// ClusterInfrastructure.SecurityGroups came back empty on every call
			// and any scale-up refused with "has no subnet/security group to place
			// workers in". Taking them from the running instances means a new
			// worker lands in exactly the same groups as its peers.
			for _, sg := range instance.SecurityGroups {
				if sg.GroupId != nil {
					securityGroupMap[*sg.GroupId] = true
				}
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

	// Fall back to the cluster's tagged security group when no instance is
	// running to copy from — a node group scaled to zero and back has none.
	if len(securityGroupMap) == 0 && vpcId != "" {
		sgResult, sgErr := p.ec2Client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
			Filters: []ec2types.Filter{
				{Name: aws.String("vpc-id"), Values: []string{vpcId}},
				{Name: aws.String("tag:Cluster"), Values: []string{clusterName}},
			},
		})
		if sgErr == nil {
			for _, sg := range sgResult.SecurityGroups {
				if sg.GroupId != nil {
					securityGroupMap[*sg.GroupId] = true
				}
			}
		}
	}
	var securityGroups []string
	for id := range securityGroupMap {
		securityGroups = append(securityGroups, id)
	}
	// Deterministic, so a scale-up does not pick a different group each time the
	// map is ranged over.
	sort.Strings(subnetIds)
	sort.Strings(securityGroups)

	return &ClusterInfrastructure{
		VPCId:          vpcId,
		SubnetIds:      subnetIds,
		SecurityGroups: securityGroups,
		MasterNodes:    masterNodes,
		WorkerNodes:    workerNodes,
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
		fmt.Printf("▸ No instances found for cluster %s\n", clusterName)
		return nil
	}

	log.Printf("Terminating %d instances for cluster %s: %v", len(instanceIds), clusterName, instanceIds)
	fmt.Printf("◌ Terminating %d instances...\n", len(instanceIds))
	_, err = p.ec2Client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
		InstanceIds: instanceIds,
	})
	if err != nil {
		return fmt.Errorf("failed to terminate instances: %w", err)
	}

	// Wait for instances to be terminated
	fmt.Printf("◌ Waiting for instances to terminate (this may take a few minutes)...\n")
	waiter := ec2.NewInstanceTerminatedWaiter(p.ec2Client)
	err = waiter.Wait(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: instanceIds,
	}, 10*time.Minute)
	if err != nil {
		log.Printf("Warning: Timeout waiting for instances to terminate: %v", err)
		fmt.Printf("▲ Warning: Timeout waiting for instances to terminate, but termination was initiated\n")
	} else {
		fmt.Printf("● All instances terminated successfully\n")
	}

	log.Printf("● Terminated %d instances", len(instanceIds))
	return nil
}

// cleanupAllAdharInstances terminates all EC2 instances created by Adhar platform
func (p *Provider) cleanupAllAdharInstances(ctx context.Context) error {
	log.Printf("▸ Finding and terminating all Adhar EC2 instances...")

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
		log.Printf("● No Adhar instances found to terminate")
		return nil
	}

	log.Printf("✖ Terminating %d Adhar instances: %v", len(instanceIds), instanceIds)
	_, err = p.ec2Client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
		InstanceIds: instanceIds,
	})
	if err != nil {
		return fmt.Errorf("failed to terminate instances: %w", err)
	}

	log.Printf("● Terminated %d instances", len(instanceIds))
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

	log.Printf("● Added %d master nodes", len(newMasters))
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

	log.Printf("● Removed %d master nodes", count)
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

	log.Printf("● Added %d worker nodes", len(newWorkers))
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

	log.Printf("● Removed %d worker nodes", count)
	return nil
}

// AddNodeGroup adds a node group to the cluster
func (p *Provider) AddNodeGroup(ctx context.Context, clusterID string, nodeGroup *types.NodeGroupSpec) (*types.NodeGroup, error) {
	if p.isManagedCluster(ctx, clusterID) {
		return p.managedAddNodeGroup(ctx, extractClusterName(clusterID), nodeGroup)
	}
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
	if p.isManagedCluster(ctx, clusterID) {
		return p.managedRemoveNodeGroup(ctx, extractClusterName(clusterID), nodeGroupName)
	}
	return nil
}

// ScaleNodeGroup scales a node group
// ScaleNodeGroup moves a self-managed node group to `replicas` workers with the
// lifecycle DigitalOcean established (platform/providers helpers): a fresh join
// token, prepared-then-joined instances, drain-then-terminate on the way down.
// Members are the running instances tagged Cluster=<cluster>, Role=worker and
// NodeGroup=<group>; their Name tags are `<cluster>-<group>-<n>`. This used to
// return nil without touching an instance, and scaleDownWorkerNodes terminated
// instances without draining them.
func (p *Provider) ScaleNodeGroup(ctx context.Context, clusterID string, nodeGroupName string, replicas int) error {
	if p.isManagedCluster(ctx, clusterID) {
		return p.managedScaleNodeGroup(ctx, extractClusterName(clusterID), nodeGroupName, replicas)
	}
	// Everything below addresses AWS by the BARE cluster name, because that is
	// what the instances carry: their Cluster tag is "dev", their Name tags are
	// "dev-workers-N", and their SSH key lives in ~/.adhar/clusters/dev/. The
	// incoming clusterID is this provider's own id and is prefixed ("aws-dev"),
	// which the autoscaler passes verbatim — and using it unnormalised failed
	// three ways in a row on a live cluster: the tag filter matched nothing
	// ("control-plane public IP unknown"), then the key path was wrong
	// ("open ~/.adhar/clusters/aws-dev/id_ed25519: no such file"), and the
	// instance-name prefix would have produced "aws-dev-workers-3". Normalise
	// once, here.
	clusterName := extractClusterName(clusterID)
	log.Printf("Scaling node group %s in cluster %s to %d replicas", nodeGroupName, clusterName, replicas)
	infra, err := p.getClusterInfrastructure(ctx, clusterName)
	if err != nil {
		return err
	}
	if len(infra.MasterNodes) == 0 || infra.MasterNodes[0].PublicIP == "" {
		return fmt.Errorf("cannot scale cluster %s: control-plane public IP unknown", clusterName)
	}
	members, err := p.nodeGroupInstances(ctx, clusterName, nodeGroupName)
	if err != nil {
		return err
	}
	prefix := fmt.Sprintf("%s-%s-", clusterName, nodeGroupName)
	current := make([]string, 0, len(members))
	for name := range members {
		current = append(current, name)
	}
	add, remove := provider.WorkerScalePlan(prefix, current, replicas)
	if len(add) == 0 && len(remove) == 0 {
		log.Printf("Node group %s already at %d workers", nodeGroupName, replicas)
		return nil
	}
	// Bare name: the key is written to ~/.adhar/clusters/<name>/id_ed25519 at
	// create time, so "aws-dev" here looked for a directory that never exists.
	signer, err := provider.LoadClusterSSHKey(clusterName)
	if err != nil {
		return fmt.Errorf("failed to load cluster SSH key: %w", err)
	}
	masterIP := infra.MasterNodes[0].PublicIP

	if len(add) > 0 {
		if len(infra.SubnetIds) == 0 || len(infra.SecurityGroups) == 0 {
			return fmt.Errorf("cluster %s has no subnet/security group to place workers in", clusterID)
		}
		joinCmd, err := provider.JoinCommand(signer, awsSSHUser, masterIP)
		if err != nil {
			return err
		}
		amiID, err := p.getUbuntuAMI(ctx)
		if err != nil {
			return fmt.Errorf("failed to find Ubuntu AMI: %w", err)
		}
		sshKeyName, err := p.ensureSSHKeyPair(ctx, clusterName)
		if err != nil {
			return fmt.Errorf("failed to resolve the cluster SSH key pair: %w", err)
		}
		instanceType := "t3.medium"
		for _, m := range members {
			if m.InstanceType != "" {
				instanceType = m.InstanceType
				break
			}
		}
		// The same node-prep the cluster was created with; the version comes
		// from the running control plane so a scaled cluster cannot skew.
		spec := &types.ClusterSpec{}
		if c, gerr := p.GetCluster(ctx, clusterID); gerr == nil && c != nil {
			spec.Version = c.Version
		}
		for _, name := range add {
			node, err := p.runWorkerInstance(ctx, workerInstanceSpec{
				Name: name, ClusterName: clusterName, NodeGroup: nodeGroupName, InstanceType: instanceType,
				AMI: amiID, KeyName: sshKeyName, SubnetID: infra.SubnetIds[0], SecurityGroupID: infra.SecurityGroups[0], UserData: nodeUserData(spec),
			})
			if err != nil {
				return err
			}
			if err := provider.WaitForNodePrep(ctx, signer, awsSSHUser, node.PublicIP, 15*time.Minute); err != nil {
				return fmt.Errorf("new worker %s not ready: %w", name, err)
			}
			// The EBS CSI driver is a platform addon on AWS, so a worker carries
			// the CSI startup taint until its CSINode registers; no
			// cloud-controller-manager is installed yet (docs/PROVIDERS.md).
			// externalCCM MUST be true, exactly as it is for a worker created by
			// `adhar up` (provider_cluster.go). It was false, and the whole chain
			// downstream of it failed silently:
			//
			//   no --cloud-provider=external  ->  kubelet registers with no
			//   uninitialized taint, so the CCM never touches the node  ->
			//   .spec.providerID stays empty  ->  the EBS CSI node plugin cannot
			//   identify its instance ("node providerID empty, cannot parse", after
			//   IMDS also times out on the pod network) and crash-loops  ->  no
			//   CSINode object is published  ->  the autoscaler never lifts
			//   node.adhar.io/csi-not-ready:NoSchedule.
			//
			// The visible result is an autoscaled node that is Ready and completely
			// EMPTY while pods stay Pending: measured at 5 nodes with two sitting at
			// 3% CPU and 27 pods unschedulable.
			if err := provider.EnableExternalCloudProvider(signer, awsSSHUser, node.PublicIP, node.PrivateIP, true, true, node.PrivateDNSName); err != nil {
				return fmt.Errorf("new worker %s: %w", name, err)
			}
			if err := provider.KubeadmJoinWorker(signer, awsSSHUser, node.PublicIP, joinCmd, node.PrivateDNSName); err != nil {
				return fmt.Errorf("new worker %s: %w", name, err)
			}
			log.Printf("Added worker %s (%s) to cluster %s", name, node.InstanceId, clusterID)
		}
	}
	for _, name := range remove {
		if err := provider.RetireWorker(signer, awsSSHUser, masterIP, name); err != nil {
			log.Printf("Warning: %v", err)
		}
		if _, err := p.ec2Client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{members[name].InstanceId}}); err != nil {
			return fmt.Errorf("failed to terminate worker %s: %w", name, err)
		}
		log.Printf("Removed worker %s (%s) from cluster %s", name, members[name].InstanceId, clusterID)
	}
	return nil
}

// nodeGroupInstances lists the running workers of one node group by Name tag.
func (p *Provider) nodeGroupInstances(ctx context.Context, clusterName, nodeGroupName string) (map[string]NodeInfo, error) {
	result, err := p.ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("tag:Cluster"), Values: []string{clusterName}},
			{Name: aws.String("tag:Role"), Values: []string{"worker"}},
			{Name: aws.String("tag:NodeGroup"), Values: []string{nodeGroupName}},
			{Name: aws.String("instance-state-name"), Values: []string{"running", "pending"}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list node group %s instances: %w", nodeGroupName, err)
	}
	members := map[string]NodeInfo{}
	for _, r := range result.Reservations {
		for _, inst := range r.Instances {
			name := ""
			for _, tag := range inst.Tags {
				if aws.ToString(tag.Key) == "Name" {
					name = aws.ToString(tag.Value)
				}
			}
			if name == "" {
				continue
			}
			members[name] = NodeInfo{
				InstanceId:     aws.ToString(inst.InstanceId),
				PrivateIP:      aws.ToString(inst.PrivateIpAddress),
				PublicIP:       aws.ToString(inst.PublicIpAddress),
				PrivateDNSName: aws.ToString(inst.PrivateDnsName),
				InstanceType:   string(inst.InstanceType),
				Role:           "worker",
			}
		}
	}
	return members, nil
}

func (p *Provider) GetNodeGroup(ctx context.Context, clusterID string, nodeGroupName string) (*types.NodeGroup, error) {
	if p.isManagedCluster(ctx, clusterID) {
		return p.managedGetNodeGroup(ctx, extractClusterName(clusterID), nodeGroupName)
	}
	members, err := p.nodeGroupInstances(ctx, clusterID, nodeGroupName)
	if err != nil {
		return nil, err
	}
	instanceType := "t3.medium"
	for _, m := range members {
		if m.InstanceType != "" {
			instanceType = m.InstanceType
			break
		}
	}
	return &types.NodeGroup{Name: nodeGroupName, Replicas: len(members), InstanceType: instanceType, Status: "ready", UpdatedAt: time.Now()}, nil
}

func (p *Provider) ListNodeGroups(ctx context.Context, clusterID string) ([]*types.NodeGroup, error) {
	if p.isManagedCluster(ctx, clusterID) {
		return p.managedListNodeGroups(ctx, extractClusterName(clusterID))
	}
	// Group the running workers by their NodeGroup tag; workers created before
	// node groups were tagged report as one "default" group.
	result, err := p.ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("tag:Cluster"), Values: []string{clusterID}},
			{Name: aws.String("tag:Role"), Values: []string{"worker"}},
			{Name: aws.String("instance-state-name"), Values: []string{"running", "pending"}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list workers of %s: %w", clusterID, err)
	}
	groups := map[string]*types.NodeGroup{}
	for _, r := range result.Reservations {
		for _, inst := range r.Instances {
			group := "default"
			for _, tag := range inst.Tags {
				if aws.ToString(tag.Key) == "NodeGroup" && aws.ToString(tag.Value) != "" {
					group = aws.ToString(tag.Value)
				}
			}
			g := groups[group]
			if g == nil {
				g = &types.NodeGroup{Name: group, InstanceType: string(inst.InstanceType), Status: "ready", UpdatedAt: time.Now()}
				groups[group] = g
			}
			g.Replicas++
		}
	}
	out := make([]*types.NodeGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, g)
	}
	return out, nil
}

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
			node.PrivateDNSName = aws.ToString(instance.PrivateDnsName)
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
