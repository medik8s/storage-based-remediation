#!/bin/bash
set -euo pipefail

# Script to get SBD agent logs for a specific OpenShift node
# Usage: ./get-agent-logs.sh <node-name> [options]

SCRIPT_NAME="$(basename "$0")"
NAMESPACE=""
FOLLOW_LOGS=false
TAIL_LINES=""
PREVIOUS_LOGS=false
SHOW_TIMESTAMPS=true
CONTAINER_NAME="sbd-agent"

usage() {
    cat << EOF
Usage: $SCRIPT_NAME <node-name> [options]

Description:
    Display SBD agent logs for a specific OpenShift node.
    Finds the SBD agent pod running on the specified node and shows its logs.

Arguments:
    <node-name>     Name of the OpenShift node to get agent logs from

Options:
    -n, --namespace <namespace>    Namespace where SBD agents are deployed (default: auto-detect)
    -f, --follow                   Follow log output (like tail -f)
    -p, --previous                 Show logs from previous container instance
    --tail <lines>                 Show last N lines of logs (default: all)
    --no-timestamps               Hide timestamps from log output
    -c, --container <name>         Container name (default: sbd-agent)
    -h, --help                     Show this help message

Examples:
    # Get logs from worker-1 node
    $SCRIPT_NAME worker-1

    # Follow logs from master-1 with last 100 lines
    $SCRIPT_NAME master-1 --follow --tail 100

    # Get logs from specific namespace
    $SCRIPT_NAME ip-10-0-1-23.ec2.internal -n sbd-system

    # Get previous container logs (useful after pod restart)
    $SCRIPT_NAME worker-2 --previous

Environment Variables:
    KUBECONFIG     Path to kubeconfig file (if not using default)
    SBD_NAMESPACE  Default namespace for SBD agents

Dependencies:
    - oc or kubectl command line tool
    - Access to OpenShift cluster
    - Read permissions for pods and logs in SBD namespace

EOF
}

log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*" >&2
}

error() {
    log "ERROR: $*" >&2
    exit 1
}

warn() {
    log "WARNING: $*" >&2
}

check_dependencies() {
    # Check for oc or kubectl
    if command -v oc >/dev/null 2>&1; then
        KUBECTL_CMD="oc"
    elif command -v kubectl >/dev/null 2>&1; then
        KUBECTL_CMD="kubectl"
    else
        error "Neither 'oc' nor 'kubectl' command found. Please install OpenShift CLI or kubectl."
    fi

    # Verify cluster connectivity
    if ! $KUBECTL_CMD cluster-info >/dev/null 2>&1; then
        error "Cannot connect to cluster. Check your kubeconfig and cluster connectivity."
    fi
}

detect_namespace() {
    if [[ -n "${SBD_NAMESPACE:-}" ]]; then
        NAMESPACE="$SBD_NAMESPACE"
        log "Using namespace from SBD_NAMESPACE: $NAMESPACE"
        return
    fi

    # Look for common SBD operator namespaces
    local common_namespaces=("sbd-system" "sbd-operator-system" "openshift-sbd")
    
    for ns in "${common_namespaces[@]}"; do
        if $KUBECTL_CMD get namespace "$ns" >/dev/null 2>&1; then
            # Check if there are SBD agent pods in this namespace
            local agent_count
            agent_count=$($KUBECTL_CMD get pods -n "$ns" -l app=sbd-agent --no-headers 2>/dev/null | wc -l)
            if [[ $agent_count -gt 0 ]]; then
                NAMESPACE="$ns"
                log "Auto-detected SBD namespace: $NAMESPACE"
                return
            fi
        fi
    done

    # If auto-detection fails, try all namespaces
    log "Searching all namespaces for SBD agent pods..."
    local all_namespaces
    all_namespaces=$($KUBECTL_CMD get pods --all-namespaces -l app=sbd-agent --no-headers 2>/dev/null | awk '{print $1}' | sort -u)
    
    if [[ -n "$all_namespaces" ]]; then
        local ns_count
        ns_count=$(echo "$all_namespaces" | wc -l)
        if [[ $ns_count -eq 1 ]]; then
            NAMESPACE="$all_namespaces"
            log "Found SBD agents in namespace: $NAMESPACE"
        else
            error "Multiple namespaces with SBD agents found: $(echo "$all_namespaces" | tr '\n' ' '). Please specify with -n option."
        fi
    else
        error "No SBD agent pods found in any namespace. Are SBD agents deployed?"
    fi
}

find_agent_pod() {
    local node_name="$1"
    
    # Verify the node exists
    if ! $KUBECTL_CMD get node "$node_name" >/dev/null 2>&1; then
        error "Node '$node_name' not found in cluster"
    fi

    # Find SBD agent pod on the specified node
    local pod_info
    pod_info=$($KUBECTL_CMD get pods -n "$NAMESPACE" -l app=sbd-agent \
        --field-selector spec.nodeName="$node_name" \
        --no-headers -o custom-columns=NAME:.metadata.name,STATUS:.status.phase 2>/dev/null)

    if [[ -z "$pod_info" ]]; then
        error "No SBD agent pod found on node '$node_name' in namespace '$NAMESPACE'"
    fi

    local pod_name pod_status
    pod_name=$(echo "$pod_info" | awk '{print $1}')
    pod_status=$(echo "$pod_info" | awk '{print $2}')

    if [[ -z "$pod_name" ]]; then
        error "Failed to parse pod information for node '$node_name'"
    fi

    # Check if multiple pods found (shouldn't happen with DaemonSet, but let's be safe)
    local pod_count
    pod_count=$(echo "$pod_info" | wc -l)
    if [[ $pod_count -gt 1 ]]; then
        warn "Multiple SBD agent pods found on node '$node_name'. Using first one: $pod_name"
        pod_name=$(echo "$pod_info" | head -1 | awk '{print $1}')
        pod_status=$(echo "$pod_info" | head -1 | awk '{print $2}')
    fi

    log "Found SBD agent pod: $pod_name (status: $pod_status) on node: $node_name"
    
    # Warn if pod is not running (but still try to get logs)
    if [[ "$pod_status" != "Running" ]]; then
        warn "Pod is not in Running state: $pod_status"
    fi

    echo "$pod_name"
}

get_logs() {
    local pod_name="$1"
    local node_name="$2"
    
    # Build kubectl logs command
    local log_cmd=("$KUBECTL_CMD" "logs" "-n" "$NAMESPACE" "$pod_name" "-c" "$CONTAINER_NAME")
    
    if [[ "$FOLLOW_LOGS" == "true" ]]; then
        log_cmd+=("-f")
    fi
    
    if [[ "$PREVIOUS_LOGS" == "true" ]]; then
        log_cmd+=("--previous")
    fi
    
    if [[ -n "$TAIL_LINES" ]]; then
        log_cmd+=("--tail=$TAIL_LINES")
    fi
    
    if [[ "$SHOW_TIMESTAMPS" == "true" ]]; then
        log_cmd+=("--timestamps")
    fi

    log "Retrieving logs from SBD agent pod '$pod_name' on node '$node_name'..."
    log "Command: ${log_cmd[*]}"
    log "=============================================="
    
    # Execute the logs command
    "${log_cmd[@]}" || {
        local exit_code=$?
        if [[ $exit_code -eq 1 ]] && [[ "$PREVIOUS_LOGS" == "true" ]]; then
            error "Previous logs not available for pod '$pod_name'. Pod may not have restarted."
        else
            error "Failed to retrieve logs from pod '$pod_name' (exit code: $exit_code)"
        fi
    }
}

main() {
    # Parse arguments
    if [[ $# -eq 0 ]]; then
        usage
        exit 1
    fi

    local node_name=""
    
    while [[ $# -gt 0 ]]; do
        case $1 in
            -h|--help)
                usage
                exit 0
                ;;
            -n|--namespace)
                NAMESPACE="$2"
                shift 2
                ;;
            -f|--follow)
                FOLLOW_LOGS=true
                shift
                ;;
            -p|--previous)
                PREVIOUS_LOGS=true
                shift
                ;;
            --tail)
                TAIL_LINES="$2"
                shift 2
                ;;
            --no-timestamps)
                SHOW_TIMESTAMPS=false
                shift
                ;;
            -c|--container)
                CONTAINER_NAME="$2"
                shift 2
                ;;
            -*)
                error "Unknown option: $1"
                ;;
            *)
                if [[ -z "$node_name" ]]; then
                    node_name="$1"
                else
                    error "Multiple node names provided. Only one node name is allowed."
                fi
                shift
                ;;
        esac
    done

    if [[ -z "$node_name" ]]; then
        error "Node name is required. Use $SCRIPT_NAME --help for usage information."
    fi

    # Validate tail lines if provided
    if [[ -n "$TAIL_LINES" ]] && ! [[ "$TAIL_LINES" =~ ^[0-9]+$ ]]; then
        error "Invalid --tail value: '$TAIL_LINES'. Must be a positive number."
    fi

    log "SBD Agent Log Retrieval Tool"
    log "Node: $node_name"
    
    check_dependencies
    
    if [[ -z "$NAMESPACE" ]]; then
        detect_namespace
    else
        log "Using specified namespace: $NAMESPACE"
    fi

    local pod_name
    pod_name=$(find_agent_pod "$node_name")
    
    get_logs "$pod_name" "$node_name"
    
    log "Log retrieval completed."
}

# Only run main if script is executed directly
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    main "$@"
fi 