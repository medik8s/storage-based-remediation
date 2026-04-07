#!/bin/bash
set -euo pipefail

# Script to list all SBR agent pods and their status across OpenShift nodes
# Usage: ./list-agent-pods.sh [options]

SCRIPT_NAME="$(basename "$0")"
NAMESPACE=""
SHOW_DETAILS=false
OUTPUT_FORMAT="table"

usage() {
    cat << EOF
Usage: $SCRIPT_NAME [options]

Description:
    List all SBR agent pods and their status across OpenShift nodes.
    Useful for getting an overview before using get-agent-logs.sh on specific nodes.

Options:
    -n, --namespace <namespace>    Namespace where SBR agents are deployed (default: auto-detect)
    -d, --details                  Show detailed information (ready/total containers, restarts, age)
    -o, --output <format>          Output format: table, wide, json, yaml (default: table)
    -h, --help                     Show this help message

Examples:
    # List all SBR agent pods
    $SCRIPT_NAME

    # Show detailed information
    $SCRIPT_NAME --details

    # List from specific namespace
    $SCRIPT_NAME -n sbr-system

    # Get output in JSON format
    $SCRIPT_NAME --output json

Environment Variables:
    KUBECONFIG     Path to kubeconfig file (if not using default)
    SBR_NAMESPACE  Default namespace for SBR agents

Dependencies:
    - oc or kubectl command line tool
    - Access to OpenShift cluster
    - Read permissions for pods in SBR namespace

EOF
}

log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*" >&2
}

error() {
    log "ERROR: $*" >&2
    exit 1
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
    if [[ -n "${SBR_NAMESPACE:-}" ]]; then
        NAMESPACE="$SBR_NAMESPACE"
        log "Using namespace from SBR_NAMESPACE: $NAMESPACE"
        return
    fi

    # Look for common SBR operator namespaces
    local common_namespaces=("sbr-system" "sbr-operator-system" "openshift-sbr")
    
    for ns in "${common_namespaces[@]}"; do
        if $KUBECTL_CMD get namespace "$ns" >/dev/null 2>&1; then
            # Check if there are SBR agent pods in this namespace
            local agent_count
            agent_count=$($KUBECTL_CMD get pods -n "$ns" -l app=sbr-agent --no-headers 2>/dev/null | wc -l)
            if [[ $agent_count -gt 0 ]]; then
                NAMESPACE="$ns"
                log "Auto-detected SBR namespace: $NAMESPACE"
                return
            fi
        fi
    done

    # If auto-detection fails, try all namespaces
    log "Searching all namespaces for SBR agent pods..."
    local all_namespaces
    all_namespaces=$($KUBECTL_CMD get pods --all-namespaces -l app=sbr-agent --no-headers 2>/dev/null | awk '{print $1}' | sort -u)
    
    if [[ -n "$all_namespaces" ]]; then
        local ns_count
        ns_count=$(echo "$all_namespaces" | wc -l)
        if [[ $ns_count -eq 1 ]]; then
            NAMESPACE="$all_namespaces"
            log "Found SBR agents in namespace: $NAMESPACE"
        else
            error "Multiple namespaces with SBR agents found: $(echo "$all_namespaces" | tr '\n' ' '). Please specify with -n option."
        fi
    else
        error "No SBR agent pods found in any namespace. Are SBR agents deployed?"
    fi
}

list_agent_pods() {
    local pods_exist
    pods_exist=$($KUBECTL_CMD get pods -n "$NAMESPACE" -l app=sbr-agent --no-headers 2>/dev/null | wc -l)
    
    if [[ $pods_exist -eq 0 ]]; then
        log "No SBR agent pods found in namespace '$NAMESPACE'"
        return
    fi

    log "SBR Agent Pods in namespace: $NAMESPACE"
    log "=============================================="

    case "$OUTPUT_FORMAT" in
        "json")
            $KUBECTL_CMD get pods -n "$NAMESPACE" -l app=sbr-agent -o json
            ;;
        "yaml")
            $KUBECTL_CMD get pods -n "$NAMESPACE" -l app=sbr-agent -o yaml
            ;;
        "wide")
            if [[ "$SHOW_DETAILS" == "true" ]]; then
                $KUBECTL_CMD get pods -n "$NAMESPACE" -l app=sbr-agent -o wide \
                    --show-labels
            else
                $KUBECTL_CMD get pods -n "$NAMESPACE" -l app=sbr-agent -o wide
            fi
            ;;
        "table")
            if [[ "$SHOW_DETAILS" == "true" ]]; then
                $KUBECTL_CMD get pods -n "$NAMESPACE" -l app=sbr-agent \
                    -o custom-columns="NAME:.metadata.name,NODE:.spec.nodeName,STATUS:.status.phase,READY:.status.containerStatuses[0].ready,RESTARTS:.status.containerStatuses[0].restartCount,AGE:.metadata.creationTimestamp"
            else
                echo ""
                printf "%-40s %-30s %-15s\n" "POD NAME" "NODE NAME" "STATUS"
                printf "%-40s %-30s %-15s\n" "----------------------------------------" "------------------------------" "---------------"
                $KUBECTL_CMD get pods -n "$NAMESPACE" -l app=sbr-agent --no-headers \
                    -o custom-columns="NAME:.metadata.name,NODE:.spec.nodeName,STATUS:.status.phase" | \
                while IFS=$'\t' read -r name node status; do
                    printf "%-40s %-30s %-15s\n" "$name" "$node" "$status"
                done
            fi
            ;;
        *)
            error "Invalid output format: $OUTPUT_FORMAT"
            ;;
    esac

    # Show summary
    if [[ "$OUTPUT_FORMAT" == "table" ]]; then
        echo ""
        local total_pods running_pods pending_pods failed_pods
        total_pods=$($KUBECTL_CMD get pods -n "$NAMESPACE" -l app=sbr-agent --no-headers | wc -l)
        running_pods=$($KUBECTL_CMD get pods -n "$NAMESPACE" -l app=sbr-agent --no-headers | grep -c "Running" || true)
        pending_pods=$($KUBECTL_CMD get pods -n "$NAMESPACE" -l app=sbr-agent --no-headers | grep -c "Pending" || true)
        failed_pods=$($KUBECTL_CMD get pods -n "$NAMESPACE" -l app=sbr-agent --no-headers | grep -E "Failed|Error|CrashLoopBackOff" | wc -l || true)

        log "Summary: $total_pods total pods, $running_pods running, $pending_pods pending, $failed_pods failed"
        
        # Show nodes without agents
        local all_nodes worker_nodes nodes_with_agents nodes_without_agents
        all_nodes=$($KUBECTL_CMD get nodes --no-headers -o custom-columns=NAME:.metadata.name | sort)
        worker_nodes=$($KUBECTL_CMD get nodes --no-headers -l node-role.kubernetes.io/worker= -o custom-columns=NAME:.metadata.name 2>/dev/null | sort || echo "")
        nodes_with_agents=$($KUBECTL_CMD get pods -n "$NAMESPACE" -l app=sbr-agent --no-headers -o custom-columns=NODE:.spec.nodeName | sort -u)
        
        if [[ -n "$all_nodes" ]]; then
            nodes_without_agents=$(comm -23 <(echo "$all_nodes") <(echo "$nodes_with_agents") | tr '\n' ' ')
            if [[ -n "$nodes_without_agents" && "$nodes_without_agents" != " " ]]; then
                log "Nodes without SBR agents: $nodes_without_agents"
            fi
        fi
    fi
}

main() {
    # Parse arguments
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
            -d|--details)
                SHOW_DETAILS=true
                shift
                ;;
            -o|--output)
                OUTPUT_FORMAT="$2"
                shift 2
                ;;
            -*)
                error "Unknown option: $1"
                ;;
            *)
                error "Unexpected argument: $1"
                ;;
        esac
    done

    # Validate output format
    case "$OUTPUT_FORMAT" in
        "table"|"wide"|"json"|"yaml")
            ;;
        *)
            error "Invalid output format: $OUTPUT_FORMAT. Valid options: table, wide, json, yaml"
            ;;
    esac

    log "SBR Agent Pod Listing Tool"
    
    check_dependencies
    
    if [[ -z "$NAMESPACE" ]]; then
        detect_namespace
    else
        log "Using specified namespace: $NAMESPACE"
    fi

    list_agent_pods
}

# Only run main if script is executed directly
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    main "$@"
fi 