//go:build linux

package blockpernode

import (
	"fmt"
	"testing"
	"unsafe"
)

type probeMemory struct {
	bytes   []byte
	failure string
}

func (m *probeMemory) WriteAt(b []byte, offset int64) (int, error) {
	if m.failure == "write" {
		return 0, fmt.Errorf("write")
	}
	m.bytes = append([]byte(nil), b...)
	return len(b), nil
}
func (m *probeMemory) Sync() error {
	if m.failure == "flush" {
		return fmt.Errorf("flush")
	}
	return nil
}
func (m *probeMemory) ReadAt(b []byte, offset int64) (int, error) {
	if m.failure == "read" {
		return 0, fmt.Errorf("read")
	}
	copy(b, m.bytes)
	if m.failure == "verify" {
		b[29] ^= 1
	}
	return len(b), nil
}

func TestProbeRequiresWriteFlushAndVerification(t *testing.T) {
	for _, failure := range []string{"write", "flush", "read", "verify", ""} {
		t.Run(failure, func(t *testing.T) {
			memory := &probeMemory{failure: failure}
			err := verifyProbe(memory, 41, 40)
			if (err != nil) != (failure != "") {
				t.Fatalf("result %v", err)
			}
		})
	}
	b := alignedSlot()
	if len(b) != 4096 || uintptr(unsafe.Pointer(&b[0]))%4096 != 0 {
		t.Fatal("unaligned slot")
	}
	if _, err := OpenDevice("/dev/null", false); err == nil {
		t.Fatal("accepted character device")
	}
}
