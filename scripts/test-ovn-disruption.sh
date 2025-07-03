#!/bin/bash

# test-ovn-disruption.sh
# Host-level network disruption for OpenShift clusters with load balancer API access
# Uses iptables on the host to block kubelet communication to the API server load balancer

set -euo pipefail

echo "=== OpenShift Load Balancer Network Disruption Test ==="
echo

# Test node
WORKER_NODE="ip-10-0-38-215.ap-southeast-2.compute.internal"
echo "Testing host-level network disruption on: $WORKER_NODE"

# Get API server endpoint and resolve load balancer IP
echo
echo "Resolving API server load balancer..."
API_SERVER_URL=$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}')
API_SERVER_HOST=$(echo "$API_SERVER_URL" | sed 's|https://||' | cut -d: -f1)
API_SERVER_PORT=$(echo "$API_SERVER_URL" | sed 's|https://||' | cut -d: -f2)

echo "API Server URL: $API_SERVER_URL"
echo "API Server Host: $API_SERVER_HOST"
echo "API Server Port: $API_SERVER_PORT"

# Resolve load balancer IP
API_SERVER_IP=$(nslookup "$API_SERVER_HOST" | grep "^Address:" | grep -v "#53" | awk '{print $2}')
if [ -z "$API_SERVER_IP" ]; then
    echo "Error: Could not resolve API server load balancer IP"
    exit 1
fi

echo "API Server Load Balancer IP: $API_SERVER_IP"

# Check initial node status
echo
echo "Initial node status:"
kubectl get node "$WORKER_NODE" -o jsonpath='{.status.conditions[?(@.type=="Ready")]}' | jq -r '"Ready: " + .status + " (" + .reason + ") - " + .message'

# Get AWS instance ID for the target node
echo
echo "Getting AWS instance ID..."
PROVIDER_ID=$(kubectl get node "$WORKER_NODE" -o jsonpath='{.spec.providerID}')
INSTANCE_ID=$(echo "$PROVIDER_ID" | grep -o 'i-[a-f0-9]*')

if [ -z "$INSTANCE_ID" ]; then
    echo "Error: Could not extract instance ID from provider ID: $PROVIDER_ID"
    exit 1
fi

echo "Instance ID: $INSTANCE_ID"

# Create iptables rule to block access to API server load balancer
echo
echo "Creating host-level iptables rule to block API server access..."

# Build nsenter command to add iptables rule on the host
BLOCK_RULE_CMD="iptables -I OUTPUT -d $API_SERVER_IP -p tcp --dport $API_SERVER_PORT -j DROP"
NSENTER_BLOCK_CMD="nsenter --target 1 --mount --uts --ipc --net --pid -- $BLOCK_RULE_CMD"

echo "Executing: $NSENTER_BLOCK_CMD"

# Create a simple privileged pod to modify host iptables
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: network-disruptor-$INSTANCE_ID
  namespace: default
spec:
  hostNetwork: true
  hostPID: true
  hostIPC: true
  nodeName: $WORKER_NODE
  containers:
  - name: disruptor
    image: alpine:latest
    command:
    - /bin/sh
    - -c
    - |
      echo "Installing iptables..."
      apk add --no-cache iptables
      
      echo "Current iptables OUTPUT rules:"
      nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -L OUTPUT -n --line-numbers || echo "Failed to list rules"
      
      echo "Blocking API server access: $API_SERVER_IP:$API_SERVER_PORT"
      nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -I OUTPUT -d $API_SERVER_IP -p tcp --dport $API_SERVER_PORT -j DROP
      
      echo "Updated iptables OUTPUT rules:"
      nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -L OUTPUT -n --line-numbers || echo "Failed to list rules"
      
      echo "Network disruption active. Keeping pod running for cleanup..."
      sleep 3600
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

echo "Waiting for network disruptor pod to start..."
kubectl wait --for=condition=Ready pod/network-disruptor-$INSTANCE_ID --timeout=60s

echo "Waiting for iptables rule to take effect..."
sleep 15

# Monitor node status for becoming NotReady
echo
echo "Monitoring node status (checking every 15 seconds for 5 minutes)..."
for i in {1..20}; do
    echo "Check $i/20:"
    NODE_STATUS=$(kubectl get node "$WORKER_NODE" -o jsonpath='{.status.conditions[?(@.type=="Ready")]}' | jq -r '"Ready: " + .status + " (" + .reason + ") - " + .message')
    echo "  $NODE_STATUS"
    
    if echo "$NODE_STATUS" | grep -q -E "(False|Unknown)"; then
        echo
        echo "SUCCESS: Node is now NotReady due to API server communication failure!"
        break
    fi
    
    if [ $i -eq 20 ]; then
        echo
        echo "WARNING: Node is still Ready after 5 minutes of blocking API server access"
        echo "This suggests the disruption method is not effective"
    fi
    
    sleep 15
done

echo
echo "Detailed node status:"
kubectl describe node "$WORKER_NODE" | grep -A 10 -B 5 "Conditions\\|Lease\\|Heartbeat\\|Ready"

echo
echo "Current iptables rules on node (via disruptor pod):"
kubectl exec network-disruptor-$INSTANCE_ID -- nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -L OUTPUT -n --line-numbers

echo
echo "=== Cleanup Phase ==="
echo "Removing iptables rule and disruptor pod..."

# Remove the iptables rule
kubectl exec network-disruptor-$INSTANCE_ID -- nsenter --target 1 --mount --uts --ipc --net --pid -- iptables -D OUTPUT -d $API_SERVER_IP -p tcp --dport $API_SERVER_PORT -j DROP

echo "Removed iptables rule"

# Clean up the disruptor pod
kubectl delete pod network-disruptor-$INSTANCE_ID --wait=false

echo "Cleaned up disruptor pod"

echo
echo "Waiting for node to recover (checking every 30 seconds for 5 minutes)..."
for i in {1..10}; do
    echo "Recovery check $i/10:"
    NODE_STATUS=$(kubectl get node "$WORKER_NODE" -o jsonpath='{.status.conditions[?(@.type=="Ready")]}' | jq -r '"Ready: " + .status + " (" + .reason + ") - " + .message')
    echo "  $NODE_STATUS"
    
    if echo "$NODE_STATUS" | grep -q "True"; then
        echo
        echo "SUCCESS: Node has recovered and is Ready again!"
        break
    fi
    
    if [ $i -eq 10 ]; then
        echo
        echo "WARNING: Node has not recovered after 5 minutes"
    fi
    
    sleep 30
done

echo
echo "=== OpenShift Load Balancer Network Disruption Test Complete ===" 