#!/bin/bash

# OpenShift Cluster Provisioning Script for AWS
# This script provisions an OpenShift cluster on AWS using the openshift-install tool
# It automatically downloads and installs required binaries if they are missing

set -euo pipefail

# Script configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
CLUSTER_DIR="${PROJECT_ROOT}/cluster"
INSTALL_CONFIG_TEMPLATE="${SCRIPT_DIR}/install-config.yaml.template"
TOOLS_DIR="${PROJECT_ROOT}/.tools"

# Default values
DEFAULT_CLUSTER_NAME="sbd-operator-test"
DEFAULT_REGION="us-east-1"
DEFAULT_WORKER_COUNT=3
DEFAULT_INSTANCE_TYPE="m5.large"
DEFAULT_OCP_VERSION="4.18"

# Tool versions
OPENSHIFT_INSTALL_VERSION="4.18.0"
OC_CLI_VERSION="4.18.0"

# Color codes for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Logging functions
log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

# Platform detection
detect_platform() {
    local os arch
    os=$(uname -s | tr '[:upper:]' '[:lower:]')
    arch=$(uname -m)
    
    case "$os" in
        linux)
            PLATFORM_OS="linux"
            ;;
        darwin)
            PLATFORM_OS="mac"
            ;;
        *)
            log_error "Unsupported operating system: $os"
            exit 1
            ;;
    esac
    
    case "$arch" in
        x86_64|amd64)
            PLATFORM_ARCH="amd64"
            ;;
        arm64|aarch64)
            PLATFORM_ARCH="arm64"
            ;;
        *)
            log_error "Unsupported architecture: $arch"
            exit 1
            ;;
    esac
    
    log_info "Detected platform: ${PLATFORM_OS}-${PLATFORM_ARCH}"
}

# Download and install AWS CLI
install_aws_cli() {
    log_info "Installing AWS CLI..."
    
    local install_dir="${TOOLS_DIR}/aws"
    mkdir -p "${install_dir}" || return 1
    mkdir -p "${TOOLS_DIR}/bin" || return 1
    
    case "${PLATFORM_OS}" in
        linux)
            if [[ "${PLATFORM_ARCH}" == "amd64" ]]; then
                curl -sL "https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" -o "${install_dir}/awscliv2.zip" || return 1
            else
                curl -sL "https://awscli.amazonaws.com/awscli-exe-linux-aarch64.zip" -o "${install_dir}/awscliv2.zip" || return 1
            fi
            cd "${install_dir}" || return 1
            unzip -q awscliv2.zip || return 1
            ./aws/install --install-dir "${install_dir}/aws-cli" --bin-dir "${TOOLS_DIR}/bin" --update || return 1
            ;;
        mac)
            # For macOS, use the zip version to avoid sudo requirements
            if [[ "${PLATFORM_ARCH}" == "arm64" ]]; then
                log_info "Downloading AWS CLI for macOS ARM64..."
                curl -sL "https://awscli.amazonaws.com/awscli-exe-macos-arm64.zip" -o "${install_dir}/awscliv2.zip" || return 1
            else
                log_info "Downloading AWS CLI for macOS x86_64..."
                curl -sL "https://awscli.amazonaws.com/awscli-exe-macos-x86_64.zip" -o "${install_dir}/awscliv2.zip" || return 1
            fi
            cd "${install_dir}" || return 1
            unzip -q awscliv2.zip || return 1
            ./aws/install --install-dir "${install_dir}/aws-cli" --bin-dir "${TOOLS_DIR}/bin" --update || return 1
            ;;
    esac
    
    # Verify installation
    if ! "${TOOLS_DIR}/bin/aws" --version &> /dev/null; then
        log_error "AWS CLI installation verification failed"
        return 1
    fi
    
    log_success "AWS CLI installed successfully"
    return 0
}

# Download and install OpenShift Install
install_openshift_install() {
    log_info "Installing openshift-install..."
    
    local install_dir="${TOOLS_DIR}/openshift-install"
    mkdir -p "${install_dir}" || return 1
    mkdir -p "${TOOLS_DIR}/bin" || return 1
    
    local download_url
    case "${PLATFORM_OS}" in
        linux)
            download_url="https://mirror.openshift.com/pub/openshift-v4/clients/ocp/${OPENSHIFT_INSTALL_VERSION}/openshift-install-linux.tar.gz"
            ;;
        mac)
            download_url="https://mirror.openshift.com/pub/openshift-v4/clients/ocp/${OPENSHIFT_INSTALL_VERSION}/openshift-install-mac.tar.gz"
            ;;
        *)
            log_error "Unsupported platform for openshift-install: ${PLATFORM_OS}"
            return 1
            ;;
    esac
    
    if ! curl -sL "${download_url}" | tar -xzC "${install_dir}"; then
        log_error "Failed to download or extract openshift-install"
        return 1
    fi
    
    if [[ ! -f "${install_dir}/openshift-install" ]]; then
        log_error "openshift-install binary not found after extraction"
        return 1
    fi
    
    cp "${install_dir}/openshift-install" "${TOOLS_DIR}/bin/" || return 1
    chmod +x "${TOOLS_DIR}/bin/openshift-install" || return 1
    
    # Verify installation
    if ! "${TOOLS_DIR}/bin/openshift-install" version &> /dev/null; then
        log_error "openshift-install installation verification failed"
        return 1
    fi
    
    log_success "openshift-install installed successfully"
    return 0
}

# Download and install oc CLI
install_oc_cli() {
    log_info "Installing oc CLI..."
    
    local install_dir="${TOOLS_DIR}/oc"
    mkdir -p "${install_dir}" || return 1
    mkdir -p "${TOOLS_DIR}/bin" || return 1
    
    local download_url
    case "${PLATFORM_OS}" in
        linux)
            download_url="https://mirror.openshift.com/pub/openshift-v4/clients/ocp/${OC_CLI_VERSION}/openshift-client-linux.tar.gz"
            ;;
        mac)
            download_url="https://mirror.openshift.com/pub/openshift-v4/clients/ocp/${OC_CLI_VERSION}/openshift-client-mac.tar.gz"
            ;;
        *)
            log_error "Unsupported platform for oc CLI: ${PLATFORM_OS}"
            return 1
            ;;
    esac
    
    if ! curl -sL "${download_url}" | tar -xzC "${install_dir}"; then
        log_error "Failed to download or extract oc CLI"
        return 1
    fi
    
    if [[ ! -f "${install_dir}/oc" ]]; then
        log_error "oc binary not found after extraction"
        return 1
    fi
    
    cp "${install_dir}/oc" "${TOOLS_DIR}/bin/" || return 1
    cp "${install_dir}/kubectl" "${TOOLS_DIR}/bin/" 2>/dev/null || true
    chmod +x "${TOOLS_DIR}/bin/oc" || return 1
    [[ -f "${TOOLS_DIR}/bin/kubectl" ]] && chmod +x "${TOOLS_DIR}/bin/kubectl"
    
    # Verify installation
    if ! "${TOOLS_DIR}/bin/oc" version --client &> /dev/null; then
        log_error "oc CLI installation verification failed"
        return 1
    fi
    
    log_success "oc CLI installed successfully"
    return 0
}

# Install jq
install_jq() {
    log_info "Installing jq..."
    
    mkdir -p "${TOOLS_DIR}/bin" || return 1
    
    local download_url
    case "${PLATFORM_OS}-${PLATFORM_ARCH}" in
        linux-amd64)
            download_url="https://github.com/jqlang/jq/releases/latest/download/jq-linux-amd64"
            ;;
        linux-arm64)
            download_url="https://github.com/jqlang/jq/releases/latest/download/jq-linux-arm64"
            ;;
        mac-amd64)
            download_url="https://github.com/jqlang/jq/releases/latest/download/jq-macos-amd64"
            ;;
        mac-arm64)
            download_url="https://github.com/jqlang/jq/releases/latest/download/jq-macos-arm64"
            ;;
        *)
            log_error "Unsupported platform for jq: ${PLATFORM_OS}-${PLATFORM_ARCH}"
            return 1
            ;;
    esac
    
    if ! curl -sL "${download_url}" -o "${TOOLS_DIR}/bin/jq"; then
        log_error "Failed to download jq"
        return 1
    fi
    
    chmod +x "${TOOLS_DIR}/bin/jq" || return 1
    
    # Verify installation
    if ! "${TOOLS_DIR}/bin/jq" --version &> /dev/null; then
        log_error "jq installation verification failed"
        return 1
    fi
    
    log_success "jq installed successfully"
    return 0
}

# Update PATH to include tools directory
update_path() {
    if [[ -d "${TOOLS_DIR}/bin" ]]; then
        export PATH="${TOOLS_DIR}/bin:${PATH}"
        log_info "Added ${TOOLS_DIR}/bin to PATH"
    fi
}

# Usage function
usage() {
    cat << EOF
Usage: $0 [OPTIONS]

Provision an OpenShift cluster on AWS for SBD operator testing.
This script automatically downloads and installs required tools if missing.

OPTIONS:
    -n, --cluster-name NAME     Cluster name (default: ${DEFAULT_CLUSTER_NAME})
    -r, --region REGION         AWS region (default: ${DEFAULT_REGION})
    -w, --workers COUNT         Number of worker nodes (minimum 3, default: ${DEFAULT_WORKER_COUNT})
    -t, --instance-type TYPE    EC2 instance type (default: ${DEFAULT_INSTANCE_TYPE})
    -v, --ocp-version VERSION   OpenShift version (default: ${DEFAULT_OCP_VERSION})
    -c, --cleanup              Clean up existing cluster directory
    --skip-tool-install        Skip automatic tool installation
    -h, --help                 Show this help message

EXAMPLES:
    # Provision cluster with defaults (auto-installs missing tools)
    $0

    # Provision cluster with 5 workers
    $0 --workers 5

    # Provision cluster in different region
    $0 --region us-west-2 --workers 4

    # Clean up and provision new cluster
    $0 --cleanup --cluster-name my-test-cluster

    # Skip tool installation (use existing tools)
    $0 --skip-tool-install

REQUIRED TOOLS (auto-installed if missing):
    - AWS CLI v2
    - openshift-install (version ${OPENSHIFT_INSTALL_VERSION})
    - oc CLI (version ${OC_CLI_VERSION})
    - jq (latest)

PREREQUISITES:
    - AWS credentials configured (run 'aws configure' after installation)
    - Sufficient AWS quota for EC2 instances and VPCs
    - Red Hat pull secret (download from https://console.redhat.com/openshift/install/pull-secret)

EOF
}

# Parse command line arguments
parse_args() {
    CLUSTER_NAME="${DEFAULT_CLUSTER_NAME}"
    REGION="${DEFAULT_REGION}"
    WORKER_COUNT="${DEFAULT_WORKER_COUNT}"
    INSTANCE_TYPE="${DEFAULT_INSTANCE_TYPE}"
    OCP_VERSION="${DEFAULT_OCP_VERSION}"
    CLEANUP=false
    SKIP_TOOL_INSTALL=false

    while [[ $# -gt 0 ]]; do
        case $1 in
            -n|--cluster-name)
                CLUSTER_NAME="$2"
                shift 2
                ;;
            -r|--region)
                REGION="$2"
                shift 2
                ;;
            -w|--workers)
                WORKER_COUNT="$2"
                shift 2
                ;;
            -t|--instance-type)
                INSTANCE_TYPE="$2"
                shift 2
                ;;
            -v|--ocp-version)
                OCP_VERSION="$2"
                # Update tool versions to match
                OPENSHIFT_INSTALL_VERSION="$2"
                OC_CLI_VERSION="$2"
                shift 2
                ;;
            -c|--cleanup)
                CLEANUP=true
                shift
                ;;
            --skip-tool-install)
                SKIP_TOOL_INSTALL=true
                shift
                ;;
            -h|--help)
                usage
                exit 0
                ;;
            *)
                log_error "Unknown option: $1"
                usage
                exit 1
                ;;
        esac
    done

    # Validate worker count
    if [[ "${WORKER_COUNT}" -lt 3 ]]; then
        log_error "Worker count must be at least 3 for proper SBD testing"
        exit 1
    fi

    # Validate cluster name
    if [[ ! "${CLUSTER_NAME}" =~ ^[a-z0-9-]+$ ]]; then
        log_error "Cluster name must contain only lowercase letters, numbers, and hyphens"
        exit 1
    fi
}

# Install missing tools
install_missing_tools() {
    if [[ "${SKIP_TOOL_INSTALL}" == "true" ]]; then
        log_info "Skipping automatic tool installation"
        return
    fi
    
    log_info "Checking and installing missing tools..."
    
    # Detect platform first
    detect_platform
    
    # Update PATH to include our tools directory
    update_path
    
    # Check and install AWS CLI
    if ! command -v aws &> /dev/null; then
        log_info "AWS CLI not found, installing..."
        if ! install_aws_cli; then
            log_error "Failed to install AWS CLI"
            log_info "You can install it manually:"
            log_info "  macOS: brew install awscli"
            log_info "  Linux: https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html"
            exit 1
        fi
        update_path  # Update PATH again after installation
    else
        log_info "AWS CLI already installed: $(aws --version)"
    fi
    
    # Check and install openshift-install
    if ! command -v openshift-install &> /dev/null; then
        log_info "openshift-install not found, installing..."
        if ! install_openshift_install; then
            log_error "Failed to install openshift-install"
            log_info "You can download it manually from: https://console.redhat.com/openshift/install"
            exit 1
        fi
    else
        log_info "openshift-install already installed: $(openshift-install version | head -n1)"
    fi
    
    # Check and install oc CLI
    if ! command -v oc &> /dev/null; then
        log_info "oc CLI not found, installing..."
        if ! install_oc_cli; then
            log_error "Failed to install oc CLI"
            log_info "You can download it manually from: https://console.redhat.com/openshift/install"
            exit 1
        fi
    else
        log_info "oc CLI already installed: $(oc version --client --short 2>/dev/null || echo 'oc version unknown')"
    fi
    
    # Check and install jq
    if ! command -v jq &> /dev/null; then
        log_info "jq not found, installing..."
        if ! install_jq; then
            log_error "Failed to install jq"
            log_info "You can install it manually:"
            log_info "  macOS: brew install jq"
            log_info "  Linux: apt-get install jq (Ubuntu/Debian) or yum install jq (RHEL/CentOS)"
            exit 1
        fi
    else
        log_info "jq already installed: $(jq --version)"
    fi
    
    log_success "All required tools are now available"
}

# Check prerequisites
check_prerequisites() {
    log_info "Checking prerequisites..."

    # Check AWS CLI
    if ! command -v aws &> /dev/null; then
        log_error "AWS CLI is not installed or not in PATH"
        exit 1
    fi

    # Check AWS credentials
    if ! aws sts get-caller-identity &> /dev/null; then
        log_error "AWS credentials not configured or invalid"
        log_info "Run 'aws configure' to set up your AWS credentials"
        exit 1
    fi

    # Check openshift-install
    if ! command -v openshift-install &> /dev/null; then
        log_error "openshift-install is not installed or not in PATH"
        exit 1
    fi

    # Check oc CLI
    if ! command -v oc &> /dev/null; then
        log_error "oc CLI is not installed or not in PATH"
        exit 1
    fi

    # Check jq
    if ! command -v jq &> /dev/null; then
        log_error "jq is not installed or not in PATH"
        exit 1
    fi

    # Check Red Hat pull secret
    if [[ ! -f "${HOME}/.docker/config.json" ]] && [[ ! -f "${HOME}/.config/containers/auth.json" ]]; then
        log_warn "Red Hat pull secret not found in standard locations"
        log_info "Download your pull secret from https://console.redhat.com/openshift/install/pull-secret"
        log_info "Save it as ${HOME}/.docker/config.json or ${HOME}/.config/containers/auth.json"
    fi

    log_success "Prerequisites check passed"
}

# Get AWS account information
get_aws_info() {
    log_info "Getting AWS account information..."
    
    AWS_ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)
    AWS_USER_ARN=$(aws sts get-caller-identity --query Arn --output text)
    
    log_info "AWS Account ID: ${AWS_ACCOUNT_ID}"
    log_info "AWS User/Role: ${AWS_USER_ARN}"
    log_info "Target Region: ${REGION}"
}

# Ensure install config template exists
ensure_install_config_template() {
    if [[ ! -f "${INSTALL_CONFIG_TEMPLATE}" ]]; then
        log_error "Install config template not found: ${INSTALL_CONFIG_TEMPLATE}"
        log_info "Please ensure the template file exists in the scripts directory"
        exit 1
    fi
}

# Get or create SSH key
setup_ssh_key() {
    log_info "Setting up SSH key..."
    
    SSH_KEY_PATH="${HOME}/.ssh/id_rsa"
    if [[ ! -f "${SSH_KEY_PATH}" ]]; then
        log_info "Creating new SSH key..."
        ssh-keygen -t rsa -b 4096 -f "${SSH_KEY_PATH}" -N "" -C "sbd-operator-test"
    fi
    
    SSH_PUBLIC_KEY=$(cat "${SSH_KEY_PATH}.pub")
    log_info "SSH public key: ${SSH_PUBLIC_KEY}"
}

# Get pull secret
get_pull_secret() {
    log_info "Getting Red Hat pull secret..."
    
    PULL_SECRET_FILE=""
    if [[ -f "${HOME}/.docker/config.json" ]]; then
        PULL_SECRET_FILE="${HOME}/.docker/config.json"
    elif [[ -f "${HOME}/.config/containers/auth.json" ]]; then
        PULL_SECRET_FILE="${HOME}/.config/containers/auth.json"
    else
        log_error "Pull secret not found. Please download from https://console.redhat.com/openshift/install/pull-secret"
        exit 1
    fi
    
    PULL_SECRET=$(cat "${PULL_SECRET_FILE}" | jq -c .)
}

# Determine base domain
get_base_domain() {
    log_info "Determining base domain..."
    
    # Try to get a Route53 hosted zone
    HOSTED_ZONES=$(aws route53 list-hosted-zones --query 'HostedZones[?Config.PrivateZone==`false`].Name' --output text)
    
    if [[ -n "${HOSTED_ZONES}" ]]; then
        BASE_DOMAIN=$(echo "${HOSTED_ZONES}" | head -n1 | sed 's/\.$//')
        log_info "Using existing Route53 domain: ${BASE_DOMAIN}"
    else
        # Use a default domain that OpenShift can create
        BASE_DOMAIN="aws.example.com"
        log_warn "No Route53 hosted zone found, using default: ${BASE_DOMAIN}"
        log_info "OpenShift will create necessary DNS records"
    fi
}

# Create install config
create_install_config() {
    log_info "Creating install-config.yaml..."
    
    CLUSTER_FULL_DIR="${CLUSTER_DIR}/${CLUSTER_NAME}"
    mkdir -p "${CLUSTER_FULL_DIR}"
    
    # Get AWS user name for tagging
    AWS_USER=$(echo "${AWS_USER_ARN}" | cut -d'/' -f2)
    
    # Create install config from template
    sed -e "s/__CLUSTER_NAME__/${CLUSTER_NAME}/g" \
        -e "s/__BASE_DOMAIN__/${BASE_DOMAIN}/g" \
        -e "s/__REGION__/${REGION}/g" \
        -e "s/__WORKER_COUNT__/${WORKER_COUNT}/g" \
        -e "s/__INSTANCE_TYPE__/${INSTANCE_TYPE}/g" \
        -e "s/__AWS_USER__/${AWS_USER}/g" \
        -e "s|__PULL_SECRET__|${PULL_SECRET}|g" \
        -e "s|__SSH_KEY__|${SSH_PUBLIC_KEY}|g" \
        "${INSTALL_CONFIG_TEMPLATE}" > "${CLUSTER_FULL_DIR}/install-config.yaml"
    
    log_success "Install config created at ${CLUSTER_FULL_DIR}/install-config.yaml"
    
    # Backup the install config
    cp "${CLUSTER_FULL_DIR}/install-config.yaml" "${CLUSTER_FULL_DIR}/install-config.yaml.backup"
}

# Clean up existing cluster
cleanup_cluster() {
    if [[ "${CLEANUP}" == "true" ]]; then
        log_info "Cleaning up existing cluster directory..."
        CLUSTER_FULL_DIR="${CLUSTER_DIR}/${CLUSTER_NAME}"
        if [[ -d "${CLUSTER_FULL_DIR}" ]]; then
            rm -rf "${CLUSTER_FULL_DIR}"
            log_success "Cleaned up ${CLUSTER_FULL_DIR}"
        fi
    fi
}

# Provision cluster
provision_cluster() {
    log_info "Starting OpenShift cluster provisioning..."
    log_info "Cluster: ${CLUSTER_NAME}"
    log_info "Region: ${REGION}"
    log_info "Workers: ${WORKER_COUNT}"
    log_info "Instance Type: ${INSTANCE_TYPE}"
    log_info "OCP Version: ${OCP_VERSION}"
    
    CLUSTER_FULL_DIR="${CLUSTER_DIR}/${CLUSTER_NAME}"
    
    # Check if cluster already exists
    if [[ -f "${CLUSTER_FULL_DIR}/metadata.json" ]]; then
        log_warn "Cluster appears to already exist"
        log_info "Use --cleanup to remove existing cluster first"
        read -p "Continue anyway? (y/N): " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            exit 1
        fi
    fi
    
    # Start installation
    log_info "Running openshift-install create cluster..."
    log_info "This will take approximately 30-45 minutes..."
    
    cd "${CLUSTER_FULL_DIR}"
    
    # Run the installation with timeout
    timeout 3600 openshift-install create cluster --dir "${CLUSTER_FULL_DIR}" --log-level=info
    
    if [[ $? -eq 0 ]]; then
        log_success "Cluster provisioning completed successfully!"
    else
        log_error "Cluster provisioning failed or timed out"
        exit 1
    fi
}

# Post-installation setup
post_install_setup() {
    log_info "Performing post-installation setup..."
    
    CLUSTER_FULL_DIR="${CLUSTER_DIR}/${CLUSTER_NAME}"
    
    # Export kubeconfig
    export KUBECONFIG="${CLUSTER_FULL_DIR}/auth/kubeconfig"
    
    # Wait for cluster to be ready
    log_info "Waiting for cluster to be ready..."
    oc wait --for=condition=Available --timeout=300s clusteroperator/openshift-apiserver
    
    # Get cluster info
    CLUSTER_VERSION=$(oc get clusterversion version -o jsonpath='{.status.desired.version}')
    CONSOLE_URL=$(oc get route console -n openshift-console -o jsonpath='{.spec.host}')
    
    log_success "Cluster is ready!"
    log_info "OpenShift Version: ${CLUSTER_VERSION}"
    log_info "Console URL: https://${CONSOLE_URL}"
    log_info "Kubeconfig: ${CLUSTER_FULL_DIR}/auth/kubeconfig"
    
    # Create cluster info file
    cat > "${CLUSTER_FULL_DIR}/cluster-info.txt" << EOF
Cluster Name: ${CLUSTER_NAME}
OpenShift Version: ${CLUSTER_VERSION}
Region: ${REGION}
Worker Nodes: ${WORKER_COUNT}
Instance Type: ${INSTANCE_TYPE}
Console URL: https://${CONSOLE_URL}
Kubeconfig: ${CLUSTER_FULL_DIR}/auth/kubeconfig

To use this cluster:
export KUBECONFIG=${CLUSTER_FULL_DIR}/auth/kubeconfig
oc get nodes

To destroy this cluster:
openshift-install destroy cluster --dir ${CLUSTER_FULL_DIR}
EOF
    
    log_info "Cluster information saved to ${CLUSTER_FULL_DIR}/cluster-info.txt"
}

# Main function
main() {
    log_info "Starting OpenShift cluster provisioning on AWS"
    
    parse_args "$@"
    install_missing_tools
    check_prerequisites
    get_aws_info
    cleanup_cluster
    ensure_install_config_template
    setup_ssh_key
    get_pull_secret
    get_base_domain
    create_install_config
    provision_cluster
    post_install_setup
    
    log_success "OpenShift cluster provisioning completed successfully!"
    log_info "Run 'export KUBECONFIG=${CLUSTER_DIR}/${CLUSTER_NAME}/auth/kubeconfig' to use the cluster"
}

# Run main function
main "$@" 