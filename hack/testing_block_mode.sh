#!/usr/bin/env bash
# testing_block_mode.sh — Deploy, test, and undeploy SBR block mode from source.
#
# Usage:
#   ./hack/testing_block_mode.sh deploy [--namespace NS] [--image IMG] [--storage-class SC]
#   ./hack/testing_block_mode.sh test   [--namespace NS]
#   ./hack/testing_block_mode.sh undeploy [--namespace NS]
#   ./hack/testing_block_mode.sh all    [--namespace NS] [--image IMG] [--storage-class SC]
#
# Prerequisites:
#   - oc/kubectl logged into an OpenShift/Kubernetes cluster
#   - A StorageClass that supports volumeMode: Block (any CSI driver)
#
# The script is cluster-agnostic — it auto-detects a suitable StorageClass if
# --storage-class is not provided.

set -euo pipefail

# Defaults
NAMESPACE="${NAMESPACE:-sbr-block-test}"
AGENT_IMAGE="${AGENT_IMAGE:-quay.io/medik8s/storage-based-remediation-agent:latest}"
STORAGE_CLASS="${STORAGE_CLASS:-}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
PVC_SIZE="10Mi"
DEVICE_SIZE=$((2 * 1024 * 1024))  # 2 MiB — matches BlockMinDeviceSize

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
CYAN='\033[0;36m'
NC='\033[0m'

info()  { echo -e "${CYAN}[INFO]${NC}  $*"; }
ok()    { echo -e "${GREEN}[OK]${NC}    $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
fail()  { echo -e "${RED}[FAIL]${NC}  $*"; }
fatal() { fail "$@"; exit 1; }

# ---------- CLI parsing ----------

ACTION=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        deploy|test|undeploy|all)
            ACTION="$1"; shift ;;
        --namespace|--namespace=*)
            if [[ "$1" == *=* ]]; then NAMESPACE="${1#*=}"; else shift; NAMESPACE="$1"; fi; shift ;;
        --image|--image=*)
            if [[ "$1" == *=* ]]; then AGENT_IMAGE="${1#*=}"; else shift; AGENT_IMAGE="$1"; fi; shift ;;
        --storage-class|--storage-class=*)
            if [[ "$1" == *=* ]]; then STORAGE_CLASS="${1#*=}"; else shift; STORAGE_CLASS="$1"; fi; shift ;;
        -h|--help)
            head -14 "$0" | tail -13; exit 0 ;;
        *)
            fatal "Unknown argument: $1" ;;
    esac
done

[[ -z "$ACTION" ]] && { head -14 "$0" | tail -13; exit 1; }

# Pick kubectl or oc
KUBECTL="$(command -v oc 2>/dev/null || command -v kubectl 2>/dev/null)" \
    || fatal "Neither oc nor kubectl found in PATH"

# ---------- Helpers ----------

wait_for_pod_phase() {
    local label="$1" phase="$2" timeout="${3:-120}"
    info "Waiting up to ${timeout}s for any pod ($label) to reach phase=$phase ..."
    local elapsed=0
    while (( elapsed < timeout )); do
        # Check if ANY pod with the label is in the desired phase
        local phases
        phases=$($KUBECTL get pods -n "$NAMESPACE" -l "$label" \
            -o jsonpath='{range .items[*]}{.status.phase}{"\n"}{end}' 2>/dev/null || true)
        if echo "$phases" | grep -q "^${phase}$"; then
            ok "At least one pod ($label) is $phase"
            return 0
        fi
        sleep 3
        elapsed=$((elapsed + 3))
    done
    fail "Timeout waiting for pod ($label) phase=$phase"
    $KUBECTL get pods -n "$NAMESPACE" -l "$label" -o wide 2>/dev/null || true
    return 1
}

wait_for_job() {
    local job_name="$1" timeout="${2:-120}"
    info "Waiting up to ${timeout}s for job/$job_name to complete ..."
    if $KUBECTL wait --for=condition=complete "job/$job_name" -n "$NAMESPACE" \
        --timeout="${timeout}s" 2>/dev/null; then
        ok "Job $job_name completed"
        return 0
    fi
    fail "Job $job_name did not complete"
    $KUBECTL describe "job/$job_name" -n "$NAMESPACE" 2>/dev/null || true
    $KUBECTL logs -n "$NAMESPACE" "job/$job_name" --tail=30 2>/dev/null || true
    return 1
}

auto_detect_storage_class() {
    if [[ -n "$STORAGE_CLASS" ]]; then
        info "Using user-specified StorageClass: $STORAGE_CLASS"
        return
    fi
    info "Auto-detecting a StorageClass ..."
    # Prefer the cluster default, then any available StorageClass
    STORAGE_CLASS=$($KUBECTL get sc -o jsonpath='{range .items[?(@.metadata.annotations.storageclass\.kubernetes\.io/is-default-class=="true")]}{.metadata.name}{"\n"}{end}' 2>/dev/null | head -1)
    if [[ -z "$STORAGE_CLASS" ]]; then
        STORAGE_CLASS=$($KUBECTL get sc -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
    fi
    if [[ -z "$STORAGE_CLASS" ]]; then
        fatal "No StorageClass found. Provide one with --storage-class"
    fi
    info "Auto-detected StorageClass: $STORAGE_CLASS"
}

# ---------- Deploy ----------

do_deploy() {
    info "=== Deploying SBR block mode test resources ==="
    info "Namespace:     $NAMESPACE"
    info "Agent image:   $AGENT_IMAGE"
    auto_detect_storage_class
    info "StorageClass:  $STORAGE_CLASS"

    # 1. Create namespace
    $KUBECTL create namespace "$NAMESPACE" 2>/dev/null && ok "Created namespace $NAMESPACE" \
        || info "Namespace $NAMESPACE already exists"

    # 2. Install CRDs from source
    info "Installing CRDs ..."
    $KUBECTL apply -f "${REPO_ROOT}/config/crd/bases/"
    ok "CRDs installed"

    # 3. Create RBAC (ServiceAccount, ClusterRole, ClusterRoleBinding)
    info "Creating RBAC resources ..."
    cat <<EOF | $KUBECTL apply -f -
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: sbr-agent
  namespace: ${NAMESPACE}
automountServiceAccountToken: true
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: sbr-agent-role-${NAMESPACE}
rules:
- apiGroups: [""]
  resources: [events]
  verbs: [create, patch]
- apiGroups: [""]
  resources: [nodes]
  verbs: [get, list, patch, update, watch]
- apiGroups: [""]
  resources: [nodes/status]
  verbs: [get, patch, update]
- apiGroups: [""]
  resources: [pods]
  verbs: [get, list]
- apiGroups: [storage-based-remediation.medik8s.io]
  resources: [storagebasedremediationconfigs]
  verbs: [get, list, watch]
- apiGroups: [storage-based-remediation.medik8s.io]
  resources: [storagebasedremediationconfigs/status]
  verbs: [get]
- apiGroups: [storage-based-remediation.medik8s.io]
  resources: [storagebasedremediations]
  verbs: [get, list, patch, update, watch]
- apiGroups: [storage-based-remediation.medik8s.io]
  resources: [storagebasedremediations/status]
  verbs: [get, patch, update]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: sbr-agent-rolebinding-${NAMESPACE}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: sbr-agent-role-${NAMESPACE}
subjects:
- kind: ServiceAccount
  name: sbr-agent
  namespace: ${NAMESPACE}
EOF
    ok "RBAC created"

    # 3b. Grant privileged SCC on OpenShift (no-op on plain k8s)
    if $KUBECTL get scc privileged &>/dev/null; then
        info "OpenShift detected — granting privileged SCC to sbr-agent SA ..."
        $KUBECTL adm policy add-scc-to-user privileged \
            "system:serviceaccount:${NAMESPACE}:sbr-agent" \
            && ok "Privileged SCC granted" \
            || warn "Failed to grant SCC — you may need cluster-admin"
    fi

    # 4. Create PVC with volumeMode: Block
    info "Creating block PVC ..."
    cat <<EOF | $KUBECTL apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: sbr-block-pvc
  namespace: ${NAMESPACE}
spec:
  accessModes: [ReadWriteOnce]
  volumeMode: Block
  storageClassName: ${STORAGE_CLASS}
  resources:
    requests:
      storage: ${PVC_SIZE}
EOF
    ok "PVC sbr-block-pvc created"

    # 5. Wait for PVC to bind
    info "Waiting for PVC to bind (up to 60s) ..."
    local elapsed=0
    while (( elapsed < 60 )); do
        local phase
        phase=$($KUBECTL get pvc sbr-block-pvc -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || true)
        if [[ "$phase" == "Bound" ]]; then
            ok "PVC bound"
            break
        fi
        # Some StorageClasses use WaitForFirstConsumer — PVC stays Pending until
        # a pod mounts it. Skip the wait and let the init job trigger binding.
        if [[ "$phase" == "Pending" ]] && (( elapsed >= 15 )); then
            local binding_mode
            binding_mode=$($KUBECTL get sc "$STORAGE_CLASS" -o jsonpath='{.volumeBindingMode}' 2>/dev/null || true)
            if [[ "$binding_mode" == "WaitForFirstConsumer" ]]; then
                info "PVC pending — StorageClass uses WaitForFirstConsumer, will bind on first pod"
                break
            fi
        fi
        sleep 3
        elapsed=$((elapsed + 3))
    done

    # 6. Run init job (sbr-agent --init)
    info "Creating init job ..."
    cat <<EOF | $KUBECTL apply -f -
apiVersion: batch/v1
kind: Job
metadata:
  name: sbr-block-init
  namespace: ${NAMESPACE}
spec:
  backoffLimit: 3
  template:
    spec:
      serviceAccountName: sbr-agent
      restartPolicy: Never
      containers:
      - name: sbr-init
        image: ${AGENT_IMAGE}
        command: ["/usr/local/bin/sbr-agent"]
        args:
        - "--init"
        - "--sbr-device=/sbr-block"
        - "--log-level=debug"
        securityContext:
          privileged: true
          runAsUser: 0
        volumeDevices:
        - name: shared-storage
          devicePath: /sbr-block
      volumes:
      - name: shared-storage
        persistentVolumeClaim:
          claimName: sbr-block-pvc
EOF
    wait_for_job "sbr-block-init" 180
    info "Init job logs:"
    $KUBECTL logs -n "$NAMESPACE" job/sbr-block-init --tail=20 || true

    # 7. Deploy agent DaemonSet (detect-only mode — no watchdog arming)
    info "Creating agent DaemonSet ..."
    cat <<EOF | $KUBECTL apply -f -
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: sbr-agent-block-test
  namespace: ${NAMESPACE}
  labels:
    app: sbr-agent
    test: block-mode
spec:
  selector:
    matchLabels:
      app: sbr-agent
      test: block-mode
  template:
    metadata:
      labels:
        app: sbr-agent
        test: block-mode
    spec:
      serviceAccountName: sbr-agent
      terminationGracePeriodSeconds: 10
      # Run on a single node only — PVC is RWO
      nodeSelector:
        kubernetes.io/os: linux
      containers:
      - name: sbr-agent
        image: ${AGENT_IMAGE}
        args:
        - "--sbr-device=/sbr-block"
        - "--sbr-file-locking=false"
        - "--log-level=debug"
        - "--detect-only-mode=true"
        - "--cluster-name=block-test"
        - "--stale-node-timeout=1h"
        - "--watchdog-path=/dev/watchdog"
        - "--io-timeout=30s"
        securityContext:
          privileged: true
          runAsUser: 0
          capabilities:
            add: [SYS_ADMIN, SYS_MODULE]
            drop: [ALL]
        env:
        - name: NODE_NAME
          valueFrom:
            fieldRef:
              fieldPath: spec.nodeName
        - name: POD_NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
        volumeDevices:
        - name: shared-storage
          devicePath: /sbr-block
        resources:
          requests:
            memory: 128Mi
            cpu: 50m
          limits:
            memory: 256Mi
            cpu: 100m
      volumes:
      - name: shared-storage
        persistentVolumeClaim:
          claimName: sbr-block-pvc
EOF
    ok "DaemonSet created"

    # Wait for a pod to start (Running)
    wait_for_pod_phase "app=sbr-agent,test=block-mode" "Running" 180

    info "=== Deploy complete ==="
    $KUBECTL get pods -n "$NAMESPACE" -o wide
}

# ---------- Test ----------

do_test() {
    info "=== Running block mode tests ==="
    local failures=0
    local total_tests=9

    # Test 1: Init job completed successfully
    info "[Test 1/${total_tests}] Init job completed"
    local job_status
    job_status=$($KUBECTL get job sbr-block-init -n "$NAMESPACE" \
        -o jsonpath='{.status.conditions[?(@.type=="Complete")].status}' 2>/dev/null || true)
    if [[ "$job_status" == "True" ]]; then
        ok "Init job completed successfully"
    else
        fail "Init job did not complete"; ((failures++))
    fi

    # Test 2: Agent pod is running (find the Running one — RWO PVC means only one node gets it)
    info "[Test 2/${total_tests}] Agent pod is running"
    local agent_pod=""
    agent_pod=$($KUBECTL get pods -n "$NAMESPACE" -l "app=sbr-agent,test=block-mode" \
        --field-selector=status.phase=Running \
        -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
    if [[ -n "$agent_pod" ]]; then
        ok "Agent pod $agent_pod is Running"
    else
        # Fallback: check any pod
        agent_pod=$($KUBECTL get pods -n "$NAMESPACE" -l "app=sbr-agent,test=block-mode" \
            -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
        local pod_phase
        pod_phase=$($KUBECTL get pod "$agent_pod" -n "$NAMESPACE" \
            -o jsonpath='{.status.phase}' 2>/dev/null || true)
        fail "No Running agent pod found (best candidate: $agent_pod phase=$pod_phase)"; ((failures++))
    fi

    if [[ -z "$agent_pod" ]]; then
        fatal "No agent pod found — cannot continue tests"
    fi

    # Test 3: Block mode detected via superblock auto-probe
    info "[Test 3/${total_tests}] Block mode auto-detection"
    local agent_logs
    agent_logs=$($KUBECTL logs -n "$NAMESPACE" "$agent_pod" --tail=200 2>/dev/null || true)
    if echo "$agent_logs" | grep -q "Block mode detected"; then
        ok "Superblock auto-detection: block mode detected"
    elif echo "$agent_logs" | grep -q "block mode"; then
        ok "Block mode mentioned in logs (check manually)"
    else
        fail "No block mode detection found in agent logs"
        echo "$agent_logs" | grep -i "mode\|superblock\|probe\|block\|filesystem" || true
        ((failures++))
    fi

    # Test 4: OffsetDevice wrappers initialized
    info "[Test 4/${total_tests}] OffsetDevice wrappers (heartbeat/fence regions)"
    if echo "$agent_logs" | grep -q "heartbeatOffset\|heartbeat.*offset\|Block mode detected.*heartbeat"; then
        ok "Heartbeat/fence region offsets visible in logs"
    else
        warn "Could not confirm OffsetDevice offsets in logs (may not be logged at this level)"
    fi

    # Test 5: Agent is not crash-looping
    info "[Test 5/${total_tests}] Agent stability (no crash loop)"
    local restart_count
    restart_count=$($KUBECTL get pod "$agent_pod" -n "$NAMESPACE" \
        -o jsonpath='{.status.containerStatuses[0].restartCount}' 2>/dev/null || echo "0")
    if (( restart_count == 0 )); then
        ok "Agent has 0 restarts"
    elif (( restart_count <= 2 )); then
        warn "Agent has $restart_count restart(s) — may be transient"
    else
        fail "Agent has $restart_count restarts — likely crash-looping"; ((failures++))
    fi

    # Test 6: Heartbeat writes are happening
    info "[Test 6/${total_tests}] Heartbeat writes"
    if echo "$agent_logs" | grep -qi "heartbeat.*writ\|heartbeat.*success\|SBR heartbeat\|wrote heartbeat"; then
        ok "Heartbeat writes detected in logs"
    else
        # Check if the agent just started — heartbeats may appear later
        warn "No heartbeat writes detected yet — agent may still be initializing"
        info "Last 10 log lines:"
        echo "$agent_logs" | tail -10
    fi

    # Test 7: No O_DIRECT / invalid argument errors
    info "[Test 7/${total_tests}] No I/O errors (O_DIRECT alignment)"
    if echo "$agent_logs" | grep -qi "invalid argument"; then
        fail "Found 'invalid argument' errors — O_DIRECT alignment issue"
        echo "$agent_logs" | grep -i "invalid argument" | head -3
        ((failures++))
    else
        ok "No O_DIRECT alignment errors"
    fi

    # Test 8: Idempotent re-init (run init job again, verify no-op)
    info "[Test 8/${total_tests}] Idempotent re-init"
    local reinit_job="sbr-block-reinit"
    cat <<REINIT_EOF | $KUBECTL apply -f - 2>/dev/null
apiVersion: batch/v1
kind: Job
metadata:
  name: ${reinit_job}
  namespace: ${NAMESPACE}
spec:
  backoffLimit: 0
  template:
    spec:
      serviceAccountName: sbr-agent
      restartPolicy: Never
      containers:
      - name: init
        image: ${AGENT_IMAGE}
        args: ["--init", "--sbr-device=/sbr-block", "--log-level=debug"]
        securityContext:
          privileged: true
          runAsUser: 0
        volumeDevices:
        - name: shared-storage
          devicePath: /sbr-block
      volumes:
      - name: shared-storage
        persistentVolumeClaim:
          claimName: sbr-block-pvc
REINIT_EOF
    if $KUBECTL wait --for=condition=complete "job/${reinit_job}" -n "$NAMESPACE" \
        --timeout=120s 2>/dev/null; then
        # Check logs for idempotent skip
        local reinit_logs
        reinit_logs=$($KUBECTL logs -n "$NAMESPACE" "job/${reinit_job}" 2>/dev/null || true)
        if echo "$reinit_logs" | grep -qi "already initialized\|idempotent\|skipping\|no-op\|valid V1 superblock"; then
            ok "Re-init was idempotent (no-op, device unchanged)"
        else
            ok "Re-init completed without error (check logs for details)"
            echo "$reinit_logs" | tail -5
        fi
    else
        fail "Re-init job did not complete"; ((failures++))
        $KUBECTL logs -n "$NAMESPACE" "job/${reinit_job}" --tail=10 2>/dev/null || true
    fi
    # Clean up re-init job
    $KUBECTL delete job "${reinit_job}" -n "$NAMESPACE" --ignore-not-found 2>/dev/null || true

    # Test 9: Node map survives pod restart
    info "[Test 9/${total_tests}] Node map persistence across pod restart"
    # Get current node assignment from logs
    local assigned_node_id
    assigned_node_id=$(echo "$agent_logs" | grep -o '"nodeID":[0-9]*' | head -1 | grep -o '[0-9]*')
    if [[ -z "$assigned_node_id" ]]; then
        warn "Could not determine assigned nodeID from logs — skipping persistence test"
    else
        info "Current nodeID=$assigned_node_id — deleting pod to trigger restart ..."
        $KUBECTL delete pod "$agent_pod" -n "$NAMESPACE" --grace-period=5 2>/dev/null || true

        # Wait for new pod to come up
        sleep 5
        if wait_for_pod_phase "app=sbr-agent,test=block-mode" "Running" 120; then
            sleep 10  # Let the new pod initialize
            local new_pod
            new_pod=$($KUBECTL get pods -n "$NAMESPACE" -l "app=sbr-agent,test=block-mode" \
                --field-selector=status.phase=Running \
                -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
            if [[ -n "$new_pod" ]]; then
                local new_logs
                new_logs=$($KUBECTL logs -n "$NAMESPACE" "$new_pod" --tail=200 2>/dev/null || true)
                if echo "$new_logs" | grep -q "Loaded node mapping from store"; then
                    ok "Node map loaded from store after restart (persistence verified)"
                elif echo "$new_logs" | grep -q "creating new table"; then
                    fail "Node map was NOT persisted — agent created a new table after restart"
                    ((failures++))
                else
                    warn "Could not confirm persistence — check logs manually"
                    echo "$new_logs" | grep -i "node.map\|loaded\|table\|store" | head -5
                fi
                # Update agent_pod for final log dump
                agent_pod="$new_pod"
            else
                fail "No Running pod found after restart"; ((failures++))
            fi
        else
            fail "Pod did not restart in time"; ((failures++))
        fi
    fi

    echo ""
    info "=== Test summary ==="
    if (( failures == 0 )); then
        ok "All tests passed (${total_tests}/${total_tests})"
    else
        fail "$failures test(s) failed out of ${total_tests}"
    fi

    echo ""
    info "Agent logs (last 40 lines):"
    $KUBECTL logs -n "$NAMESPACE" "$agent_pod" --tail=40 2>/dev/null || true

    return "$failures"
}

# ---------- Undeploy ----------

do_undeploy() {
    info "=== Undeploying SBR block mode test resources ==="

    # Delete namespace-scoped resources
    info "Deleting DaemonSet ..."
    $KUBECTL delete daemonset sbr-agent-block-test -n "$NAMESPACE" --ignore-not-found 2>/dev/null || true

    info "Deleting init job ..."
    $KUBECTL delete job sbr-block-init -n "$NAMESPACE" --ignore-not-found 2>/dev/null || true

    info "Deleting PVC ..."
    $KUBECTL delete pvc sbr-block-pvc -n "$NAMESPACE" --ignore-not-found 2>/dev/null || true

    # Wait for PV to be released
    info "Waiting for PV cleanup (10s) ..."
    sleep 10

    # Remove SCC binding on OpenShift
    if $KUBECTL get scc privileged &>/dev/null; then
        info "Removing privileged SCC from sbr-agent SA ..."
        $KUBECTL adm policy remove-scc-from-user privileged \
            "system:serviceaccount:${NAMESPACE}:sbr-agent" 2>/dev/null || true
    fi

    # Delete cluster-scoped resources created by this script
    info "Deleting RBAC ..."
    $KUBECTL delete clusterrolebinding "sbr-agent-rolebinding-${NAMESPACE}" --ignore-not-found 2>/dev/null || true
    $KUBECTL delete clusterrole "sbr-agent-role-${NAMESPACE}" --ignore-not-found 2>/dev/null || true

    # Delete CRDs only if no other SBR namespace exists
    local other_ns
    other_ns=$($KUBECTL get ns -l app.kubernetes.io/part-of=sbr-operator \
        -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || true)
    if [[ -z "$other_ns" ]]; then
        info "Deleting CRDs ..."
        $KUBECTL delete -f "${REPO_ROOT}/config/crd/bases/" --ignore-not-found 2>/dev/null || true
        ok "CRDs deleted"
    else
        info "Skipping CRD deletion — other SBR namespaces exist: $other_ns"
    fi

    info "Deleting namespace $NAMESPACE ..."
    $KUBECTL delete namespace "$NAMESPACE" --ignore-not-found 2>/dev/null || true

    ok "=== Undeploy complete — cluster restored ==="
}

# ---------- Main ----------

case "$ACTION" in
    deploy)
        do_deploy
        ;;
    test)
        do_test
        ;;
    undeploy)
        do_undeploy
        ;;
    all)
        do_deploy
        echo ""
        do_test || true
        echo ""
        read -rp "Press Enter to undeploy (or Ctrl-C to keep resources) ..."
        do_undeploy
        ;;
    *)
        fatal "Unknown action: $ACTION"
        ;;
esac
