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
	"fmt"
	"os"
	"path/filepath"
)

// NodeMapStore abstracts the persistence layer for node map data.
// Implementations handle storage-specific concerns (file I/O, block device
// double-buffering) while NodeManager handles CAS logic, locking, and
// corruption recovery.
type NodeMapStore interface {
	// Load reads the current node map data from storage.
	// Returns the raw marshaled bytes or an error.
	// If no node mapping exists, Load must return an error that satisfies
	// errors.Is(err, fs.ErrNotExist). Implementations may wrap the
	// underlying error; NodeManager uses errors.Is (not os.IsNotExist)
	// to distinguish "not found" from other failures.
	Load() ([]byte, error)

	// Save writes node map data to storage and makes it the current
	// visible version before returning. A subsequent Load on the same
	// store must return the saved data unless another writer
	// successfully committed a newer version.
	// The data is the output of NodeMapTable.Marshal() (CRC32 prefix +
	// JSON payload). Implementations must ensure crash-safe writes
	// (e.g. write-rename for files, CRC-protected double-buffering
	// for block devices).
	Save(data []byte) error
}

// FileNodeMapStore implements NodeMapStore using filesystem operations.
// It uses write-to-temp-then-rename for crash-safe atomic writes.
type FileNodeMapStore struct {
	filePath string
}

// NewFileNodeMapStore creates a FileNodeMapStore for the given path.
func NewFileNodeMapStore(filePath string) *FileNodeMapStore {
	return &FileNodeMapStore{filePath: filePath}
}

// Load reads the node map data from the file.
func (s *FileNodeMapStore) Load() ([]byte, error) {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Save writes node map data atomically using write-to-temp-then-rename.
func (s *FileNodeMapStore) Save(data []byte) error {
	// Create the directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(s.filePath), 0755); err != nil {
		return fmt.Errorf("failed to create directory for node mapping file: %w", err)
	}

	// Write data to temporary file first, then rename (atomic operation)
	tempFilePath := s.filePath + ".tmp"
	if err := os.WriteFile(tempFilePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write node mapping to temporary file %s: %w", tempFilePath, err)
	}

	// Atomic rename to final location
	if err := os.Rename(tempFilePath, s.filePath); err != nil {
		// Clean up temporary file on failure
		_ = os.Remove(tempFilePath)
		return fmt.Errorf("failed to rename temporary file to %s: %w", s.filePath, err)
	}

	return nil
}
