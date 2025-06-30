#!/bin/bash

# Simple EC2 test to isolate the InvalidCharacter issue
set -eo pipefail

AWS_REGION="ap-southeast-2"
KEY_NAME="aws-linux"
export AWS_PAGER=""

echo "Testing simple EC2 instance creation..."

# Get default VPC
vpc_id=$(aws ec2 describe-vpcs \
    --region "${AWS_REGION}" \
    --filters "Name=is-default,Values=true" \
    --query 'Vpcs[0].VpcId' \
    --output text)

echo "VPC ID: ${vpc_id}"

# Create simple security group
sg_name="test-sg-$(date +%s)"
sg_id=$(aws ec2 create-security-group \
    --region "${AWS_REGION}" \
    --group-name "${sg_name}" \
    --description "Test security group" \
    --vpc-id "${vpc_id}" \
    --query 'GroupId' \
    --output text)

echo "Created security group: ${sg_id}"

# Simple user data
user_data="#!/bin/bash
echo 'Test instance' > /tmp/test.txt"

echo "Trying to create instance with simple user data..."

# Try to create instance
aws ec2 run-instances \
    --region "${AWS_REGION}" \
    --image-id "ami-0e6874cbf738602e7" \
    --instance-type "t3.micro" \
    --key-name "${KEY_NAME}" \
    --security-group-ids "${sg_id}" \
    --user-data "${user_data}" \
    --tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=test-instance}]" \
    --query 'Instances[0].InstanceId' \
    --output text

echo "Test completed"
