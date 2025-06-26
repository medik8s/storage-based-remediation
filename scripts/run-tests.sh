#!/bin/bash

# SBD Operator Test Runner Script
# This script runs smoke or e2e tests for the SBD Operator
# It replaces the test-smoke make target and inlines build-smoke-installer functionality

set -e

# Script configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${PROJECT_ROOT}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Default values
TEST_TYPE="smoke"
TEST_ENVIRONMENT=""
CLEANUP_AFTER_TEST="true"
SKIP_BUILD="false"
SKIP_DEPLOY="false"
VERBOSE="false"
CRC_CLUSTER="sbd-operator-test"

# Environment variables with defaults
QUAY_REGISTRY="${QUAY_REGISTRY:-quay.io}"
QUAY_ORG="${QUAY_ORG:-medik8s}"
TAG="${TAG:-latest}"
VERSION="${VERSION:-latest}"
CONTAINER_TOOL="${CONTAINER_TOOL:-podman}"
KUBECTL="${KUBECTL:-kubectl}"

# Derived variables
OPERATOR_IMG="${OPERATOR_IMG:-sbd-operator}"
AGENT_IMG="${AGENT_IMG:-sbd-agent}"
QUAY_OPERATOR_IMG="${QUAY_OPERATOR_IMG:-${QUAY_REGISTRY}/${QUAY_ORG}/${OPERATOR_IMG}}"
QUAY_AGENT_IMG="${QUAY_AGENT_IMG:-${QUAY_REGISTRY}/${QUAY_ORG}/${AGENT_IMG}}"

# Build information
BUILD_DATE="${BUILD_DATE:-$(date -u +"%Y-%m-%dT%H:%M:%SZ")}"
GIT_COMMIT="${GIT_COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")}"
GIT_DESCRIBE="${GIT_DESCRIBE:-$(git describe --tags --dirty 2>/dev/null || echo "unknown")}"

# Functions
log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

show_usage() {
    cat << EOF
Usage: $0 [OPTIONS]

Run smoke or e2e tests for the SBD Operator

OPTIONS:
    -t, --type TYPE         Test type: 'smoke' or 'e2e' (default: smoke)
    -e, --env ENV           Test environment: 'crc', 'kind', 'cluster' (auto-detected if not specified)
    -c, --no-cleanup        Skip cleanup after successful tests (cleanup is always skipped on failure)
    -b, --skip-build        Skip building container images
    -d, --skip-deploy       Skip deploying operator (assumes already deployed)
    -v, --verbose           Enable verbose output
    -h, --help              Show this help message

ENVIRONMENT VARIABLES:
    QUAY_REGISTRY          Container registry (default: quay.io)
    QUAY_ORG              Container organization (default: medik8s)
    TAG                   Image tag (default: latest)
    CONTAINER_TOOL        Container tool (default: podman)
    KUBECTL               Kubernetes CLI tool (default: kubectl)

TEST ENVIRONMENTS:
    crc                   CodeReady Containers (OpenShift local)
    kind                  Kind (Kubernetes in Docker)
    cluster               Existing Kubernetes/OpenShift cluster

EXAMPLES:
    # Run smoke tests with auto-detected environment
    $0

    # Run e2e tests on existing cluster
    $0 --type e2e --env cluster

    # Run smoke tests on CRC without cleanup (for debugging)
    $0 --type smoke --env crc --no-cleanup

    # Run tests with custom registry
    QUAY_REGISTRY=my-registry.io QUAY_ORG=myorg $0

    # Skip building images (use existing ones)
    $0 --skip-build

EOF
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -t|--type)
            TEST_TYPE="$2"
            shift 2
            ;;
        -e|--env)
            TEST_ENVIRONMENT="$2"
            shift 2
            ;;
        -c|--no-cleanup)
            CLEANUP_AFTER_TEST="false"
            shift
            ;;
        -b|--skip-build)
            SKIP_BUILD="true"
            shift
            ;;
        -d|--skip-deploy)
            SKIP_DEPLOY="true"
            shift
            ;;
        -v|--verbose)
            VERBOSE="true"
            shift
            ;;
        -h|--help)
            show_usage
            exit 0
            ;;
        *)
            log_error "Unknown option: $1"
            show_usage
            exit 1
            ;;
    esac
done

# Validate test type
if [[ "$TEST_TYPE" != "smoke" && "$TEST_TYPE" != "e2e" ]]; then
    log_error "Invalid test type: $TEST_TYPE. Must be 'smoke' or 'e2e'"
    exit 1
fi

# Auto-detect test environment if not specified
if [[ -z "$TEST_ENVIRONMENT" ]]; then
    if command -v crc &> /dev/null && crc status | grep -q "CRC VM.*Running"; then
        TEST_ENVIRONMENT="crc"
        log_info "Auto-detected environment: CRC"
    elif command -v kind &> /dev/null && kind get clusters | grep -q "$CRC_CLUSTER"; then
        TEST_ENVIRONMENT="kind"
        log_info "Auto-detected environment: Kind"
    elif $KUBECTL cluster-info &> /dev/null; then
        TEST_ENVIRONMENT="cluster"
        log_info "Auto-detected environment: existing cluster"
    else
        # Default to CRC for smoke tests, cluster for e2e tests
        if [[ "$TEST_TYPE" == "smoke" ]]; then
            TEST_ENVIRONMENT="crc"
            log_info "Defaulting to CRC environment for smoke tests"
        else
            TEST_ENVIRONMENT="cluster"
            log_info "Defaulting to existing cluster environment for e2e tests"
        fi
    fi
fi

# Validate test environment
if [[ "$TEST_ENVIRONMENT" != "crc" && "$TEST_ENVIRONMENT" != "kind" && "$TEST_ENVIRONMENT" != "cluster" ]]; then
    log_error "Invalid test environment: $TEST_ENVIRONMENT. Must be 'crc', 'kind', or 'cluster'"
    exit 1
fi

# Set verbose output if requested
if [[ "$VERBOSE" == "true" ]]; then
    set -x
fi

log_info "Starting SBD Operator $TEST_TYPE tests"
log_info "Configuration:"
log_info "  Test Type: $TEST_TYPE"
log_info "  Test Environment: $TEST_ENVIRONMENT"
log_info "  Operator Image: $QUAY_OPERATOR_IMG:$TAG"
log_info "  Agent Image: $QUAY_AGENT_IMG:$TAG"
log_info "  Cleanup After Test: $CLEANUP_AFTER_TEST"
log_info "  Skip Build: $SKIP_BUILD"
log_info "  Skip Deploy: $SKIP_DEPLOY"

# Function to check required tools
check_tools() {
    local missing_tools=()
    
    if ! command -v $CONTAINER_TOOL &> /dev/null; then
        missing_tools+=("$CONTAINER_TOOL")
    fi
    
    if ! command -v $KUBECTL &> /dev/null; then
        missing_tools+=("$KUBECTL")
    fi
    
    if ! command -v go &> /dev/null; then
        missing_tools+=("go")
    fi
    
    if ! command -v make &> /dev/null; then
        missing_tools+=("make")
    fi
    
    if [[ "$TEST_ENVIRONMENT" == "crc" ]] && ! command -v crc &> /dev/null; then
        missing_tools+=("crc")
    fi
    
    if [[ "$TEST_ENVIRONMENT" == "kind" ]] && ! command -v kind &> /dev/null; then
        missing_tools+=("kind")
    fi
    
    if [[ ${#missing_tools[@]} -gt 0 ]]; then
        log_error "Missing required tools: ${missing_tools[*]}"
        exit 1
    fi
}

# Function to setup test environment
setup_environment() {
    case "$TEST_ENVIRONMENT" in
        "crc")
            setup_crc_environment
            ;;
        "kind")
            setup_kind_environment
            ;;
        "cluster")
            setup_cluster_environment
            ;;
        *)
            log_error "Unknown test environment: $TEST_ENVIRONMENT"
            exit 1
            ;;
    esac
}

# Function to setup CRC environment
setup_crc_environment() {
    log_info "Setting up CRC environment"
    
    if crc status | grep -q "CRC VM.*Running"; then
        log_info "CRC is already running"
    else
        log_info "Starting CRC cluster..."
        crc start
    fi
    
    log_info "Setting up CRC environment..."
    eval $(crc oc-env)
    oc whoami || {
        log_error "Failed to authenticate with CRC cluster"
        exit 1
    }
    
    log_success "CRC environment setup complete"
}

# Function to setup Kind environment
setup_kind_environment() {
    log_info "Setting up Kind environment"
    
    if ! kind get clusters | grep -q "$CRC_CLUSTER"; then
        log_info "Creating Kind cluster: $CRC_CLUSTER"
        kind create cluster --name "$CRC_CLUSTER"
    else
        log_info "Kind cluster $CRC_CLUSTER already exists"
    fi
    
    # Set kubectl context to kind cluster
    kubectl config use-context "kind-$CRC_CLUSTER"
    
    log_success "Kind environment setup complete"
}

# Function to setup cluster environment for e2e tests
setup_cluster_environment() {
    log_info "Checking cluster connectivity"
    
    if ! $KUBECTL cluster-info &> /dev/null; then
        log_error "Cannot connect to Kubernetes cluster. Please ensure KUBECONFIG is set correctly."
        exit 1
    fi
    
    log_success "Cluster connectivity verified"
}

# Function to cleanup test environment before starting
cleanup_before_tests() {
    log_info "Cleaning up any existing test resources before starting"
    
    # Set up kubectl context based on environment
    case "$TEST_ENVIRONMENT" in
        "crc")
            eval $(crc oc-env) || true
            ;;
        "kind")
            kubectl config use-context "kind-$CRC_CLUSTER" || true
            ;;
        "cluster")
            # Use current context
            ;;
    esac
    
    # Clean up test resources
    $KUBECTL delete sbdconfig --all --ignore-not-found=true || true
    $KUBECTL delete daemonset sbd-agent-test-sbdconfig -n sbd-system --ignore-not-found=true || true
    $KUBECTL delete clusterrolebinding -l app.kubernetes.io/managed-by=sbd-operator --ignore-not-found=true || true
    $KUBECTL delete clusterrole -l app.kubernetes.io/managed-by=sbd-operator --ignore-not-found=true || true
    $KUBECTL delete ns sbd-operator-system --ignore-not-found=true || true
    $KUBECTL delete ns sbd-system --ignore-not-found=true || true
    
    # Clean up OpenShift-specific resources if on CRC
    if [[ "$TEST_ENVIRONMENT" == "crc" ]]; then
        $KUBECTL delete scc sbd-operator-sbd-agent-privileged --ignore-not-found=true || true
        $KUBECTL delete clusterrolebinding sbd-operator-sbd-agent-scc-user --ignore-not-found=true || true
        $KUBECTL delete clusterrole sbd-operator-sbd-agent-scc-user --ignore-not-found=true || true
    fi
    
    # Clean up CRDs
    if [[ -f "bin/kustomize" ]]; then
        ./bin/kustomize build config/crd | $KUBECTL delete --ignore-not-found=true -f - || true
    fi
    
    log_success "Pre-test cleanup completed"
}

# Function to build container images using make
build_images() {
    if [[ "$SKIP_BUILD" == "true" ]]; then
        log_info "Skipping image build as requested"
        return 0
    fi
    
    log_info "Building container images using make"
    
    # Use make to build images
    make build-images
    
    log_success "Container images built successfully"
}

# Function to load/push images based on environment
load_push_images() {
    case "$TEST_ENVIRONMENT" in
        "crc")
            load_images_crc
            ;;
        "kind")
            load_images_kind
            ;;
        "cluster")
            push_images_registry
            ;;
    esac
}

# Function to load images into CRC
load_images_crc() {
    log_info "Loading images into CRC"
    
    # Save images
    $CONTAINER_TOOL save --format docker-archive $QUAY_OPERATOR_IMG:$TAG -o bin/sbd-operator.tar
    $CONTAINER_TOOL save --format docker-archive $QUAY_AGENT_IMG:$TAG -o bin/sbd-agent.tar
    
    # Load into CRC
    eval $(crc podman-env)
    $CONTAINER_TOOL load -i bin/sbd-operator.tar
    $CONTAINER_TOOL load -i bin/sbd-agent.tar
    
    log_success "Images loaded into CRC"
}

# Function to load images into Kind
load_images_kind() {
    log_info "Loading images into Kind cluster"
    
    kind load docker-image $QUAY_OPERATOR_IMG:$TAG --name "$CRC_CLUSTER"
    kind load docker-image $QUAY_AGENT_IMG:$TAG --name "$CRC_CLUSTER"
    
    log_success "Images loaded into Kind cluster"
}

# Function to push images to registry
push_images_registry() {
    if [[ "$SKIP_BUILD" == "true" ]]; then
        log_info "Skipping image push (build was skipped)"
        return 0
    fi
    
    log_info "Pushing images to registry using make"
    
    # Use make to push images
    make push-images
    
    log_success "Images pushed to registry"
}

# Function to build installer (inlined from build-smoke-installer)
build_installer() {
    if [[ "$SKIP_DEPLOY" == "true" ]]; then
        log_info "Skipping installer build as deployment is skipped"
        return 0
    fi
    
    log_info "Building installer manifest"
    
    # Create dist directory
    mkdir -p dist
    
    # Set image in kustomize
    cd config/manager
    ./../../bin/kustomize edit set image controller=$QUAY_OPERATOR_IMG:$TAG
    cd ../..
    
    # Build installer based on environment and test type
    local kustomize_target=""
    if [[ "$TEST_ENVIRONMENT" == "crc" ]]; then
        log_info "Building OpenShift installer with SecurityContextConstraints"
        kustomize_target="test/smoke"
    elif [[ "$TEST_ENVIRONMENT" == "kind" ]]; then
        log_info "Building Kubernetes installer for Kind"
        kustomize_target="config/default"
    else
        log_info "Building installer for existing cluster"
        if [[ "$TEST_TYPE" == "smoke" ]]; then
            kustomize_target="test/smoke"
        else
            kustomize_target="test/e2e"
        fi
    fi
    
    ./bin/kustomize build "$kustomize_target" > dist/install.yaml
    
    log_success "Installer manifest built: dist/install.yaml"
}

# Function to deploy operator
deploy_operator() {
    if [[ "$SKIP_DEPLOY" == "true" ]]; then
        log_info "Skipping operator deployment as requested"
        return 0
    fi
    
    log_info "Deploying operator to cluster"
    
    # Set up kubectl context based on environment
    case "$TEST_ENVIRONMENT" in
        "crc")
            eval $(crc oc-env)
            ;;
        "kind")
            kubectl config use-context "kind-$CRC_CLUSTER"
            ;;
        "cluster")
            # Use current context
            ;;
    esac
    
    $KUBECTL apply -f dist/install.yaml --server-side=true
    
    log_info "Waiting for operator to be ready..."
    $KUBECTL wait --for=condition=ready pod -l control-plane=controller-manager -n sbd-operator-system --timeout=120s || {
        log_error "Operator failed to start, checking logs..."
        $KUBECTL logs -n sbd-operator-system -l control-plane=controller-manager --tail=20 || true
        exit 1
    }
    
    log_success "Operator deployed and ready"
}

# Function to run tests
run_tests() {
    log_info "Running $TEST_TYPE tests"
    
    # Set up kubectl context based on environment
    case "$TEST_ENVIRONMENT" in
        "crc")
            eval $(crc oc-env)
            ;;
        "kind")
            kubectl config use-context "kind-$CRC_CLUSTER"
            ;;
        "cluster")
            # Use current context
            ;;
    esac
    
    # Set environment variables for tests
    export QUAY_REGISTRY
    export QUAY_ORG
    export TAG
    
    # Run the appropriate test suite
    local test_cmd="go test ./test/$TEST_TYPE/ -v"
    if [[ "$VERBOSE" == "true" ]]; then
        test_cmd="$test_cmd -ginkgo.v"
    fi
    
    if $test_cmd; then
        log_success "$TEST_TYPE tests passed"
        return 0
    else
        log_error "$TEST_TYPE tests failed"
        return 1
    fi
}

# Function to cleanup test environment after tests
cleanup_after_tests() {
    if [[ "$CLEANUP_AFTER_TEST" != "true" ]]; then
        log_info "Skipping cleanup as requested"
        return 0
    fi
    
    log_info "Cleaning up test environment"
    
    # Set up kubectl context based on environment
    case "$TEST_ENVIRONMENT" in
        "crc")
            eval $(crc oc-env) || true
            ;;
        "kind")
            kubectl config use-context "kind-$CRC_CLUSTER" || true
            ;;
        "cluster")
            # Use current context
            ;;
    esac
    
    # Clean up test resources
    $KUBECTL delete sbdconfig --all --ignore-not-found=true || true
    $KUBECTL delete daemonset sbd-agent-test-sbdconfig -n sbd-system --ignore-not-found=true || true
    $KUBECTL delete clusterrolebinding -l app.kubernetes.io/managed-by=sbd-operator --ignore-not-found=true || true
    $KUBECTL delete clusterrole -l app.kubernetes.io/managed-by=sbd-operator --ignore-not-found=true || true
    $KUBECTL delete ns sbd-operator-system --ignore-not-found=true || true
    $KUBECTL delete ns sbd-system --ignore-not-found=true || true
    
    # Clean up environment-specific resources
    if [[ "$TEST_ENVIRONMENT" == "crc" ]]; then
        $KUBECTL delete scc sbd-operator-sbd-agent-privileged --ignore-not-found=true || true
        $KUBECTL delete clusterrolebinding sbd-operator-sbd-agent-scc-user --ignore-not-found=true || true
        $KUBECTL delete clusterrole sbd-operator-sbd-agent-scc-user --ignore-not-found=true || true
    fi
    
    # Clean up CRDs
    if [[ -f "bin/kustomize" ]]; then
        ./bin/kustomize build config/crd | $KUBECTL delete --ignore-not-found=true -f - || true
    fi
    
    # Clean up Kind cluster if requested
    if [[ "$TEST_ENVIRONMENT" == "kind" && "${CLEANUP_KIND_CLUSTER:-false}" == "true" ]]; then
        log_info "Destroying Kind cluster: $CRC_CLUSTER"
        kind delete cluster --name "$CRC_CLUSTER" || true
    fi
    
    log_success "Cleanup completed"
}

# Function to ensure required tools are available
ensure_tools() {
    log_info "Ensuring required tools are available"
    
    # Ensure kustomize is available
    if [[ ! -f "bin/kustomize" ]]; then
        make kustomize
    fi
    
    # Ensure controller-gen is available
    if [[ ! -f "bin/controller-gen" ]]; then
        make controller-gen
    fi
    
    # Create bin directory if it doesn't exist
    mkdir -p bin
}

# Main execution flow
main() {
    log_info "SBD Operator Test Runner"
    log_info "========================"
    
    # Check prerequisites
    check_tools
    ensure_tools
    
    # Setup test environment
    setup_environment
    
    # Always cleanup before starting tests
    cleanup_before_tests
    
    # Build and prepare
    build_images
    load_push_images
    build_installer
    
    # Deploy and test
    deploy_operator
    
    # Run tests and handle results
    local test_exit_code=0
    if ! run_tests; then
        test_exit_code=1
        log_warning "Tests failed - skipping cleanup to preserve environment for debugging"
        CLEANUP_AFTER_TEST="false"
    fi
    
    # Cleanup only if tests passed and cleanup is requested
    cleanup_after_tests
    
    # Final status
    if [[ $test_exit_code -eq 0 ]]; then
        log_success "All $TEST_TYPE tests completed successfully!"
    else
        log_error "$TEST_TYPE tests failed!"
    fi
    
    exit $test_exit_code
}

# Handle script interruption - only cleanup if tests haven't failed
cleanup_on_interrupt() {
    log_warning "Script interrupted"
    # Don't cleanup on interrupt to preserve state for debugging
    exit 130
}

trap cleanup_on_interrupt INT TERM

# Run main function
main "$@" 