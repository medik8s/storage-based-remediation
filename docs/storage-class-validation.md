# Storage Class Validation

## Overview

The SBD operator now includes automatic storage class validation to ensure that the specified storage class supports the `ReadWriteMany` (RWX) access mode required for SBD shared storage coordination.

## Why This Matters

SBD requires shared storage that can be accessed by multiple nodes simultaneously to coordinate fencing decisions. Storage classes that only support `ReadWriteOnce` (RWO) access mode, such as AWS EBS volumes, are not compatible with SBD's shared storage requirements.

## How It Works

The SBD controller validates storage classes in two ways:

### 1. Known Provisioner Detection

The controller maintains a list of known RWX-compatible provisioners:

**Compatible Provisioners:**

- `efs.csi.aws.com` (AWS EFS)
- `file.csi.azure.com` (Azure Files)
- `filestore.csi.storage.gke.io` (GCP Filestore)
- `nfs.csi.k8s.io` (NFS CSI)
- `cephfs.csi.ceph.com` (CephFS)
- `openshift-storage.cephfs.csi.ceph.com` (OpenShift Data Foundation CephFS)
- `gluster.org/glusterfs` (GlusterFS)
- Various NFS provisioners

**Incompatible Provisioners:**

- `ebs.csi.aws.com` (AWS EBS)
- `disk.csi.azure.com` (Azure Disk)
- `pd.csi.storage.gke.io` (GCP Persistent Disk)

### 2. Runtime Testing

For unknown provisioners, the controller creates a temporary PVC with `ReadWriteMany` access mode to test compatibility. The test PVC is automatically cleaned up regardless of the test outcome.

## Configuration

No additional configuration is required. The validation happens automatically when you specify a `sharedStorageClass` in your SBDConfig:

```yaml
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDConfig
metadata:
  name: sbd-config
spec:
  sharedStorageClass: "efs-csi"  # This will be validated
  sbdWatchdogPath: "/dev/watchdog"
```

## Error Handling

If an incompatible storage class is detected, the controller will:

1. **Emit a Warning Event** with details about the incompatibility
2. **Prevent PVC Creation** to avoid resource waste
3. **Log the Error** with specific details about why the storage class is incompatible
4. **Requeue the Reconciliation** with exponential backoff

Example error message:

```text
StorageClass 'gp3-csi' does not support ReadWriteMany access mode required for SBD shared storage
```

## Compatible Storage Solutions

### AWS

- **EFS (Elastic File System)**: Use `efs.csi.aws.com` provisioner
- **Third-party NFS**: Use NFS CSI drivers

### Azure

- **Azure Files**: Use `file.csi.azure.com` provisioner
- **Third-party NFS**: Use NFS CSI drivers

### GCP

- **Filestore**: Use `filestore.csi.storage.gke.io` provisioner
- **Third-party NFS**: Use NFS CSI drivers

### On-Premises

- **NFS**: Use various NFS CSI drivers
- **CephFS**: Use `cephfs.csi.ceph.com` provisioner
- **GlusterFS**: Use GlusterFS provisioners

## Testing

The SBD operator includes comprehensive tests for storage class validation:

### Unit Tests

- Tests for known provisioner detection
- Tests for temporary PVC creation and cleanup
- Tests for proper error handling and event emission

### E2E Tests

- Tests with compatible storage classes (EFS, NFS)
- Tests with incompatible storage classes (EBS, Azure Disk)
- Tests with non-existent storage classes

## Troubleshooting

### Common Issues

1. **"StorageClass not found"**
   - Ensure the storage class exists in the cluster
   - Check the spelling of the storage class name

2. **"Does not support ReadWriteMany"**
   - The specified storage class only supports ReadWriteOnce
   - Switch to a RWX-compatible storage class (see list above)

3. **"Unknown provisioner"**
   - The controller will test unknown provisioners automatically
   - If testing fails, the provisioner likely doesn't support RWX

### Debugging

To debug storage class validation issues:

1. **Check Events:**

   ```bash
   kubectl get events -n <namespace> --field-selector involvedObject.name=<sbdconfig-name>
   ```

2. **Check Controller Logs:**

   ```bash
   kubectl logs -n sbd-operator-system deployment/sbd-operator-controller-manager
   ```

3. **Check PVC Status:**

   ```bash
   kubectl get pvc -n <namespace>
   kubectl describe pvc <pvc-name> -n <namespace>
   ```

## Migration Guide

If you're currently using an incompatible storage class:

1. **Create a compatible storage class** (e.g., EFS, NFS)
2. **Update your SBDConfig** to use the new storage class
3. **Wait for the controller** to reconcile and create the new PVC
4. **Clean up the old PVC** if necessary

Example migration from EBS to EFS:

```yaml
# Before (incompatible)
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDConfig
metadata:
  name: sbd-config
spec:
  sharedStorageClass: "gp3-csi"  # EBS - ReadWriteOnce only
  sbdWatchdogPath: "/dev/watchdog"

---

# After (compatible)
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDConfig
metadata:
  name: sbd-config
spec:
  sharedStorageClass: "efs-csi"  # EFS - ReadWriteMany compatible
  sbdWatchdogPath: "/dev/watchdog"
```

## Best Practices

1. **Use cloud-native shared storage** (EFS, Azure Files, Filestore) when available
2. **Test storage class compatibility** before deploying to production
3. **Monitor events and logs** during initial deployment
4. **Document your storage class choice** for team knowledge
5. **Plan for storage class migration** if needed

## Implementation Details

The storage class validation is implemented in the SBD controller's reconciliation loop:

1. **Early Validation**: Storage class validation happens before PVC creation
2. **Fail Fast**: Invalid storage classes are rejected immediately
3. **Automatic Cleanup**: Temporary test PVCs are cleaned up automatically
4. **Retry Logic**: Transient errors are retried with exponential backoff
5. **Event Emission**: Users get clear feedback about validation failures

This ensures that SBD configurations are validated early and users get clear feedback about any issues.
