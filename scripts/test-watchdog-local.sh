#!/bin/bash

# Local watchdog test script - test the debug tool locally before AWS deployment

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
DEBUG_TOOL_SOURCE="${PROJECT_ROOT}/debug-watchdog-arm64.go"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log() {
    echo -e "${BLUE}[$(date +'%H:%M:%S')]${NC} $*"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $*"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $*"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $*"
}

test_local_watchdog() {
    log "Testing watchdog debug tool locally..."
    
    # Check if the debug tool exists
    if [[ ! -f "${DEBUG_TOOL_SOURCE}" ]]; then
        log_error "Debug tool source not found: ${DEBUG_TOOL_SOURCE}"
        exit 1
    fi
    
    # Create temp directory for testing
    local temp_dir="/tmp/sbd-watchdog-test-$$"
    mkdir -p "${temp_dir}"
    
    # Copy and compile the debug tool
    cp "${DEBUG_TOOL_SOURCE}" "${temp_dir}/debug-watchdog.go"
    cd "${temp_dir}"
    
    log "Compiling debug tool..."
    if go build -o debug-watchdog debug-watchdog.go; then
        log_success "Debug tool compiled successfully"
    else
        log_error "Failed to compile debug tool"
        exit 1
    fi
    
    # Run the debug tool (this will likely fail on macOS, but will show compilation works)
    log "Running debug tool (may fail on macOS - this is expected)..."
    echo ""
    echo "========================================="
    echo "         DEBUG TOOL OUTPUT"
    echo "========================================="
    
    if ./debug-watchdog 2>&1; then
        log_success "Debug tool executed successfully"
    else
        log_warning "Debug tool execution failed (expected on macOS/non-Linux systems)"
    fi
    
    # Cleanup
    cd - > /dev/null
    rm -rf "${temp_dir}"
    
    echo ""
    echo "========================================="
    log_success "Local test completed"
    echo "========================================="
}

show_aws_instructions() {
    cat << 'INSTRUCTIONS'

========================================
       AWS COMPARISON INSTRUCTIONS
========================================

To run the full ARM64 vs AMD64 comparison on AWS:

1. Prerequisites:
   - AWS CLI configured with appropriate credentials
   - EC2 key pair created and SSH key available at ~/.ssh/KEY_NAME.pem  
   - Permissions for EC2 instance management, security groups, etc.

2. Set environment variables (optional):
   export AWS_REGION="us-west-2"          # Your preferred region
   export AWS_KEY_NAME="your-key-name"    # Your EC2 key pair name

3. Run the comparison:
   ./scripts/debug-watchdog-comparison.sh

4. Or with specific options:
   ./scripts/debug-watchdog-comparison.sh -r us-east-1 -k my-key-pair

5. Cleanup only:
   ./scripts/debug-watchdog-comparison.sh --cleanup-only

The script will:
- Create ARM64 and AMD64 EC2 instances
- Deploy and compile the debug tool on both
- Run watchdog tests on both architectures
- Compare results and identify ARM64-specific issues
- Clean up resources automatically

Expected outcome:
- ARM64 instance should show ENOTTY error for WDIOC_KEEPALIVE
- AMD64 instance should work correctly
- This will confirm the ARM64 softdog compatibility issue

INSTRUCTIONS
}

main() {
    echo "=== SBD Watchdog Debug Tool - Local Test ==="
    echo ""
    
    test_local_watchdog
    show_aws_instructions
}

main "$@"
