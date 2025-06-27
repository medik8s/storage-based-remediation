#!/bin/bash

# AWS EFS RWX Persistent Volume Creation Script for OpenShift
# This script creates an AWS EFS filesystem and corresponding Kubernetes resources
# for ReadWriteMany (RWX) storage that can be shared across multiple OCP worker nodes

set -e

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
NAMESPACE="sbd-system"
STORAGE_SIZE="10Gi"
AWS_REGION="${AWS_REGION:-us-east-1}"
CLUSTER_NAME=""
PERFORMANCE_MODE="generalPurpose"  # generalPurpose or maxIO
THROUGHPUT_MODE="provisioned"      # provisioned or burstingThroughput
PROVISIONED_THROUGHPUT="100"       # MiB/s (only for provisioned mode)
KUBECTL="${KUBECTL:-kubectl}"
CLEANUP="false"
DRY_RUN="false"

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

Create AWS EFS-based ReadWriteMany (RWX) Persistent Volume for OpenShift

OPTIONS:
    -n, --name NAME           EFS filesystem name (default: sbd-operator-shared-storage)
    -p, --pv-name NAME        Persistent Volume name (default: sbd-shared-pv)
    -c, --pvc-name NAME       Persistent Volume Claim name (default: sbd-shared-pvc)
    -s, --size SIZE           Storage size (default: 10Gi)
    -r, --region REGION       AWS region (default: us-east-1)
    -k, --cluster-name NAME   OpenShift cluster name (auto-detected if not provided)
    -N, --namespace NS        Kubernetes namespace (default: sbd-system)
    --performance-mode MODE   EFS performance mode: generalPurpose|maxIO (default: generalPurpose)
    --throughput-mode MODE    EFS throughput mode: provisioned|burstingThroughput (default: provisioned)
    --provisioned-tp MBPS     Provisioned throughput in MiB/s (default: 100, only for provisioned mode)
    --cleanup                 Delete existing EFS and Kubernetes resources
    --dry-run                 Show what would be created without actually creating
    -h, --help                Show this help message

EXAMPLES:
    # Create RWX PV with defaults
    $0

    # Create with custom settings
    $0 --name my-shared-storage --size 50Gi --cluster-name my-cluster

    # Cleanup existing resources
    $0 --cleanup

    # Preview what would be created
    $0 --dry-run

PREREQUISITES:
    - AWS CLI configured with appropriate permissions
    - kubectl/oc configured for target OpenShift cluster
    - AWS EFS CSI driver installed in the cluster
    - Cluster must have worker nodes in multiple AZs for HA

AWS PERMISSIONS REQUIRED:
    - efs:CreateFileSystem
    - efs:CreateMountTarget
    - efs:DescribeFileSystems
    - efs:DescribeMountTargets
    - ec2:DescribeSubnets
    - ec2:DescribeSecurityGroups
    - ec2:DescribeVpcs

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
        --cleanup)
            CLEANUP="true"
            shift
            ;;
        --dry-run)
            DRY_RUN="true"
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
        
        # Try to get cluster name from current context
        local context_cluster
        context_cluster=$($KUBECTL config current-context 2>/dev/null || echo "")
        
        if [[ -n "$context_cluster" ]]; then
            # Extract cluster name from context (format varies)
            if [[ "$context_cluster" =~ admin@(.+) ]]; then
                CLUSTER_NAME="${BASH_REMATCH[1]}"
            elif [[ "$context_cluster" =~ (.+)/api ]]; then
                CLUSTER_NAME="${BASH_REMATCH[1]}"
            else
                CLUSTER_NAME="$context_cluster"
            fi
        fi
        
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

# Function to get cluster VPC and subnets
get_cluster_network_info() {
    log_info "Getting cluster network information..."
    
    # Get VPC ID from worker nodes
    local vpc_id
    vpc_id=$(aws ec2 describe-instances \
        --region "$AWS_REGION" \
        --filters "Name=tag:Name,Values=${CLUSTER_NAME}-worker-*" "Name=instance-state-name,Values=running" \
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
        --filters "Name=tag:Name,Values=${CLUSTER_NAME}-worker-*" "Name=instance-state-name,Values=running" \
        --query 'Reservations[].Instances[].SubnetId' \
        --output text | tr '\t' '\n' | sort -u
}

# Function to get or create security group for EFS
get_efs_security_group() {
    local vpc_id="$1"
    
    log_info "Setting up security group for EFS..."
    
    # Check if EFS security group already exists
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
        --source-group "$sg_id"
    
    # Tag the security group
    aws ec2 create-tags \
        --region "$AWS_REGION" \
        --resources "$sg_id" \
        --tags "Key=Name,Value=${CLUSTER_NAME}-efs-sg" \
               "Key=kubernetes.io/cluster/${CLUSTER_NAME},Value=owned"
    
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
        --tags "Key=Name,Value=$EFS_NAME,Key=kubernetes.io/cluster/${CLUSTER_NAME},Value=owned"
    )
    
    if [[ "$THROUGHPUT_MODE" == "provisioned" ]]; then
        create_args+=(--provisioned-throughput-in-mibps "$PROVISIONED_THROUGHPUT")
    fi
    
    efs_id=$(aws efs create-file-system "${create_args[@]}" --query 'FileSystemId' --output text)
    
    # Wait for EFS to be available
    log_info "Waiting for EFS filesystem to be available..."
    aws efs wait file-system-available --region "$AWS_REGION" --file-system-id "$efs_id"
    
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
    aws efs wait mount-target-available --region "$AWS_REGION" --file-system-id "$efs_id"
    
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