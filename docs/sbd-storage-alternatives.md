# SBD Storage Alternatives for Cache Coherency

This document provides alternatives to the EFS CSI driver when you need explicit control over NFS cache coherency, synchronous operations, and server-side locking for SBD (STONITH Block Device) deployments.

## Problem Statement

The EFS CSI driver in `efs-ap` (Access Point) mode:
- ❌ Does not support standard NFS mount options like `cache=none`, `sync`, `local_lock=none`
- ❌ Handles cache coherency internally without external control
- ❌ Cannot guarantee the strict consistency requirements needed for SBD clustering

For **SBD clustering**, these explicit controls are **critical** for:
- **Cache coherency**: Ensuring all nodes see writes immediately
- **Synchronous operations**: Guaranteeing writes are committed before returning
- **Server-side locking**: Coordinating access across cluster nodes

## Recommended Alternatives

### 1. Standard NFS CSI Driver (Recommended)

**Best choice** for explicit NFS mount option control.

#### Installation

```bash
# Install the Standard NFS CSI Driver
kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/csi-driver-nfs/master/deploy/install-driver.yaml

# Verify installation
kubectl get pods -n kube-system -l app=csi-nfs
```

#### StorageClass Configuration

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: sbd-nfs-coherent
provisioner: nfs.csi.k8s.io
parameters:
  server: fs-03af5af5e88a8d0d6.efs.ap-southeast-2.amazonaws.com
  share: /sbd
mountOptions:
  # Critical SBD cache coherency options
  - nfsvers=4.1        # Use NFSv4.1
  - cache=none         # ✅ Disable client-side caching
  - sync               # ✅ Force synchronous operations  
  - local_lock=none    # ✅ Use server-side locking
  
  # Performance and reliability
  - hard               # Hard mount for reliability
  - timeo=600          # 60 second timeout
  - retrans=2          # 2 retries before recovery
  - rsize=1048576      # 1MB read size
  - wsize=1048576      # 1MB write size
```

### 2. NFS Subdir External Provisioner

**Good choice** for dynamic subdirectory provisioning with mount control.

#### Installation

```bash
# Install via Helm
helm repo add nfs-subdir-external-provisioner https://kubernetes-sigs.github.io/nfs-subdir-external-provisioner/
helm install nfs-subdir-external-provisioner nfs-subdir-external-provisioner/nfs-subdir-external-provisioner \
    --set nfs.server=fs-03af5af5e88a8d0d6.efs.ap-southeast-2.amazonaws.com \
    --set nfs.path=/sbd \
    --set storageClass.mountOptions="{nfsvers=4.1,cache=none,sync,local_lock=none,hard}"
```

#### Manual Installation

```yaml
# See: https://github.com/kubernetes-sigs/nfs-subdir-external-provisioner/tree/master/deploy
```

### 3. NFS Ganesha Server + Provisioner

**Advanced choice** for deploying your own NFS server with full control.

```bash
# Install via repository
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/nfs-ganesha-server-and-external-provisioner/master/deploy/kubernetes/deployment.yaml
```

## Migration Steps

### Step 1: Install Alternative Driver

Choose one of the alternatives above and install it.

### Step 2: Create New StorageClass

Use the example configurations to create a StorageClass with proper mount options.

### Step 3: Test Cache Coherency

```bash
# Apply the test configuration
kubectl apply -f examples/alternative-nfs-storage.yaml

# Check mount options
kubectl exec test-sbd-nfs-mount -- mount | grep /mnt/sbd

# Expected output should show:
# server:/sbd on /mnt/sbd type nfs4 (rw,sync,hard,cache=none,local_lock=none,...)
```

### Step 4: Update SBD Configuration

```yaml
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDConfig
metadata:
  name: sbd-config-coherent
spec:
  # ... other settings ...
  sharedStorageClass: sbd-nfs-coherent  # Use the new StorageClass
```

### Step 5: Migrate Existing SBD

```bash
# 1. Backup current SBD configuration
kubectl get sbdconfig -o yaml > sbd-backup.yaml

# 2. Delete current SBD
kubectl delete sbdconfig test-sbd-config

# 3. Apply new SBD configuration
kubectl apply -f examples/alternative-nfs-storage.yaml
```

## Validation

### Verify Mount Options

```bash
# Check that mount options are applied correctly
kubectl exec <sbd-agent-pod> -- mount | grep /dev/sbd

# Should show: cache=none, sync, local_lock=none
```

### Test Cache Coherency

```bash
# From pod 1
echo "test-data-$(date)" > /dev/sbd/test-coherency

# From pod 2 (should see the data immediately)
cat /dev/sbd/test-coherency
```

### Test File Locking

```bash
# Should work without errors
kubectl exec <sbd-agent-pod> -- flock /dev/sbd/test-lock echo "Locking works"
```

## Performance Considerations

### Mount Option Impact

| Option | Performance Impact | SBD Requirement |
|--------|-------------------|-----------------|
| `cache=none` | ⚠️ Reduces read performance | ✅ **Critical** for coherency |
| `sync` | ⚠️ Reduces write performance | ✅ **Critical** for durability |
| `local_lock=none` | ➖ Minimal impact | ✅ **Critical** for coordination |

### Recommendations

1. **Use larger `rsize`/`wsize`** to offset cache=none performance impact
2. **Monitor I/O patterns** to ensure acceptable performance
3. **Consider faster storage** (NVMe-backed EFS) for better baseline performance
4. **Test thoroughly** in your environment before production deployment

## Troubleshooting

### Common Issues

1. **Mount failures**: Check that the NFS server (EFS) is accessible
2. **Permission denied**: Ensure EFS access points allow the mount
3. **Performance issues**: Monitor with `iostat` and adjust mount options
4. **Lock timeouts**: Increase `timeo` parameter if needed

### Debug Commands

```bash
# Check NFS mount details
mount -t nfs4

# Monitor NFS statistics  
nfsstat -c

# Test NFS connectivity
showmount -e <efs-dns-name>

# Check CSI driver logs
kubectl logs -n kube-system -l app=csi-nfs -f
```

## Conclusion

For **SBD clustering**, the **Standard NFS CSI Driver** provides the explicit cache coherency controls you need:

- ✅ **`cache=none`** - Disables client-side caching
- ✅ **`sync`** - Forces synchronous operations  
- ✅ **`local_lock=none`** - Uses server-side locking

This ensures proper SBD operation with guaranteed consistency across cluster nodes, something that the EFS CSI driver in `efs-ap` mode cannot provide. 