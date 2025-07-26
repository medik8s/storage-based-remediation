#!/bin/bash

set -euo pipefail

# Emergency Reboot All Workers Script
# This script will reboot ALL worker nodes in the cluster simultaneously using AWS EC2 API
# WARNING: This will make your cluster UNAVAILABLE for several minutes

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EMERGENCY_REBOOT_SCRIPT="${SCRIPT_DIR}/emergency-reboot-node.sh"
TIMEOUT_SECONDS=300
DRY_RUN=false
FORCE=false

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Usage function
usage() {
    cat << EOF
Usage: $0 [OPTIONS]

Emergency reboot ALL worker nodes in the cluster simultaneously.

WARNING: This will make your cluster UNAVAILABLE for several minutes!

OPTIONS:
    -d, --dry-run          Show what would be done without executing
    -f, --force            Skip all confirmation prompts (for automation)
    -t, --timeout SECONDS  Timeout for waiting for nodes to reboot (default: 300)
    -h, --help             Show this help message

EXAMPLES:
    $0                     # Interactive mode with confirmations
    $0 --dry-run           # Show what nodes would be rebooted
    $0 --force             # Non-interactive mode for automation
    $0 -f -t 600           # Force mode with 10-minute timeout

REQUIREMENTS:
    - kubectl configured for cluster access
    - AWS CLI configured with appropriate permissions
    - emergency-reboot-node.sh script in same directory

EOF
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -d|--dry-run)
            DRY_RUN=true
            shift
            ;;
        -f|--force)
            FORCE=true
            shift
            ;;
        -t|--timeout)
            TIMEOUT_SECONDS="$2"
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            usage
            exit 1
            ;;
    esac
done

# Function to log with timestamp
log() {
    echo -e "${BLUE}[$(date +'%Y-%m-%d %H:%M:%S')]${NC} $1"
}

log_error() {
    echo -e "${RED}[$(date +'%Y-%m-%d %H:%M:%S')] ERROR:${NC} $1" >&2
}

log_warning() {
    echo -e "${YELLOW}[$(date +'%Y-%m-%d %H:%M:%S')] WARNING:${NC} $1"
}

log_success() {
    echo -e "${GREEN}[$(date +'%Y-%m-%d %H:%M:%S')] SUCCESS:${NC} $1"
}

# Function to check prerequisites
check_prerequisites() {
    log "Checking prerequisites..."
    
    # Check if kubectl is available
    if ! command -v kubectl &> /dev/null; then
        log_error "kubectl not found. Please install kubectl and configure cluster access."
        exit 1
    fi
    
    # Check if AWS CLI is available
    if ! command -v aws &> /dev/null; then
        log_error "AWS CLI not found. Please install AWS CLI and configure credentials."
        exit 1
    fi
    
    # Check if emergency-reboot-node.sh exists
    if [[ ! -f "$EMERGENCY_REBOOT_SCRIPT" ]]; then
        log_error "Emergency reboot script not found: $EMERGENCY_REBOOT_SCRIPT"
        exit 1
    fi
    
    # Check if script is executable
    if [[ ! -x "$EMERGENCY_REBOOT_SCRIPT" ]]; then
        log_error "Emergency reboot script is not executable: $EMERGENCY_REBOOT_SCRIPT"
        exit 1
    fi
    
    # Test cluster connectivity
    if ! kubectl cluster-info &> /dev/null; then
        log_error "Cannot connect to Kubernetes cluster. Please check kubectl configuration."
        exit 1
    fi
    
    log_success "All prerequisites satisfied"
}

# Function to get worker nodes
get_worker_nodes() {
    local worker_nodes
    
    # Capture node names first without any logging to avoid contamination
    worker_nodes=$(kubectl get nodes -l node-role.kubernetes.io/worker -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || true)
    
    # Now log after capturing the data
    log "Getting list of worker nodes..."
    
    if [[ -z "$worker_nodes" ]]; then
        log_error "No worker nodes found in the cluster"
        exit 1
    fi
    
    # Return only the node names without any log output
    printf "%s" "$worker_nodes"
}

# Function to show worker nodes and get confirmation
show_nodes_and_confirm() {
    local worker_nodes=("$@")
    
    echo ""
    echo -e "${RED}===========================================${NC}"
    echo -e "${RED}  EXTREME DANGER: ALL WORKER REBOOT${NC}"
    echo -e "${RED}===========================================${NC}"
    echo ""
    echo -e "${YELLOW}The following ${#worker_nodes[@]} worker nodes will be REBOOTED:${NC}"
    echo ""
    
    for i in "${!worker_nodes[@]}"; do
        echo -e "${BLUE}  $((i+1)). ${worker_nodes[i]}${NC}"
    done
    
    echo ""
    echo -e "${RED}WARNING: This will make your cluster UNAVAILABLE for several minutes!${NC}"
    echo -e "${YELLOW}- All workloads on worker nodes will be terminated${NC}"
    echo -e "${YELLOW}- Only control plane nodes will remain operational${NC}"
    echo -e "${YELLOW}- Cluster will be unusable until worker nodes recover${NC}"
    echo ""
    
    if [[ "$FORCE" == "true" ]]; then
        log_warning "FORCE mode enabled - proceeding without confirmation"
        return 0
    fi
    
    echo -e "${YELLOW}Type 'REBOOT-ALL-WORKERS' to confirm this destructive operation:${NC}"
    read -r confirmation
    
    if [[ "$confirmation" != "REBOOT-ALL-WORKERS" ]]; then
        log_error "Operation cancelled - incorrect confirmation phrase"
        exit 1
    fi
    
    log_success "Confirmation received - proceeding with reboot operation"
}

# Function to reboot all worker nodes in parallel
reboot_all_workers() {
    local worker_nodes=("$@")
    local pids=()
    local start_time
    
    log "Starting parallel reboot of ${#worker_nodes[@]} worker nodes..."
    start_time=$(date +%s)
    
    # Start reboot processes in parallel
    for node in "${worker_nodes[@]}"; do
        if [[ "$DRY_RUN" == "true" ]]; then
            log "DRY RUN: Would reboot node: $node"
        else
            log "Starting reboot for node: $node"
            "$EMERGENCY_REBOOT_SCRIPT" --force "$node" &
            pids+=($!)
        fi
    done
    
    if [[ "$DRY_RUN" == "true" ]]; then
        log_success "DRY RUN completed - no actual reboots performed"
        return 0
    fi
    
    # Wait for all reboot commands to complete
    log "Waiting for all reboot commands to complete..."
    local failed_count=0
    
    for i in "${!pids[@]}"; do
        local pid=${pids[i]}
        local node=${worker_nodes[i]}
        
        if wait "$pid"; then
            log_success "Reboot command completed for node: $node"
        else
            log_error "Reboot command failed for node: $node"
            ((failed_count++))
        fi
    done
    
    local end_time
    end_time=$(date +%s)
    local duration=$((end_time - start_time))
    
    if [[ $failed_count -eq 0 ]]; then
        log_success "All reboot commands completed successfully in ${duration} seconds"
    else
        log_error "$failed_count reboot commands failed"
        return 1
    fi
}

# Function to monitor node recovery
monitor_node_recovery() {
    local worker_nodes=("$@")
    local start_time
    local recovered_nodes=()
    
    if [[ "$DRY_RUN" == "true" ]]; then
        return 0
    fi
    
    log "Monitoring node recovery (timeout: ${TIMEOUT_SECONDS}s)..."
    start_time=$(date +%s)
    
    while [[ ${#recovered_nodes[@]} -lt ${#worker_nodes[@]} ]]; do
        local current_time
        current_time=$(date +%s)
        local elapsed=$((current_time - start_time))
        
        if [[ $elapsed -gt $TIMEOUT_SECONDS ]]; then
            log_error "Timeout reached waiting for nodes to recover"
            break
        fi
        
        # Check each node that hasn't recovered yet
        for node in "${worker_nodes[@]}"; do
            # Skip if already recovered
            if [[ " ${recovered_nodes[*]} " =~ " $node " ]]; then
                continue
            fi
            
            # Check if node is Ready
            local node_status
            node_status=$(kubectl get node "$node" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || echo "Unknown")
            
            if [[ "$node_status" == "True" ]]; then
                recovered_nodes+=("$node")
                log_success "Node recovered: $node (${#recovered_nodes[@]}/${#worker_nodes[@]})"
            fi
        done
        
        if [[ ${#recovered_nodes[@]} -lt ${#worker_nodes[@]} ]]; then
            sleep 10
        fi
    done
    
    local end_time
    end_time=$(date +%s)
    local total_duration=$((end_time - start_time))
    
    if [[ ${#recovered_nodes[@]} -eq ${#worker_nodes[@]} ]]; then
        log_success "All ${#worker_nodes[@]} worker nodes recovered in ${total_duration} seconds"
    else
        local missing_count=$((${#worker_nodes[@]} - ${#recovered_nodes[@]}))
        log_error "$missing_count nodes failed to recover within timeout"
        
        # Show which nodes are still missing
        for node in "${worker_nodes[@]}"; do
            if [[ ! " ${recovered_nodes[*]} " =~ " $node " ]]; then
                log_error "Node still not ready: $node"
            fi
        done
        
        return 1
    fi
}

# Main execution
main() {
    log "Starting emergency reboot all workers script"
    
    # Check prerequisites
    check_prerequisites
    
    # Get worker nodes
    log "Getting list of worker nodes..."
    local worker_nodes_str
    worker_nodes_str=$(kubectl get nodes -l node-role.kubernetes.io/worker -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || true)
    
    # Convert to array
    local worker_nodes
    read -ra worker_nodes <<< "$worker_nodes_str"
    
    if [[ ${#worker_nodes[@]} -eq 0 ]] || [[ -z "$worker_nodes_str" ]]; then
        log_error "No worker nodes found in the cluster"
        exit 1
    fi
    
    # Show nodes and get confirmation
    show_nodes_and_confirm "${worker_nodes[@]}"
    
    # Reboot all workers
    if ! reboot_all_workers "${worker_nodes[@]}"; then
        log_error "Some reboot commands failed"
        exit 1
    fi
    
    # Monitor recovery
    if ! monitor_node_recovery "${worker_nodes[@]}"; then
        log_error "Some nodes failed to recover"
        exit 1
    fi
    
    log_success "Emergency reboot all workers operation completed successfully"
}

# Run main function
main "$@" 