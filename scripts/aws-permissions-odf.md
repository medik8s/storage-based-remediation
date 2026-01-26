# AWS Permissions for ODF Setup Tool

The `setup-odf-storage` tool requires specific AWS IAM permissions to automatically provision EBS volumes for OpenShift Data Foundation storage clusters.

## Overview

When deploying ODF on AWS clusters, the tool can automatically:
- Analyze existing worker node storage capacity
- Provision additional EBS volumes if needed
- Attach volumes to worker nodes
- Configure appropriate volume types and settings

## Required AWS Policy

Create an IAM policy with the following comprehensive permissions:

```json
{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Sid": "ODFStorageManagement",
            "Effect": "Allow",
            "Action": [
                "ec2:CreateVolume",
                "ec2:DeleteVolume",
                "ec2:AttachVolume",
                "ec2:DetachVolume",
                "ec2:ModifyVolume",
                "ec2:DescribeVolumes",
                "ec2:DescribeVolumeStatus",
                "ec2:DescribeVolumeAttribute",
                "ec2:DescribeInstances",
                "ec2:DescribeInstanceStatus",
                "ec2:DescribeInstanceAttribute",
                "ec2:DescribeRegions",
                "ec2:DescribeAvailabilityZones",
                "ec2:CreateTags",
                "ec2:DescribeTags",
                "ec2:CreateSnapshot",
                "ec2:DeleteSnapshot",
                "ec2:DescribeSnapshots"
            ],
            "Resource": "*"
        },
        {
            "Sid": "KMSForEncryption",
            "Effect": "Allow",
            "Action": [
                "kms:CreateGrant",
                "kms:Decrypt",
                "kms:DescribeKey",
                "kms:Encrypt",
                "kms:GenerateDataKey",
                "kms:ReEncrypt*"
            ],
            "Resource": "*",
            "Condition": {
                "StringEquals": {
                    "kms:ViaService": "ec2.REGION.amazonaws.com"
                }
            }
        },
        {
            "Sid": "IAMReadOnly",
            "Effect": "Allow",
            "Action": [
                "iam:GetUser",
                "iam:ListAccessKeys"
            ],
            "Resource": "*"
        }
    ]
}
```

**Note**: Replace `REGION` in the KMS condition with your actual AWS region (e.g., `us-east-1`).

## Minimal Policy (Storage Only)

For environments where you only need storage provisioning:

```json
{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Sid": "MinimalODFStorage",
            "Effect": "Allow",
            "Action": [
                "ec2:CreateVolume",
                "ec2:AttachVolume",
                "ec2:DescribeInstances",
                "ec2:DescribeVolumes",
                "ec2:CreateTags"
            ],
            "Resource": "*"
        }
    ]
}
```

## Restrictive Policy (Production Recommended)

For production environments, restrict access to specific clusters and resources:

```json
{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Sid": "ODFStorageClusterSpecific",
            "Effect": "Allow",
            "Action": [
                "ec2:CreateVolume",
                "ec2:AttachVolume",
                "ec2:DescribeInstances",
                "ec2:DescribeVolumes"
            ],
            "Resource": "*",
            "Condition": {
                "StringEquals": {
                    "ec2:ResourceTag/kubernetes.io/cluster/CLUSTER-NAME": "owned"
                }
            }
        },
        {
            "Sid": "VolumeTagging",
            "Effect": "Allow",
            "Action": [
                "ec2:CreateTags"
            ],
            "Resource": "arn:aws:ec2:*:*:volume/*",
            "Condition": {
                "StringEquals": {
                    "ec2:CreateAction": "CreateVolume"
                }
            }
        },
        {
            "Sid": "ReadOnlyOperations",
            "Effect": "Allow",
            "Action": [
                "ec2:DescribeRegions",
                "ec2:DescribeAvailabilityZones",
                "iam:GetUser"
            ],
            "Resource": "*"
        }
    ]
}
```

**Note**: Replace `CLUSTER-NAME` with your actual OpenShift cluster name.

## Setup Options

### Option 1: IAM User with Policy (Development)
1. Create an IAM user for ODF operations
2. Attach the ODF storage policy to the user
3. Generate access keys
4. Configure AWS CLI: `aws configure`

```bash
# Create IAM user
aws iam create-user --user-name odf-storage-user

# Attach policy
aws iam attach-user-policy \
  --user-name odf-storage-user \
  --policy-arn arn:aws:iam::ACCOUNT:policy/ODFStoragePolicy

# Create access keys
aws iam create-access-key --user-name odf-storage-user
```

### Option 2: IAM Role (Production Recommended)
1. Create an IAM role with the ODF storage policy
2. Attach the role to OpenShift cluster nodes using instance profiles

```bash
# Create role
aws iam create-role \
  --role-name ODFStorageRole \
  --assume-role-policy-document file://trust-policy.json

# Attach policy
aws iam attach-role-policy \
  --role-name ODFStorageRole \
  --policy-arn arn:aws:iam::ACCOUNT:policy/ODFStoragePolicy

# Create instance profile
aws iam create-instance-profile --instance-profile-name ODFStorageProfile
aws iam add-role-to-instance-profile \
  --instance-profile-name ODFStorageProfile \
  --role-name ODFStorageRole
```

### Option 3: IRSA (OpenShift Service Account)
For OpenShift clusters with IAM Roles for Service Accounts (IRSA):

```bash
# Create role for service account
aws iam create-role \
  --role-name ODFOperatorRole \
  --assume-role-policy-document file://irsa-trust-policy.json

# Attach policy
aws iam attach-role-policy \
  --role-name ODFOperatorRole \
  --policy-arn arn:aws:iam::ACCOUNT:policy/ODFStoragePolicy
```

## Permission Testing

The tool automatically validates permissions before proceeding. You can also test manually:

```bash
# Test basic EC2 permissions
aws ec2 describe-instances --max-results 5

# Test volume operations (dry-run)
aws ec2 create-volume \
  --size 100 \
  --volume-type gp3 \
  --availability-zone us-east-1a \
  --dry-run

# Test volume attachment (dry-run)
aws ec2 attach-volume \
  --volume-id vol-12345678 \
  --instance-id i-12345678 \
  --device /dev/sdf \
  --dry-run

# Run tool permission check
./bin/setup-odf-storage --dry-run --verbose
```

## Permission Errors and Troubleshooting

### Common Error Messages

#### "UnauthorizedOperation: You are not authorized to perform this operation"
- **Cause**: Missing IAM permissions
- **Solution**: Attach the required IAM policy to your user/role

#### "InvalidVolume.NotFound"
- **Cause**: Normal during dry-run testing
- **Solution**: No action needed - this is expected behavior

#### "InvalidInstanceID.NotFound"
- **Cause**: Testing with non-existent instance IDs
- **Solution**: No action needed during testing

### Permission Validation Process

The tool performs the following permission checks:

1. **Authentication**: Validates AWS credentials
2. **EC2 Read**: Tests `DescribeInstances` and `DescribeVolumes`
3. **EC2 Write**: Tests `CreateVolume` and `AttachVolume` (dry-run)
4. **IAM Read**: Tests identity retrieval (optional)

If any permission check fails, the tool will:
- Display the specific permission error
- Generate the complete required IAM policy
- Exit without making changes

### Input vs Permission Errors

The tool differentiates between:

**Input Errors** (User Configuration):
- Invalid storage sizes
- Unsupported volume types
- Missing required flags

**Permission Errors** (AWS IAM):
- Missing EC2 permissions
- Insufficient KMS access
- Invalid AWS credentials

Permission errors include the complete IAM policy needed to resolve the issue.

## Volume Configuration Options

### Volume Types and IOPS

| Volume Type | Default IOPS | Max IOPS | Use Case |
|-------------|--------------|----------|----------|
| gp3 (default) | 3,000 | 16,000 | Balanced performance |
| gp2 | 100-3,000 | 3,000 | General purpose |
| io1 | User-defined | 64,000 | High IOPS requirements |
| io2 | User-defined | 64,000 | Highest performance |

### Storage Sizing

The tool calculates required storage per node:
- Total storage size ÷ replica count
- Adds 20% overhead for metadata
- Minimum 100GB per node

Example for 2Ti cluster with 3 replicas:
- Per node: 2048GB ÷ 3 = 682GB
- With overhead: 682GB × 1.2 = 819GB
- Rounded to 820GB per node

## Security Best Practices

### Principle of Least Privilege
- Use the restrictive policy for production
- Limit permissions to specific clusters
- Use resource-based conditions

### Encryption
- Enable EBS encryption: `--enable-encryption`
- Use customer-managed KMS keys: `--aws-kms-key=alias/my-key`
- Rotate access keys regularly

### Monitoring
- Enable CloudTrail for API auditing
- Monitor volume creation/attachment events
- Set up alerts for unusual activity

## Integration with ODF

### Storage Class Configuration
The tool creates EBS volumes that ODF uses for:
- Ceph OSD storage
- Metadata storage
- WAL (Write-Ahead Log) storage

### Volume Management
- Volumes are tagged for identification
- Automatic cleanup on cluster deletion
- Supports volume expansion

### High Availability
- Distributes storage across multiple AZs
- Replication handled by Ceph
- Automatic failover capabilities

## Troubleshooting Common Issues

### "Insufficient storage on nodes"
- **Solution**: Tool will automatically provision additional volumes
- **Manual**: Add `--storage-size` parameter to increase allocation

### "Volume attachment failed"
- **Cause**: Instance may not support additional volumes
- **Solution**: Check instance type limits and available device names

### "KMS key access denied"
- **Cause**: Missing KMS permissions or invalid key
- **Solution**: Verify KMS policy and key availability

## Related Documentation

- [SBD Operator User Guide](../docs/sbdconfig-user-guide.md)
- [OpenShift Data Foundation Documentation](https://access.redhat.com/documentation/en-us/red_hat_openshift_data_foundation)
- [AWS EBS Volume Types](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ebs-volume-types.html)
- [AWS IAM Best Practices](https://docs.aws.amazon.com/IAM/latest/UserGuide/best-practices.html) 
