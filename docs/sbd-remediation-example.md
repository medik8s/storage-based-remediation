# SBDRemediation Controller Usage

The SBDRemediation controller provides automated fencing capabilities for Kubernetes nodes through SBD (Storage-Based Death) mechanisms.

## Overview

When a node becomes unresponsive or requires manual fencing, create an `SBDRemediation` resource to initiate the fencing process. The controller will:

1. Validate that it's the leader (if leader election is enabled)
2. Map the node name to a numeric node ID
3. Write a fence message to the shared SBD device
4. Monitor and update the remediation status

## Prerequisites

- Shared SBD device accessible by the operator pod
- SBD Agents running on all nodes that need monitoring
- Operator configured with appropriate RBAC permissions

## Basic Example

```yaml
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDRemediation
metadata:
  name: fence-worker-1
  namespace: default
spec:
  nodeName: "worker-1"
  reason: HeartbeatTimeout
  timeoutSeconds: 60
```

## Configuration Fields

### Spec Fields

- **nodeName** (required): The name of the Kubernetes node to fence
- **reason** (optional): Why the node needs fencing
  - `HeartbeatTimeout`: Node stopped sending heartbeats
  - `NodeUnresponsive`: Node is unresponsive to health checks
  - `ManualFencing`: Operator-initiated manual fencing
- **timeoutSeconds** (optional): Maximum time to wait before considering fencing failed (30-300 seconds, default: 60)

### Status Fields

- **phase**: Current state of the remediation
  - `Pending`: Remediation queued for processing
  - `WaitingForLeadership`: Waiting for operator to become leader
  - `FencingInProgress`: Fence message being written to SBD device
  - `FencedSuccessfully`: Node successfully fenced
  - `Failed`: Remediation failed
- **message**: Human-readable description of current state
- **nodeID**: Numeric ID assigned to the target node
- **fenceMessageWritten**: Whether fence message was successfully written
- **operatorInstance**: Which operator instance handled the remediation

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
kubectl get sbdremediation -o wide
```

### View Detailed Status

```bash
kubectl describe sbdremediation fence-worker-1
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

## Integration with SBD Agents

The SBDRemediation controller works in conjunction with SBD Agents:

1. **Controller**: Writes fence messages to target node slots
2. **Target Node Agent**: Detects fence message in its slot and initiates self-fencing
3. **Other Agents**: Continue monitoring and can detect if target node goes down

## Environment Variables

The operator supports these environment variables:

- `SBD_DEVICE_PATH`: Path to SBD device (default: `/mnt/sbd-operator-device`)
- `POD_NAME`: Operator pod name (used for operator instance identification)

## Security Considerations

- Only the leader operator can write fence messages
- SBD devices should be properly secured and accessible only to authorized pods
- Fence messages are persistent and survive operator restarts
- Node IDs are validated to prevent targeting invalid slots

## Example Deployment

```yaml
# SBD operator with leader election enabled
apiVersion: apps/v1
kind: Deployment
metadata:
  name: sbd-operator
spec:
  replicas: 3  # Multiple instances for HA
  template:
    spec:
      containers:
      - name: manager
        image: sbd-operator:latest
        args:
        - --leader-elect=true
        env:
        - name: SBD_DEVICE_PATH
          value: "/mnt/sbd-operator-device"
        - name: POD_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        volumeDevices:
        - name: sbd-device
          devicePath: /mnt/sbd-operator-device
      volumes:
      - name: sbd-device
        persistentVolumeClaim:
          claimName: sbd-shared-storage
```

This setup ensures high availability while maintaining safe, coordinated fencing operations across the cluster.
