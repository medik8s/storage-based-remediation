# StorageBasedRemediation Controller Usage

The StorageBasedRemediation controller provides automated fencing capabilities for Kubernetes nodes through SBD (STONITH block device) mechanisms.

## Overview

When a node becomes unresponsive or requires manual fencing, create an `StorageBasedRemediation` resource to initiate the fencing process. The controller will:

1. Validate that it's the leader (if leader election is enabled)
2. Map the node name to a numeric node ID
3. Write a fence message to the shared SBD device
4. Monitor and update the remediation status

## Prerequisites

- Shared SBD device accessible by the operator pod
- SBR Agents running on all nodes that need monitoring
- Operator configured with appropriate RBAC permissions

## Basic Example

```yaml
apiVersion: storage-based-remediation.medik8s.io/v1alpha1
kind: StorageBasedRemediation
metadata:
  name: worker-1
  namespace: default
spec: {}
```

The **node to fence** is **`metadata.name`** (it must match the Kubernetes node name). **`spec`** is empty; there is no per-CR `reason` or `timeoutSeconds`.

Fencing completion is monitored for a fixed duration (60 seconds, matching the former omitted `timeoutSeconds` default) in the operator (`DefaultFencingMonitorTimeoutSeconds` in `pkg/controller/storagebasedremediation_controller.go`), not per-CR.

## Configuration Fields

### Spec Fields

- **`spec`** is intentionally empty for `StorageBasedRemediation`.

### NHC, node conditions, and fencing

- SBR agents may set the Node condition **`SBRStorageUnhealthy`** (with status, reason, and message) so remediators such as **NHC** can decide **whether** to create a **`StorageBasedRemediation`**.
- That condition context is **pre-remediation** signaling; it is **not** copied into this CR's **`spec`** and is **not** read again from the Node when the operator writes the fence message.
- The operator writes a **fixed** SBD fence reason on the wire (`FENCE_REASON_MANUAL`); see `writeFenceMessage` in `pkg/controller/storagebasedremediation_controller.go`.

### Status Fields

Status is reported on **`status`** (not a single `phase` / `message` pair). Important fields:

- **conditions**: Standard **`metav1.Condition`** entries (for example **`Ready`**, **`FencingSucceeded`**, **`FencingInProgress`**, **`LeadershipAcquired`**) with `status`, `reason`, `message`, and timestamps. Use `kubectl describe storagebasedremediation <name>` or `-o yaml` to inspect them.
- **nodeID**: Numeric ID assigned to the target node for SBR operations
- **fenceMessageWritten**: Whether the fence message was successfully written to the SBR device
- **operatorInstance**: Which operator instance is handling this remediation
- **lastUpdateTime**: When status was last updated

## Node Name to Node ID Mapping

The controller automatically maps Kubernetes node names to numeric node IDs for SBD operations:

- `node-1` → Node ID 1
- `worker-2` → Node ID 2
- `k8s-node-10` → Node ID 10
- `control-plane-3` → Node ID 3

The mapping extracts the numeric suffix from hyphenated node names. Node IDs must be in the range 1-254 (255 is reserved for the operator).

## Leader Election

When leader election is enabled (default), only the leader operator instance can perform fencing operations. This prevents:

- Race conditions between multiple operators
- Conflicting fencing decisions
- Data corruption from overlapping SBD writes

Non-leader instances will wait and requeue until they become leader or another leader processes the remediation.

## Monitoring and Troubleshooting

### Check Remediation Status

```bash
kubectl get storagebasedremediation -o wide
```

### View Detailed Status

```bash
kubectl describe storagebasedremediation worker-1
```

### Common Issues

1. **Status: WaitingForLeadership**
   - Multiple operator instances are running
   - Check leader election configuration

2. **Status: Failed with "Failed to determine node ID"**
   - Node name doesn't follow expected format (e.g., `node-1`, `worker-2`)
   - Verify node naming convention

3. **Status: Failed with "Failed to initialize SBD device"**
   - SBD device not accessible to operator pod
   - Check volume mounts and device path configuration

## Integration with SBR Agents

The StorageBasedRemediation controller works in conjunction with SBR Agents:

1. **Controller**: Writes fence messages to target node slots
2. **Target Node Agent**: Detects fence message in its slot and initiates self-fencing
3. **Other Agents**: Continue monitoring and can detect if target node goes down

## Environment Variables

The operator supports these environment variables:

- `SBR_DEVICE_PATH`: Path to SBD device (default: `/mnt/sbr-operator-device`)
- `POD_NAME`: Operator pod name (used for operator instance identification)

## Security Considerations

- Only the leader operator can write fence messages
- SBD devices should be properly secured and accessible only to authorized pods
- Fence messages are persistent and survive operator restarts
- Node IDs are validated to prevent targeting invalid slots

## Example Deployment

```yaml
# SBR operator with leader election enabled
apiVersion: apps/v1
kind: Deployment
metadata:
  name: sbr-operator
spec:
  replicas: 3  # Multiple instances for HA
  template:
    spec:
      containers:
      - name: manager
        image: sbr-operator:latest
        args:
        - --leader-elect=true
        env:
        - name: SBR_DEVICE_PATH
          value: "/mnt/sbr-operator-device"
        - name: POD_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        volumeDevices:
        - name: sbr-device
          devicePath: /mnt/sbr-operator-device
      volumes:
      - name: sbr-device
        persistentVolumeClaim:
          claimName: sbr-shared-storage
```

This setup ensures high availability while maintaining safe, coordinated fencing operations across the cluster. 
