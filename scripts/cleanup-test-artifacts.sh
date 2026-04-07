#!/bin/bash

# cleanup-test-artifacts.sh
# Script to clean up leftover artifacts from SBR operator e2e test runs
# This removes temporary security groups and other AWS resources created during testing

set -euo pipefail

echo "=== SBR Operator Test Cleanup Script ==="
echo

# Check if AWS CLI is available
if ! command -v aws &> /dev/null; then
    echo "Error: AWS CLI is not installed or not in PATH"
    echo "Please install AWS CLI and configure credentials"
    exit 1
fi

# Check if we can access AWS
if ! aws sts get-caller-identity &> /dev/null; then
    echo "Error: Cannot access AWS. Please check your credentials and configuration"
    exit 1
fi

echo "✓ AWS access verified"

# Get the current AWS region
AWS_REGION="${AWS_REGION:-}"
if [ -z "$AWS_REGION" ]; then
    AWS_REGION="${AWS_DEFAULT_REGION:-}"
fi
if [ -z "$AWS_REGION" ]; then
    AWS_REGION=$(aws configure get region 2>/dev/null || echo "")
fi
if [ -z "$AWS_REGION" ]; then
    AWS_REGION=$(aws ec2 describe-availability-zones --query 'AvailabilityZones[0].RegionName' --output text 2>/dev/null || echo "")
fi

if [ -z "$AWS_REGION" ]; then
    echo "Error: Could not determine AWS region. Please set AWS_REGION environment variable"
    exit 1
fi

echo "✓ Using AWS region: $AWS_REGION"
export AWS_PAGER=""

# Find test-related security groups
echo
echo "Finding test-related security groups..."

TEST_SECURITY_GROUPS=$(aws ec2 describe-security-groups \
    --query 'SecurityGroups[?contains(GroupName, `sbr-e2e-`) || contains(GroupName, `sbr-e2e-network-disruptor`) || contains(GroupName, `sbr-e2e-restore`) || contains(GroupName, `sbr-e2e-network-test`) || contains(GroupName, `sbr-e2e-storage-test`)].{GroupId:GroupId,GroupName:GroupName}' \
    --output text 2>/dev/null || echo "")

if [ -z "$TEST_SECURITY_GROUPS" ]; then
    echo "✓ No test security groups found"
else
    echo "Found test security groups:"
    echo "$TEST_SECURITY_GROUPS" | while read -r GROUP_ID GROUP_NAME; do
        echo "  - $GROUP_ID ($GROUP_NAME)"
    done
fi

# Get all running instances (to check which security groups are in use)
echo
echo "Finding cluster instances..."

RUNNING_INSTANCES=$(aws ec2 describe-instances \
    --filters "Name=instance-state-name,Values=running" \
    --query 'Reservations[].Instances[].InstanceId' \
    --output text 2>/dev/null || echo "")

if [ -z "$RUNNING_INSTANCES" ]; then
    echo "✓ No running instances found"
else
    echo "Found running instances:"
    for INSTANCE_ID in $RUNNING_INSTANCES; do
        echo "  - $INSTANCE_ID"
    done
fi

# Clean up security groups from instances
if [ -n "$TEST_SECURITY_GROUPS" ] && [ -n "$RUNNING_INSTANCES" ]; then
    echo
    echo "Cleaning up security groups from instances..."
    
    for INSTANCE_ID in $RUNNING_INSTANCES; do
        echo "Checking instance $INSTANCE_ID..."
        
        # Get current security groups for this instance
        CURRENT_GROUPS=$(aws ec2 describe-instances \
            --instance-ids "$INSTANCE_ID" \
            --query 'Reservations[0].Instances[0].SecurityGroups[].GroupId' \
            --output text 2>/dev/null || echo "")
        
        if [ -z "$CURRENT_GROUPS" ]; then
            echo "  No security groups found for instance $INSTANCE_ID"
            continue
        fi
        
        # Filter out test security groups
        CLEAN_GROUPS=""
        HAS_TEST_GROUPS=false
        
        for GROUP_ID in $CURRENT_GROUPS; do
            IS_TEST_GROUP=false
            echo "$TEST_SECURITY_GROUPS" | while read -r TEST_GROUP_ID TEST_GROUP_NAME; do
                if [ "$GROUP_ID" = "$TEST_GROUP_ID" ]; then
                    IS_TEST_GROUP=true
                    HAS_TEST_GROUPS=true
                    echo "  Found test security group on instance: $GROUP_ID ($TEST_GROUP_NAME)"
                    break
                fi
            done
            
            if [ "$IS_TEST_GROUP" = "false" ]; then
                if [ -z "$CLEAN_GROUPS" ]; then
                    CLEAN_GROUPS="$GROUP_ID"
                else
                    CLEAN_GROUPS="$CLEAN_GROUPS $GROUP_ID"
                fi
            fi
        done
        
        # If instance has test groups and clean groups, restore to clean groups
        if [ "$HAS_TEST_GROUPS" = "true" ] && [ -n "$CLEAN_GROUPS" ]; then
            echo "  Removing test security groups from instance $INSTANCE_ID"
            if aws ec2 modify-instance-attribute \
                --instance-id "$INSTANCE_ID" \
                --groups $CLEAN_GROUPS 2>/dev/null; then
                echo "  ✓ Cleaned security groups from instance $INSTANCE_ID"
            else
                echo "  ✗ Failed to clean security groups from instance $INSTANCE_ID"
            fi
        elif [ "$HAS_TEST_GROUPS" = "true" ] && [ -z "$CLEAN_GROUPS" ]; then
            echo "  Warning: Instance $INSTANCE_ID only has test security groups"
            echo "  Manual intervention may be required to restore proper security groups"
        fi
    done
    
    # Wait for changes to take effect
    echo
    echo "Waiting for security group changes to take effect..."
    sleep 30
fi

# Delete test security groups
if [ -n "$TEST_SECURITY_GROUPS" ]; then
    echo
    echo "Deleting test security groups..."
    
    echo "$TEST_SECURITY_GROUPS" | while read -r GROUP_ID GROUP_NAME; do
        echo "Deleting security group $GROUP_ID ($GROUP_NAME)..."
        
        if aws ec2 delete-security-group --group-id "$GROUP_ID" 2>/dev/null; then
            echo "  ✓ Successfully deleted security group $GROUP_ID"
        else
            echo "  ✗ Failed to delete security group $GROUP_ID (may have dependencies)"
            
            # Try again after waiting
            echo "  Waiting 60 seconds and retrying..."
            sleep 60
            
            if aws ec2 delete-security-group --group-id "$GROUP_ID" 2>/dev/null; then
                echo "  ✓ Successfully deleted security group $GROUP_ID after retry"
            else
                echo "  ✗ Failed to delete security group $GROUP_ID after retry"
                echo "  Manual cleanup may be required"
            fi
        fi
    done
fi

# Clean up any test namespaces in Kubernetes (if kubectl is available)
if command -v kubectl &> /dev/null; then
    echo
    echo "Cleaning up test namespaces in Kubernetes..."
    
    TEST_NAMESPACES=$(kubectl get namespaces -o name 2>/dev/null | grep -E 'sbr-.*-test|sbr-e2e' || echo "")
    
    if [ -z "$TEST_NAMESPACES" ]; then
        echo "✓ No test namespaces found"
    else
        echo "Found test namespaces:"
        echo "$TEST_NAMESPACES"
        
        for NS in $TEST_NAMESPACES; do
            NS_NAME=$(echo "$NS" | cut -d'/' -f2)
            echo "Deleting namespace $NS_NAME..."
            
            if kubectl delete namespace "$NS_NAME" --timeout=60s 2>/dev/null; then
                echo "  ✓ Successfully deleted namespace $NS_NAME"
            else
                echo "  ✗ Failed to delete namespace $NS_NAME"
            fi
        done
    fi
else
    echo
    echo "kubectl not available - skipping Kubernetes namespace cleanup"
fi

echo
echo "=== Cleanup completed ==="
echo
echo "Summary:"
echo "- Cleaned up test security groups from instances"
echo "- Deleted temporary test security groups"
echo "- Cleaned up test Kubernetes namespaces (if kubectl available)"
echo
echo "If any resources failed to clean up, you may need to:"
echo "1. Wait for dependencies to be released"
echo "2. Manually remove remaining resources via AWS console"
echo "3. Check for any running pods or services that might be holding references" 