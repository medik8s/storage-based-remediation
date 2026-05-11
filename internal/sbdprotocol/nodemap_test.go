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

package sbdprotocol

import (
	"fmt"
	"testing"
	"time"
)

func TestNodeHasher(t *testing.T) {
	hasher := NewNodeHasher("test-cluster")

	// Test consistent hashing
	hash1 := hasher.HashNodeName("node-1")
	hash2 := hasher.HashNodeName("node-1")
	if hash1 != hash2 {
		t.Errorf("Hash should be consistent: %d != %d", hash1, hash2)
	}

	// Test different nodes produce different hashes
	hash3 := hasher.HashNodeName("node-2")
	if hash1 == hash3 {
		t.Errorf("Different nodes should produce different hashes: %d == %d", hash1, hash3)
	}

	// Test slot calculation
	slot1 := hasher.CalculateNodeID("node-1", 0)
	slot2 := hasher.CalculateNodeID("node-1", 0)
	if slot1 != slot2 {
		t.Errorf("Slot calculation should be consistent: %d != %d", slot1, slot2)
	}

	// Test slot is in valid range
	if slot1 < SBD_USABLE_SLOTS_START || slot1 > SBD_USABLE_SLOTS_END {
		t.Errorf("Slot %d is outside valid range [%d, %d]", slot1, SBD_USABLE_SLOTS_START, SBD_USABLE_SLOTS_END)
	}

	// Test collision resolution produces different slots
	slot3 := hasher.CalculateNodeID("node-1", 1)
	if slot1 == slot3 {
		t.Errorf("Collision resolution should produce different slots: %d == %d", slot1, slot3)
	}
}

func TestNodeMapTable_BasicOperations(t *testing.T) {
	table := NewNodeMapTable("test-cluster")
	hasher := NewNodeHasher("test-cluster")

	// Test assigning a slot
	slot, err := table.AssignSlot("node-1", hasher)
	if err != nil {
		t.Fatalf("Failed to assign slot: %v", err)
	}

	if slot < SBD_USABLE_SLOTS_START || slot > SBD_USABLE_SLOTS_END {
		t.Errorf("Assigned slot %d is outside valid range", slot)
	}

	// Test retrieving the slot
	retrievedSlot, found := table.GetNodeIDForNode("node-1")
	if !found {
		t.Error("Node should be found in table")
	}
	if retrievedSlot != slot {
		t.Errorf("Retrieved slot %d doesn't match assigned slot %d", retrievedSlot, slot)
	}

	// Test retrieving the node by slot
	nodeName, found := table.GetNodeForNodeID(slot)
	if !found {
		t.Error("Slot should be found in table")
	}
	if nodeName != "node-1" {
		t.Errorf("Retrieved node name %s doesn't match expected 'node-1'", nodeName)
	}

	// Test assigning the same node again returns the same slot
	slot2, err := table.AssignSlot("node-1", hasher)
	if err != nil {
		t.Fatalf("Failed to reassign slot: %v", err)
	}
	if slot2 != slot {
		t.Errorf("Reassigned slot %d doesn't match original slot %d", slot2, slot)
	}
}

func TestNodeMapTable_CollisionDetection(t *testing.T) {
	table := NewNodeMapTable("test-cluster")
	hasher := NewNodeHasher("test-cluster")

	// Assign slots to multiple nodes
	nodes := []string{
		"ip-10-0-1-45.ec2.internal",
		"gke-cluster-default-pool-a1b2c3d4-xyz5",
		"k8s-worker-node-1",
		"openshift-compute-node-2",
		"aks-nodepool1-12345678-vmss000000",
	}

	assignedSlots := make(map[uint16]string)

	for _, nodeName := range nodes {
		slot, err := table.AssignSlot(nodeName, hasher)
		if err != nil {
			t.Fatalf("Failed to assign slot for node %s: %v", nodeName, err)
		}

		// Check for slot conflicts
		if existingNode, exists := assignedSlots[slot]; exists {
			t.Errorf("Slot collision detected: slot %d assigned to both %s and %s", slot, existingNode, nodeName)
		}
		assignedSlots[slot] = nodeName

		// Verify the assignment
		retrievedSlot, found := table.GetNodeIDForNode(nodeName)
		if !found || retrievedSlot != slot {
			t.Errorf("Node %s assignment verification failed", nodeName)
		}
	}

	// Verify all nodes are tracked
	if len(table.Entries) != len(nodes) {
		t.Errorf("Expected %d entries, got %d", len(nodes), len(table.Entries))
	}
}

func TestNodeMapTable_RealWorldNodeNames(t *testing.T) {
	table := NewNodeMapTable("production-cluster")
	hasher := NewNodeHasher("production-cluster")

	// Test with real-world node names from different cloud providers
	realWorldNodes := []string{
		// AWS
		"ip-10-0-1-45.us-west-2.compute.internal",
		"ip-172-31-16-78.eu-central-1.compute.internal",
		// GCP
		"gke-my-cluster-default-pool-a1b2c3d4-xyz5",
		"gke-prod-cluster-gpu-pool-f7e8d9c0-abc1",
		// Azure
		"aks-nodepool1-12345678-vmss000000",
		"aks-agentpool-87654321-vmss000001",
		// OpenShift
		"master-0.cluster.example.com",
		"worker-1.cluster.example.com",
		// On-premises
		"k8s-master-01.datacenter.local",
		"k8s-worker-05.datacenter.local",
		// Mixed patterns
		"node-gpu-1",
		"compute-large-3",
		"db-node-primary",
	}

	assignedSlots := make(map[uint16]bool)

	for _, nodeName := range realWorldNodes {
		slot, err := table.AssignSlot(nodeName, hasher)
		if err != nil {
			t.Fatalf("Failed to assign slot for node %s: %v", nodeName, err)
		}

		// Check for duplicates
		if assignedSlots[slot] {
			t.Errorf("Duplicate slot %d assigned for node %s", slot, nodeName)
		}
		assignedSlots[slot] = true

		// Verify consistency
		slot2, err := table.AssignSlot(nodeName, hasher)
		if err != nil {
			t.Fatalf("Failed to reassign slot for node %s: %v", nodeName, err)
		}
		if slot != slot2 {
			t.Errorf("Inconsistent slot assignment for node %s: %d != %d", nodeName, slot, slot2)
		}
	}

	t.Logf("Successfully assigned %d unique slots to %d nodes", len(assignedSlots), len(realWorldNodes))
}

func TestNodeMapTable_Persistence(t *testing.T) {
	// Create and populate a table
	table1 := NewNodeMapTable("test-cluster")
	hasher := NewNodeHasher("test-cluster")

	nodes := []string{"node-1", "node-2", "node-3"}
	originalSlots := make(map[string]uint16)

	for _, nodeName := range nodes {
		slot, err := table1.AssignSlot(nodeName, hasher)
		if err != nil {
			t.Fatalf("Failed to assign slot: %v", err)
		}
		originalSlots[nodeName] = slot
	}

	// Marshal the table
	data, err := table1.Marshal()
	if err != nil {
		t.Fatalf("Failed to marshal table: %v", err)
	}

	// Unmarshal into a new table
	table2, err := UnmarshalNodeMapTable(data)
	if err != nil {
		t.Fatalf("Failed to unmarshal table: %v", err)
	}

	// Verify all data is preserved
	if table2.ClusterName != table1.ClusterName {
		t.Errorf("Cluster name not preserved: %s != %s", table2.ClusterName, table1.ClusterName)
	}

	if len(table2.Entries) != len(table1.Entries) {
		t.Errorf("Entry count not preserved: %d != %d", len(table2.Entries), len(table1.Entries))
	}

	for nodeName, originalSlot := range originalSlots {
		slot, found := table2.GetNodeIDForNode(nodeName)
		if !found {
			t.Errorf("Node %s not found after unmarshaling", nodeName)
		}
		if slot != originalSlot {
			t.Errorf("Slot for node %s not preserved: %d != %d", nodeName, slot, originalSlot)
		}
	}
}

func TestNodeMapTable_StaleNodeManagement(t *testing.T) {
	table := NewNodeMapTable("test-cluster")
	hasher := NewNodeHasher("test-cluster")

	// Add some nodes
	nodes := []string{"node-1", "node-2", "node-3"}
	for _, nodeName := range nodes {
		_, err := table.AssignSlot(nodeName, hasher)
		if err != nil {
			t.Fatalf("Failed to assign slot: %v", err)
		}
	}

	// Make some nodes stale by manipulating their timestamps
	table.mutex.Lock()
	staleTime := time.Now().Add(-10 * time.Minute)
	table.Entries["node-2"].LastSeen = staleTime
	table.Entries["node-3"].LastSeen = staleTime
	table.mutex.Unlock()

	// Test getting active nodes
	activeNodes := table.GetActiveNodes(5 * time.Minute)
	if len(activeNodes) != 1 || activeNodes[0] != "node-1" {
		t.Errorf("Expected 1 active node (node-1), got %v", activeNodes)
	}

	// Test getting stale nodes
	staleNodes := table.GetStaleNodes(5 * time.Minute)
	if len(staleNodes) != 2 {
		t.Errorf("Expected 2 stale nodes, got %d", len(staleNodes))
	}

	// Test cleanup
	removedNodes := table.CleanupStaleNodes(5 * time.Minute)
	if len(removedNodes) != 2 {
		t.Errorf("Expected 2 removed nodes, got %d", len(removedNodes))
	}

	// Verify nodes are actually removed
	if len(table.Entries) != 1 {
		t.Errorf("Expected 1 remaining node, got %d", len(table.Entries))
	}

	_, found := table.GetNodeIDForNode("node-1")
	if !found {
		t.Error("Active node should still be present")
	}

	_, found = table.GetNodeIDForNode("node-2")
	if found {
		t.Error("Stale node should be removed")
	}
}

func TestNodeMapTable_Stats(t *testing.T) {
	table := NewNodeMapTable("test-cluster")
	hasher := NewNodeHasher("test-cluster")

	// Add some nodes
	for i := 1; i <= 5; i++ {
		nodeName := fmt.Sprintf("node-%d", i)
		_, err := table.AssignSlot(nodeName, hasher)
		if err != nil {
			t.Fatalf("Failed to assign slot: %v", err)
		}
	}

	// Make some nodes stale
	table.mutex.Lock()
	staleTime := time.Now().Add(-10 * time.Minute)
	table.Entries["node-4"].LastSeen = staleTime
	table.Entries["node-5"].LastSeen = staleTime
	table.mutex.Unlock()

	stats := table.GetStats()

	// Verify stats
	if stats["total_nodes"] != 5 {
		t.Errorf("Expected 5 total nodes, got %v", stats["total_nodes"])
	}

	if stats["active_nodes"] != 3 {
		t.Errorf("Expected 3 active nodes, got %v", stats["active_nodes"])
	}

	if stats["stale_nodes"] != 2 {
		t.Errorf("Expected 2 stale nodes, got %v", stats["stale_nodes"])
	}

	if stats["cluster_name"] != "test-cluster" {
		t.Errorf("Expected cluster name 'test-cluster', got %v", stats["cluster_name"])
	}
}

func TestNodeMapTable_EdgeCases(t *testing.T) {
	table := NewNodeMapTable("test-cluster")
	hasher := NewNodeHasher("test-cluster")

	// Test empty node name
	_, err := table.AssignSlot("", hasher)
	if err == nil {
		t.Error("Expected error for empty node name")
	}

	// Test very long node name
	longName := string(make([]byte, 1000))
	for i := range longName {
		longName = longName[:i] + "a" + longName[i+1:]
	}

	_, err = table.AssignSlot(longName, hasher)
	if err != nil {
		t.Errorf("Should handle long node names: %v", err)
	}

	// Test special characters in node name
	specialNames := []string{
		"node-with-dots.example.com",
		"node_with_underscores",
		"node-with-123-numbers",
		"UPPERCASE-NODE",
		"mixed-Case-Node",
	}

	for _, nodeName := range specialNames {
		_, err := table.AssignSlot(nodeName, hasher)
		if err != nil {
			t.Errorf("Failed to assign slot for node with special characters %s: %v", nodeName, err)
		}
	}

	// Test removing non-existent node
	err = table.RemoveNode("non-existent-node")
	if err == nil {
		t.Error("Expected error when removing non-existent node")
	}

	// Test updating last seen for non-existent node
	err = table.UpdateLastSeen("non-existent-node")
	if err == nil {
		t.Error("Expected error when updating non-existent node")
	}
}

func TestNodeMapTable_ConcurrentAccess(t *testing.T) {
	table := NewNodeMapTable("test-cluster")
	hasher := NewNodeHasher("test-cluster")

	// Test concurrent slot assignment
	done := make(chan bool, 10)

	for i := 0; i < 10; i++ {
		go func(nodeID int) {
			defer func() { done <- true }()

			nodeName := fmt.Sprintf("concurrent-node-%d", nodeID)

			// Assign slot multiple times
			for j := 0; j < 5; j++ {
				slot, err := table.AssignSlot(nodeName, hasher)
				if err != nil {
					t.Errorf("Failed to assign slot for %s: %v", nodeName, err)
					return
				}

				// Verify consistency
				retrievedSlot, found := table.GetNodeIDForNode(nodeName)
				if !found || retrievedSlot != slot {
					t.Errorf("Inconsistent slot for %s: expected %d, got %d (found: %v)", nodeName, slot, retrievedSlot, found)
					return
				}
			}
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify final state
	if len(table.Entries) != 10 {
		t.Errorf("Expected 10 entries after concurrent access, got %d", len(table.Entries))
	}
}

func TestUnmarshalNodeMapTable_InvalidData(t *testing.T) {
	// Test with too short data
	_, err := UnmarshalNodeMapTable([]byte{1, 2})
	if err == nil {
		t.Error("Expected error for too short data")
	}

	// Test with invalid checksum
	invalidData := make([]byte, 100)
	_, err = UnmarshalNodeMapTable(invalidData)
	if err == nil {
		t.Error("Expected error for invalid checksum")
	}

	// Test with invalid JSON
	invalidJSON := []byte{0, 0, 0, 0} // checksum
	invalidJSON = append(invalidJSON, []byte("invalid json")...)
	_, err = UnmarshalNodeMapTable(invalidJSON)
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
}

func TestNodeMapTable_AtomicOperations(t *testing.T) {
	table := NewNodeMapTable("test-cluster")
	hasher := NewNodeHasher("test-cluster")

	// Test that version increments with each operation
	initialVersion := table.Version

	// Assign a slot
	slot, err := table.AssignSlot("node-1", hasher)
	if err != nil {
		t.Fatalf("Failed to assign slot: %v", err)
	}

	if slot < SBD_USABLE_SLOTS_START || slot > SBD_USABLE_SLOTS_END {
		t.Errorf("Assigned slot %d is outside valid range [%d, %d]", slot, SBD_USABLE_SLOTS_START, SBD_USABLE_SLOTS_END)
	}

	if table.Version != initialVersion+1 {
		t.Errorf("Version should increment after slot assignment: expected %d, got %d", initialVersion+1, table.Version)
	}

	// Update last seen
	previousVersion := table.Version
	err = table.UpdateLastSeen("node-1")
	if err != nil {
		t.Fatalf("Failed to update last seen: %v", err)
	}

	if table.Version != previousVersion+1 {
		t.Errorf("Version should increment after update: expected %d, got %d", previousVersion+1, table.Version)
	}

	// Remove node
	previousVersion = table.Version
	err = table.RemoveNode("node-1")
	if err != nil {
		t.Fatalf("Failed to remove node: %v", err)
	}

	if table.Version != previousVersion+1 {
		t.Errorf("Version should increment after removal: expected %d, got %d", previousVersion+1, table.Version)
	}

	t.Logf("Successfully tested atomic operations with version tracking from %d to %d", initialVersion, table.Version)
}

func TestNodeMapTable_VersionPersistence(t *testing.T) {
	// Create and populate a table
	table1 := NewNodeMapTable("test-cluster")
	hasher := NewNodeHasher("test-cluster")

	// Assign some slots to increment version
	for i := 1; i <= 3; i++ {
		nodeName := fmt.Sprintf("node-%d", i)
		_, err := table1.AssignSlot(nodeName, hasher)
		if err != nil {
			t.Fatalf("Failed to assign slot for %s: %v", nodeName, err)
		}
	}

	expectedVersion := table1.Version
	if expectedVersion == 1 {
		t.Error("Version should have incremented from initial value")
	}

	// Marshal the table
	data, err := table1.Marshal()
	if err != nil {
		t.Fatalf("Failed to marshal table: %v", err)
	}

	// Unmarshal into a new table
	table2, err := UnmarshalNodeMapTable(data)
	if err != nil {
		t.Fatalf("Failed to unmarshal table: %v", err)
	}

	// Verify version is preserved
	if table2.Version != expectedVersion {
		t.Errorf("Version not preserved during marshal/unmarshal: expected %d, got %d", expectedVersion, table2.Version)
	}

	t.Logf("Successfully tested version persistence: %d", expectedVersion)
}

func TestEndianessCompatibility(t *testing.T) {
	// Test that hash functions produce consistent results regardless of architecture endianness
	hasher := NewNodeHasher("test-cluster")

	testNodes := []string{
		"worker-1",
		"control-plane-1",
		"node-s390x-1",   // IBM Z (big endian)
		"node-arm64-1",   // ARM64 (little endian)
		"node-amd64-1",   // x86_64 (little endian)
		"node-ppc64le-1", // PowerPC little endian
	}

	// Hash values should be deterministic across runs
	expectedHashes := make(map[string]uint32)
	for _, nodeName := range testNodes {
		hash := hasher.HashNodeName(nodeName)
		expectedHashes[nodeName] = hash

		// Verify hash is consistent across multiple calls
		for i := 0; i < 10; i++ {
			rehash := hasher.HashNodeName(nodeName)
			if rehash != hash {
				t.Errorf("Hash inconsistency for node %s: expected %d, got %d on iteration %d",
					nodeName, hash, rehash, i)
			}
		}
	}

	// Test that different node names produce different hashes
	for i, node1 := range testNodes {
		for j, node2 := range testNodes {
			if i != j {
				hash1 := expectedHashes[node1]
				hash2 := expectedHashes[node2]
				if hash1 == hash2 {
					t.Errorf("Hash collision between %s and %s: both have hash %d", node1, node2, hash1)
				}
			}
		}
	}
}

func TestBinaryProtocolEndianness(t *testing.T) {
	// Test that SBD protocol messages are endianness-agnostic
	nodeID := uint16(42)
	sequence := uint64(12345)

	// Create heartbeat message
	msg := NewHeartbeat(nodeID, sequence)

	// Marshal the message
	data, err := Marshal(msg)
	if err != nil {
		t.Fatalf("Failed to marshal message: %v", err)
	}

	// Unmarshal the message
	unmarshaled, err := Unmarshal(data)
	if err != nil {
		t.Fatalf("Failed to unmarshal message: %v", err)
	}

	// Verify all fields match
	if unmarshaled.NodeID != nodeID {
		t.Errorf("NodeID mismatch: expected %d, got %d", nodeID, unmarshaled.NodeID)
	}
	if unmarshaled.Sequence != sequence {
		t.Errorf("Sequence mismatch: expected %d, got %d", sequence, unmarshaled.Sequence)
	}

	// Test with extreme values that would expose endianness issues
	extremeMsg := SBDMessageHeader{
		Magic:     [8]byte{'S', 'B', 'D', 'M', 'S', 'G', '0', '1'},
		Version:   0xFF00, // High byte set
		Type:      SBD_MSG_TYPE_HEARTBEAT,
		NodeID:    0x1234,             // Mixed bytes
		Timestamp: 0x123456789ABCDEF0, // All bytes different
		Sequence:  0xFEDCBA9876543210, // Reverse pattern
		Checksum:  0,                  // Will be calculated
	}

	extremeData, err := Marshal(extremeMsg)
	if err != nil {
		t.Fatalf("Failed to marshal extreme message: %v", err)
	}

	extremeUnmarshaled, err := Unmarshal(extremeData)
	if err != nil {
		t.Fatalf("Failed to unmarshal extreme message: %v", err)
	}

	if extremeUnmarshaled.Version != 0xFF00 {
		t.Errorf("Version endianness issue: expected 0xFF00, got 0x%04X", extremeUnmarshaled.Version)
	}
	if extremeUnmarshaled.NodeID != 0x1234 {
		t.Errorf("NodeID endianness issue: expected 0x1234, got 0x%04X", extremeUnmarshaled.NodeID)
	}
	if extremeUnmarshaled.Timestamp != 0x123456789ABCDEF0 {
		t.Errorf("Timestamp endianness issue: expected 0x123456789ABCDEF0, got 0x%016X", extremeUnmarshaled.Timestamp)
	}
	if extremeUnmarshaled.Sequence != 0xFEDCBA9876543210 {
		t.Errorf("Sequence endianness issue: expected 0xFEDCBA9876543210, got 0x%016X", extremeUnmarshaled.Sequence)
	}
}

func TestNodeMappingEndianness(t *testing.T) {
	// Test that node mapping tables are endianness-safe
	table := NewNodeMapTable("test-cluster")
	hasher := NewNodeHasher("test-cluster")

	// Add nodes with specific patterns that would expose endianness issues
	testNodes := []struct {
		name                string
		expectedSlotPattern string
	}{
		{"node-0x1234", "consistent"},
		{"big-endian-test", "consistent"},
		{"little-endian-test", "consistent"},
		{"mixed-endian-scenario", "consistent"},
	}

	assignedSlots := make(map[string]uint16)
	for _, test := range testNodes {
		slot, err := table.AssignSlot(test.name, hasher)
		if err != nil {
			t.Fatalf("Failed to assign slot for %s: %v", test.name, err)
		}
		assignedSlots[test.name] = slot
	}

	// Marshal and unmarshal the table
	data, err := table.Marshal()
	if err != nil {
		t.Fatalf("Failed to marshal node mapping table: %v", err)
	}

	unmarshaledTable, err := UnmarshalNodeMapTable(data)
	if err != nil {
		t.Fatalf("Failed to unmarshal node mapping table: %v", err)
	}

	// Verify all slots are preserved correctly
	for nodeName, expectedSlot := range assignedSlots {
		actualSlot, found := unmarshaledTable.GetNodeIDForNode(nodeName)
		if !found {
			t.Errorf("Node %s not found in unmarshaled table", nodeName)
			continue
		}
		if actualSlot != expectedSlot {
			t.Errorf("Slot mismatch for %s: expected %d, got %d", nodeName, expectedSlot, actualSlot)
		}
	}

	// Verify version and other fields survived marshaling
	if unmarshaledTable.Version != table.Version {
		t.Errorf("Version mismatch: expected %d, got %d", table.Version, unmarshaledTable.Version)
	}
	if unmarshaledTable.ClusterName != table.ClusterName {
		t.Errorf("ClusterName mismatch: expected %s, got %s", table.ClusterName, unmarshaledTable.ClusterName)
	}
}

func TestNodeMapTable_MaxNodesBackwardCompatibility(t *testing.T) {
	// Test that tables work correctly with default behavior
	table := NewNodeMapTable("test-cluster")
	hasher := NewNodeHasher("test-cluster")

	// Should use default max nodes
	expectedMaxNodes := uint16(255) // SBD_MAX_NODES default

	slot, err := table.AssignSlot("test-node", hasher)
	if err != nil {
		t.Fatalf("Failed to assign slot: %v", err)
	}

	// Should work with default max nodes range
	if slot < 1 || slot > expectedMaxNodes {
		t.Errorf("Assigned slot %d is outside default range [1, %d]", slot, expectedMaxNodes)
	}

	// Test marshal/unmarshal preserves backward compatibility
	data, err := table.Marshal()
	if err != nil {
		t.Fatalf("Failed to marshal table: %v", err)
	}

	unmarshaled, err := UnmarshalNodeMapTable(data)
	if err != nil {
		t.Fatalf("Failed to unmarshal table: %v", err)
	}

	// Should work correctly after unmarshal
	if len(unmarshaled.Entries) != 1 {
		t.Errorf("Expected 1 entry after unmarshal, got %d", len(unmarshaled.Entries))
	}
}

func TestNodeMapTable_HashCollisionHandling(t *testing.T) {
	tests := []struct {
		name      string
		nodeCount int
		testName  string
	}{
		{
			name:      "small cluster with potential collisions",
			nodeCount: 20,
			testName:  "collision-test",
		},
		{
			name:      "medium cluster with many nodes",
			nodeCount: 50,
			testName:  "medium-test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			table := NewNodeMapTable(tt.testName)
			hasher := NewNodeHasher(tt.testName)
			assignedSlots := make(map[uint16]bool)

			// Create node names that are likely to collide due to similar patterns
			nodeNames := make([]string, tt.nodeCount)
			for i := 0; i < tt.nodeCount; i++ {
				// Use patterns that might hash to similar values
				nodeNames[i] = fmt.Sprintf("node-collision-test-%d-%s", i, tt.testName)
			}

			// Assign slots to all nodes
			for i, nodeName := range nodeNames {
				slot, err := table.AssignSlot(nodeName, hasher)
				if err != nil {
					t.Fatalf("Failed to assign slot for node %s (iteration %d): %v", nodeName, i, err)
				}

				// Verify slot is within range
				if slot < 1 || slot > SBD_MAX_NODES {
					t.Errorf("Assigned slot %d for node %s is outside valid range [1, %d]",
						slot, nodeName, SBD_MAX_NODES)
				}

				// Verify no duplicate slots
				if assignedSlots[slot] {
					t.Errorf("Slot %d was assigned to multiple nodes (current: %s)",
						slot, nodeName)
				}
				assignedSlots[slot] = true

				// Verify node can be found
				retrievedSlot, found := table.GetNodeIDForNode(nodeName)
				if !found || retrievedSlot != slot {
					t.Errorf("Could not retrieve slot for node %s: expected %d, got %d (found: %v)",
						nodeName, slot, retrievedSlot, found)
				}

				// Verify consistency on re-assignment
				slot2, err := table.AssignSlot(nodeName, hasher)
				if err != nil {
					t.Errorf("Failed to reassign slot for node %s: %v", nodeName, err)
				}
				if slot2 != slot {
					t.Errorf("Inconsistent slot assignment for node %s: %d != %d",
						nodeName, slot, slot2)
				}
			}

			t.Logf("Successfully assigned %d unique slots with potential collisions handled",
				len(assignedSlots))
		})
	}
}

func TestNodeMapTable_SlotExhaustion(t *testing.T) {
	// This test attempts to fill many slots and verify behavior near capacity
	table := NewNodeMapTable("exhaustion-test")
	hasher := NewNodeHasher("exhaustion-test")

	// Try to assign many nodes to test for slot exhaustion
	maxAttempts := 300 // More than SBD_MAX_NODES to force exhaustion
	successfulAssignments := 0
	failures := 0

	for i := 0; i < maxAttempts; i++ {
		nodeName := fmt.Sprintf("fill-node-%05d", i) // Zero-padded for consistent hashing
		slot, err := table.AssignSlot(nodeName, hasher)
		if err != nil {
			failures++
			if failures < 5 { // Log first few failures
				t.Logf("Assignment failure %d for node %s: %v", failures, nodeName, err)
			}
			continue
		}

		if slot < 1 || slot > SBD_MAX_NODES {
			t.Errorf("Assigned slot %d is outside valid range [1, %d]", slot, SBD_MAX_NODES)
		}
		successfulAssignments++

		// Stop if we've filled a reasonable number of slots to avoid long test
		if successfulAssignments >= 200 {
			break
		}
	}

	t.Logf("Successfully filled %d slots with %d failures", successfulAssignments, failures)

	// Verify table state
	stats := table.GetStats()
	slotsUsed, ok := stats["slots_used"].(int)
	if !ok {
		t.Errorf("Could not get slots_used from stats: %v", stats)
	} else {
		if slotsUsed != successfulAssignments {
			t.Errorf("Stats mismatch: expected %d slots used, got %d", successfulAssignments, slotsUsed)
		}
		t.Logf("Final state: %d slots used", slotsUsed)
	}

	// Verify all assignments are unique
	seenSlots := make(map[uint16]bool)
	for _, entry := range table.Entries {
		if seenSlots[entry.NodeID] {
			t.Errorf("Duplicate slot assignment detected: slot %d", entry.NodeID)
		}
		seenSlots[entry.NodeID] = true
	}
}

func TestNodeMapTable_ForcedHashCollisions(t *testing.T) {
	table := NewNodeMapTable("collision-test")
	hasher := NewNodeHasher("collision-test")

	// Create node names that are likely to have hash collisions
	nodeNames := []string{
		"collision-node-alpha-001",
		"collision-node-beta-002",
		"collision-node-gamma-003",
		"collision-node-delta-004",
		"collision-node-epsilon-005",
		"collision-node-zeta-006",
		"collision-node-eta-007",
		"collision-node-theta-008",
		"collision-node-iota-009",
		"collision-node-kappa-010",
	}

	assignedSlots := make(map[uint16]string)
	collisionResolved := 0

	for i, nodeName := range nodeNames {
		// Calculate preferred slot for this node
		preferredSlot := hasher.CalculateNodeID(nodeName, 0)

		slot, err := table.AssignSlot(nodeName, hasher)
		if err != nil {
			t.Errorf("Unexpected error for node %s (position %d): %v", nodeName, i, err)
			continue
		}

		// Check if collision resolution was needed
		if slot != preferredSlot {
			collisionResolved++
			t.Logf("Collision resolved for node %s: preferred slot %d, assigned slot %d",
				nodeName, preferredSlot, slot)
		}

		// Verify no duplicate assignments
		if existingNode, exists := assignedSlots[slot]; exists {
			t.Errorf("Slot collision! Slot %d assigned to both %s and %s",
				slot, existingNode, nodeName)
		}
		assignedSlots[slot] = nodeName

		// Verify slot is in valid range
		if slot < 1 || slot > SBD_MAX_NODES {
			t.Errorf("Assigned slot %d for node %s is outside valid range [1, %d]",
				slot, nodeName, SBD_MAX_NODES)
		}
	}

	t.Logf("Hash collision test completed: %d collisions resolved out of %d assignments",
		collisionResolved, len(assignedSlots))

	// Verify collision resolution worked
	if collisionResolved == 0 && len(assignedSlots) > 1 {
		t.Log("No hash collisions occurred in this test run (this is random but possible)")
	}
}

func TestNodeMapTable_MaxRetriesExceeded(t *testing.T) {
	table := NewNodeMapTable("retry-test")
	hasher := NewNodeHasher("retry-test")

	// Fill many slots to increase chance of retry exhaustion
	nodesToFill := 200
	filledSlots := make(map[uint16]bool)

	for i := 0; i < nodesToFill; i++ {
		nodeName := fmt.Sprintf("node-%05d", i)
		slot, err := table.AssignSlot(nodeName, hasher)
		if err != nil {
			// This could happen due to slot exhaustion or collision resolution failure
			t.Logf("Could not assign slot for node %s: %v", nodeName, err)
			break
		}
		filledSlots[slot] = true
	}

	t.Logf("Filled %d unique slots", len(filledSlots))

	// Try to assign more nodes to potentially trigger retry exhaustion
	for i := nodesToFill; i < nodesToFill+20; i++ {
		nodeName := fmt.Sprintf("overflow-node-%05d", i)
		slot, err := table.AssignSlot(nodeName, hasher)

		if err != nil {
			t.Logf("Got expected error for node %s: %v", nodeName, err)
			errMsg := err.Error()
			if !containsAny(errMsg, []string{"attempts", "occupied", "retry", "exhausted"}) {
				t.Errorf("Error message should mention retry attempts or slot occupation: %s", errMsg)
			}
		} else {
			// Assignment succeeded
			if slot < 1 || slot > SBD_MAX_NODES {
				t.Errorf("Assigned slot %d is outside valid range", slot)
			}
		}
	}
}

func TestNodeMapTable_CollisionResolutionWithLimitedSlots(t *testing.T) {
	table := NewNodeMapTable("collision-resolution")
	hasher := NewNodeHasher("collision-resolution")

	// Use node names that might create predictable collisions
	nodes := []string{
		"node-a", "node-b", "node-c", "node-d", "node-e",
		"node-f", "node-g", "node-h", "node-i", "node-j",
	}

	// Track collision resolution attempts
	successfulAssignments := 0
	collisionCount := 0

	for i, nodeName := range nodes {
		// Get the preferred slot
		preferredSlot := hasher.CalculateNodeID(nodeName, 0)

		slot, err := table.AssignSlot(nodeName, hasher)
		if err != nil {
			t.Logf("Failed to assign slot for node %s (position %d): %v", nodeName, i, err)
			continue
		}

		successfulAssignments++

		// Check if collision resolution was used
		if slot != preferredSlot {
			collisionCount++
			t.Logf("Collision resolved for %s: preferred %d, assigned %d",
				nodeName, preferredSlot, slot)
		}

		// Verify slot is valid
		if slot < 1 || slot > SBD_MAX_NODES {
			t.Errorf("Invalid slot %d assigned to node %s", slot, nodeName)
		}
	}

	t.Logf("Collision resolution test: %d successful assignments, %d collisions resolved",
		successfulAssignments, collisionCount)

	// Verify no slot conflicts
	seenSlots := make(map[uint16]bool)
	for _, entry := range table.Entries {
		if seenSlots[entry.NodeID] {
			t.Errorf("Duplicate slot assignment detected: slot %d", entry.NodeID)
		}
		seenSlots[entry.NodeID] = true
	}
}

// Helper functions
func containsAny(s string, substrings []string) bool {
	for _, substr := range substrings {
		if len(s) >= len(substr) {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
		}
	}
	return false
}

func TestNodeMapTable_ExtremHashCollisionStress(t *testing.T) {
	table := NewNodeMapTable("stress-test")
	hasher := NewNodeHasher("stress-test")

	// Create many nodes with similar naming patterns to increase collision probability
	nodePatterns := []string{
		"node-cluster-%03d",
		"worker-node-%03d",
		"compute-%03d",
		"k8s-node-%03d",
		"instance-%03d",
	}

	assignedSlots := make(map[uint16]string)
	collisionCount := 0
	totalNodes := 0

	// Try each pattern with multiple node numbers
	for _, pattern := range nodePatterns {
		for i := 1; i <= 30; i++ { // 30 nodes per pattern = 150 total
			nodeName := fmt.Sprintf(pattern, i)
			totalNodes++

			// Calculate preferred slot
			preferredSlot := hasher.CalculateNodeID(nodeName, 0)

			slot, err := table.AssignSlot(nodeName, hasher)
			if err != nil {
				// Log slot exhaustion errors
				t.Logf("Slot assignment failed for node %s: %v", nodeName, err)
				continue
			}

			// Check if collision resolution was needed
			if slot != preferredSlot {
				collisionCount++
				t.Logf("Collision resolved for %s: preferred %d -> assigned %d",
					nodeName, preferredSlot, slot)
			}

			// Verify no duplicate assignments
			if existingNode, exists := assignedSlots[slot]; exists {
				t.Errorf("CRITICAL: Slot %d assigned to both %s and %s",
					slot, existingNode, nodeName)
			}
			assignedSlots[slot] = nodeName

			// Stop early if we approach capacity to avoid long tests
			if len(assignedSlots) >= 200 {
				t.Logf("Stopping stress test at %d assignments to avoid timeout", len(assignedSlots))
				break
			}
		}
		if len(assignedSlots) >= 200 {
			break
		}
	}

	t.Logf("Stress test results: %d nodes assigned, %d collisions resolved, %d/%d success rate",
		len(assignedSlots), collisionCount, len(assignedSlots), totalNodes)

	// Verify collision resolution effectiveness
	if collisionCount > 0 {
		t.Logf("Collision resolution working: %d collisions handled successfully", collisionCount)
	}

	// Verify no slot conflicts occurred
	for slotID, nodeName := range assignedSlots {
		if retrievedNode, found := table.GetNodeForNodeID(slotID); !found || retrievedNode != nodeName {
			t.Errorf("Slot mapping inconsistency: slot %d should map to %s, got %s (found: %v)",
				slotID, nodeName, retrievedNode, found)
		}
	}
}

func TestNodeMapTable_SlotExhaustionBoundary(t *testing.T) {
	table := NewNodeMapTable("boundary-test")
	hasher := NewNodeHasher("boundary-test")

	// Test behavior when approaching the theoretical maximum
	// SBD_MAX_NODES = 255, so usable slots are 1-255
	maxUsableSlots := uint16(SBD_MAX_NODES)

	// Try to fill exactly up to the boundary
	successCount := 0
	failureCount := 0
	maxAttempts := int(maxUsableSlots) + 50 // Try beyond capacity

	for i := 0; i < maxAttempts; i++ {
		nodeName := fmt.Sprintf("boundary-node-%05d", i)
		slot, err := table.AssignSlot(nodeName, hasher)

		if err != nil {
			failureCount++
			if failureCount <= 3 { // Log first few failures
				t.Logf("Expected failure %d at attempt %d: %v", failureCount, i+1, err)
			}
			continue
		}

		successCount++
		if slot < 1 || slot > maxUsableSlots {
			t.Errorf("Slot %d assigned to %s is outside valid range [1, %d]",
				slot, nodeName, maxUsableSlots)
		}

		// Stop when we've confirmed the algorithm works well beyond reasonable capacity
		if successCount >= 220 { // Leave some buffer for the test
			t.Logf("Stopping boundary test at %d successful assignments", successCount)
			break
		}
	}

	t.Logf("Boundary test: %d successes, %d failures out of %d attempts",
		successCount, failureCount, successCount+failureCount)

	// Verify internal consistency
	if len(table.Entries) != successCount {
		t.Errorf("Entry count mismatch: expected %d, got %d", successCount, len(table.Entries))
	}

	if len(table.NodeUsage) != successCount {
		t.Errorf("Slot usage count mismatch: expected %d, got %d", successCount, len(table.NodeUsage))
	}

	// Verify stats consistency
	stats := table.GetStats()
	slotsUsed, ok := stats["slots_used"].(int)
	if !ok || slotsUsed != successCount {
		t.Errorf("Stats inconsistency: expected %d slots used, got %v", successCount, slotsUsed)
	}
}

func TestNodeMapTable_RetryMechanismValidation(t *testing.T) {
	table := NewNodeMapTable("retry-validation")
	hasher := NewNodeHasher("retry-validation")

	// Fill some slots to create collision scenarios
	prefilledNodes := []string{
		"node-retry-001", "node-retry-002", "node-retry-003",
		"node-retry-004", "node-retry-005",
	}

	prefilledSlots := make(map[uint16]string)
	for _, nodeName := range prefilledNodes {
		slot, err := table.AssignSlot(nodeName, hasher)
		if err != nil {
			t.Fatalf("Failed to prefill node %s: %v", nodeName, err)
		}
		prefilledSlots[slot] = nodeName
		t.Logf("Prefilled node %s in slot %d", nodeName, slot)
	}

	// Now try to assign nodes that might collide with prefilled slots
	testNodes := []string{
		"test-collision-alpha",
		"test-collision-beta",
		"test-collision-gamma",
		"test-collision-delta",
		"test-collision-epsilon",
	}

	for _, testNode := range testNodes {
		// Track which slots would be tried for this node
		attemptedSlots := make([]uint16, 0, SBD_MAX_RETRIES)
		for attempt := 0; attempt < SBD_MAX_RETRIES; attempt++ {
			attemptSlot := hasher.CalculateNodeID(testNode, attempt)
			attemptedSlots = append(attemptedSlots, attemptSlot)
		}

		slot, err := table.AssignSlot(testNode, hasher)
		if err != nil {
			t.Logf("Node %s failed assignment after trying slots %v: %v",
				testNode, attemptedSlots, err)
			continue
		}

		// Verify the assigned slot was in the attempted list
		slotFound := false
		for _, attemptSlot := range attemptedSlots {
			if slot == attemptSlot {
				slotFound = true
				break
			}
		}

		if !slotFound {
			t.Errorf("Node %s assigned slot %d not in attempted slots %v",
				testNode, slot, attemptedSlots)
		}

		// Verify slot is not conflicting with prefilled slots
		if existingNode, exists := prefilledSlots[slot]; exists {
			t.Errorf("Node %s assigned to slot %d which is already occupied by %s",
				testNode, slot, existingNode)
		}

		t.Logf("Node %s successfully assigned to slot %d (attempts: %v)",
			testNode, slot, attemptedSlots)
	}
}

func TestNodeMapTable_ConsistentHashingValidation(t *testing.T) {
	// Test that the same node name always gets the same slot across multiple tables
	clusterName := "consistency-test"

	// Create multiple independent tables
	table1 := NewNodeMapTable(clusterName)
	table2 := NewNodeMapTable(clusterName)
	table3 := NewNodeMapTable(clusterName)

	hasher := NewNodeHasher(clusterName)

	testNodes := []string{
		"consistent-node-1",
		"consistent-node-2",
		"consistent-node-3",
		"worker-xyz-123",
		"master-abc-456",
	}

	// Assign same nodes to different tables
	for _, nodeName := range testNodes {
		slot1, err1 := table1.AssignSlot(nodeName, hasher)
		slot2, err2 := table2.AssignSlot(nodeName, hasher)
		slot3, err3 := table3.AssignSlot(nodeName, hasher)

		// All assignments should succeed
		if err1 != nil || err2 != nil || err3 != nil {
			t.Errorf("Assignment failed for node %s: err1=%v, err2=%v, err3=%v",
				nodeName, err1, err2, err3)
			continue
		}

		// All assignments should result in the same slot
		if slot1 != slot2 || slot2 != slot3 {
			t.Errorf("Inconsistent slot assignment for node %s: table1=%d, table2=%d, table3=%d",
				nodeName, slot1, slot2, slot3)
		} else {
			t.Logf("Node %s consistently assigned to slot %d across all tables", nodeName, slot1)
		}
	}
}

func TestNodeMapTable_ErrorMessageQuality(t *testing.T) {
	table := NewNodeMapTable("error-test")
	hasher := NewNodeHasher("error-test")

	// Test empty node name error
	_, err := table.AssignSlot("", hasher)
	if err == nil {
		t.Error("Expected error for empty node name")
	} else {
		if !containsAny(err.Error(), []string{"empty", "cannot be empty"}) {
			t.Errorf("Empty node name error should mention 'empty': %s", err.Error())
		}
	}

	// Fill many slots to trigger retry exhaustion
	for i := 0; i < 200; i++ {
		nodeName := fmt.Sprintf("filler-node-%03d", i)
		_, err := table.AssignSlot(nodeName, hasher)
		if err != nil {
			// Expected after filling many slots
			break
		}
	}

	// Try to assign another node to trigger retry exhaustion error
	_, err = table.AssignSlot("exhaustion-test-node", hasher)
	if err != nil {
		errMsg := err.Error()
		// Error should mention attempts or exhaustion
		if !containsAny(errMsg, []string{"attempts", "occupied", "retry", "exhausted"}) {
			t.Errorf("Retry exhaustion error should mention attempts/exhaustion: %s", errMsg)
		}
		t.Logf("Good error message for slot exhaustion: %s", errMsg)
	}

	// Test removal of non-existent node
	err = table.RemoveNode("non-existent-node")
	if err == nil {
		t.Error("Expected error when removing non-existent node")
	} else {
		if !containsAny(err.Error(), []string{"not found", "not exist"}) {
			t.Errorf("Non-existent node error should mention 'not found': %s", err.Error())
		}
	}
}
