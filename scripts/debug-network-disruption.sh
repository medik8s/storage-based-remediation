#!/bin/bash

# debug-network-disruption.sh
# Simple script to debug network disruption functionality
# Tests if createNetworkDisruption actually blocks kubelet communication

set -euo pipefail

echo "=== Network Disruption Debug Script ==="
echo

# Check if we're in the right region
AWS_REGION="${AWS_REGION:-ap-southeast-2}"
export AWS_REGION
export AWS_PAGER=""

echo "Using AWS region: $AWS_REGION"

# Test the specific problematic node
echo "Testing specific problematic node..."
WORKER_NODE="ip-10-0-38-215.ap-southeast-2.compute.internal"

if [ -z "$WORKER_NODE" ]; then
    echo "Error: No worker nodes found"
    exit 1
fi

echo "Selected worker node: $WORKER_NODE"

# Validate this is actually a worker node (not control plane)
NODE_ROLES=$(kubectl get node "$WORKER_NODE" -o jsonpath='{.metadata.labels}' | grep -o 'node-role\.kubernetes\.io/[^"]*' || echo "worker")
echo "Node roles: $NODE_ROLES"

if echo "$NODE_ROLES" | grep -q -E "(master|control-plane)"; then
    echo "ERROR: Selected node is a control plane node, not a worker. This will not work for testing."
    exit 1
fi

echo "Confirmed: This is a worker node, safe to test network disruption"

# Get the AWS instance ID from the node
echo "Getting AWS instance ID..."
PROVIDER_ID=$(kubectl get node "$WORKER_NODE" -o jsonpath='{.spec.providerID}')
INSTANCE_ID=$(echo "$PROVIDER_ID" | grep -o 'i-[a-f0-9]*')

if [ -z "$INSTANCE_ID" ]; then
    echo "Error: Could not extract instance ID from provider ID: $PROVIDER_ID"
    exit 1
fi

echo "Instance ID: $INSTANCE_ID"

# Check initial node status
echo
echo "Initial node status:"
kubectl get node "$WORKER_NODE" -o jsonpath='{.status.conditions[?(@.type=="Ready")]}' | jq -r '"Ready: " + .status + " (" + .reason + ") - " + .message'

# Get original security groups
echo
echo "Getting original security groups..."
ORIGINAL_SG_JSON=$(aws ec2 describe-instances --instance-ids "$INSTANCE_ID" --query 'Reservations[0].Instances[0].SecurityGroups' --output json)
echo "Original security groups:"
echo "$ORIGINAL_SG_JSON" | jq -r '.[] | "  - " + .GroupId + " (" + .GroupName + ")"'

# Get VPC ID for creating security group
VPC_ID=$(aws ec2 describe-instances --instance-ids "$INSTANCE_ID" --query 'Reservations[0].Instances[0].VpcId' --output text)
echo "VPC ID: $VPC_ID"

# Create a simple restrictive security group for testing
SG_NAME="debug-network-disruptor-$(date +%s)"
echo
echo "Creating restrictive security group: $SG_NAME"

SG_ID=$(aws ec2 create-security-group \
    --group-name "$SG_NAME" \
    --description "Debug network disruption - blocks kubelet ports" \
    --vpc-id "$VPC_ID" \
    --query 'GroupId' \
    --output text)

echo "Created security group: $SG_ID"

# Function to cleanup
cleanup() {
    echo
    echo "=== CLEANUP ==="
    
    # Restore original security groups
    if [ -n "${ORIGINAL_SG_JSON:-}" ] && [ -n "${INSTANCE_ID:-}" ]; then
        echo "Restoring original security groups..."
        ORIGINAL_SG_IDS=$(echo "$ORIGINAL_SG_JSON" | jq -r '.[].GroupId' | tr '\n' ' ')
        if [ -n "$ORIGINAL_SG_IDS" ]; then
            aws ec2 modify-instance-attribute --instance-id "$INSTANCE_ID" --groups $ORIGINAL_SG_IDS || true
            echo "Original security groups restored"
            sleep 10
        fi
    fi
    
    # Delete test security group
    if [ -n "${SG_ID:-}" ]; then
        echo "Deleting test security group: $SG_ID"
        aws ec2 delete-security-group --group-id "$SG_ID" || true
    fi
    
    echo "Cleanup completed"
}

# Set up cleanup trap
trap cleanup EXIT

# Remove all default egress rules (blocks outbound)
echo "Removing default egress rules (blocks API server communication)..."
aws ec2 revoke-security-group-egress \
    --group-id "$SG_ID" \
    --ip-permissions '[{"IpProtocol": "-1", "IpRanges": [{"CidrIp": "0.0.0.0/0"}]}]'

# Add minimal ingress rules (excludes kubelet ports)
echo "Adding minimal ingress rules (excludes kubelet ports 10250, 10255)..."
aws ec2 authorize-security-group-ingress \
    --group-id "$SG_ID" \
    --ip-permissions '[
        {
            "IpProtocol": "tcp",
            "FromPort": 22,
            "ToPort": 22,
            "IpRanges": [{"CidrIp": "0.0.0.0/0"}]
        },
        {
            "IpProtocol": "icmp",
            "FromPort": -1,
            "ToPort": -1,
            "IpRanges": [{"CidrIp": "0.0.0.0/0"}]
        }
    ]'

# Add minimal egress rules (excludes API server)
echo "Adding minimal egress rules (excludes API server communication)..."
aws ec2 authorize-security-group-egress \
    --group-id "$SG_ID" \
    --ip-permissions '[
        {
            "IpProtocol": "tcp",
            "FromPort": 22,
            "ToPort": 22,
            "IpRanges": [{"CidrIp": "0.0.0.0/0"}]
        },
        {
            "IpProtocol": "tcp",
            "FromPort": 53,
            "ToPort": 53,
            "IpRanges": [{"CidrIp": "0.0.0.0/0"}]
        },
        {
            "IpProtocol": "udp",
            "FromPort": 53,
            "ToPort": 53,
            "IpRanges": [{"CidrIp": "0.0.0.0/0"}]
        },
        {
            "IpProtocol": "icmp",
            "FromPort": -1,
            "ToPort": -1,
            "IpRanges": [{"CidrIp": "0.0.0.0/0"}]
        }
    ]'

# Apply the restrictive security group to the instance
echo
echo "Applying restrictive security group to instance..."
aws ec2 modify-instance-attribute --instance-id "$INSTANCE_ID" --groups "$SG_ID"

echo "Security group applied. Waiting for changes to take effect..."
sleep 30

# Monitor node status
echo
echo "Monitoring node status for 10 minutes..."
echo "Looking for node to become NotReady due to blocked kubelet communication"
echo

START_TIME=$(date +%s)
MAX_WAIT=600  # 10 minutes

while [ $(($(date +%s) - START_TIME)) -lt $MAX_WAIT ]; do
    CURRENT_TIME=$(date '+%H:%M:%S')
    
    # Get current node status
    NODE_STATUS=$(kubectl get node "$WORKER_NODE" -o jsonpath='{.status.conditions[?(@.type=="Ready")]}' 2>/dev/null || echo '{"status":"Unknown","reason":"Error","message":"Failed to get node status"}')
    
    STATUS=$(echo "$NODE_STATUS" | jq -r '.status // "Unknown"')
    REASON=$(echo "$NODE_STATUS" | jq -r '.reason // "Unknown"')
    MESSAGE=$(echo "$NODE_STATUS" | jq -r '.message // "No message"')
    
    echo "[$CURRENT_TIME] Node $WORKER_NODE - Ready: $STATUS ($REASON) - $MESSAGE"
    
    # Check if node became NotReady
    if [ "$STATUS" != "True" ]; then
        echo
        echo "SUCCESS: Node became NotReady!"
        echo "  Status: $STATUS"
        echo "  Reason: $REASON"
        echo "  Message: $MESSAGE"
        echo
        echo "Network disruption is working - kubelet communication blocked"
        exit 0
    fi
    
    sleep 30
done

echo
echo "TIMEOUT: Node did not become NotReady after 10 minutes"
echo "This suggests the network disruption is not effectively blocking kubelet communication"
echo

# Show current security group details for debugging
echo "Current security group rules for debugging:"
aws ec2 describe-security-groups --group-ids "$SG_ID" --query 'SecurityGroups[0]' | jq '{
    GroupId: .GroupId,
    GroupName: .GroupName,
    IngressRules: .IpPermissions,
    EgressRules: .IpPermissionsEgress
}'

exit 1 