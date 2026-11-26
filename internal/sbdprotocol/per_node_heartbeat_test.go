package sbdprotocol

import (
	"encoding/binary"
	"hash/crc32"
	"testing"
)

func TestPerNodeHeartbeatLayoutAndIntegrity(t *testing.T) {
	want := PerNodeHeartbeat{Timestamp: 0x0102030405060708, Sequence: 41, CompletedSequence: 40}
	b := MarshalPerNodeHeartbeat(want)
	if len(b) != 4096 || b[13] != 8 || b[21] != 41 || b[29] != 40 || binary.LittleEndian.Uint32(b[37:41]) != crc32.ChecksumIEEE(b[:37]) {
		t.Fatal("invalid little-endian slot layout")
	}
	got, err := UnmarshalPerNodeHeartbeat(b)
	if err != nil || got != want {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	for _, offset := range []int{0, 8, 10, 11, 21, 29, 37} {
		bad := append([]byte(nil), b...)
		bad[offset] ^= 1
		if _, err := UnmarshalPerNodeHeartbeat(bad); err == nil {
			t.Fatalf("accepted damaged offset %d", offset)
		}
	}
	for _, length := range []int{0, 33, 41, 4095} {
		if _, err := UnmarshalPerNodeHeartbeat(b[:length]); err == nil {
			t.Fatalf("accepted length %d", length)
		}
	}
	for _, value := range b[41:] {
		if value != 0 {
			t.Fatal("nonzero slot padding")
		}
	}
	if SBD_SLOT_SIZE != 512 || SBD_HEADER_SIZE != 33 {
		t.Fatal("shared protocol changed")
	}
}
