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
SCRIPT_NAME="show-node-map.sh"
SCRIPT_VERSION="1.0.0"
DEFAULT_SBD_DEVICE="/dev/sbd0"
SBD_DEVICE=""
SHOW_HEARTBEATS=false
JSON_OUTPUT=false
VERBOSE=false
USE_KUBERNETES=false
KUBERNETES_NAMESPACE=""
KUBECTL_CMD=""

# Colors for output
RED='\033[0;31m'
YELLOW='\033[1;33m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
MAGENTA='\033[0;35m'
GRAY='\033[0;37m'
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

log_debug() {
    if [[ "$VERBOSE" == "true" ]]; then
        echo -e "${GRAY}[DEBUG]${NC} $*" >&2
    fi
}

# Usage function
usage() {
    cat <<EOF
Usage: $SCRIPT_NAME [OPTIONS]

Display SBD agent node map showing node-to-slot assignments and heartbeat status.

OPTIONS:
  -d, --device PATH       SBD device path (default: $DEFAULT_SBD_DEVICE)
  -k, --kubernetes        Use Kubernetes to read from SBD agent pods (recommended)
  -n, --namespace NAME    Kubernetes namespace for SBD agents (auto-detected if not specified)
  -H, --heartbeats        Show current heartbeat status from SBD device
  -j, --json              Output in JSON format
  -v, --verbose           Enable verbose output
  -h, --help              Show this help message
  --version               Show script version

EXAMPLES:
  # Show basic node mapping using Kubernetes (recommended)
  $SCRIPT_NAME --kubernetes

  # Show node mapping with heartbeat status via Kubernetes
  $SCRIPT_NAME --kubernetes --heartbeats

  # Use specific namespace
  $SCRIPT_NAME --kubernetes --namespace sbd-system

  # Direct device access (requires local filesystem access)
  $SCRIPT_NAME --device /dev/sbd0

  # JSON output for automation
  $SCRIPT_NAME --kubernetes --json

  # Verbose output with debug information
  $SCRIPT_NAME --kubernetes --verbose --heartbeats

DESCRIPTION:
  This script reads the SBD node mapping file (.nodemap) and displays:
  - Node names and their assigned slot IDs
  - Hash values used for slot assignment
  - Last seen timestamps
  - Cluster information
  - Optional: Current heartbeat status from SBD device slots

  Two access modes are supported:
  1. Kubernetes mode (--kubernetes): Reads from SBD agent pods via kubectl/oc
  2. Direct mode: Reads from local filesystem (requires direct device access)

REQUIREMENTS:
  For Kubernetes mode (recommended):
  - kubectl or oc command available
  - KUBECONFIG configured for target cluster
  - Read permissions for pods in SBD namespace
  
  For direct mode:
  - Read access to SBD device and node mapping file
  - jq command for JSON parsing (automatically checked)
  - Optional: hexdump for binary SBD device analysis

EOF
}

# Version function
version() {
    echo "$SCRIPT_NAME version $SCRIPT_VERSION"
}

# Check if command exists
command_exists() {
    command -v "$1" >/dev/null 2>&1
}

# Check prerequisites
check_prerequisites() {
    log_debug "Checking prerequisites..."

    # Check if jq is available for JSON parsing
    if ! command_exists jq; then
        log_error "jq command not found. Please install jq for JSON parsing."
        exit 1
    fi

    # Check Kubernetes prerequisites if using Kubernetes mode
    if [[ "$USE_KUBERNETES" == "true" ]]; then
        detect_kubectl_command
        check_cluster_connectivity
    else
        # Check if hexdump is available (for heartbeat analysis)
        if [[ "$SHOW_HEARTBEATS" == "true" ]] && ! command_exists hexdump; then
            log_warn "hexdump not available - heartbeat analysis may be limited"
        fi
    fi

    log_debug "Prerequisites check passed"
}

# Detect kubectl or oc command
detect_kubectl_command() {
    log_debug "Detecting Kubernetes CLI command..."
    
    if command_exists oc; then
        KUBECTL_CMD="oc"
        log_debug "Using OpenShift CLI (oc)"
    elif command_exists kubectl; then
        KUBECTL_CMD="kubectl"
        log_debug "Using Kubernetes CLI (kubectl)"
    else
        log_error "Neither 'oc' nor 'kubectl' command found"
        log_info "Please install kubectl or oc to use Kubernetes mode"
        exit 1
    fi
}

# Check cluster connectivity
check_cluster_connectivity() {
    log_debug "Checking cluster connectivity..."
    
    if ! $KUBECTL_CMD cluster-info >/dev/null 2>&1; then
        log_error "Cannot connect to Kubernetes cluster"
        log_info "Please check your KUBECONFIG and cluster connectivity"
        log_info "Test with: $KUBECTL_CMD cluster-info"
        exit 1
    fi
    
    log_debug "Cluster connectivity verified"
}

# Check if SBD device exists and is accessible
check_sbd_device() {
    local device="$1"
    
    log_debug "Checking SBD device: $device"

    if [[ ! -e "$device" ]]; then
        log_error "SBD device does not exist: $device"
        exit 1
    fi

    if [[ ! -r "$device" ]]; then
        log_error "Cannot read SBD device: $device (permission denied)"
        log_info "Try running with sudo or ensure proper permissions"
        exit 1
    fi

    log_debug "SBD device is accessible: $device"
}

# Check if node mapping file exists
check_node_mapping_file() {
    local device="$1"
    local nodemap_file="${device}.nodemap"
    
    log_debug "Checking node mapping file: $nodemap_file"

    if [[ ! -e "$nodemap_file" ]]; then
        log_error "Node mapping file does not exist: $nodemap_file"
        log_info "This could mean:"
        log_info "  - SBD agent has not yet created the mapping"
        log_info "  - Node mapping is stored elsewhere"
        log_info "  - SBD device path is incorrect"
        exit 1
    fi

    if [[ ! -r "$nodemap_file" ]]; then
        log_error "Cannot read node mapping file: $nodemap_file (permission denied)"
        exit 1
    fi

    echo "$nodemap_file"
}

# Auto-detect SBD namespace if not specified
detect_sbd_namespace() {
    log_debug "Auto-detecting SBD namespace..."
    
    # Common namespace patterns for SBD agents
    local common_namespaces=("sbd-system" "sbd" "medik8s" "openshift-sbd" "kube-system" "default")
    
    for ns in "${common_namespaces[@]}"; do
        log_debug "Checking namespace: $ns"
        local pod_count
        pod_count=$($KUBECTL_CMD get pods -n "$ns" -l app=sbd-agent --no-headers 2>/dev/null | wc -l)
        if [[ $pod_count -gt 0 ]]; then
            KUBERNETES_NAMESPACE="$ns"
            log_debug "Found $pod_count SBD agent pods in namespace: $ns"
            return 0
        fi
    done
    
    # Search all namespaces
    log_debug "Searching all namespaces for SBD agent pods..."
    local all_namespaces
    all_namespaces=$($KUBECTL_CMD get pods --all-namespaces -l app=sbd-agent --no-headers 2>/dev/null | awk '{print $1}' | sort -u)
    
    if [[ -n "$all_namespaces" ]]; then
        local ns_count
        ns_count=$(echo "$all_namespaces" | wc -l)
        if [[ $ns_count -eq 1 ]]; then
            KUBERNETES_NAMESPACE="$all_namespaces"
            log_debug "Found SBD agents in namespace: $KUBERNETES_NAMESPACE"
            return 0
        else
            log_error "Multiple namespaces with SBD agents found: $(echo "$all_namespaces" | tr '\n' ' ')"
            log_info "Please specify namespace with --namespace option"
            exit 1
        fi
    else
        log_error "No SBD agent pods found in any namespace"
        log_info "Are SBD agents deployed? Check with: $KUBECTL_CMD get pods -A -l app=sbd-agent"
        exit 1
    fi
}

# Find a running SBD agent pod
find_sbd_agent_pod() {
    log_debug "Finding running SBD agent pod in namespace: $KUBERNETES_NAMESPACE"
    
    # Get running SBD agent pods
    local pod_info
    pod_info=$($KUBECTL_CMD get pods -n "$KUBERNETES_NAMESPACE" -l app=sbd-agent --field-selector=status.phase=Running --no-headers 2>/dev/null)
    
    if [[ -z "$pod_info" ]]; then
        log_error "No running SBD agent pods found in namespace '$KUBERNETES_NAMESPACE'"
        log_info "Check pod status with: $KUBECTL_CMD get pods -n $KUBERNETES_NAMESPACE -l app=sbd-agent"
        exit 1
    fi
    
    # Use the first running pod
    local pod_name
    pod_name=$(echo "$pod_info" | head -1 | awk '{print $1}')
    local pod_status
    pod_status=$(echo "$pod_info" | head -1 | awk '{print $3}')
    
    log_debug "Selected SBD agent pod: $pod_name (status: $pod_status)"
    echo "$pod_name"
}

# Read node mapping from Kubernetes pod
read_node_mapping_from_k8s() {
    local pod_name="$1"
    local device_path="$2"
    local nodemap_file="${device_path}.nodemap"
    
    log_debug "Reading node mapping from pod $pod_name at path: $nodemap_file"
    
    # Check if the node mapping file exists in the pod
    if ! $KUBECTL_CMD exec -n "$KUBERNETES_NAMESPACE" "$pod_name" -- test -f "$nodemap_file" 2>/dev/null; then
        log_error "Node mapping file not found in pod: $nodemap_file"
        log_info "This could mean:"
        log_info "  - SBD agent has not yet created the mapping"
        log_info "  - SBD device path is incorrect: $device_path"
        log_info "  - Volume mount configuration issue"
        exit 1
    fi
    
    # Read the node mapping file from the pod
    local json_data
    if ! json_data=$($KUBECTL_CMD exec -n "$KUBERNETES_NAMESPACE" "$pod_name" -- tail -c +5 "$nodemap_file" 2>/dev/null); then
        log_error "Failed to read node mapping file from pod: $nodemap_file"
        exit 1
    fi
    
    # Validate JSON
    if ! echo "$json_data" | jq . >/dev/null 2>&1; then
        log_error "Invalid JSON data in node mapping file from pod"
        log_debug "Raw data preview: $(echo "$json_data" | head -c 100)..."
        exit 1
    fi
    
    echo "$json_data"
}

# Get heartbeat status from Kubernetes pod
get_heartbeat_status_from_k8s() {
    local pod_name="$1"
    local device_path="$2"
    local slot_id="$3"
    
    # Each slot is 512 bytes (SBD_SLOT_SIZE)
    local slot_size=512
    local offset=$((slot_id * slot_size))
    
    # Read the first 64 bytes of the slot (contains the header)
    local header_data
    if header_data=$($KUBECTL_CMD exec -n "$KUBERNETES_NAMESPACE" "$pod_name" -- dd if="$device_path" bs=64 count=1 skip=$((offset / 64)) 2>/dev/null | hexdump -C 2>/dev/null); then
        # Look for non-zero data which indicates activity
        if echo "$header_data" | grep -q -v "00 00 00 00 00 00 00 00"; then
            # Try to extract timestamp (assuming it's in the first 8 bytes)
            local timestamp_hex
            timestamp_hex=$($KUBECTL_CMD exec -n "$KUBERNETES_NAMESPACE" "$pod_name" -- dd if="$device_path" bs=8 count=1 skip=$((offset / 8)) 2>/dev/null | hexdump -e '1/8 "%016x"' 2>/dev/null || echo "unknown")
            echo "active:$timestamp_hex"
        else
            echo "empty"
        fi
    else
        echo "error"
    fi
}

# Get all heartbeat statuses from Kubernetes pod
get_all_heartbeats_from_k8s() {
    local pod_name="$1"
    local device_path="$2"
    local json_data="$3"
    
    log_debug "Reading heartbeat status from SBD device via pod $pod_name..."

    # Extract slot usage from JSON
    local slot_usage
    slot_usage=$(echo "$json_data" | jq -r '.slot_usage // {}')
    
    local heartbeats="{}"
    
    # Check each slot that has a node assigned
    while IFS= read -r slot_id; do
        if [[ -n "$slot_id" && "$slot_id" != "null" ]]; then
            local status
            status=$(get_heartbeat_status_from_k8s "$pod_name" "$device_path" "$slot_id")
            heartbeats=$(echo "$heartbeats" | jq --arg slot "$slot_id" --arg status "$status" '. + {($slot): $status}')
        fi
    done < <(echo "$slot_usage" | jq -r 'keys[]' 2>/dev/null || true)

    echo "$heartbeats"
}

# Parse node mapping file
parse_node_mapping() {
    local nodemap_file="$1"
    
    log_debug "Parsing node mapping file: $nodemap_file"

    # The node mapping file format is: 4-byte checksum + JSON data
    # Skip the first 4 bytes (checksum) and parse the JSON
    local json_data
    if ! json_data=$(tail -c +5 "$nodemap_file" 2>/dev/null); then
        log_error "Failed to read node mapping file: $nodemap_file"
        exit 1
    fi

    # Validate JSON
    if ! echo "$json_data" | jq . >/dev/null 2>&1; then
        log_error "Invalid JSON data in node mapping file"
        log_debug "Raw data preview: $(echo "$json_data" | head -c 100)..."
        exit 1
    fi

    echo "$json_data"
}

# Get heartbeat status for a specific slot
get_heartbeat_status() {
    local device="$1"
    local slot_id="$2"
    
    # Each slot is 512 bytes (SBD_SLOT_SIZE)
    local slot_size=512
    local offset=$((slot_id * slot_size))
    
    # Read the first 64 bytes of the slot (contains the header)
    local header_data
    if header_data=$(dd if="$device" bs=64 count=1 skip=$((offset / 64)) 2>/dev/null | hexdump -C 2>/dev/null); then
        # Look for non-zero data which indicates activity
        if echo "$header_data" | grep -q -v "00 00 00 00 00 00 00 00"; then
            # Try to extract timestamp (assuming it's in the first 8 bytes)
            local timestamp_hex
            timestamp_hex=$(dd if="$device" bs=8 count=1 skip=$((offset / 8)) 2>/dev/null | hexdump -e '1/8 "%016x"' 2>/dev/null || echo "unknown")
            echo "active:$timestamp_hex"
        else
            echo "empty"
        fi
    else
        echo "error"
    fi
}

# Get all heartbeat statuses
get_all_heartbeats() {
    local device="$1"
    local json_data="$2"
    
    log_debug "Reading heartbeat status from SBD device..."

    # Extract slot usage from JSON
    local slot_usage
    slot_usage=$(echo "$json_data" | jq -r '.slot_usage // {}')
    
    local heartbeats="{}"
    
    # Check each slot that has a node assigned
    while IFS= read -r slot_id; do
        if [[ -n "$slot_id" && "$slot_id" != "null" ]]; then
            local status
            status=$(get_heartbeat_status "$device" "$slot_id")
            heartbeats=$(echo "$heartbeats" | jq --arg slot "$slot_id" --arg status "$status" '. + {($slot): $status}')
        fi
    done < <(echo "$slot_usage" | jq -r 'keys[]' 2>/dev/null || true)

    echo "$heartbeats"
}

# Format timestamp for display
format_timestamp() {
    local iso_timestamp="$1"
    
    if [[ "$iso_timestamp" == "null" || -z "$iso_timestamp" ]]; then
        echo "never"
    else
        # Try to convert ISO timestamp to relative time
        if command_exists date; then
            local epoch
            if epoch=$(date -d "$iso_timestamp" +%s 2>/dev/null); then
                local now
                now=$(date +%s)
                local diff=$((now - epoch))
                
                if [[ $diff -lt 60 ]]; then
                    echo "${diff}s ago"
                elif [[ $diff -lt 3600 ]]; then
                    echo "$((diff / 60))m ago"
                elif [[ $diff -lt 86400 ]]; then
                    echo "$((diff / 3600))h ago"
                else
                    echo "$((diff / 86400))d ago"
                fi
            else
                echo "$iso_timestamp"
            fi
        else
            echo "$iso_timestamp"
        fi
    fi
}

# Display node mapping in human-readable format
display_node_mapping() {
    local json_data="$1"
    local heartbeats="$2"
    
    # Extract basic information
    local cluster_name
    cluster_name=$(echo "$json_data" | jq -r '.cluster_name // "unknown"')
    local version
    version=$(echo "$json_data" | jq -r '.version // "unknown"')
    local last_update
    last_update=$(echo "$json_data" | jq -r '.last_update // "never"')
    local formatted_update
    formatted_update=$(format_timestamp "$last_update")

    # Display header
    echo
    echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
    echo -e "${CYAN}                     SBD Agent Node Mapping${NC}"
    echo -e "${CYAN}═══════════════════════════════════════════════════════════════${NC}"
    echo
    echo -e "${BLUE}Cluster Name:${NC} $cluster_name"
    echo -e "${BLUE}Version:${NC} $version"
    echo -e "${BLUE}Last Update:${NC} $formatted_update"
    
    # Show access mode
    if [[ "$USE_KUBERNETES" == "true" ]]; then
        echo -e "${BLUE}Access Mode:${NC} Kubernetes (namespace: $KUBERNETES_NAMESPACE)"
        echo -e "${BLUE}SBD Device:${NC} $SBD_DEVICE"
    else
        echo -e "${BLUE}Access Mode:${NC} Direct filesystem"
        echo -e "${BLUE}SBD Device:${NC} $SBD_DEVICE"
    fi
    
    # Count nodes
    local node_count
    node_count=$(echo "$json_data" | jq -r '.entries | length')
    echo -e "${BLUE}Total Nodes:${NC} $node_count"
    echo

    # Check if we have any nodes
    if [[ "$node_count" == "0" ]]; then
        echo -e "${YELLOW}No nodes are currently mapped.${NC}"
        return
    fi

    # Display table header
    if [[ "$SHOW_HEARTBEATS" == "true" ]]; then
        printf "%-4s %-30s %-10s %-15s %-12s %s\n" "SLOT" "NODE NAME" "HASH" "LAST SEEN" "HEARTBEAT" "STATUS"
        echo "────────────────────────────────────────────────────────────────────────────────────────"
    else
        printf "%-4s %-30s %-10s %-15s %s\n" "SLOT" "NODE NAME" "HASH" "LAST SEEN" "STATUS"
        echo "─────────────────────────────────────────────────────────────────────────────────"
    fi

    # Extract and sort entries by slot ID
    local sorted_entries
    sorted_entries=$(echo "$json_data" | jq -r '.entries | to_entries | map({slot: .value.slot_id, name: .key, hash: .value.hash, last_seen: .value.last_seen}) | sort_by(.slot)')

    # Display each entry
    while IFS= read -r entry; do
        local slot_id
        slot_id=$(echo "$entry" | jq -r '.slot // "?"')
        local node_name
        node_name=$(echo "$entry" | jq -r '.name // "unknown"')
        local hash
        hash=$(echo "$entry" | jq -r '.hash // "unknown"')
        local last_seen
        last_seen=$(echo "$entry" | jq -r '.last_seen // "never"')
        local formatted_last_seen
        formatted_last_seen=$(format_timestamp "$last_seen")

        # Determine status color
        local status_color="$GREEN"
        local status_text="OK"
        
        # Check if last seen is too old (more than 5 minutes)
        if [[ "$last_seen" != "never" && "$last_seen" != "null" ]]; then
            if command_exists date; then
                local last_epoch
                if last_epoch=$(date -d "$last_seen" +%s 2>/dev/null); then
                    local now
                    now=$(date +%s)
                    local diff=$((now - last_epoch))
                    if [[ $diff -gt 300 ]]; then  # 5 minutes
                        status_color="$YELLOW"
                        status_text="STALE"
                    fi
                    if [[ $diff -gt 1800 ]]; then  # 30 minutes
                        status_color="$RED"
                        status_text="OFFLINE"
                    fi
                fi
            fi
        else
            status_color="$RED"
            status_text="NEVER"
        fi

        # Get heartbeat status if requested
        local heartbeat_status=""
        if [[ "$SHOW_HEARTBEATS" == "true" ]]; then
            local hb_status
            hb_status=$(echo "$heartbeats" | jq -r --arg slot "$slot_id" '.[$slot] // "unknown"')
            case "$hb_status" in
                "empty")
                    heartbeat_status="${GRAY}EMPTY${NC}"
                    ;;
                "error")
                    heartbeat_status="${RED}ERROR${NC}"
                    ;;
                active:*)
                    heartbeat_status="${GREEN}ACTIVE${NC}"
                    ;;
                *)
                    heartbeat_status="${YELLOW}UNKNOWN${NC}"
                    ;;
            esac
        fi

        # Display the row
        if [[ "$SHOW_HEARTBEATS" == "true" ]]; then
            printf "%-4s %-30s %-10s %-15s %-20s %s\n" \
                "$slot_id" \
                "$node_name" \
                "$hash" \
                "$formatted_last_seen" \
                "$heartbeat_status" \
                "${status_color}${status_text}${NC}"
        else
            printf "%-4s %-30s %-10s %-15s %s\n" \
                "$slot_id" \
                "$node_name" \
                "$hash" \
                "$formatted_last_seen" \
                "${status_color}${status_text}${NC}"
        fi
    done < <(echo "$sorted_entries" | jq -c '.[]')

    echo
}

# Display in JSON format
display_json() {
    local json_data="$1"
    local heartbeats="$2"
    
    if [[ "$SHOW_HEARTBEATS" == "true" ]]; then
        # Merge heartbeat data with the main JSON
        echo "$json_data" | jq --argjson heartbeats "$heartbeats" '. + {heartbeats: $heartbeats}'
    else
        echo "$json_data" | jq .
    fi
}

# Main function
main() {
    # Parse command line arguments
    while [[ $# -gt 0 ]]; do
        case $1 in
            -d|--device)
                SBD_DEVICE="$2"
                shift 2
                ;;
            -k|--kubernetes)
                USE_KUBERNETES=true
                shift
                ;;
            -n|--namespace)
                KUBERNETES_NAMESPACE="$2"
                shift 2
                ;;
            -H|--heartbeats)
                SHOW_HEARTBEATS=true
                shift
                ;;
            -j|--json)
                JSON_OUTPUT=true
                shift
                ;;
            -v|--verbose)
                VERBOSE=true
                shift
                ;;
            -h|--help)
                usage
                exit 0
                ;;
            --version)
                version
                exit 0
                ;;
            -*)
                log_error "Unknown option: $1"
                usage
                exit 1
                ;;
            *)
                log_error "Unexpected argument: $1"
                usage
                exit 1
                ;;
        esac
    done

    # Set default SBD device if not specified
    if [[ -z "$SBD_DEVICE" ]]; then
        SBD_DEVICE="$DEFAULT_SBD_DEVICE"
    fi

    # Check prerequisites
    check_prerequisites

    local json_data
    local heartbeats="{}"

    if [[ "$USE_KUBERNETES" == "true" ]]; then
        # Kubernetes mode - read from SBD agent pods
        log_debug "Using Kubernetes mode to read node mapping"
        
        # Auto-detect namespace if not specified
        if [[ -z "$KUBERNETES_NAMESPACE" ]]; then
            detect_sbd_namespace
        fi
        
        # Find a running SBD agent pod
        local pod_name
        pod_name=$(find_sbd_agent_pod)
        
        # Read node mapping from the pod
        log_debug "Reading node mapping from pod $pod_name using device $SBD_DEVICE"
        json_data=$(read_node_mapping_from_k8s "$pod_name" "$SBD_DEVICE")
        
        # Get heartbeat status if requested
        if [[ "$SHOW_HEARTBEATS" == "true" ]]; then
            heartbeats=$(get_all_heartbeats_from_k8s "$pod_name" "$SBD_DEVICE" "$json_data")
        fi
    else
        # Direct mode - read from local filesystem
        log_debug "Using direct mode to read node mapping"
        
        # Check SBD device
        check_sbd_device "$SBD_DEVICE"

        # Check and get node mapping file
        local nodemap_file
        nodemap_file=$(check_node_mapping_file "$SBD_DEVICE")

        # Parse node mapping
        log_debug "Reading node mapping from: $nodemap_file"
        json_data=$(parse_node_mapping "$nodemap_file")

        # Get heartbeat status if requested
        if [[ "$SHOW_HEARTBEATS" == "true" ]]; then
            heartbeats=$(get_all_heartbeats "$SBD_DEVICE" "$json_data")
        fi
    fi

    # Display output
    if [[ "$JSON_OUTPUT" == "true" ]]; then
        display_json "$json_data" "$heartbeats"
    else
        display_node_mapping "$json_data" "$heartbeats"
    fi
}

# Run main function
main "$@" 