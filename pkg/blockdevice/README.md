# Block Device Package

The `blockdevice` package provides utilities for interacting with raw block devices in Go. This package is specifically designed for SBD (Storage-Based Death) operations that require direct, synchronous access to block devices for reliable fencing in Kubernetes clusters.

## Features

- **Direct Block Device Access**: Opens raw block devices with `O_RDWR` and `O_SYNC` flags for immediate writes
- **Positioned I/O**: Implements `io.ReaderAt` and `io.WriterAt` interfaces for concurrent, positioned operations
- **Synchronous Operations**: Ensures data integrity with explicit sync operations
- **Thread-Safe**: Safe for concurrent use across multiple goroutines
- **Comprehensive Error Handling**: Detailed error messages for debugging and monitoring

## API

### Types

#### `Device`
Represents a raw block device that can be read from and written to.

```go
type Device struct {
    // Contains filtered or unexported fields
}
```

### Functions

#### `Open(path string) (*Device, error)`
Opens a raw block device at the specified path for read/write operations.

**Parameters:**
- `path`: The filesystem path to the block device (e.g., "/dev/sdb1")

**Returns:**
- `*Device`: A new Device instance if successful
- `error`: An error if the device cannot be opened

#### `(*Device) ReadAt(p []byte, off int64) (n int, err error)`
Reads len(p) bytes from the device starting at byte offset off. Implements `io.ReaderAt`.

**Parameters:**
- `p`: The buffer to read data into
- `off`: The byte offset from the beginning of the device to start reading

**Returns:**
- `n`: The number of bytes actually read
- `err`: An error if the read operation fails

#### `(*Device) WriteAt(p []byte, off int64) (n int, err error)`
Writes len(p) bytes to the device starting at byte offset off. Implements `io.WriterAt`.

**Parameters:**
- `p`: The data to write to the device
- `off`: The byte offset from the beginning of the device to start writing

**Returns:**
- `n`: The number of bytes actually written
- `err`: An error if the write operation fails

#### `(*Device) Sync() error`
Flushes any buffered writes to the underlying storage device.

**Returns:**
- `error`: An error if the sync operation fails

#### `(*Device) Close() error`
Closes the block device and releases any associated resources.

**Returns:**
- `error`: An error if the close operation fails

#### `(*Device) Path() string`
Returns the filesystem path of the block device.

#### `(*Device) IsClosed() bool`
Returns true if the device has been closed.

#### `(*Device) String() string`
Returns a string representation of the Device for debugging purposes.

## Usage Examples

### Basic Usage

```go
package main

import (
    "fmt"
    "log"

    "github.com/medik8s/sbd-operator/pkg/blockdevice"
)

func main() {
    // Open a block device
    device, err := blockdevice.Open("/dev/sdb1")
    if err != nil {
        log.Fatalf("Failed to open device: %v", err)
    }
    defer device.Close()

    // Write some data
    data := []byte("SBD message data")
    n, err := device.WriteAt(data, 0)
    if err != nil {
        log.Fatalf("Failed to write: %v", err)
    }
    fmt.Printf("Wrote %d bytes\n", n)

    // Ensure data is flushed to disk
    if err := device.Sync(); err != nil {
        log.Fatalf("Failed to sync: %v", err)
    }

    // Read back the data
    buf := make([]byte, len(data))
    n, err = device.ReadAt(buf, 0)
    if err != nil {
        log.Fatalf("Failed to read: %v", err)
    }
    fmt.Printf("Read %d bytes: %s\n", n, string(buf[:n]))
}
```

### SBD Message Operations

```go
func writeSBDMessage(devicePath string, message []byte, offset int64) error {
    device, err := blockdevice.Open(devicePath)
    if err != nil {
        return fmt.Errorf("failed to open SBD device: %w", err)
    }
    defer device.Close()

    // Write the SBD message
    if _, err := device.WriteAt(message, offset); err != nil {
        return fmt.Errorf("failed to write SBD message: %w", err)
    }

    // Critical: ensure the message is immediately written to disk
    if err := device.Sync(); err != nil {
        return fmt.Errorf("failed to sync SBD message: %w", err)
    }

    return nil
}

func readSBDMessage(devicePath string, messageSize int, offset int64) ([]byte, error) {
    device, err := blockdevice.Open(devicePath)
    if err != nil {
        return nil, fmt.Errorf("failed to open SBD device: %w", err)
    }
    defer device.Close()

    buf := make([]byte, messageSize)
    n, err := device.ReadAt(buf, offset)
    if err != nil && err != io.EOF {
        return nil, fmt.Errorf("failed to read SBD message: %w", err)
    }

    return buf[:n], nil
}
```

### Concurrent Operations

```go
func concurrentSBDOperations(devicePath string) error {
    device, err := blockdevice.Open(devicePath)
    if err != nil {
        return err
    }
    defer device.Close()

    var wg sync.WaitGroup
    
    // Multiple goroutines can safely read/write to different offsets
    for i := 0; i < 10; i++ {
        wg.Add(1)
        go func(id int) {
            defer wg.Done()
            
            offset := int64(id * 512) // 512-byte sectors
            data := fmt.Sprintf("Message from goroutine %d", id)
            
            // Write operation
            if _, err := device.WriteAt([]byte(data), offset); err != nil {
                log.Printf("Write failed for goroutine %d: %v", id, err)
                return
            }
            
            // Read operation
            buf := make([]byte, len(data))
            if _, err := device.ReadAt(buf, offset); err != nil {
                log.Printf("Read failed for goroutine %d: %v", id, err)
                return
            }
        }(i)
    }
    
    wg.Wait()
    return device.Sync() // Final sync to ensure all writes are committed
}
```

## Important Considerations

### SBD Operations
- **Synchronous I/O**: The package opens devices with `O_SYNC` flag, ensuring writes are immediately committed to storage
- **Data Integrity**: Always call `Sync()` after critical writes to guarantee data persistence
- **Error Handling**: Check all return values; partial writes are treated as errors
- **Device Permissions**: Ensure proper permissions to access block devices (typically requires root or specific group membership)

### Performance
- **Direct Access**: Operations bypass filesystem caches for maximum reliability
- **Positioned I/O**: `ReadAt` and `WriteAt` don't affect file position, enabling concurrent access
- **Synchronous Writes**: Write performance is slower due to `O_SYNC`, but provides data integrity guarantees

### Security
- **Privileged Access**: Block device operations typically require elevated privileges
- **Data Safety**: Always validate offsets and data sizes to prevent corruption
- **Resource Management**: Always close devices to prevent resource leaks

## Testing

The package includes comprehensive tests that can be run with:

```bash
go test ./pkg/blockdevice -v
```

For benchmarks:

```bash
go test ./pkg/blockdevice -bench=. -benchmem
```

## Error Handling

All operations return detailed errors that include:
- The device path for context
- The specific operation that failed
- The underlying system error

Example error messages:
- `failed to open block device "/dev/sdb1": permission denied`
- `failed to write to device "/dev/sdb1" at offset 1024: input/output error`
- `device "/dev/sdb1" is closed`

## Thread Safety

The `Device` type is safe for concurrent use:
- `ReadAt` and `WriteAt` operations are thread-safe
- Multiple goroutines can perform positioned I/O concurrently
- `Sync()` and `Close()` operations are also thread-safe

However, be cautious about:
- Closing a device while other operations are in progress
- Coordinating writes to overlapping regions between goroutines

## License

This package is part of the SBD Operator project and is licensed under the Apache License 2.0. 
