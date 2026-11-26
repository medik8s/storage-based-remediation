//go:build linux

package blockpernode

import (
	"bytes"
	"fmt"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/medik8s/storage-based-remediation/v5/internal/sbdprotocol"
)

type Device struct{ file *os.File }

func OpenDevice(path string, writable bool) (*Device, error) {
	flags := unix.O_RDONLY | unix.O_DIRECT | unix.O_CLOEXEC
	if writable {
		flags = unix.O_RDWR | unix.O_DIRECT | unix.O_CLOEXEC
	}
	fd, err := unix.Open(path, flags, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if info.Mode()&os.ModeDevice == 0 || info.Mode()&os.ModeCharDevice != 0 {
		_ = f.Close()
		return nil, fmt.Errorf("%s is not a block device", path)
	}
	return &Device{file: f}, nil
}

func alignedSlot() []byte {
	data := make([]byte, 2*sbdprotocol.PerNodeHeartbeatSlotSize)
	offset := int((uintptr(sbdprotocol.PerNodeHeartbeatSlotSize) - uintptr(unsafe.Pointer(&data[0]))%uintptr(sbdprotocol.PerNodeHeartbeatSlotSize)) % uintptr(sbdprotocol.PerNodeHeartbeatSlotSize))
	return data[offset : offset+sbdprotocol.PerNodeHeartbeatSlotSize]
}

func (d *Device) Read() (uint64, error) {
	b := alignedSlot()
	n, err := d.file.ReadAt(b, 0)
	if err != nil {
		return 0, err
	}
	if n != len(b) {
		return 0, fmt.Errorf("short probe read: %d", n)
	}
	h, err := sbdprotocol.UnmarshalPerNodeHeartbeat(b)
	return h.CompletedSequence, err
}

// Probe publishes only the completed value from the preceding successful probe.
func (d *Device) Probe(sequence, completed uint64) error {
	return verifyProbe(d.file, sequence, completed)
}

type probeIO interface {
	WriteAt([]byte, int64) (int, error)
	ReadAt([]byte, int64) (int, error)
	Sync() error
}

func verifyProbe(device probeIO, sequence, completed uint64) error {
	written := alignedSlot()
	copy(written, sbdprotocol.MarshalPerNodeHeartbeat(sbdprotocol.PerNodeHeartbeat{Timestamp: uint64(time.Now().UnixNano()), Sequence: sequence, CompletedSequence: completed}))
	if n, err := device.WriteAt(written, 0); err != nil {
		return err
	} else if n != len(written) {
		return fmt.Errorf("short probe write: %d", n)
	}
	if err := device.Sync(); err != nil {
		return err
	}
	read := alignedSlot()
	if n, err := device.ReadAt(read, 0); err != nil {
		return err
	} else if n != len(read) {
		return fmt.Errorf("short probe verification: %d", n)
	}
	if !bytes.Equal(written, read) {
		return fmt.Errorf("probe verification mismatch")
	}
	return nil
}

func (d *Device) Close() error { return d.file.Close() }
