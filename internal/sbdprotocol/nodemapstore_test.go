/*
Copyright 2026.

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
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

func TestFileNodeMapStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodemap")
	store := NewFileNodeMapStore(path)

	// Create a table, marshal it, save, load, and verify
	table := NewNodeMapTable("test-cluster")
	hasher := NewNodeHasher("test-cluster")
	if _, err := table.AssignSlot("node-1", hasher); err != nil {
		t.Fatalf("AssignSlot failed: %v", err)
	}

	data, err := table.Marshal()
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	if err := store.Save(data); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if !bytes.Equal(data, loaded) {
		t.Error("loaded data does not match saved data")
	}

	// Verify the loaded data can be unmarshaled
	loadedTable, err := UnmarshalNodeMapTable(loaded)
	if err != nil {
		t.Fatalf("UnmarshalNodeMapTable failed: %v", err)
	}
	if loadedTable.ClusterName != "test-cluster" {
		t.Errorf("cluster name mismatch: expected test-cluster, got %s", loadedTable.ClusterName)
	}
	if _, found := loadedTable.GetNodeIDForNode("node-1"); !found {
		t.Error("node-1 not found in loaded table")
	}
}

func TestFileNodeMapStore_LoadNonExistent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist")
	store := NewFileNodeMapStore(path)

	_, err := store.Load()
	if err == nil {
		t.Fatal("expected error loading non-existent file")
	}
	if !os.IsNotExist(err) {
		t.Errorf("expected os.ErrNotExist, got: %v", err)
	}
}

func TestFileNodeMapStore_SaveCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "deep", "nodemap")
	store := NewFileNodeMapStore(path)

	data := []byte("test data")
	if err := store.Save(data); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load after save failed: %v", err)
	}
	if !bytes.Equal(data, loaded) {
		t.Error("loaded data does not match saved data")
	}
}

func TestFileNodeMapStore_SaveOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodemap")
	store := NewFileNodeMapStore(path)

	// Save first version
	data1 := []byte("version 1")
	if err := store.Save(data1); err != nil {
		t.Fatalf("first Save failed: %v", err)
	}

	// Save second version
	data2 := []byte("version 2")
	if err := store.Save(data2); err != nil {
		t.Fatalf("second Save failed: %v", err)
	}

	// Load should return second version
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !bytes.Equal(data2, loaded) {
		t.Errorf("expected %q, got %q", data2, loaded)
	}
}

func TestFileNodeMapStore_AtomicRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodemap")
	store := NewFileNodeMapStore(path)

	data := []byte("atomic test data")
	if err := store.Save(data); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify no .tmp file remains after save
	tmpPath := path + ".tmp"
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Error("temporary file should not exist after successful save")
	}

	// Verify the final file exists with correct content
	loaded, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if !bytes.Equal(data, loaded) {
		t.Error("file content mismatch")
	}
}

func TestFileNodeMapStore_ImplementsInterface(t *testing.T) {
	// Compile-time check that FileNodeMapStore implements NodeMapStore
	var _ NodeMapStore = (*FileNodeMapStore)(nil)
}

func TestNodeManager_WithCustomStore(t *testing.T) {
	// Verify that NodeManager uses an injected store
	dir := t.TempDir()
	storePath := filepath.Join(dir, "custom-store")
	store := NewFileNodeMapStore(storePath)

	device := NewMockSBDDevice("/dev/test", SBD_SLOT_SIZE*10)
	config := NodeManagerConfig{
		ClusterName:        "custom-store-cluster",
		SyncInterval:       time.Minute,
		StaleNodeTimeout:   10 * time.Minute,
		Logger:             logr.Discard(),
		FileLockingEnabled: false,
		NodeMapStore:       store,
	}

	nm, err := NewNodeManager(device, config)
	if err != nil {
		t.Fatalf("NewNodeManager failed: %v", err)
	}
	defer func() { _ = nm.Close() }()

	// Assign a node and sync
	slot, err := nm.GetNodeIDForNode("test-node")
	if err != nil {
		t.Fatalf("GetNodeIDForNode failed: %v", err)
	}

	if err := nm.Sync(); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	// Verify data was written to the custom store path (not the default device-derived path)
	data, err := store.Load()
	if err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}

	table, err := UnmarshalNodeMapTable(data)
	if err != nil {
		t.Fatalf("UnmarshalNodeMapTable failed: %v", err)
	}

	loadedSlot, found := table.GetNodeIDForNode("test-node")
	if !found {
		t.Fatal("test-node not found in stored data")
	}
	if loadedSlot != slot {
		t.Errorf("slot mismatch: expected %d, got %d", slot, loadedSlot)
	}
}

func TestNodeManager_DefaultStoreUsesDevicePath(t *testing.T) {
	// Verify that NewNodeManager with nil NodeMapStore creates a FileNodeMapStore
	// using the device-derived path
	devicePath := filepath.Join(t.TempDir(), "sbr-device")
	device := NewMockSBDDevice(devicePath, SBD_SLOT_SIZE*10)

	config := NodeManagerConfig{
		ClusterName:        "default-store-cluster",
		SyncInterval:       time.Second,
		StaleNodeTimeout:   time.Minute,
		Logger:             logr.Discard(),
		FileLockingEnabled: false,
		// NodeMapStore intentionally nil — should default to FileNodeMapStore
	}

	nm, err := NewNodeManager(device, config)
	if err != nil {
		t.Fatalf("NewNodeManager failed: %v", err)
	}
	defer func() { _ = nm.Close() }()

	// Assign a node and sync
	if _, err := nm.GetNodeIDForNode("default-node"); err != nil {
		t.Fatalf("GetNodeIDForNode failed: %v", err)
	}
	if err := nm.Sync(); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	// Verify data was written to the expected device-derived path
	expectedPath := fmt.Sprintf("%s%s", devicePath, SBD_NODE_MAP_FILE_SUFFIX)
	data, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("expected node map file at %s, got error: %v", expectedPath, err)
	}

	table, err := UnmarshalNodeMapTable(data)
	if err != nil {
		t.Fatalf("UnmarshalNodeMapTable failed: %v", err)
	}
	if _, found := table.GetNodeIDForNode("default-node"); !found {
		t.Error("default-node not found in stored data at device-derived path")
	}
}

func TestNodeManager_ClearCorruptedSlotSavesViaStore(t *testing.T) {
	// Verify that clearCorruptedSlot writes a clean table via the store
	// instead of deleting the file
	dir := t.TempDir()
	storePath := filepath.Join(dir, "nodemap")
	store := NewFileNodeMapStore(storePath)

	device := NewMockSBDDevice("", SBD_SLOT_SIZE*10)
	config := NodeManagerConfig{
		ClusterName:        "corruption-test",
		SyncInterval:       time.Second,
		StaleNodeTimeout:   time.Minute,
		Logger:             logr.Discard(),
		FileLockingEnabled: false,
		NodeMapStore:       store,
	}

	nm, err := NewNodeManager(device, config)
	if err != nil {
		t.Fatalf("NewNodeManager failed: %v", err)
	}
	defer func() { _ = nm.Close() }()

	// Write some corrupt data directly to the store
	if err := store.Save([]byte("corrupt garbage data")); err != nil {
		t.Fatalf("failed to write corrupt data: %v", err)
	}

	// clearCorruptedSlot is unexported, so trigger it via ReloadFromDevice
	// which calls loadFromDeviceWithRecovery → attemptTableRecovery → clearCorruptedSlot
	if err := nm.ReloadFromDevice(); err != nil {
		t.Fatalf("ReloadFromDevice failed: %v", err)
	}

	// Verify the store now has a valid clean table (not deleted)
	data, err := store.Load()
	if err != nil {
		t.Fatalf("store.Load failed after recovery — file should exist, got: %v", err)
	}

	table, err := UnmarshalNodeMapTable(data)
	if err != nil {
		t.Fatalf("stored data is not a valid table after recovery: %v", err)
	}
	if table.ClusterName != "corruption-test" {
		t.Errorf("recovered table has wrong cluster: expected corruption-test, got %s", table.ClusterName)
	}
	if len(table.Entries) != 0 {
		t.Errorf("recovered table should be empty, has %d entries", len(table.Entries))
	}
}

// mockFailStore is a NodeMapStore that can be configured to fail.
type mockFailStore struct {
	data     []byte
	failLoad bool
	failSave bool
}

func (m *mockFailStore) Load() ([]byte, error) {
	if m.failLoad {
		return nil, errors.New("mock store load failure")
	}
	if m.data == nil {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), m.data...), nil
}

func (m *mockFailStore) Save(data []byte) error {
	if m.failSave {
		return errors.New("mock store save failure")
	}
	m.data = make([]byte, len(data))
	copy(m.data, data)
	return nil
}

func TestNodeManager_StoreErrorPropagation(t *testing.T) {
	store := &mockFailStore{}
	device := NewMockSBDDevice("", SBD_SLOT_SIZE*10)

	config := NodeManagerConfig{
		ClusterName:        "error-test",
		SyncInterval:       time.Second,
		StaleNodeTimeout:   time.Minute,
		Logger:             logr.Discard(),
		FileLockingEnabled: false,
		NodeMapStore:       store,
	}

	// Initial creation succeeds (load fails → new table created)
	nm, err := NewNodeManager(device, config)
	if err != nil {
		t.Fatalf("NewNodeManager failed: %v", err)
	}
	defer func() { _ = nm.Close() }()

	// Assign a node — this saves internally via atomicSyncToDevice
	if _, err := nm.GetNodeIDForNode("test-node"); err != nil {
		t.Fatalf("GetNodeIDForNode failed: %v", err)
	}

	// Make a local change so dirty=true, then test that Save failure propagates
	store.failSave = true
	nm.mutex.Lock()
	_ = nm.table.UpdateLastSeen("test-node")
	nm.dirty = true
	nm.mutex.Unlock()

	if err := nm.Sync(); err == nil {
		t.Error("expected Sync to fail when store.Save fails")
	}
	store.failSave = false

	// Successful sync
	if err := nm.Sync(); err != nil {
		t.Fatalf("Sync should succeed after clearing failSave: %v", err)
	}

	// Load failure should propagate through ReloadFromDevice
	// (recovery will also fail, resulting in a new clean table being saved)
	store.failLoad = true
	store.failSave = true
	err = nm.ReloadFromDevice()
	// Both load and save fail — recovery can't save clean table
	if err == nil {
		t.Error("expected ReloadFromDevice to fail when both load and save fail")
	}
}

func TestNodeManager_CorruptStoreDataRecovery(t *testing.T) {
	// Test that recovery works through a mock store returning corrupt bytes,
	// verifying the recovery path is backend-agnostic (not filesystem-specific).
	store := &mockFailStore{
		data: []byte("garbage corrupt data that is not valid marshal output"),
	}
	device := NewMockSBDDevice("", SBD_SLOT_SIZE*10)

	config := NodeManagerConfig{
		ClusterName:        "corrupt-mock-test",
		SyncInterval:       time.Second,
		StaleNodeTimeout:   time.Minute,
		Logger:             logr.Discard(),
		FileLockingEnabled: false,
		NodeMapStore:       store,
	}

	// NewNodeManager should recover from corrupt store data by creating a new table
	nm, err := NewNodeManager(device, config)
	if err != nil {
		t.Fatalf("NewNodeManager failed: %v", err)
	}
	defer func() { _ = nm.Close() }()

	// Assign a node — proves the manager is functional after recovery
	slot, err := nm.GetNodeIDForNode("recovery-node")
	if err != nil {
		t.Fatalf("GetNodeIDForNode failed after recovery: %v", err)
	}
	if slot == 0 {
		t.Error("expected valid slot assignment after recovery")
	}

	// Now inject corrupt data into the store and reload
	store.data = []byte("more garbage after initial recovery")

	if err := nm.ReloadFromDevice(); err != nil {
		t.Fatalf("ReloadFromDevice failed: %v", err)
	}

	// After recovery, store should contain a valid clean table
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("store.Load failed after reload recovery: %v", err)
	}

	table, err := UnmarshalNodeMapTable(loaded)
	if err != nil {
		t.Fatalf("stored data invalid after reload recovery: %v", err)
	}
	if table.ClusterName != "corrupt-mock-test" {
		t.Errorf("wrong cluster name after recovery: expected corrupt-mock-test, got %s", table.ClusterName)
	}
}
