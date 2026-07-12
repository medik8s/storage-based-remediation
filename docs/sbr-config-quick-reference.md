# StorageBasedRemediationConfig Quick Reference

Quick reference for common StorageBasedRemediationConfig operations and configurations.

## Multiple StorageBasedRemediationConfig Support

**NEW**: Multiple StorageBasedRemediationConfig resources can coexist in the same namespace.

### Quick Commands

```bash
# Deploy multiple configs in same namespace
kubectl apply -f production-sbr.yaml -f canary-sbr.yaml

# List all configs in namespace
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

---

## Essential Commands

### Deployment

```bash
# Apply StorageBasedRemediationConfig
kubectl apply -f sbrconfig.yaml

# Check status
kubectl get storagebasedremediationconfig -n <namespace>
kubectl get storagebasedremediationconfig -o wide -n <namespace>

# Describe configuration
kubectl describe storagebasedremediationconfig <name> -n <namespace>
```

### Monitoring

```bash
# Check DaemonSet
kubectl get daemonset -n <namespace>

# Check pods
kubectl get pods -n <namespace> -o wide

# View logs (app=sbr-agent matches every config in the namespace;
# scope to one config with the storagebasedremediationconfig label)
kubectl logs -n <namespace> -l storagebasedremediationconfig=<config-name>
kubectl logs -n <namespace> -l storagebasedremediationconfig=<config-name> -f

# Check events
kubectl get events -n <namespace> --sort-by='.lastTimestamp'

# Check conditions
kubectl get storagebasedremediationconfig <name> -n <namespace> \
  -o jsonpath='{range .status.conditions[*]}{.type}: {.status} ({.reason}){"\n"}{end}'
```

### Troubleshooting

```bash
# Debug pod issues
kubectl describe pods -n <namespace>

# Check node watchdog devices
# For OpenShift (OCP):
oc debug node/<node-name> -- chroot /host sh -c 'ls -la /dev/watchdog*'
# For Standard Kubernetes:
kubectl debug node/<node-name> -it --image=busybox --profile=sysadmin \
  -- chroot /host sh -c 'ls -la /dev/watchdog*'

# Check metrics (agent does not use hostNetwork; use a Service + port-forward,
# not a direct node-IP curl — see Monitoring and Observability)
kubectl port-forward -n <namespace> svc/sbr-agent-metrics 8082:8082
curl http://localhost:8082/metrics

# Verify SCC ClusterRoleBinding (OpenShift)
oc get clusterrolebinding | grep sbr-agent
```

---

## Common Configurations

### Minimal (Watchdog-Only)

```yaml
apiVersion: storage-based-remediation.medik8s.io/v1alpha1
kind: StorageBasedRemediationConfig
metadata:
  name: basic-sbr
  namespace: sbr-operator-system
spec: {}
```

### Production

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

### Detect-Only (No Fencing)

```yaml
apiVersion: storage-based-remediation.medik8s.io/v1alpha1
kind: StorageBasedRemediationConfig
metadata:
  name: dev-sbr
  namespace: sbr-dev
spec:
  detectOnlyMode: Enabled
```

### Zone-Scoped

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

---

## Field Reference

| Field | Default | Description |
| ----- | ------- | ----------- |
| `watchdogPath` | `/dev/watchdog` | Watchdog device path on nodes |
| `sharedStorageClass` | None | StorageClass for shared storage coordination (RWX required); mounted at `/dev/sbr` |
| `nodeSelector` | Worker nodes | Label selector restricting which nodes run the agent |
| `sbrTimeoutSeconds` | `30` | SBR detection timeout in seconds (10–300) |
| `maxConsecutiveFailures` | `7` | Consecutive watchdog, SBR-device, local-heartbeat, or peer-heartbeat failures before declaring a node unhealthy (2–32) |
| `detectOnlyMode` | `Disabled` | When `Enabled`, detects failures without performing remediation |

**Note**: The namespace is set via `metadata.namespace`. The agent image is managed automatically
by the operator — do not set `spec.image` (the field does not exist in the CRD).

---

## Status Fields

| Field | Type | Description |
| ----- | ---- | ----------- |
| `conditions` | array | Kubernetes-standard conditions array |

### Condition Types

| Type | `status: "True"` means... |
| ---- | ------------------------- |
| `DaemonSetReady` | All desired agent pods are ready |
| `SharedStorageReady` | Shared storage PVC is configured and available, or not used |
| `Ready` | All above conditions are True |

---

## Key Metrics

Exposed on port **8082**:

| Metric | Description |
| ------ | ----------- |
| `sbr_agent_status_healthy` | Agent health (1=healthy, 0=unhealthy) |
| `sbr_watchdog_pets_total` | Successful watchdog pets |
| `sbr_device_io_errors_total` | I/O errors with shared storage |
| `sbr_peer_status` | Peer node status |
| `sbr_self_fenced_total` | Self-fencing events |

---

## Troubleshooting Checklist

### Pod Issues

- [ ] Verify watchdog device exists on nodes (see debug commands above)
- [ ] Check resource limits and node capacity
- [ ] Review pod logs for specific errors
- [ ] On OpenShift: verify ClusterRoleBinding exists (`oc get clusterrolebinding | grep sbr-agent`)

### Watchdog Issues

- [ ] Verify `/dev/watchdog*` exists on nodes
- [ ] Check BIOS/UEFI watchdog settings
- [ ] Confirm softdog module can load as fallback
- [ ] Check `watchdogPath` matches actual device path

### Storage Issues

- [ ] Verify `sharedStorageClass` exists and supports RWX
- [ ] Check file locking support (NFS/CephFS/GlusterFS)
- [ ] Confirm read/write permissions on PVC
- [ ] Review coordination strategy logs

### Tuning Issues

- [ ] Adjust `sbrTimeoutSeconds` if detection is too fast/slow
- [ ] Adjust `maxConsecutiveFailures` if getting spurious fencing events
- [ ] Use `detectOnlyMode: Enabled` to observe without fencing during rollout

---

## Best Practices

### Production Best Practices

- Monitor key metrics (port 8082) with alerts
- Test fencing scenarios regularly
- Tune `sbrTimeoutSeconds` to your storage latency profile
- Roll out with `detectOnlyMode: Enabled` first, then switch to `Disabled`

### Security

- Use minimal required privileges
- Monitor self-fencing events
- Secure metrics endpoint access

### Performance

- Monitor resource usage
- Use appropriate storage for coordination
- Test under load conditions
