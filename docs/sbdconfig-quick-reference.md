# SBDConfig Quick Reference

Quick reference for common SBDConfig operations and configurations.

## Multiple SBDConfig Support

**NEW**: Multiple SBDConfig resources can coexist in the same namespace.

### Quick Commands
```bash
# Deploy multiple configs in same namespace
kubectl apply -f production-sbd.yaml -f canary-sbd.yaml

# List all SBDConfigs in namespace
kubectl get sbdconfig -n my-app

# Check DaemonSets for each config
kubectl get daemonset -n my-app -l app=sbd-agent

# View logs for specific config
kubectl logs -n my-app -l sbdconfig=production-sbd
kubectl logs -n my-app -l sbdconfig=canary-sbd
```

### Resource Naming Pattern
- Service Account: `sbd-agent` (shared)
- DaemonSet: `sbd-agent-{config-name}`
- ClusterRoleBinding: `sbd-agent-{namespace}-{config-name}`

## Basic Operations

## Essential Commands

### Deployment
```bash
# Apply SBDConfig
kubectl apply -f sbdconfig.yaml

# Check status
kubectl get sbdconfig
kubectl get sbdconfig -o wide

# Describe configuration
kubectl describe sbdconfig <name>
```

### Monitoring
```bash
# Check DaemonSet
kubectl get daemonset -n sbd-system

# Check pods
kubectl get pods -n sbd-system -o wide

# View logs
kubectl logs -n sbd-system -l app=sbd-agent
kubectl logs -n sbd-system -l app=sbd-agent -f

# Check events
kubectl get events -n sbd-system --sort-by='.lastTimestamp'
```

### Troubleshooting
```bash
# Debug pod issues
kubectl describe pods -n sbd-system

# Check node watchdog devices
kubectl debug node/<node-name> -- ls -la /dev/watchdog*

# Check metrics
curl http://<node-ip>:8080/metrics
```

## Common Configurations

### Minimal (Watchdog-Only)
```yaml
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDConfig
metadata:
  name: basic-sbd
spec:
  image: "quay.io/medik8s/sbd-agent:v1.0.0"
```

### Production
```yaml
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDConfig
metadata:
  name: production-sbd
spec:
  image: "quay.io/medik8s/sbd-agent:v1.2.3"
  namespace: "high-availability"
  sbdWatchdogPath: "/dev/watchdog1"
  staleNodeTimeout: "30m"
```

### Development
```yaml
apiVersion: medik8s.medik8s.io/v1alpha1
kind: SBDConfig
metadata:
  name: dev-sbd
spec:
  image: "quay.io/medik8s/sbd-agent:latest"
  staleNodeTimeout: "5m"
```

## Field Reference

| Field | Default | Description |
|-------|---------|-------------|
| `image` | `sbd-agent:latest` | Container image for SBD agent |
| `namespace` | `sbd-system` | Deployment namespace |
| `sbdWatchdogPath` | `/dev/watchdog` | Watchdog device path |
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
| `sbd_agent_status_healthy` | Agent health (1=healthy, 0=unhealthy) |
| `sbd_watchdog_pets_total` | Successful watchdog pets |
| `sbd_device_io_errors_total` | I/O errors with shared storage |
| `sbd_peer_status` | Peer node status |
| `sbd_self_fenced_total` | Self-fencing events |

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