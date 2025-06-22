# Atomic Slot Assignment Implementation

## Problem

The original slot assignment mechanism had race conditions when multiple nodes started simultaneously:

1. **Node A** and **Node B** both read the same mapping table from SBD device
2. Both see the same available slots 
3. Both assign themselves to the same slot (e.g., slot 5)
4. The last writer wins, causing slot conflicts

## Solution: Compare-and-Swap (CAS) Operations

We implemented atomic operations using version-based Compare-and-Swap semantics:

### Key Changes

1. **Added Version Field**: Each mapping table now has a `uint64` version that increments with every change
2. **Atomic Assignment**: `GetSlotForNode()` now uses atomic read-modify-write operations
3. **Version Checking**: Before writing to SBD device, we verify the version hasn't changed
4. **Retry Logic**: On version conflicts, nodes retry with exponential backoff

### How It Works

```go
// Atomic slot assignment process:
for attempt := 0; attempt < MaxAtomicRetries; attempt++ {
    // 1. Read current mapping table from SBD device
    currentVersion := loadFromDevice()
    
    // 2. Check if node already exists (another node might have assigned it)
    if existingSlot := getExistingSlot(nodeName); existingSlot != 0 {
        return existingSlot
    }
    
    // 3. Assign slot locally
    slot := assignSlotLocally(nodeName)
    
    // 4. Attempt atomic write with version check
    if writeWithVersionCheck(currentVersion) {
        return slot // Success!
    }
    
    // 5. Version mismatch - retry with backoff
    sleep(backoffDelay)
}
```

### Race Condition Prevention

**Before (Race Condition)**:
- Node A reads mapping (version 5)
- Node B reads mapping (version 5)  
- Node A assigns slot 10, writes to device (version 6)
- Node B assigns slot 10, overwrites A's mapping (version 7)
- **Result**: Both nodes think they have slot 10

**After (Atomic Operations)**:
- Node A reads mapping (version 5)
- Node B reads mapping (version 5)
- Node A assigns slot 10, writes with version check (5→6) ✅
- Node B tries to write with version check (expects 5, finds 6) ❌
- Node B retries: reads new mapping (version 6), sees slot 10 taken
- Node B assigns slot 11, writes with version check (6→7) ✅
- **Result**: Node A gets slot 10, Node B gets slot 11

## Configuration

```go
const (
    MaxAtomicRetries = 5                    // Max retry attempts
    AtomicRetryDelay = 100 * time.Millisecond // Base delay between retries
)
```

## Error Handling

- `ErrVersionMismatch`: Indicates concurrent modification, triggers retry
- `ErrMaxRetriesExceeded`: All retry attempts exhausted, indicates high contention

## Benefits

1. **Race-Free**: Eliminates slot assignment conflicts
2. **Self-Healing**: Nodes automatically resolve conflicts via retry
3. **Minimal Overhead**: Only adds version checking, no complex locking
4. **Backwards Compatible**: Existing deployments upgrade seamlessly
5. **Reliable**: Based on proven atomic operation patterns

## Testing

The implementation includes comprehensive tests:
- Atomic operation version tracking
- Version persistence across marshal/unmarshal
- Concurrent access scenarios
- Real-world node name patterns

This ensures the system remains reliable even under high contention scenarios like cluster initialization with many nodes starting simultaneously. 