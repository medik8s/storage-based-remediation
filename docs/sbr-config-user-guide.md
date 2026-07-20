# StorageBasedRemediationConfig User Guide

## Overview

StorageBasedRemediationConfig is a **namespaced** Kubernetes custom resource that configures the Storage-Based Remediation (SBR) operator for high-availability clustering with automatic node remediation. The SBR operator provides watchdog-based fencing to ensure cluster integrity by automatically rebooting unresponsive nodes.

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

---

## Quick Start

1. Install the operator from OperatorHub (OpenShift) or using manifests (Kubernetes)
2. Create a StorageBasedRemediationConfig in your target namespace:

```yaml
apiVersion: storage-based-remediation.medik8s.io/v1alpha1
kind: StorageBasedRemediationConfig
metadata:
  name: default
  namespace: sbr-operator-system
spec:
  sharedStorageClass: "ocs-storagecluster-cephfs"
```

3. Verify deployment:

```bash
kubectl get storagebasedremediationconfig -n <namespace>
kubectl get daemonset -n <namespace>
kubectl get pods -n <namespace> -l app=sbr-agent
```

> **Note**: The agent image is managed automatically by the operator via the `RELATED_IMAGE_AGENT`
> environment variable set during OLM installation. Do not set `spec.image` — the field does not exist
> in the CRD.

---

## Multiple StorageBasedRemediationConfig Support

**New in v1.1+**: The SBR operator now supports multiple StorageBasedRemediationConfig resources in the same namespace, enabling advanced deployment scenarios:

### Use Cases
- **A/B Testing**: Deploy different SBR configurations side by side
- **Gradual Rollouts**: Roll out new configurations incrementally
- **Environment Separation**: Separate dev/staging configs in the same namespace
- **Different Requirements**: Multiple teams with different SBR settings

### How It Works
- **Shared Service Account**: All StorageBasedRemediationConfigs in a namespace share the same `sbr-agent` service account
- **Separate DaemonSets**: Each StorageBasedRemediationConfig creates its own DaemonSet with unique naming
- **Independent Configurations**: Each StorageBasedRemediationConfig can have different settings
- **Automatic Cleanup**: Deleting a StorageBasedRemediationConfig only removes its specific resources

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
  watchdogPath: "/dev/watchdog1"
---
# Canary configuration for testing
apiVersion: storage-based-remediation.medik8s.io/v1alpha1
kind: StorageBasedRemediationConfig
metadata:
  name: canary-sbr
  namespace: my-app
spec:
  nodeSelector:
    canary: "true"
```

This creates:
- Shared service account: `my-app/sbr-agent`
- Production DaemonSet: `my-app/sbr-agent-production-sbr`
- Canary DaemonSet: `my-app/sbr-agent-canary-sbr`
- Separate ClusterRoleBindings for each configuration

---

## Installation

### Standard Kubernetes
```bash
kubectl apply -f https://github.com/medik8s/storage-based-remediation/releases/latest/download/install.yaml
```

### OpenShift (Recommended)

1. **Install from OperatorHub:**
   - Navigate to **Operators** → **OperatorHub**
   - Search for "Storage Based Remediation"
   - Click **Install** and follow the wizard

2. **Verify operator installation:**

```bash
oc get csv -n openshift-operators | grep storage-based-remediation
oc get pods -n openshift-operators | grep -E 'storage-based-remediation|sbr'
```

> **OpenShift SCC**: The operator controller automatically creates a ClusterRoleBinding granting the
> `sbr-agent` service account access to the `privileged` SCC. No manual SCC configuration is needed.

---

## Configuration Reference

### StorageBasedRemediationConfig Fields

#### `watchdogPath` (string, optional)
- **Default**: `/dev/watchdog`
- **Description**: Path to the watchdog device on cluster nodes
- **Notes**:
  - Must exist on all nodes or softdog will be used as fallback
  - Common paths: `/dev/watchdog`, `/dev/watchdog0`, `/dev/watchdog1`

#### `sharedStorageClass` (string, optional)
- **Default**: None (shared storage disabled)
- **Description**: StorageClass name for automatic shared storage provisioning
- **Requirements**:
  - StorageClass must support ReadWriteMany (RWX) access mode
  - Storage backend must support POSIX file locking for coordination
- **Examples**: `"efs-sc"`, `"nfs-client"`, `"cephfs"`, `"glusterfs"`
- **Behavior**: When specified, the controller automatically creates a PVC using this StorageClass and mounts it at `/dev/sbr` in each agent pod

#### `nodeSelector` (map[string]string, optional)
- **Default**: Worker nodes only (`node-role.kubernetes.io/worker: ""`)
- **Description**: Label selector to restrict which nodes run the SBR agent DaemonSet
- **Note**: Merged with the default requirement `kubernetes.io/os=linux`
- **Example**:
  ```yaml
  nodeSelector:
    topology.kubernetes.io/zone: us-east-1a
  ```

#### `sbrTimeoutSeconds` (integer, optional)
- **Default**: `30`
- **Range**: `10` to `300`
- **Description**: Controls the SBR detection timing. Drives derived intervals:
  - Heartbeat = `sbrTimeoutSeconds / 2`
  - Peer-check and update = `sbrTimeoutSeconds / 6` (~3 scans per heartbeat)

#### `maxConsecutiveFailures` (integer, optional)
- **Default**: `7`
- **Range**: `2` to `32`
- **Description**: Number of consecutive watchdog, SBR-device, local-heartbeat, or peer-heartbeat
  failures before a node is marked unhealthy — not limited to storage I/O.
  Time-to-detection scales with `maxConsecutiveFailures × heartbeatInterval` for both local
  and peer failures.

#### `detectOnlyMode` (string, optional)
- **Default**: `Disabled`
- **Valid values**: `Disabled`, `Enabled`
- **Description**: When `Enabled`, the agent detects storage failures and updates node conditions
  but does **not** perform any remediation actions (no node reboot). Useful for monitoring-only
  deployments or gradual rollouts.

---

### StorageBasedRemediationConfig Status Fields

The operator reports status via a standard Kubernetes `conditions` array. Inspect it with:

```bash
kubectl get storagebasedremediationconfig <name> -n <namespace> -o yaml
```

#### Condition Types

| Type | `status: "True"` | `status: "False"` | Purpose |
| ---- | ---------------- | ----------------- | ------- |
| **DaemonSetReady** | All desired SBR agent pods are ready | No nodes scheduled, or some pods not ready | DaemonSet rollout and pod health |
| **SharedStorageReady** | Shared storage PVC is configured, or not used | PVC not bound or unavailable | Shared storage availability |
| **Ready** | DaemonSetReady and SharedStorageReady are both True | Any dependency not ready | Overall readiness |

Each condition object contains:

| Field | Type | Description |
| ----- | ---- | ----------- |
| `type` | string | Condition type |
| `status` | string | `"True"`, `"False"`, or `"Unknown"` |
| `reason` | string | Short, CamelCase reason |
| `message` | string | Human-readable description |
| `lastTransitionTime` | string (RFC3339) | Time of the last change |
| `observedGeneration` | int64 | `.metadata.generation` when condition was set |

**Example status:**

```yaml
status:
  conditions:
  - type: DaemonSetReady
    status: "True"
    reason: DaemonSetReady
    message: All 3 SBR agent pods are ready
    lastTransitionTime: "2026-07-12T10:00:00Z"
  - type: SharedStorageReady
    status: "True"
    reason: SharedStorageConfigured
    message: Shared storage PVC 'default-shared-storage' is configured
    lastTransitionTime: "2026-07-12T10:00:00Z"
  - type: Ready
    status: "True"
    reason: Ready
    message: StorageBasedRemediationConfig is ready
    lastTransitionTime: "2026-07-12T10:00:00Z"
```

---

## Configuration Examples

### Basic Watchdog-Only Configuration

```yaml
apiVersion: storage-based-remediation.medik8s.io/v1alpha1
kind: StorageBasedRemediationConfig
metadata:
  name: basic-sbr
  namespace: sbr-operator-system
spec:
  # Minimal configuration - uses defaults for all fields
```

### Production Configuration

```yaml
apiVersion: storage-based-remediation.medik8s.io/v1alpha1
kind: StorageBasedRemediationConfig
metadata:
  name: production-sbr
  namespace: sbr-operator-system
spec:
  watchdogPath: "/dev/watchdog1"
  sharedStorageClass: "ocs-storagecluster-cephfs"
  sbrTimeoutSeconds: 60
  maxConsecutiveFailures: 5
```

### Development/Testing Configuration

```yaml
apiVersion: storage-based-remediation.medik8s.io/v1alpha1
kind: StorageBasedRemediationConfig
metadata:
  name: dev-sbr
  namespace: sbr-dev
spec:
  watchdogPath: "/dev/watchdog"
  detectOnlyMode: Enabled   # detect without fencing
```

### Zone-Scoped Configuration

```yaml
apiVersion: storage-based-remediation.medik8s.io/v1alpha1
kind: StorageBasedRemediationConfig
metadata:
  name: zone-a-sbr
  namespace: sbr-operator-system
spec:
  nodeSelector:
    topology.kubernetes.io/zone: us-east-1a
  sharedStorageClass: "efs-sc"
```

### Multi-Cluster Configuration

Each StorageBasedRemediationConfig below targets a **different cluster** — apply each manifest
against its own cluster context (`kubectl config use-context <cluster>` or `--context`), not
together with a single `kubectl apply`.

```yaml
# Apply against the "west" cluster context
apiVersion: storage-based-remediation.medik8s.io/v1alpha1
kind: StorageBasedRemediationConfig
metadata:
  name: cluster-west-sbr
  namespace: sbr-cluster-west
spec:
  watchdogPath: "/dev/watchdog"
```

```yaml
# Apply against the "east" cluster context
apiVersion: storage-based-remediation.medik8s.io/v1alpha1
kind: StorageBasedRemediationConfig
metadata:
  name: cluster-east-sbr
  namespace: sbr-cluster-east
spec:
  watchdogPath: "/dev/watchdog"
```

---

## Deployment and Management

### Create StorageBasedRemediationConfig

```bash
# Apply configuration
kubectl apply -f sbrconfig.yaml

# Verify creation
kubectl get storagebasedremediationconfig -n <namespace>
kubectl describe storagebasedremediationconfig basic-sbr -n <namespace>
```

### Monitor Deployment

```bash
# Check SBR agent DaemonSet
kubectl get daemonset -n <namespace>

# Check agent pods
kubectl get pods -n <namespace> -o wide

# View agent logs
kubectl logs -n <namespace> -l app=sbr-agent

# Check conditions
kubectl get storagebasedremediationconfig <name> -n <namespace> \
  -o jsonpath='{range .status.conditions[*]}{.type}: {.status} ({.reason}){"\n"}{end}'
```

### Update Configuration

```bash
# Edit existing configuration
kubectl edit storagebasedremediationconfig basic-sbr -n <namespace>

# Apply updated configuration
kubectl apply -f updated-sbrconfig.yaml
```

### Remove StorageBasedRemediationConfig

```bash
# Delete configuration (this will remove all associated SBR agents)
kubectl delete storagebasedremediationconfig basic-sbr -n <namespace>

# Verify cleanup
kubectl get pods -n <namespace> -l app=sbr-agent
kubectl get daemonset -n <namespace>
```

> **Note**: The shared service account (`sbr-agent`) is not automatically deleted when removing
> a StorageBasedRemediationConfig, as other configs in the same namespace may still be using it.

---

## Storage Integration

### Supported Storage Types

#### ✅ Fully Supported (with file locking)
- **NFS**: Network File System with full POSIX locking
- **CephFS**: Ceph filesystem with distributed locking
- **GlusterFS**: Distributed filesystem with locking support

#### ⚠️ Partially Supported (jitter coordination)
- **Ceph RBD**: Block storage without file locking
- **iSCSI**: Block storage protocols
- **Local storage**: Node-local block devices
- **Cloud block storage**: AWS EBS, Azure Disk, GCP PD

#### ❌ Not Recommended
- **Object storage**: S3, MinIO, etc. (not block devices)
- **Read-only storage**: ConfigMaps, Secrets

### Storage Configuration

The SBR operator automatically detects storage capabilities:
- **File locking enabled**: Used with NFS, CephFS, GlusterFS
- **Jitter coordination**: Used with block storage without file locking
- **Watchdog-only**: Used when no shared storage is configured

---

## Shared Storage Configuration

### StorageClass-Based Approach (Recommended)

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
  namespace: sbr-operator-system
spec:
  sharedStorageClass: "efs-sc"
```

#### How It Works
1. **Automatic PVC Creation**: Controller creates a PVC using the specified StorageClass
2. **RWX Validation**: Ensures the StorageClass supports ReadWriteMany access mode
3. **DaemonSet Integration**: Mounts the PVC at `/dev/sbr` in all sbr-agent pods
4. **Coordination**: Enables cross-node coordination via shared storage
5. **Lifecycle Management**: PVC is owned by StorageBasedRemediationConfig and cleaned up automatically

#### Supported StorageClass Examples
- **AWS EFS**: `efs-sc` (via EFS CSI driver)
- **NFS**: `nfs-client` (via NFS CSI driver)
- **CephFS**: `cephfs` (via Ceph CSI driver)
- **GlusterFS**: `glusterfs` (via GlusterFS CSI driver)
- **Azure Files**: `azurefile-csi`, **NFS v4.1-backed shares only** — SMB-backed Azure Files
  does not provide the POSIX file-locking semantics the coordination contract requires

---

## Monitoring and Observability

### Prometheus Metrics

The SBR agent exposes metrics on port **8082**:

```yaml
sbr_agent_status_healthy          # Agent health (1=healthy, 0=unhealthy)
sbr_device_io_errors_total        # I/O errors with shared storage
sbr_watchdog_pets_total           # Successful watchdog pets
sbr_peer_status                   # Peer node status
sbr_self_fenced_total             # Self-fencing events
```

See [SBR Agent Prometheus Metrics](sbr-agent-prometheus-metrics.md) for the complete list.

### Health Checks

```bash
# Check overall status
kubectl get storagebasedremediationconfig -o wide -n <namespace>

# Check agent pod health
kubectl get pods -n <namespace> -o wide

# View recent events
kubectl get events -n <namespace> --sort-by='.lastTimestamp'
```

### Log Analysis

```bash
# View agent logs
kubectl logs -n <namespace> -l app=sbr-agent --tail=100

# Follow logs in real-time
kubectl logs -n <namespace> -l app=sbr-agent -f

# Check for specific issues
kubectl logs -n <namespace> -l app=sbr-agent | grep -i error
```

---

## Troubleshooting

### Common Issues

#### SBR Agent Pods Not Starting

**Symptoms**: Pods in `Pending` or `CrashLoopBackOff` state

**Diagnosis**:
```bash
kubectl describe pods -n <namespace>
kubectl logs -n <namespace> <pod-name>
```

**Common Causes**:

1. **Missing watchdog device**: Check if `/dev/watchdog` exists on nodes

   For OpenShift:
   ```bash
   oc debug node/<node-name> -- chroot /host sh -c 'ls -la /dev/watchdog*'
   ```

   For Standard Kubernetes:
   ```bash
   kubectl debug node/<node-name> -it --image=busybox --profile=sysadmin \
     -- chroot /host sh -c 'ls -la /dev/watchdog*'
   ```

2. **Insufficient privileges (OpenShift)**: Verify the ClusterRoleBinding was created:
   ```bash
   oc get clusterrolebinding | grep sbr-agent
   ```

3. **Resource constraints**: Verify node resources:
   ```bash
   kubectl describe node <node-name>
   ```

#### Watchdog Device Issues

**Symptoms**: Logs showing "failed to open watchdog device"

**Diagnosis**:
```bash
# For OpenShift:
oc debug node/<node-name> -- chroot /host sh -c 'dmesg | grep -i watchdog'
# For standard Kubernetes:
kubectl debug node/<node-name> -it --image=busybox --profile=sysadmin \
  -- chroot /host sh -c 'dmesg | grep -i watchdog'
```

**Solutions**:
- **Hardware watchdog**: Ensure hardware watchdog is enabled in BIOS/UEFI
- **Software fallback**: The agent will automatically try to load `softdog` module
- **Custom path**: Update `watchdogPath` if watchdog is at non-standard location

#### Shared Storage Issues

**Symptoms**: I/O errors or coordination failures

**Diagnosis**:
```bash
kubectl logs -n <namespace> -l app=sbr-agent | grep -i "sbr device"
kubectl logs -n <namespace> -l app=sbr-agent | grep -i "coordination strategy"
kubectl get pvc -n <namespace>
kubectl describe pvc <pvc-name> -n <namespace>
```

#### Node Slot Exhaustion

**Symptoms**: Logs showing "all preferred slots are occupied"

**Solutions**:
- **Manual cleanup**: Remove stale node entries if needed
- **Scale considerations**: SBR supports up to 255 nodes per cluster

### Performance Tuning

#### `sbrTimeoutSeconds` Guidance

- **Responsive environments**: `15`–`30` (fast detection, lower tolerance for transient failures)
- **Standard environments**: `30`–`60` (default 30 is appropriate for most cases)
- **Conservative**: `60`–`300` for high-latency storage or noisy environments

#### `maxConsecutiveFailures` Guidance

- **Fast detection**: `2`–`3` (detect quickly, higher false-positive risk)
- **Balanced**: `5`–`7` (default 7 balances sensitivity vs. flap avoidance)
- **Resilient**: `10`–`32` for environments with frequent transient I/O errors

#### Resource Limits

```yaml
resources:
  limits:
    cpu: 100m
    memory: 128Mi
  requests:
    cpu: 50m
    memory: 64Mi
```

---

## Security Considerations

### Privileges Required

The SBR agent requires elevated privileges for:
- **Watchdog access**: Direct hardware device access
- **System reboot**: Ability to trigger node reboot
- **Shared storage**: Access to block devices

### OpenShift Security

The controller automatically creates a ClusterRoleBinding binding the `sbr-agent` service account
to the `privileged` SCC at reconcile time. No manual SCC configuration is needed.

To verify:
```bash
oc get clusterrolebinding | grep sbr-agent
```

### Network Security

- **Metrics endpoint**: Port 8082 for Prometheus scraping
- **No external network**: Agent operates locally on each node
- **Shared storage**: Network access to storage backend required

---

## Best Practices

### Production Deployment

1. **Monitor metrics**: Set up Prometheus monitoring and alerting on port 8082
2. **Test failover**: Regularly test node failure scenarios
3. **Resource planning**: Ensure adequate node resources
4. **Backup configuration**: Store StorageBasedRemediationConfig in version control
5. **Tune thresholds**: Adjust `sbrTimeoutSeconds` and `maxConsecutiveFailures` to your storage latency profile
6. **Roll out detect-only first**: Use `detectOnlyMode: Enabled` initially to validate behavior before enabling fencing

### High Availability

1. **Multiple watchdogs**: Use hardware watchdog with software fallback
2. **Shared storage**: Use redundant storage with file locking
3. **Network redundancy**: Ensure multiple paths to shared storage
4. **Regular testing**: Verify fencing behavior under load

---

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

> The `port` field above must match the **named port on the Service** fronting the agent
> (see `config/prometheus/sbr-agent-metrics.yaml`), not the container's port name.

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

---

## API Reference

For complete API documentation, see:
- [StorageBasedRemediationConfig API Types](../api/v1alpha1/storagebasedremediationconfig_types.go)
- [Generated CRD](../config/crd/bases/storage-based-remediation.medik8s.io_storagebasedremediationconfigs.yaml)
- [Sample Configurations](../config/samples/)

## Repository

- <https://github.com/medik8s/storage-based-remediation>

## Related Documentation

- [SBR Coordination Strategies](sbr-coordination-strategies.md)
- [SBR Agent Prometheus Metrics](sbr-agent-prometheus-metrics.md)
- [Design Documentation](archive/design.md)
- [OpenShift Configuration](../config/openshift/README.md)
