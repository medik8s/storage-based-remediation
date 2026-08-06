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

package blockformat

import (
	"sync"
)

// ClosableDevice is the subset of a block device needed for lifecycle
// operations that OffsetDevice cannot provide on its own (Close, Path,
// IsClosed). Typically satisfied by blockdevice.Device or
// mocks.MockBlockDevice.
type ClosableDevice interface {
	Close() error
	Path() string
	IsClosed() bool
}

// SharedCloser wraps a ClosableDevice so that Close() is called exactly
// once regardless of how many callers invoke it. This is necessary when
// multiple BlockDeviceAdapters share the same underlying device.
type SharedCloser struct {
	once   sync.Once
	device ClosableDevice
	err    error
}

// NewSharedCloser creates a SharedCloser for the given device.
// Panics if device is nil (always a programmer error).
func NewSharedCloser(device ClosableDevice) *SharedCloser {
	if device == nil {
		panic("blockformat: NewSharedCloser called with nil device")
	}
	return &SharedCloser{device: device}
}

func (s *SharedCloser) Close() error {
	s.once.Do(func() {
		s.err = s.device.Close()
	})
	return s.err
}

func (s *SharedCloser) Path() string {
	return s.device.Path()
}

func (s *SharedCloser) IsClosed() bool {
	return s.device.IsClosed()
}

// BlockDeviceAdapter wraps an OffsetDevice (for region-scoped I/O) with
// a SharedCloser (for lifecycle). Multiple adapters sharing the same
// SharedCloser safely close the underlying device exactly once.
type BlockDeviceAdapter struct {
	offset  *OffsetDevice
	backing *SharedCloser
}

// NewBlockDeviceAdapter creates an adapter that uses the OffsetDevice for
// ReadAt/WriteAt/Sync and the SharedCloser for Close/Path/IsClosed.
// Panics if either argument is nil (always a programmer error).
func NewBlockDeviceAdapter(offset *OffsetDevice, backing *SharedCloser) *BlockDeviceAdapter {
	if offset == nil {
		panic("blockformat: NewBlockDeviceAdapter called with nil OffsetDevice")
	}
	if backing == nil {
		panic("blockformat: NewBlockDeviceAdapter called with nil SharedCloser")
	}
	return &BlockDeviceAdapter{
		offset:  offset,
		backing: backing,
	}
}

func (a *BlockDeviceAdapter) ReadAt(p []byte, off int64) (int, error) {
	return a.offset.ReadAt(p, off)
}

func (a *BlockDeviceAdapter) WriteAt(p []byte, off int64) (int, error) {
	return a.offset.WriteAt(p, off)
}

func (a *BlockDeviceAdapter) Sync() error {
	return a.offset.Sync()
}

func (a *BlockDeviceAdapter) Close() error {
	return a.backing.Close()
}

func (a *BlockDeviceAdapter) Path() string {
	return a.backing.Path()
}

func (a *BlockDeviceAdapter) IsClosed() bool {
	return a.backing.IsClosed()
}

