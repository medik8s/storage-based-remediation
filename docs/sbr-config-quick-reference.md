# StorageBasedRemediationConfig Quick Reference

Quick reference for common StorageBasedRemediationConfig operations and configurations.

## Multiple StorageBasedRemediationConfig Support

**NEW**: Multiple StorageBasedRemediationConfig resources can coexist in the same namespace.

### Quick Commands
```bash
# Deploy multiple configs in same namespace
kubectl apply -f production-sbr.yaml -f canary-sbr.yaml

# List all StorageBasedRemediationConfigs in namespace
kubectl get storagebasedremediationconfig -n my-app

# Check DaemonSets for each config
kubectl get daemonset -n my-app -l app=sbr-agent

# View logs for specific config
kubectl logs -n my-app -l storagebasedremediationconfig=production-sbr
kubectl logs -n my-app -l storagebasedremediationconfig=canary-sbr
```

### Resource Naming Pattern
- Service Account: `sbr-agent` (shared)
- DaemonSet: `sbr-agent-{config-name}`
- ClusterRoleBinding: `sbr-agent-{namespace}-{config-name}`

## Basic Operations

## Essential Commands

### Deployment
```bash
# Apply StorageBasedRemediationConfig
kubectl apply -f storagebasedremediationconfig.yaml

# Check status
kubectl get storagebasedremediationconfig
kubectl get storagebasedremediationconfig -o wide

# Describe configuration
kubectl describe storagebasedremediationconfig <name>
```

### Monitoring
```bash
# Check DaemonSet
kubectl get daemonset -n sbr-operator-system

# Check pods
kubectl get pods -n sbr-operator-system -o wide

# View logs
kubectl logs -n sbr-operator-system -l app=sbr-agent
kubectl logs -n sbr-operator-system -l app=sbr-agent -f

# Check events
kubectl get events -n sbr-operator-system --sort-by='.lastTimestamp'
```

### Troubleshooting
```bash
# Debug pod issues
kubectl describe pods -n sbr-operator-system

# Check node watchdog devices
kubectl debug node/<node-name> -- ls -la /dev/watchdog*

# Check metrics
curl http://<node-ip>:8080/metrics
```

## Common Configurations

### Minimal (Watchdog-Only)
```yaml
apiVersion: storage-based-remediation.medik8s.io/v1alpha1
kind: StorageBasedRemediationConfig
metadata:
  name: basic-sbr
spec:
  image: "quay.io/medik8s/storage-based-remediation-agent:v1.0.0"
```

### Production
```yaml
apiVersion: storage-based-remediation.medik8s.io/v1alpha1
kind: StorageBasedRemediationConfig
metadata:
  name: production-sbr
spec:
  image: "quay.io/medik8s/storage-based-remediation-agent:v1.2.3"
  namespace: "high-availability"
  watchdogPath: "/dev/watchdog1"
  staleNodeTimeout: "30m"
```

### Development
```yaml
apiVersion: storage-based-remediation.medik8s.io/v1alpha1
kind: StorageBasedRemediationConfig
metadata:
  name: dev-sbr
spec:
  image: "quay.io/medik8s/storage-based-remediation-agent:latest"
  staleNodeTimeout: "5m"
```

## Field Reference

| Field | Default | Description |
|-------|---------|-------------|
| `image` | `sbr-agent:latest` | Container image for SBR agent |
| `namespace` | `sbr-operator-system` | Deployment namespace |
| `watchdogPath` | `/dev/watchdog` | Watchdog device path |
| `staleNodeTimeout` | `1h` | Node cleanup timeout |

## Status Fields

| Field | Type | Description |
|-------|------|-------------|
| `daemonSetReady` | boolean | DaemonSet ready status |
| `readyNodes` | int32 | Number of ready nodes |
| `totalNodes` | int32 | Total target nodes |

## Key Metrics

| Metric | Description |
|--------|-------------|
| `sbr_agent_status_healthy` | Agent health (1=healthy, 0=unhealthy) |
| `sbr_watchdog_pets_total` | Successful watchdog pets |
| `sbr_device_io_errors_total` | I/O errors with shared storage |
| `sbr_peer_status` | Peer node status |
| `sbr_self_fenced_total` | Self-fencing events |

## Troubleshooting Checklist

### Pod Issues
- [ ] Check SecurityContextConstraints (OpenShift)
- [ ] Verify watchdog device exists on nodes
- [ ] Check resource limits and node capacity
- [ ] Review pod logs for specific errors

### Watchdog Issues  
- [ ] Verify `/dev/watchdog*` exists on nodes
- [ ] Check BIOS/UEFI watchdog settings
- [ ] Confirm softdog module can load
- [ ] Test custom watchdog paths

### Storage Issues
- [ ] Verify shared storage connectivity
- [ ] Check file locking support (NFS/CephFS/GlusterFS)
- [ ] Confirm read/write permissions
- [ ] Review coordination strategy logs

## Best Practices

### Production
- Use specific image versions (not `latest`)
- Set appropriate `staleNodeTimeout` for environment
- Monitor key metrics with alerts
- Test fencing scenarios regularly

### Security
- Use minimal required privileges
- Monitor self-fencing events
- Secure metrics endpoint access
- Regular security updates

### Performance
- Choose optimal `staleNodeTimeout`
- Monitor resource usage
- Use appropriate storage for coordination
- Test under load conditions 