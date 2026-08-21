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
	"fmt"
	"io"
)

// Syncer is implemented by devices that support flushing writes to storage.
type Syncer interface {
	Sync() error
}

// OffsetDevice wraps a device with a base offset and optional region size
// bound. All ReadAt/WriteAt calls are translated by baseOffset. This allows
// the existing heartbeat and fence I/O code to operate on a region of a
// block device without modification.
type OffsetDevice struct {
	device     io.ReaderAt
	writer     io.WriterAt
	syncer     Syncer
	baseOffset int64
	regionSize int64 // 0 = unbounded
}

// NewOffsetDevice creates an OffsetDevice over the given device.
// regionSize of 0 means unbounded (no upper limit check).
// Panics if baseOffset or regionSize is negative (always a programmer error).
func NewOffsetDevice(dev DeviceReadWriterAt, baseOffset, regionSize int64) *OffsetDevice {
	if baseOffset < 0 {
		panic(fmt.Sprintf("blockformat: NewOffsetDevice called with negative baseOffset %d", baseOffset))
	}
	if regionSize < 0 {
		panic(fmt.Sprintf("blockformat: NewOffsetDevice called with negative regionSize %d", regionSize))
	}
	od := &OffsetDevice{
		device:     dev,
		writer:     dev,
		baseOffset: baseOffset,
		regionSize: regionSize,
	}
	if s, ok := dev.(Syncer); ok {
		od.syncer = s
	}
	return od
}

// ReadAt reads from the underlying device at baseOffset + off.
func (d *OffsetDevice) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("negative offset %d not allowed", off)
	}
	if d.regionSize > 0 && off+int64(len(p)) > d.regionSize {
		return 0, fmt.Errorf("read at offset %d + %d bytes exceeds region size %d",
			off, len(p), d.regionSize)
	}
	return d.device.ReadAt(p, d.baseOffset+off)
}

// WriteAt writes to the underlying device at baseOffset + off.
func (d *OffsetDevice) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("negative offset %d not allowed", off)
	}
	if d.regionSize > 0 && off+int64(len(p)) > d.regionSize {
		return 0, fmt.Errorf("write at offset %d + %d bytes exceeds region size %d",
			off, len(p), d.regionSize)
	}
	return d.writer.WriteAt(p, d.baseOffset+off)
}

// Sync delegates to the underlying device's Sync if available.
func (d *OffsetDevice) Sync() error {
	if d.syncer != nil {
		return d.syncer.Sync()
	}
	return nil
}
