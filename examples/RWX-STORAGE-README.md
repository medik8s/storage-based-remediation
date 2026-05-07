# ReadWriteMany (RWX) Shared Storage for SBR Operator

This directory contains scripts and examples for setting up ReadWriteMany (RWX) persistent volumes that can be shared between multiple OpenShift worker nodes on AWS.

## Overview

The SBR operator can benefit from shared storage for:
- **Coordination between agents**: Share status and coordination data between SBR agents running on different nodes
- **Shared configuration**: Distribute configuration files across all nodes
- **Logging and monitoring**: Centralized logging and status collection
- **SBR device metadata**: Share information about SBR devices and slot assignments

## Prerequisites

### 1. AWS EFS CSI Driver
The cluster must have the AWS EFS CSI driver installed:

```bash
# Install EFS CSI driver
oc apply -k "github.com/kubernetes-sigs/aws-efs-csi-driver/deploy/kubernetes/overlays/stable/?ref=release-1.7"

# Verify installation
oc get csidriver efs.csi.aws.com
```

### 2. AWS Permissions
Your AWS credentials must have the following permissions:
- `efs:CreateFileSystem`
- `efs:CreateMountTarget`
- `efs:DescribeFileSystems`
- `efs:DescribeMountTargets`
- `ec2:DescribeSubnets`
- `ec2:DescribeSecurityGroups`
- `ec2:DescribeVpcs`

### 3. Tools Required
- AWS CLI configured
- `kubectl` or `oc` CLI
- `jq` for JSON processing

## Quick Start

### 1. Create RWX Persistent Volume

```bash
# Basic setup with defaults
./scripts/setup-shared-storage.sh

# Custom configuration
./scripts/setup-shared-storage.sh \
  --name "sbr-shared-storage" \
  --size "20Gi" \
  --namespace "sbr-system" \
  --cluster-name "my-ocp-cluster"
```

### 2. Verify Setup

```bash
# Check PV and PVC
oc get pv sbr-shared-pv
oc get pvc sbr-shared-pvc -n sbr-system

# Check EFS filesystem
aws efs describe-file-systems --region us-east-1 \
  --query "FileSystems[?Tags[?Key=='Name' && Value=='sbr-operator-shared-storage']]"
```

### 3. Test RWX Functionality

```bash
# Deploy test resources
oc apply -f examples/rwx-shared-storage-example.yaml

# Check test pod
oc logs rwx-storage-test -n sbr-system

# Run verification job
oc get job verify-rwx-storage -n sbr-system
oc logs job/verify-rwx-storage -n sbr-system
```

## Script Options

### Basic Options
- `--name`: EFS filesystem name (default: `sbr-operator-shared-storage`)
- `--pv-name`: Persistent Volume name (default: `sbr-shared-pv`)
- `--pvc-name`: PVC name (default: `sbr-shared-pvc`)
- `--size`: Storage size (default: `10Gi`)
- `--namespace`: Kubernetes namespace (default: `sbr-system`)

### Advanced Options
- `--performance-mode`: EFS performance (`generalPurpose` or `maxIO`)
- `--throughput-mode`: EFS throughput (`provisioned` or `burstingThroughput`)
- `--provisioned-tp`: Provisioned throughput in MiB/s (default: 100)

### Utility Options
- `--dry-run`: Preview what would be created
- `--cleanup`: Remove all created resources
- `--cluster-name`: Specify cluster name (auto-detected if not provided)

## Usage Examples

### Example 1: StorageBasedRemediationConfig with Shared Storage

```yaml
apiVersion: storage-based-remediation.medik8s.io/v1alpha1
kind: StorageBasedRemediationConfig
metadata:
  name: sbr-config-with-shared-storage
  namespace: sbr-system
spec:
  # Watchdog configuration
  watchdogPath: "/dev/watchdog"

  # Note: Additional volumes for shared storage would require
  # extending the StorageBasedRemediationConfig CRD to support custom volumes
```

### Example 2: Coordination Between Agents

```bash
# Each agent writes its status to shared storage
echo "node-status" > /shared/nodes/$NODE_NAME/status.json

# Agents can read status from other nodes
ls /shared/nodes/
cat /shared/nodes/*/status.json
```

### Example 3: Shared Configuration

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: sbr-shared-config
data:
  sbr.conf: |
    SBR_DEVICE="/dev/disk/by-id/scsi-36001405example"
    SBR_WATCHDOG_DEV="/dev/watchdog"
    SBR_WATCHDOG_TIMEOUT="30"
```

## Performance Considerations

### EFS Performance Modes

1. **General Purpose** (default)
   - Up to 7,000 file operations per second
   - Lower latency per operation
   - Best for most workloads

2. **Max I/O**
   - Higher levels of aggregate throughput
   - Higher latency per operation
   - Best for applications that need higher performance

### Throughput Modes

1. **Provisioned** (default)
   - Consistent throughput regardless of size
   - Pay for provisioned throughput
   - Good for predictable workloads

2. **Bursting**
   - Throughput scales with file system size
   - Bursts to higher levels temporarily
   - Cost-effective for variable workloads

## Security Considerations

### Network Security
- EFS mount targets are created in worker node subnets
- Security group allows NFS traffic (port 2049) only within the cluster
- No external access to EFS filesystem

### Access Control
- Kubernetes RBAC controls access to PVC
- File system permissions can be set via `directoryPerms`
- Consider using EFS Access Points for additional security

### Encryption
- EFS supports encryption at rest and in transit
- Add encryption settings to the script if required:

```bash
# Add to create_efs_filesystem function
--encrypted \
--kms-key-id "alias/aws/elasticfilesystem"
```

## Troubleshooting

### Common Issues

1. **Mount targets not created**
   ```bash
   # Check security groups
   aws ec2 describe-security-groups --group-ids sg-xxx

   # Check subnets
   aws ec2 describe-subnets --subnet-ids subnet-xxx
   ```

2. **PVC stuck in Pending**
   ```bash
   # Check EFS CSI driver
   oc get pods -n kube-system | grep efs

   # Check PVC events
   oc describe pvc sbr-shared-pvc -n sbr-system
   ```

3. **Mount failures in pods**
   ```bash
   # Check pod events
   oc describe pod <pod-name> -n sbr-system

   # Check EFS mount targets
   aws efs describe-mount-targets --file-system-id fs-xxx
   ```

### Debugging Commands

```bash
# Check EFS filesystem status
aws efs describe-file-systems --file-system-id fs-xxx

# Check mount target health
aws efs describe-mount-targets --file-system-id fs-xxx

# Test NFS connectivity from worker node
telnet <mount-target-ip> 2049

# Check CSI driver logs
oc logs -n kube-system -l app=efs-csi-controller
```

## Cleanup

### Remove All Resources

```bash
# Use the cleanup option
./scripts/setup-shared-storage.sh --cleanup

# Manual cleanup if needed
oc delete pvc sbr-shared-pvc -n sbr-system
oc delete pv sbr-shared-pv
oc delete storageclass efs-sc
aws efs delete-file-system --file-system-id fs-xxx
```

### Partial Cleanup

```bash
# Remove only Kubernetes resources (keep EFS)
oc delete pvc sbr-shared-pvc -n sbr-system
oc delete pv sbr-shared-pv

# Remove only test resources
oc delete -f examples/rwx-shared-storage-example.yaml
```

## Cost Optimization

### EFS Pricing Factors
- Storage used (GB-month)
- Throughput provisioned (MiB/s-month)
- Requests (per million)

### Cost Reduction Tips
1. Use bursting throughput for variable workloads
2. Monitor actual storage usage
3. Use EFS Intelligent Tiering for infrequently accessed data
4. Consider EFS One Zone for non-critical data

### Monitoring Usage

```bash
# Check EFS metrics
aws cloudwatch get-metric-statistics \
  --namespace AWS/EFS \
  --metric-name ClientConnections \
  --dimensions Name=FileSystemId,Value=fs-xxx \
  --start-time 2025-01-01T00:00:00Z \
  --end-time 2025-01-02T00:00:00Z \
  --period 3600 \
  --statistics Sum
```

## Integration with SBR Operator

The RWX storage can be integrated with the SBR operator for:

1. **Slot coordination**: Share slot assignment data between agents
2. **Status reporting**: Centralized status collection
3. **Configuration distribution**: Share SBR device configurations
4. **Log aggregation**: Collect logs from all nodes

### Current Integration

The StorageBasedRemediationConfig CRD now supports shared storage through the following fields:

```yaml
apiVersion: storage-based-remediation.medik8s.io/v1alpha1
kind: StorageBasedRemediationConfig
metadata:
  name: sbr-config-with-shared-storage
spec:
  # Standard SBR configuration
  watchdogPath: "/dev/watchdog"
  # Shared storage configuration
  sharedStorageClass: "efs-sc"                 # Name of RWX StorageClass

```

### Implementation in Controller

The SBR operator controller automatically:

1. **Detects shared storage configuration** when `sharedStorageClass` is specified
2. **Creates a PVC** using the specified StorageClass (named `sbr-shared-{config-name}`)
3. **Validates PVC creation** and waits for it to be bound
4. **Adds PVC volume to DaemonSet** template
5. **Mounts shared storage** at the specified path in all agent pods
6. **Configures sbr-agent** with the shared storage path
7. **Enables coordination features** for cross-node communication

### Benefits of Integration

- **Simplified deployment**: Single StorageBasedRemediationConfig resource configures everything
- **Automatic validation**: Controller ensures storage configuration is valid
- **Seamless integration**: Shared storage is automatically mounted when configured
- **Consistent configuration**: All agents use same shared storage settings
- **Optional feature**: Existing deployments continue to work without changes

See `rwx-shared-storage-example.yaml` for detailed integration examples and the generated DaemonSet structure.
