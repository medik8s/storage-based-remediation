# SBDConfig User Guide

## Overview

SBDConfig is a namespaced Kubernetes custom resource that configures the Storage-based Remediation operator (formerly SBD operator) for high-availability clustering with automatic node remediation. The operator provides watchdog-based fencing to ensure cluster integrity by automatically rebooting unresponsive nodes.

## Quick Start

For a quick setup, follow these steps:

1. **Install the operator** from OperatorHub (OpenShift) or using manifests (Kubernetes)
2. **Create the SCC** (OpenShift only) - see [Installation](#installation) section
3. **Create an SBDConfig** with required fields:

    ```yaml
    apiVersion: medik8s.medik8s.io/v1alpha1
    kind: SBDConfig
    metadata:
      name: default
      namespace: openshift-operators  # or your preferred namespace
    spec:
      # REQUIRED: Must specify image explicitly (see Known Issues)
      image: "registry.redhat.io/workload-availability/storage-base-remediation-agent-rhel9:v0.1.0"
      
      # Configure shared storage for multi-node coordination
      # Replace with your StorageClass that supports ReadWriteMany (RWX)
      sharedStorageClass: "ocs-storagecluster-cephfs"  # Example: use your actual StorageClass name
    ```

4. **Verify deployment:**

   ```bash
   kubectl get sbdconfig
   kubectl get daemonset -n <namespace>
   kubectl get pods -n <namespace> -l app=sbd-agent
   ```

For detailed information, see the sections below.

## Key Features

- **Automatic Node Fencing**: Unresponsive nodes are automatically rebooted via watchdog timeout
- **Shared Storage Coordination**: Optional coordination via shared block devices for split-brain prevention
- **Flexible Watchdog Support**: Works with hardware watchdogs or software fallback (softdog)
- **File Locking Coordination**: Configurable file locking for shared storage environments
- **Prometheus Metrics**: Built-in monitoring and observability
- **Multiple Configurations**: Support for multiple SBDConfig resources in the same namespace for different use cases

## Prerequisites

### Required

- Kubernetes cluster (1.21+) or OpenShift (4.8+)
- Cluster administrator privileges
- Nodes with watchdog devices (hardware or software)

### Optional (for shared storage mode)

- Shared block storage accessible from all nodes
- Storage that supports POSIX file locking (NFS, CephFS, GlusterFS)
- **StorageClass** with ReadWriteMany (RWX) access mode support

## Known Issues and Workarounds

### Issue 1: Invalid Default Image Path

**Problem**: When creating an SBDConfig without specifying `spec.image`, the operator attempts to use an invalid default image path that doesn't exist in the registry.

**Workaround**: **Always specify `spec.image` explicitly** in your SBDConfig. Use one of these valid images:

- Red Hat: `registry.redhat.io/workload-availability/storage-base-remediation-agent-rhel9:v0.1.0`
- Community: `quay.io/medik8s/sbd-agent:latest` (if available)

**Example:**

```yaml
spec:
  image: "registry.redhat.io/workload-availability/storage-base-remediation-agent-rhel9:v0.1.0"  # Required
```

### Issue 2: Missing SecurityContextConstraints (OpenShift Only)

**Problem**: The operator cannot create SecurityContextConstraints (SCC) at runtime on OpenShift. Without the SCC, agent pods cannot start with the required privileges.

**Workaround**: Create the SCC manually **before** creating your first SBDConfig. See the [Installation](#installation) section for the complete SCC YAML.

**Steps:**

1. Apply the SCC YAML provided in the Installation section
2. Verify the SCC was created: `oc get scc sbd-operator-sbd-agent-privileged`
3. Then create your SBDConfig resources

## Multiple SBDConfig Support

**New**: The Storage-based Remediation operator now supports multiple SBDConfig resources in the same namespace, enabling advanced deployment scenarios:

### Use Cases

- **A/B Testing**: Deploy different SBD agent versions side by side
- **Gradual Rollouts**: Roll out new configurations incrementally
- **Environment Separation**: Separate dev/staging configs in the same namespace
- **Different Requirements**: Multiple teams with different SBD settings

### How It Works

- **Shared Service Account**: All SBDConfigs in a namespace share the same `sbd-agent` service account
- **Separate DaemonSets**: Each SBDConfig creates its own DaemonSet with unique naming
- **Independent Configurations**: Each SBDConfig can have different images, timeouts, and settings
- **Automatic Cleanup**: Deleting an SBDConfig only removes its specific resources

### Resource Naming

- Service Account: `sbd-agent` (shared across all SBDConfigs in namespace)
- DaemonSet: `sbd-agent-{sbdconfig-name}` (unique per SBDConfig)
- ClusterRoleBinding: `sbd-agent-{namespace}-{sbdconfig-name}` (globally unique)

### Example: Multiple Configurations

```yaml
# Production configuration
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDConfig
metadata:
  name: production-sbd
  namespace: my-app
spec:
  image: "quay.io/medik8s/sbd-agent:v1.2.3"
  imagePullPolicy: "IfNotPresent"
  staleNodeTimeout: "1h"
---
# Canary configuration for testing
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDConfig
metadata:
  name: canary-sbd
  namespace: my-app
spec:
  image: "quay.io/medik8s/sbd-agent:v1.3.0-beta"
  imagePullPolicy: "Always"
  staleNodeTimeout: "30m"
  nodeSelector:
    canary: "true"
```

This creates:

- Shared service account: `my-app/sbd-agent`
- Production DaemonSet: `my-app/sbd-agent-production-sbd`
- Canary DaemonSet: `my-app/sbd-agent-canary-sbd`
- Separate ClusterRoleBindings for each configuration

## Installation

The Storage-based Remediation operator is available in the Red Hat Ecosystem Catalog as a Tech Preview operator. You can install it via Operator Lifecycle Manager (OLM) on OpenShift or standard Kubernetes.

### OpenShift (Recommended)

1. **Install the operator from OperatorHub:**
   - Navigate to **Operators** → **OperatorHub** in the OpenShift web console
   - Search for "Storage Base Remediation" or "Storage-based Remediation" or "SBD"
   - Click **Install** and follow the installation wizard
   - Select your desired namespace (e.g., `openshift-operators`)

2. **Create SecurityContextConstraints (SCC) - Required Workaround:**

   ⚠️ **Known Issue**: The operator cannot create SecurityContextConstraints at runtime. You must create the SCC manually before creating an SBDConfig.

   Apply the following SCC:

   ```yaml
   apiVersion: security.openshift.io/v1
   kind: SecurityContextConstraints
   metadata:
     name: sbd-operator-sbd-agent-privileged
     annotations:
       kubernetes.io/description: SecurityContextConstraints for SBD Agent pods that
         require privileged access to hardware watchdog and block devices
     labels:
       app: sbd-agent
       app.kubernetes.io/component: openshift-resources
       app.kubernetes.io/name: sbd-operator
   allowHostDirVolumePlugin: true
   allowHostIPC: false
   allowHostNetwork: true
   allowHostPID: true
   allowHostPorts: false
   allowPrivilegedContainer: true
   allowedCapabilities:
   - SYS_ADMIN
   - SYS_MODULE
   defaultAddCapabilities: null
   fsGroup:
     type: RunAsAny
   groups: []
   priority: 10
   readOnlyRootFilesystem: false
   requiredDropCapabilities: null
   runAsUser:
     type: RunAsAny
   seLinuxContext:
     type: RunAsAny
   seccompProfiles:
   - '*'
   supplementalGroups:
     type: RunAsAny
   users: []
   volumes:
   - configMap
   - downwardAPI
   - emptyDir
   - hostPath
   - persistentVolumeClaim
   - projected
   - secret
   ```

   ```bash
   oc apply -f scc.yaml
   ```

3. **Verify operator installation:**

   ```bash
   oc get csv -n openshift-operators | grep storage-base-remediation
   oc get pods -n openshift-operators | grep -wE 'storage-base-remediation|sbd'
   ```

### Standard Kubernetes

For standard Kubernetes clusters, you can install the operator using kubectl:

```bash
# Note: Installation manifests may be available in the repository
# Check the repository for the latest installation instructions
# Repository: https://github.com/medik8s/storage-base-remediation
```

## Configuration Reference

### SBDConfig Fields

#### `sbdWatchdogPath` (string, optional)

- **Default**: `/dev/watchdog`
- **Description**: Path to the watchdog device on cluster nodes
- **Notes**:
  - Must exist on all nodes or softdog will be used as fallback
  - Common paths: `/dev/watchdog`, `/dev/watchdog0`, `/dev/watchdog1`

#### `image` (string, **required**)

- **Default**: None (currently must be specified)
- **Description**: Container image for the SBD agent DaemonSet
- **⚠️ Important**: This field is currently **required** due to a known issue where the operator's default image path is invalid
- **Valid Images**:
  - Red Hat: `registry.redhat.io/workload-availability/storage-base-remediation-agent-rhel9:v0.1.0`
  - Community: `quay.io/medik8s/sbd-agent:latest` (if available)
- **Recommended**: Use specific version tags for production
- **Example**: `registry.redhat.io/workload-availability/storage-base-remediation-agent-rhel9:v0.1.0`

**Note**: The namespace for the SBD agent DaemonSet is determined by the `metadata.namespace` field of the SBDConfig resource, not a spec field. The DaemonSet will be deployed in the same namespace as the SBDConfig.

#### `imagePullPolicy` (string, optional)

- **Default**: `IfNotPresent`
- **Description**: Pull policy for the SBD agent container image
- **Valid values**: `Always`, `Never`, `IfNotPresent`
- **Recommended**: Use `IfNotPresent` for production stability; use `Always` when testing rolling image updates

#### `staleNodeTimeout` (duration, optional)

- **Default**: `1h`
- **Range**: `1m` to `24h`
- **Description**: Time before inactive nodes are cleaned up from slot mapping
- **Purpose**: Determines when node slots in shared storage are freed for reuse
- **Format**: Go duration format (e.g., `30m`, `2h`, `90s`)

#### `watchdogTimeout` (duration, optional)

- **Default**: `60s`
- **Range**: `10s` to `300s` (5 minutes)
- **Description**: Timeout for the hardware or software watchdog device
- **Purpose**: How long the system waits before triggering a reboot if the watchdog is not "pet" (refreshed). The agent pets the watchdog at an interval derived from this value and `petIntervalMultiple`.
- **Format**: Go duration format (e.g., `60s`, `2m`)

#### `petIntervalMultiple` (integer, optional)

- **Default**: `4`
- **Range**: `3` to `20`
- **Description**: Multiple used to compute the pet interval from the watchdog timeout
- **Formula**: Pet interval = `watchdogTimeout` / `petIntervalMultiple`
- **Purpose**: Ensures the agent pets the watchdog more often than the timeout, with a safety margin. A value of 4 is a typical balance of safety and efficiency.

#### `logLevel` (string, optional)

- **Default**: `info`
- **Description**: Logging level for the SBD agent pods
- **Valid values**: `debug`, `info`, `warn`, `error`
- **Use**: `debug` for troubleshooting; `info` or `warn` for production

#### `iotimeout` (duration, optional)

- **Default**: `2s`
- **Range**: `100ms` to `5m`
- **Description**: Timeout for SBD I/O operations on shared storage
- **Purpose**: How long the agent waits for reads/writes to the shared SBD device before considering the operation failed
- **Format**: Go duration format (e.g., `2s`, `500ms`)

#### `rebootMethod` (string, optional)

- **Default**: `panic`
- **Description**: Method used for self-fencing when the node must be rebooted
- **Valid values**:
  - `panic`: Immediate kernel panic (fastest failover, no graceful shutdown)
  - `systemctl-reboot`: Graceful `systemctl reboot` (allows services to shut down; may be slower)
  - `none`: Disable agent-initiated reboot; rely only on hardware watchdog timeout
- **Use**: Prefer `panic` for fast recovery; use `systemctl-reboot` when graceful shutdown is required.

#### `sbdTimeoutSeconds` (integer, optional)

- **Default**: `30`
- **Range**: `10` to `300`
- **Description**: SBD timeout in seconds; influences heartbeat and failure detection
- **Purpose**: Heartbeat interval is derived from this (e.g., timeout/2). Lower values detect failures faster but increase I/O on shared storage.

#### `sbdUpdateInterval` (duration, optional)

- **Default**: `5s`
- **Range**: `1s` to `60s`
- **Description**: Interval at which the node writes its status to the shared SBD device
- **Purpose**: More frequent updates improve failure detection and increase I/O load
- **Format**: Go duration format (e.g., `5s`, `10s`)

#### `peerCheckInterval` (duration, optional)

- **Default**: `5s`
- **Range**: `1s` to `60s`
- **Description**: Interval at which the node reads and evaluates peer heartbeats from the SBD device
- **Purpose**: More frequent checks improve peer failure detection and increase I/O load
- **Format**: Go duration format (e.g., `5s`, `10s`)

#### `nodeSelector` (map[string]string, optional)

- **Default**: Worker nodes only (`node-role.kubernetes.io/worker: ""`) when unset
- **Description**: Node label selector that must match for the SBD agent pod to be scheduled
- **Purpose**: Restrict which nodes run the SBD agent (e.g., specific roles, zones, or canary nodes)
- **Note**: Merged with the default requirement `kubernetes.io/os=linux`
- **Example**: `nodeSelector: { "node-role.kubernetes.io/worker": "" }` or `{ "canary": "true" }`

#### `sharedStorageClass` (string, optional)

- **Default**: None (shared storage disabled; use for testing purposes)
- **Description**: StorageClass name for automatic shared storage provisioning
- **Requirements**:
  - StorageClass must support ReadWriteMany (RWX) access mode
  - Storage backend must support POSIX file locking for coordination
- **Examples**: `"efs-sc"`, `"nfs-client"`, `"cephfs"`, `"glusterfs"`
- **Behavior**: When specified, the controller automatically creates a PVC using this StorageClass

The controller automatically chooses a sensible mount path (`/dev/sbd`) for shared storage, eliminating the need for manual configuration and reducing potential conflicts.

### SBDConfig Status Fields

The SBDConfig status is updated by the operator and reflects the observed state of the SBD agent DaemonSet and shared storage. Status is read-only; do not edit it manually.

#### `conditions` (array)

- **Description**: Latest observations of the SBDConfig state, as standard [Kubernetes Condition](https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties) objects
- **Merge semantics**: Conditions are merged by `type` (patch merge key). The controller updates conditions on each reconciliation

**Condition types:**

| Type | When `status: "True"` | When `status: "False"` | Purpose |
| ---- | --------------------- | ---------------------- | ------- |
| **DaemonSetReady** | All desired SBD agent pods are ready on their nodes | No nodes scheduled, or some pods not ready | DaemonSet rollout and pod health |
| **SharedStorageReady** | Shared storage PVC is configured, or shared storage is not used | Not set (always True in practice) | Shared storage availability when `sharedStorageClass` is set |
| **Ready** | DaemonSetReady and SharedStorageReady are both True | Any dependency not ready | Overall SBDConfig readiness for use |

**Typical reason and message values:**

- **DaemonSetReady**
  - True: `reason: "DaemonSetReady"`, `message: "All N SBD agent pods are ready"`
  - False: `reason: "NoNodesScheduled"`, `message: "No nodes are scheduled to run SBD agent pods"` when no pods are scheduled
  - False: `reason: "PodsNotReady"`, `message: "X of Y SBD agent pods are ready"` when some pods are not ready
- **SharedStorageReady**
  - True: `reason: "SharedStorageConfigured"`, `message: "Shared storage PVC '...' is configured"` when shared storage is in use
  - True: `reason: "SharedStorageNotRequired"`, `message: "Shared storage is not configured and not required"` when `sharedStorageClass` is not set
- **Ready**
  - True: `reason: "Ready"`, `message: "SBDConfig is ready and all components are operational"`
  - False: `reason: "NotReady"`, `message: "SBDConfig is not ready: DaemonSet not ready, Shared storage not ready"` (or a subset of those reasons)

**Each condition object contains:**

| Field | Type | Description |
| ----- | ---- | ----------- |
| `type` | string | Condition type: `DaemonSetReady`, `SharedStorageReady`, or `Ready` |
| `status` | string | `"True"`, `"False"`, or `"Unknown"` |
| `reason` | string | Short, CamelCase reason for the last transition (e.g. `PodsNotReady`) |
| `message` | string | Human-readable description of the condition state |
| `lastTransitionTime` | string (RFC3339) | Time of the last change to this condition |
| `observedGeneration` | int64 | `.metadata.generation` of the SBDConfig when this condition was set; used to detect stale status |

#### `readyNodes` (int32)

- **Description**: Number of nodes where the SBD agent pod is ready and running
- **Source**: Set from the DaemonSet’s `status.numberReady`
- **Range**: `0` to `totalNodes`
- **Use**: Compare with `totalNodes` to see rollout progress or detect partial failure (e.g. `readyNodes < totalNodes`)

#### `totalNodes` (int32)

- **Description**: Number of nodes where the SBD agent DaemonSet is supposed to run (desired pod count)
- **Source**: Set from the DaemonSet’s `status.desiredNumberScheduled`
- **Use**: Denominator for readiness (e.g. “X of Y nodes ready”). Zero when no nodes match the DaemonSet’s node selector.

**Example status:**

```yaml
status:
  conditions:
  - type: DaemonSetReady
    status: "True"
    reason: DaemonSetReady
    message: All 3 SBD agent pods are ready
  - type: SharedStorageReady
    status: "True"
    reason: SharedStorageConfigured
    message: Shared storage PVC 'default-shared-storage' is configured
  - type: Ready
    status: "True"
    reason: Ready
    message: SBDConfig is ready and all components are operational
  readyNodes: 3
  totalNodes: 3
```

## Configuration Examples

### Basic Watchdog-Only Configuration

For clusters that only need watchdog-based fencing without shared storage:

```yaml
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDConfig
metadata:
  name: basic-sbd
  namespace: sbd-system  # DaemonSet will be created in this namespace
spec:
  # Image is required - must be specified explicitly
  image: "registry.redhat.io/workload-availability/storage-base-remediation-agent-rhel9:v0.1.0"
  # Use defaults for most other settings
```

### Production Configuration

For production environments with specific requirements:

```yaml
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDConfig
metadata:
  name: production-sbd
  namespace: high-availability  # DaemonSet will be created in this namespace
spec:
  # Specific image version for reproducibility (required)
  image: "registry.redhat.io/workload-availability/storage-base-remediation-agent-rhel9:v0.1.0"
  
  # Custom watchdog device
  sbdWatchdogPath: "/dev/watchdog1"
  
  # Faster cleanup for dynamic environments
  staleNodeTimeout: "30m"
```

### Development/Testing Configuration

For development or testing environments:

```yaml
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDConfig
metadata:
  name: dev-sbd
  namespace: sbd-system
spec:
  # Image is required - use appropriate image for your environment
  image: "registry.redhat.io/workload-availability/storage-base-remediation-agent-rhel9:v0.1.0"
  
  # Faster cleanup for rapid testing
  staleNodeTimeout: "5m"
  
  # Default watchdog path (will use softdog if no hardware watchdog)
  sbdWatchdogPath: "/dev/watchdog"
  
  # Disable self-fencing for testing (rely only on watchdog timeout)
  rebootMethod: "none"
```

### Multi-Cluster Configuration

For environments with multiple clusters:

```yaml
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDConfig
metadata:
  name: cluster-west-sbd
  namespace: sbd-cluster-west
spec:
  image: "registry.redhat.io/workload-availability/storage-base-remediation-agent-rhel9:v0.1.0"
  staleNodeTimeout: "45m"
---
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDConfig
metadata:
  name: cluster-east-sbd
  namespace: sbd-cluster-east
spec:
  image: "registry.redhat.io/workload-availability/storage-base-remediation-agent-rhel9:v0.1.0"
  staleNodeTimeout: "45m"
```

## Deployment and Management

### Create SBDConfig

```bash
# Apply configuration
kubectl apply -f sbdconfig.yaml

# Verify creation
kubectl get sbdconfig
kubectl describe sbdconfig basic-sbd
```

### Monitor Deployment

```bash
# Check SBDConfig status
kubectl get sbdconfig -n <namespace>
kubectl describe sbdconfig <name> -n <namespace>

# Check SBD agent DaemonSet (replace <namespace> with your namespace)
kubectl get daemonset -n <namespace>

# Check agent pods
kubectl get pods -n <namespace> -l app=sbd-agent

# View agent logs
kubectl logs -n <namespace> -l app=sbd-agent

# Check status conditions
kubectl get sbdconfig <name> -n <namespace> -o jsonpath='{.status.conditions[*].type}{"\n"}{.status.conditions[*].status}'
```

### Update Configuration

```bash
# Edit existing configuration
kubectl edit sbdconfig basic-sbd

# Apply updated configuration
kubectl apply -f updated-sbdconfig.yaml
```

### Remove SBDConfig

```bash
# Delete configuration (this will remove the associated DaemonSet)
kubectl delete sbdconfig <name> -n <namespace>

# Verify cleanup
kubectl get pods -n <namespace> -l app=sbd-agent
kubectl get daemonset -n <namespace>
```

**Note**: The shared service account (`sbd-agent`) in the namespace is not automatically deleted when removing an SBDConfig, as other SBDConfigs in the same namespace may still be using it. The service account will be cleaned up when the namespace is deleted or can be manually removed if no longer needed.

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

The Storage-based Remediation operator enables file locking when shared storage is configured. The SBD agent automatically falls back to jitter coordination if file locking is unavailable:

- **File locking enabled**: Enabled by default when shared storage is configured; works with NFS, CephFS, GlusterFS
- **Jitter coordination**: Automatically used as fallback when file locking fails or is unavailable
- **Watchdog-only**: Used when no shared storage is configured

## Shared Storage Configuration

### StorageClass-Based Approach (Recommended)

The Storage-based Remediation operator supports automatic shared storage provisioning using Kubernetes StorageClasses. This approach simplifies configuration and ensures proper PVC lifecycle management.

#### Requirements

- **StorageClass**: Must support ReadWriteMany (RWX) access mode
- **Storage Backend**: POSIX file locking is recommended (NFS, CephFS, GlusterFS, EFS) but not required; the agent automatically falls back to jitter coordination if file locking is unavailable
- **Permissions**: Controller needs permissions to create/manage PVCs

#### Configuration

```yaml
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDConfig
metadata:
  name: shared-storage-example
  namespace: sbd-system
spec:
  # Image is required
  image: "registry.redhat.io/workload-availability/storage-base-remediation-agent-rhel9:v0.1.0"
  watchdogTimeout: "60s"
  
  # Shared storage configuration
  sharedStorageClass: "efs-sc"              # Required: StorageClass name
```

#### How StorageClass Provisioning Works

1. **Automatic PVC Creation**: Controller creates a PVC using the specified StorageClass
2. **RWX Validation**: Ensures the StorageClass supports ReadWriteMany access mode
3. **DaemonSet Integration**: Mounts the PVC in all sbd-agent pods
4. **Coordination**: Enables cross-node coordination via shared storage
5. **Lifecycle Management**: PVC is owned by SBDConfig and cleaned up automatically

#### StorageClass Examples

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
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDConfig
metadata:
  name: custom-storage-sbd
  namespace: sbd-system
spec:
  # Image is required
  image: "registry.redhat.io/workload-availability/storage-base-remediation-agent-rhel9:v0.1.0"
  
  # Use custom StorageClass with specific parameters
  sharedStorageClass: "custom-nfs-sc"
  
  # Faster cleanup for dynamic environments
  staleNodeTimeout: "15m"
```

## Monitoring and Observability

### Prometheus Metrics

The SBD agent exposes metrics on port 8080:

```yaml
# Key metrics to monitor
sbd_agent_status_healthy          # Agent health (1=healthy, 0=unhealthy)
sbd_device_io_errors_total        # I/O errors with shared storage
sbd_watchdog_pets_total           # Successful watchdog pets
sbd_peer_status                   # Peer node status
sbd_self_fenced_total             # Self-fencing events
```

### Health Checks

```bash
# Check overall status
kubectl get sbdconfig -o wide

# Check agent pod health
kubectl get pods -n sbd-system -o wide

# View recent events
kubectl get events -n sbd-system --sort-by='.lastTimestamp'
```

### Log Analysis

```bash
# View agent logs
kubectl logs -n sbd-system -l app=sbd-agent --tail=100

# Follow logs in real-time
kubectl logs -n sbd-system -l app=sbd-agent -f

# Check for specific issues
kubectl logs -n sbd-system -l app=sbd-agent | grep -i error
```

## Troubleshooting

### Common Issues

#### SBD Agent Pods Not Starting

**Symptoms**: Pods in `Pending` or `CrashLoopBackOff` state

**Diagnosis**:

```bash
kubectl describe pods -n <namespace>
kubectl logs -n <namespace> <pod-name>
```

**Common Causes**:

1. **Invalid Image Path (Known Issue)**: If you didn't specify `spec.image`, pods will fail with `ImagePullBackOff` or `ErrImagePull`
   - **Solution**: Always specify `spec.image` explicitly. See [Known Issues and Workarounds](#known-issues-and-workarounds)

2. **Missing SCC (OpenShift Only)**: Pods cannot start without the required SecurityContextConstraints
   - **Solution**: Create the SCC manually before creating SBDConfig. See [Installation](#installation) section

3. **Insufficient privileges**: Ensure SecurityContextConstraints (OpenShift) or PodSecurityPolicy is configured

   - **Solution**: Verify SCC exists and includes the service account:

     ```bash
     oc get scc sbd-operator-sbd-agent-privileged
     oc get scc sbd-operator-sbd-agent-privileged -o yaml | grep -A 5 users
     ```

4. **Missing watchdog device**: Check if `/dev/watchdog` exists on nodes

   - **Solution**:
    For OpenShift (OCP):

     ```bash
     oc debug node/<node-name> -- chroot /host sh -c 'ls -la /dev/watchdog*'
     ```

     For Standard Kubernetes:

     ```bash
     kubectl debug node/<node-name> -it --image=busybox --profile=sysadmin -- chroot /host sh -c 'ls -la /dev/watchdog*'
     ```

   - The agent will automatically fall back to `softdog` if no hardware watchdog is available

5. **Resource constraints**: Verify node resources

   - **Required resources per node for scheduling**: Each SBD agent pod requires at minimum:
     - CPU: 50m (0.05 cores)
     - Memory: 128Mi
   - **Solution**:

     ```bash
     kubectl describe node <node-name>
     ```

#### Watchdog Device Issues

**Symptoms**: Logs showing "failed to open watchdog device"

**Diagnosis**:

```bash
# Check dmesg for watchdog-related messages
kubectl debug node/<node-name> -- dmesg | grep -i watchdog
```

**Solutions**:

- **Hardware watchdog**: Ensure hardware watchdog is enabled in BIOS/UEFI
- **Software fallback**: The agent will automatically try to load `softdog` module
- **Custom path**: Update `sbdWatchdogPath` if watchdog is at non-standard location

#### Shared Storage Issues

**Symptoms**: I/O errors or coordination failures

**Diagnosis**:

```bash
# Check storage connectivity (replace <namespace> with your namespace)
kubectl logs -n <namespace> -l app=sbd-agent | grep -iE "(error|failed|device|node mapping)"

# Verify file locking capability
kubectl logs -n <namespace> -l app=sbd-agent | grep -iE "(file lock|jitter coordination|coordination)"

# Check PVC status
kubectl get pvc -n <namespace>
kubectl describe pvc <pvc-name> -n <namespace>
```

**Solutions**:

- **File locking**: Ensure storage supports POSIX file locking (NFS, CephFS, GlusterFS)
- **Permissions**: Verify read/write access to shared storage
- **Network**: Check network connectivity to storage backend
- **StorageClass**: Verify the StorageClass supports ReadWriteMany (RWX) access mode

#### Node Slot Exhaustion

**Symptoms**: Logs showing "all preferred slots are occupied"

**Diagnosis**:

```bash
# Check number of active nodes
kubectl get nodes

# Review stale node timeout
kubectl get sbdconfig -o yaml | grep staleNodeTimeout
```

**Solutions**:

- **Reduce timeout**: Lower `staleNodeTimeout` for faster cleanup
- **Manual cleanup**: Remove stale node entries if needed
- **Scale considerations**: SBD supports up to 255 nodes per cluster

### Performance Tuning

#### Stale Node Timeout

- **Fast environments**: `5m` - `15m` for rapid node turnover
- **Stable environments**: `30m` - `2h` for long-running workloads
- **Conservative**: `2h` - `24h` for critical production systems

#### Resource Limits

```yaml
# Recommended resource limits for SBD agent
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

The SBD agent requires elevated privileges for:

- **Watchdog access**: Direct hardware device access
- **System reboot**: Ability to trigger node reboot
- **Shared storage**: Access to block devices

### OpenShift Security

For OpenShift, the agent requires SecurityContextConstraints (SCC) to run with the necessary privileges. **Important**: At this time, the SCC must be created manually before creating SBDConfig resources (see [Known Issues and Workarounds](#known-issues-and-workarounds)).

The required SCC is named `sbd-operator-sbd-agent-privileged` and is provided in the [Installation](#installation) section.

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
5. **Backup configuration**: Store SBDConfig in version control

### High Availability

1. **Multiple watchdogs**: Use hardware watchdog with software fallback
2. **Shared storage**: Use redundant storage with file locking
3. **Network redundancy**: Ensure multiple paths to shared storage
4. **Regular testing**: Verify fencing behavior under load

### Operational Procedures

1. **Gradual rollout**: Test configuration changes in development first
2. **Monitoring setup**: Monitor SBD metrics and node health
3. **Incident response**: Have procedures for handling fencing events
4. **Documentation**: Maintain runbooks for common scenarios

## Integration Examples

### With Prometheus Monitoring

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: sbd-agent-metrics
spec:
  selector:
    matchLabels:
      app: sbd-agent
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
- name: sbd-agent
  rules:
  - alert: SBDAgentUnhealthy
    expr: sbd_agent_status_healthy == 0
    for: 1m
    labels:
      severity: critical
    annotations:
      summary: "SBD Agent unhealthy on {{ $labels.instance }}"
      
  - alert: WatchdogPetFailure
    expr: increase(sbd_watchdog_pets_total[5m]) == 0
    for: 2m
    labels:
      severity: warning
    annotations:
      summary: "Watchdog not being petted on {{ $labels.instance }}"
```

## API Reference

For complete API documentation, see:

- [SBDConfig API Types](../api/v1alpha1/sbdconfig_types.go)
- [Generated CRD](../config/crd/bases/medik8s.medik8s.io_sbdconfigs.yaml)
- [Sample Configurations](../config/samples/)

## Repository

The source code for the Storage-based Remediation operator is available at:

- <https://github.com/medik8s/storage-base-remediation>

## Related Documentation

- [SBD Coordination Strategies](sbd-coordination-strategies.md)
- [SBD Agent Prometheus Metrics](sbd-agent-prometheus-metrics.md)
- [Design Documentation](design.md)
- [OpenShift Configuration](../config/openshift/README.md)
