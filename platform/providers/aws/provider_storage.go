package aws

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"adhar-io/adhar/platform/types"
)

// cleanupAllAdharVolumes deletes all EBS volumes created by Adhar platform
func (p *Provider) cleanupAllAdharVolumes(ctx context.Context) error {
	log.Printf("🔍 Finding and deleting all Adhar EBS volumes...")

	result, err := p.ec2Client.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("tag:Created-By"),
				Values: []string{"adhar-platform"},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to describe Adhar volumes: %w", err)
	}

	deletedCount := 0
	for _, volume := range result.Volumes {
		if volume.VolumeId != nil && volume.State == ec2types.VolumeStateAvailable {
			log.Printf("🗑️  Deleting volume %s", *volume.VolumeId)
			_, err = p.ec2Client.DeleteVolume(ctx, &ec2.DeleteVolumeInput{
				VolumeId: volume.VolumeId,
			})
			if err != nil {
				log.Printf("Warning: Failed to delete volume %s: %v", *volume.VolumeId, err)
			} else {
				deletedCount++
			}
		}
	}

	log.Printf("✓ Deleted %d volumes", deletedCount)
	return nil
}

// CreateStorage creates an EBS volume using AWS EC2
func (p *Provider) CreateStorage(ctx context.Context, spec *types.StorageSpec) (*types.Storage, error) {
	// Parse size (convert from Kubernetes format to GB)
	// This is a simplified parser - production would need robust size parsing
	sizeGB := int32(100) // Default 100GB
	if spec.Size != "" {
		// Simple parsing - in production, use proper size parsing library
		if spec.Size == "1Ti" {
			sizeGB = 1024
		} else if spec.Size == "500Gi" {
			sizeGB = 500
		}
		// Add more size parsing as needed
	}

	// Map storage type to AWS EBS volume type
	awsVolumeType := ec2types.VolumeTypeGp3
	switch spec.Type {
	case "gp2":
		awsVolumeType = ec2types.VolumeTypeGp2
	case "gp3":
		awsVolumeType = ec2types.VolumeTypeGp3
	case "io1":
		awsVolumeType = ec2types.VolumeTypeIo1
	case "io2":
		awsVolumeType = ec2types.VolumeTypeIo2
	case "sc1":
		awsVolumeType = ec2types.VolumeTypeSc1
	case "st1":
		awsVolumeType = ec2types.VolumeTypeSt1
	}

	// Get first availability zone for the region
	azResult, err := p.ec2Client.DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{})
	if err != nil {
		return nil, fmt.Errorf("failed to get availability zones: %w", err)
	}
	if len(azResult.AvailabilityZones) == 0 {
		return nil, fmt.Errorf("no availability zones found in region %s", p.config.Region)
	}
	availabilityZone := *azResult.AvailabilityZones[0].ZoneName

	// Prepare tags
	var tags []ec2types.Tag
	tags = append(tags, ec2types.Tag{
		Key:   aws.String("Created-By"),
		Value: aws.String("adhar-platform"),
	})

	if spec.Tags != nil {
		for key, value := range spec.Tags {
			tags = append(tags, ec2types.Tag{
				Key:   aws.String(key),
				Value: aws.String(value),
			})
		}
	}

	// Create EBS volume
	createVolumeInput := &ec2.CreateVolumeInput{
		AvailabilityZone: aws.String(availabilityZone),
		Size:             aws.Int32(sizeGB),
		VolumeType:       awsVolumeType,
		TagSpecifications: []ec2types.TagSpecification{
			{
				ResourceType: ec2types.ResourceTypeVolume,
				Tags:         tags,
			},
		},
	}

	result, err := p.ec2Client.CreateVolume(ctx, createVolumeInput)
	if err != nil {
		return nil, fmt.Errorf("failed to create EBS volume: %w", err)
	}

	volumeID := *result.VolumeId

	// Wait for volume to be available
	waiter := ec2.NewVolumeAvailableWaiter(p.ec2Client)
	err = waiter.Wait(ctx, &ec2.DescribeVolumesInput{
		VolumeIds: []string{volumeID},
	}, 5*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("failed waiting for volume to be available: %w", err)
	}

	return &types.Storage{
		ID:     volumeID,
		Type:   spec.Type,
		Size:   spec.Size,
		Status: "available",
		Tags:   spec.Tags,
	}, nil
}

// DeleteStorage deletes an EBS volume
func (p *Provider) DeleteStorage(ctx context.Context, storageID string) error {
	_, err := p.ec2Client.DeleteVolume(ctx, &ec2.DeleteVolumeInput{
		VolumeId: aws.String(storageID),
	})
	if err != nil {
		return fmt.Errorf("failed to delete EBS volume: %w", err)
	}

	return nil
}

// GetStorage retrieves EBS volume information
func (p *Provider) GetStorage(ctx context.Context, storageID string) (*types.Storage, error) {
	result, err := p.ec2Client.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{
		VolumeIds: []string{storageID},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe EBS volume: %w", err)
	}

	if len(result.Volumes) == 0 {
		return nil, fmt.Errorf("EBS volume %s not found", storageID)
	}

	volume := result.Volumes[0]

	// Extract tags
	tags := make(map[string]string)
	for _, tag := range volume.Tags {
		if tag.Key != nil && tag.Value != nil {
			tags[*tag.Key] = *tag.Value
		}
	}

	// Convert size back to Kubernetes format
	sizeStr := fmt.Sprintf("%dGi", *volume.Size)

	return &types.Storage{
		ID:     *volume.VolumeId,
		Type:   string(volume.VolumeType),
		Size:   sizeStr,
		Status: string(volume.State),
		Tags:   tags,
	}, nil
}
