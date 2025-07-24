/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package sbdprotocol implements node mapping management for SBD devices
package sbdprotocol

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"encoding/binary"

	"hash/crc32"

	"github.com/go-logr/logr"
)

// Error types for node manager operations
var (
	ErrVersionMismatch    = errors.New("version mismatch during atomic update")
	ErrMaxRetriesExceeded = errors.New("maximum retry attempts exceeded")
)

// Constants for atomic operations
const (
	// MaxAtomicRetries is the maximum number of retries for atomic operations
	MaxAtomicRetries = 5
	// AtomicRetryDelay is the base delay between retries (will be randomized)
	AtomicRetryDelay = 100 * time.Millisecond

	// File locking constants
	// FileLockTimeout is the maximum time to wait for acquiring a file lock
	FileLockTimeout = 5 * time.Second
	// FileLockRetryInterval is the interval between file lock acquisition attempts
	FileLockRetryInterval = 100 * time.Millisecond
)

// PathProvider interface for getting device path (needed for file locking)
type PathProvider interface {
	Path() string
}

// SBDDevice interface for block device operations
type SBDDevice interface {
	io.ReaderAt
	io.WriterAt
	Sync() error
	Close() error
	PathProvider
}

// NodeManager manages node-to-slot mappings with persistence to a separate file
type NodeManager struct {
	device             SBDDevice
	nodeMapFilePath    string // Path to the separate node mapping file
	clusterName        string
	table              *NodeMapTable
	hasher             *NodeHasher
	mutex              sync.RWMutex
	dirty              bool
	lastSync           time.Time
	logger             logr.Logger
	syncInterval       time.Duration
	staleNodeTimeout   time.Duration
	fileLockingEnabled bool
}

// NodeManagerConfig holds configuration for the node manager
type NodeManagerConfig struct {
	ClusterName      string
	SyncInterval     time.Duration
	StaleNodeTimeout time.Duration
	Logger           logr.Logger
	// File locking configuration
	FileLockingEnabled bool
}

// DefaultNodeManagerConfig returns a default configuration
func DefaultNodeManagerConfig() NodeManagerConfig {
	return NodeManagerConfig{
		ClusterName:        "default-cluster",
		SyncInterval:       30 * time.Second,
		StaleNodeTimeout:   10 * time.Minute,
		Logger:             logr.Discard(),
		FileLockingEnabled: true, // Default to enabled
	}
}

// NewNodeManager creates a new node manager with the specified SBD device
func NewNodeManager(device SBDDevice, config NodeManagerConfig) (*NodeManager, error) {
	if device == nil {
		return nil, fmt.Errorf("SBD device cannot be nil")
	}

	if config.ClusterName == "" {
		config.ClusterName = "default-cluster"
	}

	if config.SyncInterval == 0 {
		config.SyncInterval = 30 * time.Second
	}

	if config.StaleNodeTimeout == 0 {
		config.StaleNodeTimeout = 10 * time.Minute
	}

	// Determine the node mapping file path based on the SBD device path
	devicePath := device.Path()
	if devicePath == "" {
		return nil, fmt.Errorf("SBD device path is not available")
	}
	nodeMapFilePath := fmt.Sprintf("%s%s", devicePath, SBD_NODE_MAP_FILE_SUFFIX)

	manager := &NodeManager{
		device:             device,
		nodeMapFilePath:    nodeMapFilePath,
		clusterName:        config.ClusterName,
		logger:             config.Logger,
		syncInterval:       config.SyncInterval,
		staleNodeTimeout:   config.StaleNodeTimeout,
		hasher:             NewNodeHasher(config.ClusterName),
		fileLockingEnabled: config.FileLockingEnabled,
	}

	// Try to load existing mapping table from file
	if err := manager.loadFromDevice(); err != nil {
		manager.logger.Info("Failed to load existing node mapping, creating new table", "error", err)
		manager.table = NewNodeMapTable(config.ClusterName)
		manager.dirty = true
	}

	return manager, nil
}

// GetSlotForNode returns the slot ID for a given node name, assigning one if necessary
// This method uses atomic Compare-and-Swap operations to prevent race conditions
func (nm *NodeManager) GetSlotForNode(nodeName string) (uint16, error) {
	if nodeName == "" {
		return 0, fmt.Errorf("node name cannot be empty")
	}

	// First, try to get the slot without locking if it already exists locally
	nm.mutex.RLock()
	if slot, found := nm.table.GetSlotForNode(nodeName); found {
		nm.mutex.RUnlock()
		// Update last seen timestamp atomically
		if err := nm.atomicUpdateLastSeen(nodeName); err != nil {
			nm.logger.Error(err, "Failed to update last seen timestamp atomically", "nodeName", nodeName)
		}
		return slot, nil
	}
	nm.mutex.RUnlock()

	// Node doesn't exist locally, use atomic assignment
	return nm.atomicAssignSlot(nodeName)
}

// atomicAssignSlot assigns a slot using Compare-and-Swap operations
func (nm *NodeManager) atomicAssignSlot(nodeName string) (uint16, error) {
	lockFile, err := nm.acquireDeviceLock()
	if err != nil {
		return 0, fmt.Errorf("failed to acquire device lock: %w", err)
	}
	defer nm.releaseDeviceLock(lockFile)

	for attempt := 0; attempt < MaxAtomicRetries; attempt++ {
		// Step 1: Load current state from device
		nm.mutex.Lock()
		if err := nm.loadFromDevice(); err != nil {
			nm.mutex.Unlock()
			// If loading fails, create a new table
			nm.table = NewNodeMapTable(nm.clusterName)
			nm.dirty = true
		} else {
			nm.mutex.Unlock()
		}

		// Step 2: Check if node already exists (another node might have assigned it)
		nm.mutex.RLock()
		if slot, found := nm.table.GetSlotForNode(nodeName); found {
			nm.mutex.RUnlock()
			nm.logger.Info("Node found in mapping during atomic assignment", "nodeName", nodeName, "slotID", slot)
			return slot, nil
		}
		originalVersion := nm.table.Version
		nm.mutex.RUnlock()

		// Step 3: Assign slot locally
		nm.mutex.Lock()
		slot, err := nm.table.AssignSlot(nodeName, nm.hasher)
		if err != nil {
			nm.mutex.Unlock()
			return 0, fmt.Errorf("failed to assign slot for node %s: %w", nodeName, err)
		}
		nm.dirty = true
		nm.mutex.Unlock()

		// Step 4: Attempt atomic write with version check
		if err := nm.atomicSyncToDeviceWithLock(originalVersion, lockFile); err != nil {
			if errors.Is(err, ErrVersionMismatch) {
				nm.logger.V(1).Info("Version mismatch during atomic assignment, retrying",
					"nodeName", nodeName,
					"attempt", attempt+1,
					"maxAttempts", MaxAtomicRetries)

				// Add randomized exponential backoff to reduce contention
				baseDelay := AtomicRetryDelay * time.Duration(1<<attempt) // Exponential backoff
				jitter := time.Duration(rand.Intn(int(baseDelay / 2)))    // Add up to 50% jitter
				totalDelay := baseDelay + jitter

				nm.logger.V(2).Info("Retrying with exponential backoff",
					"baseDelay", baseDelay,
					"jitter", jitter,
					"totalDelay", totalDelay)

				time.Sleep(totalDelay)
				continue
			}
			return 0, fmt.Errorf("failed to sync node mapping atomically: %w", err)
		}

		// Success!
		nm.logger.Info("Assigned new slot to node atomically", "nodeName", nodeName, "slotID", slot, "entries", len(nm.table.Entries))
		return slot, nil
	}

	return 0, fmt.Errorf("%w: failed to assign slot for node %s after %d attempts", ErrMaxRetriesExceeded, nodeName, MaxAtomicRetries)
}

// atomicUpdateLastSeen updates the last seen timestamp using atomic operations
func (nm *NodeManager) atomicUpdateLastSeen(nodeName string) error {
	for attempt := 0; attempt < MaxAtomicRetries; attempt++ {
		// Step 1: Load current state
		nm.mutex.Lock()
		if err := nm.loadFromDevice(); err != nil {
			nm.mutex.Unlock()
			return fmt.Errorf("failed to load current state: %w", err)
		}
		originalVersion := nm.table.Version
		nm.mutex.Unlock()

		// Step 2: Update timestamp locally
		nm.mutex.Lock()
		if err := nm.table.UpdateLastSeen(nodeName); err != nil {
			nm.mutex.Unlock()
			return err
		}
		nm.dirty = true
		nm.mutex.Unlock()

		// Step 3: Attempt atomic write
		if err := nm.atomicSyncToDevice(originalVersion); err != nil {
			if errors.Is(err, ErrVersionMismatch) {
				time.Sleep(AtomicRetryDelay)
				continue
			}
			return fmt.Errorf("failed to sync timestamp update atomically: %w", err)
		}

		return nil
	}

	return fmt.Errorf("%w: failed to update last seen for node %s", ErrMaxRetriesExceeded, nodeName)
}

// GetNodeForSlot returns the node name for a given slot ID
func (nm *NodeManager) GetNodeForSlot(slotID uint16) (string, bool) {
	nm.mutex.RLock()
	defer nm.mutex.RUnlock()

	return nm.table.GetNodeForSlot(slotID)
}

// RemoveNode removes a node from the mapping table
func (nm *NodeManager) RemoveNode(nodeName string) error {
	nm.mutex.Lock()
	defer nm.mutex.Unlock()

	if err := nm.table.RemoveNode(nodeName); err != nil {
		return err
	}

	nm.dirty = true
	nm.logger.Info("Removed node from mapping", "nodeName", nodeName)

	return nil
}

// GetActiveNodes returns a list of nodes that have been seen recently
func (nm *NodeManager) GetActiveNodes() []string {
	nm.mutex.RLock()
	defer nm.mutex.RUnlock()

	return nm.table.GetActiveNodes(nm.staleNodeTimeout)
}

// GetStats returns statistics about the node mapping
func (nm *NodeManager) GetStats() map[string]interface{} {
	nm.mutex.RLock()
	defer nm.mutex.RUnlock()

	stats := nm.table.GetStats()
	stats["last_sync"] = nm.lastSync
	stats["sync_interval"] = nm.syncInterval
	stats["stale_timeout"] = nm.staleNodeTimeout
	stats["node_map_file"] = nm.nodeMapFilePath

	return stats
}

// Sync forces an immediate synchronization of the mapping table to the SBD device
func (nm *NodeManager) Sync() error {
	nm.mutex.Lock()
	defer nm.mutex.Unlock()

	return nm.syncToDevice()
}

// ReloadFromDevice forces a reload of the node mapping table from the device
func (nm *NodeManager) ReloadFromDevice() error {
	nm.mutex.Lock()
	defer nm.mutex.Unlock()

	// Use coordinated read with file locking to prevent reading during writes
	return nm.ReadWithLock("reload node mapping", func() error {
		return nm.loadFromDeviceWithRecovery()
	})
}

// CleanupStaleNodes removes nodes that haven't been seen for the configured timeout
func (nm *NodeManager) CleanupStaleNodes() ([]string, error) {
	for attempt := 0; attempt < MaxAtomicRetries; attempt++ {
		// Step 1: Load current state from device
		nm.mutex.Lock()
		if err := nm.loadFromDevice(); err != nil {
			nm.mutex.Unlock()
			return nil, fmt.Errorf("failed to load current state for cleanup: %w", err)
		}
		originalVersion := nm.table.Version
		nm.mutex.Unlock()

		// Step 2: Clean up stale nodes locally
		nm.mutex.Lock()
		removedNodes := nm.table.CleanupStaleNodes(nm.staleNodeTimeout)
		if len(removedNodes) == 0 {
			// No nodes to clean up
			nm.mutex.Unlock()
			return removedNodes, nil
		}
		nm.dirty = true
		nm.mutex.Unlock()

		// Step 3: Attempt atomic write
		if err := nm.atomicSyncToDevice(originalVersion); err != nil {
			if errors.Is(err, ErrVersionMismatch) {
				nm.logger.V(1).Info("Version mismatch during cleanup, retrying",
					"attempt", attempt+1,
					"maxAttempts", MaxAtomicRetries,
					"removedNodes", removedNodes)
				time.Sleep(AtomicRetryDelay)
				continue
			}
			return nil, fmt.Errorf("failed to sync after cleanup atomically: %w", err)
		}

		// Success!
		nm.logger.Info("Cleaned up stale nodes atomically", "count", len(removedNodes), "nodes", removedNodes)
		return removedNodes, nil
	}

	return nil, fmt.Errorf("%w: failed to cleanup stale nodes after %d attempts", ErrMaxRetriesExceeded, MaxAtomicRetries)
}

// StartPeriodicSync starts a background goroutine that periodically syncs and cleans up
func (nm *NodeManager) StartPeriodicSync() chan struct{} {
	stopChan := make(chan struct{})

	go func() {
		ticker := time.NewTicker(nm.syncInterval)
		defer ticker.Stop()

		cleanupTicker := time.NewTicker(nm.staleNodeTimeout / 2) // Cleanup twice as often as stale timeout
		defer cleanupTicker.Stop()

		for {
			select {
			case <-stopChan:
				nm.logger.Info("Stopping periodic sync")
				return
			case <-ticker.C:
				if err := nm.Sync(); err != nil {
					nm.logger.Error(err, "Periodic sync failed")
				}
			case <-cleanupTicker.C:
				if _, err := nm.CleanupStaleNodes(); err != nil {
					nm.logger.Error(err, "Periodic cleanup failed")
				}
			}
		}
	}()

	nm.logger.Info("Started periodic sync", "syncInterval", nm.syncInterval, "staleTimeout", nm.staleNodeTimeout)
	return stopChan
}

// loadFromDevice loads the node mapping table from the separate node mapping file
func (nm *NodeManager) loadFromDevice() error {
	// Read from the separate node mapping file
	data, err := os.ReadFile(nm.nodeMapFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("node mapping file does not exist: %s", nm.nodeMapFilePath)
		}
		return fmt.Errorf("failed to read node mapping file %s: %w", nm.nodeMapFilePath, err)
	}

	// Unmarshal the data
	table, err := UnmarshalNodeMapTable(data)
	if err != nil {
		return fmt.Errorf("failed to unmarshal node mapping table from file %s: %w", nm.nodeMapFilePath, err)
	}

	// Verify cluster name matches
	if table.ClusterName != nm.clusterName {
		return fmt.Errorf("cluster name mismatch: expected %s, got %s", nm.clusterName, table.ClusterName)
	}

	nm.table = table
	nm.lastSync = time.Now()
	nm.dirty = false

	nm.logger.Info("Loaded node mapping from file",
		"nodeCount", len(table.Entries),
		"clusterName", table.ClusterName,
		"lastUpdate", table.LastUpdate,
		"filePath", nm.nodeMapFilePath)

	return nil
}

// loadFromDeviceWithRecovery attempts to load the node mapping table with recovery strategies
func (nm *NodeManager) loadFromDeviceWithRecovery() error {
	// First, try the normal load process
	if err := nm.loadFromDevice(); err != nil {
		nm.logger.Info("Primary load failed, attempting recovery", "error", err)

		// Attempt recovery strategies
		if recoveryErr := nm.attemptTableRecovery(err); recoveryErr != nil {
			nm.logger.Error(recoveryErr, "All recovery attempts failed, creating new table")

			// Last resort: create a new clean table
			nm.table = NewNodeMapTable(nm.clusterName)
			nm.dirty = true

			// Try to immediately sync the new table to clear corruption
			if syncErr := nm.syncToDevice(); syncErr != nil {
				nm.logger.Error(syncErr, "Failed to sync new clean table")
				return fmt.Errorf("failed to create clean table after corruption: %w", syncErr)
			}

			nm.logger.Info("Created new clean node mapping table to replace corrupted data",
				"clusterName", nm.clusterName)
			return nil
		}
	}

	return nil
}

// attemptTableRecovery tries multiple strategies to recover from corruption
func (nm *NodeManager) attemptTableRecovery(originalErr error) error {
	nm.logger.Info("Attempting node mapping table recovery", "originalError", originalErr)

	// Strategy 1: Check if corruption is due to partial write (try reading multiple times)
	if err := nm.retryLoadWithDelay(); err == nil {
		nm.logger.Info("Recovery successful: corruption was due to timing/partial write")
		return nil
	}

	// Strategy 2: Try to read raw data and diagnose corruption
	if err := nm.diagnoseAndFixCorruption(); err == nil {
		nm.logger.Info("Recovery successful: corruption was detected and fixed")
		return nil
	}

	// Strategy 3: Clear the corrupted slot and start fresh
	if err := nm.clearCorruptedSlot(); err == nil {
		nm.logger.Info("Recovery successful: cleared corrupted slot and created new table")
		return nil
	}

	return fmt.Errorf("all recovery strategies failed")
}

// retryLoadWithDelay attempts to load the table multiple times with delays
func (nm *NodeManager) retryLoadWithDelay() error {
	for attempt := 1; attempt <= 3; attempt++ {
		nm.logger.V(1).Info("Retry attempt to load node mapping", "attempt", attempt)

		// Add delay to allow any concurrent writes to complete
		time.Sleep(time.Duration(attempt*100) * time.Millisecond)

		if err := nm.loadFromDevice(); err == nil {
			nm.logger.Info("Retry load successful", "attempt", attempt)
			return nil
		} else {
			nm.logger.V(1).Info("Retry load failed", "attempt", attempt, "error", err)
		}
	}

	return fmt.Errorf("all retry attempts failed")
}

// diagnoseAndFixCorruption attempts to diagnose and fix specific corruption patterns
func (nm *NodeManager) diagnoseAndFixCorruption() error {
	// Read the raw file data for analysis
	fileData, err := os.ReadFile(nm.nodeMapFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			nm.logger.Info("Corruption diagnosis: file does not exist, creating new table")
			nm.table = NewNodeMapTable(nm.clusterName)
			nm.dirty = true
			return nil
		}
		return fmt.Errorf("failed to read node mapping file for diagnosis: %w", err)
	}

	// Check if it's completely empty
	if len(fileData) == 0 {
		nm.logger.Info("Corruption diagnosis: file is empty, creating new table")
		nm.table = NewNodeMapTable(nm.clusterName)
		nm.dirty = true
		return nil
	}

	// Check if we have at least a checksum
	if len(fileData) >= 4 {
		expectedChecksum := binary.LittleEndian.Uint32(fileData[:4])
		jsonData := fileData[4:]

		// Files don't have null padding like slots, so use full JSON data
		if len(jsonData) > 0 {
			actualChecksum := crc32.ChecksumIEEE(jsonData)

			nm.logger.Info("Corruption diagnosis details",
				"expectedChecksum", fmt.Sprintf("0x%08x", expectedChecksum),
				"actualChecksum", fmt.Sprintf("0x%08x", actualChecksum),
				"jsonDataLength", len(jsonData),
				"checksumMatch", expectedChecksum == actualChecksum)

			// If checksums match, try to unmarshal
			if expectedChecksum == actualChecksum {
				nm.logger.Info("Attempting to unmarshal with file data")

				// Try to unmarshal the data
				table, err := UnmarshalNodeMapTable(fileData)
				if err == nil {
					nm.logger.Info("Successfully recovered table from file",
						"nodeCount", len(table.Entries),
						"clusterName", table.ClusterName)

					// Verify cluster name matches
					if table.ClusterName == nm.clusterName {
						nm.table = table
						nm.lastSync = time.Now()
						nm.dirty = false
						return nil
					} else {
						nm.logger.Info("Recovered table has wrong cluster name",
							"expected", nm.clusterName, "actual", table.ClusterName)
					}
				}
			}
		}
	}

	return fmt.Errorf("unable to diagnose or fix corruption")
}

// clearCorruptedSlot clears the corrupted slot and creates a new table
func (nm *NodeManager) clearCorruptedSlot() error {
	nm.logger.Info("Clearing corrupted node mapping slot")

	// Acquire lock to prevent interference
	lockFile, err := nm.acquireDeviceLock()
	if err != nil {
		return fmt.Errorf("failed to acquire lock for slot clearing: %w", err)
	}
	defer nm.releaseDeviceLock(lockFile)

	// Remove the corrupted file
	if err := os.Remove(nm.nodeMapFilePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove corrupted node mapping file %s: %w", nm.nodeMapFilePath, err)
	}

	// Create new table
	nm.table = NewNodeMapTable(nm.clusterName)
	nm.dirty = true

	nm.logger.Info("Successfully cleared corrupted slot and created new table")
	return nil
}

// syncToDevice writes the node mapping table to the SBD device
func (nm *NodeManager) syncToDevice() error {
	if !nm.dirty {
		return nil // Nothing to sync
	}

	// Marshal the table
	data, err := nm.table.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal node mapping table: %w", err)
	}

	// Write to the separate node mapping file
	// Create the directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(nm.nodeMapFilePath), 0755); err != nil {
		return fmt.Errorf("failed to create directory for node mapping file: %w", err)
	}

	// Write data to temporary file first, then rename (atomic operation)
	tempFilePath := nm.nodeMapFilePath + ".tmp"
	if err := os.WriteFile(tempFilePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write node mapping to temporary file %s: %w", tempFilePath, err)
	}

	// Atomic rename to final location
	if err := os.Rename(tempFilePath, nm.nodeMapFilePath); err != nil {
		// Clean up temporary file on failure
		os.Remove(tempFilePath)
		return fmt.Errorf("failed to rename temporary file to %s: %w", nm.nodeMapFilePath, err)
	}

	nm.lastSync = time.Now()
	nm.dirty = false

	nm.logger.V(1).Info("Synced node mapping to device",
		"dataSize", len(data),
		"nodeCount", len(nm.table.Entries))

	return nil
}

// isEmptySlot checks if a slot contains only zeros or is uninitialized
func isEmptySlot(data []byte) bool {
	for _, b := range data {
		if b != 0 {
			return false
		}
	}
	return true
}

// Close closes the node manager and syncs any pending changes
func (nm *NodeManager) Close() error {
	nm.mutex.Lock()
	defer nm.mutex.Unlock()

	// Sync any pending changes
	if nm.dirty {
		if err := nm.syncToDevice(); err != nil {
			nm.logger.Error(err, "Failed to sync during close")
			return err
		}
	}

	nm.logger.Info("Node manager closed")
	return nil
}

// ValidateIntegrity checks the integrity of the node mapping
func (nm *NodeManager) ValidateIntegrity() error {
	nm.mutex.RLock()
	defer nm.mutex.RUnlock()

	// Check for duplicate slots
	slotCount := make(map[uint16]int)
	for _, entry := range nm.table.Entries {
		slotCount[entry.SlotID]++
	}

	for slotID, count := range slotCount {
		if count > 1 {
			return fmt.Errorf("duplicate slot assignment detected: slot %d assigned to %d nodes", slotID, count)
		}
	}

	// Check for orphaned slots in SlotUsage
	for slotID, nodeName := range nm.table.SlotUsage {
		if entry, exists := nm.table.Entries[nodeName]; !exists {
			return fmt.Errorf("orphaned slot usage: slot %d points to non-existent node %s", slotID, nodeName)
		} else if entry.SlotID != slotID {
			return fmt.Errorf("slot usage mismatch: slot %d points to node %s, but node has slot %d", slotID, nodeName, entry.SlotID)
		}
	}

	// Check for orphaned entries
	for nodeName, entry := range nm.table.Entries {
		if usedNode, exists := nm.table.SlotUsage[entry.SlotID]; !exists {
			return fmt.Errorf("orphaned entry: node %s has slot %d, but slot is not in usage map", nodeName, entry.SlotID)
		} else if usedNode != nodeName {
			return fmt.Errorf("entry mismatch: node %s has slot %d, but slot is assigned to %s", nodeName, entry.SlotID, usedNode)
		}
	}

	return nil
}

// GetClusterName returns the cluster name
func (nm *NodeManager) GetClusterName() string {
	return nm.clusterName
}

// IsDirty returns whether the mapping table has unsaved changes
func (nm *NodeManager) IsDirty() bool {
	nm.mutex.RLock()
	defer nm.mutex.RUnlock()
	return nm.dirty
}

// GetLastSync returns the timestamp of the last successful sync
func (nm *NodeManager) GetLastSync() time.Time {
	nm.mutex.RLock()
	defer nm.mutex.RUnlock()
	return nm.lastSync
}

// atomicSyncToDevice writes the node mapping table to the SBD device with version checking
// This implements Compare-and-Swap semantics to prevent race conditions
func (nm *NodeManager) atomicSyncToDevice(expectedVersion uint64) error {
	return nm.atomicSyncToDeviceWithLock(expectedVersion, nil)
}

// atomicSyncToDeviceWithLock writes the node mapping table to the SBD device with version checking
// using an optional existing lockFile. If lockFile is nil, a new lock will be acquired.
// This implements Compare-and-Swap semantics to prevent race conditions
func (nm *NodeManager) atomicSyncToDeviceWithLock(expectedVersion uint64, lockFile *os.File) error {
	if !nm.dirty {
		return nil // Nothing to sync
	}

	// Marshal the table
	data, err := nm.table.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal node mapping table: %w", err)
	}

	// Note: No size limit check needed for file-based storage (files can grow beyond slot size)

	// Use advisory locking to prevent concurrent physical writes
	// This prevents data corruption when multiple nodes write to slot 0 simultaneously
	if lockFile == nil {
		// No existing lock provided, acquire a new one
		lockFile, err = nm.acquireDeviceLock()
		if err != nil {
			return fmt.Errorf("failed to acquire device lock: %w", err)
		}

		defer func() {
			if unlockErr := nm.releaseDeviceLock(lockFile); unlockErr != nil {
				nm.logger.Error(unlockErr, "Failed to release device lock")
			}
		}()
	}

	// Read current version from file to check for conflicts
	// This read happens under the same lock as the write to prevent race conditions
	currentFileData, err := os.ReadFile(nm.nodeMapFilePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read current node mapping file for version check: %w", err)
	}

	// Check if file exists and has content (not first time write)
	if err == nil && len(currentFileData) > 0 {
		// Parse current version from file
		currentTable, err := UnmarshalNodeMapTable(currentFileData)
		if err != nil {
			// If we can't parse the current data, it might be corrupted
			// This could happen if we're reading during another node's write
			nm.logger.Info("Warning: failed to parse current node mapping file during version check",
				"error", err,
				"expectedVersion", expectedVersion,
				"attempting", "corruption recovery")

			// Try a brief retry in case we caught a write in progress
			time.Sleep(50 * time.Millisecond)

			// Re-read and try again
			retryFileData, retryErr := os.ReadFile(nm.nodeMapFilePath)
			if retryErr == nil && len(retryFileData) > 0 {
				currentTable, err = UnmarshalNodeMapTable(retryFileData)
				if err != nil {
					nm.logger.Info("Corruption persists after retry, proceeding with write to clear it", "error", err)
					// Proceed with write to potentially fix corruption
				}
			}
		}

		// If we successfully parsed the table, check version
		if err == nil && currentTable != nil {
			if currentTable.Version != expectedVersion {
				nm.logger.V(1).Info("Version mismatch detected",
					"expectedVersion", expectedVersion,
					"currentVersion", currentTable.Version,
					"clusterName", currentTable.ClusterName)
				return ErrVersionMismatch
			}
		}
	}

	// Write to the separate node mapping file using atomic operations
	// Create the directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(nm.nodeMapFilePath), 0755); err != nil {
		return fmt.Errorf("failed to create directory for node mapping file: %w", err)
	}

	// Write data to temporary file first, then rename (atomic operation)
	tempFilePath := nm.nodeMapFilePath + ".tmp"
	if err := os.WriteFile(tempFilePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write node mapping to temporary file %s: %w", tempFilePath, err)
	}

	// Atomic rename to final location
	if err := os.Rename(tempFilePath, nm.nodeMapFilePath); err != nil {
		// Clean up temporary file on failure
		os.Remove(tempFilePath)
		return fmt.Errorf("failed to rename temporary file to %s: %w", nm.nodeMapFilePath, err)
	}

	// Note: File write verification could be added here if needed
	// For now, we rely on atomic file operations (write to .tmp, then rename)

	nm.lastSync = time.Now()
	nm.dirty = false

	nm.logger.V(1).Info("Synced node mapping to device atomically",
		"dataSize", len(data),
		"nodeCount", len(nm.table.Entries),
		"version", nm.table.Version)

	return nil
}

// applyJitterDelay applies a randomized delay with the specified maximum duration and logs the action
func (nm *NodeManager) applyJitterDelay(maxDelayMs int, reason string) {
	jitter := time.Duration(rand.Intn(maxDelayMs)) * time.Millisecond
	time.Sleep(jitter)
	nm.logger.V(2).Info("Applied jitter delay for coordination",
		"jitter", jitter,
		"reason", reason)
}

// acquireDeviceLock attempts to acquire an exclusive file lock on the node mapping file
// This prevents concurrent writes to the node mapping file which can cause corruption
func (nm *NodeManager) acquireDeviceLock() (*os.File, error) {
	// Check if file locking is disabled - use jitter-only coordination
	if !nm.fileLockingEnabled {
		nm.applyJitterDelay(100, "file locking disabled")
		return nil, nil // No lock file when using jitter-only approach
	}

	// File locking is enabled - proceed with file locking on the node mapping file
	// Add small jitter before lock acquisition to reduce contention during cluster events
	nm.applyJitterDelay(50, "reducing file lock contention")

	nm.logger.V(1).Info("Attempting to acquire file lock on node mapping file", "filePath", nm.nodeMapFilePath)

	// Create a lock file alongside the node mapping file for coordination
	lockFilePath := nm.nodeMapFilePath + ".lock"

	// Ensure the directory exists
	if err := os.MkdirAll(filepath.Dir(lockFilePath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory for lock file: %w", err)
	}

	// Open/create the lock file for exclusive locking
	lockFile, err := os.OpenFile(lockFilePath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open lock file %s: %w", lockFilePath, err)
	}

	// Try to acquire an exclusive lock with timeout
	lockCtx, cancel := context.WithTimeout(context.Background(), FileLockTimeout)
	defer cancel()

	lockAcquired := make(chan error, 1)
	go func() {
		err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX)
		lockAcquired <- err
	}()

	select {
	case err := <-lockAcquired:
		if err != nil {
			lockFile.Close()
			return nil, fmt.Errorf("failed to acquire file lock: %w", err)
		}
		nm.logger.V(1).Info("Successfully acquired file lock on node mapping file", "lockFile", lockFilePath)
		return lockFile, nil

	case <-lockCtx.Done():
		// Timeout occurred, try to close the file and return error
		lockFile.Close()
		return nil, fmt.Errorf("timeout waiting for file lock on node mapping file after %v", FileLockTimeout)
	}
}

// releaseDeviceLock releases the file lock by closing the file handle
func (nm *NodeManager) releaseDeviceLock(lockFile *os.File) error {
	if lockFile == nil {
		// No lock was acquired (either jitter approach or no device path)
		return nil
	}

	devicePath := nm.device.Path()
	nm.logger.V(1).Info("Releasing file lock on SBD device", "devicePath", devicePath)
	if err := lockFile.Close(); err != nil {
		return fmt.Errorf("failed to release file lock on %s: %w", devicePath, err)
	}
	return nil
}

// createLockFile creates a lock file atomically
func (nm *NodeManager) createLockFile(lockPath string) error {
	// Not used in the randomized delay approach
	return nil
}

// removeLockFile removes a lock file
func (nm *NodeManager) removeLockFile(lockPath string) error {
	// Not used in the randomized delay approach
	return nil
}

// verifyWrite verifies that the data was written correctly by reading it back
func (nm *NodeManager) verifyWrite(expectedData []byte, offset int64) error {
	readBuffer := make([]byte, len(expectedData))
	n, err := nm.device.ReadAt(readBuffer, offset)
	if err != nil {
		return fmt.Errorf("failed to read back data for verification: %w", err)
	}

	if n != len(expectedData) {
		return fmt.Errorf("verification read size mismatch: expected %d bytes, got %d", len(expectedData), n)
	}

	// Compare the data
	for i, expected := range expectedData {
		if readBuffer[i] != expected {
			return fmt.Errorf("data verification failed at byte %d: expected 0x%02x, got 0x%02x", i, expected, readBuffer[i])
		}
	}

	nm.logger.V(2).Info("Write verification successful", "bytesVerified", len(expectedData))
	return nil
}

// WriteWithLock executes a write operation using the configured coordination strategy.
// When file locking is enabled and a device path is available, it uses POSIX file locking.
// When file locking is disabled or no device path is available, it uses randomized jitter
// to reduce write contention between nodes.
func (nm *NodeManager) WriteWithLock(operation string, fn func() error) error {
	lockFile, err := nm.acquireDeviceLock()
	if err != nil {
		return fmt.Errorf("failed to acquire device coordination for %s: %w", operation, err)
	}
	defer func() {
		if unlockErr := nm.releaseDeviceLock(lockFile); unlockErr != nil {
			nm.logger.Error(unlockErr, "Failed to release device coordination", "operation", operation)
		}
	}()

	if nm.fileLockingEnabled && lockFile != nil {
		nm.logger.V(1).Info("Executing operation with file lock",
			"operation", operation,
			"lockingEnabled", nm.fileLockingEnabled,
			"devicePath", nm.device.Path())
	} else {
		nm.logger.V(1).Info("Executing operation with jitter coordination",
			"operation", operation,
			"lockingEnabled", nm.fileLockingEnabled)
	}

	return fn()
}

// IsFileLockingEnabled returns whether file locking is enabled
func (nm *NodeManager) IsFileLockingEnabled() bool {
	return nm.fileLockingEnabled
}

// GetCoordinationStrategy returns a string describing the coordination strategy being used
func (nm *NodeManager) GetCoordinationStrategy() string {
	if nm.fileLockingEnabled {
		devicePath := nm.device.Path()
		if devicePath != "" {
			return "file-locking"
		}
		return "jitter-fallback" // File locking enabled but no device path
	}
	return "jitter-only"
}

// ReadWithLock executes a read operation using the same coordination strategy as writes.
// This ensures reads don't happen while writes are in progress, preventing corruption.
func (nm *NodeManager) ReadWithLock(operation string, fn func() error) error {
	lockFile, err := nm.acquireDeviceLock()
	if err != nil {
		return fmt.Errorf("failed to acquire device coordination for read %s: %w", operation, err)
	}
	defer func() {
		if unlockErr := nm.releaseDeviceLock(lockFile); unlockErr != nil {
			nm.logger.Error(unlockErr, "Failed to release device coordination", "operation", operation)
		}
	}()

	if nm.fileLockingEnabled && lockFile != nil {
		nm.logger.V(1).Info("Executing read operation with file lock",
			"operation", operation,
			"lockingEnabled", nm.fileLockingEnabled,
			"devicePath", nm.device.Path())
	} else {
		nm.logger.V(1).Info("Executing read operation with jitter coordination",
			"operation", operation,
			"lockingEnabled", nm.fileLockingEnabled)
	}

	return fn()
}
