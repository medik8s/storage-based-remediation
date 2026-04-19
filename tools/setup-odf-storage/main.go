package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/medik8s/storage-based-remediation/pkg/storage/odf"
)

// Configuration holds all the configuration for the ODF storage setup
type Config struct {
	// ODF Configuration
	StorageClassName       string
	ClusterName            string
	Namespace              string
	ODFSubscriptionChannel string

	// Storage Configuration
	StorageSize            string
	ReplicaCount           int
	EnableEncryption       bool
	EnableStorageDeviceSet bool

	// AWS Integration
	EnableAWSIntegration bool
	AWSRegion            string
	AWSVolumeType        string
	AWSIOPS              int
	AWSThroughput        int
	AWSKMSKeyID          string

	// Cache Coherency Configuration
	AggressiveCoherency bool

	// Behavior flags
	DryRun     bool
	Cleanup    bool
	UpdateMode bool
	Verbose    bool
}

func main() {
	// Parse command line arguments
	config := parseFlags()

	// Setup logging
	if config.Verbose {
		log.SetFlags(log.LstdFlags | log.Lshortfile)
	}

	// Create ODF storage manager
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	odfManager, err := odf.NewManager(ctx, config.toODFConfig())
	if err != nil {
		log.Fatalf("Failed to create ODF storage manager: %v", err)
	}

	// Execute the requested operation
	if config.Cleanup {
		if err := odfManager.Cleanup(ctx); err != nil {
			log.Fatalf("Cleanup failed: %v", err)
		}
		log.Println("✅ Cleanup completed successfully")
		return
	}

	// Setup ODF storage
	result, err := odfManager.SetupODFStorage(ctx)
	if err != nil {
		log.Fatalf("Failed to setup ODF storage: %v", err)
	}

	// Print results
	printResults(result)
}

func parseFlags() *Config {
	config := &Config{}

	// ODF Configuration
	flag.StringVar(&config.StorageClassName, "storage-class-name", "sbr-cephfs", "CephFS StorageClass name for SBR")
	flag.StringVar(&config.ClusterName, "cluster-name", "ocs-storagecluster", "ODF StorageCluster name")
	flag.StringVar(&config.Namespace, "namespace", "openshift-storage", "Namespace for ODF installation")
	flag.StringVar(&config.ODFSubscriptionChannel, "odf-operator-channel", odf.DefaultODFSubscriptionChannel,
		"OLM Subscription channel for the ODF operator (e.g. stable-4.19)")

	// Storage Configuration
	flag.StringVar(&config.StorageSize, "storage-size", "2Ti", "Total storage size for the cluster")
	flag.IntVar(&config.ReplicaCount, "replica-count", 3, "Number of storage replicas")
	flag.BoolVar(&config.EnableEncryption, "enable-encryption", false, "Enable storage encryption")
	flag.BoolVar(&config.EnableStorageDeviceSet, "enable-device-set", true, "Enable automatic storage device set creation")

	// AWS Integration
	flag.BoolVar(&config.EnableAWSIntegration, "enable-aws-integration", true, "Enable automatic AWS EBS volume provisioning")
	flag.StringVar(&config.AWSRegion, "aws-region", "", "AWS region (auto-detected if not specified)")
	flag.StringVar(&config.AWSVolumeType, "aws-volume-type", "gp3", "AWS EBS volume type (gp3, gp2, io1, io2)")
	flag.IntVar(&config.AWSIOPS, "aws-iops", 3000, "AWS EBS volume IOPS (for gp3, io1, io2)")
	flag.IntVar(&config.AWSThroughput, "aws-throughput", 125, "AWS EBS volume throughput in MB/s (for gp3)")
	flag.StringVar(&config.AWSKMSKeyID, "aws-kms-key", "", "AWS KMS key ID for encryption")

	// Cache Coherency Configuration
	flag.BoolVar(&config.AggressiveCoherency, "aggressive-coherency", false, "Enable aggressive cache coherency for strict SBR coordination")

	// Behavior flags
	flag.BoolVar(&config.DryRun, "dry-run", false, "Show what would be done without executing")
	flag.BoolVar(&config.Cleanup, "cleanup", false, "Clean up created ODF resources")
	flag.BoolVar(&config.UpdateMode, "update-mode", false, "Force update/recreation of resources")
	flag.BoolVar(&config.Verbose, "verbose", false, "Enable verbose logging")

	// Show help
	help := flag.Bool("help", false, "Show help message")

	flag.Parse()

	if *help {
		showUsage()
		os.Exit(0)
	}

	return config
}

func (c *Config) toODFConfig() *odf.Config {
	return &odf.Config{
		StorageClassName:       c.StorageClassName,
		ClusterName:            c.ClusterName,
		Namespace:              c.Namespace,
		ODFSubscriptionChannel: c.ODFSubscriptionChannel,
		StorageSize:            c.StorageSize,
		ReplicaCount:           c.ReplicaCount,
		EnableEncryption:       c.EnableEncryption,
		EnableStorageDeviceSet: c.EnableStorageDeviceSet,
		AggressiveCoherency:    c.AggressiveCoherency,
		DryRun:                 c.DryRun,
		UpdateMode:             c.UpdateMode,
		// AWS Integration fields
		EnableAWSIntegration: c.EnableAWSIntegration,
		AWSRegion:            c.AWSRegion,
		AWSVolumeType:        c.AWSVolumeType,
		AWSIOPS:              c.AWSIOPS,
		AWSThroughput:        c.AWSThroughput,
		AWSKMSKeyID:          c.AWSKMSKeyID,
	}
}

func showUsage() {
	fmt.Printf(`
Usage: %s [OPTIONS]

This tool sets up OpenShift Data Foundation (ODF) with CephFS storage optimized for SBR.
It provides ReadWriteMany (RWX) storage with POSIX file locking required for reliable 
SBR cluster coordination and automatic node remediation.

DESCRIPTION:
This tool deploys OpenShift Data Foundation and creates a CephFS StorageClass with 
SBR-optimized mount options. CephFS provides distributed file storage with full POSIX 
locking support, enabling proper inter-node heartbeat coordination and preventing 
split-brain scenarios in SBR clusters.

AWS INTEGRATION:
For AWS clusters, the tool automatically:
• Checks required AWS IAM permissions upfront
• Analyzes existing node storage capacity
• Provisions additional EBS volumes if needed
• Attaches volumes to worker nodes automatically
• Provides complete IAM policy if permissions are missing

OPENSHIFT DATA FOUNDATION COMPONENTS:
• Ceph Storage Cluster: Provides distributed storage backend
• CephFS: Distributed file system with ReadWriteMany support
• CSI Driver: Kubernetes CSI integration for dynamic provisioning
• Storage Classes: Pre-configured classes for different storage types

CACHE COHERENCY FOR SBR:
• CephFS provides native cache coherency across all clients
• POSIX file locking: Full distributed locking support for SBR coordination
• Real-time consistency: Changes are immediately visible across all nodes
• No NFS cache issues: Direct file system semantics

AGGRESSIVE CACHE COHERENCY:
For strict SBR coordination use --aggressive-coherency flag which configures:
• cache=strict: Disable client-side caching for real-time updates
• recover_session=clean: Clean session recovery for reliability
• sync: Force synchronous operations
• _netdev: Ensure network availability before mounting

Use this mode when SBR requires the strictest cache coherency guarantees.

EXAMPLES:

    # Standard ODF setup with AWS integration
    %s

    # Custom cluster with specific AWS volume configuration
    %s --storage-size=4Ti --aws-volume-type=io1 --aws-iops=5000

    # Disable AWS integration (manual storage setup)
    %s --enable-aws-integration=false

    # Aggressive coherency with encrypted storage
    %s --aggressive-coherency --enable-encryption --aws-kms-key=alias/my-key

    # Clean up all ODF resources
    %s --cleanup

    # Preview changes without executing
    %s --dry-run --verbose

    # Non-default ODF OLM subscription channel
    %s --odf-operator-channel=stable-4.17

REQUIREMENTS:
    • OpenShift cluster (4.8+) or Kubernetes (1.21+) with OLM
    • At least 3 worker nodes for storage replication
    • Cluster admin permissions
    • For AWS clusters: Required AWS IAM permissions (checked automatically)

AWS PERMISSIONS:
The tool requires the following AWS permissions:
• EC2: CreateVolume, AttachVolume, DescribeInstances, DescribeVolumes
• EC2: CreateTags, DescribeTags (for resource tagging)
• KMS: CreateGrant, Encrypt, Decrypt (if encryption enabled)
• IAM: GetUser, ListAccessKeys (for identity verification)

If permissions are missing, the tool will display the complete required IAM policy.

STORAGE CLASSES CREATED:
The tool creates optimized StorageClasses:
• %s: CephFS with SBR cache coherency settings
• Auto-configured mount options for reliable SBR operation
• ReadWriteMany (RWX) access mode support
• POSIX file locking enabled

This ensures optimal SBR operation with proper inter-node coordination.

OPTIONS:
`, os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0], "sbr-cephfs")

	flag.PrintDefaults()
}

func printResults(result *odf.SetupResult) {
	fmt.Println("\n🎉 OpenShift Data Foundation Setup Completed Successfully!")
	fmt.Println("=========================================================")

	if result.StorageClassName != "" {
		fmt.Printf("💾 CephFS StorageClass: %s\n", result.StorageClassName)
	}

	if result.ClusterName != "" {
		fmt.Printf("🏗️  ODF Storage Cluster: %s\n", result.ClusterName)
	}

	if result.Namespace != "" {
		fmt.Printf("📦 Installed in Namespace: %s\n", result.Namespace)
	}

	fmt.Printf("🧪 Storage Test: ")
	if result.TestPassed {
		fmt.Println("✅ PASSED")
	} else {
		fmt.Println("⚠️  FAILED")
	}

	fmt.Println("\n✅ Your cluster now has CephFS ReadWriteMany (RWX) storage!")
	fmt.Printf("   Use StorageClass '%s' in your StorageBasedRemediationConfig for shared storage.\n", result.StorageClassName)
	fmt.Println("\n📖 Example StorageBasedRemediationConfig configuration:")
	fmt.Printf(`
apiVersion: storage-based-remediation.medik8s.io/v1alpha1
kind: StorageBasedRemediationConfig
metadata:
  name: sbr-with-odf
spec:
  sharedStorageClass: "%s"
  watchdogPath: "/dev/watchdog"
  watchdogTimeout: "60s"
`, result.StorageClassName)
}
