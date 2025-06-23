package sbdprotocol

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

// MockSBDDevice is a mock implementation of SBDDevice for testing
type MockSBDDevice struct {
	data      []byte
	path      string
	closed    bool
	failRead  bool
	failWrite bool
	failSync  bool
	mutex     sync.RWMutex
}

// NewMockSBDDevice creates a new mock SBD device with the specified size
func NewMockSBDDevice(path string, size int) *MockSBDDevice {
	return &MockSBDDevice{
		data: make([]byte, size),
		path: path,
	}
}

func (m *MockSBDDevice) ReadAt(p []byte, off int64) (int, error) {
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

func (m *MockSBDDevice) WriteAt(p []byte, off int64) (int, error) {
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

func (m *MockSBDDevice) Sync() error {
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

func (m *MockSBDDevice) Close() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	m.closed = true
	return nil
}

func (m *MockSBDDevice) Path() string {
	return m.path
}

func TestNodeManager_CoordinationStrategy(t *testing.T) {
	tests := []struct {
		name                string
		fileLockingEnabled  bool
		devicePath          string
		expectedStrategy    string
		shouldTestWriteLock bool
	}{
		{
			name:                "file locking enabled with valid path",
			fileLockingEnabled:  true,
			devicePath:          "/dev/test",
			expectedStrategy:    "file-locking",
			shouldTestWriteLock: false, // Skip WriteWithLock test for file locking (would need real file)
		},
		{
			name:                "file locking enabled with empty path",
			fileLockingEnabled:  true,
			devicePath:          "",
			expectedStrategy:    "jitter-fallback",
			shouldTestWriteLock: true, // This should use jitter fallback
		},
		{
			name:                "file locking disabled",
			fileLockingEnabled:  false,
			devicePath:          "/dev/test",
			expectedStrategy:    "jitter-only",
			shouldTestWriteLock: true, // This should use jitter
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a mock device
			mockDevice := NewMockSBDDevice(tt.devicePath, SBD_SLOT_SIZE*10)

			config := NodeManagerConfig{
				ClusterName:        "test-cluster",
				SyncInterval:       time.Second,
				StaleNodeTimeout:   time.Minute,
				Logger:             logr.Discard(),
				FileLockingEnabled: tt.fileLockingEnabled,
			}

			nm, err := NewNodeManager(mockDevice, config)
			if err != nil {
				t.Fatalf("Failed to create node manager: %v", err)
			}
			defer nm.Close()

			// Test coordination strategy
			strategy := nm.GetCoordinationStrategy()
			if strategy != tt.expectedStrategy {
				t.Errorf("Expected coordination strategy %s, got %s", tt.expectedStrategy, strategy)
			}

			// Test file locking enabled flag
			if nm.IsFileLockingEnabled() != tt.fileLockingEnabled {
				t.Errorf("Expected file locking enabled %v, got %v", tt.fileLockingEnabled, nm.IsFileLockingEnabled())
			}

			// Test WriteWithLock for cases that should work (jitter-based coordination)
			if tt.shouldTestWriteLock {
				err = nm.WriteWithLock("test operation", func() error {
					return nil
				})
				if err != nil {
					t.Errorf("WriteWithLock failed: %v", err)
				}
			}
		})
	}
}
