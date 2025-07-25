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
  # Interactive reboot with confirmation
  $SCRIPT_NAME worker-node-1

  # Force reboot without confirmation (dangerous!)
  $SCRIPT_NAME --force worker-node-1

  # Dry run to see what would happen
  $SCRIPT_NAME --dry-run worker-node-1

  # Reboot with custom timeout
  $SCRIPT_NAME --timeout 60 worker-node-1

SAFETY NOTES:
  - This script uses 'systemctl reboot --force --force' for immediate reboot
  - No graceful shutdown of applications occurs
  - Use only when normal node remediation has failed
  - Ensure you have cluster access before running
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

# Execute emergency reboot
execute_reboot() {
    local node_name="$1"
    
    if [[ "$DRY_RUN" == "true" ]]; then
        log_info "DRY RUN: Would execute emergency reboot on node '$node_name'"
        log_info "DRY RUN: Command that would be executed:"
        echo "  oc debug node/$node_name -- chroot /host systemctl reboot --force --force"
        return 0
    fi

    log_warn "Executing EMERGENCY REBOOT on node '$node_name'..."
    
    # Create debug session and execute immediate reboot
    # Using --force --force for systemctl reboot bypasses all normal shutdown procedures
    local reboot_command="chroot /host bash -c 'echo \"Emergency reboot initiated by SBD operator at \$(date)\" | logger -t sbd-emergency-reboot; systemctl reboot --force --force'"
    
    log_info "Creating debug session on node '$node_name'..."
    
    # Execute the reboot command with timeout
    local exit_code=0
    timeout "$TIMEOUT_SECONDS" oc debug "node/$node_name" --image=registry.redhat.io/ubi8/ubi:latest -- bash -c "$reboot_command" || exit_code=$?
    
    # Note: We expect this command to fail/timeout because the node will reboot
    # Exit codes 124 (timeout) or 130 (SIGINT) are expected when the node reboots during execution
    case $exit_code in
        0)
            log_success "Reboot command executed successfully"
            ;;
        124)
            log_info "Command timed out (expected - node likely rebooting)"
            ;;
        130)
            log_info "Command interrupted (expected - node likely rebooting)"
            ;;
        *)
            log_warn "Command exited with code $exit_code (may be expected during reboot)"
            ;;
    esac

    log_success "Emergency reboot initiated on node '$node_name'"
    log_info "The node should be rebooting now. Monitor cluster status with 'oc get nodes'"
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
        log_success "Emergency reboot process completed"
        log_info "Monitor the node status with: oc get node $node_name -w"
        log_info "Check node events with: oc describe node $node_name"
    fi
}

# Run main function
main "$@" 