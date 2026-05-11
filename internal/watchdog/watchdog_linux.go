//go:build linux

package watchdog

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// petWatchdogIoctl performs the WDIOC_KEEPALIVE ioctl syscall (Linux-specific)
func (w *Watchdog) petWatchdogIoctl() error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(w.file.Fd()), WDIOC_KEEPALIVE, 0) //nolint:unconvert
	if errno == 0 {
		w.logger.V(3).Info("Watchdog pet successful using WDIOC_KEEPALIVE ioctl")
		return nil
	}

	if errno == unix.ENOTTY {
		return ErrIoctlNotSupported // Signal to use write fallback
	}

	return fmt.Errorf("ioctl WDIOC_KEEPALIVE failed: %w", errno)
}
