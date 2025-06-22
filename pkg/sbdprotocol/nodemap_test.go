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
	slot1 := hasher.CalculateSlotID("node-1", 0)
	slot2 := hasher.CalculateSlotID("node-1", 0)
	if slot1 != slot2 {
		t.Errorf("Slot calculation should be consistent: %d != %d", slot1, slot2)
	}

	// Test slot is in valid range
	if slot1 < SBD_USABLE_SLOTS_START || slot1 > SBD_USABLE_SLOTS_END {
		t.Errorf("Slot %d is outside valid range [%d, %d]", slot1, SBD_USABLE_SLOTS_START, SBD_USABLE_SLOTS_END)
	}

	// Test collision resolution produces different slots
	slot3 := hasher.CalculateSlotID("node-1", 1)
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
	retrievedSlot, found := table.GetSlotForNode("node-1")
	if !found {
		t.Error("Node should be found in table")
	}
	if retrievedSlot != slot {
		t.Errorf("Retrieved slot %d doesn't match assigned slot %d", retrievedSlot, slot)
	}

	// Test retrieving the node by slot
	nodeName, found := table.GetNodeForSlot(slot)
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
		retrievedSlot, found := table.GetSlotForNode(nodeName)
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
		slot, found := table2.GetSlotForNode(nodeName)
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

	_, found := table.GetSlotForNode("node-1")
	if !found {
		t.Error("Active node should still be present")
	}

	_, found = table.GetSlotForNode("node-2")
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
				retrievedSlot, found := table.GetSlotForNode(nodeName)
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
