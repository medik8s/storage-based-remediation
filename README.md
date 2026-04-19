# Storage Based Remediation Operator

A Kubernetes operator for managing STONITH Block Device (SBD) configurations and remediations for high-availability clustering. The operator provides automated node remediation when nodes become unresponsive by leveraging shared block storage for fencing operations.

## Overview

The Storage-based remediator operator implements a cloud-native approach to Storage-Based Death (SBD) for Kubernetes environments where traditional out-of-band management (IPMI, iDRAC) is unavailable. It uses shared block storage to provide reliable node fencing capabilities, ensuring data consistency and preventing split-brain scenarios in stateful workloads.

## Architecture

The operator consists of two main components:

- **SBR Operator**: Manages `StorageBasedRemediationConfig` and `StorageBasedRemediation` custom resources and deploys the SBR agent
- **SBR Agent**: Runs as a DaemonSet on cluster nodes, handling local watchdog operations and shared storage communication

### Key Features

- **Shared Storage Fencing**: Uses CSI block PVs with concurrent multi-node access for inter-node communication
- **Dual Watchdog System**: Combines shared storage watchdog with local kernel watchdog for robust failure detection
- **Kubernetes Integration**: Native CRDs for configuration management and remediation requests
- **Prometheus Metrics**: Built-in monitoring and observability
- **Split-Brain Prevention**: Shared storage arbitration ensures cluster consistency

## Custom Resources

### StorageBasedRemediationConfig
Defines the SBR configuration for the cluster:
- Shared block device PVC name
- Timeout settings
- Watchdog device path
- Node exclusion lists
- Reboot methods

### StorageBasedRemediation
Triggers node remediation operations:
- Target node specification
- Remediation status tracking
- Integration with Medik8s Node Healthcheck Operator

## Quick Start

### Prerequisites
- Kubernetes cluster with CSI driver supporting `volumeMode: Block`
- Shared block storage with concurrent multi-node access (e.g., Ceph RBD, cloud provider shared volumes)
- Cluster nodes with kernel watchdog support

### Installation

1. Install the operator:
```bash
make deploy
```

2. Create a StorageBasedRemediationConfig:
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
make docker-build docker-push IMG=<your-registry>/sbr-operator:tag
```

## Documentation

Comprehensive documentation is available in the `docs/` directory:

- [Design Document](docs/design.md) - Architecture and design principles
- [Blueprint](docs/blueprint.md) - Detailed implementation blueprint
- [User Guide](docs/sbr-config-user-guide.md) - Configuration and usage
- [Webhook Requirements](docs/WEBHOOK-REQUIREMENTS.md) - Admission webhook setup

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

- Go 1.21+
- Kubernetes 1.28+
- Docker/Podman for container builds
- Make for build automation

## License

Licensed under the Apache License 2.0. See [LICENSE](LICENSE) for details.

## Support

For issues, questions, or contributions, please use the GitHub issue tracker.
