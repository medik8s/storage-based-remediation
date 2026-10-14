#!/bin/bash
# local-run.sh — Replicate the Kind e2e GitHub Actions workflow locally
#
# Uses local checkouts of SBR and tools instead of cloning from GitHub.
# Assumes standard medik8s directory layout:
#   medik8s/storage-based-remediation  (this repo)
#   medik8s/tools
#
# NFS CSI storage requires a Linux host with the nfsd kernel module. It will
# NOT work under Docker Desktop or rootless podman (LinuxKit/macOS kernels).
# The script fails early if rootful container support is unavailable.
#
# Usage:
#   ./hack/local-run.sh              # Full run (setup + build + deploy + test)
#   ./hack/local-run.sh --skip-setup # Skip cluster creation (reuse existing)
#   ./hack/local-run.sh --skip-build # Skip build and deploy (reuse existing)
#   ./hack/local-run.sh --teardown   # Tear down the cluster

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SBR_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
TOOLS_DIR="${TOOLS_DIR:-${SBR_DIR}/../tools}"
# Only export TOOLS_DIR if it exists; otherwise the Makefile auto-clones tools into .tools.
if [ ! -d "${TOOLS_DIR}" ]; then
    unset TOOLS_DIR
fi
# Resolve the tools dir for direct script calls (make targets handle TOOLS_DIR themselves).
TOOLS_DIR_RESOLVED="${TOOLS_DIR:-${SBR_DIR}/.tools}"

# --- Container tool: prefer podman (rootful), fall back to docker ---
if [ -z "${CONTAINER_TOOL:-}" ]; then
    if command -v podman &>/dev/null; then
        export CONTAINER_TOOL=podman
    elif command -v docker &>/dev/null; then
        export CONTAINER_TOOL=docker
    else
        echo "Error: neither podman nor docker found in PATH"
        exit 1
    fi
fi
export CONTAINER_TOOL

# Rootful containers are required for watchdog mknod and NFS privileged pods.
# On macOS, podman delegates to a VM; the machine itself is rootful by default, so skip the check.
if [ "${CONTAINER_TOOL}" = "podman" ] && [[ "$(uname)" != "Darwin" ]]; then
    if podman info --format '{{.Host.Security.Rootless}}' 2>/dev/null | grep -q '^true$'; then
        echo "Error: rootless podman detected. Rootful containers are required."
        echo "       Run as root, use 'sudo podman', or switch to a rootful podman socket."
        exit 1
    fi
fi

# --- Configuration (mirrors GitHub Actions env) ---
export MEDIK8S_CLUSTER_NAME="${MEDIK8S_CLUSTER_NAME:-medik8s-ci}"
export MEDIK8S_REGISTRY_NAME="${MEDIK8S_REGISTRY_NAME:-kind-registry}"
export MEDIK8S_REGISTRY_PORT="${MEDIK8S_REGISTRY_PORT:-5000}"
export IMAGE_REGISTRY="${IMAGE_REGISTRY:-${MEDIK8S_REGISTRY_NAME}:${MEDIK8S_REGISTRY_PORT}/medik8s}"
export OPM_RENDER_FLAGS="${OPM_RENDER_FLAGS:---skip-tls-verify}"
export OPERATOR_NAMESPACE="${OPERATOR_NAMESPACE:-sbr-operator-system}"
[ -n "${TOOLS_DIR:-}" ] && export TOOLS_DIR

SKIP_SETUP=false
SKIP_BUILD=false
SKIP_TEST=false
TEARDOWN=false

while [[ $# -gt 0 ]]; do
    case $1 in
        --skip-setup) SKIP_SETUP=true; shift ;;
        --skip-build) SKIP_BUILD=true; shift ;;
        --skip-test)  SKIP_TEST=true; shift ;;
        --teardown)   TEARDOWN=true; shift ;;
        -h|--help)
            echo "Usage: $0 [--skip-setup] [--skip-build] [--skip-test] [--teardown]"
            echo ""
            echo "Replicates the Kind e2e GitHub Actions workflow locally."
            echo ""
            echo "Options:"
            echo "  --skip-setup   Skip cluster creation (reuse existing)"
            echo "  --skip-build   Skip build and deploy (reuse existing images/deployment)"
            echo "  --skip-test    Skip running e2e tests"
            echo "  --teardown     Tear down the cluster and exit"
            echo ""
            echo "Environment variables:"
            echo "  MEDIK8S_CLUSTER_NAME   Kind cluster name (default: medik8s-ci)"
            echo "  CONTAINER_TOOL         Container tool (default: auto-detect podman/docker)"
            echo "  OPERATOR_NAMESPACE     Operator namespace (default: sbr-operator-system)"
            echo "  TOOLS_DIR              Path to medik8s/tools checkout (default: ../tools)"
            exit 0
            ;;
        *) echo "Unknown option: $1"; exit 1 ;;
    esac
done

echo "=== Local repositories ==="
echo "  SBR:   ${SBR_DIR}"
echo "         branch: $(cd "${SBR_DIR}" && git branch --show-current)"
echo "         commit: $(cd "${SBR_DIR}" && git log --oneline -1)"
if [ -n "${TOOLS_DIR:-}" ] && [ -d "${TOOLS_DIR}" ]; then
    echo "  Tools: ${TOOLS_DIR}"
    echo "         branch: $(cd "${TOOLS_DIR}" && git branch --show-current)"
    echo "         commit: $(cd "${TOOLS_DIR}" && git log --oneline -1)"
else
    echo "  Tools: (will be auto-cloned into .tools by make)"
fi
echo ""

step() {
    echo ""
    echo "========================================"
    echo "  $1"
    echo "========================================"
}

REBOOT_WATCHER_PID=""

cleanup() {
    if [ -n "${REBOOT_WATCHER_PID}" ]; then
        kill "${REBOOT_WATCHER_PID}" 2>/dev/null || true
    fi
}
trap cleanup EXIT

# --- Teardown ---
if [ "${TEARDOWN}" = true ]; then
    step "Tearing down cluster"
    cd "${SBR_DIR}"
    make dev-teardown 2>/dev/null || true
    exit 0
fi

# --- Setup ---
if [ "${SKIP_SETUP}" = false ]; then
    step "Installing operator-sdk"
    cd "${SBR_DIR}"
    make operator-sdk
    export PATH="${SBR_DIR}/bin:${PATH}"

    step "Loading nfsd kernel module"
    # nfs-server-alpine is a kernel NFS server; nfsd must be loaded on the host kernel.
    # On macOS with podman machine, kind nodes run inside the VM so modprobe must run there.
    if [[ "$(uname)" == "Darwin" && "${CONTAINER_TOOL}" == "podman" ]]; then
        PODMAN_MACHINE="${PODMAN_MACHINE:-$(podman machine list --format '{{.Name}}' --noheading 2>/dev/null | head -1)}"
        if [ -n "${PODMAN_MACHINE}" ]; then
            echo "  macOS + podman machine '${PODMAN_MACHINE}': loading nfsd inside VM"
            podman machine ssh "${PODMAN_MACHINE}" -- sudo modprobe nfsd nfs || {
                echo "Warning: could not load nfsd/nfs in podman machine. NFS storage may not work."
            }
        else
            echo "Warning: macOS detected but no podman machine found. NFS storage may not work."
        fi
    elif ! sudo modprobe nfsd nfs 2>/dev/null; then
        echo "Warning: could not load nfsd/nfs modules. NFS storage may not work."
    fi

    step "Creating Kind cluster with registry, OLM, cert-manager, NFS storage, and null watchdog"
    cd "${SBR_DIR}"
    SETUP_NFS_RWX=true SETUP_NULL_DEVICE_WATCHDOG=true make dev-setup

    step "Cluster info"
    cd "${SBR_DIR}"
    make dev-cluster-info
else
    echo "Skipping setup (--skip-setup)"
    export PATH="${SBR_DIR}/bin:${PATH}"
fi

# --- Prepare namespace ---
step "Preparing operator namespace"
kubectl create ns "${OPERATOR_NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
kubectl label --overwrite ns "${OPERATOR_NAMESPACE}" \
    pod-security.kubernetes.io/enforce=privileged \
    pod-security.kubernetes.io/audit=privileged \
    pod-security.kubernetes.io/warn=privileged

# --- Build and deploy ---
if [ "${SKIP_BUILD}" = false ]; then
    step "Building and pushing operator + agent images"
    cd "${SBR_DIR}"
    make build-images push-images

    step "Building and pushing OLM bundle"
    cd "${SBR_DIR}"
    make bundle bundle-build bundle-push

    step "Deploying SBR via OLM bundle"
    SBR_TAG=$(make -s print-image-tag)
    SBR_BUNDLE="${IMAGE_REGISTRY}/storage-based-remediation-operator-bundle:${SBR_TAG}"
    operator-sdk cleanup medik8s-storage-based-remediation -n "${OPERATOR_NAMESPACE}" --timeout 2m 2>/dev/null || true
    operator-sdk run bundle -n "${OPERATOR_NAMESPACE}" --use-http \
        --timeout 5m \
        "${SBR_BUNDLE}"
else
    echo "Skipping build (--skip-build)"
fi

# --- Wait for operator ---
step "Waiting for operator to be ready"
cd "${SBR_DIR}"
make dev-wait

# --- Start reboot watcher ---
if [ "${SKIP_TEST}" = false ]; then
step "Starting kind reboot watcher"
MEDIK8S_CLUSTER_NAME="${MEDIK8S_CLUSTER_NAME}" \
CONTAINER_TOOL="${CONTAINER_TOOL}" \
"${TOOLS_DIR_RESOLVED}/dev/kind-reboot-watcher.sh" --mode sbr > /tmp/sbr-reboot-watcher.log 2>&1 &
REBOOT_WATCHER_PID=$!
echo "Reboot watcher started (pid ${REBOOT_WATCHER_PID}), log: /tmp/sbr-reboot-watcher.log"
fi

# --- Run tests ---
if [ "${SKIP_TEST}" = true ]; then
    echo "Skipping tests (--skip-test)"
else
step "Running e2e tests (filesystem subset)"
cd "${SBR_DIR}"
CERT_MANAGER_INSTALL_SKIP=true \
E2E_KIND=true \
OPERATOR_NS="${OPERATOR_NAMESPACE}" \
LABEL_FILTER="fs && !block" \
make test-e2e || {
    step "Debug (test failed)"
    echo "=== SBR configs ==="
    kubectl get storagebasedremediationconfig -A -o yaml 2>/dev/null || true
    echo ""
    echo "=== SBR remediations ==="
    kubectl get storagebasedremediation -A -o yaml 2>/dev/null || true
    echo ""
    echo "=== PVCs ==="
    kubectl get pvc -A 2>/dev/null || true
    echo ""
    echo "=== Agent pods ==="
    kubectl get pods -A -l app.kubernetes.io/component=agent -o wide 2>/dev/null || true
    echo ""
    echo "=== Operator logs ==="
    kubectl logs -n "${OPERATOR_NAMESPACE}" -l control-plane=controller-manager --tail=200 2>/dev/null || true
    echo ""
    echo "=== Reboot watcher log ==="
    cat /tmp/sbr-reboot-watcher.log 2>/dev/null || true
    echo ""
    make dev-ci-debug 2>/dev/null || true
    exit 1
}

echo ""
echo "========================================"
echo "  All tests passed!"
echo "========================================"
fi
