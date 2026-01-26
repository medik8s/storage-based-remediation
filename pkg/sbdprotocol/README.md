# SBD Protocol Package

The `sbdprotocol` package implements the SBD (Storage-Based Death) protocol message format for inter-node communication in cluster environments. This package provides a standardized way to create, marshal, and unmarshal SBD messages for cluster fencing operations.

## Overview

The SBD protocol is used for cluster node fencing and health monitoring. It supports two main message types:
- **Heartbeat Messages**: Indicate that a node is alive and functioning
- **Fence Messages**: Request that a specific node be fenced (shut down)

## Features

- Binary message marshaling/unmarshaling using little-endian encoding
- CRC32 checksum validation for message integrity
- Support for heartbeat and fence message types
- Comprehensive error handling and validation
- Thread-safe operations
- High performance with minimal allocations

## Message Format

### SBD Message Header (33 bytes)
```
+--------+--------+------+--------+-----------+----------+----------+
| Magic  | Version| Type | NodeID | Timestamp | Sequence | Checksum |
| 8 bytes| 2 bytes|1 byte|2 bytes | 8 bytes   | 8 bytes  | 4 bytes  |
+--------+--------+------+--------+-----------+----------+----------+
```

### Fence Message (36 bytes total)
```
+--------+--------+------+--------+-----------+----------+----------+--------+--------+
| Header (33 bytes)                                                  | Target | Reason |
|                                                                     |2 bytes |1 byte  |
+--------+--------+------+--------+-----------+----------+----------+--------+--------+
```

## Usage Examples

### Creating and Marshaling a Heartbeat Message

```go
package main

import (
    "fmt"
    "github.com/medik8s/sbd-operator/pkg/sbdprotocol"
)

func main() {
    // Create a heartbeat message
    nodeID := uint16(1)
    sequence := uint64(100)
    
    heartbeat := sbdprotocol.SBDHeartbeatMessage{
        Header: sbdprotocol.NewHeartbeat(nodeID, sequence),
    }
    
    // Marshal the message to bytes
    data, err := sbdprotocol.MarshalHeartbeat(heartbeat)
    if err != nil {
        panic(fmt.Sprintf("Failed to marshal heartbeat: %v", err))
    }
    
    fmt.Printf("Marshaled heartbeat: %d bytes\n", len(data))
    
    // Unmarshal the message
    unmarshaled, err := sbdprotocol.UnmarshalHeartbeat(data)
    if err != nil {
        panic(fmt.Sprintf("Failed to unmarshal heartbeat: %v", err))
    }
    
    fmt.Printf("Node ID: %d, Sequence: %d\n", 
        unmarshaled.Header.NodeID, unmarshaled.Header.Sequence)
}
```

### Creating and Marshaling a Fence Message

```go
package main

import (
    "fmt"
    "github.com/medik8s/sbd-operator/pkg/sbdprotocol"
)

func main() {
    // Create a fence message
    nodeID := uint16(1)
    targetNodeID := uint16(2)
    sequence := uint64(200)
    reason := sbdprotocol.FENCE_REASON_HEARTBEAT_TIMEOUT
    
    fence := sbdprotocol.SBDFenceMessage{
        Header:       sbdprotocol.NewFence(nodeID, targetNodeID, sequence, reason),
        TargetNodeID: targetNodeID,
        Reason:       reason,
    }
    
    // Marshal the message to bytes
    data, err := sbdprotocol.MarshalFence(fence)
    if err != nil {
        panic(fmt.Sprintf("Failed to marshal fence: %v", err))
    }
    
    fmt.Printf("Marshaled fence message: %d bytes\n", len(data))
    
    // Unmarshal the message
    unmarshaled, err := sbdprotocol.UnmarshalFence(data)
    if err != nil {
        panic(fmt.Sprintf("Failed to unmarshal fence: %v", err))
    }
    
    fmt.Printf("Fence request: Node %d -> Target %d, Reason: %s\n",
        unmarshaled.Header.NodeID, 
        unmarshaled.TargetNodeID,
        sbdprotocol.GetFenceReasonName(unmarshaled.Reason))
}
```

### Direct Header Marshaling

```go
package main

import (
    "fmt"
    "github.com/medik8s/sbd-operator/pkg/sbdprotocol"
)

func main() {
    // Create a header directly
    header := sbdprotocol.NewHeartbeat(42, 123)
    
    // Marshal just the header
    data, err := sbdprotocol.Marshal(header)
    if err != nil {
        panic(fmt.Sprintf("Failed to marshal header: %v", err))
    }
    
    // Unmarshal the header
    unmarshaled, err := sbdprotocol.Unmarshal(data)
    if err != nil {
        panic(fmt.Sprintf("Failed to unmarshal header: %v", err))
    }
    
    fmt.Printf("Message Type: %s\n", 
        sbdprotocol.GetMessageTypeName(unmarshaled.Type))
    fmt.Printf("Node ID: %d\n", unmarshaled.NodeID)
    fmt.Printf("Sequence: %d\n", unmarshaled.Sequence)
    fmt.Printf("Checksum: 0x%08x\n", unmarshaled.Checksum)
}
```

## Constants

### Message Types
- `SBD_MSG_TYPE_HEARTBEAT` (0x01): Heartbeat message
- `SBD_MSG_TYPE_FENCE` (0x02): Fence message

### Fence Reasons
- `FENCE_REASON_HEARTBEAT_TIMEOUT` (0x01): Fencing due to missed heartbeats
- `FENCE_REASON_MANUAL` (0x02): Manual fencing request
- `FENCE_REASON_SPLIT_BRAIN` (0x03): Fencing due to split-brain detection
- `FENCE_REASON_RESOURCE_CONFLICT` (0x04): Fencing due to resource conflicts

### Protocol Constants
- `SBD_MAGIC`: Magic string "SBDMSG01" for message identification
- `SBD_HEADER_SIZE`: Size of message header (33 bytes)
- `SBD_SLOT_SIZE`: Size of a single SBD slot on device (512 bytes)
- `SBD_MAX_NODES`: Maximum number of nodes supported (255)

## Validation Functions

The package provides several validation and utility functions:

```go
// Check if message type is valid
if sbdprotocol.IsValidMessageType(msgType) {
    // Process message
}

// Check if node ID is valid
if sbdprotocol.IsValidNodeID(nodeID) {
    // Use node ID
}

// Get human-readable names
typeName := sbdprotocol.GetMessageTypeName(msgType)
reasonName := sbdprotocol.GetFenceReasonName(reason)
```

## Performance

Based on benchmark results on Apple M1 Pro:
- **Marshal**: ~269 ns/op, 152 B/op, 9 allocs/op
- **Unmarshal**: ~296 ns/op, 136 B/op, 9 allocs/op
- **Checksum**: ~122 ns/op, 0 B/op, 0 allocs/op
- **Round Trip**: ~590 ns/op, 288 B/op, 18 allocs/op

## Error Handling

The package provides detailed error messages for various failure conditions:
- Invalid magic string
- Checksum validation failures
- Data too short for message type
- Invalid message type for operation
- Binary encoding/decoding errors

All functions return descriptive errors that can be used for debugging and logging.

## Thread Safety

All functions in this package are thread-safe and can be called concurrently from multiple goroutines without external synchronization.

## Integration with SBD Agent

This package is designed to be used by the SBD Agent for:
1. Creating heartbeat messages to write to shared storage
2. Reading and validating messages from other nodes
3. Creating fence requests when node failures are detected
4. Parsing fence requests directed at the local node

Example integration:
```go
// In SBD Agent - sending heartbeat
nodeID := getCurrentNodeID()
sequence := getNextSequence()
heartbeat := sbdprotocol.SBDHeartbeatMessage{
    Header: sbdprotocol.NewHeartbeat(nodeID, sequence),
}
data, _ := sbdprotocol.MarshalHeartbeat(heartbeat)
writeToSBDDevice(data, nodeID)

// Reading messages from other nodes
data := readFromSBDDevice(otherNodeID)
message, err := sbdprotocol.UnmarshalHeartbeat(data)
if err == nil {
    updateNodeStatus(otherNodeID, message.Header.Timestamp)
}
``` 
