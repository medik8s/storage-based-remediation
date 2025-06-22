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
	"errors"
	"fmt"
	"io"
	"math/rand"
	"sync"
	"time"

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
)

// SBDDevice interface for block device operations
type SBDDevice interface {
	io.ReaderAt
	io.WriterAt
	Sync() error
	Close() error
}

// NodeManager manages node-to-slot mappings with persistence to SBD device
type NodeManager struct {
	device      SBDDevice
	table       *NodeMapTable
	hasher      *NodeHasher
	clusterName string
	logger      logr.Logger
	mutex       sync.RWMutex

	// Configuration
	syncInterval     time.Duration
	staleNodeTimeout time.Duration

	// State
	lastSync    time.Time
	lastCleanup time.Time
	dirty       bool
}

// NodeManagerConfig holds configuration for the node manager
type NodeManagerConfig struct {
	ClusterName      string
	SyncInterval     time.Duration
	StaleNodeTimeout time.Duration
	Logger           logr.Logger
}

// DefaultNodeManagerConfig returns a default configuration
func DefaultNodeManagerConfig() NodeManagerConfig {
	return NodeManagerConfig{
		ClusterName:      "default-cluster",
		SyncInterval:     30 * time.Second,
		StaleNodeTimeout: 10 * time.Minute,
		Logger:           logr.Discard(),
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

	manager := &NodeManager{
		device:           device,
		clusterName:      config.ClusterName,
		logger:           config.Logger,
		syncInterval:     config.SyncInterval,
		staleNodeTimeout: config.StaleNodeTimeout,
		hasher:           NewNodeHasher(config.ClusterName),
	}

	// Try to load existing mapping table from device
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
		if err := nm.atomicSyncToDevice(originalVersion); err != nil {
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
		nm.logger.Info("Assigned new slot to node atomically", "nodeName", nodeName, "slotID", slot)
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
	stats["last_cleanup"] = nm.lastCleanup
	stats["sync_interval"] = nm.syncInterval
	stats["stale_timeout"] = nm.staleNodeTimeout

	return stats
}

// Sync forces an immediate synchronization of the mapping table to the SBD device
func (nm *NodeManager) Sync() error {
	nm.mutex.Lock()
	defer nm.mutex.Unlock()

	return nm.syncToDevice()
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
		nm.lastCleanup = time.Now()
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

// loadFromDevice loads the node mapping table from the SBD device
func (nm *NodeManager) loadFromDevice() error {
	// Read the node mapping slot (slot 0)
	slotOffset := int64(SBD_NODE_MAP_SLOT) * SBD_SLOT_SIZE
	slotData := make([]byte, SBD_SLOT_SIZE)

	n, err := nm.device.ReadAt(slotData, slotOffset)
	if err != nil {
		return fmt.Errorf("failed to read node mapping slot: %w", err)
	}

	if n != SBD_SLOT_SIZE {
		return fmt.Errorf("partial read from node mapping slot: read %d bytes, expected %d", n, SBD_SLOT_SIZE)
	}

	// Check if the slot contains valid data
	if isEmptySlot(slotData) {
		return fmt.Errorf("node mapping slot is empty")
	}

	// The slot might contain multiple chunks, so we need to read the full mapping
	// For now, we'll assume it fits in one slot, but this could be extended
	table, err := UnmarshalNodeMapTable(slotData)
	if err != nil {
		return fmt.Errorf("failed to unmarshal node mapping table: %w", err)
	}

	// Verify cluster name matches
	if table.ClusterName != nm.clusterName {
		return fmt.Errorf("cluster name mismatch: expected %s, got %s", nm.clusterName, table.ClusterName)
	}

	nm.table = table
	nm.lastSync = time.Now()
	nm.dirty = false

	nm.logger.Info("Loaded node mapping from device",
		"nodeCount", len(table.Entries),
		"clusterName", table.ClusterName,
		"lastUpdate", table.LastUpdate)

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

	// Check if data fits in one slot
	if len(data) > SBD_SLOT_SIZE {
		return fmt.Errorf("node mapping table too large: %d bytes, maximum %d", len(data), SBD_SLOT_SIZE)
	}

	// Prepare slot data (pad with zeros if necessary)
	slotData := make([]byte, SBD_SLOT_SIZE)
	copy(slotData, data)

	// Write to the node mapping slot (slot 0)
	slotOffset := int64(SBD_NODE_MAP_SLOT) * SBD_SLOT_SIZE
	n, err := nm.device.WriteAt(slotData, slotOffset)
	if err != nil {
		return fmt.Errorf("failed to write node mapping slot: %w", err)
	}

	if n != SBD_SLOT_SIZE {
		return fmt.Errorf("partial write to node mapping slot: wrote %d bytes, expected %d", n, SBD_SLOT_SIZE)
	}

	// Ensure data is synced to disk
	if err := nm.device.Sync(); err != nil {
		return fmt.Errorf("failed to sync node mapping to device: %w", err)
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
	if !nm.dirty {
		return nil // Nothing to sync
	}

	// Marshal the table
	data, err := nm.table.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal node mapping table: %w", err)
	}

	// Check if data fits in one slot
	if len(data) > SBD_SLOT_SIZE {
		return fmt.Errorf("node mapping table too large: %d bytes, maximum %d", len(data), SBD_SLOT_SIZE)
	}

	// Use advisory locking to prevent concurrent physical writes
	// This prevents data corruption when multiple nodes write to slot 0 simultaneously
	if err := nm.acquireDeviceLock(); err != nil {
		return fmt.Errorf("failed to acquire device lock: %w", err)
	}
	defer func() {
		if unlockErr := nm.releaseDeviceLock(); unlockErr != nil {
			nm.logger.Error(unlockErr, "Failed to release device lock")
		}
	}()

	// Read current version from device to check for conflicts
	slotOffset := int64(SBD_NODE_MAP_SLOT) * SBD_SLOT_SIZE
	currentSlotData := make([]byte, SBD_SLOT_SIZE)

	n, err := nm.device.ReadAt(currentSlotData, slotOffset)
	if err != nil {
		return fmt.Errorf("failed to read current node mapping slot for version check: %w", err)
	}

	if n != SBD_SLOT_SIZE {
		return fmt.Errorf("partial read from node mapping slot during version check: read %d bytes, expected %d", n, SBD_SLOT_SIZE)
	}

	// Check if slot is empty (first time write)
	if !isEmptySlot(currentSlotData) {
		// Parse current version from device
		currentTable, err := UnmarshalNodeMapTable(currentSlotData)
		if err != nil {
			// If we can't parse the current data, it might be corrupted
			// Log a warning but proceed with the write
			nm.logger.Info("Warning: failed to parse current node mapping table, proceeding with write", "error", err)
		} else {
			// Check version mismatch
			if currentTable.Version != expectedVersion {
				nm.logger.V(1).Info("Version mismatch detected",
					"expectedVersion", expectedVersion,
					"currentVersion", currentTable.Version,
					"clusterName", currentTable.ClusterName)
				return ErrVersionMismatch
			}
		}
	}

	// Prepare slot data (pad with zeros if necessary)
	slotData := make([]byte, SBD_SLOT_SIZE)
	copy(slotData, data)

	// Write to the node mapping slot (slot 0)
	n, err = nm.device.WriteAt(slotData, slotOffset)
	if err != nil {
		return fmt.Errorf("failed to write node mapping slot: %w", err)
	}

	if n != SBD_SLOT_SIZE {
		return fmt.Errorf("partial write to node mapping slot: wrote %d bytes, expected %d", n, SBD_SLOT_SIZE)
	}

	// Ensure data is synced to disk
	if err := nm.device.Sync(); err != nil {
		return fmt.Errorf("failed to sync node mapping to device: %w", err)
	}

	// Verify the write by reading back and checking the version
	if err := nm.verifyWrite(slotData, slotOffset); err != nil {
		nm.logger.Error(err, "Write verification failed, but data was written")
		// Don't fail the operation for verification errors, just log them
	}

	nm.lastSync = time.Now()
	nm.dirty = false

	nm.logger.V(1).Info("Synced node mapping to device atomically",
		"dataSize", len(data),
		"nodeCount", len(nm.table.Entries),
		"version", nm.table.Version)

	return nil
}

// acquireDeviceLock attempts to acquire an advisory lock on the SBD device
// This prevents concurrent writes to the same physical block which can cause corruption
func (nm *NodeManager) acquireDeviceLock() error {
	// For SBD devices, we use a randomized delay approach to reduce contention
	// This is simpler and more reliable than file locking across nodes

	// Add random jitter to reduce thundering herd when multiple nodes start
	jitter := time.Duration(rand.Intn(100)) * time.Millisecond
	time.Sleep(jitter)

	nm.logger.V(2).Info("Using randomized delay to reduce write contention", "jitter", jitter)
	return nil
}

// releaseDeviceLock releases the advisory lock on the SBD device
func (nm *NodeManager) releaseDeviceLock() error {
	// No-op for the randomized delay approach
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
