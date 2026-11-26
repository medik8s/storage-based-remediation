# Admission Webhook Validation

The SBR Operator includes a ValidatingAdmissionWebhook that performs validation checks on StorageBasedRemediationConfig resources at admission time (when they are created or updated via the Kubernetes API).

## Features

### Planned Node Selector Overlap Prevention

Configuration overlap checks remain separate work. This API update does not implement them.
The following examples describe proposed checks. The installed webhook does not enforce these overlap checks.
The complete design requires checks across namespaces, with effective worker selectors and the Linux restriction.
It also requires controller checks after concurrent creates and Node label changes.

#### Why This Validation Is Important

In SBD (STONITH Block Device) coordination, each node must be assigned a unique slot ID in the shared storage device. If multiple StorageBasedRemediationConfigs select the same nodes, this could lead to:

- **Slot assignment conflicts**: Multiple StorageBasedRemediationConfigs trying to assign different slot IDs to the same node
- **Split-brain scenarios**: Conflicting remediation decisions from different StorageBasedRemediationConfigs
- **Data corruption**: Overlapping writes to SBD device slots

#### Validation Logic

The proposed checks must compare the effective node selections. These examples alone do not define that algorithm:

1. **Empty selectors**: An omitted or empty selector uses the effective worker selector.
2. **Identical selectors**: Selectors with the same key-value pairs overlap
3. **Compatible selectors**: Selectors that don't contradict each other may overlap
4. **Contradictory selectors**: Selectors with different values for the same key don't overlap

#### Examples

**Proposed non-overlapping example:**

```yaml
# StorageBasedRemediationConfig 1
nodeSelector:
  example.com/pool: a

# StorageBasedRemediationConfig 2
nodeSelector:
  example.com/pool: b
```

**Proposed overlapping example:**

```yaml
# StorageBasedRemediationConfig 1
nodeSelector:
  node-role.kubernetes.io/worker: ""

# StorageBasedRemediationConfig 2
nodeSelector:
  node-role.kubernetes.io/worker: ""
```

**Proposed overlap with an omitted worker selector:**

```yaml
# StorageBasedRemediationConfig 1
nodeSelector: {}  # The effective selector uses worker nodes.

# StorageBasedRemediationConfig 2
nodeSelector:
  zone: "us-west-2a"
```

### Spec Validation

The CRD accepts Filesystem, Block, and BlockPerNode modes.
Block and BlockPerNode require a nonempty `sharedStorageClass`.
The CRD checks duration bounds. It limits outage, recovery, policy, boot-unit, and watcher fields to BlockPerNode.
The config webhook applies matching typed checks and preserves StorageClass name checks.

The webhook compares effective modes and duration values on updates.
It permits omission and explicit Filesystem to replace each other.
It rejects changes between distinct effective modes.
It permits valid `ioTimeout` changes.
It rejects changes to effective outage and recovery durations.
Omission and an explicit effective default can replace each other.
Equivalent duration strings, such as `60s` and `1m`, are equal.

The duration getters do not persist defaults or change the object.

The new policy, boot-unit, and watcher fields are mutable.
Shared modes reject their presence, including `bootUnitEnabled: false` and an empty watcher label list.
The policy must be `OnTimeout` or `OnReboot`. Reboot grace and boot-unit timeouts must be positive.
Enabling the boot unit requires an explicit timeout. A disabled unit accepts a valid timeout without using it.
Watcher group size must be at least one. Watcher label keys must be valid and distinct.
The CRD set list rejects duplicate keys. The item pattern checks label-key syntax and bounds the name to 63 characters.
The webhook also enforces the exact 253-character prefix limit.
This prefix check requires the installed and enabled configuration webhook.
The CRD alone permits some longer prefixes.
The installed webhook uses `failurePolicy: Fail`.
Default manager and bundle settings disable webhooks. This API change does not change those settings.
Admission does not check whether selected Nodes have these labels.
BlockPerNode defines the API only. The operator does not implement this mode or apply `ioTimeout` to running agents.
See the [API contract](sbr-config-user-guide.md#storage-mode-and-duration-api) for defaults and bounds.

The remediation CRD uses root CEL rules to protect assigned status identities, policy, offer time, and deadlines.
The rules reject removal of these fields or their parent objects.
The remediation resource does not use a webhook for these checks.

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
