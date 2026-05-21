//go:build !linux

package watchdog

import "time"

// petWatchdogIoctl is a stub for non-Linux platforms where watchdog ioctl operations don't exist
// Returns ErrIoctlNotSupported to trigger the write-based fallback mechanism
func (w *Watchdog) petWatchdogIoctl() error {
	return ErrIoctlNotSupported
}

// getTimeoutIoctl is a stub for non-Linux platforms where watchdog ioctl operations don't exist
// Returns ErrIoctlNotSupported to signal that the caller should use the sysfs fallback
func (w *Watchdog) getTimeoutIoctl() (time.Duration, error) {
	return 0, ErrIoctlNotSupported
}

// getTimeoutSysfs is a stub for non-Linux platforms where sysfs doesn't exist
// Returns ErrSysfsNotAvailable to signal that the caller should use the default timeout
func (w *Watchdog) getTimeoutSysfs() (time.Duration, error) {
	return 0, ErrSysfsNotAvailable
}
