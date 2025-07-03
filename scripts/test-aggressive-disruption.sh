#!/bin/bash

# test-aggressive-disruption.sh
# Aggressive network disruption that blocks ALL traffic except SSH

set -euo pipefail

echo "=== Aggressive Network Disruption Test ==="
echo

# Check if we're in the right region
AWS_REGION="${AWS_REGION:-ap-southeast-2}"
export AWS_REGION
export AWS_PAGER=""

echo "Using AWS region: $AWS_REGION"

# Test the specific problematic node
WORKER_NODE="ip-10-0-38-215.ap-southeast-2.compute.internal"
echo "Testing aggressive disruption on: $WORKER_NODE"

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

# Create an extremely restrictive security group
SG_NAME="aggressive-disruptor-$(date +%s)"
echo
echo "Creating extremely restrictive security group: $SG_NAME"

SG_ID=$(aws ec2 create-security-group \
    --group-name "$SG_NAME" \
    --description "Aggressive network disruption - blocks nearly all traffic" \
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

# Remove ALL default egress rules
echo "Removing ALL default egress rules (complete outbound block)..."
aws ec2 revoke-security-group-egress \
    --group-id "$SG_ID" \
    --ip-permissions '[{"IpProtocol": "-1", "IpRanges": [{"CidrIp": "0.0.0.0/0"}]}]'

# Add ONLY SSH ingress (nothing else)
echo "Adding ONLY SSH ingress (blocks ALL other inbound including kubelet)..."
aws ec2 authorize-security-group-ingress \
    --group-id "$SG_ID" \
    --ip-permissions '[
        {
            "IpProtocol": "tcp",
            "FromPort": 22,
            "ToPort": 22,
            "IpRanges": [{"CidrIp": "0.0.0.0/0"}]
        }
    ]'

# Add ONLY SSH egress (nothing else - no DNS, no API server, nothing)
echo "Adding ONLY SSH egress (blocks ALL other outbound including API server)..."
aws ec2 authorize-security-group-egress \
    --group-id "$SG_ID" \
    --ip-permissions '[
        {
            "IpProtocol": "tcp",
            "FromPort": 22,
            "ToPort": 22,
            "IpRanges": [{"CidrIp": "0.0.0.0/0"}]
        }
    ]'

# Apply the extremely restrictive security group
echo
echo "Applying extremely restrictive security group to instance..."
aws ec2 modify-instance-attribute --instance-id "$INSTANCE_ID" --groups "$SG_ID"

echo "Security group applied. Waiting for changes to take effect..."
sleep 30

# Monitor node status
echo
echo "Monitoring node status for 5 minutes..."
echo "With complete traffic blocking, node should become NotReady quickly"
echo

START_TIME=$(date +%s)
MAX_WAIT=300  # 5 minutes

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
        echo "SUCCESS: Node became NotReady with aggressive blocking!"
        echo "  Status: $STATUS"
        echo "  Reason: $REASON"
        echo "  Message: $MESSAGE"
        echo
        echo "Aggressive network disruption is working"
        exit 0
    fi
    
    sleep 15  # More frequent checks
done

echo
echo "TIMEOUT: Node did not become NotReady even with complete traffic blocking"
echo "This suggests some fundamental issue with our approach"
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