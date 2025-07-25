#!/bin/bash

# Copyright 2025.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

# Script configuration
SCRIPT_NAME="emergency-reboot-node.sh"
SCRIPT_VERSION="1.0.0"
TIMEOUT_SECONDS=${TIMEOUT_SECONDS:-30}
FORCE_REBOOT=${FORCE_REBOOT:-false}
DRY_RUN=${DRY_RUN:-false}

# Colors for output
RED='\033[0;31m'
YELLOW='\033[1;33m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Logging functions
log_info() {
    echo -e "${BLUE}[INFO]${NC} $*" >&2
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $*" >&2
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $*" >&2
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $*" >&2
}

# Usage function
usage() {
    cat <<EOF
Usage: $SCRIPT_NAME [OPTIONS] <NODE_NAME>

Emergency ungraceful reboot script for OpenShift nodes.

WARNING: This script performs an IMMEDIATE, UNGRACEFUL reboot of the specified node.
         All running workloads will be terminated without proper shutdown.
         Use only for emergency fencing scenarios.

ARGUMENTS:
  NODE_NAME               Name of the OpenShift node to reboot

OPTIONS:
  -f, --force             Skip confirmation prompt (use with caution)
  -d, --dry-run           Show what would be done without executing
  -t, --timeout SECONDS   Timeout for operations (default: 30)
  -h, --help              Show this help message
  -v, --version           Show script version

ENVIRONMENT VARIABLES:
  TIMEOUT_SECONDS         Override default timeout (default: 30)
  FORCE_REBOOT           Skip confirmation if set to 'true'
  DRY_RUN               Enable dry-run mode if set to 'true'

EXAMPLES:
  # Interactive reboot with confirmation and automatic monitoring
  $SCRIPT_NAME worker-node-1

  # Force reboot without confirmation (dangerous!)
  $SCRIPT_NAME --force worker-node-1

  # Dry run to see what would happen
  $SCRIPT_NAME --dry-run worker-node-1

  # Reboot with custom timeout and monitoring
  $SCRIPT_NAME --timeout 60 worker-node-1

SAFETY NOTES:
  - This script uses AWS EC2 API to reboot instances directly
  - Performs immediate ungraceful reboot at the hypervisor level
  - No graceful shutdown of applications occurs
  - Automatically monitors node status to verify reboot completed
  - Requires AWS CLI and appropriate EC2 permissions (ec2:RebootInstances)
  - Only works with AWS-hosted OpenShift/Kubernetes clusters
  - Use only when normal node remediation has failed
  - Consider cluster impact before rebooting control plane nodes

EOF
}

# Version function
version() {
    echo "$SCRIPT_NAME version $SCRIPT_VERSION"
}

# Check prerequisites
check_prerequisites() {
    log_info "Checking prerequisites..."

    # Check if oc command is available
    if ! command -v oc &> /dev/null; then
        log_error "OpenShift CLI (oc) is not installed or not in PATH"
        exit 1
    fi

    # Check if we're logged into a cluster
    if ! oc whoami &> /dev/null; then
        log_error "Not logged into an OpenShift cluster. Please run 'oc login' first."
        exit 1
    fi

    # Get cluster info
    local cluster_info
    cluster_info=$(oc cluster-info | head -n 1 || true)
    log_info "Connected to cluster: $cluster_info"

    log_success "Prerequisites check passed"
}

# Validate node exists and get node info
validate_node() {
    local node_name="$1"
    
    log_info "Validating node '$node_name'..."

    # Check if node exists
    if ! oc get node "$node_name" &> /dev/null; then
        log_error "Node '$node_name' not found in the cluster"
        log_info "Available nodes:"
        oc get nodes --no-headers -o custom-columns=":metadata.name" || true
        exit 1
    fi

    # Get node information
    local node_info
    node_info=$(oc get node "$node_name" -o wide --no-headers)
    log_info "Node information:"
    echo "  $node_info"

    # Check node status
    local node_status
    node_status=$(oc get node "$node_name" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')
    local node_role
    node_role=$(oc get node "$node_name" -o jsonpath='{.metadata.labels.node-role\.kubernetes\.io/master}' 2>/dev/null || echo "")
    
    if [[ "$node_role" == "true" ]]; then
        log_warn "WARNING: '$node_name' is a CONTROL PLANE node!"
        log_warn "Rebooting control plane nodes can cause cluster instability!"
    fi

    if [[ "$node_status" != "True" ]]; then
        log_warn "Node '$node_name' is not in Ready status (current: $node_status)"
    fi

    log_success "Node validation completed"
}

# Show confirmation prompt
confirm_reboot() {
    local node_name="$1"
    
    if [[ "$FORCE_REBOOT" == "true" ]]; then
        log_warn "FORCE mode enabled - skipping confirmation"
        return 0
    fi

    echo
    log_warn "═══════════════════════════════════════════════════════════════"
    log_warn "                        ⚠️  DANGER ZONE  ⚠️"
    log_warn "═══════════════════════════════════════════════════════════════"
    log_warn "You are about to IMMEDIATELY and UNGRACEFULLY reboot:"
    log_warn "  Node: $node_name"
    log_warn ""
    log_warn "This will:"
    log_warn "  • Immediately terminate ALL running containers/pods"
    log_warn "  • Cause data loss for applications without persistent storage"
    log_warn "  • Potentially cause cluster instability"
    log_warn "  • Trigger workload rescheduling to other nodes"
    log_warn ""
    log_warn "This action CANNOT be undone!"
    log_warn "═══════════════════════════════════════════════════════════════"
    echo

    read -p "Are you absolutely sure you want to reboot '$node_name'? (type 'YES' to confirm): " confirmation
    
    if [[ "$confirmation" != "YES" ]]; then
        log_info "Reboot cancelled by user"
        exit 0
    fi

    log_warn "Confirmation received. Proceeding with emergency reboot..."
}

# Get AWS instance ID for cloud API reboot
get_aws_instance_id() {
    local node_name="$1"
    
    # Try to get instance ID from node's provider ID
    local instance_id
    instance_id=$(oc get node "$node_name" -o jsonpath='{.spec.providerID}' 2>/dev/null | sed 's|^aws:///[^/]*/||')
    
    if [[ -n "$instance_id" ]]; then
        echo "$instance_id"
        return 0
    fi
    
    return 1
}

# Execute emergency reboot using AWS EC2 API
execute_reboot() {
    local node_name="$1"
    
    if [[ "$DRY_RUN" == "true" ]]; then
        log_info "DRY RUN: Would execute emergency reboot on node '$node_name'"
        log_info "DRY RUN: Would use AWS EC2 API to reboot instance"
        echo "  Command: aws ec2 reboot-instances --instance-ids <instance-id>"
        log_info "DRY RUN: Would monitor node reboot status after command execution"
        return 0
    fi

    log_warn "Executing EMERGENCY REBOOT on node '$node_name'..."
    
    # Get AWS instance ID from node
    local instance_id
    instance_id=$(get_aws_instance_id "$node_name")
    
    if [[ -z "$instance_id" ]]; then
        log_error "Could not determine AWS instance ID for node '$node_name'"
        return 1
    fi
    
    log_info "Found AWS instance ID: $instance_id"
    
    # Check if AWS CLI is available
    if ! command -v aws &> /dev/null; then
        log_error "AWS CLI not available, cannot reboot node"
        return 1
    fi
    
    # Execute AWS reboot command
    log_info "Executing AWS EC2 reboot command..."
    if AWS_PAGER="" aws ec2 reboot-instances --instance-ids "$instance_id"; then
        log_success "Emergency reboot initiated via AWS EC2 API"
        
        # Monitor node reboot
        monitor_node_reboot "$node_name"
        return 0
    else
        log_error "AWS EC2 reboot failed for instance $instance_id"
        return 1
    fi
}

# Monitor node reboot to verify it actually happens
monitor_node_reboot() {
    local node_name="$1"
    local max_wait_offline=120  # Maximum time to wait for node to go offline (2 minutes)
    local max_wait_online=300   # Maximum time to wait for node to come back online (5 minutes)
    local check_interval=5      # Check every 5 seconds
    
    log_info "Monitoring node reboot process..."
    log_info "This may take several minutes as we wait for the node to restart"
    
    # Get initial node status and timestamp
    local initial_ready_time
    initial_ready_time=$(oc get node "$node_name" -o jsonpath='{.status.conditions[?(@.type=="Ready")].lastTransitionTime}' 2>/dev/null || echo "unknown")
    log_info "Initial node Ready transition time: $initial_ready_time"
    
    # Wait a moment for the reboot command to take effect
    log_info "Waiting 10 seconds for reboot command to take effect..."
    sleep 10
    
    # Phase 1: Wait for node to become NotReady (indicating reboot started)
    log_info "Phase 1: Waiting for node to become NotReady (max ${max_wait_offline}s)..."
    local wait_time=0
    local node_offline=false
    
    while [[ $wait_time -lt $max_wait_offline ]]; do
        local current_status
        current_status=$(oc get node "$node_name" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || echo "Unknown")
        
        if [[ "$current_status" == "False" ]] || [[ "$current_status" == "Unknown" ]]; then
            log_success "Node is now NotReady - reboot appears to have started"
            node_offline=true
            break
        fi
        
        log_info "Node still Ready, waiting... (${wait_time}s/${max_wait_offline}s)"
        sleep $check_interval
        wait_time=$((wait_time + check_interval))
    done
    
    if [[ "$node_offline" != "true" ]]; then
        log_warn "Node did not become NotReady within ${max_wait_offline} seconds"
        log_warn "The reboot command may have failed or the node is not responding as expected"
        log_info "Current node status:"
        oc get node "$node_name" -o wide || log_error "Failed to get node status"
        return 1
    fi
    
    # Phase 2: Wait for node to come back online and become Ready
    log_info "Phase 2: Waiting for node to come back online and become Ready (max ${max_wait_online}s)..."
    wait_time=0
    local node_ready=false
    
    while [[ $wait_time -lt $max_wait_online ]]; do
        local current_status
        current_status=$(oc get node "$node_name" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || echo "Unknown")
        
        if [[ "$current_status" == "True" ]]; then
            # Get the new Ready transition time to verify it changed
            local new_ready_time
            new_ready_time=$(oc get node "$node_name" -o jsonpath='{.status.conditions[?(@.type=="Ready")].lastTransitionTime}' 2>/dev/null || echo "unknown")
            
            if [[ "$new_ready_time" != "$initial_ready_time" ]]; then
                log_success "Node is back online and Ready!"
                log_info "New Ready transition time: $new_ready_time"
                node_ready=true
                break
            else
                log_info "Node reports Ready but timestamp unchanged, waiting for actual restart..."
            fi
        fi
        
        log_info "Node not ready yet, waiting... (${wait_time}s/${max_wait_online}s)"
        sleep $check_interval
        wait_time=$((wait_time + check_interval))
    done
    
    if [[ "$node_ready" != "true" ]]; then
        log_error "Node did not come back online within ${max_wait_online} seconds"
        log_error "The node may have failed to reboot properly or there may be cluster issues"
        log_info "Final node status:"
        oc get node "$node_name" -o wide || log_error "Failed to get node status"
        return 1
    fi
    
    # Phase 3: Verify reboot by checking additional indicators
    log_info "Phase 3: Verifying successful reboot..."
    
    # Check if any pods were rescheduled (indirect indication of reboot)
    local rescheduled_pods
    rescheduled_pods=$(oc get pods --all-namespaces --field-selector spec.nodeName="$node_name" -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.startTime}{"\n"}{end}' | wc -l)
    log_info "Found $rescheduled_pods pods currently running on the rebooted node"
    
    # Show final node status
    log_info "Final node status after reboot:"
    oc get node "$node_name" -o wide
    
    log_success "Node reboot verification completed successfully!"
    log_info "The emergency reboot appears to have worked as expected"
    
    return 0
}

# Cleanup function
cleanup() {
    local exit_code=$?
    if [[ $exit_code -ne 0 ]]; then
        log_error "Script failed with exit code $exit_code"
    fi
    exit $exit_code
}

# Main function
main() {
    local node_name=""
    
    # Parse command line arguments
    while [[ $# -gt 0 ]]; do
        case $1 in
            -f|--force)
                FORCE_REBOOT=true
                shift
                ;;
            -d|--dry-run)
                DRY_RUN=true
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
            -v|--version)
                version
                exit 0
                ;;
            -*)
                log_error "Unknown option: $1"
                usage
                exit 1
                ;;
            *)
                if [[ -z "$node_name" ]]; then
                    node_name="$1"
                else
                    log_error "Multiple node names specified: '$node_name' and '$1'"
                    usage
                    exit 1
                fi
                shift
                ;;
        esac
    done

    # Validate arguments
    if [[ -z "$node_name" ]]; then
        log_error "Node name is required"
        usage
        exit 1
    fi

    # Validate timeout
    if ! [[ "$TIMEOUT_SECONDS" =~ ^[0-9]+$ ]] || [[ "$TIMEOUT_SECONDS" -lt 5 ]]; then
        log_error "Invalid timeout value: $TIMEOUT_SECONDS (must be a number >= 5)"
        exit 1
    fi

    # Set up cleanup trap
    trap cleanup EXIT

    # Show script info
    log_info "Starting $SCRIPT_NAME v$SCRIPT_VERSION"
    log_info "Target node: $node_name"
    log_info "Timeout: ${TIMEOUT_SECONDS}s"
    log_info "Force mode: $FORCE_REBOOT"
    log_info "Dry run: $DRY_RUN"
    echo

    # Execute main workflow
    check_prerequisites
    validate_node "$node_name"
    confirm_reboot "$node_name"
    execute_reboot "$node_name"

    if [[ "$DRY_RUN" != "true" ]]; then
        echo
        log_success "Emergency reboot process completed with verification"
        log_info "Node reboot monitoring finished successfully"
        log_info "Additional monitoring: oc get node $node_name -w"
        log_info "Check node events: oc describe node $node_name"
    fi
}

# Run main function
main "$@" 