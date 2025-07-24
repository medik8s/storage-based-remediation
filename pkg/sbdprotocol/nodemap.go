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

// Package sbdprotocol implements node-to-slot mapping for SBD devices
package sbdprotocol

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"sort"
	"sync"
	"time"
)

// SBD Node Mapping Constants
const (
	// SBD_NODE_MAP_FILE_SUFFIX is the suffix for the node mapping file
	// The node mapping is stored in a separate file alongside the SBD device
	// e.g., /dev/sbd0 -> /dev/sbd0.nodemap
	SBD_NODE_MAP_FILE_SUFFIX = ".nodemap"

	// SBD_NODE_MAP_MAGIC is the magic string for node mapping data
	SBD_NODE_MAP_MAGIC = "SBDNMAP1"

	// SBD_NODE_MAP_VERSION is the current version of the node mapping format
	SBD_NODE_MAP_VERSION = 1

	// SBD_USABLE_SLOTS_START is the first slot available for node communication
	// All slots are now available since node mapping is stored in a separate file
	SBD_USABLE_SLOTS_START = 1

	// SBD_USABLE_SLOTS_END is the last slot available for node communication
	SBD_USABLE_SLOTS_END = SBD_MAX_NODES

	// SBD_MAX_RETRIES is the maximum number of collision resolution attempts
	SBD_MAX_RETRIES = 10
)

// NodeMapEntry represents a single entry in the node mapping table
type NodeMapEntry struct {
	NodeName    string    `json:"node_name"`
	SlotID      uint16    `json:"slot_id"`
	Hash        uint32    `json:"hash"`
	Timestamp   time.Time `json:"timestamp"`
	LastSeen    time.Time `json:"last_seen"`
	ClusterName string    `json:"cluster_name,omitempty"`
}

// NodeMapTable represents the complete node-to-slot mapping table
type NodeMapTable struct {
	Magic       [8]byte                  `json:"-"`
	Version     uint64                   `json:"version"`
	ClusterName string                   `json:"cluster_name"`
	Entries     map[string]*NodeMapEntry `json:"entries"`
	SlotUsage   map[uint16]string        `json:"slot_usage"` // slot_id -> node_name
	LastUpdate  time.Time                `json:"last_update"`
	Checksum    uint32                   `json:"-"`
	mutex       sync.RWMutex             `json:"-"`
}

// NewNodeMapTable creates a new node mapping table
func NewNodeMapTable(clusterName string) *NodeMapTable {
	var magic [8]byte
	copy(magic[:], SBD_NODE_MAP_MAGIC)

	return &NodeMapTable{
		Magic:       magic,
		Version:     1, // Start with version 1
		ClusterName: clusterName,
		Entries:     make(map[string]*NodeMapEntry),
		SlotUsage:   make(map[uint16]string),
		LastUpdate:  time.Now(),
	}
}

// NodeHasher provides consistent hashing for node names
type NodeHasher struct {
	clusterSalt string
}

// NewNodeHasher creates a new node hasher with cluster-specific salt
func NewNodeHasher(clusterName string) *NodeHasher {
	return &NodeHasher{
		clusterSalt: fmt.Sprintf("sbd-cluster-%s", clusterName),
	}
}

// HashNodeName generates a consistent hash for a node name
func (h *NodeHasher) HashNodeName(nodeName string) uint32 {
	// Use SHA256 for initial hash to get good distribution
	hasher := sha256.New()
	hasher.Write([]byte(h.clusterSalt))
	hasher.Write([]byte(nodeName))
	hash := hasher.Sum(nil)

	// Convert to uint32 using little-endian for consistency with SBD protocol
	// This ensures compatibility across different architectures (amd64, arm64, s390x, ppc64le)
	return binary.LittleEndian.Uint32(hash[:4])
}

// CalculateSlotID calculates the preferred slot ID for a node based on its hash
func (h *NodeHasher) CalculateSlotID(nodeName string, attempt int) uint16 {
	hash := h.HashNodeName(nodeName)

	// Add attempt number to handle collisions
	hash = crc32.ChecksumIEEE([]byte(fmt.Sprintf("%d-%d", hash, attempt)))

	// Map to usable slot range [1, 255]
	slotRange := SBD_USABLE_SLOTS_END - SBD_USABLE_SLOTS_START + 1
	slotID := uint16(hash%uint32(slotRange)) + SBD_USABLE_SLOTS_START

	return slotID
}

// AssignSlot assigns a slot to a node name using consistent hashing with collision detection
func (table *NodeMapTable) AssignSlot(nodeName string, hasher *NodeHasher) (uint16, error) {
	table.mutex.Lock()
	defer table.mutex.Unlock()

	// Validate node name
	if nodeName == "" {
		return 0, fmt.Errorf("node name cannot be empty")
	}

	// Check if node already has a slot assigned
	if entry, exists := table.Entries[nodeName]; exists {
		entry.LastSeen = time.Now()
		table.LastUpdate = time.Now()
		table.Version++ // Increment version for any change
		return entry.SlotID, nil
	}

	// Try to find an available slot using collision resolution
	for attempt := 0; attempt < SBD_MAX_RETRIES; attempt++ {
		preferredSlot := hasher.CalculateSlotID(nodeName, attempt)

		// Check if slot is available
		if existingNode, occupied := table.SlotUsage[preferredSlot]; !occupied {
			// Slot is available, assign it
			hash := hasher.HashNodeName(nodeName)
			entry := &NodeMapEntry{
				NodeName:    nodeName,
				SlotID:      preferredSlot,
				Hash:        hash,
				Timestamp:   time.Now(),
				LastSeen:    time.Now(),
				ClusterName: table.ClusterName,
			}

			table.Entries[nodeName] = entry
			table.SlotUsage[preferredSlot] = nodeName
			table.LastUpdate = time.Now()
			table.Version++ // Increment version for slot assignment

			return preferredSlot, nil
		} else if existingNode == nodeName {
			// This shouldn't happen, but handle gracefully
			return preferredSlot, nil
		}
		// Slot is occupied by another node, try next attempt
	}

	return 0, fmt.Errorf("failed to assign slot for node %s after %d attempts: all preferred slots are occupied", nodeName, SBD_MAX_RETRIES)
}

// GetSlotForNode returns the slot ID for a given node name
func (table *NodeMapTable) GetSlotForNode(nodeName string) (uint16, bool) {
	table.mutex.RLock()
	defer table.mutex.RUnlock()

	if entry, exists := table.Entries[nodeName]; exists {
		return entry.SlotID, true
	}
	return 0, false
}

// GetNodeForSlot returns the node name for a given slot ID
func (table *NodeMapTable) GetNodeForSlot(slotID uint16) (string, bool) {
	table.mutex.RLock()
	defer table.mutex.RUnlock()

	if nodeName, exists := table.SlotUsage[slotID]; exists {
		return nodeName, true
	}
	return "", false
}

// RemoveNode removes a node from the mapping table
func (table *NodeMapTable) RemoveNode(nodeName string) error {
	table.mutex.Lock()
	defer table.mutex.Unlock()

	entry, exists := table.Entries[nodeName]
	if !exists {
		return fmt.Errorf("node %s not found in mapping table", nodeName)
	}

	delete(table.Entries, nodeName)
	delete(table.SlotUsage, entry.SlotID)
	table.LastUpdate = time.Now()
	table.Version++ // Increment version for removal

	return nil
}

// UpdateLastSeen updates the last seen timestamp for a node
func (table *NodeMapTable) UpdateLastSeen(nodeName string) error {
	table.mutex.Lock()
	defer table.mutex.Unlock()

	entry, exists := table.Entries[nodeName]
	if !exists {
		return fmt.Errorf("node %s not found in mapping table", nodeName)
	}

	entry.LastSeen = time.Now()
	table.LastUpdate = time.Now()
	table.Version++ // Increment version for timestamp update
	return nil
}

// GetActiveNodes returns a list of nodes that have been seen recently
func (table *NodeMapTable) GetActiveNodes(maxAge time.Duration) []string {
	table.mutex.RLock()
	defer table.mutex.RUnlock()

	var activeNodes []string
	cutoff := time.Now().Add(-maxAge)

	for nodeName, entry := range table.Entries {
		if entry.LastSeen.After(cutoff) {
			activeNodes = append(activeNodes, nodeName)
		}
	}

	sort.Strings(activeNodes)
	return activeNodes
}

// GetStaleNodes returns a list of nodes that haven't been seen recently
func (table *NodeMapTable) GetStaleNodes(maxAge time.Duration) []string {
	table.mutex.RLock()
	defer table.mutex.RUnlock()

	var staleNodes []string
	cutoff := time.Now().Add(-maxAge)

	for nodeName, entry := range table.Entries {
		if entry.LastSeen.Before(cutoff) {
			staleNodes = append(staleNodes, nodeName)
		}
	}

	sort.Strings(staleNodes)
	return staleNodes
}

// CleanupStaleNodes removes nodes that haven't been seen for a specified duration
func (table *NodeMapTable) CleanupStaleNodes(maxAge time.Duration) []string {
	table.mutex.Lock()
	defer table.mutex.Unlock()

	var removedNodes []string
	cutoff := time.Now().Add(-maxAge)

	for nodeName, entry := range table.Entries {
		if entry.LastSeen.Before(cutoff) {
			delete(table.Entries, nodeName)
			delete(table.SlotUsage, entry.SlotID)
			removedNodes = append(removedNodes, nodeName)
		}
	}

	if len(removedNodes) > 0 {
		table.LastUpdate = time.Now()
		table.Version++ // Increment version for cleanup changes
	}

	sort.Strings(removedNodes)
	return removedNodes
}

// Marshal serializes the node mapping table to bytes for storage
func (table *NodeMapTable) Marshal() ([]byte, error) {
	table.mutex.RLock()
	defer table.mutex.RUnlock()

	// Create a serializable version
	data := struct {
		Magic       [8]byte                  `json:"magic"`
		Version     uint64                   `json:"version"`
		ClusterName string                   `json:"cluster_name"`
		Entries     map[string]*NodeMapEntry `json:"entries"`
		SlotUsage   map[uint16]string        `json:"slot_usage"`
		LastUpdate  time.Time                `json:"last_update"`
	}{
		Magic:       table.Magic,
		Version:     table.Version,
		ClusterName: table.ClusterName,
		Entries:     table.Entries,
		SlotUsage:   table.SlotUsage,
		LastUpdate:  table.LastUpdate,
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal node mapping table: %w", err)
	}

	// Calculate checksum
	checksum := crc32.ChecksumIEEE(jsonData)

	// Prepend checksum to the data
	result := make([]byte, 4+len(jsonData))
	binary.LittleEndian.PutUint32(result[:4], checksum)
	copy(result[4:], jsonData)

	return result, nil
}

// Unmarshal deserializes bytes into a node mapping table
func UnmarshalNodeMapTable(data []byte) (*NodeMapTable, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("invalid node mapping data: too short")
	}

	// Extract and verify checksum
	expectedChecksum := binary.LittleEndian.Uint32(data[:4])
	jsonData := data[4:]
	actualChecksum := crc32.ChecksumIEEE(jsonData)

	if expectedChecksum != actualChecksum {
		return nil, fmt.Errorf("node mapping data corruption detected: checksum mismatch. expected %v got %v", expectedChecksum, actualChecksum)
	}

	// Unmarshal JSON data
	var serializedData struct {
		Magic       [8]byte                  `json:"magic"`
		Version     uint64                   `json:"version"`
		ClusterName string                   `json:"cluster_name"`
		Entries     map[string]*NodeMapEntry `json:"entries"`
		SlotUsage   map[uint16]string        `json:"slot_usage"`
		LastUpdate  time.Time                `json:"last_update"`
	}

	if err := json.Unmarshal(jsonData, &serializedData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal node mapping table: %w", err)
	}

	// Verify magic and version
	expectedMagic := [8]byte{}
	copy(expectedMagic[:], SBD_NODE_MAP_MAGIC)
	if serializedData.Magic != expectedMagic {
		return nil, fmt.Errorf("invalid node mapping magic: expected %s", SBD_NODE_MAP_MAGIC)
	}

	// Version validation is more flexible now since we use uint64 for atomic operations
	if serializedData.Version == 0 {
		return nil, fmt.Errorf("invalid node mapping version: version cannot be zero")
	}

	// Create table
	table := &NodeMapTable{
		Magic:       serializedData.Magic,
		Version:     serializedData.Version,
		ClusterName: serializedData.ClusterName,
		Entries:     serializedData.Entries,
		SlotUsage:   serializedData.SlotUsage,
		LastUpdate:  serializedData.LastUpdate,
	}

	// Ensure maps are initialized
	if table.Entries == nil {
		table.Entries = make(map[string]*NodeMapEntry)
	}
	if table.SlotUsage == nil {
		table.SlotUsage = make(map[uint16]string)
	}

	return table, nil
}

// GetStats returns statistics about the node mapping table
func (table *NodeMapTable) GetStats() map[string]interface{} {
	table.mutex.RLock()
	defer table.mutex.RUnlock()

	activeCount := 0
	staleCount := 0
	cutoff := time.Now().Add(-5 * time.Minute) // 5 minutes

	for _, entry := range table.Entries {
		if entry.LastSeen.After(cutoff) {
			activeCount++
		} else {
			staleCount++
		}
	}

	return map[string]interface{}{
		"total_nodes":     len(table.Entries),
		"active_nodes":    activeCount,
		"stale_nodes":     staleCount,
		"slots_used":      len(table.SlotUsage),
		"slots_available": int(SBD_USABLE_SLOTS_END-SBD_USABLE_SLOTS_START+1) - len(table.SlotUsage),
		"cluster_name":    table.ClusterName,
		"last_update":     table.LastUpdate,
	}
}

// incrementVersion safely increments the table version
func (table *NodeMapTable) incrementVersion() {
	table.mutex.Lock()
	defer table.mutex.Unlock()
	table.Version++
}
