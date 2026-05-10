//go:build linux

package watchdog

import (
	"fmt"
	"os"
	"strconv"
	"strings"
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

// readTimeoutFromSysfsFile reads and parses timeout from a sysfs file
func (w *Watchdog) readTimeoutFromSysfsFile(path, deviceName string) (time.Duration, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	timeoutStr := strings.TrimSpace(string(data))
	timeoutSeconds, err := strconv.Atoi(timeoutStr)
	if err != nil {
		return 0, fmt.Errorf("failed to parse timeout value '%s': %w", timeoutStr, err)
	}

	if timeoutSeconds <= 0 {
		return 0, fmt.Errorf("invalid timeout value: %d", timeoutSeconds)
	}

	timeoutDuration := time.Duration(timeoutSeconds) * time.Second
	w.logger.V(2).Info("Read watchdog timeout from sysfs", "device", deviceName, "timeout", timeoutDuration)
	return timeoutDuration, nil
}

// getTimeoutSysfs reads the watchdog timeout from sysfs
// This is a fallback when ioctl is not available
func (w *Watchdog) getTimeoutSysfs() (time.Duration, error) {
	// Try watchdog0 first (most common case - primary hardware watchdog)
	watchdog0Path := fmt.Sprintf("%s/%s/%s", SysfsWatchdogClass, SysfsWatchdog0, SysfsTimeoutFile)
	if timeout, err := w.readTimeoutFromSysfsFile(watchdog0Path, SysfsWatchdog0); err == nil {
		return timeout, nil
	}

	// If watchdog0 doesn't exist or failed, enumerate all watchdog devices
	entries, err := os.ReadDir(SysfsWatchdogClass)
	if err != nil {
		return 0, fmt.Errorf("failed to read %s: %w", SysfsWatchdogClass, err)
	}

	for _, entry := range entries {
		timeoutPath := fmt.Sprintf("%s/%s/%s", SysfsWatchdogClass, entry.Name(), SysfsTimeoutFile)
		if timeout, err := w.readTimeoutFromSysfsFile(timeoutPath, entry.Name()); err == nil {
			return timeout, nil
		}
	}

	return 0, fmt.Errorf("no readable timeout found in %s", SysfsWatchdogClass)
}

// getTimeoutIoctl reads the actual hardware timeout from the watchdog device using ioctl
func (w *Watchdog) getTimeoutIoctl() (time.Duration, error) {
	timeoutSeconds, err := unix.IoctlGetInt(int(w.file.Fd()), WDIOC_GETTIMEOUT)
	if err != nil {
		return 0, fmt.Errorf("ioctl WDIOC_GETTIMEOUT failed: %w", err)
	}

	timeoutDuration := time.Duration(timeoutSeconds) * time.Second
	w.logger.V(2).Info("Read watchdog timeout from hardware via ioctl", "timeout", timeoutDuration)
	return timeoutDuration, nil
}
