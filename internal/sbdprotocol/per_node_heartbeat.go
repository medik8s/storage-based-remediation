package sbdprotocol

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
)

const (
	PerNodeHeartbeatSlotSize         = 4096
	PerNodeHeartbeatMessageSize      = 41
	PerNodeHeartbeatMessageType byte = 0x03
)

type PerNodeHeartbeat struct {
	Timestamp         uint64
	Sequence          uint64
	CompletedSequence uint64
}

func MarshalPerNodeHeartbeat(h PerNodeHeartbeat) []byte {
	b := make([]byte, PerNodeHeartbeatSlotSize)
	copy(b[:8], SBD_MAGIC)
	binary.LittleEndian.PutUint16(b[8:10], 1)
	b[10] = PerNodeHeartbeatMessageType
	binary.LittleEndian.PutUint16(b[11:13], 1)
	binary.LittleEndian.PutUint64(b[13:21], h.Timestamp)
	binary.LittleEndian.PutUint64(b[21:29], h.Sequence)
	binary.LittleEndian.PutUint64(b[29:37], h.CompletedSequence)
	binary.LittleEndian.PutUint32(b[37:41], crc32.ChecksumIEEE(b[:37]))
	return b
}

func UnmarshalPerNodeHeartbeat(b []byte) (PerNodeHeartbeat, error) {
	if len(b) != PerNodeHeartbeatSlotSize || string(b[:8]) != SBD_MAGIC || binary.LittleEndian.Uint16(b[8:10]) != 1 || b[10] != PerNodeHeartbeatMessageType || binary.LittleEndian.Uint16(b[11:13]) != 1 {
		return PerNodeHeartbeat{}, fmt.Errorf("invalid per-node heartbeat header or length")
	}
	if binary.LittleEndian.Uint32(b[37:41]) != crc32.ChecksumIEEE(b[:37]) {
		return PerNodeHeartbeat{}, fmt.Errorf("invalid per-node heartbeat checksum")
	}
	return PerNodeHeartbeat{Timestamp: binary.LittleEndian.Uint64(b[13:21]), Sequence: binary.LittleEndian.Uint64(b[21:29]), CompletedSequence: binary.LittleEndian.Uint64(b[29:37])}, nil
}
