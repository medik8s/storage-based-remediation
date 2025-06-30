#!/bin/bash

# ARM64 vs AMD64 Watchdog Comparison Script
# This script deploys and tests watchdog behavior on both architectures

set -euo pipefail

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
DEBUG_TOOL_SOURCE="${PROJECT_ROOT}/debug-watchdog-arm64.go"
AWS_REGION="${AWS_REGION:-us-west-2}"
KEY_NAME="${AWS_KEY_NAME:-sbd-debug-key}"
SECURITY_GROUP_NAME="sbd-watchdog-debug-sg"
INSTANCE_TAG_NAME="sbd-watchdog-debug"

# Instance configurations - using current region AMIs
declare -A INSTANCE_CONFIGS=(
    ["amd64"]="ami-0c02fb55956c7d316 t3.micro x86_64"  # Amazon Linux 2 AMD64
    ["arm64"]="ami-0f69dd1d0d03ad669 t4g.micro arm64"  # Amazon Linux 2 ARM64
)

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log() {
    echo -e "${BLUE}[$(date +'%Y-%m-%d %H:%M:%S')]${NC} $*"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $*"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $*"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $*"
}

usage() {
    cat << 'USAGE'
Usage: ./debug-watchdog-comparison.sh [OPTIONS]

Compare watchdog behavior between ARM64 and AMD64 instances on AWS.

OPTIONS:
    -r, --region REGION     AWS region (default: us-west-2)
    -k, --key-name NAME     EC2 key pair name (default: sbd-debug-key)
    -c, --cleanup-only      Only cleanup existing resources
    -t, --test-only         Only run tests on existing instances
    -h, --help              Show this help message

ENVIRONMENT VARIABLES:
    AWS_REGION              AWS region to use
    AWS_KEY_NAME            EC2 key pair name
    AWS_PAGER               Set to "" to disable pager

EXAMPLES:
    ./debug-watchdog-comparison.sh                Run full comparison test
    ./debug-watchdog-comparison.sh -r us-east-1   Run test in us-east-1
    ./debug-watchdog-comparison.sh --cleanup-only Clean up resources only
    ./debug-watchdog-comparison.sh --test-only    Test existing instances only

USAGE
}

cleanup_resources() {
    log "Cleaning up AWS resources..."
    
    # Terminate instances
    for arch in "${!INSTANCE_CONFIGS[@]}"; do
        local instance_id
        instance_id=$(aws ec2 describe-instances \
            --region "${AWS_REGION}" \
            --filters "Name=tag:Name,Values=${INSTANCE_TAG_NAME}-${arch}" \
                     "Name=instance-state-name,Values=running,pending,stopping,stopped" \
            --query 'Reservations[0].Instances[0].InstanceId' \
            --output text 2>/dev/null | grep -v "None" || echo "")
        
        if [[ -n "${instance_id}" && "${instance_id}" != "None" ]]; then
            log "Terminating ${arch} instance: ${instance_id}"
            aws ec2 terminate-instances --region "${AWS_REGION}" --instance-ids "${instance_id}" > /dev/null || true
        fi
    done
    
    # Delete security group (wait a bit for instances to terminate)
    sleep 10
    local sg_id
    sg_id=$(aws ec2 describe-security-groups \
        --region "${AWS_REGION}" \
        --group-names "${SECURITY_GROUP_NAME}" \
        --query 'SecurityGroups[0].GroupId' \
        --output text 2>/dev/null | grep -v "None" || echo "")
    
    if [[ -n "${sg_id}" && "${sg_id}" != "None" ]]; then
        log "Deleting security group: ${sg_id}"
        aws ec2 delete-security-group --region "${AWS_REGION}" --group-id "${sg_id}" || true
    fi
    
    log_success "Cleanup completed"
}

setup_security_group() {
    log "Setting up security group..."
    
    # Get default VPC
    local vpc_id
    vpc_id=$(aws ec2 describe-vpcs \
        --region "${AWS_REGION}" \
        --filters "Name=is-default,Values=true" \
        --query 'Vpcs[0].VpcId' \
        --output text)
    
    if [[ "${vpc_id}" == "None" ]]; then
        log_error "No default VPC found in region ${AWS_REGION}"
        exit 1
    fi
    
    # Create security group
    local sg_id
    sg_id=$(aws ec2 create-security-group \
        --region "${AWS_REGION}" \
        --group-name "${SECURITY_GROUP_NAME}" \
        --description "Security group for SBD watchdog debugging" \
        --vpc-id "${vpc_id}" \
        --query 'GroupId' \
        --output text)
    
    # Add SSH access
    aws ec2 authorize-security-group-ingress \
        --region "${AWS_REGION}" \
        --group-id "${sg_id}" \
        --protocol tcp \
        --port 22 \
        --cidr "0.0.0.0/0" > /dev/null
    
    log_success "Security group created: ${sg_id}"
    echo "${sg_id}"
}

create_instance() {
    local arch="$1"
    local config="${INSTANCE_CONFIGS[$arch]}"
    local ami_id instance_type cpu_arch
    read -r ami_id instance_type cpu_arch <<< "${config}"
    
    log "Creating ${arch} instance (${instance_type}, ${ami_id})..."
    
    local sg_id="$2"
    local user_data
    user_data=$(cat << 'USERDATA'
#!/bin/bash
yum update -y
yum install -y golang git gcc kernel-devel

# Enable watchdog-related kernel modules
modprobe softdog soft_margin=60 || true

# Create debug directory
mkdir -p /opt/sbd-debug
chmod 755 /opt/sbd-debug

# Make sure watchdog devices have proper permissions
chmod 666 /dev/watchdog* 2>/dev/null || true

echo "Instance setup completed at $(date)" > /opt/sbd-debug/setup.log
USERDATA
    )
    
    local instance_id
    instance_id=$(aws ec2 run-instances \
        --region "${AWS_REGION}" \
        --image-id "${ami_id}" \
        --instance-type "${instance_type}" \
        --key-name "${KEY_NAME}" \
        --security-group-ids "${sg_id}" \
        --user-data "${user_data}" \
        --tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=${INSTANCE_TAG_NAME}-${arch}},{Key=Architecture,Value=${arch}},{Key=Purpose,Value=watchdog-debug}]" \
        --query 'Instances[0].InstanceId' \
        --output text)
    
    log "Waiting for ${arch} instance to be running: ${instance_id}"
    aws ec2 wait instance-running --region "${AWS_REGION}" --instance-ids "${instance_id}"
    
    log_success "${arch} instance created: ${instance_id}"
    echo "${instance_id}"
}

get_instance_ip() {
    local instance_id="$1"
    aws ec2 describe-instances \
        --region "${AWS_REGION}" \
        --instance-ids "${instance_id}" \
        --query 'Reservations[0].Instances[0].PublicIpAddress' \
        --output text
}

wait_for_ssh() {
    local ip="$1"
    local arch="$2"
    local max_attempts=30
    local attempt=1
    
    log "Waiting for SSH access to ${arch} instance (${ip})..."
    
    while [[ ${attempt} -le ${max_attempts} ]]; do
        if ssh -i ~/.ssh/"${KEY_NAME}".pem -o ConnectTimeout=5 -o StrictHostKeyChecking=no ec2-user@"${ip}" echo "SSH ready" &>/dev/null; then
            log_success "SSH access ready for ${arch} instance"
            return 0
        fi
        log "SSH attempt ${attempt}/${max_attempts} failed, retrying..."
        sleep 10
        ((attempt++))
    done
    
    log_error "SSH access failed for ${arch} instance after ${max_attempts} attempts"
    return 1
}

deploy_debug_tool() {
    local ip="$1"
    local arch="$2"
    
    log "Deploying debug tool to ${arch} instance (${ip})..."
    
    # Copy the debug tool source
    scp -i ~/.ssh/"${KEY_NAME}".pem -o StrictHostKeyChecking=no \
        "${DEBUG_TOOL_SOURCE}" ec2-user@"${ip}":/opt/sbd-debug/debug-watchdog.go
    
    # Compile the debug tool
    ssh -i ~/.ssh/"${KEY_NAME}".pem -o StrictHostKeyChecking=no \
        ec2-user@"${ip}" "cd /opt/sbd-debug && go build -o debug-watchdog debug-watchdog.go && chmod +x debug-watchdog"
    
    # Create system info
    ssh -i ~/.ssh/"${KEY_NAME}".pem -o StrictHostKeyChecking=no \
        ec2-user@"${ip}" "cd /opt/sbd-debug && uname -a > system-info.txt && lscpu >> system-info.txt"
    
    log_success "Debug tool deployed to ${arch} instance"
}

run_debug_test() {
    local ip="$1"
    local arch="$2"
    
    log "Running watchdog debug test on ${arch} instance (${ip})..."
    
    # Run the debug tool and capture output
    local output_file="/tmp/watchdog-debug-${arch}-$(date +%s).txt"
    
    ssh -i ~/.ssh/"${KEY_NAME}".pem -o StrictHostKeyChecking=no \
        ec2-user@"${ip}" "cd /opt/sbd-debug && sudo ./debug-watchdog" > "${output_file}" 2>&1 || true
    
    # Also get system info  
    local sysinfo_file="/tmp/system-info-${arch}-$(date +%s).txt"
    ssh -i ~/.ssh/"${KEY_NAME}".pem -o StrictHostKeyChecking=no \
        ec2-user@"${ip}" "cat /opt/sbd-debug/system-info.txt" > "${sysinfo_file}" 2>&1 || true
    
    log_success "Debug test completed for ${arch} instance"
    echo "${output_file}:${sysinfo_file}"
}

analyze_results() {
    local amd64_files="$1"
    local arm64_files="$2"
    
    local amd64_output amd64_sysinfo arm64_output arm64_sysinfo
    IFS=':' read -r amd64_output amd64_sysinfo <<< "${amd64_files}"
    IFS=':' read -r arm64_output arm64_sysinfo <<< "${arm64_files}"
    
    log "Analyzing results..."
    
    echo ""
    echo "========================================="
    echo "         COMPARISON RESULTS"
    echo "========================================="
    
    # System information comparison
    echo ""
    echo "--- SYSTEM INFORMATION COMPARISON ---"
    echo ""
    echo "AMD64 System:"
    head -5 "${amd64_sysinfo}" 2>/dev/null || echo "No system info available"
    echo ""
    echo "ARM64 System:"
    head -5 "${arm64_sysinfo}" 2>/dev/null || echo "No system info available"
    
    # WDIOC_KEEPALIVE comparison
    echo ""
    echo "--- WDIOC_KEEPALIVE BEHAVIOR COMPARISON ---"
    echo ""
    
    local amd64_keepalive arm64_keepalive
    amd64_keepalive=$(grep -A 5 "Testing WDIOC_KEEPALIVE" "${amd64_output}" 2>/dev/null || echo "Not found")
    arm64_keepalive=$(grep -A 5 "Testing WDIOC_KEEPALIVE" "${arm64_output}" 2>/dev/null || echo "Not found")
    
    echo "AMD64 WDIOC_KEEPALIVE Result:"
    echo "${amd64_keepalive}"
    echo ""
    echo "ARM64 WDIOC_KEEPALIVE Result:"
    echo "${arm64_keepalive}"
    
    # Error analysis
    if grep -q "ENOTTY" "${arm64_output}" 2>/dev/null && ! grep -q "ENOTTY" "${amd64_output}" 2>/dev/null; then
        echo ""
        log_error "CONFIRMED: ARM64-specific ENOTTY error detected for WDIOC_KEEPALIVE"
        echo "This confirms the ARM64 compatibility issue with softdog WDIOC_KEEPALIVE ioctl"
    elif grep -q "ENOTTY" "${amd64_output}" 2>/dev/null && grep -q "ENOTTY" "${arm64_output}" 2>/dev/null; then
        echo ""
        log_warning "Both architectures show ENOTTY error - may be a broader kernel issue"
    elif ! grep -q "❌.*WDIOC_KEEPALIVE" "${amd64_output}" 2>/dev/null && ! grep -q "❌.*WDIOC_KEEPALIVE" "${arm64_output}" 2>/dev/null; then
        echo ""
        log_success "Both architectures support WDIOC_KEEPALIVE - issue may be environment-specific"
    fi
    
    # Save full results
    local results_dir="/tmp/watchdog-comparison-$(date +%Y%m%d-%H%M%S)"
    mkdir -p "${results_dir}"
    
    cp "${amd64_output}" "${results_dir}/amd64-debug-output.txt" 2>/dev/null || true
    cp "${amd64_sysinfo}" "${results_dir}/amd64-system-info.txt" 2>/dev/null || true
    cp "${arm64_output}" "${results_dir}/arm64-debug-output.txt" 2>/dev/null || true
    cp "${arm64_sysinfo}" "${results_dir}/arm64-system-info.txt" 2>/dev/null || true
    
    echo ""
    echo "========================================="
    log_success "Full results saved to: ${results_dir}"
    echo "========================================="
}

main() {
    local cleanup_only=false
    local test_only=false
    
    # Parse arguments
    while [[ $# -gt 0 ]]; do
        case $1 in
            -r|--region)
                AWS_REGION="$2"
                shift 2
                ;;
            -k|--key-name)
                KEY_NAME="$2"
                shift 2
                ;;
            -c|--cleanup-only)
                cleanup_only=true
                shift
                ;;
            -t|--test-only)
                test_only=true
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
    
    # Ensure AWS credentials are available
    if ! aws sts get-caller-identity &>/dev/null; then
        log_error "AWS credentials not available. Please configure AWS CLI."
        exit 1
    fi
    
    # Check if debug tool source exists
    if [[ ! -f "${DEBUG_TOOL_SOURCE}" ]]; then
        log_error "Debug tool source not found: ${DEBUG_TOOL_SOURCE}"
        log "Please ensure debug-watchdog-arm64.go exists in the project root"
        exit 1
    fi
    
    # Check if SSH key exists
    if [[ ! -f ~/.ssh/"${KEY_NAME}".pem ]]; then
        log_error "SSH key not found: ~/.ssh/${KEY_NAME}.pem"
        log "Please ensure your EC2 key pair is available"
        exit 1
    fi
    
    # Set AWS pager to empty to avoid interactive prompts
    export AWS_PAGER=""
    
    log "Starting ARM64 vs AMD64 watchdog comparison"
    log "Region: ${AWS_REGION}"
    log "Key Name: ${KEY_NAME}"
    
    # Cleanup if requested
    if [[ "${cleanup_only}" == true ]]; then
        cleanup_resources
        exit 0
    fi
    
    # Cleanup previous resources
    cleanup_resources
    
    if [[ "${test_only}" != true ]]; then
        # Setup new resources
        local sg_id
        sg_id=$(setup_security_group)
        
        # Create instances
        declare -A instance_ids
        declare -A instance_ips
        
        for arch in "${!INSTANCE_CONFIGS[@]}"; do
            instance_ids[$arch]=$(create_instance "${arch}" "${sg_id}")
            instance_ips[$arch]=$(get_instance_ip "${instance_ids[$arch]}")
            
            # Wait for SSH and deploy
            wait_for_ssh "${instance_ips[$arch]}" "${arch}"
            deploy_debug_tool "${instance_ips[$arch]}" "${arch}"
        done
    else
        # Get existing instances
        declare -A instance_ips
        for arch in "${!INSTANCE_CONFIGS[@]}"; do
            local instance_id
            instance_id=$(aws ec2 describe-instances \
                --region "${AWS_REGION}" \
                --filters "Name=tag:Name,Values=${INSTANCE_TAG_NAME}-${arch}" \
                         "Name=instance-state-name,Values=running" \
                --query 'Reservations[0].Instances[0].InstanceId' \
                --output text 2>/dev/null | grep -v "None" || echo "")
            
            if [[ -z "${instance_id}" || "${instance_id}" == "None" ]]; then
                log_error "No running ${arch} instance found with tag ${INSTANCE_TAG_NAME}-${arch}"
                exit 1
            fi
            
            instance_ips[$arch]=$(get_instance_ip "${instance_id}")
        done
    fi
    
    # Run debug tests
    declare -A test_results
    for arch in "${!INSTANCE_CONFIGS[@]}"; do
        test_results[$arch]=$(run_debug_test "${instance_ips[$arch]}" "${arch}")
    done
    
    # Analyze results
    analyze_results "${test_results[amd64]}" "${test_results[arm64]}"
    
    # Cleanup unless test-only mode
    if [[ "${test_only}" != true ]]; then
        log "Cleaning up resources..."
        cleanup_resources
    fi
    
    log_success "ARM64 vs AMD64 watchdog comparison completed"
}

# Handle script interruption
trap cleanup_resources EXIT

main "$@"
