# ARM64 Watchdog Debug Tools

This directory contains tools to debug and compare watchdog behavior between ARM64 and AMD64 architectures, specifically to investigate the WDIOC_KEEPALIVE ioctl compatibility issue discovered in the SBD operator.

## Problem

The SBD operator experiences continuous reboot loops on ARM64 Red Hat Enterprise Linux CoreOS 9.6 nodes due to:

- SBD agent successfully opens `/dev/watchdog` device
- SBD agent successfully loads `softdog` kernel module
- **CRITICAL FAILURE**: `WDIOC_KEEPALIVE` ioctl call returns `ENOTTY` (inappropriate ioctl for device)
- Watchdog timer expires (~60 seconds) → kernel panic → reboot → repeat

## Tools

### 1. debug-watchdog-arm64.go

Standalone Go program that performs comprehensive watchdog device testing:

- Tests multiple watchdog devices (`/dev/watchdog`, `/dev/watchdog0`, `/dev/watchdog1`)
- Tests all major watchdog ioctl calls:
  - `WDIOC_GETINFO` - Get watchdog information
  - `WDIOC_GETTIMEOUT` - Get current timeout
  - `WDIOC_SETTIMEOUT` - Set timeout
  - `WDIOC_GETTIMELEFT` - Get time remaining
  - **`WDIOC_KEEPALIVE`** - Keep watchdog alive (the problematic one)
- Tests alternative keep-alive methods (write-based)
- Provides detailed error analysis with errno codes
- Checks kernel modules and system information

### 2. test-watchdog-local.sh

Local testing script that:

- Compiles the debug tool locally
- Verifies compilation works before AWS deployment
- Provides AWS deployment instructions

Usage:
```bash
./scripts/test-watchdog-local.sh
```

### 3. debug-watchdog-comparison.sh

Comprehensive AWS-based comparison script that:

- Creates both ARM64 (t4g.micro) and AMD64 (t3.micro) EC2 instances
- Deploys and compiles the debug tool on both architectures
- Runs watchdog tests on both instances
- Compares results to identify architecture-specific issues
- Automatically cleans up resources

Usage:
```bash
# Full comparison test
./scripts/debug-watchdog-comparison.sh

# With specific region and key
./scripts/debug-watchdog-comparison.sh -r us-east-1 -k my-key-pair

# Test existing instances only
./scripts/debug-watchdog-comparison.sh --test-only

# Cleanup resources only
./scripts/debug-watchdog-comparison.sh --cleanup-only
```

## Prerequisites for AWS Testing

1. **AWS CLI** configured with appropriate credentials
2. **EC2 Key Pair** created and SSH private key available at `~/.ssh/KEY_NAME.pem`
3. **AWS Permissions** for:
   - EC2 instance management
   - Security group creation/deletion
   - VPC operations

4. **Environment Variables** (optional):
   ```bash
   export AWS_REGION="us-west-2"
   export AWS_KEY_NAME="your-key-name"
   export AWS_PAGER=""
   ```

## Expected Results

Based on the ARM64 compatibility issue:

- **ARM64 Instance**: Should show `ENOTTY` error for `WDIOC_KEEPALIVE`
- **AMD64 Instance**: Should successfully execute `WDIOC_KEEPALIVE`
- **Alternative Methods**: Both should support write-based keep-alive

This will confirm that the issue is ARM64-specific and help identify potential workarounds.

## Technical Details

### WDIOC_KEEPALIVE ioctl

```c
#define WDIOC_KEEPALIVE   0x40045705  // _IOR(WATCHDOG_IOCTL_BASE, 5, int)
```

The `WDIOC_KEEPALIVE` ioctl is used to "pet" the watchdog, resetting its timer to prevent system reset. The issue appears to be that the ARM64 `softdog` driver doesn't properly implement this ioctl, returning `ENOTTY` instead of success.

### Alternative Keep-Alive Methods

Some watchdog devices support keep-alive via writing to the device file:

```bash
echo "1" > /dev/watchdog
```

The debug tool tests this alternative method to determine if it's a viable workaround.

## Integration with SBD Operator

The results of this debugging will inform potential fixes to the SBD operator:

1. **Architecture Detection**: Add ARM64-specific handling
2. **Alternative Methods**: Use write-based keep-alive on ARM64
3. **Kernel Module Options**: Try different watchdog drivers on ARM64
4. **Error Handling**: Improve error messages and recovery

## Safety Notes

⚠️ **WARNING**: Running watchdog tests can potentially cause system reboots if not handled carefully. The debug tool uses the "magic close" method (writing 'V' before closing) to disable the watchdog safely.

The AWS testing script runs on disposable EC2 instances specifically to avoid any impact on production systems.
