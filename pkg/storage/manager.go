package storage

import (
	"context"
	"fmt"
	"log"

	"github.com/medik8s/sbd-operator/pkg/storage/k8s"
)

// Config holds all configuration for storage setup
type Config struct {
	// NFS Configuration
	NFSServer        string
	NFSShare         string
	StorageClassName string

	// Cache Coherency Configuration
	AggressiveCoherency bool

	// Behavior flags
	DryRun     bool
	UpdateMode bool
}

// SetupResult contains the results of storage setup
type SetupResult struct {
	StorageClassName string
	MountTargets     []string
	TestPassed       bool
}

// Manager orchestrates Kubernetes operations for shared storage setup
type Manager struct {
	config     *Config
	k8sManager *k8s.Manager
}

// NewManager creates a new storage manager
func NewManager(ctx context.Context, config *Config) (*Manager, error) {
	// Set defaults
	setDefaults(config)

	if config.DryRun {
		log.Println("[DRY-RUN] Would create storage manager with configuration:")
		printConfig(config)
		return &Manager{config: config}, nil
	}

	// Initialize Kubernetes manager
	k8sManager, err := k8s.NewManager(ctx, &k8s.Config{
		StorageClassName:    config.StorageClassName,
		AggressiveCoherency: config.AggressiveCoherency,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes manager: %w", err)
	}

	return &Manager{
		config:     config,
		k8sManager: k8sManager,
	}, nil
}

// SetupSharedStorage orchestrates the complete setup process
func (m *Manager) SetupSharedStorage(ctx context.Context) (*SetupResult, error) {
	result := &SetupResult{}

	if m.config.DryRun {
		return m.dryRunSetup(ctx)
	}

	log.Printf("🚀 Starting Standard NFS CSI shared storage setup...")

	// Step 1: Check if Standard NFS CSI driver is installed
	log.Println("🔧 Checking Standard NFS CSI driver installation...")
	if err := m.k8sManager.CheckStandardNFSCSIDriver(ctx); err != nil {
		log.Println("⚠️ Standard NFS CSI driver not found, installing automatically...")
		if installErr := m.k8sManager.InstallStandardNFSCSIDriver(ctx); installErr != nil {
			return nil, fmt.Errorf("failed to install Standard NFS CSI driver: %w", installErr)
		}
	} else {
		log.Println("✅ Standard NFS CSI driver found")
	}

	// Step 2: Create StorageClass for Standard NFS CSI
	log.Println("💾 Creating Standard NFS StorageClass with SBD cache coherency options...")
	if err := m.k8sManager.CreateStandardNFSStorageClass(ctx, m.config.NFSServer, m.config.NFSShare); err != nil {
		return nil, fmt.Errorf("failed to create Standard NFS StorageClass: %w", err)
	}
	result.StorageClassName = m.config.StorageClassName
	log.Printf("✅ Standard NFS StorageClass created: %s", result.StorageClassName)

	// Step 3: Test Standard NFS storage
	log.Println("🧪 Testing Standard NFS CSI driver...")
	testPassed, err := m.k8sManager.TestCredentials(ctx, result.StorageClassName)
	if err != nil {
		log.Printf("⚠️ Storage test failed: %v", err)
		result.TestPassed = false
	} else {
		result.TestPassed = testPassed
		if testPassed {
			log.Println("✅ Standard NFS storage test passed")
		} else {
			log.Println("⚠️ Storage test failed, but setup completed")
		}
	}

	return result, nil
}

// Cleanup removes created StorageClass
func (m *Manager) Cleanup(ctx context.Context) error {
	if m.config.DryRun {
		log.Println("[DRY-RUN] Would clean up StorageClass")
		return nil
	}

	log.Println("🧹 Starting cleanup...")

	// Cleanup Kubernetes resources
	if m.k8sManager != nil {
		log.Println("🗑️ Cleaning up Kubernetes resources...")
		if err := m.k8sManager.Cleanup(ctx); err != nil {
			log.Printf("⚠️ Kubernetes cleanup failed: %v", err)
		} else {
			log.Println("✅ Kubernetes resources cleaned up")
		}
	}

	return nil
}

// dryRunSetup shows what would be done without executing
func (m *Manager) dryRunSetup(ctx context.Context) (*SetupResult, error) {
	result := &SetupResult{}

	log.Println("[DRY-RUN] 🔧 Would check Standard NFS CSI driver installation")
	log.Println("[DRY-RUN] 📦 Would automatically install Standard NFS CSI driver if not present")
	log.Println("[DRY-RUN] 💾 Would create Standard NFS StorageClass with SBD cache coherency options:")
	log.Printf("[DRY-RUN]   📡 NFS Server: %s", m.config.NFSServer)
	log.Printf("[DRY-RUN]   📁 NFS Share: %s", m.config.NFSShare)
	log.Printf("[DRY-RUN]   🔄 Mount Options: cache=none, sync, local_lock=none")
	log.Println("[DRY-RUN] 🧪 Would test Standard NFS CSI driver")

	result.StorageClassName = m.config.StorageClassName
	result.TestPassed = true

	return result, nil
}

// setDefaults sets default values for configuration
func setDefaults(config *Config) {
	if config.StorageClassName == "" {
		config.StorageClassName = "sbd-nfs-coherent"
	}
}

// printConfig prints the configuration for dry-run mode
func printConfig(config *Config) {
	log.Printf("  NFS Server: %s", config.NFSServer)
	log.Printf("  NFS Share: %s", config.NFSShare)
	log.Printf("  StorageClass Name: %s", config.StorageClassName)
	log.Printf("  Update Mode: %t", config.UpdateMode)
}
