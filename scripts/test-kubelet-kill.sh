#!/bin/bash

# test-kubelet-kill.sh
# Direct kubelet termination to test SBR fencing behavior
# This bypasses all network complexity and directly simulates kubelet failure

set -euo pipefail

echo "=== SBR Kubelet Kill Test ==="
echo

# Test node
WORKER_NODE="ip-10-0-38-215.ap-southeast-2.compute.internal"
echo "Testing direct kubelet termination on: $WORKER_NODE"

# Check initial node status
echo
echo "Initial node status:"
kubectl get node "$WORKER_NODE" -o jsonpath='{.status.conditions[?(@.type=="Ready")]}' | jq -r '"Ready: " + .status + " (" + .reason + ") - " + .message'

echo
echo "Creating privileged pod to stop kubelet service..."

# Create a privileged pod that can stop the kubelet service
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: kubelet-killer-$(date +%s)
  namespace: default
spec:
  hostNetwork: true
  hostPID: true
  nodeName: $WORKER_NODE
  containers:
  - name: killer
    image: busybox:latest
    command:
    - /bin/sh
    - -c
    - |
      echo "SBR kubelet kill test starting..."
      echo "Target: Stop kubelet service on host"
      
      echo "Current kubelet status:"
      nsenter --target 1 --mount --uts --ipc --net --pid -- systemctl status kubelet.service --no-pager || echo "Failed to get status"
      
      echo "Stopping kubelet service..."
      nsenter --target 1 --mount --uts --ipc --net --pid -- systemctl stop kubelet.service
      
      echo "Kubelet stopped. Waiting 5 minutes before restarting..."
      echo "Node should become NotReady during this time"
      
      sleep 300  # Wait 5 minutes
      
      echo "Restarting kubelet service..."
      nsenter --target 1 --mount --uts --ipc --net --pid -- systemctl start kubelet.service
      
      echo "Kubelet restarted. Node should recover."
      echo "Test completed - pod will exit"
    securityContext:
      privileged: true
    resources:
      requests:
        memory: "64Mi"
        cpu: "100m"
      limits:
        memory: "128Mi"
        cpu: "200m"
  restartPolicy: Never
  tolerations:
  - operator: Exists
EOF

# Get the pod name
KILLER_POD=$(kubectl get pods -o name | grep kubelet-killer | head -1 | sed 's/pod\///')
echo "Created killer pod: $KILLER_POD"

echo "Waiting for killer pod to start and stop kubelet..."
kubectl wait --for=condition=Ready pod/$KILLER_POD --timeout=60s

# Monitor for initial kubelet stop
echo
echo "Monitoring for kubelet to stop (checking every 15 seconds for 2 minutes)..."
for i in {1..8}; do
    echo "Check $i/8:"
    
    # Check pod logs to see if kubelet has been stopped
    LOGS=$(kubectl logs $KILLER_POD --tail=5 2>/dev/null || echo "No logs yet")
    echo "  Pod logs: $LOGS"
    
    # Check node status
    NODE_STATUS=$(kubectl get node "$WORKER_NODE" -o jsonpath='{.status.conditions[?(@.type=="Ready")]}' 2>/dev/null | jq -r '"Ready: " + .status + " (" + .reason + ") - " + .message' 2>/dev/null || echo "Failed to get node status")
    echo "  Node status: $NODE_STATUS"
    
    if echo "$LOGS" | grep -q "Stopping kubelet service"; then
        echo "  ✓ Kubelet stop command issued"
        break
    fi
    
    sleep 15
done

echo
echo "Monitoring for node to become NotReady (checking every 30 seconds for 10 minutes)..."
for i in {1..20}; do
    echo "Check $i/20:"
    
    NODE_STATUS=$(kubectl get node "$WORKER_NODE" -o jsonpath='{.status.conditions[?(@.type=="Ready")]}' 2>/dev/null | jq -r '"Ready: " + .status + " (" + .reason + ") - " + .message' 2>/dev/null || echo "Failed to get node status")
    echo "  $NODE_STATUS"
    
    if echo "$NODE_STATUS" | grep -q -E "(False|Unknown)"; then
        echo
        echo "SUCCESS: Node is now NotReady due to kubelet termination!"
        break
    fi
    
    # Check if kubelet has been restarted (pod completed)
    POD_STATUS=$(kubectl get pod $KILLER_POD -o jsonpath='{.status.phase}' 2>/dev/null || echo "Unknown")
    if [ "$POD_STATUS" = "Succeeded" ]; then
        echo "  Pod has completed - kubelet should have been restarted"
    fi
    
    if [ $i -eq 20 ]; then
        echo
        echo "WARNING: Node is still Ready after 10 minutes"
        echo "This is unexpected for kubelet termination"
    fi
    
    sleep 30
done

echo
echo "Detailed node status:"
kubectl describe node "$WORKER_NODE" | grep -A 10 -B 5 "Conditions\|Lease\|Heartbeat\|Ready"

echo
echo "Killer pod logs:"
kubectl logs $KILLER_POD

echo
echo "Cleanup - deleting killer pod:"
kubectl delete pod $KILLER_POD --wait=false

echo
echo "=== SBR Kubelet Kill Test Complete ===" 