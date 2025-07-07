package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/medik8s/sbd-operator/pkg/storage"
)

// Configuration holds all the configuration for the storage setup
type Config struct {
	// AWS Configuration
	AWSRegion        string
	ClusterName      string
	EFSName          string
	EFSFilesystemID  string
	StorageClassName string

	// Behavior flags
	CreateEFS         bool
	DryRun            bool
	Cleanup           bool
	UpdateMode        bool
	Verbose           bool
	GenerateIAMPolicy bool

	// EFS Configuration
	PerformanceMode       string
	ThroughputMode        string
	ProvisionedThroughput int64

	// IAM Configuration
	EFSCSIRoleName string
}

func main() {
	// Parse command line arguments
	config := parseFlags()

	// Setup logging
	if config.Verbose {
		log.SetFlags(log.LstdFlags | log.Lshortfile)
	}

	// Handle IAM policy generation
	if config.GenerateIAMPolicy {
		printIAMPolicy()
		return
	}

	// Create storage manager
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	storageManager, err := storage.NewManager(ctx, config.toStorageConfig())
	if err != nil {
		log.Fatalf("Failed to create storage manager: %v", err)
	}

	// Execute the requested operation
	if config.Cleanup {
		if err := storageManager.Cleanup(ctx); err != nil {
			log.Fatalf("Cleanup failed: %v", err)
		}
		log.Println("✅ Cleanup completed successfully")
		return
	}

	// Setup shared storage
	result, err := storageManager.SetupSharedStorage(ctx)
	if err != nil {
		log.Fatalf("Failed to setup shared storage: %v", err)
	}

	// Print results
	printResults(result)
}

func parseFlags() *Config {
	config := &Config{}

	// AWS Configuration
	flag.StringVar(&config.AWSRegion, "aws-region", "", "AWS region (auto-detected if not specified)")
	flag.StringVar(&config.ClusterName, "cluster-name", "", "Cluster name (auto-detected if not specified)")
	flag.StringVar(&config.EFSName, "efs-name", "", "EFS filesystem name (default: sbd-efs-CLUSTER_NAME)")
	flag.StringVar(&config.EFSFilesystemID, "filesystem-id", "", "Use existing EFS filesystem ID")
	flag.StringVar(&config.StorageClassName, "storage-class-name", "", "StorageClass name (default: sbd-efs-sc)")

	// Behavior flags
	flag.BoolVar(&config.CreateEFS, "create-efs", true, "Create new EFS filesystem")
	flag.BoolVar(&config.DryRun, "dry-run", false, "Show what would be done without executing")
	flag.BoolVar(&config.Cleanup, "cleanup", false, "Clean up all created resources")
	flag.BoolVar(&config.UpdateMode, "update-mode", false, "Force update/recreation of StorageClass")
	flag.BoolVar(&config.Verbose, "verbose", false, "Enable verbose logging")
	flag.BoolVar(&config.GenerateIAMPolicy, "generate-iam-policy", false, "Generate and print the required IAM policy for the EFS CSI driver")

	// EFS Configuration
	flag.StringVar(&config.PerformanceMode, "performance-mode", "generalPurpose", "EFS performance mode (generalPurpose|maxIO)")
	flag.StringVar(&config.ThroughputMode, "throughput-mode", "provisioned", "EFS throughput mode (provisioned|burstingThroughput)")
	flag.Int64Var(&config.ProvisionedThroughput, "provisioned-throughput", 10, "Provisioned throughput in MiB/s")

	// IAM Configuration
	flag.StringVar(&config.EFSCSIRoleName, "efs-csi-role-name", "", "EFS CSI IAM role name (auto-generated if not specified)")

	// Show help
	help := flag.Bool("help", false, "Show help message")

	flag.Parse()

	if *help {
		showUsage()
		os.Exit(0)
	}

	// Validate configuration
	if config.EFSFilesystemID != "" {
		config.CreateEFS = false
	}

	return config
}

func (c *Config) toStorageConfig() *storage.Config {
	return &storage.Config{
		AWSRegion:             c.AWSRegion,
		ClusterName:           c.ClusterName,
		EFSName:               c.EFSName,
		EFSFilesystemID:       c.EFSFilesystemID,
		StorageClassName:      c.StorageClassName,
		CreateEFS:             c.CreateEFS,
		DryRun:                c.DryRun,
		UpdateMode:            c.UpdateMode,
		PerformanceMode:       c.PerformanceMode,
		ThroughputMode:        c.ThroughputMode,
		ProvisionedThroughput: c.ProvisionedThroughput,
		EFSCSIRoleName:        c.EFSCSIRoleName,
	}
}

func showUsage() {
	fmt.Printf(`
Usage: %s [OPTIONS]

This tool sets up EFS-based shared storage for OpenShift/Kubernetes clusters.
It creates an EFS filesystem, configures networking, installs the EFS CSI driver,
and creates a StorageClass with ReadWriteMany (RWX) access mode.

For OpenShift on AWS, this tool also configures the proper IAM roles and 
service account annotations required for the EFS CSI driver.

EXAMPLES:
    # Create new EFS with auto-detection (recommended)
    %s

    # Override auto-detected values
    %s --cluster-name my-cluster --aws-region us-east-1

    # Use existing EFS filesystem
    %s --filesystem-id fs-1234567890abcdef0

    # Clean up everything
    %s --cleanup --efs-name sbd-efs-mycluster

    # Preview changes without executing
    %s --dry-run

REQUIREMENTS:
    • OpenShift/Kubernetes cluster with AWS provider
    • AWS credentials configured (via environment, profile, or IAM role)
    • Cluster admin permissions
    • IAM permissions for resource creation

OPTIONS:
`, os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0])

	flag.PrintDefaults()
}

func printResults(result *storage.SetupResult) {
	fmt.Println("\n🎉 Shared Storage Setup Completed Successfully!")
	fmt.Println("==========================================")

	if result.EFSFilesystemID != "" {
		fmt.Printf("📁 EFS Filesystem: %s\n", result.EFSFilesystemID)
	}

	if result.StorageClassName != "" {
		fmt.Printf("💾 StorageClass: %s\n", result.StorageClassName)
	}

	if result.IAMRoleARN != "" {
		fmt.Printf("🔐 IAM Role: %s\n", result.IAMRoleARN)
	}

	if len(result.MountTargets) > 0 {
		fmt.Printf("🔗 Mount Targets: %d created\n", len(result.MountTargets))
	}

	if result.SecurityGroupID != "" {
		fmt.Printf("🛡️  Security Group: %s\n", result.SecurityGroupID)
	}

	fmt.Println("\n✅ Your cluster now has ReadWriteMany (RWX) storage capability!")
	fmt.Printf("   Use StorageClass '%s' in your PVCs for shared storage.\n", result.StorageClassName)
}

// printIAMPolicy generates and prints the required IAM policy for the EFS CSI driver
func printIAMPolicy() {
	policy := `{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "ec2:DescribeVpcs",
        "ec2:DescribeSubnets",
        "ec2:DescribeSecurityGroups",
        "ec2:CreateSecurityGroup",
        "ec2:AuthorizeSecurityGroupIngress",
        "ec2:CreateTags"
      ],
      "Resource": "*"
    },
    {
      "Effect": "Allow",
      "Action": [
        "efs:CreateFileSystem",
        "efs:DescribeFileSystems",
        "efs:CreateMountTarget",
        "efs:DescribeMountTargets",
        "efs:CreateTags",
        "efs:DescribeTags"
      ],
      "Resource": "*"
    }
  ]
}`

	fmt.Println("📋 Required IAM Policy for OpenShift EFS CSI Driver Setup")
	fmt.Println("========================================================")
	fmt.Println()
	fmt.Println("This policy grants the minimum required permissions for the")
	fmt.Println("setup-shared-storage tool to create and configure EFS resources")
	fmt.Println("for OpenShift clusters.")
	fmt.Println()
	fmt.Println("USAGE:")
	fmt.Println("1. Save this policy as 'efs-setup-policy.json'")
	fmt.Println("2. Create IAM policy: aws iam create-policy --policy-name EFS-Setup-Policy --policy-document file://efs-setup-policy.json")
	fmt.Println("3. Attach to user/role: aws iam attach-user-policy --user-name YOUR_USER --policy-arn arn:aws:iam::ACCOUNT:policy/EFS-Setup-Policy")
	fmt.Println()
	fmt.Println("POLICY JSON:")
	fmt.Println(policy)
	fmt.Println()
	fmt.Println("NOTE: This policy is for the setup tool only. The EFS CSI driver itself")
	fmt.Println("uses AWS credentials from the 'aws-creds' secret in OpenShift clusters.")
}
