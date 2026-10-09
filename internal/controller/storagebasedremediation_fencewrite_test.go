/*
Copyright 2024.

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

package controller

import (
	"testing"

	"github.com/go-logr/logr"

	"github.com/medik8s/storage-based-remediation/v5/internal/mocks"
	"github.com/medik8s/storage-based-remediation/v5/internal/sbdprotocol"
)

// blockSlotSize mirrors blockformat.BlockSlotSize without importing the linux-only blockformat
// package into this cross-platform test.
const testBlockSlotSize int64 = 4096

// TestWriteFenceMessageSlotGeometry pins the fence-write slot geometry in both modes. The block-mode
// case is a regression guard for two O_DIRECT bugs: writeFenceMessage used the 512-byte slot size
// and an unpadded buffer (so the poison pill failed to write with EINVAL), and slotOffset used the
// filesystem base nodeID*slotSize instead of the packed block-mode base (nodeID-1)*slotSize (so the
// pill landed one slot past where the victim reads).
//
// wantOffset is written out per case rather than derived from r.slotOffset, so the test pins the
// offset the victim actually reads instead of restating whatever the reconciler computes.
func TestWriteFenceMessageSlotGeometry(t *testing.T) {
	const targetNodeID = 14

	cases := []struct {
		name       string
		blockMode  bool
		wantOffset int64
	}{
		{
			name:       "filesystem-mode",
			blockMode:  false,
			wantOffset: targetNodeID * sbdprotocol.SBD_SLOT_SIZE,
		},
		{
			name:       "block-mode",
			blockMode:  true,
			wantOffset: (targetNodeID - 1) * testBlockSlotSize,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Size the device to hold the whole fence region for 189 nodes at the larger geometry.
			dev := mocks.NewMockBlockDevice("/tmp/test-fence", int(testBlockSlotSize)*189)
			r := &SBRRemediationReconciler{ownNodeID: 1}
			r.SetSBRDevices(nil, dev)
			if tc.blockMode {
				r.SetBlockMode(true, testBlockSlotSize, make([]byte, testBlockSlotSize), make([]byte, testBlockSlotSize))
			}

			if got := r.SlotOffset(targetNodeID); got != tc.wantOffset {
				t.Fatalf("SlotOffset(%d) = %d, want %d", targetNodeID, got, tc.wantOffset)
			}

			if err := r.writeFenceMessage(targetNodeID, logr.Discard()); err != nil {
				t.Fatalf("writeFenceMessage: %v", err)
			}

			// Assert a valid fence message targeting the victim landed exactly at the offset the
			// victim reads its own slot from.
			slotOffset := tc.wantOffset
			slot := make([]byte, sbdprotocol.SBD_HEADER_SIZE+3)
			n, err := dev.ReadAt(slot, slotOffset)
			if err != nil {
				t.Fatalf("ReadAt(%d): %v", slotOffset, err)
			}
			if n < sbdprotocol.SBD_HEADER_SIZE {
				t.Fatalf("short read at slot offset %d: %d bytes", slotOffset, n)
			}
			fenceMsg, err := sbdprotocol.UnmarshalFence(slot)
			if err != nil {
				t.Fatalf("no valid fence message at victim slot offset %d: %v", slotOffset, err)
			}
			if fenceMsg.Header.Type != sbdprotocol.SBD_MSG_TYPE_FENCE {
				t.Fatalf("wrong message type at slot: %d", fenceMsg.Header.Type)
			}
			if fenceMsg.TargetNodeID != targetNodeID {
				t.Fatalf("fence message targets node %d, want %d", fenceMsg.TargetNodeID, targetNodeID)
			}
		})
	}
}
