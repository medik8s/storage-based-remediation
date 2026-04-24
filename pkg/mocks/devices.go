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

package mocks

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/medik8s/storage-based-remediation/pkg/sbdprotocol"
)

// BlockDevice defines the interface for block device operations
type BlockDeviceInterface interface {
	io.ReaderAt
	io.WriterAt
	Sync() error
	Close() error
	Path() string
	IsClosed() bool
}

// WatchdogInterface defines the interface for watchdog operations
// This is duplicated here to avoid import cycles with specific implementations
type WatchdogInterface interface {
	Pet() error
	Close() error
	Path() string
	Timeout() time.Duration
}

// MockBlockDevice is a mock implementation of BlockDevice for testing
type MockBlockDevice struct {
	data      []byte
	path      string
	closed    bool
	failRead  bool
	failWrite bool
	failSync  bool
	mutex     sync.RWMutex
}

// NewMockBlockDevice creates a new mock block device with the specified size
func NewMockBlockDevice(path string, size int) *MockBlockDevice {
	return &MockBlockDevice{
		data: make([]byte, size),
		path: path,
	}
}

func (m *MockBlockDevice) ReadAt(p []byte, off int64) (int, error) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	if m.closed {
		return 0, errors.New("device is closed")
	}

	if m.failRead {
		return 0, errors.New("mock read failure")
	}

	if off < 0 || off >= int64(len(m.data)) {
		return 0, errors.New("offset out of range")
	}

	n := copy(p, m.data[off:])
	return n, nil
}

func (m *MockBlockDevice) WriteAt(p []byte, off int64) (int, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.closed {
		return 0, errors.New("device is closed")
	}

	if m.failWrite {
		return 0, errors.New("mock write failure")
	}

	if off < 0 || off >= int64(len(m.data)) {
		return 0, errors.New("offset out of range")
	}

	// Ensure we don't write beyond the buffer
	end := off + int64(len(p))
	if end > int64(len(m.data)) {
		end = int64(len(m.data))
	}

	n := copy(m.data[off:end], p)
	return n, nil
}

func (m *MockBlockDevice) Sync() error {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	if m.closed {
		return errors.New("device is closed")
	}

	if m.failSync {
		return errors.New("mock sync failure")
	}

	return nil
}

func (m *MockBlockDevice) Close() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	m.closed = true
	return nil
}

func (m *MockBlockDevice) Path() string {
	return m.path
}

func (m *MockBlockDevice) IsClosed() bool {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	return m.closed
}

// SetFailRead configures the mock to fail read operations
func (m *MockBlockDevice) SetFailRead(fail bool) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.failRead = fail
}

// SetFailWrite configures the mock to fail write operations
func (m *MockBlockDevice) SetFailWrite(fail bool) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.failWrite = fail
}

// SetFailSync configures the mock to fail sync operations
func (m *MockBlockDevice) SetFailSync(fail bool) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.failSync = fail
}

// GetData returns a copy of the current data buffer
func (m *MockBlockDevice) GetData() []byte {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	result := make([]byte, len(m.data))
	copy(result, m.data)
	return result
}

// WritePeerHeartbeat writes a heartbeat message to a specific peer slot for testing
func (m *MockBlockDevice) WritePeerHeartbeat(nodeID uint16, timestamp uint64, sequence uint64) error {
	// Create heartbeat message
	header := sbdprotocol.NewHeartbeat(nodeID, sequence)
	header.Timestamp = timestamp
	heartbeatMsg := sbdprotocol.SBDHeartbeatMessage{Header: header}

	// Marshal the message
	msgBytes, err := sbdprotocol.MarshalHeartbeat(heartbeatMsg)
	if err != nil {
		return fmt.Errorf("failed to marshal heartbeat message: %w", err)
	}

	// Calculate slot offset for this node
	slotOffset := int64(nodeID) * sbdprotocol.SBD_SLOT_SIZE

	// Write heartbeat message to the designated slot
	_, err = m.WriteAt(msgBytes, slotOffset)
	return err
}

// WriteFenceMessage writes a fence message to a specific slot for testing
func (m *MockBlockDevice) WriteFenceMessage(nodeID, targetNodeID uint16, sequence uint64, reason uint8) error {
	// Create fence message header
	fenceMsg := sbdprotocol.NewFence(nodeID, targetNodeID, sequence, reason)

	// Marshal the fence message
	msgBytes, err := sbdprotocol.MarshalFence(fenceMsg)
	if err != nil {
		return fmt.Errorf("failed to marshal fence message: %w", err)
	}

	// Calculate slot offset for the target node (where the fence message is written)
	slotOffset := int64(targetNodeID) * sbdprotocol.SBD_SLOT_SIZE

	// Write fence message to the designated slot
	_, err = m.WriteAt(msgBytes, slotOffset)
	return err
}

// MockWatchdog is a mock implementation of WatchdogInterface for testing
type MockWatchdog struct {
	path      string
	petCount  int
	closed    bool
	failPet   bool
	failClose bool
	mutex     sync.RWMutex
}

// NewMockWatchdog creates a new mock watchdog
func NewMockWatchdog(path string) *MockWatchdog {
	return &MockWatchdog{
		path: path,
	}
}

func (m *MockWatchdog) Pet() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.closed {
		return errors.New("watchdog is closed")
	}

	if m.failPet {
		return errors.New("mock pet failure")
	}

	m.petCount++
	return nil
}

func (m *MockWatchdog) Close() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.failClose {
		return errors.New("mock close failure")
	}

	m.closed = true
	return nil
}

func (m *MockWatchdog) Path() string {
	return m.path
}

func (m *MockWatchdog) Timeout() time.Duration {
	return 60 * time.Second
}

// GetPetCount returns the number of times Pet() was called
func (m *MockWatchdog) GetPetCount() int {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	return m.petCount
}

// SetFailPet configures the mock to fail pet operations
func (m *MockWatchdog) SetFailPet(fail bool) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.failPet = fail
}

// SetFailClose configures the mock to fail close operations
func (m *MockWatchdog) SetFailClose(fail bool) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.failClose = fail
}

// IsClosed returns true if the watchdog is closed
func (m *MockWatchdog) IsClosed() bool {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	return m.closed
}

// IsOpen returns true if the watchdog is open (opposite of IsClosed)
func (m *MockWatchdog) IsOpen() bool {
	return !m.IsClosed()
}

// SharedMockDevices provides a thread-safe registry for shared mock devices
// This is useful for tests that need multiple processes/goroutines to share the same device data
type SharedMockDevices struct {
	devices map[string]*MockBlockDevice
	mutex   sync.Mutex
}

var globalSharedDevices = &SharedMockDevices{
	devices: make(map[string]*MockBlockDevice),
}

// GetSharedMockDevice returns a shared mock device for the given path
func GetSharedMockDevice(path string, size int) BlockDeviceInterface {
	return globalSharedDevices.Get(path, size)
}

// Get returns a shared mock device for the given path
func (s *SharedMockDevices) Get(path string, size int) BlockDeviceInterface {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if device, exists := s.devices[path]; exists {
		return device
	}

	device := NewMockBlockDevice(path, size)
	s.devices[path] = device
	return device
}

// Clear removes all shared devices (for test cleanup)
func (s *SharedMockDevices) Clear() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.devices = make(map[string]*MockBlockDevice)
}

// ClearSharedMockDevices clears all shared devices (convenience function)
func ClearSharedMockDevices() {
	globalSharedDevices.Clear()
}
