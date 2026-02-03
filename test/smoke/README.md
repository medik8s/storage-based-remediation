# SBD Operator Smoke Tests

This directory contains smoke tests for the SBD Operator that validate basic functionality in a real Kubernetes cluster environment.

## Prerequisites

- Access to a Kubernetes cluster (OpenShift recommended for full feature testing)
- `kubectl` configured to access the cluster
- CertManager installed (or set `CERT_MANAGER_INSTALL_SKIP=true` if already present)
- Container registry access for pushing test images

## Running Smoke Tests

### Using the Makefile (Recommended)

```bash
# Run smoke tests with automatic setup
make test-smoke
```

This will:
1. Build and push operator and agent images
2. Deploy the operator to the cluster
3. Run all smoke tests
4. Clean up resources

### Manual Execution

```bash
# Set environment variables (optional)
export OPERATOR_IMG=quay.io/your-org/sbd-operator:test
export AGENT_IMG=quay.io/your-org/sbd-agent:test

# Run tests directly
cd test/smoke
ginkgo -v .
```

## Test Structure

The smoke tests validate:

1. **Manager Functionality**
   - Controller pod startup and health
   - Metrics endpoint availability

2. **SBD Configuration**
   - SBDConfig resource creation and processing
   - DaemonSet deployment with correct configuration
   - Pod startup and readiness
   - Status updates

3. **SBD Remediation**
   - StorageBasedRemediation resource handling
   - Integration with SBD protocol

## Finalizer Considerations

The SBD Operator uses finalizers for proper cleanup of shared resources. This affects test behavior:

- **Resource Creation**: After creating an SBDConfig, the controller adds a finalizer and then creates resources in a subsequent reconciliation cycle
- **Resource Deletion**: When deleting an SBDConfig, the controller performs cleanup before removing the finalizer
- **Timing**: Tests use `Eventually` with appropriate timeouts to account for these reconciliation cycles

## Troubleshooting

### Test Failures

If tests fail, check:

1. **Cluster Access**: Ensure `kubectl cluster-info` works
2. **Operator Logs**: Check controller manager pod logs in `sbd-operator-system` namespace
3. **Resource Status**: Verify SBDConfig and DaemonSet status fields
4. **Events**: Check Kubernetes events for error details

### Common Issues

- **Image Pull Errors**: Ensure test images are available in the specified registry
- **RBAC Issues**: Verify the operator has necessary permissions
- **Node Compatibility**: SBD agents require privileged access and specific node capabilities

### Cleanup

Tests automatically clean up resources, but if manual cleanup is needed:

```bash
# Delete test SBDConfigs
kubectl delete sbdconfig --all -n sbd-test

# Delete test namespace
kubectl delete namespace sbd-test

# Uninstall operator (if needed)
kubectl delete namespace sbd-operator-system
```

## Environment Variables

- `OPERATOR_IMG`: Override operator image (default: derived from QUAY_* vars)
- `AGENT_IMG`: Override agent image (default: derived from QUAY_* vars)
- `CERT_MANAGER_INSTALL_SKIP`: Skip CertManager installation if already present
- `QUAY_REGISTRY`: Container registry (default: localhost:5000)
- `QUAY_ORG`: Registry organization (default: sbd-operator)
- `TAG`: Image tag (default: smoke-test) 