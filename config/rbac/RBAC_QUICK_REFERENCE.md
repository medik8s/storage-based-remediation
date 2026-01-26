# SBD Operator RBAC Quick Reference

## Files Overview

### SBD Agent RBAC (Minimal Permissions)
- `sbd_agent_service_account.yaml` - ServiceAccount for SBD Agent pods
- `sbd_agent_role.yaml` - ClusterRole with read-only permissions
- `sbd_agent_role_binding.yaml` - Binds ServiceAccount to ClusterRole

### SBD Operator RBAC (Orchestration Permissions)
- `sbd_operator_service_account.yaml` - ServiceAccount for SBD Operator
- `sbd_operator_role.yaml` - ClusterRole with management permissions
- `sbd_operator_role_binding.yaml` - Binds ServiceAccount to ClusterRole

## Quick Deployment

```bash
# Deploy all RBAC resources
kubectl apply -k config/rbac/

# Deploy only SBD Agent RBAC
kubectl apply -f config/rbac/sbd_agent_service_account.yaml
kubectl apply -f config/rbac/sbd_agent_role.yaml
kubectl apply -f config/rbac/sbd_agent_role_binding.yaml

# Deploy only SBD Operator RBAC
kubectl apply -f config/rbac/sbd_operator_service_account.yaml
kubectl apply -f config/rbac/sbd_operator_role.yaml
kubectl apply -f config/rbac/sbd_operator_role_binding.yaml
```

## Permission Summary

### SBD Agent Permissions (Read-Only)
| Resource | Permissions | Purpose |
|----------|-------------|---------|
| `pods` | `get`, `list` | Read own pod metadata |
| `nodes` | `get`, `list`, `watch` | Node name to ID mapping |
| `events` | `create`, `patch` | Observability events |

### SBD Operator Permissions (Management)
| Resource | Permissions | Purpose |
|----------|-------------|---------|
| `namespaces` | `create`, `get`, `list`, `patch`, `update`, `watch` | Namespace management |
| `daemonsets` | `create`, `delete`, `get`, `list`, `patch`, `update`, `watch` | Agent deployment |
| `nodes` | `get`, `list`, `watch` | Node information (read-only) |
| `pods` | `get`, `list`, `watch` | Pod monitoring |
| `leases` | `create`, `get`, `list`, `patch`, `update`, `watch` | Leader election |
| `events` | `create`, `patch` | Event emission |
| `sbdconfigs` | Full CRUD + `finalizers`, `status` | Configuration management |
| `sbdremediations` | Full CRUD + `finalizers`, `status` | Fencing operations |

## Security Validation

```bash
# Test SBD Agent permissions
kubectl auth can-i get pods --as=system:serviceaccount:sbd-system:sbd-agent
kubectl auth can-i list nodes --as=system:serviceaccount:sbd-system:sbd-agent
kubectl auth can-i delete nodes --as=system:serviceaccount:sbd-system:sbd-agent  # Should be "no"

# Test SBD Operator permissions
kubectl auth can-i create daemonsets --as=system:serviceaccount:sbd-system:sbd-operator-controller-manager
kubectl auth can-i update sbdremediations/status --as=system:serviceaccount:sbd-system:sbd-operator-controller-manager
kubectl auth can-i delete nodes --as=system:serviceaccount:sbd-system:sbd-operator-controller-manager  # Should be "no"
```

## Troubleshooting

### Common Issues
1. **Permission Denied**: Verify ClusterRole and ClusterRoleBinding are applied
2. **Wrong Namespace**: Ensure ServiceAccounts are in correct namespace (`sbd-system`)
3. **Missing Resources**: Check if CRDs are installed before applying RBAC

### Debug Commands
```bash
# List all SBD-related RBAC
kubectl get clusterroles | grep sbd
kubectl get clusterrolebindings | grep sbd
kubectl get serviceaccounts -n sbd-system | grep sbd

# Check specific permissions
kubectl describe clusterrole sbd-agent-role
kubectl describe clusterrole sbd-operator-manager-role

# Verify bindings
kubectl describe clusterrolebinding sbd-agent-rolebinding
kubectl describe clusterrolebinding sbd-operator-manager-rolebinding
```

## Security Notes

- ✅ **Principle of Least Privilege**: Each component has minimal necessary permissions
- ✅ **No Direct Node Fencing**: Neither component can delete/modify nodes via Kubernetes API
- ✅ **Read-Only Node Access**: Both components only read node information
- ✅ **Isolated Scope**: Permissions limited to SBD system resources
- ✅ **Hardware-Based Fencing**: Actual fencing occurs via SBD block device, not API calls 
