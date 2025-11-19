package odf

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// AWSManager handles AWS operations for ODF storage provisioning
type AWSManager struct {
	ec2Client *ec2.Client
	iamClient *iam.Client
	config    aws.Config
	region    string
}

// AWSConfig holds AWS-specific configuration
type AWSConfig struct {
	Region           string
	VolumeSize       int32  // Size in GB
	VolumeType       string // gp3, gp2, io1, etc.
	IOPS             int32  // For io1/io2/gp3 volumes
	Throughput       int32  // For gp3 volumes (MB/s)
	Encrypted        bool
	KMSKeyID         string
	AvailabilityZone string
	NodeStorageSize  string // Required storage per node (e.g., "512Gi")
}

// NodeStorageInfo contains storage information for a node
type NodeStorageInfo struct {
	NodeName             string
	InstanceID           string
	AvailabilityZone     string
	ExistingVolumes      []string
	RequiredStorage      int64 // In GB
	HasSufficientStorage bool
}

// RequiredAWSPermissions defines the AWS permissions needed for ODF setup
type RequiredAWSPermissions struct {
	EC2Permissions []string
	IAMPermissions []string
}

// NewAWSManager creates a new AWS manager for ODF operations
func NewAWSManager(ctx context.Context, awsConfig *AWSConfig) (*AWSManager, error) {
	// Load AWS configuration
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(awsConfig.Region),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS configuration: %w", err)
	}

	// Create AWS service clients
	ec2Client := ec2.NewFromConfig(cfg)
	iamClient := iam.NewFromConfig(cfg)

	// Detect region if not specified
	region := awsConfig.Region
	if region == "" {
		region = cfg.Region
		if region == "" {
			return nil, fmt.Errorf("AWS region not specified and could not be detected")
		}
	}

	return &AWSManager{
		ec2Client: ec2Client,
		iamClient: iamClient,
		config:    cfg,
		region:    region,
	}, nil
}

// CheckRequiredPermissions validates that all required AWS permissions are available
func (a *AWSManager) CheckRequiredPermissions(ctx context.Context) error {
	log.Println("🔍 Checking AWS permissions...")

	// Get current user identity
	identity, err := a.getCurrentUserIdentity(ctx)
	if err != nil {
		return &PermissionError{
			Type:    "authentication",
			Message: fmt.Sprintf("Failed to get AWS identity: %v", err),
			Policy:  a.generateRequiredPolicy(),
		}
	}

	log.Printf("📋 AWS Identity: %s", identity)

	// Check EC2 permissions
	if err := a.checkEC2Permissions(ctx); err != nil {
		return err
	}

	// Check IAM permissions (for policy generation)
	if err := a.checkIAMPermissions(ctx); err != nil {
		log.Printf("⚠️ Limited IAM permissions: %v", err)
		// IAM permissions are optional - we can still proceed
	}

	log.Println("✅ AWS permissions validated")
	return nil
}

// AnalyzeNodeStorage analyzes storage requirements for all worker nodes
func (a *AWSManager) AnalyzeNodeStorage(ctx context.Context, clientset kubernetes.Interface,
	requiredStoragePerNode int64) ([]NodeStorageInfo, error) {
	log.Println("🔍 Analyzing node storage requirements...")

	// Get worker nodes
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: "node-role.kubernetes.io/worker",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list worker nodes: %w", err)
	}

	nodeStorageInfo := make([]NodeStorageInfo, 0, len(nodes.Items))

	for _, node := range nodes.Items {
		info, err := a.analyzeNodeStorage(ctx, &node, requiredStoragePerNode)
		if err != nil {
			log.Printf("⚠️ Failed to analyze storage for node %s: %v", node.Name, err)
			continue
		}
		nodeStorageInfo = append(nodeStorageInfo, *info)
	}

	// Log summary
	insufficientCount := 0
	for _, info := range nodeStorageInfo {
		if !info.HasSufficientStorage {
			insufficientCount++
		}
	}

	log.Printf("📊 Storage Analysis: %d nodes analyzed, %d need additional storage",
		len(nodeStorageInfo), insufficientCount)

	return nodeStorageInfo, nil
}

// ProvisionStorageForNodes provisions additional EBS volumes for nodes that need them
func (a *AWSManager) ProvisionStorageForNodes(ctx context.Context, nodes []NodeStorageInfo,
	awsConfig *AWSConfig, dryRun bool) error {
	var nodesToProvision []NodeStorageInfo
	for _, node := range nodes {
		if !node.HasSufficientStorage {
			nodesToProvision = append(nodesToProvision, node)
		}
	}

	if len(nodesToProvision) == 0 {
		log.Println("✅ All nodes have sufficient storage")
		return nil
	}

	log.Printf("💾 Provisioning storage for %d nodes...", len(nodesToProvision))

	for _, node := range nodesToProvision {
		if err := a.provisionVolumeForNode(ctx, node, awsConfig, dryRun); err != nil {
			return fmt.Errorf("failed to provision storage for node %s: %w", node.NodeName, err)
		}
	}

	if !dryRun {
		log.Println("⏳ Waiting for volumes to be attached...")
		if err := a.waitForVolumesAttached(ctx, nodesToProvision); err != nil {
			return fmt.Errorf("failed waiting for volumes to attach: %w", err)
		}
	}

	log.Println("✅ Storage provisioning completed")
	return nil
}

// CleanupODFVolumes finds and removes EBS volumes created by this tool
func (a *AWSManager) CleanupODFVolumes(ctx context.Context) error {
	log.Println("🔍 Searching for ODF volumes to clean up...")

	// Find volumes created by this tool using tags
	describeInput := &ec2.DescribeVolumesInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("tag:kubernetes.io/created-by"),
				Values: []string{"sbd-operator-odf-setup"},
			},
			{
				Name:   aws.String("tag:medik8s.io/component"),
				Values: []string{"odf-storage"},
			},
		},
	}

	result, err := a.ec2Client.DescribeVolumes(ctx, describeInput)
	if err != nil {
		return fmt.Errorf("failed to describe volumes: %w", err)
	}

	if len(result.Volumes) == 0 {
		log.Println("✅ No ODF volumes found to clean up")
		return nil
	}

	log.Printf("🗑️ Found %d ODF volumes to clean up", len(result.Volumes))

	// Clean up each volume
	for _, volume := range result.Volumes {
		if err := a.cleanupVolume(ctx, volume); err != nil {
			log.Printf("⚠️ Failed to clean up volume %s: %v", *volume.VolumeId, err)
		}
	}

	log.Println("✅ ODF volume cleanup completed")
	return nil
}

// cleanupVolume detaches and deletes a single EBS volume
func (a *AWSManager) cleanupVolume(ctx context.Context, volume types.Volume) error {
	volumeID := *volume.VolumeId

	// Get volume name from tags for logging
	volumeName := volumeID
	for _, tag := range volume.Tags {
		if *tag.Key == "Name" {
			volumeName = *tag.Value
			break
		}
	}

	log.Printf("🗑️ Cleaning up volume: %s (%s)", volumeName, volumeID)

	// If volume is attached, detach it first
	if len(volume.Attachments) > 0 {
		for _, attachment := range volume.Attachments {
			if attachment.State == types.VolumeAttachmentStateAttached {
				log.Printf("📎 Detaching volume %s from instance %s", volumeID, *attachment.InstanceId)

				_, err := a.ec2Client.DetachVolume(ctx, &ec2.DetachVolumeInput{
					VolumeId:   aws.String(volumeID),
					InstanceId: attachment.InstanceId,
					Force:      aws.Bool(true), // Force detach for cleanup
				})
				if err != nil {
					return fmt.Errorf("failed to detach volume %s: %w", volumeID, err)
				}

				// Wait for volume to be detached
				if err := a.waitForVolumeDetached(ctx, volumeID); err != nil {
					return fmt.Errorf("failed waiting for volume %s to detach: %w", volumeID, err)
				}
			}
		}
	}

	// Delete the volume
	log.Printf("🗑️ Deleting volume: %s", volumeID)
	_, err := a.ec2Client.DeleteVolume(ctx, &ec2.DeleteVolumeInput{
		VolumeId: aws.String(volumeID),
	})
	if err != nil {
		return fmt.Errorf("failed to delete volume %s: %w", volumeID, err)
	}

	log.Printf("✅ Successfully deleted volume: %s", volumeName)
	return nil
}

// waitForVolumeDetached waits for a volume to be detached
func (a *AWSManager) waitForVolumeDetached(ctx context.Context, volumeID string) error {
	maxAttempts := 30
	waitTime := 10 * time.Second

	for i := 0; i < maxAttempts; i++ {
		result, err := a.ec2Client.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{
			VolumeIds: []string{volumeID},
		})
		if err != nil {
			return err
		}

		if len(result.Volumes) == 0 {
			return fmt.Errorf("volume %s not found", volumeID)
		}

		volume := result.Volumes[0]
		if len(volume.Attachments) == 0 || volume.Attachments[0].State == types.VolumeAttachmentStateDetached {
			log.Printf("✅ Volume %s is now detached", volumeID)
			return nil
		}

		log.Printf("⏳ Waiting for volume %s to detach... (attempt %d/%d)", volumeID, i+1, maxAttempts)
		time.Sleep(waitTime)
	}

	return fmt.Errorf("timeout waiting for volume %s to detach", volumeID)
}

// getCurrentUserIdentity gets the current AWS user/role identity
func (a *AWSManager) getCurrentUserIdentity(ctx context.Context) (string, error) {
	// Try to get current user info by attempting a simple IAM operation
	getUserInput := &iam.GetUserInput{}
	userOutput, err := a.iamClient.GetUser(ctx, getUserInput)
	if err == nil && userOutput.User != nil {
		return fmt.Sprintf("User: %s (ARN: %s)", *userOutput.User.UserName, *userOutput.User.Arn), nil
	}

	// If GetUser fails, try to list access keys (this works for roles too)
	listKeysInput := &iam.ListAccessKeysInput{}
	keysOutput, err := a.iamClient.ListAccessKeys(ctx, listKeysInput)
	if err == nil && len(keysOutput.AccessKeyMetadata) > 0 {
		return fmt.Sprintf("User: %s", *keysOutput.AccessKeyMetadata[0].UserName), nil
	}

	// Fallback: try a simple EC2 operation to validate credentials
	describeRegionsInput := &ec2.DescribeRegionsInput{}
	_, err = a.ec2Client.DescribeRegions(ctx, describeRegionsInput)
	if err != nil {
		return "", fmt.Errorf("AWS credentials validation failed: %w", err)
	}

	return "AWS credentials valid (unable to determine specific identity)", nil
}

// checkEC2Permissions checks required EC2 permissions
func (a *AWSManager) checkEC2Permissions(ctx context.Context) error {
	log.Println("🔍 Checking EC2 permissions...")

	// Test DescribeInstances permission
	_, err := a.ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		MaxResults: aws.Int32(5),
	})
	if err != nil {
		return &PermissionError{
			Type:    "ec2_describe",
			Message: fmt.Sprintf("Missing EC2 DescribeInstances permission: %v", err),
			Policy:  a.generateRequiredPolicy(),
		}
	}

	// Test DescribeVolumes permission
	_, err = a.ec2Client.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{
		MaxResults: aws.Int32(5),
	})
	if err != nil {
		return &PermissionError{
			Type:    "ec2_describe",
			Message: fmt.Sprintf("Missing EC2 DescribeVolumes permission: %v", err),
			Policy:  a.generateRequiredPolicy(),
		}
	}

	// Test CreateVolume permission with dry-run
	_, err = a.ec2Client.CreateVolume(ctx, &ec2.CreateVolumeInput{
		Size:       aws.Int32(1),
		VolumeType: types.VolumeTypeGp3,
		DryRun:     aws.Bool(true),
	})
	// For dry-run, we expect DryRunOperation error, not UnauthorizedOperation
	if err != nil && !strings.Contains(err.Error(), "DryRunOperation") {
		if strings.Contains(err.Error(), "UnauthorizedOperation") {
			return &PermissionError{
				Type:    "ec2_create",
				Message: fmt.Sprintf("Missing EC2 CreateVolume permission: %v", err),
				Policy:  a.generateRequiredPolicy(),
			}
		}
	}

	// Test AttachVolume permission with dry-run
	_, err = a.ec2Client.AttachVolume(ctx, &ec2.AttachVolumeInput{
		VolumeId:   aws.String("vol-12345678"), // Dummy volume ID
		InstanceId: aws.String("i-12345678"),   // Dummy instance ID
		Device:     aws.String("/dev/sdf"),
		DryRun:     aws.Bool(true),
	})
	// For dry-run, we expect DryRunOperation or InvalidVolume error, not UnauthorizedOperation
	if err != nil && !strings.Contains(err.Error(), "DryRunOperation") && !strings.Contains(err.Error(), "InvalidVolume") {
		if strings.Contains(err.Error(), "UnauthorizedOperation") {
			return &PermissionError{
				Type:    "ec2_attach",
				Message: fmt.Sprintf("Missing EC2 AttachVolume permission: %v", err),
				Policy:  a.generateRequiredPolicy(),
			}
		}
	}

	log.Println("✅ EC2 permissions validated")
	return nil
}

// checkIAMPermissions checks optional IAM permissions
func (a *AWSManager) checkIAMPermissions(ctx context.Context) error {
	// Try to get current user - this is optional
	_, err := a.iamClient.GetUser(ctx, &iam.GetUserInput{})
	return err // We don't fail on this, just return the error for logging
}

// analyzeNodeStorage analyzes storage for a specific node
func (a *AWSManager) analyzeNodeStorage(ctx context.Context, node *corev1.Node,
	requiredStorageGB int64) (*NodeStorageInfo, error) {
	// Get instance ID from node
	instanceID := getInstanceIDFromNode(node)
	if instanceID == "" {
		return nil, fmt.Errorf("could not determine instance ID for node %s", node.Name)
	}

	// Get instance details
	instanceInput := &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	}
	instanceOutput, err := a.ec2Client.DescribeInstances(ctx, instanceInput)
	if err != nil {
		return nil, fmt.Errorf("failed to describe instance %s: %w", instanceID, err)
	}

	if len(instanceOutput.Reservations) == 0 || len(instanceOutput.Reservations[0].Instances) == 0 {
		return nil, fmt.Errorf("instance %s not found", instanceID)
	}

	instance := instanceOutput.Reservations[0].Instances[0]

	// Get attached volumes
	volumeInput := &ec2.DescribeVolumesInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("attachment.instance-id"),
				Values: []string{instanceID},
			},
		},
	}
	volumeOutput, err := a.ec2Client.DescribeVolumes(ctx, volumeInput)
	if err != nil {
		return nil, fmt.Errorf("failed to describe volumes for instance %s: %w", instanceID, err)
	}

	// Calculate total storage and identify existing volumes
	var totalStorageGB int64
	existingVolumes := make([]string, 0, len(volumeOutput.Volumes))
	var availableStorageGB int64

	for _, volume := range volumeOutput.Volumes {
		existingVolumes = append(existingVolumes, *volume.VolumeId)
		volumeSizeGB := int64(*volume.Size)
		totalStorageGB += volumeSizeGB

		// Only count non-root volumes as available for ODF
		if !isRootVolume(volume, instance) {
			availableStorageGB += volumeSizeGB
		}
	}

	return &NodeStorageInfo{
		NodeName:             node.Name,
		InstanceID:           instanceID,
		AvailabilityZone:     *instance.Placement.AvailabilityZone,
		ExistingVolumes:      existingVolumes,
		RequiredStorage:      requiredStorageGB,
		HasSufficientStorage: availableStorageGB >= requiredStorageGB,
	}, nil
}

// provisionVolumeForNode provisions an EBS volume for a specific node
func (a *AWSManager) provisionVolumeForNode(ctx context.Context, nodeInfo NodeStorageInfo,
	awsConfig *AWSConfig, dryRun bool) error {
	volumeSizeGB := nodeInfo.RequiredStorage

	if dryRun {
		log.Printf("[DRY-RUN] Would provision %dGB %s volume for node %s (instance %s) in AZ %s",
			volumeSizeGB, awsConfig.VolumeType, nodeInfo.NodeName, nodeInfo.InstanceID, nodeInfo.AvailabilityZone)
		return nil
	}

	log.Printf("💾 Provisioning %dGB %s volume for node %s...",
		volumeSizeGB, awsConfig.VolumeType, nodeInfo.NodeName)

	// Create volume
	createVolumeInput := &ec2.CreateVolumeInput{
		Size:             aws.Int32(int32(volumeSizeGB)),
		VolumeType:       types.VolumeType(awsConfig.VolumeType),
		AvailabilityZone: aws.String(nodeInfo.AvailabilityZone),
		Encrypted:        aws.Bool(awsConfig.Encrypted),
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeVolume,
				Tags: []types.Tag{
					{
						Key:   aws.String("Name"),
						Value: aws.String(fmt.Sprintf("sbd-odf-storage-%s", nodeInfo.NodeName)),
					},
					{
						Key:   aws.String("kubernetes.io/created-for/node"),
						Value: aws.String(nodeInfo.NodeName),
					},
					{
						Key:   aws.String("kubernetes.io/created-by"),
						Value: aws.String("sbd-operator-odf-setup"),
					},
					{
						Key:   aws.String("medik8s.io/component"),
						Value: aws.String("odf-storage"),
					},
				},
			},
		},
	}

	// Add KMS key if specified
	if awsConfig.KMSKeyID != "" {
		createVolumeInput.KmsKeyId = aws.String(awsConfig.KMSKeyID)
	}

	// Add IOPS and throughput for appropriate volume types
	switch awsConfig.VolumeType {
	case "gp3":
		if awsConfig.IOPS > 0 {
			createVolumeInput.Iops = aws.Int32(awsConfig.IOPS)
		}
		if awsConfig.Throughput > 0 {
			createVolumeInput.Throughput = aws.Int32(awsConfig.Throughput)
		}
	case "io1", "io2":
		if awsConfig.IOPS > 0 {
			createVolumeInput.Iops = aws.Int32(awsConfig.IOPS)
		}
	}

	volumeOutput, err := a.ec2Client.CreateVolume(ctx, createVolumeInput)
	if err != nil {
		return fmt.Errorf("failed to create volume: %w", err)
	}

	volumeID := *volumeOutput.VolumeId
	log.Printf("✅ Created volume %s", volumeID)

	// Wait for volume to be available
	log.Printf("⏳ Waiting for volume %s to be available...", volumeID)
	waiter := ec2.NewVolumeAvailableWaiter(a.ec2Client)
	err = waiter.Wait(ctx, &ec2.DescribeVolumesInput{
		VolumeIds: []string{volumeID},
	}, 5*time.Minute)
	if err != nil {
		return fmt.Errorf("volume %s did not become available: %w", volumeID, err)
	}

	// Find next available device name
	deviceName, err := a.getNextAvailableDevice(ctx, nodeInfo.InstanceID)
	if err != nil {
		return fmt.Errorf("failed to find available device name: %w", err)
	}

	// Attach volume to instance
	log.Printf("🔗 Attaching volume %s to instance %s as %s...", volumeID, nodeInfo.InstanceID, deviceName)
	_, err = a.ec2Client.AttachVolume(ctx, &ec2.AttachVolumeInput{
		VolumeId:   aws.String(volumeID),
		InstanceId: aws.String(nodeInfo.InstanceID),
		Device:     aws.String(deviceName),
	})
	if err != nil {
		return fmt.Errorf("failed to attach volume %s to instance %s: %w", volumeID, nodeInfo.InstanceID, err)
	}

	log.Printf("✅ Attached volume %s to node %s", volumeID, nodeInfo.NodeName)
	return nil
}

// waitForVolumesAttached waits for all volumes to be properly attached
func (a *AWSManager) waitForVolumesAttached(ctx context.Context, nodes []NodeStorageInfo) error {
	// This is a simplified implementation - in a real scenario, we'd track the specific volume IDs
	// and wait for their attachment status
	log.Printf("⏳ Waiting 60 seconds for volume attachments to complete on %d nodes...", len(nodes))

	select {
	case <-time.After(60 * time.Second):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// getNextAvailableDevice finds the next available device name for the instance
func (a *AWSManager) getNextAvailableDevice(ctx context.Context, instanceID string) (string, error) {
	// Get current volumes to see which device names are used
	volumeInput := &ec2.DescribeVolumesInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("attachment.instance-id"),
				Values: []string{instanceID},
			},
		},
	}
	volumeOutput, err := a.ec2Client.DescribeVolumes(ctx, volumeInput)
	if err != nil {
		return "", fmt.Errorf("failed to describe volumes for instance %s: %w", instanceID, err)
	}

	// Collect used device names
	usedDevices := make(map[string]bool)
	for _, volume := range volumeOutput.Volumes {
		for _, attachment := range volume.Attachments {
			if attachment.Device != nil {
				usedDevices[*attachment.Device] = true
			}
		}
	}

	// Try device names from /dev/sdf to /dev/sdz
	for i := 'f'; i <= 'z'; i++ {
		deviceName := fmt.Sprintf("/dev/sd%c", i)
		if !usedDevices[deviceName] {
			return deviceName, nil
		}
	}

	return "", fmt.Errorf("no available device names found for instance %s", instanceID)
}

// generateRequiredPolicy generates the complete IAM policy required for ODF setup
func (a *AWSManager) generateRequiredPolicy() string {
	policy := map[string]interface{}{
		"Version": "2012-10-17",
		"Statement": []map[string]interface{}{
			{
				"Sid":    "ODFStorageManagement",
				"Effect": "Allow",
				"Action": []string{
					// EC2 Volume Management
					"ec2:CreateVolume",
					"ec2:DeleteVolume",
					"ec2:AttachVolume",
					"ec2:DetachVolume",
					"ec2:ModifyVolume",
					"ec2:DescribeVolumes",
					"ec2:DescribeVolumeStatus",
					"ec2:DescribeVolumeAttribute",

					// EC2 Instance Management
					"ec2:DescribeInstances",
					"ec2:DescribeInstanceStatus",
					"ec2:DescribeInstanceAttribute",

					// EC2 Regions and Zones
					"ec2:DescribeRegions",
					"ec2:DescribeAvailabilityZones",

					// EC2 Tags
					"ec2:CreateTags",
					"ec2:DescribeTags",

					// EC2 Snapshots (for backup/restore)
					"ec2:CreateSnapshot",
					"ec2:DeleteSnapshot",
					"ec2:DescribeSnapshots",
				},
				"Resource": "*",
			},
			{
				"Sid":    "KMSForEncryption",
				"Effect": "Allow",
				"Action": []string{
					"kms:CreateGrant",
					"kms:Decrypt",
					"kms:DescribeKey",
					"kms:Encrypt",
					"kms:GenerateDataKey",
					"kms:ReEncrypt*",
				},
				"Resource": "*",
				"Condition": map[string]interface{}{
					"StringEquals": map[string]string{
						"kms:ViaService": fmt.Sprintf("ec2.%s.amazonaws.com", a.region),
					},
				},
			},
			{
				"Sid":    "IAMReadOnly",
				"Effect": "Allow",
				"Action": []string{
					"iam:GetUser",
					"iam:ListAccessKeys",
				},
				"Resource": "*",
			},
		},
	}

	policyJSON, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return fmt.Sprintf("Error generating policy: %v", err)
	}

	return string(policyJSON)
}

// Helper functions

// getInstanceIDFromNode extracts the AWS instance ID from a Kubernetes node
func getInstanceIDFromNode(node *corev1.Node) string {
	// Try spec.providerID first (format: aws:///zone/instance-id)
	if node.Spec.ProviderID != "" {
		parts := strings.Split(node.Spec.ProviderID, "/")
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}

	// Try node annotations
	if instanceID, exists := node.Annotations["csi.volume.kubernetes.io/nodeid"]; exists {
		return instanceID
	}

	// Try node labels
	if instanceID, exists := node.Labels["node.kubernetes.io/instance-id"]; exists {
		return instanceID
	}

	return ""
}

// isRootVolume determines if a volume is the root volume for an instance
func isRootVolume(volume types.Volume, instance types.Instance) bool {
	for _, attachment := range volume.Attachments {
		if attachment.Device != nil && (*attachment.Device == "/dev/sda1" || *attachment.Device == "/dev/xvda") {
			return true
		}
	}

	// Check if it's the boot volume
	for _, bdm := range instance.BlockDeviceMappings {
		if bdm.Ebs != nil && bdm.Ebs.VolumeId != nil && *bdm.Ebs.VolumeId == *volume.VolumeId {
			return bdm.DeviceName != nil && (*bdm.DeviceName == "/dev/sda1" || *bdm.DeviceName == "/dev/xvda")
		}
	}

	return false
}

// PermissionError represents an AWS permission error with policy information
type PermissionError struct {
	Type    string // Type of permission error
	Message string // Human-readable error message
	Policy  string // Required IAM policy JSON
}

func (e *PermissionError) Error() string {
	return e.Message
}

// IsPermissionError checks if an error is a permission error
func IsPermissionError(err error) (*PermissionError, bool) {
	if permErr, ok := err.(*PermissionError); ok {
		return permErr, true
	}
	return nil, false
}
