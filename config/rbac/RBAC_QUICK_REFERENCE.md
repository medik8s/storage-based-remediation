# SBR Operator RBAC Quick Reference

## Files Overview

### SBR Agent RBAC (Minimal Permissions)
- `sbr_agent_service_account.yaml` - ServiceAccount for SBR Agent pods
- `sbr_agent_role.yaml` - ClusterRole with read-only permissions
- `sbr_agent_role_binding.yaml` - Binds ServiceAccount to ClusterRole

### SBR Operator RBAC (Orchestration Permissions)
- `sbr_operator_service_account.yaml` - ServiceAccount for SBR Operator
- `sbr_operator_role.yaml` - ClusterRole with management permissions
- `sbr_operator_role_binding.yaml` - Binds ServiceAccount to ClusterRole

## Quick Deployment

```bash
# Deploy all RBAC resources
kubectl apply -k config/rbac/

# Deploy only SBR Agent RBAC
kubectl apply -f config/rbac/sbr_agent_service_account.yaml
kubectl apply -f config/rbac/sbr_agent_role.yaml
kubectl apply -f config/rbac/sbr_agent_role_binding.yaml

# Deploy only SBR Operator RBAC
kubectl apply -f config/rbac/sbr_operator_service_account.yaml
kubectl apply -f config/rbac/sbr_operator_role.yaml
kubectl apply -f config/rbac/sbr_operator_role_binding.yaml
```

## Permission Summary

### SBR Agent Permissions (Read-Only)
| Resource | Permissions | Purpose |
|----------|-------------|---------|
| `pods` | `get`, `list` | Read own pod metadata |
| `nodes` | `get`, `list`, `watch` | Node name to ID mapping |
| `events` | `create`, `patch` | Observability events |

### SBR Operator Permissions (Management)
| Resource | Permissions | Purpose |
|----------|-------------|---------|
| `namespaces` | `create`, `get`, `list`, `patch`, `update`, `watch` | Namespace management |
| `daemonsets` | `create`, `delete`, `get`, `list`, `patch`, `update`, `watch` | Agent deployment |
| `nodes` | `get`, `list`, `watch` | Node information (read-only) |
| `pods` | `get`, `list`, `watch` | Pod monitoring |
| `leases` | `create`, `get`, `list`, `patch`, `update`, `watch` | Leader election |
| `events` | `create`, `patch` | Event emission |
| `storagebasedremediationconfigs` | Full CRUD + `finalizers`, `status` | Configuration management |
| `storagebasedremediations` | Full CRUD + `finalizers`, `status` | Fencing operations |

## Security Validation

```bash
# Test SBR Agent permissions
kubectl auth can-i get pods --as=system:serviceaccount:sbr-system:sbr-agent
kubectl auth can-i list nodes --as=system:serviceaccount:sbr-system:sbr-agent
kubectl auth can-i delete nodes --as=system:serviceaccount:sbr-system:sbr-agent  # Should be "no"

# Test SBR Operator permissions
kubectl auth can-i create daemonsets --as=system:serviceaccount:sbr-system:sbr-operator-controller-manager
kubectl auth can-i update storagebasedremediations/status --as=system:serviceaccount:sbr-system:sbr-operator-controller-manager
kubectl auth can-i delete nodes --as=system:serviceaccount:sbr-system:sbr-operator-controller-manager  # Should be "no"
```

## Troubleshooting

### Common Issues
1. **Permission Denied**: Verify ClusterRole and ClusterRoleBinding are applied
2. **Wrong Namespace**: Ensure ServiceAccounts are in correct namespace (`sbr-system`)
3. **Missing Resources**: Check if CRDs are installed before applying RBAC

### Debug Commands
```bash
# List all SBR-related RBAC
kubectl get clusterroles | grep sbr
kubectl get clusterrolebindings | grep sbr
kubectl get serviceaccounts -n sbr-system | grep sbr

# Check specific permissions
kubectl describe clusterrole sbr-agent-role
kubectl describe clusterrole sbr-operator-manager-role

# Verify bindings
kubectl describe clusterrolebinding sbr-agent-rolebinding
kubectl describe clusterrolebinding sbr-operator-manager-rolebinding
```

## Security Notes

- **Principle of Least Privilege**: Each component has minimal necessary permissions
- **No Direct Node Fencing**: Neither component can delete/modify nodes via Kubernetes API
- **Read-Only Node Access**: Both components only read node information
- **Isolated Scope**: Permissions limited to SBR system resources
- **Hardware-Based Fencing**: Actual fencing occurs via SBR block device, not API calls
