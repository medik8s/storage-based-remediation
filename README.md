# SBD Operator

The SBD (STONITH Block Device) operator provides watchdog-based fencing for Kubernetes clusters to ensure high availability through automatic node remediation.

## Documentation

### User Documentation
- **[SBDConfig User Guide](docs/sbdconfig-user-guide.md)** - Complete configuration reference and examples
- **[Quick Reference](docs/sbdconfig-quick-reference.md)** - Essential commands and common configurations
- **[Sample Configuration](config/samples/medik8s_v1alpha1_sbdconfig.yaml)** - Annotated configuration examples

### Technical Documentation
- **[Coordination Strategies](docs/sbd-coordination-strategies.md)** - File locking and coordination mechanisms
- **[Design Documentation](docs/design.md)** - Architecture and design decisions
- **[Concurrent Writes Analysis](docs/concurrent-writes-analysis.md)** - Storage coordination analysis

## Installation

### Standard Kubernetes Installation

```bash
# Build and install the operator
make build-installer
kubectl apply -f dist/install.yaml
```

### OpenShift Installation

For OpenShift clusters, use the OpenShift-specific installer that includes the required SecurityContextConstraints:

```bash
# Build and install the operator with OpenShift support
make build-openshift-installer
kubectl apply -f dist/install-openshift.yaml
```

The OpenShift installer includes:
- All standard operator resources (CRDs, RBAC, deployment)
- SecurityContextConstraints for SBD Agent privileged access
- Proper service account bindings for OpenShift security model

For more details on OpenShift-specific configuration, see [config/openshift/README.md](config/openshift/README.md).

## Building and Pushing Images

### Build Images Locally

To build both images locally (without pushing):

```bash
# Build both images with Quay tags
make quay-build

# Build with a specific version
make quay-build VERSION=v1.2.3
```

### Build and Push to Quay Registry

**First, authenticate with the registry:**

```bash
# Login to Quay
docker login quay.io
```

Then build and push:

```bash
# Build and push with default settings (latest tag)
make quay-build-push

# Build and push with a specific version
make quay-build-push VERSION=v1.2.3

# Build and push to a different registry/organization
make quay-build-push QUAY_REGISTRY=your-registry.com QUAY_ORG=your-org VERSION=v1.2.3

# Build and push multi-platform images (linux/amd64, linux/arm64, etc.)
make quay-buildx VERSION=v1.2.3
```

### Authentication

For pushing images, you need to authenticate with the container registry:

```bash
# For Quay.io
docker login quay.io

# For other registries
docker login your-registry.com
```

### Configuration Variables

The following variables can be customized:

- `QUAY_REGISTRY`: Registry URL (default: `quay.io`)
- `QUAY_ORG`: Organization name (default: `medik8s`)
- `VERSION`: Image tag version (default: `latest`)
- `PLATFORMS`: Target platforms for multi-arch builds (default: `linux/arm64,linux/amd64,linux/s390x,linux/ppc64le`)

### Individual Image Building

You can also build images individually:

```bash
# Build operator image only
make docker-build

# Build SBD agent image only
make docker-build-agent

# Build both binaries locally
make build build-agent
```

## Running Tests

The SBD operator includes comprehensive testing through both **smoke tests** and **end-to-end (e2e) tests**. Tests can be run using either Make targets or the unified test runner script.

### Test Runner Script

The `scripts/run-tests.sh` script provides a unified interface for running both smoke and e2e tests with flexible configuration options:

```bash
# Run smoke tests with auto-detected environment
scripts/run-tests.sh

# Run e2e tests with auto-detected environment
scripts/run-tests.sh --type e2e

# Run tests on specific environments
scripts/run-tests.sh --type smoke --env crc      # CRC (OpenShift local)
scripts/run-tests.sh --type e2e --env kind       # Kind (Kubernetes in Docker)  
scripts/run-tests.sh --type e2e --env cluster    # Existing cluster

# Run tests without cleanup (useful for debugging)
scripts/run-tests.sh --type smoke --no-cleanup

# Build images and run tests
scripts/run-tests.sh --build

# Run with verbose output
scripts/run-tests.sh --verbose

# Custom registry settings
QUAY_REGISTRY=my-registry.io QUAY_ORG=myorg scripts/run-tests.sh --type e2e
```

### Make Targets

For convenience, the following Make targets are available:

```bash
# Smoke tests
make test-smoke                    # Auto-detect environment (uses existing images)
make test-smoke-crc               # CRC (OpenShift local)
make test-smoke-kind              # Kind (Kubernetes in Docker)
make test-smoke-no-cleanup        # Skip cleanup after tests
make test-smoke-build             # Build images before testing

# E2E tests  
make test-e2e                     # Auto-detect environment (uses existing images)
make test-e2e-crc                 # CRC (OpenShift local)
make test-e2e-kind                # Kind (Kubernetes in Docker)
make test-e2e-cluster             # Existing cluster
make test-e2e-no-cleanup          # Skip cleanup after tests  
make test-e2e-build               # Build images before testing
```

### Test Types

#### Smoke Tests
- **Purpose**: Quick validation of core functionality
- **Environment**: CRC (CodeReady Containers) with OpenShift
- **Duration**: ~5-10 minutes
- **Use case**: Development validation, CI/CD pipelines

#### E2E Tests  
- **Purpose**: Comprehensive integration testing
- **Environment**: Any Kubernetes/OpenShift cluster
- **Duration**: ~15-30 minutes
- **Use case**: Release validation, full feature testing

### Prerequisites

#### For Smoke Tests (CRC)
1. **Install CRC**: Download from [Red Hat Developers](https://developers.redhat.com/products/codeready-containers/download)
2. **Setup CRC**: Run `crc setup` once to configure CRC with appropriate resources
3. **Available disk space**: Ensure sufficient disk space as CRC will be stopped/started

#### For E2E Tests
1. **Kubernetes cluster**: Any Kubernetes 1.21+ or OpenShift 4.8+ cluster
2. **kubectl/oc**: Cluster CLI tool configured and authenticated
3. **KUBECONFIG**: Set to point to your target cluster

### What the Tests Do

Both test types automatically:

1. **Build container images** (operator and agent)
2. **Load/push images** to the target environment
3. **Generate installation manifests** with proper SecurityContextConstraints
4. **Deploy the operator** with all required RBAC and resources
5. **Wait for operator readiness**
6. **Run the test suite** with comprehensive validation
7. **Clean up resources** (unless cleanup is skipped)

### Environment Variables

The following environment variables can be used to customize test execution:

- `QUAY_REGISTRY`: Container registry (default: `quay.io`)
- `QUAY_ORG`: Organization/namespace (default: `medik8s`)
- `TAG`: Image tag (default: `latest`)
- `CONTAINER_TOOL`: Container tool (default: `podman`)
- `KUBECTL`: Kubernetes CLI tool (default: `kubectl`)

### OpenShift vs Kubernetes Differences

When running on OpenShift, the tests automatically handle:

- **Security Context Constraints (SCC)** for privileged pod access
- **OpenShift Routes** for ingress (if applicable)
- **OpenShift Registry** integration
- **oc** command usage where appropriate

The tests use environment-specific installers that include all necessary permissions and resources for the target platform.

## AWS OpenShift Cluster Provisioning

For testing on real OpenShift clusters, the SBD operator includes automated provisioning scripts for AWS:

### Automatic Tool Installation

The provisioning script automatically downloads and installs required tools if they're missing:

- **AWS CLI v2** (with platform-specific installation)
- **openshift-install** (configurable version)
- **oc CLI** (includes kubectl)
- **jq** (latest version)

Tools are installed to `.tools/bin` directory and automatically added to PATH.

### Quick Start

```bash
# Provision cluster with defaults (auto-installs missing tools)
make provision-ocp-aws

# Provision cluster with custom configuration
make provision-ocp-aws OCP_CLUSTER_NAME=my-test-cluster OCP_WORKER_COUNT=5

# Run multi-node e2e tests on existing cluster
make test-e2e-multinode

# Full workflow: provision cluster and run tests
make provision-and-test-multinode
```

### Prerequisites

1. **AWS Credentials**: Configure AWS credentials (script will prompt to run `aws configure`)
2. **Red Hat Pull Secret**: Download from [Red Hat Console](https://console.redhat.com/openshift/install/pull-secret)
   - Save as `~/.docker/config.json` or `~/.config/containers/auth.json`

### Configuration Variables

```bash
# Cluster configuration
OCP_CLUSTER_NAME=sbd-operator-test    # Cluster name
AWS_REGION=us-east-1                  # AWS region
OCP_WORKER_COUNT=4                    # Number of worker nodes (minimum 3)
OCP_INSTANCE_TYPE=m5.large           # EC2 instance type
OCP_VERSION=4.18                     # OpenShift version

# Example: Provision 5-node cluster in us-west-2
make provision-ocp-aws \
  OCP_CLUSTER_NAME=sbd-west-test \
  AWS_REGION=us-west-2 \
  OCP_WORKER_COUNT=5 \
  OCP_INSTANCE_TYPE=m5.xlarge
```

### Advanced Usage

```bash
# Skip automatic tool installation (use existing tools)
scripts/provision-ocp-aws.sh --skip-tool-install

# Clean up existing cluster directory before provisioning
scripts/provision-ocp-aws.sh --cleanup --cluster-name my-cluster

# Get help and see all options
scripts/provision-ocp-aws.sh --help
```

### Multi-Node E2E Testing

The multi-node e2e tests automatically adapt to your cluster size:

```bash
# Run on existing OpenShift cluster (discovers topology automatically)
make test-e2e-multinode

# Tests adapt based on available worker nodes:
# - 3 nodes: Basic SBD configuration tests
# - 4+ nodes: Node failure simulation tests  
# - 5+ nodes: Large cluster coordination tests
```

### Cleanup

```bash
# Destroy the cluster
make destroy-ocp-aws OCP_CLUSTER_NAME=my-test-cluster

# Or use openshift-install directly
openshift-install destroy cluster --dir cluster/my-test-cluster
```

The provisioning creates a `cluster/` directory with all cluster artifacts, kubeconfig, and connection information.