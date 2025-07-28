# OpenShift Data Foundation Setup Tool for SBD

This tool automates the deployment and configuration of OpenShift Data Foundation (ODF) with CephFS storage optimized for SBD (STONITH Block Device) coordination.

## Overview

The `setup-odf-storage` tool provides:
- **Automated ODF Deployment**: Installs OpenShift Data Foundation operator and creates storage cluster
- **CephFS Configuration**: Sets up CephFS with ReadWriteMany (RWX) support for SBD coordination  
- **Cache Coherency**: Configures mount options for reliable inter-node coordination
- **SBD Integration**: Creates StorageClass ready for use with SBDConfig resources

## Features

### OpenShift Data Foundation Components
- **Ceph Storage Cluster**: Distributed storage backend with high availability
- **CephFS**: Distributed file system with full POSIX locking support
- **CSI Integration**: Kubernetes CSI driver for dynamic provisioning
- **Storage Classes**: Optimized for SBD cache coherency requirements

### SBD Optimization
- **ReadWriteMany Support**: Multi-node access for SBD coordination
- **POSIX File Locking**: Full distributed locking for SBD heartbeat coordination
- **Cache Coherency**: Configurable coherency modes for different SBD requirements
- **Real-time Consistency**: Changes immediately visible across all nodes

## Prerequisites

### Cluster Requirements
- OpenShift 4.8+ or Kubernetes 1.21+ with OLM (Operator Lifecycle Manager)
- At least 3 worker nodes for storage replication
- Cluster administrator privileges
- Adequate storage capacity on worker nodes (minimum 2Ti recommended)

### Node Requirements
- Worker nodes with local storage devices or sufficient ephemeral storage
- Network connectivity between all storage nodes
- CPU and memory resources for Ceph daemons

### Tools Required
- `kubectl` or `oc` CLI configured
- Access to OpenShift/Kubernetes cluster

## Quick Start

### Basic Setup

```bash
# Standard ODF setup with defaults
./setup-odf-storage

# This creates:
# - ODF operator installation
# - StorageCluster with 3 replicas and 2Ti storage
# - CephFS StorageClass named 'sbd-cephfs'
```

### Custom Configuration

```bash
# Custom cluster with specific requirements
./setup-odf-storage \
  --cluster-name="my-sbd-cluster" \
  --storage-size="4Ti" \
  --replica-count=5 \
  --storage-class-name="custom-sbd-cephfs"
```

### Advanced Options

```bash
# Enable encryption and aggressive coherency
./setup-odf-storage \
  --enable-encryption \
  --aggressive-coherency \
  --storage-size="1Ti"
```

## Command Line Options

### Core Configuration
- `--storage-class-name`: CephFS StorageClass name (default: "sbd-cephfs")
- `--cluster-name`: ODF StorageCluster name (default: "ocs-storagecluster")
- `--namespace`: Installation namespace (default: "openshift-storage")

### Storage Configuration  
- `--storage-size`: Total storage size for cluster (default: "2Ti")
- `--replica-count`: Number of storage replicas (default: 3)
- `--enable-encryption`: Enable storage encryption (default: false)
- `--enable-device-set`: Enable automatic device set creation (default: true)

### Cache Coherency
- `--aggressive-coherency`: Enable strict cache coherency for SBD (default: false)

### Behavior Control
- `--dry-run`: Preview changes without executing
- `--cleanup`: Remove created ODF resources
- `--update-mode`: Force update/recreation of resources
- `--verbose`: Enable detailed logging

## Cache Coherency Modes

### Standard Mode (Default)
```bash
./setup-odf-storage
```
- **Mount Options**: `_netdev` (network device dependency)
- **Use Case**: General SBD deployments with good performance
- **Performance**: High throughput, low latency
- **Coherency**: Native CephFS cache coherency

### Aggressive Mode
```bash
./setup-odf-storage --aggressive-coherency
```
- **Mount Options**: `cache=strict`, `sync`, `recover_session=clean`, `_netdev`
- **Use Case**: Strict SBD coordination requirements
- **Performance**: Lower throughput, higher latency
- **Coherency**: Disabled client caching, synchronous operations

## Usage Examples

### Development Environment
```bash
# Minimal setup for testing
./setup-odf-storage \
  --storage-size="512Gi" \
  --replica-count=3
```

### Production Environment
```bash
# Production setup with encryption
./setup-odf-storage \
  --storage-size="10Ti" \
  --replica-count=5 \
  --enable-encryption \
  --cluster-name="prod-sbd-storage"
```

### High Availability Setup
```bash
# Maximum reliability configuration
./setup-odf-storage \
  --storage-size="8Ti" \
  --replica-count=5 \
  --enable-encryption \
  --aggressive-coherency \
  --storage-class-name="ha-sbd-cephfs"
```

## Integration with SBD

### SBDConfig Example
Once the tool completes, use the created StorageClass in your SBDConfig:

```yaml
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDConfig
metadata:
  name: sbd-with-odf
spec:
  sharedStorageClass: "sbd-cephfs"  # StorageClass created by this tool
  sbdWatchdogPath: "/dev/watchdog"
  watchdogTimeout: "60s"
  staleNodeTimeout: "1h"
```

### Verification
```bash
# Check StorageClass
kubectl get storageclass sbd-cephfs

# Check SBD integration
kubectl apply -f your-sbdconfig.yaml
kubectl get sbdconfig -o wide
```

## Troubleshooting

### Common Issues

#### ODF Operator Installation Fails
```bash
# Check operator status
kubectl get csv -n openshift-storage
kubectl get subscription -n openshift-storage

# Check logs
kubectl logs -n openshift-storage deployment/ocs-operator
```

#### ODF Operator Timeout ("ODF operator failed to become ready: timeout waiting for ODF operator to be ready")

This is the most common issue when installing ODF. The enhanced tool now provides detailed debugging information.

**Root Causes:**
1. **Slow operator installation**: ODF operator can take 10-15 minutes to install on some clusters
2. **Subscription issues**: OperatorHub connectivity or subscription configuration problems
3. **Resource constraints**: Insufficient cluster resources for operator pods
4. **Image pull issues**: Problems pulling ODF operator images

**Debugging Steps:**

1. **Check the enhanced debug output**: The tool now provides detailed subscription and CSV status information when timeout occurs.

2. **Manually check subscription status**:
```bash
# Check subscription
kubectl get subscription -n openshift-storage odf-operator -o yaml

# Check install plan
kubectl get installplan -n openshift-storage

# Check catalog source
kubectl get catalogsource -n openshift-marketplace
```

3. **Check CSV (ClusterServiceVersion) status**:
```bash
# List all CSVs in openshift-storage namespace
kubectl get csv -n openshift-storage

# Check specific ODF CSV status
kubectl get csv -n openshift-storage -l operators.coreos.com/odf-operator.openshift-storage

# Describe CSV for detailed status
kubectl describe csv -n openshift-storage <csv-name>
```

4. **Check operator pod status**:
```bash
# Check if operator pods are running
kubectl get pods -n openshift-storage

# Check operator logs
kubectl logs -n openshift-storage deployment/ocs-operator
kubectl logs -n openshift-storage deployment/odf-console
```

5. **Check cluster resources**:
```bash
# Check node resources
kubectl top nodes

# Check if nodes have sufficient storage
kubectl get nodes -o wide
```

**Solutions:**

1. **Increase timeout**: The tool now uses a 15-minute timeout (increased from 10 minutes)

2. **Manual operator installation**: If automatic installation fails, install manually:
```bash
# Create namespace
kubectl create namespace openshift-storage

# Install operator manually
cat <<EOF | kubectl apply -f -
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: openshift-storage-operatorgroup
  namespace: openshift-storage
spec:
  targetNamespaces:
  - openshift-storage
---
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: odf-operator
  namespace: openshift-storage
spec:
  channel: stable-4.15
  installPlanApproval: Automatic
  name: odf-operator
  source: redhat-operators
  sourceNamespace: openshift-marketplace
EOF

# Wait for CSV to be ready
kubectl wait --for=condition=Succeeded csv -n openshift-storage -l operators.coreos.com/odf-operator.openshift-storage --timeout=20m
```

3. **Check cluster prerequisites**:
```bash
# Ensure cluster has enough nodes
kubectl get nodes --no-headers | wc -l

# Check StorageClass for device sets
kubectl get storageclass

# Verify cluster can pull images
kubectl run test-pod --image=registry.redhat.io/ubi8/ubi:latest --restart=Never --rm -it -- echo "Image pull test"
```

4. **Alternative channel**: Try different operator channel:
```bash
# Delete existing subscription
kubectl delete subscription odf-operator -n openshift-storage

# Use stable-4.14 channel
./bin/setup-odf-storage --dry-run  # Check configuration first
```

#### StorageCluster Not Ready
```bash
# Check cluster status
kubectl get storagecluster -n openshift-storage -o yaml

# Check Ceph cluster health
kubectl exec -n openshift-storage deployment/rook-ceph-tools -- ceph status
```

#### StorageClass Test Fails
```bash
# Check CSI driver
kubectl get csidriver

# Test manual PVC creation
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-cephfs
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 1Gi
  storageClassName: sbd-cephfs
EOF
```

#### AWS Integration Issues

If you're experiencing AWS-related issues:

1. **Permission errors**: The tool will display the exact IAM policy needed
2. **Instance storage analysis fails**: Check if instances are properly tagged
3. **Volume attachment fails**: Verify instance limits and available device names

```bash
# Test AWS permissions manually
aws ec2 describe-instances --max-results 5
aws sts get-caller-identity

# Run tool with AWS integration disabled
./bin/setup-odf-storage --enable-aws-integration=false
```

### Cleanup and Recovery
```bash
# Clean up all resources
./bin/setup-odf-storage --cleanup

# Force cleanup if stuck
kubectl delete storagecluster ocs-storagecluster -n openshift-storage
kubectl delete storageclass sbd-cephfs

# Manual operator cleanup
kubectl delete subscription odf-operator -n openshift-storage
kubectl delete csv -n openshift-storage -l operators.coreos.com/odf-operator.openshift-storage
```

### Performance Issues

#### Slow Operator Installation
- **Normal**: ODF operator installation can take 10-15 minutes
- **Check progress**: Use the enhanced debug output to monitor installation
- **Resource constraints**: Ensure cluster has adequate CPU/memory

#### Slow Storage Provisioning
- **Storage performance**: Use faster EBS volume types (io1, io2)
- **Network performance**: Ensure adequate network bandwidth between nodes
- **Ceph configuration**: Check Ceph cluster health and performance

### Getting Help

#### Log Collection
```bash
# Collect ODF operator logs
kubectl logs -n openshift-storage -l app.kubernetes.io/name=odf-operator --tail=100

# Collect storage cluster logs  
kubectl logs -n openshift-storage -l app=rook-ceph-operator --tail=100

# Collect subscription and CSV information
kubectl get subscription,csv -n openshift-storage -o yaml > odf-debug.yaml
```

#### Tool Debugging
```bash
# Enable verbose output
./bin/setup-odf-storage --verbose

# Use dry-run to check configuration
./bin/setup-odf-storage --dry-run --verbose

# Check specific issues
kubectl get events -n openshift-storage --sort-by='.lastTimestamp'
```

## Security Considerations

### Encryption
- Enable encryption for sensitive environments: `--enable-encryption`
- Encryption is applied at the storage layer (Ceph OSD level)
- Keys are managed automatically by ODF

### Network Security
- CephFS traffic is contained within cluster network
- CSI operations use service accounts with minimal required permissions
- Storage network can be isolated using NetworkPolicies

### Access Control
- StorageClass uses Kubernetes RBAC for access control
- PVCs inherit namespace-level permissions
- SBD agents run with restricted service accounts

## Performance Tuning

### Storage Performance
- Use SSD storage for better IOPS performance
- Configure appropriate replica count for availability vs performance
- Monitor Ceph cluster health and performance metrics

### Mount Options
- Standard mode: Better performance, good coherency
- Aggressive mode: Maximum coherency, lower performance
- Choose based on SBD coordination requirements

## Monitoring

### ODF Metrics
```bash
# Check ODF cluster health
kubectl exec -n openshift-storage deployment/rook-ceph-tools -- ceph health

# View storage utilization
kubectl exec -n openshift-storage deployment/rook-ceph-tools -- ceph df
```

### Storage Performance
```bash
# Monitor CephFS performance
kubectl exec -n openshift-storage deployment/rook-ceph-tools -- ceph fs status

# Check CSI driver metrics
kubectl get pods -n openshift-storage | grep csi
```

## Support

For issues with:
- **ODF Installation**: Check OpenShift Data Foundation documentation
- **SBD Integration**: Refer to SBD operator documentation  
- **Storage Performance**: Monitor Ceph cluster health and logs
- **Tool Bugs**: Submit issues to sbd-operator repository

## Related Documentation

- [SBD Operator User Guide](../docs/sbdconfig-user-guide.md)
- [Storage Class Validation](../docs/storage-class-validation.md)
- [OpenShift Data Foundation Documentation](https://access.redhat.com/documentation/en-us/red_hat_openshift_data_foundation)
- [Ceph Documentation](https://docs.ceph.com/) 