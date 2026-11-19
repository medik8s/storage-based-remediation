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

package inspection

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-logr/logr"

	"github.com/medik8s/storage-based-remediation/v5/internal/agent"
	"github.com/medik8s/storage-based-remediation/v5/internal/blockformat"
	"github.com/medik8s/storage-based-remediation/v5/internal/sbdprotocol"
)

const blockMode = "block"

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T, block bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), agent.SharedStorageSBRDeviceFile)
	if block {
		f, err := os.Create(path)
		must(t, err)
		must(t, f.Truncate(blockformat.BlockMinDeviceSize))
		sb, err := blockformat.NewV1Superblock().Marshal()
		must(t, err)
		_, err = f.WriteAt(sb, 0)
		must(t, err)
		must(t, f.Close())
	} else {
		for _, p := range []string{path, path + agent.SharedStorageFenceDeviceSuffix} {
			must(t, os.WriteFile(p, make([]byte, 256*sbdprotocol.SBD_SLOT_SIZE), 0600))
		}
	}
	return path
}

func writeSlot(t *testing.T, path string, block, fence bool, id uint16, data []byte) {
	t.Helper()
	off := int64(id) * sbdprotocol.SBD_SLOT_SIZE
	if block {
		off = blockformat.BlockHeartbeatRegionOffset + int64(id-1)*blockformat.BlockSlotSize
		if fence {
			off = blockformat.BlockFenceRegionOffset + int64(id-1)*blockformat.BlockSlotSize
		}
	} else if fence {
		path += agent.SharedStorageFenceDeviceSuffix
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	must(t, err)
	_, err = f.WriteAt(data, off)
	must(t, err)
	must(t, f.Close())
}

func writeMap(t *testing.T, path string, block bool, cluster string) {
	t.Helper()
	table := sbdprotocol.NewNodeMapTable(cluster)
	_, err := table.AssignSlot("node-a", sbdprotocol.NewNodeHasher(cluster))
	must(t, err)
	data, err := table.Marshal()
	must(t, err)
	if block {
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		must(t, err)
		must(t, blockformat.NewBlockNodeMapStore(f, logr.Discard()).Save(data))
		must(t, f.Close())
	} else {
		must(t, sbdprotocol.NewFileNodeMapStore(path+agent.SharedStorageNodeMappingSuffix).Save(data))
	}
}

func diskBytes(t *testing.T, path string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(path))
	must(t, err)
	out := make(map[string][]byte)
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(filepath.Dir(path), e.Name()))
		must(t, err)
		out[e.Name()] = b
	}
	return out
}

func assertUnchanged(t *testing.T, path string, before map[string][]byte) {
	t.Helper()
	after := diskBytes(t, path)
	if len(after) != len(before) {
		t.Fatal("inspection created or removed files")
	}
	for name, b := range before {
		if !bytes.Equal(b, after[name]) {
			t.Fatalf("inspection modified %s", name)
		}
	}
}

func TestInspectBothModesReadOnly(t *testing.T) {
	for _, block := range []bool{false, true} {
		name := "fs"
		if block {
			name = blockMode
		}
		t.Run(name, func(t *testing.T) {
			path := fixture(t, block)
			writeMap(t, path, block, "cluster")
			for _, id := range []uint16{1, 127, 255} {
				h := sbdprotocol.NewHeartbeat(id, uint64(id)+100)
				h.Timestamp = 1700000000123456789
				data, err := sbdprotocol.Marshal(h)
				must(t, err)
				writeSlot(t, path, block, false, id, data)
			}
			msg := sbdprotocol.NewFence(127, 255, 42, sbdprotocol.FENCE_REASON_MANUAL)
			data, err := sbdprotocol.MarshalFence(msg)
			must(t, err)
			writeSlot(t, path, block, true, 255, data)
			cleared, err := sbdprotocol.MarshalFence(sbdprotocol.NewFence(1, 1, 0, sbdprotocol.FENCE_REASON_NONE))
			must(t, err)
			writeSlot(t, path, block, true, 1, cleared)
			before := diskBytes(t, path)
			for file := range before {
				must(t, os.Chmod(filepath.Join(filepath.Dir(path), file), 0400))
			}
			report, err := Read(path)
			must(t, err)
			if len(report.Errors) != 0 {
				t.Fatal(report.Errors)
			}
			if report.Mode != name || report.NodeMapStatus != "valid" || report.NodeMap.ClusterName != "cluster" {
				t.Fatalf("bad report: %+v", report)
			}
			if len(report.Heartbeats) != 3 || len(report.Fences) != 2 {
				t.Fatalf("slots missing: %+v", report)
			}
			for i, id := range []uint16{1, 127, 255} {
				s := report.Heartbeats[i]
				if s.NodeID != id || s.SenderNodeID != id || s.Timestamp.UnixNano() != 1700000000123456789 {
					t.Fatalf("bad slot: %+v", s)
				}
			}
			f := report.Fences[1]
			if f.NodeID != 255 || f.SenderNodeID != 127 || f.TargetNodeID != 255 ||
				*f.Reason != sbdprotocol.FENCE_REASON_MANUAL {
				t.Fatalf("bad fence: %+v", f)
			}
			if report.Fences[0].ReasonName != "NONE" {
				t.Fatal("cleared fence misreported")
			}
			assertUnchanged(t, path, before)
		})
	}
}

func TestInspectCorruptSlotsAndNodeMap(t *testing.T) {
	for _, block := range []bool{false, true} {
		name := "fs"
		if block {
			name = blockMode
		}
		t.Run(name, func(t *testing.T) {
			path := fixture(t, block)
			writeMap(t, path, block, "cluster")
			h, err := sbdprotocol.Marshal(sbdprotocol.NewHeartbeat(1, 1))
			must(t, err)
			h[20] ^= 1
			writeSlot(t, path, block, false, 1, h)
			good, err := sbdprotocol.Marshal(sbdprotocol.NewHeartbeat(255, 2))
			must(t, err)
			writeSlot(t, path, block, false, 255, good)
			badTarget, err := sbdprotocol.MarshalFence(sbdprotocol.NewFence(1, 2, 3, 1))
			must(t, err)
			writeSlot(t, path, block, true, 255, badTarget)
			if block {
				f, err := os.OpenFile(path, os.O_RDWR, 0)
				must(t, err)
				_, err = f.WriteAt([]byte{0xff}, blockformat.BlockNodeMapAOffset+100)
				must(t, err)
				must(t, f.Close())
			} else {
				must(t, os.WriteFile(path+agent.SharedStorageNodeMappingSuffix, []byte("broken"), 0600))
			}
			before := diskBytes(t, path)
			report, err := Read(path)
			must(t, err)
			if len(report.Errors) != 3 || report.NodeMapStatus != "invalid" {
				t.Fatalf("corruption hidden: %+v", report)
			}
			if len(report.Heartbeats) != 2 || report.Heartbeats[0].Error == "" || report.Heartbeats[1].Error != "" {
				t.Fatalf("valid slot lost: %+v", report.Heartbeats)
			}
			if !strings.Contains(report.Fences[0].Error, "target") {
				t.Fatalf("wrong fence validation: %+v", report.Fences)
			}
			assertUnchanged(t, path, before)
		})
	}
}

func TestInspectBlockNodeMapFallbackAndQuiesce(t *testing.T) {
	path := fixture(t, true)
	writeMap(t, path, true, "old")
	writeMap(t, path, true, "new")
	r, err := Read(path)
	must(t, err)
	if r.NodeMap.ClusterName != "new" {
		t.Fatal("did not select newest buffer")
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	must(t, err)
	_, err = f.WriteAt([]byte{0xff}, blockformat.BlockNodeMapBOffset+100)
	must(t, err)
	sb := blockformat.NewV1Superblock()
	sb.Flags = blockformat.FlagQuiesce
	b, err := sb.Marshal()
	must(t, err)
	_, err = f.WriteAt(b, 0)
	must(t, err)
	must(t, f.Close())
	before := diskBytes(t, path)
	r, err = Read(path)
	must(t, err)
	if len(r.Errors) != 0 || r.NodeMap.ClusterName != "old" || !r.Superblock.IsQuiesced() {
		t.Fatalf("bad fallback/quiesce: %+v", r)
	}
	assertUnchanged(t, path, before)
}

func TestInspectRejectsInvalidSuperblock(t *testing.T) {
	for _, kind := range []string{"crc", "version", "offset", "length"} {
		t.Run(kind, func(t *testing.T) {
			path := fixture(t, true)
			sb := blockformat.NewV1Superblock()
			switch kind {
			case "version":
				sb.Version++
			case "offset":
				sb.FenceRegOffset += 4096
			case "length":
				sb.NodeMapALength += 4096
			}
			data, err := sb.Marshal()
			must(t, err)
			if kind == "crc" {
				data[76] ^= 1
			}
			f, err := os.OpenFile(path, os.O_RDWR, 0)
			must(t, err)
			_, err = f.WriteAt(data, 0)
			must(t, err)
			must(t, f.Close())
			before := diskBytes(t, path)
			if _, err := Read(path); err == nil {
				t.Fatal("invalid superblock accepted")
			}
			assertUnchanged(t, path, before)
		})
	}
}

func TestInspectEmptyMissingAndTruncatedStorage(t *testing.T) {
	for _, block := range []bool{false, true} {
		path := fixture(t, block)
		r, err := Read(path)
		must(t, err)
		if r.NodeMapStatus != "absent" || len(r.Heartbeats) != 0 || len(r.Fences) != 0 || len(r.Errors) != 0 {
			t.Fatalf("bad empty report: %+v", r)
		}
		if block {
			must(t, os.Truncate(path, blockformat.BlockMinDeviceSize-1))
		} else {
			// Legacy init's absent slot 255 is allowed; a partial slot is not.
			must(t, os.Truncate(path, 255*sbdprotocol.SBD_SLOT_SIZE))
			r, err = Read(filepath.Dir(path))
			must(t, err)
			if len(r.Errors) != 0 {
				t.Fatal(r.Errors)
			}
			must(t, os.Truncate(path, 255*sbdprotocol.SBD_SLOT_SIZE-1))
		}
		r, err = Read(path)
		must(t, err)
		if len(r.Errors) == 0 {
			t.Fatal("truncation hidden")
		}
	}
	path := filepath.Join(t.TempDir(), "missing")
	if _, err := Read(path); err == nil {
		t.Fatal("missing path accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("missing storage was created")
	}
	path = fixture(t, false)
	must(t, os.Remove(path+agent.SharedStorageFenceDeviceSuffix))
	r, err := Read(path)
	must(t, err)
	if len(r.Errors) == 0 {
		t.Fatal("missing fence file hidden")
	}
}

func TestInspectReadOnlyAdapter(t *testing.T) {
	if _, err := (readOnly{bytes.NewReader(nil)}).WriteAt([]byte{1}, 0); err == nil {
		t.Fatal("write permitted")
	}
}
