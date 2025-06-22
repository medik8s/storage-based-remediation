# Endianness Compatibility in SBD Operator

## Overview

The SBD Operator supports multiple CPU architectures with different byte ordering (endianness), requiring careful design to ensure binary data compatibility across the cluster.

## Supported Architectures & Endianness

| Architecture | Endianness | Status |
|--------------|------------|---------|
| `linux/amd64` | Little Endian | ✅ Supported |
| `linux/arm64` | Little Endian* | ✅ Supported |
| `linux/s390x` | **Big Endian** | ✅ Supported |
| `linux/ppc64le` | Little Endian | ✅ Supported |

*ARM64 can be configured as big endian but defaults to little endian

## The Challenge

When multiple nodes with different endianness share an SBD block device, binary data must be interpreted consistently:

```
┌─────────────────────────────────────────────────┐
│                 Shared SBD Device               │
├─────────────────────────────────────────────────┤
│ Slot 0: Node Mapping Table (ALL nodes write)   │
│ Slot 1: Node 1 Heartbeats                      │
│ Slot 2: Node 2 Heartbeats                      │
│ ...                                             │
└─────────────────────────────────────────────────┘
      ↑                                    ↑
 Little Endian                        Big Endian
   (amd64)                             (s390x)
```

## Design Decisions

### 1. **Wire Format: Little Endian**

All binary data stored on the SBD device uses **little-endian format** for consistency:

```go
// SBD Protocol Messages
func Marshal(msg SBDMessageHeader) ([]byte, error) {
    // ALL fields use little-endian
    binary.Write(buf, binary.LittleEndian, msg.Version)
    binary.Write(buf, binary.LittleEndian, msg.NodeID)
    binary.Write(buf, binary.LittleEndian, msg.Timestamp)
    // ...
}

// Node Hash Function  
func (h *NodeHasher) HashNodeName(nodeName string) uint32 {
    hash := sha256.Sum([]byte(h.clusterSalt + nodeName))
    // FIXED: Now uses little-endian consistently
    return binary.LittleEndian.Uint32(hash[:4])
}

// Node Mapping Table Checksums
func (table *NodeMapTable) Marshal() ([]byte, error) {
    checksum := crc32.ChecksumIEEE(jsonData)
    binary.LittleEndian.PutUint32(result[:4], checksum)
    // ...
}
```

### 2. **Architecture-Agnostic Hash Distribution**

Node-to-slot mapping uses **deterministic hashing** that produces the same results regardless of the host architecture:

```go
// Example: Node "worker-1" always maps to the same slot
// regardless of whether it runs on amd64, s390x, or arm64

hasher := NewNodeHasher("production-cluster")
slot := hasher.CalculateSlotID("worker-1", 0)
// slot will be identical across all architectures
```

### 3. **Validation & Testing**

Comprehensive tests ensure endianness compatibility:

- **Hash Consistency Tests**: Verify identical hashes across architectures  
- **Binary Compatibility Tests**: Test extreme values that expose endianness bugs
- **Round-trip Tests**: Marshal/unmarshal data with mixed-endian patterns

## Potential Issues & Mitigations

### ❌ **Before Fix: Inconsistent Endianness**

```go
// PROBLEM: Mixed endianness usage
func (h *NodeHasher) HashNodeName(nodeName string) uint32 {
    hash := sha256.Sum([]byte(nodeName))
    return binary.BigEndian.Uint32(hash[:4])  // ❌ BIG ENDIAN
}

func Marshal(msg SBDMessageHeader) ([]byte, error) {
    binary.Write(buf, binary.LittleEndian, msg.NodeID)  // ❌ LITTLE ENDIAN
}
```

**Result**: s390x and amd64 nodes would generate different slot assignments for the same node name, causing conflicts!

### ✅ **After Fix: Consistent Little Endian**

```go
// SOLUTION: Consistent little endian everywhere
func (h *NodeHasher) HashNodeName(nodeName string) uint32 {
    hash := sha256.Sum([]byte(nodeName))
    return binary.LittleEndian.Uint32(hash[:4])  // ✅ LITTLE ENDIAN
}

func Marshal(msg SBDMessageHeader) ([]byte, error) {
    binary.Write(buf, binary.LittleEndian, msg.NodeID)  // ✅ LITTLE ENDIAN  
}
```

**Result**: All architectures produce identical binary data on the shared SBD device.

## Verification

### Test Mixed Architecture Clusters

To verify endianness compatibility in a mixed environment:

```bash
# Deploy on amd64 control plane
kubectl apply -f config/samples/medik8s_v1alpha1_sbdconfig.yaml

# Deploy agents on different architectures
kubectl patch daemonset sbd-agent -n sbd-system -p '{
  "spec": {
    "template": {
      "spec": {
        "nodeSelector": {"kubernetes.io/arch": "amd64"}
      }
    }
  }
}'

# Verify slot assignments are consistent
kubectl logs -n sbd-system -l app=sbd-agent | grep "slot assignment"
```

### Cross-Architecture Testing

```bash
# Build for all supported architectures
make quay-buildx VERSION=endian-test PLATFORMS=linux/amd64,linux/arm64,linux/s390x,linux/ppc64le

# Test hash consistency across architectures
go test -v ./pkg/sbdprotocol/ -run TestEndianessCompatibility
```

## Migration Considerations

### Upgrading Existing Clusters

If upgrading from a version with endianness bugs:

1. **Check for conflicts**: Verify no duplicate slot assignments exist
2. **Drain big-endian nodes**: Temporarily remove s390x nodes  
3. **Upgrade operator**: Deploy fixed version
4. **Clear node mapping**: Reset slot 0 if corruption detected
5. **Re-add nodes**: Allow automatic slot reassignment

### Rollback Safety

The endianness fix is **forward-compatible** but not backward-compatible:
- **Upgrading**: New version reads old data correctly
- **Downgrading**: Old version may misinterpret new data

## Best Practices

1. **Always test** mixed-architecture deployments before production
2. **Monitor slot assignments** for conflicts during rolling upgrades  
3. **Use consistent cluster naming** across environments
4. **Validate SBD device access** from all architecture types
5. **Keep backups** of node mapping tables before major changes

## Technical Details

### Binary Format Examples

```
Little Endian (0x12345678):
Byte order: 78 56 34 12

Big Endian (0x12345678):  
Byte order: 12 34 56 78
```

The SBD operator now uses little-endian consistently, so all architectures store `0x12345678` as `78 56 34 12` on the shared device.

### Hash Function Verification

```go
// Test vector for endianness verification
nodeName := "test-node"
clusterName := "test-cluster"

hasher := NewNodeHasher(clusterName)
hash := hasher.HashNodeName(nodeName)

// This hash should be identical on all architectures:
// amd64: 0xABCDEF12
// s390x: 0xABCDEF12  (same value!)
// arm64: 0xABCDEF12
// ppc64le: 0xABCDEF12
```

## Conclusion

The endianness compatibility fixes ensure that the SBD Operator works reliably in heterogeneous clusters combining different CPU architectures. All binary data exchanged via the shared SBD device uses a consistent little-endian format, preventing slot conflicts and data corruption in mixed-endianness environments. 