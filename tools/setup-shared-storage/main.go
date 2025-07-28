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
	// NFS Configuration
	NFSServer        string
	NFSShare         string
	StorageClassName string

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

	// Create storage manager
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
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

	// NFS Configuration
	flag.StringVar(&config.NFSServer, "nfs-server", "", "NFS server address (required)")
	flag.StringVar(&config.NFSShare, "nfs-share", "", "NFS share path (required)")
	flag.StringVar(&config.StorageClassName, "storage-class-name", "sbd-nfs-coherent", "StorageClass name")

	// Cache Coherency Configuration
	flag.BoolVar(&config.AggressiveCoherency, "aggressive-coherency", false, "Enable aggressive cache coherency (cache=none, sync, local_lock=none)")

	// Behavior flags
	flag.BoolVar(&config.DryRun, "dry-run", false, "Show what would be done without executing")
	flag.BoolVar(&config.Cleanup, "cleanup", false, "Clean up created StorageClass")
	flag.BoolVar(&config.UpdateMode, "update-mode", false, "Force update/recreation of StorageClass")
	flag.BoolVar(&config.Verbose, "verbose", false, "Enable verbose logging")

	// Show help
	help := flag.Bool("help", false, "Show help message")

	flag.Parse()

	if *help {
		showUsage()
		os.Exit(0)
	}

	// Validate required configuration
	if config.NFSServer == "" || config.NFSShare == "" {
		if !config.Cleanup {
			log.Fatalf("Both --nfs-server and --nfs-share are required")
		}
	}

	return config
}

func (c *Config) toStorageConfig() *storage.Config {
	return &storage.Config{
		NFSServer:           c.NFSServer,
		NFSShare:            c.NFSShare,
		StorageClassName:    c.StorageClassName,
		DryRun:              c.DryRun,
		UpdateMode:          c.UpdateMode,
		AggressiveCoherency: c.AggressiveCoherency,
	}
}

func showUsage() {
	fmt.Printf(`
Usage: %s [OPTIONS]

This tool sets up Standard NFS CSI shared storage for Kubernetes clusters optimized for SBD.
It provides explicit cache coherency controls required for reliable SBD cluster coordination.

DESCRIPTION:
This tool creates a StorageClass using the Standard NFS CSI driver with SBD-optimized 
mount options including cache=none, sync, and local_lock=none to ensure proper 
inter-node heartbeat coordination and prevent split-brain scenarios.

CACHE COHERENCY FOR SBD:
• EFS provides built-in cache coherency across all NFS clients
• nfsvers=4.1: Modern NFS protocol with better performance and features
• hard: Ensures reliable mounting with automatic retries on server failure
• noresvport: AWS-recommended for better reconnection and cache consistency
• timeo/retrans: Optimized timeout and retry settings for EFS

AGGRESSIVE CACHE COHERENCY:
For strict SBD coordination use --aggressive-coherency flag which adds:
• actimeo=0: Disable ALL attribute caching for real-time updates
• sync: Force synchronous operations to prevent cache lag
• lookupcache=none: Disable directory lookup caching
• local_lock=none: Send all locks to server for distributed coordination
• Smaller buffer sizes (65KB) for reduced cache windows

Use this mode when SBD cache coherency tests fail with standard options.

EXAMPLES:

    # Standard setup (good performance, may have cache delays)
    %s --nfs-server=fs-12345.efs.us-east-1.amazonaws.com --nfs-share=/

    # Aggressive coherency (strict SBD coordination, slower performance)
    %s --nfs-server=fs-12345.efs.us-east-1.amazonaws.com --nfs-share=/ --aggressive-coherency

    # Use any existing NFS server with aggressive coherency
    %s --nfs-server=192.168.1.100 --nfs-share=/shared/sbd --aggressive-coherency

    # Clean up created StorageClass
    %s --cleanup --storage-class-name sbd-nfs-coherent

    # Preview changes without executing
    %s --nfs-server=fs-12345.efs.us-east-1.amazonaws.com --nfs-share=/ --dry-run

REQUIREMENTS:
    • Existing NFS server (EFS, local NFS, NetApp, etc.)
    • Standard NFS CSI driver installed in cluster
    • Cluster admin permissions

INSTALLATION OF STANDARD NFS CSI DRIVER:
    kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/csi-driver-nfs/master/deploy/install-driver.yaml

MOUNT OPTIONS APPLIED:
The tool creates a StorageClass with these EFS-optimized mount options:
• nfsvers=4.1, hard, noresvport (reliability and cache coherency)
• rsize=1048576, wsize=1048576 (performance optimization)
• timeo=600, retrans=2 (EFS-optimized timeout and retry settings)

This ensures optimal SBD operation with proper inter-node heartbeat coordination.

OPTIONS:
`, os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0], os.Args[0])

	flag.PrintDefaults()
}

func printResults(result *storage.SetupResult) {
	fmt.Println("\n🎉 Shared Storage Setup Completed Successfully!")
	fmt.Println("==========================================")

	if result.StorageClassName != "" {
		fmt.Printf("💾 StorageClass: %s\n", result.StorageClassName)
	}

	if len(result.MountTargets) > 0 {
		fmt.Printf("🔗 Mount Targets: %d created\n", len(result.MountTargets))
	}

	fmt.Println("\n✅ Your cluster now has ReadWriteMany (RWX) storage capability!")
	fmt.Printf("   Use StorageClass '%s' in your PVCs for shared storage.\n", result.StorageClassName)
}
