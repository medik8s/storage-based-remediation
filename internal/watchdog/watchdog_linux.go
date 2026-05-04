//go:build linux

package watchdog

import (
	"fmt"
	"time"

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

// getTimeoutIoctl reads the actual hardware timeout from the watchdog device using ioctl
// This returns the timeout value configured in the hardware, not the spec value.
// If this returns an error, the caller should fall back to using the default timeout.
func (w *Watchdog) getTimeoutIoctl() (time.Duration, error) {
	// Call WDIOC_GETTIMEOUT ioctl to read hardware timeout
	timeout, err := unix.IoctlGetInt(int(w.file.Fd()), WDIOC_GETTIMEOUT)
	if err != nil {
		return 0, fmt.Errorf("failed to get watchdog timeout from hardware: %w", err)
	}

	timeoutDuration := time.Duration(timeout) * time.Second
	w.logger.V(2).Info("Read watchdog timeout from hardware", "timeout", timeoutDuration)

	return timeoutDuration, nil
}
