#!/bin/bash

# Simplified ARM64 vs AMD64 watchdog test
set -eo pipefail

AWS_REGION="ap-southeast-2"
KEY_NAME="aws-linux"
export AWS_PAGER=""

log() {
    echo "[$(date +'%H:%M:%S')] $*"
}

cleanup() {
    log "Cleaning up..."
    # Terminate any test instances
    aws ec2 describe-instances \
        --region "${AWS_REGION}" \
        --filters "Name=tag:Name,Values=test-amd64,test-arm64" \
                 "Name=instance-state-name,Values=running,pending" \
        --query 'Reservations[].Instances[].InstanceId' \
        --output text | while read -r instance_id; do
        if [[ -n "$instance_id" ]]; then
            log "Terminating $instance_id"
            aws ec2 terminate-instances --region "${AWS_REGION}" --instance-ids "$instance_id" >/dev/null
        fi
    done
    
    # Delete test security groups
    for sg_name in test-sg-amd64 test-sg-arm64; do
        sg_id=$(aws ec2 describe-security-groups \
            --region "${AWS_REGION}" \
            --group-names "$sg_name" \
            --query 'SecurityGroups[0].GroupId' \
            --output text 2>/dev/null | grep -v "None" || echo "")
        if [[ -n "$sg_id" && "$sg_id" != "None" ]]; then
            log "Deleting security group $sg_id"
            aws ec2 delete-security-group --region "${AWS_REGION}" --group-id "$sg_id" 2>/dev/null || true
        fi
    done
}

create_test_instance() {
    local arch="$1"
    local ami_id="$2"
    
    log "Creating $arch test instance..."
    
    # Get VPC
    vpc_id=$(aws ec2 describe-vpcs \
        --region "${AWS_REGION}" \
        --filters "Name=is-default,Values=true" \
        --query 'Vpcs[0].VpcId' \
        --output text)
    
    # Create security group
    sg_name="test-sg-$arch"
    sg_id=$(aws ec2 create-security-group \
        --region "${AWS_REGION}" \
        --group-name "$sg_name" \
        --description "Test $arch" \
        --vpc-id "$vpc_id" \
        --query 'GroupId' \
        --output text)
    
    # Add SSH rule
    aws ec2 authorize-security-group-ingress \
        --region "${AWS_REGION}" \
        --group-id "$sg_id" \
        --protocol tcp \
        --port 22 \
        --cidr "0.0.0.0/0" >/dev/null
    
    # Simple user data
    user_data="#!/bin/bash
yum update -y
yum install -y golang
modprobe softdog || true
mkdir -p /opt/test
echo 'ready' > /opt/test/status"
    
    # Create instance
    instance_id=$(aws ec2 run-instances \
        --region "${AWS_REGION}" \
        --image-id "$ami_id" \
        --instance-type "t3.micro" \
        --key-name "${KEY_NAME}" \
        --security-group-ids "$sg_id" \
        --user-data "$user_data" \
        --tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=test-$arch}]" \
        --query 'Instances[0].InstanceId' \
        --output text)
    
    log "$arch instance created: $instance_id"
    echo "$instance_id"
}

main() {
    trap cleanup EXIT
    cleanup
    
    log "Starting simple ARM64 vs AMD64 test..."
    
    # Create both instances
    amd64_instance=$(create_test_instance "amd64" "ami-0e6874cbf738602e7")
    arm64_instance=$(create_test_instance "arm64" "ami-06494f9d4da11dbad")
    
    log "Both instances created successfully!"
    log "AMD64: $amd64_instance"
    log "ARM64: $arm64_instance"
    
    # Wait for instances to be running
    log "Waiting for instances to be running..."
    aws ec2 wait instance-running --region "${AWS_REGION}" --instance-ids "$amd64_instance" "$arm64_instance"
    
    log "Both instances are now running!"
    log "Test completed - instances will be cleaned up automatically"
}

main "$@"
