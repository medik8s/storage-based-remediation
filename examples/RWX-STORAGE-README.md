# ReadWriteMany (RWX) Shared Storage for SBD Operator

This directory contains scripts and examples for setting up ReadWriteMany (RWX) persistent volumes that can be shared between multiple OpenShift worker nodes on AWS.

## Overview

The SBD operator can benefit from shared storage for:
- **Coordination between agents**: Share status and coordination data between SBD agents running on different nodes
- **Shared configuration**: Distribute configuration files across all nodes
- **Logging and monitoring**: Centralized logging and status collection
- **SBD device metadata**: Share information about SBD devices and slot assignments

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
./scripts/create-rwx-pv.sh

# Custom configuration
./scripts/create-rwx-pv.sh \
  --name "sbd-shared-storage" \
  --size "20Gi" \
  --namespace "sbd-system" \
  --cluster-name "my-ocp-cluster"
```

### 2. Verify Setup

```bash
# Check PV and PVC
oc get pv sbd-shared-pv
oc get pvc sbd-shared-pvc -n sbd-system

# Check EFS filesystem
aws efs describe-file-systems --region us-east-1 \
  --query "FileSystems[?Tags[?Key=='Name' && Value=='sbd-operator-shared-storage']]"
```

### 3. Test RWX Functionality

```bash
# Deploy test resources
oc apply -f examples/rwx-shared-storage-example.yaml

# Check test pod
oc logs rwx-storage-test -n sbd-system

# Run verification job
oc get job verify-rwx-storage -n sbd-system
oc logs job/verify-rwx-storage -n sbd-system
```

## Script Options

### Basic Options
- `--name`: EFS filesystem name (default: `sbd-operator-shared-storage`)
- `--pv-name`: Persistent Volume name (default: `sbd-shared-pv`)
- `--pvc-name`: PVC name (default: `sbd-shared-pvc`)
- `--size`: Storage size (default: `10Gi`)
- `--namespace`: Kubernetes namespace (default: `sbd-system`)

### Advanced Options
- `--performance-mode`: EFS performance (`generalPurpose` or `maxIO`)
- `--throughput-mode`: EFS throughput (`provisioned` or `burstingThroughput`)
- `--provisioned-tp`: Provisioned throughput in MiB/s (default: 100)

### Utility Options
- `--dry-run`: Preview what would be created
- `--cleanup`: Remove all created resources
- `--cluster-name`: Specify cluster name (auto-detected if not provided)

## Usage Examples

### Example 1: SBDConfig with Shared Storage

```yaml
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDConfig
metadata:
  name: sbd-config-with-shared-storage
  namespace: sbd-system
spec:
  # SBD agent image configuration
  image: "quay.io/medik8s/sbd-agent:latest"
  imagePullPolicy: "IfNotPresent"
  
  # Watchdog configuration
  sbdWatchdogPath: "/dev/watchdog"
  watchdogTimeout: "60s"
  petIntervalMultiple: 4
  
  # Node management
  staleNodeTimeout: "1h"
  
  # Note: Additional volumes for shared storage would require
  # extending the SBDConfig CRD to support custom volumes
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
  name: sbd-shared-config
data:
  sbd.conf: |
    SBD_DEVICE="/dev/disk/by-id/scsi-36001405example"
    SBD_WATCHDOG_DEV="/dev/watchdog"
    SBD_WATCHDOG_TIMEOUT="30"
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
   oc describe pvc sbd-shared-pvc -n sbd-system
   ```

3. **Mount failures in pods**
   ```bash
   # Check pod events
   oc describe pod <pod-name> -n sbd-system
   
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
./scripts/create-rwx-pv.sh --cleanup

# Manual cleanup if needed
oc delete pvc sbd-shared-pvc -n sbd-system
oc delete pv sbd-shared-pv
oc delete storageclass efs-sc
aws efs delete-file-system --file-system-id fs-xxx
```

### Partial Cleanup

```bash
# Remove only Kubernetes resources (keep EFS)
oc delete pvc sbd-shared-pvc -n sbd-system
oc delete pv sbd-shared-pv

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

## Integration with SBD Operator

The RWX storage can be integrated with the SBD operator for:

1. **Slot coordination**: Share slot assignment data between agents
2. **Status reporting**: Centralized status collection
3. **Configuration distribution**: Share SBD device configurations
4. **Log aggregation**: Collect logs from all nodes

### Current Integration

Currently, the SBDConfig CRD doesn't support additional volumes. The examples show:
- How to create the RWX storage independently
- How the operator controller would generate DaemonSets with shared storage
- The desired integration pattern for future enhancements

### Future Enhancement: Extending SBDConfig CRD

To fully integrate RWX storage, the SBDConfig CRD could be extended:

```yaml
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDConfig
metadata:
  name: sbd-config-with-shared-storage
spec:
  # Existing fields...
  sbdWatchdogPath: "/dev/watchdog"
  watchdogTimeout: "60s"
  
  # New fields for shared storage
  sharedStorage:
    enabled: true
    persistentVolumeClaim: "sbd-shared-pvc"
    mountPath: "/shared-storage"
    
  # Additional volumes support
  additionalVolumes:
  - name: shared-storage
    persistentVolumeClaim:
      claimName: sbd-shared-pvc
    mountPath: /shared-storage
    
  # Coordination settings
  coordination:
    enabled: true
    slotAssignmentFile: "/shared-storage/coordination/slot-assignments.json"
    nodeRegistrationPath: "/shared-storage/nodes"
```

### Implementation in Controller

The SBD operator controller would then:

1. **Detect shared storage configuration** in SBDConfig
2. **Validate PVC exists** and is RWX capable
3. **Add volumes to DaemonSet** template
4. **Configure agent with shared storage paths**
5. **Enable coordination features** when shared storage is available

### Benefits of Integration

- **Simplified deployment**: Single SBDConfig resource configures everything
- **Automatic validation**: Controller ensures storage is properly configured
- **Enhanced features**: Coordination and shared state management
- **Consistent configuration**: All agents use same shared storage settings

See `rwx-shared-storage-example.yaml` for detailed integration examples and the generated DaemonSet structure. 