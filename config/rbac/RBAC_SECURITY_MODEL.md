# SBR Operator RBAC Security Model

This document outlines the Role-Based Access Control (RBAC) configuration for the SBR (Storage Based Remediation) Operator and its components, designed following the **Principle of Least Privilege**.

## Overview

The SBR Operator system consists of two main components with distinct security requirements:

1. **SBR Operator**: Orchestrates fencing operations and manages SBR Agent deployment
2. **SBR Agent**: Performs actual fencing operations via SBR block device and system panic

## Security Architecture

### Key Security Principles

1. **Separation of Concerns**: Operator handles orchestration, Agent handles execution
2. **Minimal Permissions**: Each component has only the permissions necessary for its function
3. **No Direct Node Modification**: Neither component can directly delete/modify Kubernetes nodes
4. **Block Device Fencing**: Actual fencing occurs through SBR block device, not Kubernetes API
5. **Local System Actions**: Agent uses local system panic/reboot for self-fencing

## SBR Agent RBAC

**ServiceAccount**: `sbr-agent`
**Role**: `sbr-agent-role` (ClusterRole)

### Permissions Granted

| Resource | Verbs | Justification |
|----------|-------|---------------|
| `pods` | `get`, `list` | Read own pod information to determine node name and metadata |
| `nodes` | `get`, `list`, `watch` | Read-only access to resolve node names to node IDs for SBR operations |
| `events` | `create`, `patch` | Emit observability events for monitoring and debugging |

### Permissions Explicitly NOT Granted

- **No pod/node deletion or modification**: Fencing is done via SBR device + local panic
- **No secret/configmap access**: No sensitive data access required
- **No cluster resource modification**: Cannot alter cluster state through Kubernetes API
- **No cross-namespace access**: Limited to necessary cluster-wide read operations

### Security Rationale

The SBR Agent operates with minimal Kubernetes permissions because its primary fencing mechanism works outside the Kubernetes API:

1. **SBR Block Device**: Writes fence messages to shared block device
2. **Local System Panic**: Triggers immediate system reboot when fence message is detected
3. **Watchdog Timer**: Provides additional safety mechanism for unresponsive systems

## SBR Operator RBAC

**ServiceAccount**: `sbr-operator-controller-manager`
**Role**: `sbr-operator-manager-role` (ClusterRole)

### Permissions Granted

| Resource | Verbs | Justification |
|----------|-------|---------------|
| `namespaces` | `create`, `get`, `list`, `patch`, `update`, `watch` | Create and manage sbr-system namespace |
| `daemonsets` | `create`, `delete`, `get`, `list`, `patch`, `update`, `watch` | Deploy and manage SBR Agent across cluster nodes |
| `nodes` | `get`, `list`, `watch` | Read-only access for node name to ID mapping |
| `pods` | `get`, `list`, `watch` | Monitor agent pods and leader election |
| `leases` | `create`, `get`, `list`, `patch`, `update`, `watch` | Coordinate leader election for HA |
| `events` | `create`, `patch`, `list`, `watch` | Emit operational events and monitor cluster events for observability |
| `storagebasedremediationconfigs` | `create`, `delete`, `get`, `list`, `patch`, `update`, `watch` | Manage SBR configuration resources |
| `storagebasedremediationconfigs/finalizers` | `update` | Handle proper cleanup during deletion |
| `storagebasedremediationconfigs/status` | `get`, `patch`, `update` | Update configuration status |
| `storagebasedremediations` | `create`, `delete`, `get`, `list`, `patch`, `update`, `watch` | Process fencing requests |
| `storagebasedremediations/finalizers` | `update` | Handle proper cleanup of fencing operations |
| `storagebasedremediations/status` | `get`, `patch`, `update` | Track fencing operation progress |

### Permissions Explicitly NOT Granted

- **No direct node deletion/modification**: Cannot directly fence nodes via Kubernetes API
- **No secret access**: No access to sensitive cluster data
- **No modification of other operators**: Limited to SBR system scope
- **No privileged resource access**: Cannot modify cluster-critical resources

### Security Rationale

The SBR Operator requires broader permissions than the Agent because it serves as the orchestration layer:

1. **Deployment Management**: Must deploy and manage SBR Agents across all nodes
2. **Fencing Coordination**: Processes fencing requests and coordinates with appropriate agents
3. **Status Tracking**: Maintains comprehensive status of fencing operations
4. **High Availability**: Supports multi-replica deployments with leader election

## Fencing Security Model

### How Fencing Works Securely

1. **Request Initiation**: External system creates `StorageBasedRemediation` CR
2. **Operator Processing**: Operator validates request and identifies target node
3. **Agent Coordination**: Operator coordinates with SBR Agent on target node
4. **Block Device Operation**: Agent writes fence message to SBR block device
5. **Local System Action**: Target node detects fence message and triggers panic/reboot
6. **Watchdog Safety**: Watchdog ensures system reboot even if panic fails

### Security Benefits

- **No Network Dependencies**: Fencing works even with network partitions
- **Hardware-Level Safety**: SBR block device provides reliable fence coordination
- **Immediate Response**: Local panic provides instant fencing action
- **Fail-Safe Design**: Watchdog ensures fencing completion

## Deployment Considerations

### Namespace Security

- **Operator Namespace**: Deploy operator in dedicated `sbr-system` namespace
- **Agent Deployment**: Agents run in `sbr-system` namespace with host privileges
- **Resource Isolation**: SBR resources isolated from other cluster workloads

### Host Access Requirements

The SBR Agent requires privileged access for:
- **Block Device Access**: Direct access to SBR block device
- **Watchdog Device**: Access to `/dev/watchdog` for system monitoring
- **System Calls**: Ability to trigger system panic/reboot

### Network Security

- **No External Dependencies**: SBR system works without external network access
- **Local Communication**: Agent communicates with local hardware only
- **Cluster Communication**: Operator uses standard Kubernetes API only

## Monitoring and Auditing

### Event Emission

Both components emit Kubernetes events for:
- Operational status updates
- Error conditions
- Fencing operation progress
- Configuration changes

### Audit Trail

RBAC permissions ensure all actions are:
- Logged through Kubernetes audit logs
- Traceable to specific service accounts
- Limited to authorized operations
- Compliant with least privilege principle

## Compliance and Best Practices

### Security Standards

- **CIS Kubernetes Benchmark**: RBAC follows CIS security recommendations
- **NIST Guidelines**: Implements defense-in-depth security model
- **Principle of Least Privilege**: Minimal necessary permissions only
- **Separation of Duties**: Clear separation between orchestration and execution

### Operational Security

- **Regular Review**: RBAC permissions should be reviewed regularly
- **Monitoring**: Monitor for unauthorized access attempts
- **Updates**: Keep RBAC aligned with component functionality changes
- **Documentation**: Maintain clear documentation of permission rationale

## Troubleshooting RBAC Issues

### Common Issues

1. **Permission Denied**: Check if service account has required permissions
2. **Resource Not Found**: Verify ClusterRole and ClusterRoleBinding are applied
3. **Cross-Namespace Access**: Ensure ClusterRole is used for cluster-wide access
4. **Status Update Failures**: Check `/status` subresource permissions

### Validation Commands

```bash
# Check SBR Agent permissions
kubectl auth can-i get pods --as=system:serviceaccount:sbr-system:sbr-agent

# Check SBR Operator permissions
kubectl auth can-i create daemonsets --as=system:serviceaccount:sbr-system:sbr-operator-controller-manager

# Verify role bindings
kubectl get clusterrolebindings | grep sbr

# Check service account tokens
kubectl get serviceaccounts -n sbr-system
```

## Conclusion

The SBR Operator RBAC configuration provides a secure, minimal-privilege foundation for cluster node fencing operations. By separating orchestration (Operator) from execution (Agent) and utilizing hardware-level fencing mechanisms, the system maintains security while providing reliable node fencing capabilities.
