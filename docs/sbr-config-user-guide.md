# StorageBasedRemediationConfig User Guide

## Overview

StorageBasedRemediationConfig is a cluster-scoped Kubernetes custom resource that configures the Storage-Based remediation (SBR) operator for high-availability clustering with automatic node remediation. The SBR operator provides watchdog-based fencing to ensure cluster integrity by automatically rebooting unresponsive nodes.

## Key Features

- **Automatic Node Fencing**: Unresponsive nodes are automatically rebooted via watchdog timeout
- **Shared Storage Coordination**: Optional coordination via shared block devices for split-brain prevention
- **Flexible Watchdog Support**: Works with hardware watchdogs or software fallback (softdog)
- **File Locking Coordination**: Configurable file locking for shared storage environments
- **Prometheus Metrics**: Built-in monitoring and observability
- **Multiple Configurations**: Support for multiple StorageBasedRemediationConfig resources in the same namespace for different use cases

## Prerequisites

### Required
- Kubernetes cluster (1.21+) or OpenShift (4.8+)
- Cluster administrator privileges
- Nodes with watchdog devices (hardware or software)

### Optional (for shared storage mode)
- Shared block storage accessible from all nodes
- Storage that supports POSIX file locking (NFS, CephFS, GlusterFS)
- **StorageClass** with ReadWriteMany (RWX) access mode support

## Multiple StorageBasedRemediationConfig Support

**New in v1.1+**: The SBR operator now supports multiple StorageBasedRemediationConfig resources in the same namespace, enabling advanced deployment scenarios:

### Use Cases
- **A/B Testing**: Deploy different SBR agent versions side by side
- **Gradual Rollouts**: Roll out new configurations incrementally
- **Environment Separation**: Separate dev/staging configs in the same namespace
- **Different Requirements**: Multiple teams with different SBR settings

### How It Works
- **Shared Service Account**: All StorageBasedRemediationConfigs in a namespace share the same `sbr-agent` service account
- **Separate DaemonSets**: Each StorageBasedRemediationConfig creates its own DaemonSet with unique naming
- **Independent Configurations**: Each StorageBasedRemediationConfig can have different images, timeouts, and settings
- **Automatic Cleanup**: Deleting an StorageBasedRemediationConfig only removes its specific resources

### Resource Naming
- Service Account: `sbr-agent` (shared across all StorageBasedRemediationConfigs in namespace)
- DaemonSet: `sbr-agent-{sbrconfig-name}` (unique per StorageBasedRemediationConfig)
- ClusterRoleBinding: `sbr-agent-{namespace}-{sbrconfig-name}` (globally unique)

### Example: Multiple Configurations
```yaml
# Production configuration
apiVersion: storage-based-remediation.medik8s.io/v1alpha1
kind: StorageBasedRemediationConfig
metadata:
  name: production-sbr
  namespace: my-app
spec:
  image: "quay.io/medik8s/storage-based-remediation-agent:v1.2.3"
  imagePullPolicy: "IfNotPresent"
  staleNodeTimeout: "1h"
---
# Canary configuration for testing
apiVersion: storage-based-remediation.medik8s.io/v1alpha1
kind: StorageBasedRemediationConfig
metadata:
  name: canary-sbr
  namespace: my-app
spec:
  image: "quay.io/medik8s/storage-based-remediation-agent:v1.3.0-beta"
  imagePullPolicy: "Always"
  staleNodeTimeout: "30m"
  nodeSelector:
    canary: "true"
```

This creates:
- Shared service account: `my-app/sbr-agent`
- Production DaemonSet: `my-app/sbr-agent-production-sbr`
- Canary DaemonSet: `my-app/sbr-agent-canary-sbr`
- Separate ClusterRoleBindings for each configuration

## Installation

### Standard Kubernetes
```bash
kubectl apply -f https://github.com/medik8s/storage-based-remediation/releases/latest/download/install.yaml
```

### OpenShift
```bash
kubectl apply -f https://github.com/medik8s/storage-based-remediation/releases/latest/download/install-openshift.yaml
```

## Configuration Reference

### StorageBasedRemediationConfig Fields

#### `watchdogPath` (string, optional)
- **Default**: `/dev/watchdog`
- **Description**: Path to the watchdog device on cluster nodes
- **Notes**: 
  - Must exist on all nodes or softdog will be used as fallback
  - Common paths: `/dev/watchdog`, `/dev/watchdog0`, `/dev/watchdog1`

#### `image` (string, optional)
- **Default**: `sbr-agent:latest`
- **Description**: Container image for the SBR agent DaemonSet
- **Recommended**: Use specific version tags for production
- **Example**: `quay.io/medik8s/storage-based-remediation-agent:v1.0.0`

#### `namespace` (string, optional)
- **Default**: `sbr-operator-system`
- **Description**: Namespace where the SBR agent DaemonSet will be deployed
- **Notes**: Namespace will be created if it doesn't exist

#### `staleNodeTimeout` (duration, optional)
- **Default**: `1h`
- **Range**: `1m` to `24h`
- **Description**: Time before inactive nodes are cleaned up from slot mapping
- **Purpose**: Determines when node slots in shared storage are freed for reuse
- **Format**: Go duration format (e.g., `30m`, `2h`, `90s`)

#### `sharedStorageClass` (string, optional)
- **Default**: None (shared storage disabled)
- **Description**: StorageClass name for automatic shared storage provisioning
- **Requirements**: 
  - StorageClass must support ReadWriteMany (RWX) access mode
  - Storage backend must support POSIX file locking for coordination
- **Examples**: `"efs-sc"`, `"nfs-client"`, `"cephfs"`, `"glusterfs"`
- **Behavior**: When specified, the controller automatically creates a PVC using this StorageClass

The controller automatically chooses a sensible mount path (`/sbr-shared`) for shared storage, eliminating the need for manual configuration and reducing potential conflicts.

### StorageBasedRemediationConfig Status Fields

#### `daemonSetReady` (boolean)
- **Description**: Indicates if the SBR agent DaemonSet is ready and running

#### `readyNodes` (int32)
- **Description**: Number of nodes where the SBR agent is ready and operational

#### `totalNodes` (int32)
- **Description**: Total number of nodes where the SBR agent should be deployed

## Configuration Examples

### Basic Watchdog-Only Configuration

For clusters that only need watchdog-based fencing without shared storage:

```yaml
apiVersion: storage-based-remediation.medik8s.io/v1alpha1
kind: StorageBasedRemediationConfig
metadata:
  name: basic-sbr
spec:
  # Use defaults for most settings
  image: "quay.io/medik8s/storage-based-remediation-agent:v1.0.0"
  namespace: "sbr-operator-system"
```

### Production Configuration

For production environments with specific requirements:

```yaml
apiVersion: storage-based-remediation.medik8s.io/v1alpha1
kind: StorageBasedRemediationConfig
metadata:
  name: production-sbr
spec:
  # Specific image version for reproducibility
  image: "quay.io/medik8s/storage-based-remediation-agent:v1.2.3"
  
  # Custom namespace
  namespace: "high-availability"
  
  # Custom watchdog device
  watchdogPath: "/dev/watchdog1"
  
  # Faster cleanup for dynamic environments
  staleNodeTimeout: "30m"
```

### Development/Testing Configuration

For development or testing environments:

```yaml
apiVersion: storage-based-remediation.medik8s.io/v1alpha1
kind: StorageBasedRemediationConfig
metadata:
  name: dev-sbr
spec:
  # Use latest for development
  image: "quay.io/medik8s/storage-based-remediation-agent:latest"
  
  # Faster cleanup for rapid testing
  staleNodeTimeout: "5m"
  
  # Default watchdog path (will use softdog if no hardware watchdog)
  watchdogPath: "/dev/watchdog"
```

### Multi-Cluster Configuration

For environments with multiple clusters:

```yaml
apiVersion: storage-based-remediation.medik8s.io/v1alpha1
kind: StorageBasedRemediationConfig
metadata:
  name: cluster-west-sbr
spec:
  image: "quay.io/medik8s/storage-based-remediation-agent:v1.0.0"
  namespace: "sbr-cluster-west"
  staleNodeTimeout: "45m"
---
apiVersion: storage-based-remediation.medik8s.io/v1alpha1
kind: StorageBasedRemediationConfig
metadata:
  name: cluster-east-sbr
spec:
  image: "quay.io/medik8s/storage-based-remediation-agent:v1.0.0"
  namespace: "sbr-cluster-east"
  staleNodeTimeout: "45m"
```

## Deployment and Management

### Create StorageBasedRemediationConfig

```bash
# Apply configuration
kubectl apply -f sbrconfig.yaml

# Verify creation
kubectl get storagebasedremediationconfig
kubectl describe storagebasedremediationconfig basic-sbr
```

### Monitor Deployment

```bash
# Check SBR agent DaemonSet
kubectl get daemonset -n sbr-operator-system

# Check agent pods
kubectl get pods -n sbr-operator-system

# View agent logs
kubectl logs -n sbr-operator-system -l app=sbr-agent
```

### Update Configuration

```bash
# Edit existing configuration
kubectl edit storagebasedremediationconfig basic-sbr

# Apply updated configuration
kubectl apply -f updated-sbrconfig.yaml
```

### Remove StorageBasedRemediationConfig

```bash
# Delete configuration (this will remove all SBR agents)
kubectl delete storagebasedremediationconfig basic-sbr

# Verify cleanup
kubectl get pods -n sbr-operator-system
```

## Storage Integration

### Supported Storage Types

#### ✅ **Fully Supported (with file locking)**
- **NFS**: Network File System with full POSIX locking
- **CephFS**: Ceph filesystem with distributed locking
- **GlusterFS**: Distributed filesystem with locking support

#### ⚠️ **Partially Supported (jitter coordination)**
- **Ceph RBD**: Block storage without file locking
- **iSCSI**: Block storage protocols
- **Local storage**: Node-local block devices
- **Cloud block storage**: AWS EBS, Azure Disk, GCP PD

#### ❌ **Not Recommended**
- **Object storage**: S3, MinIO, etc. (not block devices)
- **Read-only storage**: ConfigMaps, Secrets

### Storage Configuration

The SBR operator automatically detects storage capabilities:

- **File locking enabled**: Used with NFS, CephFS, GlusterFS
- **Jitter coordination**: Used with block storage without file locking
- **Watchdog-only**: Used when no shared storage is configured

## Shared Storage Configuration

### StorageClass-Based Approach (Recommended)

The SBR operator supports automatic shared storage provisioning using Kubernetes StorageClasses. This approach simplifies configuration and ensures proper PVC lifecycle management.

#### Requirements
- **StorageClass**: Must support ReadWriteMany (RWX) access mode
- **Storage Backend**: Must support POSIX file locking (NFS, CephFS, GlusterFS, EFS)
- **Permissions**: Controller needs permissions to create/manage PVCs

#### Configuration
```yaml
apiVersion: storage-based-remediation.medik8s.io/v1alpha1
kind: StorageBasedRemediationConfig
metadata:
  name: shared-storage-example
spec:
  # Basic configuration
  image: "quay.io/medik8s/storage-based-remediation-agent:v1.0.0"
  watchdogTimeout: "60s"
  
  # Shared storage configuration
  sharedStorageClass: "efs-sc"              # Required: StorageClass name
```

#### How It Works
1. **Automatic PVC Creation**: Controller creates a PVC using the specified StorageClass
2. **RWX Validation**: Ensures the StorageClass supports ReadWriteMany access mode
3. **DaemonSet Integration**: Mounts the PVC in all sbr-agent pods
4. **Coordination**: Enables cross-node coordination via shared storage
5. **Lifecycle Management**: PVC is owned by StorageBasedRemediationConfig and cleaned up automatically

#### Supported Storage Types
- **AWS EFS**: `efs-sc` (via EFS CSI driver)
- **NFS**: `nfs-client` (via NFS CSI driver)
- **CephFS**: `cephfs` (via Ceph CSI driver)
- **GlusterFS**: `glusterfs` (via GlusterFS CSI driver)
- **Azure Files**: `azurefile-csi` (via Azure Files CSI driver)

### Legacy PVC Reference (Deprecated)

**Note**: Direct PVC references are deprecated. Use the StorageClass approach instead.

### Shared Storage with Custom StorageClass

For environments with specific storage requirements:

```yaml
apiVersion: storage-based-remediation.medik8s.io/v1alpha1
kind: StorageBasedRemediationConfig
metadata:
  name: custom-storage-sbr
spec:
  # Use custom StorageClass with specific parameters
  sharedStorageClass: "custom-nfs-sc"
  
  # Faster cleanup for dynamic environments
  staleNodeTimeout: "15m"
```

## Monitoring and Observability

### Prometheus Metrics

The SBR agent exposes metrics on port 8080:

```yaml
# Key metrics to monitor
sbr_agent_status_healthy          # Agent health (1=healthy, 0=unhealthy)
sbr_device_io_errors_total        # I/O errors with shared storage
sbr_watchdog_pets_total           # Successful watchdog pets
sbr_peer_status                   # Peer node status
sbr_self_fenced_total             # Self-fencing events
```

### Health Checks

```bash
# Check overall status
kubectl get storagebasedremediationconfig -o wide

# Check agent pod health
kubectl get pods -n sbr-operator-system -o wide

# View recent events
kubectl get events -n sbr-operator-system --sort-by='.lastTimestamp'
```

### Log Analysis

```bash
# View agent logs
kubectl logs -n sbr-operator-system -l app=sbr-agent --tail=100

# Follow logs in real-time
kubectl logs -n sbr-operator-system -l app=sbr-agent -f

# Check for specific issues
kubectl logs -n sbr-operator-system -l app=sbr-agent | grep -i error
```

## Troubleshooting

### Common Issues

#### SBR Agent Pods Not Starting

**Symptoms**: Pods in `Pending` or `CrashLoopBackOff` state

**Diagnosis**:
```bash
kubectl describe pods -n sbr-operator-system
kubectl logs -n sbr-operator-system <pod-name>
```

**Common Causes**:
- **Insufficient privileges**: Ensure SecurityContextConstraints (OpenShift) or PodSecurityPolicy
- **Missing watchdog device**: Check if `/dev/watchdog` exists on nodes
- **Resource constraints**: Verify node resources and limits

**Solutions**:
```bash
# For OpenShift - check SCC
oc get scc sbr-agent-scc

# For missing watchdog - check node
kubectl debug node/<node-name> -- ls -la /dev/watchdog*

# For resources - check node capacity
kubectl describe node <node-name>
```

#### Watchdog Device Issues

**Symptoms**: Logs showing "failed to open watchdog device"

**Diagnosis**:
```bash
# Check available watchdog devices on nodes
kubectl debug node/<node-name> -- ls -la /dev/watchdog*

# Check dmesg for watchdog-related messages
kubectl debug node/<node-name> -- dmesg | grep -i watchdog
```

**Solutions**:
- **Hardware watchdog**: Ensure hardware watchdog is enabled in BIOS/UEFI
- **Software fallback**: The agent will automatically try to load `softdog` module
- **Custom path**: Update `watchdogPath` if watchdog is at non-standard location

#### Shared Storage Issues

**Symptoms**: I/O errors or coordination failures

**Diagnosis**:
```bash
# Check storage connectivity
kubectl logs -n sbr-operator-system -l app=sbr-agent | grep -i "sbr device"

# Verify file locking capability
kubectl logs -n sbr-operator-system -l app=sbr-agent | grep -i "coordination strategy"
```

**Solutions**:
- **File locking**: Ensure storage supports POSIX file locking
- **Permissions**: Verify read/write access to shared storage
- **Network**: Check network connectivity to storage backend

#### Node Slot Exhaustion

**Symptoms**: Logs showing "all preferred slots are occupied"

**Diagnosis**:
```bash
# Check number of active nodes
kubectl get nodes

# Review stale node timeout
kubectl get storagebasedremediationconfig -o yaml | grep staleNodeTimeout
```

**Solutions**:
- **Reduce timeout**: Lower `staleNodeTimeout` for faster cleanup
- **Manual cleanup**: Remove stale node entries if needed
- **Scale considerations**: STONITH Block Devices (SBD) support up to 255 nodes per cluster

### Performance Tuning

#### Stale Node Timeout

- **Fast environments**: `5m` - `15m` for rapid node turnover
- **Stable environments**: `30m` - `2h` for long-running workloads
- **Conservative**: `2h` - `24h` for critical production systems

#### Resource Limits

```yaml
# Recommended resource limits for SBR agent
resources:
  limits:
    cpu: 100m
    memory: 128Mi
  requests:
    cpu: 50m
    memory: 64Mi
```

## Security Considerations

### Privileges Required

The SBR agent requires elevated privileges for:
- **Watchdog access**: Direct hardware device access
- **System reboot**: Ability to trigger node reboot
- **Shared storage**: Access to block devices

### OpenShift Security

For OpenShift, the agent uses SecurityContextConstraints:
```yaml
# Automatically created by OpenShift installer
apiVersion: security.openshift.io/v1
kind: SecurityContextConstraint
metadata:
  name: sbr-agent-scc
allowPrivilegedContainer: true
allowHostDirVolumePlugin: true
allowHostPID: true
```

### Network Security

- **Metrics endpoint**: Port 8080 for Prometheus scraping
- **No external network**: Agent operates locally on each node
- **Shared storage**: Network access to storage backend required

## Best Practices

### Production Deployment

1. **Use specific image versions**: Avoid `latest` tag in production
2. **Monitor metrics**: Set up Prometheus monitoring and alerting
3. **Test failover**: Regularly test node failure scenarios
4. **Resource planning**: Ensure adequate node resources
5. **Backup configuration**: Store StorageBasedRemediationConfig in version control

### High Availability

1. **Multiple watchdogs**: Use hardware watchdog with software fallback
2. **Shared storage**: Use redundant storage with file locking
3. **Network redundancy**: Ensure multiple paths to shared storage
4. **Regular testing**: Verify fencing behavior under load

### Operational Procedures

1. **Gradual rollout**: Test configuration changes in development first
2. **Monitoring setup**: Monitor SBR metrics and node health
3. **Incident response**: Have procedures for handling fencing events
4. **Documentation**: Maintain runbooks for common scenarios

## Integration Examples

### With Prometheus Monitoring

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: sbr-agent-metrics
spec:
  selector:
    matchLabels:
      app: sbr-agent
  endpoints:
  - port: metrics
    interval: 30s
    path: /metrics
```

### With Grafana Dashboard

Key metrics to visualize:
- Node health status over time
- Watchdog pet success rate
- Storage I/O error trends
- Peer node status matrix

### With Alerting Rules

```yaml
groups:
- name: sbr-agent
  rules:
  - alert: SBRAgentUnhealthy
    expr: sbr_agent_status_healthy == 0
    for: 1m
    labels:
      severity: critical
    annotations:
      summary: "SBR Agent unhealthy on {{ $labels.instance }}"
      
  - alert: WatchdogPetFailure
    expr: increase(sbr_watchdog_pets_total[5m]) == 0
    for: 2m
    labels:
      severity: warning
    annotations:
      summary: "Watchdog not being petted on {{ $labels.instance }}"
```

## API Reference

For complete API documentation, see:
- [StorageBasedRemediationConfig API Types](../api/v1alpha1/storagebasedremediationconfig_types.go)
- [Generated CRD](../config/crd/bases/storage-based-remediation.medik8s.io_storagebasedremediationconfigs.yaml)
- [Sample Configurations](../config/samples/)

## Related Documentation

- [SBR Coordination Strategies](sbr-coordination-strategies.md)
- [SBR Agent Prometheus Metrics](sbr-agent-prometheus-metrics.md)
- [Design Documentation](design.md)
- [OpenShift Configuration](../config/openshift/README.md) 
