/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package watchdog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"golang.org/x/sys/unix"

	"github.com/medik8s/storage-based-remediation/internal/retry"
)

// Errors for watchdog operations
var (
	// ErrIoctlNotSupported indicates that the watchdog driver doesn't support ioctl operations
	ErrIoctlNotSupported = errors.New("ioctl not supported by watchdog driver")
	// ErrSysfsNotAvailable indicates that sysfs is not available on this platform
	ErrSysfsNotAvailable = errors.New("sysfs not available on this platform")
)

// Retry configuration constants for watchdog operations
const (
	// MaxWatchdogRetries is the maximum number of retry attempts for watchdog operations
	MaxWatchdogRetries = 2
	// InitialWatchdogRetryDelay is the initial delay between watchdog retry attempts
	InitialWatchdogRetryDelay = 50 * time.Millisecond
	// MaxWatchdogRetryDelay is the maximum delay between watchdog retry attempts
	MaxWatchdogRetryDelay = 500 * time.Millisecond
	// WatchdogRetryBackoffFactor is the exponential backoff factor for watchdog retry delays
	WatchdogRetryBackoffFactor = 2.0
)

// Softdog configuration constants
const (
	// SoftdogModule is the name of the Linux software watchdog kernel module
	SoftdogModule = "softdog"
	// DefaultSoftdogTimeout is the default timeout for the softdog module
	DefaultSoftdogTimeout = 60 * time.Second
	// SoftdogModprobe is the command to load the softdog module
	SoftdogModprobe = "modprobe"
	// NsenterCommand is the command to enter host namespaces
	NsenterCommand = "nsenter"
	// HostPID is the PID of the host init process
	HostPID = "1"
)

// Sysfs watchdog paths for timeout discovery
const (
	// SysfsWatchdogClass is the sysfs path containing watchdog devices
	SysfsWatchdogClass = "/sys/class/watchdog"
	// SysfsWatchdog0 is the primary watchdog device name
	SysfsWatchdog0 = "watchdog0"
	// SysfsTimeoutFile is the timeout file name in watchdog sysfs directories
	SysfsTimeoutFile = "timeout"
)

// Watchdog represents a Linux kernel watchdog device interface.
// It provides methods to interact with hardware watchdog devices through
// the Linux watchdog subsystem.
type Watchdog struct {
	// fd is the raw file descriptor for the watchdog device
	fd int
	// path is the filesystem path to the watchdog device
	path string
	// isOpen tracks whether the watchdog device is currently open
	isOpen bool
	// logger for logging watchdog operations and retries
	logger logr.Logger
	// retryConfig holds the retry configuration for this watchdog
	retryConfig retry.Config
	// isSoftdog indicates if this watchdog is using the software watchdog (softdog) module
	isSoftdog bool
}

// NewWithSoftdogFallback creates a new Watchdog instance, attempting to use the specified path first,
// and falling back to loading and using the softdog module if no hardware watchdog is available.
//
// This function provides automatic fallback behavior:
// 1. Try to open the specified watchdog device path
// 2. If that fails and no other watchdog devices exist, try to load softdog module
// 3. If softdog loads successfully, use /dev/watchdog as the device path
//
// Parameters:
//   - path: The preferred filesystem path to the watchdog device (e.g., "/dev/watchdog")
//   - logger: Logger for debugging and error reporting
//
// Returns:
//   - *Watchdog: A new Watchdog instance if successful
//   - error: An error if neither hardware nor software watchdog can be initialized
//
// This is the recommended function for production use as it provides the best reliability.
func NewWithSoftdogFallback(path string, logger logr.Logger) (*Watchdog, error) {
	return NewWithSoftdogFallbackAndTestMode(path, false, logger)
}

// NewWithSoftdogFallbackAndTestMode creates a new Watchdog instance with optional test mode support.
// This is similar to NewWithSoftdogFallback but allows enabling test mode for the softdog module.
//
// Parameters:
//   - path: The preferred filesystem path to the watchdog device (e.g., "/dev/watchdog")
//   - testMode: If true, enables soft_noboot=1 for softdog (prevents actual reboots during testing)
//   - logger: Logger for debugging and error reporting
//
// Returns:
//   - *Watchdog: A new Watchdog instance if successful
//   - error: An error if neither hardware nor software watchdog can be initialized
//
// Test mode is useful for development and testing environments where you want to test
// watchdog functionality without triggering actual system reboots.
func NewWithSoftdogFallbackAndTestMode(path string, testMode bool, logger logr.Logger) (*Watchdog, error) {
	if path == "" {
		return nil, fmt.Errorf("watchdog device path cannot be empty")
	}

	// First, try to open the specified watchdog device
	wd, err := NewWithLogger(path, logger)
	if err == nil {
		logger.Info("Successfully opened hardware watchdog device", "path", path)
		return wd, nil
	}

	logger.Info("Failed to open specified watchdog device, checking for alternatives",
		"requestedPath", path, "error", err.Error())

	// Check if any watchdog devices exist in the system
	existingDevices := findWatchdogDevices()
	if len(existingDevices) > 0 {
		logger.Info("Found existing watchdog devices, not loading softdog",
			"devices", existingDevices)
		// If other watchdog devices exist, return the original error
		// Don't automatically load softdog when hardware watchdogs are present
		return nil, fmt.Errorf("failed to open watchdog device at %s (other watchdog devices exist: %v): %w",
			path, existingDevices, err)
	}

	// No watchdog devices found, try to load softdog
	logger.Info("No watchdog devices found, attempting to load softdog module", "testMode", testMode)
	if err := loadSoftdogModule(testMode, logger); err != nil {
		return nil, fmt.Errorf("failed to load softdog module after watchdog device failure: %w", err)
	}

	// Wait a moment for the device to appear after module load
	time.Sleep(100 * time.Millisecond)

	// Try to open the softdog device (typically /dev/watchdog)
	softdogPath := "/dev/watchdog"
	wd, err = NewWithLogger(softdogPath, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to open softdog device at %s after loading module: %w", softdogPath, err)
	}

	// Mark this watchdog as using softdog
	wd.isSoftdog = true
	logger.Info("Successfully loaded and opened softdog watchdog device",
		"originalPath", path, "softdogPath", softdogPath, "testMode", testMode)

	return wd, nil
}

// findWatchdogDevices scans the system for existing watchdog devices
// Returns a list of watchdog device paths found in /dev/
func findWatchdogDevices() []string {
	var devices []string

	// Common watchdog device patterns
	patterns := []string{
		"/dev/watchdog*",
		"/dev/wdt*",
	}

	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue // Skip patterns that fail to expand
		}

		for _, match := range matches {
			// Verify it's actually a character device
			if info, err := os.Stat(match); err == nil {
				if info.Mode()&os.ModeCharDevice != 0 {
					devices = append(devices, match)
				}
			}
		}
	}

	// Always return an empty slice instead of nil
	if devices == nil {
		devices = []string{}
	}

	return devices
}

// loadSoftdogModule attempts to load the Linux softdog kernel module
func loadSoftdogModule(testMode bool, logger logr.Logger) error {
	// Check if softdog module is already loaded
	if isModuleLoaded(SoftdogModule) {
		logger.Info("Softdog module is already loaded")
		return nil
	}

	// Build the modprobe command with parameters
	modprobeArgs := []string{
		SoftdogModule,
		fmt.Sprintf("soft_margin=%d", int(DefaultSoftdogTimeout/time.Second)),
	}

	// Add soft_noboot parameter if test mode is enabled
	if testMode {
		modprobeArgs = append(modprobeArgs, "soft_noboot=1")
	}

	// Use nsenter to run modprobe in the host's namespace
	// This ensures the kernel module is loaded on the host system
	args := buildNsenterArgs(SoftdogModprobe, modprobeArgs...)

	cmd := exec.Command(NsenterCommand, args...)

	logger.Info("Loading softdog module using nsenter",
		"command", cmd.String(),
		"timeout", DefaultSoftdogTimeout,
		"testMode", testMode)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to load softdog module: %w (output: %s)", err, string(output))
	}

	// Verify the module was loaded successfully
	if !isModuleLoaded(SoftdogModule) {
		return fmt.Errorf("softdog module not loaded after modprobe command")
	}

	if testMode {
		logger.Info("Successfully loaded softdog module in test mode (soft_noboot=1)")
	} else {
		logger.Info("Successfully loaded softdog module")
	}
	return nil
}

// buildNsenterArgs builds the standard nsenter arguments to enter host namespaces
func buildNsenterArgs(hostCommand string, hostArgs ...string) []string {
	args := []string{
		"--target", HostPID, // Target PID 1 (init process)
		"--mount", // Enter mount namespace
		"--uts",   // Enter UTS namespace
		"--ipc",   // Enter IPC namespace
		"--net",   // Enter network namespace
		"--pid",   // Enter PID namespace
		"--",
		hostCommand,
	}
	args = append(args, hostArgs...)
	return args
}

// isModuleLoaded checks if a kernel module is currently loaded
func isModuleLoaded(moduleName string) bool {
	// Use nsenter to read /proc/modules from the host namespace
	args := buildNsenterArgs("cat", "/proc/modules")
	cmd := exec.Command(NsenterCommand, args...)

	output, err := cmd.Output()
	if err != nil {
		return false
	}

	// Each line in /proc/modules starts with the module name
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, moduleName+" ") || line == moduleName {
			return true
		}
	}

	return false
}

// IsSoftdog returns true if this watchdog is using the software watchdog (softdog) module
func (w *Watchdog) IsSoftdog() bool {
	return w.isSoftdog
}

// New creates a new Watchdog instance by opening the watchdog device at the specified path.
// Common paths include '/dev/watchdog' or '/dev/watchdog0'.
//
// Parameters:
//   - path: The filesystem path to the watchdog device (e.g., "/dev/watchdog")
//
// Returns:
//   - *Watchdog: A new Watchdog instance if successful
//   - error: An error if the device cannot be opened
//
// The device is opened with O_WRONLY flag as required by most watchdog devices.
// Once opened, the watchdog timer is typically activated and must be periodically
// reset using the Pet() method to prevent system reset.
func New(path string) (*Watchdog, error) {
	return NewWithLogger(path, logr.Discard())
}

// NewWithLogger creates a new Watchdog instance with a logger for retry operations
func NewWithLogger(path string, logger logr.Logger) (*Watchdog, error) {
	if path == "" {
		return nil, fmt.Errorf("watchdog device path cannot be empty")
	}

	// Configure retry settings for watchdog operations
	retryConfig := retry.Config{
		MaxRetries:    MaxWatchdogRetries,
		InitialDelay:  InitialWatchdogRetryDelay,
		MaxDelay:      MaxWatchdogRetryDelay,
		BackoffFactor: WatchdogRetryBackoffFactor,
		Logger:        logger.WithName("watchdog-retry"),
	}

	var fd int
	var err error

	// Retry watchdog opening for transient errors
	ctx := context.Background()
	err = retry.Do(ctx, retryConfig, "open watchdog device", func() error {
		fd, err = unix.Open(path, unix.O_WRONLY, 0644)
		if err != nil {
			return retry.NewRetryableError(err, retry.IsTransientError(err), "open watchdog device")
		}
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to open watchdog device at %s: %w", path, err)
	}

	return &Watchdog{
		fd:          fd,
		path:        path,
		isOpen:      true,
		logger:      logger.WithName("watchdog").WithValues("path", path),
		retryConfig: retryConfig,
	}, nil
}

// Pet resets the watchdog timer, preventing the system from being reset.
// This method must be called periodically (before the timeout expires)
// to keep the system running. The frequency depends on the watchdog's
// configured timeout value.
//
// This method includes retry logic for transient errors, as watchdog petting
// is critical for system stability. It uses a two-tier approach:
// 1. Primary: WDIOC_KEEPALIVE ioctl command (preferred method)
// 2. Fallback: Write-based keep-alive when ioctl is not supported (ENOTTY)
//
// Returns:
//   - error: An error if the watchdog cannot be pet after retries, or if the device is not open
//
// This method automatically falls back to write-based keep-alive when the
// WDIOC_KEEPALIVE ioctl is not supported by the watchdog driver (such as
// some softdog implementations). This ensures compatibility across different
// kernel configurations and architectures.
func (w *Watchdog) Pet() error {
	if !w.isOpen {
		return fmt.Errorf("watchdog device is not open")
	}

	if w.fd < 0 {
		return fmt.Errorf("watchdog file descriptor is invalid")
	}

	// Retry watchdog pet operations for transient errors
	ctx := context.Background()
	err := retry.Do(ctx, w.retryConfig, "pet watchdog", func() error {
		// Primary method: Use WDIOC_KEEPALIVE ioctl to reset the watchdog timer
		err := w.petWatchdogIoctl()
		if err == nil {
			return nil // Success with ioctl method
		}

		// Check if the error indicates ioctl is not supported
		if errors.Is(err, ErrIoctlNotSupported) {
			w.logger.V(2).Info("WDIOC_KEEPALIVE not supported, falling back to write-based keep-alive")

			// Fallback method: Use write-based keep-alive
			// Many watchdog devices accept any write as a keep-alive signal
			dummy := []byte{0}
			_, writeErr := unix.Write(w.fd, dummy)
			if writeErr != nil {
				return retry.NewRetryableError(
					fmt.Errorf("write-based keep-alive failed: %w", writeErr),
					retry.IsTransientError(writeErr),
					"write-based watchdog keep-alive")
			}

			w.logger.V(3).Info("Watchdog pet successful using write-based keep-alive")
			return nil
		}

		// Other ioctl error - treat as retryable
		return retry.NewRetryableError(err, retry.IsTransientError(err), "pet watchdog")
	})

	if err != nil {
		w.logger.Error(err, "Failed to pet watchdog after retries with both ioctl and write methods")
		return fmt.Errorf("failed to pet watchdog at %s: %w", w.path, err)
	}

	w.logger.V(2).Info("Watchdog pet successful")
	return nil
}

// Close closes the watchdog device file descriptor and releases associated resources.
//
// IMPORTANT: Closing the watchdog device may have different behaviors depending
// on the specific watchdog driver:
// - Some drivers stop the watchdog timer when the device is closed
// - Others continue running and will reset the system if not reopened and pet
// - Some require writing 'V' to the device before closing to stop the timer
//
// Returns:
//   - error: An error if the device cannot be closed properly
//
// This method marks the watchdog as closed and prevents further operations.
// It's safe to call Close() multiple times.
func (w *Watchdog) Close() error {
	if !w.isOpen {
		return nil // Already closed, not an error
	}

	if w.fd < 0 {
		w.isOpen = false
		return nil
	}

	// Some watchdog devices require writing 'V' to stop the timer before closing
	// This is known as the "magic close" feature and prevents accidental system resets
	// We'll write 'V' to attempt graceful shutdown, but don't fail if it doesn't work
	_, _ = unix.Write(w.fd, []byte("V"))

	err := unix.Close(w.fd)
	w.isOpen = false
	w.fd = -1

	if err != nil {
		return fmt.Errorf("failed to close watchdog device at %s: %w", w.path, err)
	}

	w.logger.V(1).Info("Watchdog device closed")
	return nil
}

// IsOpen returns true if the watchdog device is currently open and available for operations.
func (w *Watchdog) IsOpen() bool {
	return w.isOpen
}

// Path returns the filesystem path of the watchdog device.
func (w *Watchdog) Path() string {
	return w.path
}

// Timeout queries the watchdog device timeout.
// It tries multiple methods in order:
// 1. WDIOC_GETTIMEOUT ioctl (preferred method)
// 2. Sysfs reading from /sys/class/watchdog (fallback method)
// Returns the timeout as time.Duration, or an error if discovery fails.
func (w *Watchdog) Timeout() (time.Duration, error) {
	if !w.isOpen || w.fd < 0 {
		return 0, fmt.Errorf("watchdog device not open")
	}

	if w.IsSoftdog() {
		w.logger.V(1).Info("Softdog enabled. Returning default timeout", "timeout", DefaultSoftdogTimeout)
		return DefaultSoftdogTimeout, nil
	}

	// Try ioctl first (preferred method)
	timeout, err := w.getTimeoutIoctl()
	if err == nil {
		w.logger.V(1).Info("Discovered watchdog timeout via ioctl", "timeout", timeout)
		return timeout, nil
	}

	if !errors.Is(err, ErrIoctlNotSupported) {
		return 0, fmt.Errorf("ioctl timeout discovery failed: %w", err)
	}

	// If ioctl is not supported, try sysfs fallback
	w.logger.V(1).Info("WDIOC_GETTIMEOUT ioctl not supported, trying sysfs fallback", "ioctlError", err)
	timeout, sysfsErr := w.getTimeoutSysfs()
	if sysfsErr == nil {
		w.logger.V(1).Info("Discovered watchdog timeout via sysfs", "timeout", timeout)
		return timeout, nil
	}

	// Sysfs also failed
	return 0, fmt.Errorf("sysfs timeout discovery failed: %w", sysfsErr)
}
