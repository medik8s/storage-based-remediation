//go:build !linux

package watchdog

// petWatchdogIoctl is a stub for non-Linux platforms where watchdog ioctl operations don't exist
// Returns ErrIoctlNotSupported to trigger the write-based fallback mechanism
func (w *Watchdog) petWatchdogIoctl() error {
	return ErrIoctlNotSupported
}
