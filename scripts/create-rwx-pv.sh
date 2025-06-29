#!/bin/bash

# AWS EFS RWX Persistent Volume Creation Script for OpenShift
# This script creates an AWS EFS filesystem and corresponding Kubernetes resources
# for ReadWriteMany (RWX) storage that can be shared across multiple OCP worker nodes

set -e

# Disable AWS CLI pager to prevent hanging
export AWS_PAGER=""

# Script configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Default values
EFS_NAME="sbd-operator-shared-storage"
PV_NAME="sbd-shared-pv"
PVC_NAME="sbd-shared-pvc"
NAMESPACE="sbd-operator-system"
STORAGE_SIZE="10Gi"
AWS_REGION="${AWS_REGION:-}"
CLUSTER_NAME=""
PERFORMANCE_MODE="generalPurpose"  # generalPurpose or maxIO
THROUGHPUT_MODE="provisioned"      # provisioned or burstingThroughput
PROVISIONED_THROUGHPUT="100"       # MiB/s (only for provisioned mode)
KUBECTL="${KUBECTL:-kubectl}"
CLEANUP="false"
DRY_RUN="false"
SKIP_PERMISSION_CHECK="false"
EXISTING_SECURITY_GROUP=""
AUTO_DETECT_SECURITY_GROUP="true"

# Functions
log_info() {
    echo -e "${BLUE}[INFO]${NC} $1" >&2
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1" >&2
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1" >&2
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1" >&2
}

show_usage() {
    cat << EOF
Usage: $0 [OPTIONS]

Create AWS EFS-based ReadWriteMany (RWX) Persistent Volume for OpenShift

OPTIONS:
    -n, --name NAME           EFS filesystem name (default: sbd-operator-shared-storage)
    -p, --pv-name NAME        Persistent Volume name (default: sbd-shared-pv)
    -c, --pvc-name NAME       Persistent Volume Claim name (default: sbd-shared-pvc)
    -s, --size SIZE           Storage size (default: 10Gi)
    -r, --region REGION       AWS region (auto-detected if not provided)
    -k, --cluster-name NAME   OpenShift cluster name (auto-detected if not provided)
    -N, --namespace NS        Kubernetes namespace (default: sbd-system)
    --performance-mode MODE   EFS performance mode: generalPurpose|maxIO (default: generalPurpose)
    --throughput-mode MODE    EFS throughput mode: provisioned|burstingThroughput (default: provisioned)
    --provisioned-tp MBPS     Provisioned throughput in MiB/s (default: 100, only for provisioned mode)
    --existing-sg SG_ID       Use specific security group instead of auto-detection
    --allow-sg-creation       Allow creation of new security group if none found (requires additional permissions)
    --cleanup                 Delete existing EFS and Kubernetes resources
    --dry-run                 Show what would be created without actually creating
    --skip-permission-check   Skip AWS permission validation (use with caution)
    -h, --help                Show this help message

EXAMPLES:
    # Basic usage (auto-detect cluster, region, and security group)
    $0

    # Specify cluster and region, auto-detect security group
    $0 --cluster-name my-cluster --region us-west-2

    # Use specific security group (no auto-detection)
    $0 --existing-sg sg-1234567890abcdef0

    # Allow creation of new security group if auto-detection fails
    $0 --allow-sg-creation

    # Clean up resources
    $0 --cleanup

PREREQUISITES:
    - AWS CLI configured with appropriate credentials
    - kubectl configured for target OpenShift cluster
    - jq installed for JSON processing
    - AWS EFS CSI driver installed in the cluster
    - Cluster must have worker nodes in multiple AZs for HA

AWS PERMISSIONS REQUIRED:
    Required permissions:
    - elasticfilesystem:CreateFileSystem
    - elasticfilesystem:CreateMountTarget  
    - elasticfilesystem:DescribeFileSystems
    - elasticfilesystem:DescribeMountTargets
    - ec2:DescribeInstances
    - ec2:DescribeSubnets
    - ec2:DescribeSecurityGroups
    
    Conditionally required (only needed with --allow-sg-creation):
    - ec2:CreateSecurityGroup (only needed if --allow-sg-creation is used and no suitable SG found)
    - ec2:AuthorizeSecurityGroupIngress (only needed if --allow-sg-creation is used and no suitable SG found)
    
    Optional permissions (for tagging):
    - elasticfilesystem:CreateTags
    - ec2:CreateTags
    
    The script will automatically check these permissions before proceeding.
    Use --skip-permission-check to bypass this validation.

EOF
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -n|--name)
            EFS_NAME="$2"
            shift 2
            ;;
        -p|--pv-name)
            PV_NAME="$2"
            shift 2
            ;;
        -c|--pvc-name)
            PVC_NAME="$2"
            shift 2
            ;;
        -s|--size)
            STORAGE_SIZE="$2"
            shift 2
            ;;
        -r|--region)
            AWS_REGION="$2"
            shift 2
            ;;
        -k|--cluster-name)
            CLUSTER_NAME="$2"
            shift 2
            ;;
        -N|--namespace)
            NAMESPACE="$2"
            shift 2
            ;;
        --performance-mode)
            PERFORMANCE_MODE="$2"
            shift 2
            ;;
        --throughput-mode)
            THROUGHPUT_MODE="$2"
            shift 2
            ;;
        --provisioned-tp)
            PROVISIONED_THROUGHPUT="$2"
            shift 2
            ;;
        --existing-sg)
            EXISTING_SECURITY_GROUP="$2"
            AUTO_DETECT_SECURITY_GROUP="false"  # Disable auto-detection when specific SG provided
            shift 2
            ;;
        --cleanup)
            CLEANUP="true"
            shift
            ;;
        --dry-run)
            DRY_RUN="true"
            shift
            ;;
        --skip-permission-check)
            SKIP_PERMISSION_CHECK="true"
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

# Validate inputs
if [[ "$PERFORMANCE_MODE" != "generalPurpose" && "$PERFORMANCE_MODE" != "maxIO" ]]; then
    log_error "Invalid performance mode: $PERFORMANCE_MODE. Must be 'generalPurpose' or 'maxIO'"
    exit 1
fi

if [[ "$THROUGHPUT_MODE" != "provisioned" && "$THROUGHPUT_MODE" != "burstingThroughput" ]]; then
    log_error "Invalid throughput mode: $THROUGHPUT_MODE. Must be 'provisioned' or 'burstingThroughput'"
    exit 1
fi

# Function to check required tools
check_tools() {
    local missing_tools=()
    
    if ! command -v aws &> /dev/null; then
        missing_tools+=("aws")
    fi
    
    if ! command -v $KUBECTL &> /dev/null; then
        missing_tools+=("$KUBECTL")
    fi
    
    if ! command -v jq &> /dev/null; then
        missing_tools+=("jq")
    fi
    
    if [[ ${#missing_tools[@]} -gt 0 ]]; then
        log_error "Missing required tools: ${missing_tools[*]}"
        exit 1
    fi
}

# Function to auto-detect cluster name
detect_cluster_name() {
    if [[ -z "$CLUSTER_NAME" ]]; then
        log_info "Auto-detecting cluster name..."
        
        # Get cluster name from context
         CLUSTER_NAME=$($KUBECTL config view --minify -o jsonpath='{.clusters[0].name}' 2>/dev/null || echo "")
        
        # Fallback: try to get from cluster infrastructure
        if [[ -z "$CLUSTER_NAME" ]]; then
            CLUSTER_NAME=$($KUBECTL get infrastructure cluster -o jsonpath='{.status.infrastructureName}' 2>/dev/null || echo "")
        fi
        
        if [[ -n "$CLUSTER_NAME" ]]; then
            log_info "Detected cluster name: $CLUSTER_NAME"
        else
            log_error "Could not auto-detect cluster name. Please specify with --cluster-name"
            exit 1
        fi
    fi
}

# Function to auto-detect AWS region from cluster configuration
detect_aws_region() {
    if [[ -n "$AWS_REGION" ]]; then
        log_info "Using specified AWS region: $AWS_REGION"
        return
    fi
    
    log_info "Auto-detecting AWS region from cluster configuration..."
    
    local detected_region=""
    
    # Try to detect region from kubectl context and cluster configuration
    if command -v $KUBECTL >/dev/null 2>&1; then
        # Get current context
        local current_context
        current_context=$($KUBECTL config current-context 2>/dev/null || echo "")
        
        # Get cluster endpoint
        local cluster_endpoint
        cluster_endpoint=$($KUBECTL config view --minify -o jsonpath='{.clusters[0].cluster.server}' 2>/dev/null || echo "")
                 
         # Get node information to extract region from AWS node names
         local node_names
         node_names=$($KUBECTL get nodes -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || echo "")
         
         log_info "Cluster endpoint: $cluster_endpoint"
         log_info "Cluster context: $current_context"
         log_info "Cluster name: $CLUSTER_NAME"
         log_info "Node names: $node_names"
        
                 # Try various patterns to extract region
         
         # 1. Extract region from AWS node names (e.g., ip-10-0-1-1.us-east-1.compute.internal)
         if [[ -n "$node_names" ]]; then
             for node in $node_names; do
                 if [[ "$node" =~ \.([^.]*-(east|west|north|south|southeast|northeast|central)-[0-9]+)\.compute\.internal ]]; then
                     detected_region="${BASH_REMATCH[1]}"
                     log_info "Detected region from node name pattern: $detected_region"
                     break
                 fi
             done
         fi
         
         # OpenShift on AWS pattern: api.<cluster>.<region>.domain
         if [[ -z "$detected_region" && "$cluster_endpoint" =~ https://api\.[^.]+\.([^.]+)\..*\.io ]]; then
             local potential_region="${BASH_REMATCH[1]}"
             # Validate if it looks like an AWS region (e.g., us-east-1, eu-west-1, ap-southeast-2)
             if [[ "$potential_region" =~ ^(us|eu|ap|ca|sa|af|me)-(east|west|north|south|southeast|northeast|central)-[0-9]+$ ]]; then
                 detected_region="$potential_region"
                 log_info "Detected region from OpenShift endpoint pattern: $detected_region"
             fi
         fi
        
        # If we found a region, validate and set it
        if [[ -n "$detected_region" ]]; then
            # Quick validation by trying to list regions (if AWS CLI works)
            if aws ec2 describe-regions --region "$detected_region" --region-names "$detected_region" >/dev/null 2>&1; then
                log_success "Successfully validated region: $detected_region"
                AWS_REGION="$detected_region"
                return
            else
                log_warning "Detected region '$detected_region' but could not validate with AWS CLI"
                # Still use it as it might work
                AWS_REGION="$detected_region"
                return
            fi
        fi
    fi
    
    # Fallback: try AWS CLI default region
    detected_region=$(aws configure get region 2>/dev/null || echo "")
    if [[ -n "$detected_region" ]]; then
        log_info "Using AWS CLI default region: $detected_region"
        AWS_REGION="$detected_region"
        return
    fi
    
    # Fallback: try environment variable
    if [[ -n "$AWS_DEFAULT_REGION" ]]; then
        log_info "Using AWS_DEFAULT_REGION: $AWS_DEFAULT_REGION"
        AWS_REGION="$AWS_DEFAULT_REGION"
        return
    fi
    
    # If all fails, use default
    log_warning "Could not auto-detect AWS region from cluster configuration"
    log_warning "Using default region: us-east-1"
    log_warning "You can specify a region with --region or set AWS_REGION environment variable"
    AWS_REGION="us-east-1"
}

# Function to check AWS permissions
check_aws_permissions() {
    log_info "Checking AWS permissions..."
    
    local missing_permissions=()
    local warnings=()
    
    # Test EC2 permissions (required for VPC/subnet discovery and security groups)
    if ! aws ec2 describe-instances --region "$AWS_REGION" --max-items 1 >/dev/null 2>&1; then
        missing_permissions+=("ec2:DescribeInstances")
    fi
    
    if ! aws ec2 describe-security-groups --region "$AWS_REGION" --max-items 1 >/dev/null 2>&1; then
        missing_permissions+=("ec2:DescribeSecurityGroups")
    fi
    
    if ! aws ec2 describe-subnets --region "$AWS_REGION" --max-items 1 >/dev/null 2>&1; then
        missing_permissions+=("ec2:DescribeSubnets")
    fi
    
    # VPC validation removed - VPC ID is obtained from describe-instances, no separate validation needed
    
    # Test EFS permissions (core functionality)
    if ! aws efs describe-file-systems --region "$AWS_REGION" --max-items 1 >/dev/null 2>&1; then
        missing_permissions+=("elasticfilesystem:DescribeFileSystems")
    fi
    
    if ! aws efs describe-mount-targets --file-system-id fs-12345678 --region "$AWS_REGION" >/dev/null 2>&1; then
        # Check if it's a permission error vs file system not found error
        local mount_describe_output
        mount_describe_output=$(aws efs describe-mount-targets --file-system-id fs-12345678 --region "$AWS_REGION" 2>&1 || true)
        if [[ "$mount_describe_output" == *"AccessDenied"* ]] || [[ "$mount_describe_output" == *"not authorized"* ]]; then
            missing_permissions+=("elasticfilesystem:DescribeMountTargets")
        elif [[ "$mount_describe_output" == *"FileSystemNotFound"* ]]; then
            # This is expected - means we have permission but the file system doesn't exist
            log_info "DescribeMountTargets permission appears to be available"
        fi
    fi
    
    # Test EFS creation permissions (always check regardless of dry-run mode)
    # Note: EFS create-file-system doesn't support --dry-run, so we test by attempting to create with invalid parameters
    # This will fail with permission error if we don't have the permission, or with validation error if we do
    local create_test_output
    create_test_output=$(aws efs create-file-system --region "$AWS_REGION" --performance-mode invalidMode 2>&1 || true)
    if [[ "$create_test_output" == *"AccessDenied"* ]] || [[ "$create_test_output" == *"not authorized"* ]]; then
        missing_permissions+=("elasticfilesystem:CreateFileSystem")
    elif [[ "$create_test_output" == *"ValidationException"* ]] || [[ "$create_test_output" == *"InvalidParameterValue"* ]]; then
        # Validation error means we have permission but parameters are invalid (expected)
        log_info "EFS create permission appears to be available"
    else
        # Log unexpected response for debugging
        log_info "EFS create permission test result: $create_test_output"
    fi
    
    # Test mount target creation permission
    # Note: EFS create-mount-target doesn't support --dry-run, so we test with invalid parameters
    local mount_test_output
    mount_test_output=$(aws efs create-mount-target --region "$AWS_REGION" --file-system-id "fs-12345678" --subnet-id "subnet-invalid" 2>&1 || true)
    if [[ "$mount_test_output" == *"AccessDenied"* ]] || [[ "$mount_test_output" == *"not authorized"* ]]; then
        missing_permissions+=("elasticfilesystem:CreateMountTarget")
    elif [[ "$mount_test_output" == *"FileSystemNotFound"* ]] || [[ "$mount_test_output" == *"ValidationException"* ]] || [[ "$mount_test_output" == *"InvalidSubnet"* ]] || [[ "$mount_test_output" == *"Parameter validation failed"* ]]; then
        # Expected errors when we have permission but parameters are invalid
        log_info "EFS mount target creation permission appears to be available"
    else
        # Log unexpected response for debugging
        log_info "EFS mount target permission test result: $mount_test_output"
    fi
    
    
    # Test optional permissions (will generate warnings if missing)
    if ! aws efs create-tags --region "$AWS_REGION" --file-system-id "fs-test" --tags "Key=test,Value=test" --dry-run >/dev/null 2>&1; then
        local tag_test_output
        tag_test_output=$(aws efs create-tags --region "$AWS_REGION" --file-system-id "fs-test" --tags "Key=test,Value=test" --dry-run 2>&1 || true)
        if [[ "$tag_test_output" == *"DryRunOperation"* ]]; then
            log_info "EFS tagging permission available"
        elif [[ "$tag_test_output" == *"AccessDenied"* ]] || [[ "$tag_test_output" == *"not authorized"* ]]; then
            warnings+=("elasticfilesystem:CreateTags (EFS resources will be created without tags)")
        fi
    fi
    
    if ! aws ec2 create-tags --region "$AWS_REGION" --resources "sg-test" --tags "Key=test,Value=test" --dry-run >/dev/null 2>&1; then
        local ec2_tag_test_output
        ec2_tag_test_output=$(aws ec2 create-tags --region "$AWS_REGION" --resources "sg-test" --tags "Key=test,Value=test" --dry-run 2>&1 || true)
        if [[ "$ec2_tag_test_output" == *"DryRunOperation"* ]]; then
            log_info "EC2 tagging permission available"
        elif [[ "$ec2_tag_test_output" == *"AccessDenied"* ]] || [[ "$ec2_tag_test_output" == *"not authorized"* ]]; then
            warnings+=("ec2:CreateTags (security groups will be created without tags)")
        fi
    fi
    
    # Report results
    if [[ ${#missing_permissions[@]} -gt 0 ]]; then
        log_error "Missing required AWS permissions:"
        for perm in "${missing_permissions[@]}"; do
            log_error "  - $perm"
        done
        log_error ""
        log_error "Please ensure your AWS credentials have the following permissions:"
        log_error "  EFS: CreateFileSystem, DescribeFileSystems, CreateMountTarget, DescribeMountTargets"
        log_error "  EC2: DescribeInstances, DescribeSecurityGroups, DescribeSubnets"
        log_error "  AuthorizeSecurityGroupIngress (may be needed for auto-detected SGs)"
        log_error "  Optional: CreateTags (for both EFS and EC2 resources)"
        exit 1
    fi
    
    if [[ ${#warnings[@]} -gt 0 ]]; then
        log_warning "Some optional permissions are missing:"
        for warning in "${warnings[@]}"; do
            log_warning "  - $warning"
        done
        log_warning "The script will continue but some features may be limited"
    fi
    
    log_success "AWS permissions check completed successfully"
}

# Function to get cluster VPC and subnets
get_cluster_network_info() {
    log_info "Getting cluster network information..."
    
    # Get VPC ID from worker nodes
    local vpc_id
    vpc_id=$(aws ec2 describe-instances \
        --region "$AWS_REGION" \
        --filters "Name=tag:Name,Values=${CLUSTER_NAME}-*" "Name=instance-state-name,Values=running" \
        --query 'Reservations[0].Instances[0].VpcId' \
        --output text 2>/dev/null || echo "")
    
    if [[ -z "$vpc_id" || "$vpc_id" == "None" ]]; then
        log_error "Could not find VPC for cluster $CLUSTER_NAME"
        exit 1
    fi
    
    echo "$vpc_id"
}

# Function to get worker node subnets
get_worker_subnets() {
    local vpc_id="$1"
    
    log_info "Getting worker node subnets..."
    
    # Get subnets from worker nodes
    aws ec2 describe-instances \
        --region "$AWS_REGION" \
        --filters "Name=tag:Name,Values=${CLUSTER_NAME}-*-worker-*" "Name=instance-state-name,Values=running" \
        --query 'Reservations[].Instances[].SubnetId' \
        --output text | tr '\t' '\n' | sort -u
}

# Function to auto-detect suitable security group for EFS
auto_detect_security_group() {
    local vpc_id="$1"
    
    log_info "Auto-detecting suitable security group for EFS..."
    
    # Strategy 1: Look for existing EFS security groups with NFS rules
    log_info "Looking for existing EFS security groups..."
    local efs_sgs
    efs_sgs=$(aws ec2 describe-security-groups \
        --region "$AWS_REGION" \
        --filters "Name=vpc-id,Values=$vpc_id" "Name=group-name,Values=*efs*" \
        --query 'SecurityGroups[?IpPermissions[?FromPort==`2049` && ToPort==`2049`]].GroupId' \
        --output text 2>/dev/null || echo "")
    
    if [[ -n "$efs_sgs" && "$efs_sgs" != "None" ]]; then
        local first_sg=$(echo "$efs_sgs" | awk '{print $1}')
        log_success "Found existing EFS security group with NFS rules: $first_sg"
        echo "$first_sg"
        return
    fi
    
    # Strategy 2: Look for worker node security groups that allow NFS traffic
    log_info "Looking for worker node security groups with NFS access..."
    local worker_sgs
    worker_sgs=$(aws ec2 describe-instances \
        --region "$AWS_REGION" \
        --filters "Name=tag:Name,Values=${CLUSTER_NAME}-*-worker-*" "Name=instance-state-name,Values=running" \
        --query 'Reservations[].Instances[].SecurityGroups[].GroupId' \
        --output text 2>/dev/null | tr '\t' '\n' | sort -u || echo "")
    
    if [[ -n "$worker_sgs" ]]; then
        for sg_id in $worker_sgs; do
            local nfs_check
            nfs_check=$(aws ec2 describe-security-groups \
                --region "$AWS_REGION" \
                --group-ids "$sg_id" \
                --query 'SecurityGroups[0].IpPermissions[?FromPort==`2049` && ToPort==`2049`]' \
                --output text 2>/dev/null || echo "")
            
            if [[ -n "$nfs_check" ]]; then
                log_success "Found worker security group with NFS rules: $sg_id"
                echo "$sg_id"
                return
            fi
        done
    fi
    
    # Strategy 3: Look for cluster security groups that might be suitable
    log_info "Looking for cluster security groups..."
    local cluster_sgs
    cluster_sgs=$(aws ec2 describe-security-groups \
        --region "$AWS_REGION" \
        --filters "Name=vpc-id,Values=$vpc_id" "Name=tag:kubernetes.io/cluster/${CLUSTER_NAME},Values=owned" \
        --query 'SecurityGroups[].GroupId' \
        --output text 2>/dev/null || echo "")
    
    if [[ -n "$cluster_sgs" && "$cluster_sgs" != "None" ]]; then
        for sg_id in $cluster_sgs; do
            # Check if this SG allows traffic from itself (common pattern for cluster SGs)
            local self_ref_check
            self_ref_check=$(aws ec2 describe-security-groups \
                --region "$AWS_REGION" \
                --group-ids "$sg_id" \
                --query "SecurityGroups[0].IpPermissions[?UserIdGroupPairs[?GroupId=='$sg_id']]" \
                --output text 2>/dev/null || echo "")
            
            if [[ -n "$self_ref_check" ]]; then
                log_info "Found cluster security group with self-referencing rules: $sg_id"
                log_info "This can be used for EFS access (will add NFS rule if needed)"
                echo "$sg_id"
                return
            fi
        done
    fi
    
    # Strategy 4: Use the first worker node security group as fallback
    if [[ -n "$worker_sgs" ]]; then
        local first_worker_sg=$(echo "$worker_sgs" | head -1)
        log_warning "No ideal security group found, using first worker SG: $first_worker_sg"
        log_warning "Will attempt to add NFS rules to this security group"
        echo "$first_worker_sg"
        return
    fi
    
    log_warning "Could not auto-detect suitable security group"
    echo ""
}

# Function to get or create security group for EFS
get_efs_security_group() {
    local vpc_id="$1"
    
    log_info "Setting up security group for EFS..."
    
    # Use existing security group if provided
    if [[ -n "$EXISTING_SECURITY_GROUP" ]]; then
        log_info "Using specified security group: $EXISTING_SECURITY_GROUP"
        
        # Validate the security group exists
        local sg_check
        sg_check=$(aws ec2 describe-security-groups \
            --region "$AWS_REGION" \
            --group-ids "$EXISTING_SECURITY_GROUP" \
            --query 'SecurityGroups[0].GroupId' \
            --output text 2>/dev/null || echo "")
        
        if [[ -z "$sg_check" || "$sg_check" == "None" ]]; then
            log_error "Specified security group $EXISTING_SECURITY_GROUP does not exist"
            exit 1
        fi
        
        # Check if the security group allows NFS traffic (port 2049)
        local nfs_rule_check
        nfs_rule_check=$(aws ec2 describe-security-groups \
            --region "$AWS_REGION" \
            --group-ids "$EXISTING_SECURITY_GROUP" \
            --query 'SecurityGroups[0].IpPermissions[?FromPort==`2049` && ToPort==`2049`]' \
            --output text 2>/dev/null || echo "")
        
        if [[ -z "$nfs_rule_check" ]]; then
            log_warning "Security group $EXISTING_SECURITY_GROUP does not appear to have NFS (port 2049) rules"
            log_warning "Please ensure it allows inbound NFS traffic for EFS to work properly"
        else
            log_info "Security group has NFS rules configured"
        fi
        
        echo "$EXISTING_SECURITY_GROUP"
        return
    fi
    
    # Try auto-detection if enabled
    if [[ "$AUTO_DETECT_SECURITY_GROUP" == "true" ]]; then
        local detected_sg
        detected_sg=$(auto_detect_security_group "$vpc_id")
        
        if [[ -n "$detected_sg" ]]; then
            log_success "Auto-detected security group: $detected_sg"
            
            # Check if we need to add NFS rules
            local nfs_rule_check
            nfs_rule_check=$(aws ec2 describe-security-groups \
                --region "$AWS_REGION" \
                --group-ids "$detected_sg" \
                --query 'SecurityGroups[0].IpPermissions[?FromPort==`2049` && ToPort==`2049`]' \
                --output text 2>/dev/null || echo "")
            
            if [[ -z "$nfs_rule_check" ]]; then
                log_info "Adding NFS rule to detected security group..."
                if [[ "$DRY_RUN" != "true" ]]; then
                    aws ec2 authorize-security-group-ingress \
                        --region "$AWS_REGION" \
                        --group-id "$detected_sg" \
                        --protocol tcp \
                        --port 2049 \
                        --source-group "$detected_sg" >/dev/null 2>&1 || \
                        log_warning "Could not add NFS rule to security group (permission issue), please ensure NFS access is allowed"
                else
                    log_info "[DRY RUN] Would add NFS rule to security group $detected_sg"
                fi
            fi
            
                        echo "$detected_sg"
            return
        else
            log_error "Auto-detection failed to find suitable security group"
            log_error "No existing security group found that meets the requirements:"
            log_error "  - EFS security groups with NFS (port 2049) rules"
            log_error "  - Worker node security groups with NFS access"
            log_error "  - Cluster security groups with self-referencing rules"
            log_error ""
            log_error "Solutions:"
            log_error "  3. Ensure an existing security group allows NFS traffic (port 2049)"
            exit 1
        fi
    fi

    # Check if EFS security group already exists (fallback for when auto-detection is disabled)
    if [[ "$AUTO_DETECT_SECURITY_GROUP" != "true" ]]; then
        local sg_id
        sg_id=$(aws ec2 describe-security-groups \
            --region "$AWS_REGION" \
            --filters "Name=group-name,Values=${CLUSTER_NAME}-efs-sg" "Name=vpc-id,Values=$vpc_id" \
            --query 'SecurityGroups[0].GroupId' \
            --output text 2>/dev/null || echo "")

        if [[ -n "$sg_id" && "$sg_id" != "None" ]]; then
            log_info "Using existing EFS security group: $sg_id"
            echo "$sg_id"
            return
        fi
    fi

    # If we reach here and require existing SG, fail
    log_error "No suitable security group found and creation is not allowed"

    if [[ "$DRY_RUN" == "true" ]]; then
        log_info "[DRY RUN] Would create security group: ${CLUSTER_NAME}-efs-sg"
        echo "sg-dryrun"
        return
    fi

    # Create security group for EFS
    log_info "Creating EFS security group..."
    sg_id=$(aws ec2 create-security-group \
        --region "$AWS_REGION" \
        --group-name "${CLUSTER_NAME}-efs-sg" \
        --description "Security group for EFS access from ${CLUSTER_NAME} cluster" \
        --vpc-id "$vpc_id" \
        --query 'GroupId' \
        --output text)

    # Add rule to allow NFS traffic from worker nodes
    aws ec2 authorize-security-group-ingress \
        --region "$AWS_REGION" \
        --group-id "$sg_id" \
        --protocol tcp \
        --port 2049 \
        --source-group "$sg_id" >/dev/null

    # Tag the security group
    aws ec2 create-tags \
        --region "$AWS_REGION" \
        --resources "$sg_id" \
        --tags "Key=Name,Value=${CLUSTER_NAME}-efs-sg" \
               "Key=kubernetes.io/cluster/${CLUSTER_NAME},Value=owned" >/dev/null

    log_success "Created EFS security group: $sg_id"
    echo "$sg_id"
}

# Function to create EFS filesystem
create_efs_filesystem() {
    log_info "Creating EFS filesystem..."
    
    # Check if EFS already exists
    local efs_id
    efs_id=$(aws efs describe-file-systems \
        --region "$AWS_REGION" \
        --query "FileSystems[?Tags[?Key=='Name' && Value=='$EFS_NAME']].FileSystemId" \
        --output text 2>/dev/null || echo "")
    
    if [[ -n "$efs_id" && "$efs_id" != "None" ]]; then
        log_info "Using existing EFS filesystem: $efs_id"
        echo "$efs_id"
        return
    fi
    
    if [[ "$DRY_RUN" == "true" ]]; then
        log_info "[DRY RUN] Would create EFS filesystem: $EFS_NAME"
        echo "fs-dryrun"
        return
    fi
    
    # Create EFS filesystem
    local create_args=(
        --region "$AWS_REGION"
        --performance-mode "$PERFORMANCE_MODE"
        --throughput-mode "$THROUGHPUT_MODE"
    )
    
    if [[ "$THROUGHPUT_MODE" == "provisioned" ]]; then
        create_args+=(--provisioned-throughput-in-mibps "$PROVISIONED_THROUGHPUT")
    fi
    
    efs_id=$(aws efs create-file-system "${create_args[@]}" --query 'FileSystemId' --output text)
    
    # Add tags separately to handle permission issues
    aws efs create-tags \
        --region "$AWS_REGION" \
        --file-system-id "$efs_id" \
        --tags "Key=Name,Value=$EFS_NAME" "Key=kubernetes.io/cluster/${CLUSTER_NAME},Value=owned" >/dev/null 2>&1 || \
        log_warning "Could not add tags to EFS filesystem (permission issue), but filesystem was created successfully"
    
    # Wait for EFS to be available
    log_info "Waiting for EFS filesystem to be available..."
    local max_attempts=30
    local attempt=0
    while [[ $attempt -lt $max_attempts ]]; do
        local state
        state=$(aws efs describe-file-systems \
            --region "$AWS_REGION" \
            --file-system-id "$efs_id" \
            --query 'FileSystems[0].LifeCycleState' \
            --output text 2>/dev/null || echo "")
        
        if [[ "$state" == "available" ]]; then
            break
        elif [[ "$state" == "error" ]]; then
            log_error "EFS filesystem creation failed"
            exit 1
        fi
        
        log_info "EFS state: $state, waiting... (attempt $((attempt + 1))/$max_attempts)"
        sleep 10
        ((attempt++))
    done
    
    if [[ $attempt -ge $max_attempts ]]; then
        log_error "Timeout waiting for EFS filesystem to become available"
        exit 1
    fi
    
    log_success "Created EFS filesystem: $efs_id"
    echo "$efs_id"
}

# Function to create EFS mount targets
create_mount_targets() {
    local efs_id="$1"
    local sg_id="$2"
    local subnets="$3"
    
    log_info "Creating EFS mount targets..."
    
    if [[ "$DRY_RUN" == "true" ]]; then
        log_info "[DRY RUN] Would create mount targets in subnets: $(echo $subnets | tr '\n' ' ')"
        return
    fi
    
    # Create mount target in each subnet
    while IFS= read -r subnet_id; do
        if [[ -n "$subnet_id" ]]; then
            log_info "Creating mount target in subnet: $subnet_id"
            
            # Check if mount target already exists
            local existing_mt
            existing_mt=$(aws efs describe-mount-targets \
                --region "$AWS_REGION" \
                --file-system-id "$efs_id" \
                --query "MountTargets[?SubnetId=='$subnet_id'].MountTargetId" \
                --output text 2>/dev/null || echo "")
            
            if [[ -n "$existing_mt" && "$existing_mt" != "None" ]]; then
                log_info "Mount target already exists in subnet $subnet_id: $existing_mt"
                continue
            fi
            
            aws efs create-mount-target \
                --region "$AWS_REGION" \
                --file-system-id "$efs_id" \
                --subnet-id "$subnet_id" \
                --security-groups "$sg_id" >/dev/null
        fi
    done <<< "$subnets"
    
    # Wait for all mount targets to be available
    log_info "Waiting for mount targets to be available..."
    local max_attempts=30
    local attempt=0
    while [[ $attempt -lt $max_attempts ]]; do
        local mount_states
        mount_states=$(aws efs describe-mount-targets \
            --region "$AWS_REGION" \
            --file-system-id "$efs_id" \
            --query 'MountTargets[].LifeCycleState' \
            --output text 2>/dev/null || echo "")
        
        if [[ -z "$mount_states" ]]; then
            log_info "No mount targets found yet, waiting... (attempt $((attempt + 1))/$max_attempts)"
            sleep 10
            ((attempt++))
            continue
        fi
        
        # Check if all mount targets are available
        local all_available=true
        for state in $mount_states; do
            if [[ "$state" != "available" ]]; then
                if [[ "$state" == "error" ]]; then
                    log_error "Mount target creation failed"
                    exit 1
                fi
                all_available=false
                break
            fi
        done
        
        if [[ "$all_available" == "true" ]]; then
            break
        fi
        
        log_info "Mount target states: $mount_states, waiting... (attempt $((attempt + 1))/$max_attempts)"
        sleep 10
        ((attempt++))
    done
    
    if [[ $attempt -ge $max_attempts ]]; then
        log_error "Timeout waiting for mount targets to become available"
        exit 1
    fi
    
    log_success "Created EFS mount targets"
}

# Function to create Kubernetes resources
create_k8s_resources() {
    local efs_id="$1"
    
    log_info "Creating Kubernetes resources..."
    
    if [[ "$DRY_RUN" == "true" ]]; then
        log_info "[DRY RUN] Would create PV: $PV_NAME and PVC: $PVC_NAME in namespace: $NAMESPACE"
        return
    fi
    
    # Create namespace if it doesn't exist
    $KUBECTL create namespace "$NAMESPACE" --dry-run=client -o yaml | $KUBECTL apply -f -
    
    # Create PersistentVolume
    cat << EOF | $KUBECTL apply -f -
apiVersion: v1
kind: PersistentVolume
metadata:
  name: $PV_NAME
  labels:
    storage-type: efs-rwx
    cluster: $CLUSTER_NAME
spec:
  capacity:
    storage: $STORAGE_SIZE
  volumeMode: Filesystem
  accessModes:
    - ReadWriteMany
  persistentVolumeReclaimPolicy: Retain
  storageClassName: efs-sc
  csi:
    driver: efs.csi.aws.com
    volumeHandle: $efs_id
    volumeAttributes:
      region: $AWS_REGION
EOF
    
    # Create StorageClass
    cat << EOF | $KUBECTL apply -f -
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: efs-sc
  labels:
    storage-type: efs-rwx
provisioner: efs.csi.aws.com
parameters:
  provisioningMode: efs-ap
  fileSystemId: $efs_id
  directoryPerms: "0755"
allowVolumeExpansion: true
EOF
    
    # Create PersistentVolumeClaim
    cat << EOF | $KUBECTL apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: $PVC_NAME
  namespace: $NAMESPACE
  labels:
    storage-type: efs-rwx
    cluster: $CLUSTER_NAME
spec:
  accessModes:
    - ReadWriteMany
  storageClassName: efs-sc
  resources:
    requests:
      storage: $STORAGE_SIZE
EOF
    
    log_success "Created Kubernetes resources"
}

# Function to cleanup resources
cleanup_resources() {
    log_warning "Cleaning up EFS and Kubernetes resources..."
    
    if [[ "$DRY_RUN" == "true" ]]; then
        log_info "[DRY RUN] Would cleanup all resources"
        return
    fi
    
    # Delete Kubernetes resources
    log_info "Deleting Kubernetes resources..."
    $KUBECTL delete pvc "$PVC_NAME" -n "$NAMESPACE" --ignore-not-found=true
    $KUBECTL delete pv "$PV_NAME" --ignore-not-found=true
    $KUBECTL delete storageclass efs-sc --ignore-not-found=true
    
    # Get EFS ID
    local efs_id
    efs_id=$(aws efs describe-file-systems \
        --region "$AWS_REGION" \
        --query "FileSystems[?Tags[?Key=='Name' && Value=='$EFS_NAME']].FileSystemId" \
        --output text 2>/dev/null || echo "")
    
    if [[ -n "$efs_id" && "$efs_id" != "None" ]]; then
        # Delete mount targets
        log_info "Deleting EFS mount targets..."
        local mount_targets
        mount_targets=$(aws efs describe-mount-targets \
            --region "$AWS_REGION" \
            --file-system-id "$efs_id" \
            --query 'MountTargets[].MountTargetId' \
            --output text)
        
        for mt_id in $mount_targets; do
            if [[ -n "$mt_id" && "$mt_id" != "None" ]]; then
                aws efs delete-mount-target --region "$AWS_REGION" --mount-target-id "$mt_id"
            fi
        done
        
        # Wait for mount targets to be deleted
        if [[ -n "$mount_targets" ]]; then
            log_info "Waiting for mount targets to be deleted..."
            sleep 10
        fi
        
        # Delete EFS filesystem
        log_info "Deleting EFS filesystem..."
        aws efs delete-file-system --region "$AWS_REGION" --file-system-id "$efs_id"
    fi
    
    # Delete security group
    local vpc_id
    vpc_id=$(get_cluster_network_info)
    local sg_id
    sg_id=$(aws ec2 describe-security-groups \
        --region "$AWS_REGION" \
        --filters "Name=group-name,Values=${CLUSTER_NAME}-efs-sg" "Name=vpc-id,Values=$vpc_id" \
        --query 'SecurityGroups[0].GroupId' \
        --output text 2>/dev/null || echo "")
    
    if [[ -n "$sg_id" && "$sg_id" != "None" ]]; then
        log_info "Deleting EFS security group..."
        aws ec2 delete-security-group --region "$AWS_REGION" --group-id "$sg_id"
    fi
    
    log_success "Cleanup completed"
}

# Function to verify EFS CSI driver
verify_efs_csi_driver() {
    log_info "Verifying EFS CSI driver installation..."
    
    if ! $KUBECTL get csidriver efs.csi.aws.com &>/dev/null; then
        log_error "EFS CSI driver not found. Please install it first:"
        log_error "  oc apply -k 'github.com/kubernetes-sigs/aws-efs-csi-driver/deploy/kubernetes/overlays/stable/?ref=release-1.7'"
        exit 1
    fi
    
    log_success "EFS CSI driver is installed"
}

# Function to display summary
show_summary() {
    local efs_id="$1"
    
    log_success "RWX Persistent Volume setup completed!"
    echo
    echo "📋 Summary:"
    echo "  EFS Filesystem ID: $efs_id"
    echo "  EFS Name: $EFS_NAME"
    echo "  Persistent Volume: $PV_NAME"
    echo "  Persistent Volume Claim: $PVC_NAME"
    echo "  Namespace: $NAMESPACE"
    echo "  Storage Size: $STORAGE_SIZE"
    echo "  Access Mode: ReadWriteMany (RWX)"
    echo
    echo "🚀 Usage in pods:"
    echo "  spec:"
    echo "    volumes:"
    echo "    - name: shared-storage"
    echo "      persistentVolumeClaim:"
    echo "        claimName: $PVC_NAME"
    echo "    containers:"
    echo "    - name: my-container"
    echo "      volumeMounts:"
    echo "      - name: shared-storage"
    echo "        mountPath: /shared"
    echo
    echo "🔍 Verify with:"
    echo "  $KUBECTL get pv $PV_NAME"
    echo "  $KUBECTL get pvc $PVC_NAME -n $NAMESPACE"
}

# Main execution
main() {
    log_info "Starting RWX PV creation for OpenShift on AWS"
    # Check tools
    check_tools
    
    # Handle cleanup
    if [[ "$CLEANUP" == "true" ]]; then
        detect_cluster_name
        cleanup_resources
        exit 0
    fi
    
    # Verify EFS CSI driver
    verify_efs_csi_driver
    
    # Auto-detect cluster name
    detect_cluster_name
    
    # Auto-detect AWS region
    detect_aws_region
    
    # Check AWS permissions
    if [[ "$SKIP_PERMISSION_CHECK" != "true" ]]; then
        check_aws_permissions
    else
        log_warning "Skipping AWS permission check (--skip-permission-check specified)"
    fi
    
    # Get cluster network information
    local vpc_id
    vpc_id=$(get_cluster_network_info)
    
    # Get worker node subnets
    local subnets
    subnets=$(get_worker_subnets "$vpc_id")
    
    if [[ -z "$subnets" ]]; then
        log_error "No worker node subnets found"
        exit 1
    fi
    
    log_info "Found worker subnets: $(echo $subnets | tr '\n' ' ')"
    
    # Create or get security group
    local sg_id
    sg_id=$(get_efs_security_group "$vpc_id")
    
    # Create EFS filesystem
    local efs_id
    efs_id=$(create_efs_filesystem)
    
    # Create mount targets
    create_mount_targets "$efs_id" "$sg_id" "$subnets"
    
    # Create Kubernetes resources
    create_k8s_resources "$efs_id"
    
    # Show summary
    if [[ "$DRY_RUN" != "true" ]]; then
        show_summary "$efs_id"
    else
        log_info "[DRY RUN] All operations completed successfully (no actual resources created)"
    fi
}

# Run main function
main "$@" 
