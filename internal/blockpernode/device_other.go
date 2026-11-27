//go:build !linux

package blockpernode

import "fmt"

type Device struct{}

func OpenDevice(path string, writable bool) (*Device, error) {
	return nil, fmt.Errorf("per-node direct I/O requires Linux")
}
func (d *Device) Read() (uint64, error) { return 0, fmt.Errorf("per-node direct I/O requires Linux") }
func (d *Device) Probe(sequence, completed uint64) error {
	return fmt.Errorf("per-node direct I/O requires Linux")
}
func (d *Device) Close() error { return nil }
