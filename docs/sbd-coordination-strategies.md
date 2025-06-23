# SBD Coordination Strategies

## Overview

The SBD operator uses coordination strategies to prevent race conditions when multiple nodes write to the shared SBD device simultaneously. The NodeManager provides two coordination strategies with automatic fallback.

## Coordination Strategies

### 1. File Locking Strategy (`file-locking`)

Uses POSIX file locking (`flock()`) to serialize write operations across nodes.

**When Used:**
- `--sbd-file-locking=true` (default)
- SBD device path is available
- Storage system supports POSIX file locking

**How It Works:**
```go
// Acquire exclusive lock on SBD device
lockFile, err := os.OpenFile(devicePath, os.O_RDWR, 0)
err = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX)

// Perform write operation
writeOperation()

// Lock automatically released when file closed
lockFile.Close()
```

**Benefits:**
- **True Serialization**: Prevents concurrent writes at the OS level
- **Deadlock Prevention**: Timeout-based lock acquisition (5 seconds)
- **Automatic Cleanup**: Locks released on process termination

**Storage Compatibility:**
- ✅ **Well Supported**: NFS, CephFS, GlusterFS
- ⚠️ **Limited Support**: Some cloud storage CSI drivers
- ❌ **Not Supported**: Object storage backends, some distributed filesystems

### 2. Jitter Fallback Strategy (`jitter-fallback` / `jitter-only`)

Uses randomized delays to reduce write collision probability.

**When Used:**
- `--sbd-file-locking=false` (explicit disable)
- File locking enabled but no device path available
- File locking fails or times out

**How It Works:**
```go
// Add random delay before write
jitter := time.Duration(rand.Intn(100)) * time.Millisecond
time.Sleep(jitter)

// Perform write operation
writeOperation()
```

**Benefits:**
- **Universal Compatibility**: Works with any block device
- **Low Overhead**: Simple delay mechanism
- **Reliable Fallback**: No external dependencies

## Configuration

### Command Line Flags

```bash
# Enable file locking (default)
./sbd-agent --sbd-file-locking=true --sbd-device=/shared/sbd

# Disable file locking (jitter-only)
./sbd-agent --sbd-file-locking=false --sbd-device=/shared/sbd

# Watchdog-only mode (no SBD device)
./sbd-agent --node-name=node1
```

### Environment Variables

```bash
# File locking can also be controlled via environment
export SBD_FILE_LOCKING=false
./sbd-agent --sbd-device=/shared/sbd
```

### NodeManager Configuration

```go
config := sbdprotocol.NodeManagerConfig{
    ClusterName:        "my-cluster",
    FileLockingEnabled: true,  // Enable file locking
    // ... other config
}
```

## Strategy Detection

The NodeManager automatically detects and reports the active coordination strategy:

```go
strategy := nodeManager.GetCoordinationStrategy()
// Returns: "file-locking", "jitter-fallback", or "jitter-only"
```

**Strategy Selection Logic:**
1. If `FileLockingEnabled=false` → `"jitter-only"`
2. If `FileLockingEnabled=true` but no device path → `"jitter-fallback"`
3. If `FileLockingEnabled=true` and device path available → `"file-locking"`

## Monitoring and Logging

### Log Messages

```
# Successful file locking
"Node assigned to slot via hash-based mapping" coordinationStrategy="file-locking"
"Executing write operation with coordination strategy: file-locking"

# Fallback to jitter
"Node assigned to slot via hash-based mapping" coordinationStrategy="jitter-fallback"
"Executing write operation with coordination strategy: jitter-fallback"

# Explicit jitter-only
"Node assigned to slot via hash-based mapping" coordinationStrategy="jitter-only"
```

### Metrics

The coordination strategy is logged but not exposed as a separate metric. Monitor through:
- `sbd_device_io_errors_total` - I/O operation failures
- `sbd_slot_assignment_retries_total` - Retry attempts due to conflicts

## Best Practices

### Storage System Recommendations

**For POSIX-Compatible Storage (NFS, CephFS, GlusterFS):**
```bash
# Recommended: Enable file locking for optimal coordination
./sbd-agent --sbd-file-locking=true --sbd-device=/shared/sbd
```

**For Cloud/Object Storage:**
```bash
# Recommended: Disable file locking to avoid timeout delays
./sbd-agent --sbd-file-locking=false --sbd-device=/shared/sbd
```

**For Mixed Environments:**
```bash
# Default behavior: Attempt file locking with automatic fallback
./sbd-agent --sbd-device=/shared/sbd
```

### Troubleshooting

**File Locking Timeouts:**
```
# Symptoms: 5-second delays during write operations
# Solution: Disable file locking for incompatible storage
./sbd-agent --sbd-file-locking=false --sbd-device=/shared/sbd
```

**High Retry Rates:**
```
# Check coordination strategy in logs
grep "coordinationStrategy" /var/log/sbd-agent.log

# If using jitter-only, consider enabling file locking
./sbd-agent --sbd-file-locking=true --sbd-device=/shared/sbd
```

## Migration Guide

### Upgrading from Previous Versions

Previous versions only used jitter-based coordination. The new version:
- **Defaults to file locking enabled** for better coordination
- **Automatically falls back** to jitter when file locking unavailable
- **Maintains backward compatibility** with all storage types

**No configuration changes required** - the default behavior provides optimal coordination for your storage system.

### Disabling File Locking

If you prefer the previous jitter-only behavior:
```bash
# Explicitly disable file locking
./sbd-agent --sbd-file-locking=false --sbd-device=/shared/sbd
```

## Technical Details

### File Locking Implementation

- **Lock Type**: Exclusive (`LOCK_EX`)
- **Timeout**: 5 seconds maximum wait
- **Retry**: No retries on lock timeout (falls back to jitter)
- **Cleanup**: Automatic on process termination or file close

### Jitter Implementation

- **Delay Range**: 0-100ms random delay
- **Distribution**: Uniform random distribution
- **Timing**: Applied before each write operation
- **Overhead**: Minimal CPU and memory impact

### Performance Characteristics

**File Locking:**
- **Latency**: 0-5000ms (depending on lock contention)
- **Throughput**: Serialized writes (one at a time)
- **CPU**: Low overhead
- **Memory**: One file handle per operation

**Jitter Fallback:**
- **Latency**: 0-100ms random delay
- **Throughput**: Concurrent writes with collision probability
- **CPU**: Minimal overhead
- **Memory**: No additional memory usage 