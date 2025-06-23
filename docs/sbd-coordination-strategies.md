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

**Process Crashes and Lock Recovery:**
```
# Symptoms: Concern about deadlocks if SBD agent crashes while holding lock
# Solution: No action needed - POSIX guarantees automatic lock release

# Verification: Check that new processes can acquire locks after crash
# Expected: Lock acquisition succeeds within milliseconds of crash
# If not: Storage system may not support POSIX file locking properly
```

**Lock Contention During Cluster Events:**
```
# Symptoms: Multiple nodes trying to acquire locks simultaneously
# Expected: One node gets lock immediately, others wait up to 5 seconds
# If timeouts occur: System falls back to jitter coordination automatically
# No manual intervention required
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

## Frequently Asked Questions

### Q: What happens if the SBD agent crashes while holding a file lock?

**A**: The lock is **automatically released** by the kernel when the process exits or crashes. This is a fundamental POSIX guarantee.

- **No deadlocks possible**: Crashed processes cannot hold locks permanently
- **Fast recovery**: Other nodes can acquire the lock within milliseconds
- **No manual intervention**: The kernel handles cleanup automatically
- **Works for all exit scenarios**: Normal exit, crash, SIGKILL, power failure, etc.

### Q: What happens if the entire node/kernel crashes while holding a file lock?

**A**: The behavior depends on the **storage system architecture**. For SBD's target storage systems, locks are automatically released:

#### **Network-Attached Storage (NFS)**
- **Node crashes** → Network connection drops → **NFS server detects client disconnect**
- **Lock cleanup**: NFS server automatically releases all locks held by disconnected client
- **Recovery time**: Typically 30-60 seconds (depends on NFS timeout settings)
- **Other nodes**: Can acquire locks immediately after server cleanup

#### **Cluster Filesystems (CephFS, GlusterFS)**  
- **Node crashes** → Cluster detects node failure → **Distributed lock manager releases locks**
- **Lock cleanup**: Cluster consensus automatically invalidates crashed node's locks
- **Recovery time**: Usually 10-30 seconds (depends on cluster failure detection)
- **Other nodes**: Can acquire locks after cluster convergence

#### **Shared Block Storage (Ceph RBD, iSCSI SAN)**
- **Node crashes** → Storage fabric detects connection loss → **Locks released at storage level**
- **Lock cleanup**: Storage system cleans up crashed client's state
- **Recovery time**: 10-60 seconds (depends on storage timeout configuration)
- **Other nodes**: Continue operations after storage cleanup

#### **Worst Case: Storage System Doesn't Support Lock Cleanup**
- **Timeout protection**: Our 5-second timeout prevents indefinite waiting
- **Automatic fallback**: System switches to jitter coordination strategy
- **No data loss**: Operations continue with randomized delay coordination
- **Manual recovery**: Storage admin may need to restart storage services (rare)

### Q: Can file locks survive system reboots?

**A**: No. File locks are **process-local** and do not persist across system reboots. When a system restarts, all file locks are automatically cleared.

### Q: What if the storage system doesn't support POSIX file locking?

**A**: The system automatically falls back to jitter-based coordination:
1. File lock acquisition times out after 5 seconds
2. NodeManager switches to jitter fallback strategy
3. Operations continue with randomized delays instead of locks
4. No data loss or corruption occurs

### Q: How can I verify that file locking is working correctly?

**A**: Check the coordination strategy in the logs:
```bash
# Look for coordination strategy messages
grep "coordinationStrategy" /var/log/sbd-agent.log

# Expected output for working file locking:
# "coordinationStrategy=file-locking"

# Expected output for fallback:
# "coordinationStrategy=jitter-fallback" or "coordinationStrategy=jitter-only"
```

## Technical Details

### File Locking Implementation

- **Lock Type**: Exclusive (`LOCK_EX`)
- **Timeout**: 5 seconds maximum wait
- **Retry**: No retries on lock timeout (falls back to jitter)
- **Cleanup**: Automatic on process termination or file close

#### Crash Recovery Behavior

**Critical Feature**: POSIX file locks (`flock()`) are **automatically released** when the holding process crashes or exits, preventing permanent deadlocks.

**What happens on different failure scenarios:**
1. **Normal Exit**: `defer` cleanup explicitly releases lock
2. **Process Crash**: Kernel automatically releases all `flock()` locks held by crashed process
3. **SIGKILL**: Kernel cleanup releases locks immediately
4. **Node/Kernel Crash**: Storage system detects disconnection and releases locks
5. **System Reboot**: File locks don't persist across reboots

**Recovery Time**: Other processes can acquire the lock within **milliseconds** of the crashed process exiting.

**Example scenarios:**

**Process Crash:**
```
Timeline:
T0: Process A acquires lock, starts write operation
T1: Process A crashes (OOM kill, segfault, etc.)
T2: Kernel automatically releases Process A's lock
T3: Process B immediately acquires lock and continues operations
Total downtime: < 1 second
```

**Node Crash (e.g., NFS storage):**
```
Timeline:
T0: Node A acquires lock, starts write operation  
T1: Node A crashes (power failure, kernel panic, etc.)
T2: NFS server detects client disconnect (30-60 seconds)
T3: NFS server releases all locks held by Node A
T4: Node B acquires lock and continues operations
Total downtime: 30-60 seconds (NFS-dependent)
```

This automatic cleanup is a fundamental POSIX guarantee that makes file locking safe for critical operations like SBD coordination.

**Why longer recovery times are acceptable for node crashes:**
- **SBD context**: Node crashes are rare events (hardware failure, kernel panic)
- **Crash detection**: Other nodes detect the crash through missing heartbeats
- **Self-fencing**: Crashed node cannot interfere with operations (it's offline)
- **Timeout protection**: 5-second lock timeout prevents indefinite blocking
- **Automatic fallback**: System continues with jitter coordination if needed

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