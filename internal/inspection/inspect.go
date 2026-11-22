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

// Package inspection reads SBR storage without initializing or repairing it.
package inspection

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/go-logr/logr"

	"github.com/medik8s/storage-based-remediation/v5/internal/agent"
	"github.com/medik8s/storage-based-remediation/v5/internal/blockdevice"
	"github.com/medik8s/storage-based-remediation/v5/internal/blockformat"
	"github.com/medik8s/storage-based-remediation/v5/internal/sbdprotocol"
)

// Report is a best-effort observation, not an atomic snapshot across regions.
// Nonempty Errors means some data could not be inspected reliably.
type Report struct {
	SchemaVersion int                       `json:"schema_version"`
	Mode          string                    `json:"mode"`
	Device        string                    `json:"device"`
	ObservedAt    time.Time                 `json:"observed_at"`
	Superblock    *blockformat.Superblock   `json:"superblock,omitempty"`
	NodeMap       *sbdprotocol.NodeMapTable `json:"node_map"`
	NodeMapStatus string                    `json:"node_map_status"`
	Heartbeats    []Slot                    `json:"heartbeats"`
	Fences        []Slot                    `json:"fences"`
	Errors        []string                  `json:"errors"`
}

// Slot identifies the physical slot separately from the message sender.
// Zero-filled slots are omitted. Invalid nonzero slots carry an Error.
type Slot struct {
	NodeID       uint16     `json:"node_id"`
	Offset       int64      `json:"offset"`
	SenderNodeID uint16     `json:"sender_node_id,omitempty"`
	TargetNodeID uint16     `json:"target_node_id,omitempty"`
	Type         string     `json:"type,omitempty"`
	Timestamp    *time.Time `json:"timestamp,omitempty"`
	Sequence     uint64     `json:"sequence"`
	Reason       *uint8     `json:"reason,omitempty"`
	ReasonName   string     `json:"reason_name,omitempty"`
	Error        string     `json:"error,omitempty"`
}

// readOnly supplies the legacy store's WriterAt requirement without granting
// write capability. The underlying file descriptor is also opened O_RDONLY.
type readOnly struct{ io.ReaderAt }

func (readOnly) WriteAt([]byte, int64) (int, error) {
	return 0, errors.New("inspection is read-only")
}

// Read detects the format and inspects all usable slots (1 through 255).
// It never calls agent startup, NodeManager, InitDevice, Save, Sync or flock.
func Read(path string) (*Report, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		path = filepath.Join(path, agent.SharedStorageSBRDeviceFile)
		info, err = os.Stat(path)
		if err != nil {
			return nil, err
		}
	}
	if !info.Mode().IsRegular() && (info.Mode()&os.ModeDevice == 0 || info.Mode()&os.ModeCharDevice != 0) {
		return nil, fmt.Errorf("%s is not a regular file or block device", path)
	}

	// Probe with direct I/O where available. Filesystem backends may require
	// buffered I/O; block-format reads below always require O_DIRECT.
	probe, err := readFileRegion(path, int(blockformat.BlockSuperblockSize))
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("probe %s: %w", path, err)
	}
	r := &Report{SchemaVersion: 1, Mode: "fs", Device: path, ObservedAt: time.Now().UTC(),
		Heartbeats: []Slot{}, Fences: []Slot{}, Errors: []string{}}
	if blockformat.HasSuperblockMagic(probe) || info.Mode()&os.ModeDevice != 0 {
		if err := r.readBlock(path); err != nil {
			return nil, err
		}
	} else {
		r.loadNodeMap(sbdprotocol.NewFileNodeMapStore(path + agent.SharedStorageNodeMappingSuffix))
		r.readFSRegion(path, false)
		r.readFSRegion(path+agent.SharedStorageFenceDeviceSuffix, true)
	}
	return r, nil
}

// readFileRegion opens anew on every call, retaining filesystem close-to-open
// semantics. O_DIRECT is preferred; only unsupported direct I/O falls back.
func readFileRegion(path string, size int) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECT, 0)
	if err == nil {
		fsName, fsErr := blockdevice.IsDirectIOUnsupportedFS(f)
		if fsErr != nil || fsName != "" {
			_ = f.Close()
			f = nil
			err = syscall.EOPNOTSUPP
		}
	}
	if err == nil {
		buf := blockdevice.DirectIOAlloc(size)
		n, readErr := f.ReadAt(buf, 0)
		_ = f.Close()
		if !unsupportedDirectIO(readErr) {
			return buf[:n], readErr
		}
		err = readErr
	}
	if !unsupportedDirectIO(err) {
		return nil, err
	}
	f, err = os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, size)
	n, err := f.ReadAt(buf, 0)
	return buf[:n], err
}

func unsupportedDirectIO(err error) bool {
	return errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOSYS)
}

func (r *Report) readBlock(path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECT, 0)
	if err != nil {
		return fmt.Errorf("open block storage for direct read: %w", err)
	}
	defer func() { _ = f.Close() }()
	buf := blockdevice.DirectIOAlloc(int(blockformat.BlockSuperblockSize))
	if _, err := f.ReadAt(buf, blockformat.BlockSuperblockOffset); err != nil {
		return fmt.Errorf("read superblock: %w", err)
	}
	sb, err := blockformat.UnmarshalSuperblock(buf)
	if err != nil {
		return err
	}
	if err := sb.Validate(); err != nil {
		return err
	}
	r.Mode, r.Superblock = "block", sb
	// V1 validation above guarantees the store's fixed A/B offsets match.
	r.loadNodeMap(blockformat.NewBlockNodeMapStore(readOnly{f}, logr.Discard()))
	for _, region := range []struct {
		offset int64
		fence  bool
	}{{sb.HeartbeatRegOffset, false}, {sb.FenceRegOffset, true}} {
		data := blockdevice.DirectIOAlloc(int(blockformat.BlockSlotSize))
		for id := uint16(1); id <= sbdprotocol.SBD_MAX_NODES; id++ {
			offset := region.offset + int64(id-1)*blockformat.BlockSlotSize
			n, err := f.ReadAt(data, offset)
			if err != nil || n != len(data) {
				r.Errors = append(r.Errors, fmt.Sprintf("read slot %d at offset %d: got %d bytes: %v", id, offset, n, err))
				// Do not interpret short reads as empty slots or keep hammering
				// an unavailable region. Continue with the other region.
				break
			}
			r.addSlot(id, offset, data, region.fence)
		}
	}
	return nil
}

func (r *Report) loadNodeMap(store interface{ Load() ([]byte, error) }) {
	data, err := store.Load()
	if errors.Is(err, os.ErrNotExist) {
		r.NodeMapStatus = "absent"
		return
	}
	if err == nil {
		r.NodeMap, err = sbdprotocol.UnmarshalNodeMapTable(data)
	}
	if err != nil {
		r.NodeMapStatus = "invalid"
		r.Errors = append(r.Errors, "node map: "+err.Error())
		return
	}
	r.NodeMapStatus = "valid"
}

func (r *Report) readFSRegion(path string, fence bool) {
	// Legacy init preallocates 255 slots including unused slot zero; node
	// 255 extends the file on first write. Only that wholly absent last slot
	// is accepted, never a partially written slot or a shorter device.
	data, err := readFileRegion(path, (sbdprotocol.SBD_MAX_NODES+1)*sbdprotocol.SBD_SLOT_SIZE)
	if err != nil && !errors.Is(err, io.EOF) {
		r.Errors = append(r.Errors, fmt.Sprintf("read %s: %v", path, err))
		return
	}
	minimum := sbdprotocol.SBD_MAX_NODES * sbdprotocol.SBD_SLOT_SIZE
	if len(data) < minimum || len(data)%sbdprotocol.SBD_SLOT_SIZE != 0 {
		r.Errors = append(r.Errors, fmt.Sprintf("truncated %s: got %d bytes", path, len(data)))
	}
	for id := uint16(1); id <= sbdprotocol.SBD_MAX_NODES; id++ {
		offset := int(id) * sbdprotocol.SBD_SLOT_SIZE
		if offset+sbdprotocol.SBD_SLOT_SIZE > len(data) {
			break
		}
		r.addSlot(id, int64(offset), data[offset:offset+sbdprotocol.SBD_SLOT_SIZE], fence)
	}
}

func (r *Report) addSlot(id uint16, offset int64, data []byte, fence bool) {
	if bytes.Equal(data, make([]byte, len(data))) {
		return
	}
	slot := Slot{NodeID: id, Offset: offset}
	err := decodeSlot(&slot, data, fence)
	if err != nil {
		slot.Error = err.Error()
		r.Errors = append(r.Errors, fmt.Sprintf("slot %d at offset %d: %v", id, offset, err))
	}
	if fence {
		r.Fences = append(r.Fences, slot)
	} else {
		r.Heartbeats = append(r.Heartbeats, slot)
	}
}

func decodeSlot(slot *Slot, data []byte, fence bool) error {
	// The protocol CRC covers the header, not slot padding. Fence payload
	// fields are decoded by UnmarshalFence (the wire format has no payload CRC).
	h, err := sbdprotocol.Unmarshal(data[:sbdprotocol.SBD_HEADER_SIZE])
	if err != nil {
		return err
	}
	if h.Version != 1 || !sbdprotocol.IsValidNodeID(h.NodeID) {
		return fmt.Errorf("invalid version %d or sender node ID %d", h.Version, h.NodeID)
	}
	slot.SenderNodeID, slot.Sequence, slot.Type = h.NodeID, h.Sequence, sbdprotocol.GetMessageTypeName(h.Type)
	ts := time.Unix(0, int64(h.Timestamp)).UTC()
	slot.Timestamp = &ts
	if fence {
		msg, err := sbdprotocol.UnmarshalFence(data[:sbdprotocol.SBD_HEADER_SIZE+3])
		if err != nil {
			return err
		}
		slot.TargetNodeID, slot.Reason = msg.TargetNodeID, &msg.Reason
		slot.ReasonName = sbdprotocol.GetFenceReasonName(msg.Reason)
		if msg.Reason == sbdprotocol.FENCE_REASON_NONE {
			slot.ReasonName = "NONE"
		}
		if msg.TargetNodeID != slot.NodeID {
			return fmt.Errorf("fence target %d does not match slot %d", msg.TargetNodeID, slot.NodeID)
		}
	} else if h.Type != sbdprotocol.SBD_MSG_TYPE_HEARTBEAT || h.NodeID != slot.NodeID {
		return fmt.Errorf("expected heartbeat from node %d, got type %d from %d", slot.NodeID, h.Type, h.NodeID)
	}
	return nil
}
