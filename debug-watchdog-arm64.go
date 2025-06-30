package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

// Watchdog ioctl constants
const (
	WDIOC_KEEPALIVE   = 0x40045705 // _IOR(WATCHDOG_IOCTL_BASE, 5, int)
	WDIOC_GETTIMEOUT  = 0x40045707 // _IOR(WATCHDOG_IOCTL_BASE, 7, int)
	WDIOC_SETTIMEOUT  = 0xc0045706 // _IOWR(WATCHDOG_IOCTL_BASE, 6, int)
	WDIOC_GETTIMELEFT = 0x4004570a // _IOR(WATCHDOG_IOCTL_BASE, 10, int)
	WDIOC_GETINFO     = 0x40285701 // _IOR(WATCHDOG_IOCTL_BASE, 1, struct watchdog_info)
)

// WatchdogInfo structure for WDIOC_GETINFO
type WatchdogInfo struct {
	Options  uint32
	Version  uint32
	Identity [32]uint8
}

func main() {
	fmt.Printf("=== ARM64 Watchdog Debug Tool ===\n")
	fmt.Printf("Architecture: %s\n", runtime.GOARCH)
	fmt.Printf("Operating System: %s\n", runtime.GOOS)
	fmt.Printf("Time: %s\n\n", time.Now().Format(time.RFC3339))

	// Test both potential watchdog devices
	watchdogPaths := []string{"/dev/watchdog", "/dev/watchdog0", "/dev/watchdog1"}

	for _, path := range watchdogPaths {
		fmt.Printf("=== Testing Watchdog Device: %s ===\n", path)
		testWatchdogDevice(path)
		fmt.Println()
	}

	// Test loading softdog module
	fmt.Printf("=== Testing Softdog Module ===\n")
	testSoftdogModule()
}

func testWatchdogDevice(devicePath string) {
	// Check if device exists
	if _, err := os.Stat(devicePath); os.IsNotExist(err) {
		fmt.Printf("❌ Device %s does not exist\n", devicePath)
		return
	}
	fmt.Printf("✅ Device %s exists\n", devicePath)

	// Try to open the device
	file, err := os.OpenFile(devicePath, os.O_WRONLY, 0)
	if err != nil {
		fmt.Printf("❌ Failed to open %s: %v\n", devicePath, err)
		return
	}
	defer func() {
		// Write 'V' to disable watchdog before closing (magic close)
		file.Write([]byte("V"))
		file.Close()
	}()

	fmt.Printf("✅ Successfully opened %s\n", devicePath)
	fd := file.Fd()

	// Test WDIOC_GETINFO
	fmt.Printf("\n--- Testing WDIOC_GETINFO ---\n")
	testGetInfo(fd)

	// Test WDIOC_GETTIMEOUT
	fmt.Printf("\n--- Testing WDIOC_GETTIMEOUT ---\n")
	testGetTimeout(fd)

	// Test WDIOC_SETTIMEOUT
	fmt.Printf("\n--- Testing WDIOC_SETTIMEOUT ---\n")
	testSetTimeout(fd, 60)

	// Test WDIOC_GETTIMELEFT
	fmt.Printf("\n--- Testing WDIOC_GETTIMELEFT ---\n")
	testGetTimeLeft(fd)

	// Test WDIOC_KEEPALIVE (the problematic one)
	fmt.Printf("\n--- Testing WDIOC_KEEPALIVE ---\n")
	testKeepAlive(fd)

	// Test alternative keep alive methods
	fmt.Printf("\n--- Testing Alternative Keep-Alive Methods ---\n")
	testAlternativeKeepAlive(file)
}

func testGetInfo(fd uintptr) {
	var info WatchdogInfo
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, WDIOC_GETINFO, uintptr(unsafe.Pointer(&info)))
	if errno != 0 {
		fmt.Printf("❌ WDIOC_GETINFO failed: %v\n", errno)
		return
	}

	fmt.Printf("✅ WDIOC_GETINFO succeeded\n")
	fmt.Printf("  Options: 0x%x\n", info.Options)
	fmt.Printf("  Version: %d\n", info.Version)
	fmt.Printf("  Identity: %s\n", string(info.Identity[:]))
}

func testGetTimeout(fd uintptr) {
	var timeout int32
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, WDIOC_GETTIMEOUT, uintptr(unsafe.Pointer(&timeout)))
	if errno != 0 {
		fmt.Printf("❌ WDIOC_GETTIMEOUT failed: %v\n", errno)
		return
	}
	fmt.Printf("✅ WDIOC_GETTIMEOUT succeeded: %d seconds\n", timeout)
}

func testSetTimeout(fd uintptr, newTimeout int32) {
	timeout := newTimeout
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, WDIOC_SETTIMEOUT, uintptr(unsafe.Pointer(&timeout)))
	if errno != 0 {
		fmt.Printf("❌ WDIOC_SETTIMEOUT failed: %v\n", errno)
		return
	}
	fmt.Printf("✅ WDIOC_SETTIMEOUT succeeded: set to %d seconds\n", timeout)
}

func testGetTimeLeft(fd uintptr) {
	var timeLeft int32
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, WDIOC_GETTIMELEFT, uintptr(unsafe.Pointer(&timeLeft)))
	if errno != 0 {
		fmt.Printf("❌ WDIOC_GETTIMELEFT failed: %v\n", errno)
		return
	}
	fmt.Printf("✅ WDIOC_GETTIMELEFT succeeded: %d seconds remaining\n", timeLeft)
}

func testKeepAlive(fd uintptr) {
	// Test the problematic WDIOC_KEEPALIVE ioctl
	var dummy int32
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, WDIOC_KEEPALIVE, uintptr(unsafe.Pointer(&dummy)))
	if errno != 0 {
		fmt.Printf("❌ WDIOC_KEEPALIVE failed: %v (errno %d)\n", errno, errno)

		// Print more detailed error info
		if errno == syscall.ENOTTY {
			fmt.Printf("  → Error type: ENOTTY (Inappropriate ioctl for device)\n")
			fmt.Printf("  → This suggests the device doesn't support this ioctl\n")
		} else if errno == syscall.EINVAL {
			fmt.Printf("  → Error type: EINVAL (Invalid argument)\n")
		} else if errno == syscall.ENODEV {
			fmt.Printf("  → Error type: ENODEV (No such device)\n")
		}
		return
	}
	fmt.Printf("✅ WDIOC_KEEPALIVE succeeded\n")
}

func testAlternativeKeepAlive(file *os.File) {
	// Test writing to the device (alternative keep-alive method)
	fmt.Printf("Testing write-based keep-alive...\n")

	// Try writing a simple byte
	n, err := file.Write([]byte{0x01})
	if err != nil {
		fmt.Printf("❌ Write keep-alive failed: %v\n", err)
	} else {
		fmt.Printf("✅ Write keep-alive succeeded (wrote %d bytes)\n", n)
	}

	// Try writing specific characters
	testChars := []byte{'1', 'a', '\n'}
	for _, char := range testChars {
		n, err := file.Write([]byte{char})
		if err != nil {
			fmt.Printf("❌ Write '%c' failed: %v\n", char, err)
		} else {
			fmt.Printf("✅ Write '%c' succeeded (wrote %d bytes)\n", char, n)
		}
	}
}

func testSoftdogModule() {
	// Check if softdog module is loaded
	fmt.Printf("Checking if softdog module is loaded...\n")
	cmd := exec.Command("lsmod")
	output, err := cmd.Output()
	if err != nil {
		fmt.Printf("❌ Failed to run lsmod: %v\n", err)
		return
	}

	if contains(string(output), "softdog") {
		fmt.Printf("✅ softdog module is loaded\n")
	} else {
		fmt.Printf("❌ softdog module is not loaded\n")

		// Try to load it
		fmt.Printf("Attempting to load softdog module...\n")
		cmd = exec.Command("modprobe", "softdog", "soft_margin=60")
		err = cmd.Run()
		if err != nil {
			fmt.Printf("❌ Failed to load softdog: %v\n", err)
		} else {
			fmt.Printf("✅ Successfully loaded softdog module\n")
		}
	}

	// Check kernel version and architecture specific info
	fmt.Printf("\n--- Kernel Information ---\n")
	cmd = exec.Command("uname", "-a")
	output, err = cmd.Output()
	if err != nil {
		fmt.Printf("❌ Failed to get kernel info: %v\n", err)
	} else {
		fmt.Printf("Kernel: %s", string(output))
	}

	// Check available watchdog drivers
	fmt.Printf("\n--- Available Watchdog Drivers ---\n")
	cmd = exec.Command("ls", "-la", "/dev/watchdog*")
	output, err = cmd.Output()
	if err != nil {
		fmt.Printf("❌ No watchdog devices found: %v\n", err)
	} else {
		fmt.Printf("%s", string(output))
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) &&
		(s == substr ||
			(len(s) > len(substr) &&
				(s[:len(substr)] == substr ||
					s[len(s)-len(substr):] == substr ||
					containsMiddle(s, substr))))
}

func containsMiddle(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
