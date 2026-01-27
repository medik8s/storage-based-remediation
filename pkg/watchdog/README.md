# Watchdog Package

The watchdog package provides a Go interface for interacting with Linux kernel watchdog devices, with automatic fallback to software watchdog (softdog) when hardware watchdog devices are not available.

## Overview

Hardware watchdog devices are used to automatically reset the system if the software becomes unresponsive. The application must periodically "pet" or "kick" the watchdog to prevent an automatic system reset.

## Features

- **Hardware Watchdog Support**: Direct interface to hardware watchdog devices via `/dev/watchdog`, `/dev/watchdog0`, etc.
- **Software Watchdog Fallback**: Automatic loading and use of the Linux `softdog` kernel module when no hardware watchdog is present
- **Robust Error Handling**: Comprehensive retry logic with exponential backoff for critical operations
- **Device Detection**: Automatic scanning for available watchdog devices on the system
- **Logging Integration**: Structured logging with logr for debugging and monitoring

## Quick Start

### Basic Usage with Automatic Fallback

```go
package main

import (
    "github.com/medik8s/sbd-operator/pkg/watchdog"
    "github.com/go-logr/logr"
)

func main() {
    logger := logr.Discard() // Use your preferred logger
    
    // This will try hardware watchdog first, fallback to softdog if needed
    wd, err := watchdog.NewWithSoftdogFallback("/dev/watchdog", logger)
    if err != nil {
        panic(err)
    }
    defer wd.Close()
    
    // Check if we're using software watchdog
    if wd.IsSoftdog() {
        logger.Info("Using software watchdog (softdog)")
    }
    
    // Pet the watchdog periodically
    for {
        if err := wd.Pet(); err != nil {
            logger.Error(err, "Failed to pet watchdog")
            break
        }
        time.Sleep(10 * time.Second)
    }
}
```

### Hardware-Only Usage

```go
// For cases where you only want hardware watchdog (no fallback)
wd, err := watchdog.New("/dev/watchdog")
if err != nil {
    panic(err)
}
defer wd.Close()
```

### Test Mode Usage

For development and testing environments, you can enable test mode to prevent actual system reboots:

```go
// Enable test mode - softdog will use soft_noboot=1 parameter
wd, err := watchdog.NewWithSoftdogFallbackAndTestMode("/dev/watchdog", true, logger)
if err != nil {
    panic(err)
}
defer wd.Close()

// In test mode, watchdog timeouts won't cause system reboot
if wd.IsSoftdog() {
    logger.Info("Using software watchdog in test mode (no reboots)")
}
```

Test mode is useful for:

- **Development**: Testing watchdog logic without system resets
- **CI/CD**: Running tests that involve watchdog functionality
- **Debugging**: Observing watchdog behavior without consequences

**Note**: Test mode only affects the `softdog` module. Hardware watchdogs will still cause system resets regardless of the test mode setting.

## Softdog Fallback Behavior

The `NewWithSoftdogFallback` function implements intelligent fallback logic:

1. **Primary Attempt**: Try to open the specified watchdog device path
2. **Device Scan**: If that fails, scan for other existing watchdog devices
3. **Fallback Decision**: Only attempt softdog loading if no hardware watchdog devices exist
4. **Module Loading**: Load the `softdog` kernel module with appropriate timeout and optional test mode
5. **Device Creation**: Wait for `/dev/watchdog` to appear and open it

The `NewWithSoftdogFallbackAndTestMode` function extends this behavior with test mode support:

- **Test Mode Disabled** (default): `nsenter --target 1 --mount --uts --ipc --net --pid -- modprobe softdog soft_margin=60`
- **Test Mode Enabled**: `nsenter --target 1 --mount --uts --ipc --net --pid -- modprobe softdog soft_margin=60 soft_noboot=1`

**Note**: The package uses `nsenter` to run `modprobe` in the host's namespace, ensuring the kernel module is loaded on the host system rather than in the container.

### System Requirements for Softdog

- Linux kernel with `softdog` module support
- `modprobe` command available in PATH
- `nsenter` command available in PATH (for running modprobe in host namespace)
- Sufficient privileges to load kernel modules (typically requires `SYS_MODULE` capability)
- Container environments need `privileged: true` or `SYS_MODULE` capability

## Error Handling

The package provides detailed error information:

```go
wd, err := watchdog.NewWithSoftdogFallback("/dev/watchdog", logger)
if err != nil {
    if strings.Contains(err.Error(), "failed to load softdog module") {
        // Softdog loading failed - likely permission or modprobe issues
        log.Printf("Cannot load softdog: %v", err)
    } else if strings.Contains(err.Error(), "other watchdog devices exist") {
        // Hardware watchdog present but can't access specified path
        log.Printf("Hardware watchdog access issue: %v", err)
    }
    return err
}
```

## Integration with SBD Operator

In the SBD operator context, the watchdog is used for:

- **System Fencing**: Ensuring unhealthy nodes reset themselves via watchdog timeout
- **Heartbeat Monitoring**: Regular watchdog petting as a liveness indicator
- **High Availability**: Automatic fallback ensures watchdog functionality even on systems without hardware support

### Container Deployment

When deploying in containers, ensure the following:

```yaml
apiVersion: apps/v1
kind: DaemonSet
spec:
  template:
    spec:
      containers:
      - name: sbd-agent
        securityContext:
          privileged: true
          capabilities:
            add:
            - SYS_ADMIN
            - SYS_MODULE  # Required for loading softdog
        volumeMounts:
        - name: dev
          mountPath: /dev
        - name: modules
          mountPath: /lib/modules
          readOnly: true
      volumes:
      - name: dev
        hostPath:
          path: /dev
      - name: modules
        hostPath:
          path: /lib/modules
```

## Logging

The package uses structured logging to provide visibility into watchdog operations:

```
INFO Successfully opened hardware watchdog device path="/dev/watchdog"
INFO Failed to open specified watchdog device, checking for alternatives requestedPath="/dev/watchdog" error="..."
INFO No watchdog devices found, attempting to load softdog module
INFO Loading softdog module using nsenter command="nsenter --target 1 --mount --uts --ipc --net --pid -- modprobe softdog soft_margin=60" timeout=60
INFO Successfully loaded and opened softdog watchdog device originalPath="/dev/watchdog" softdogPath="/dev/watchdog"
```

## Testing

The package includes comprehensive tests:

```bash
# Run all watchdog tests
go test ./pkg/watchdog -v

# Run softdog integration tests (requires root)
sudo go test ./pkg/watchdog -v -run TestLoadSoftdogModule_Integration
```

## Constants and Configuration

- **Default Softdog Timeout**: 60 seconds
- **Retry Configuration**: 2 retries with exponential backoff (50ms to 500ms)
- **Module Load Command**:
  - Normal mode: `nsenter --target 1 --mount --uts --ipc --net --pid -- modprobe softdog soft_margin=60`
  - Test mode: `nsenter --target 1 --mount --uts --ipc --net --pid -- modprobe softdog soft_margin=60 soft_noboot=1`

## Platform Support

- **Linux**: Full support with hardware and software watchdog
- **Other Platforms**: Hardware watchdog support only (no softdog fallback)

## Troubleshooting

### Common Issues

1. **Permission Denied**: Ensure container has `SYS_MODULE` capability
2. **Module Not Found**: Verify `softdog` module is available in kernel
3. **modprobe Not Found**: Ensure `util-linux` or equivalent package is installed
4. **Device Access**: Check `/dev/watchdog` permissions and ownership

### Debug Logging

Enable debug logging to see detailed operation flow:

```go
logger := logr.New(/* your debug-enabled logger */)
wd, err := watchdog.NewWithSoftdogFallback("/dev/watchdog", logger)
```

This will show device scanning, module loading attempts, and fallback decisions.

## Linux Watchdog IOCTL Commands

The package uses standard Linux watchdog ioctl commands:

- `WDIOC_KEEPALIVE` (0x40045705): Reset the watchdog timer
- `WDIOC_SETTIMEOUT` (0x40045706): Set timeout period  
- `WDIOC_GETTIMEOUT` (0x40045707): Get current timeout

## Security Considerations

- Watchdog operations typically require root privileges
- Improper use can cause unexpected system resets
- Always implement proper error handling and logging
- Consider graceful shutdown procedures

## Dependencies

- `golang.org/x/sys/unix`: For Linux system calls and ioctl operations
- Standard Go library packages (`os`, `fmt`, etc.)

## License

Copyright 2025 - Licensed under the Apache License, Version 2.0
