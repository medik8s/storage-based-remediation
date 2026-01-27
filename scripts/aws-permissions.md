# AWS Permissions for Emergency Node Reboot

The `emergency-reboot-node.sh` script requires specific AWS IAM permissions to reboot EC2 instances.

## Required AWS Policy

Create an IAM policy with the following permissions:

```json
{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Sid": "EmergencyNodeRebootPermissions",
            "Effect": "Allow",
            "Action": [
                "ec2:RebootInstances",
                "ec2:DescribeInstances"
            ],
            "Resource": "*",
            "Condition": {
                "StringEquals": {
                    "ec2:ResourceTag/kubernetes.io/cluster/<cluster-name>": "owned"
                }
            }
        }
    ]
}
```

## Setup Options

### Option 1: IAM User with Policy

1. Create an IAM user for SBD operations
2. Attach the above policy to the user
3. Generate access keys
4. Configure AWS CLI: `aws configure`

### Option 2: IAM Role (Recommended for Production)

1. Create an IAM role with the above policy
2. Attach the role to:
   - SBD operator pods (using IRSA)
   - OpenShift cluster instances
   - EC2 instances running the script

### Option 3: Cross-Account Access

For multi-account setups, create a cross-account role:

```json
{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Effect": "Allow",
            "Principal": {
                "AWS": "arn:aws:iam::OPERATOR-ACCOUNT:role/SBDOperatorRole"
            },
            "Action": "sts:AssumeRole"
        }
    ]
}
```

## Security Considerations

⚠️ **Principle of Least Privilege**: The policy above restricts reboots to instances tagged with the cluster name.

✅ **Recommended**: Use instance tags to further restrict access:

- Only instances with specific tags (e.g., `sbd-fencing-enabled=true`)
- Only instances in specific subnets or VPCs
- Time-based conditions for emergency-only access

❌ **Avoid**: Granting `ec2:*` permissions or unrestricted `ec2:RebootInstances`

## Testing Permissions

Test the permissions before deploying:

```bash
# Test AWS CLI access
aws ec2 describe-instances --instance-ids i-1234567890abcdef0

# Test reboot permissions (dry-run)
aws ec2 reboot-instances --instance-ids i-1234567890abcdef0 --dry-run

# Test the emergency reboot script
./scripts/emergency-reboot-node.sh --dry-run <node-name>
```

## Troubleshooting

### "UnauthorizedOperation" Error

- Verify IAM policy is attached
- Check policy conditions match instance tags
- Ensure AWS credentials are configured
- Verify instance ID resolution from node

### "AccessDenied" for DescribeInstances

- Add `ec2:DescribeInstances` permission
- Check resource-level permissions
- Verify cluster tagging is correct

## Integration with SBD Operator

For automated SBD fencing, ensure:

1. SBD operator pods have the IAM role attached
2. The role includes the emergency reboot permissions
3. Cluster nodes are properly tagged
4. Network connectivity allows AWS API access
