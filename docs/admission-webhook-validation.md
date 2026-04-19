# Admission Webhook Validation

The SBR Operator includes a ValidatingAdmissionWebhook that performs validation checks on StorageBasedRemediationConfig resources at admission time (when they are created or updated via the Kubernetes API).

## Features

### Node Selector Overlap Prevention

The webhook prevents multiple StorageBasedRemediationConfigs from having overlapping node selectors, which could cause conflicts in slot assignment and SBD device management.

#### Why This Validation Is Important

In SBD (STONITH Block Device) coordination, each node must be assigned a unique slot ID in the shared storage device. If multiple StorageBasedRemediationConfigs select the same nodes, this could lead to:

- **Slot assignment conflicts**: Multiple StorageBasedRemediationConfigs trying to assign different slot IDs to the same node
- **Split-brain scenarios**: Conflicting remediation decisions from different StorageBasedRemediationConfigs
- **Data corruption**: Overlapping writes to SBD device slots

#### Validation Logic

The webhook considers two node selectors as overlapping if they could potentially select the same set of nodes:

1. **Empty selectors**: If either selector is empty (matches all nodes), there's overlap
2. **Identical selectors**: Selectors with the same key-value pairs overlap
3. **Compatible selectors**: Selectors that don't contradict each other may overlap
4. **Contradictory selectors**: Selectors with different values for the same key don't overlap

#### Examples

**✅ Non-overlapping (allowed):**
```yaml
# StorageBasedRemediationConfig 1
nodeSelector:
  node-role.kubernetes.io/worker: ""

# StorageBasedRemediationConfig 2  
nodeSelector:
  node-role.kubernetes.io/control-plane: ""
```

**❌ Overlapping (rejected):**
```yaml
# StorageBasedRemediationConfig 1
nodeSelector:
  node-role.kubernetes.io/worker: ""

# StorageBasedRemediationConfig 2
nodeSelector:
  node-role.kubernetes.io/worker: ""
```

**❌ Overlapping with empty selector (rejected):**
```yaml
# StorageBasedRemediationConfig 1
nodeSelector: {}  # Empty = matches all nodes

# StorageBasedRemediationConfig 2
nodeSelector:
  zone: "us-west-2a"
```

### Spec Validation

The webhook also validates the StorageBasedRemediationConfig spec fields, ensuring:
- Required fields are present (e.g., `watchdogPath`)
- Field values are within valid ranges
- Configuration consistency

## Benefits of Admission-Time Validation

1. **Immediate Feedback**: Users get validation errors immediately when applying configurations
2. **Prevents Invalid State**: Invalid StorageBasedRemediationConfigs never enter the cluster
3. **Better UX**: Clear error messages via `kubectl apply` rather than checking controller logs
4. **API Consistency**: Validation happens before storage, maintaining data integrity

## Error Messages

When validation fails, you'll see descriptive error messages:

```bash
$ kubectl apply -f overlapping-storagebasedremediationconfig.yaml
error validating data: ValidationError(StorageBasedRemediationConfig): 
node selector validation failed: StorageBasedRemediationConfig node selector overlaps with existing StorageBasedRemediationConfig 'existing-config' in namespace 'sbr-operator-system'. 
Each node can only be managed by one StorageBasedRemediationConfig to prevent slot assignment conflicts. 
Current selector: map[node-role.kubernetes.io/worker:], Conflicting selector: map[node-role.kubernetes.io/worker:]
```

## Configuration

The webhook is automatically deployed with the operator. The webhook configuration includes:

- **Path**: `/validate-storage-based-remediation-medik8s-io-v1alpha1-storagebasedremediationconfig`
- **Operations**: CREATE, UPDATE
- **Failure Policy**: Fail (rejects on webhook failures)
- **Side Effects**: None

## Certificates

The webhook requires TLS certificates for secure communication with the Kubernetes API server. These can be managed through:

1. **Self-signed certificates** (development/testing)
2. **cert-manager** (recommended for production)
3. **Manual certificate management**

See the [cert-manager documentation](https://cert-manager.io/) for production certificate management.

## Troubleshooting

### Webhook Not Working

1. Check webhook pod is running:
   ```bash
   kubectl get pods -n sbr-operator-system
   ```

2. Check webhook configuration:
   ```bash
   kubectl get validatingwebhookconfiguration
   ```

3. Check webhook logs:
   ```bash
   kubectl logs -n sbr-operator-system deployment/sbr-operator-controller-manager
   ```

### Certificate Issues

1. Verify webhook service exists:
   ```bash
   kubectl get service -n sbr-operator-system webhook-service
   ```

2. Check certificate secret:
   ```bash
   kubectl get secret -n sbr-operator-system webhook-server-certs
   ```

### Bypassing Validation (Emergency)

If the webhook is preventing legitimate operations, you can temporarily disable it:

```bash
kubectl delete validatingwebhookconfiguration vstoragebasedremediationconfig.kb.io
```

**⚠️ Warning**: This disables validation entirely. Re-enable as soon as possible.

## Implementation Details

The webhook is implemented in:
- `api/v1alpha1/storagebasedremediationconfig_webhook.go` - Webhook validation logic
- `config/webhook/` - Kubernetes webhook configuration
- `cmd/main.go` - Webhook registration with manager

The validation uses the same business logic as the SBR agent for consistent slot assignment behavior. 
