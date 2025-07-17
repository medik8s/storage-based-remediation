# EFS Tagging Requirements for SBD Operator

## Critical Security Issue Fixed

**Problem:** The Go `setup-shared-storage` tool relies on EFS tags to detect existing filesystems and avoid creating duplicates. However, the required tagging permissions were not properly validated, leading to:

- Silent failures in filesystem detection
- Creation of duplicate EFS resources
- Unnecessary AWS costs
- Resource management complications

## Required Permissions

The following AWS permissions are **MANDATORY** for proper operation:

### EFS Tagging Permissions
```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "EC2ReadOnlyPermissions",
      "Effect": "Allow",
      "Action": ["ec2:DescribeVpcs", "ec2:DescribeSubnets", "ec2:DescribeSecurityGroups"],
      "Resource": "*"  // Required for region-wide discovery
    },
    {
      "Sid": "EC2SecurityGroupManagement", 
      "Effect": "Allow",
      "Action": ["ec2:CreateSecurityGroup", "ec2:AuthorizeSecurityGroupIngress"],
      "Resource": ["arn:aws:ec2:*:*:security-group/*", "arn:aws:ec2:*:*:vpc/*"]
    },
    {
      "Sid": "EC2Tagging",
      "Effect": "Allow", 
      "Action": ["ec2:CreateTags"],
      "Resource": "arn:aws:ec2:*:*:security-group/*",
      "Condition": {"StringEquals": {"ec2:CreateAction": "CreateSecurityGroup"}}
    },
    {
      "Sid": "EFSReadOperations",
      "Effect": "Allow",
      "Action": ["elasticfilesystem:DescribeFileSystems", "elasticfilesystem:DescribeMountTargets", "elasticfilesystem:DescribeTags"],  
      "Resource": "*"  // Required to discover existing EFS filesystems
    },
    {
      "Sid": "EFSWriteOperations",
      "Effect": "Allow", 
      "Action": ["elasticfilesystem:CreateFileSystem", "elasticfilesystem:CreateMountTarget", "elasticfilesystem:CreateTags"],
      "Resource": ["arn:aws:elasticfilesystem:*:*:file-system/*", "arn:aws:elasticfilesystem:*:*:mount-target/*"]
    }
  ]
}
```

### Critical EFS Tagging Permissions
```json
"elasticfilesystem:DescribeTags"   // CRITICAL: Required to detect existing EFS by name
"elasticfilesystem:CreateTags"     // CRITICAL: Required to tag new EFS for future reuse
```

### Security Improvements (v2)

**BEFORE:** Two broad statements with `"Resource": "*"` for everything  
**AFTER:** Five granular statements with proper resource scoping

- ✅ **Read operations**: Use `"*"` only where necessary for discovery
- ✅ **Write operations**: Scoped to specific ARN patterns  
- ✅ **Conditional tagging**: Only during resource creation
- ✅ **Principle of least privilege**: Each statement has minimal required permissions
- ✅ **Sid identifiers**: Clear statement purposes for auditing

### Why These Permissions Are Critical

1. **`elasticfilesystem:DescribeTags`**
   - **Purpose:** Enables detection of existing EFS filesystems by reading their `Name` tag
   - **Without it:** Tool cannot find existing EFS → creates duplicates
   - **Impact:** Wasted money, resource sprawl, cleanup difficulties

2. **`elasticfilesystem:CreateTags`**  
   - **Purpose:** Tags new EFS filesystems with proper `Name`, `Cluster`, and `Purpose` tags
   - **Without it:** New EFS cannot be found in future runs → more duplicates
   - **Impact:** Exponential resource growth with each run

## Detection Logic

The tool uses this logic to find existing EFS filesystems:

```go
// pkg/storage/aws/manager.go
func (m *Manager) findEFSByName(ctx context.Context, name string) (string, error) {
    // 1. List all EFS filesystems in region
    result, err := m.efsClient.DescribeFileSystems(ctx, &efs.DescribeFileSystemsInput{})
    
    for _, fs := range result.FileSystems {
        // 2. Get tags for each filesystem (REQUIRES elasticfilesystem:DescribeTags)
        tags, err := m.efsClient.DescribeTags(ctx, &efs.DescribeTagsInput{
            FileSystemId: fs.FileSystemId,
        })
        
        // 3. Check if Name tag matches desired EFS name
        for _, tag := range tags.Tags {
            if *tag.Key == "Name" && *tag.Value == name {
                return *fs.FileSystemId, nil // Found existing EFS
            }
        }
    }
    return "", nil // No existing EFS found → will create new one
}
```

## Impact Before Fix

**Without proper tagging permissions:**
1. `DescribeTags` calls fail silently
2. No existing EFS filesystems are detected  
3. Tool creates new EFS for every run
4. Users accumulate many unused EFS resources
5. AWS bills increase unnecessarily

**Example scenario:**
- Run 1: Creates `fs-abc123` (no existing EFS found)
- Run 2: Creates `fs-def456` (can't read tags to find `fs-abc123`)  
- Run 3: Creates `fs-ghi789` (can't read tags to find previous EFS)
- Result: 3 identical EFS filesystems, 3x the cost

## Solution Implemented

### 1. **Mandatory Permission Validation**
Added `elasticfilesystem:DescribeTags` and `elasticfilesystem:CreateTags` to required permissions:

```go
// pkg/storage/aws/manager.go - ValidateAWSPermissions()
requiredPermissions := []struct{...}{
    {
        name:        "elasticfilesystem:DescribeTags",
        description: "Read EFS filesystem tags (MANDATORY for reusing existing filesystems)",
        testFn:      m.testDescribeTags,
    },
    {
        name:        "elasticfilesystem:CreateTags", 
        description: "Create tags on EFS filesystems (required for proper resource management)",
        testFn:      m.testCreateTags,
    },
    // ... other permissions
}
```

### 2. **Fixed Permission Validation**
Fixed `testDescribeTags()` to properly test the permission:

```go
// BEFORE (broken): Missing required FileSystemId parameter
_, err := m.efsClient.DescribeTags(ctx, &efs.DescribeTagsInput{
    MaxItems: aws.Int32(1),  // Would fail with parameter error, not permission error
})

// AFTER (correct): Include invalid FileSystemId to trigger validation error
_, err := m.efsClient.DescribeTags(ctx, &efs.DescribeTagsInput{
    FileSystemId: aws.String("fs-nonexistent123"), // Triggers validation error if permission exists
    MaxItems:     aws.Int32(1),
})
```

### 3. **Improved Error Handling**
Enhanced `findEFSByName()` to fail fast on permission errors:

```go
if err != nil {
    // Check if this is a permission error - this is critical and should not be ignored
    if isPermissionDeniedError(err) {
        return "", fmt.Errorf("❌ CRITICAL: Missing elasticfilesystem:DescribeTags permission - required for detecting existing EFS filesystems to avoid duplicates. Error: %w", err)
    }
    // ... handle other errors
}
```

### 4. **Clear Documentation**
Updated IAM policy generator to explain why tagging permissions are critical:

```bash
./setup-shared-storage --generate-iam-policy
```

Shows prominent warnings about tagging requirements and consequences.

## Usage Guidelines

### Before Running the Tool
1. **Generate IAM policy:**
   ```bash
   ./setup-shared-storage --generate-iam-policy > efs-policy.json
   ```

2. **Apply the policy to your AWS user/role**
3. **Verify permissions:**
   ```bash
   aws efs describe-file-systems --max-items 1  # Test basic EFS access
   aws efs describe-tags --file-system-id fs-fake123 2>&1 | grep -i "access denied" || echo "DescribeTags OK"
   ```

### When Using Existing EFS
If you want to use an existing EFS filesystem explicitly:

```bash
./setup-shared-storage --no-create-efs --filesystem-id fs-existing123
```

The tool will still validate the filesystem exists and is accessible.

## Backward Compatibility

This fix is backward compatible:
- ✅ Existing users with full permissions: No change in behavior
- ✅ New users: Will get clear error messages about missing permissions  
- ✅ Users with insufficient permissions: Will see helpful guidance instead of silent failures

## Testing the Fix

To verify the fix works correctly:

1. **Test with full permissions:** Should detect existing EFS by name
2. **Test with missing `elasticfilesystem:DescribeTags`:** Should fail with clear error message
3. **Test with missing `elasticfilesystem:CreateTags`:** Should fail during EFS creation with validation error

This ensures users cannot accidentally create duplicate resources due to missing permissions. 
