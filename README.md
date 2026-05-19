# Storage Based Remediation Operator

A Kubernetes operator for managing STONITH Block Device (SBD) configurations
and remediations for high-availability clustering. The operator provides
automated node remediation when nodes become unresponsive by leveraging shared
storage for fencing operations.

## Overview

The Storage-based remediation operator implements a cloud-native approach to
Storage-Based Death (SBD) for Kubernetes environments where traditional
out-of-band management (IPMI, iDRAC) is unavailable. It uses shared storage
(ReadWriteMany PVC with a coordination file) to provide reliable node fencing
capabilities, ensuring data consistency and preventing split-brain scenarios in
stateful workloads.

## Architecture

The operator consists of two main components:

- **SBR Operator**: Manages `StorageBasedRemediationConfig` and
  `StorageBasedRemediation` custom resources and deploys the SBR agent
- **SBR Agent**: Runs as a DaemonSet on cluster nodes, handling local watchdog
  operations and shared storage communication

### Key Features

- **Shared Storage Fencing**: Uses ReadWriteMany (RWX) shared storage and a
  coordination file for inter-node communication
- **Dual Watchdog System**: Combines shared storage watchdog with local kernel
  watchdog for robust failure detection
- **Kubernetes Integration**: Native CRDs for configuration management and
  remediation requests
- **Prometheus Metrics**: Built-in monitoring and observability
- **Split-Brain Prevention**: Shared storage arbitration ensures cluster
  consistency

## Custom Resources

### StorageBasedRemediationConfig

Defines the SBR configuration for the cluster:

- Shared storage class (RWX-capable StorageClass)
- Watchdog device path
- Node selection criteria

### StorageBasedRemediation

Triggers node remediation operations:

- Target node specification
- Remediation status tracking
- Integration with Medik8s Node Healthcheck Operator

### StorageBasedRemediationTemplate

Optional template for creating `StorageBasedRemediation` resources with a
predefined spec.

## Quick Start

### Prerequisites

- Kubernetes cluster with a StorageClass that supports **ReadWriteMany** (RWX)
  access mode (for example CephFS, NFS, or AWS EFS)
- Cluster nodes with kernel watchdog support

### Installation

1. Install the operator:

```bash
make deploy
```

1. Create a StorageBasedRemediationConfig:

```bash
kubectl apply -f config/samples/storage-based-remediation_v1alpha1_storagebasedremediationconfig.yaml
```

### Development

Build and test locally:

```bash
# Build the operator
make build

# Run tests
make test

# Run e2e tests
make test-e2e

# Build and push images
make build-images push-images IMG=<your-registry>/storage-based-remediation-operator:tag
# Or: make build-push IMG=<your-registry>/storage-based-remediation-operator:tag
# (build-images, push-images, and update-manifests)
```

## Documentation

Comprehensive documentation is available in the `docs/` directory.

Files under `docs/archive/` are historical: they use older resource names
(for example `SBDConfig`) and may not reflect the current implementation.

- [Design Document](docs/archive/design.md) - Architecture and design
  principles (archive)
- [Blueprint](docs/archive/blueprint.md) - Detailed implementation blueprint
  (archive)
- [User Guide](docs/sbr-config-user-guide.md) - Configuration and usage
- [Webhook Requirements](docs/admission-webhook-validation.md) - Admission
  webhook setup

## Testing

The project includes comprehensive testing:

- **Unit Tests**: `make test`
- **E2E Tests**: `make test-e2e`

E2E tests run against a deployed operator and verify functionality end-to-end.

## Contributing

1. Follow Go best practices and project coding standards
2. Include comprehensive tests for new features
3. Update documentation for user-facing changes
4. Ensure all tests pass before submitting PRs

## Requirements

- Go 1.25+
- Kubernetes 1.34+
- Docker/Podman for container builds
- Make for build automation

## License

Licensed under the Apache License 2.0. See [LICENSE](LICENSE) for details.

## Support

For issues, questions, or contributions, please use the GitHub issue tracker.
