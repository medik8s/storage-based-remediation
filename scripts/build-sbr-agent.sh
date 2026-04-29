#!/bin/bash

# Build script for SBR Agent container
# This script builds the SBR Agent Docker image and provides testing options

set -e

# Configuration
IMAGE_NAME="sbr-agent"
IMAGE_TAG=${IMAGE_TAG:-"latest"}
DOCKERFILE="Dockerfile.sbr-agent"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Functions
log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Function to check prerequisites
check_prerequisites() {
    log_info "Checking prerequisites..."
    
    if ! command -v docker &> /dev/null; then
        log_error "Docker is not installed or not in PATH"
        exit 1
    fi
    
    if ! command -v go &> /dev/null; then
        log_error "Go is not installed or not in PATH"
        exit 1
    fi
    
    log_success "Prerequisites check passed"
}

# Function to test Go compilation
test_go_build() {
    log_info "Testing Go compilation..."
    
    if go build -o /tmp/sbr-agent-test ./cmd/sbr-agent/main.go; then
        rm -f /tmp/sbr-agent-test
        log_success "Go compilation successful"
    else
        log_error "Go compilation failed"
        exit 1
    fi
}

# Function to build Docker image
build_image() {
    log_info "Building Docker image: ${IMAGE_NAME}:${IMAGE_TAG}"
    
    if docker build -f ${DOCKERFILE} -t ${IMAGE_NAME}:${IMAGE_TAG} .; then
        log_success "Docker image built successfully"
    else
        log_error "Docker build failed"
        exit 1
    fi
}

# Function to test the built image
test_image() {
    log_info "Testing Docker image..."
    
    # Test that the image runs without error (will fail on pet but that's expected)
    if docker run --rm ${IMAGE_NAME}:${IMAGE_TAG} --help > /dev/null 2>&1; then
        log_success "Docker image test passed"
    else
        log_warning "Docker image help test failed (this might be expected if --help is not implemented)"
    fi
    
    # Check image size
    local image_size=$(docker images ${IMAGE_NAME}:${IMAGE_TAG} --format "table {{.Size}}" | tail -n 1)
    log_info "Image size: ${image_size}"
}

# Function to show image information
show_image_info() {
    log_info "Image information:"
    docker images ${IMAGE_NAME}:${IMAGE_TAG}
    
    log_info "Image layers:"
    docker history ${IMAGE_NAME}:${IMAGE_TAG}
}

# Function to run the container for testing (with mock devices)
test_run() {
    log_info "Running test container..."
    
    # Create mock devices for testing
    local temp_dir=$(mktemp -d)
    touch "${temp_dir}/watchdog"
    touch "${temp_dir}/sbr"
    
    log_info "Starting container with mock devices..."
    log_warning "This will run for 10 seconds then stop automatically"
    
    # Run container with timeout
    timeout 10s docker run --rm \
        --name sbr-agent-test \
        -v "${temp_dir}:/tmp/mock" \
        ${IMAGE_NAME}:${IMAGE_TAG} \
        --watchdog-path=/tmp/mock/watchdog \
        --sbr-device=/tmp/mock/sbr \
        --log-level=info || true
    
    # Cleanup
    rm -rf "${temp_dir}"
    log_success "Test run completed"
}

# Function to show usage
show_usage() {
    echo "Usage: $0 [OPTIONS] [COMMAND]"
    echo ""
    echo "Commands:"
    echo "  build     Build the Docker image (default)"
    echo "  test      Test the built image"
    echo "  run       Run a test container"
    echo "  info      Show image information"
    echo "  clean     Remove built images"
    echo "  all       Run all steps (build, test, info)"
    echo ""
    echo "Options:"
    echo "  -t, --tag TAG     Image tag (default: latest)"
    echo "  -h, --help        Show this help message"
    echo ""
    echo "Environment variables:"
    echo "  IMAGE_TAG         Override the image tag"
    echo ""
    echo "Examples:"
    echo "  $0 build"
    echo "  $0 -t v1.0.0 build"
    echo "  $0 all"
    echo "  IMAGE_TAG=dev $0 build"
}

# Function to clean up images
clean_images() {
    log_info "Cleaning up images..."
    
    if docker images -q ${IMAGE_NAME} | xargs -r docker rmi; then
        log_success "Images cleaned up"
    else
        log_warning "No images to clean or cleanup failed"
    fi
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -t|--tag)
            IMAGE_TAG="$2"
            shift 2
            ;;
        -h|--help)
            show_usage
            exit 0
            ;;
        build|test|run|info|clean|all)
            COMMAND="$1"
            shift
            ;;
        *)
            log_error "Unknown option: $1"
            show_usage
            exit 1
            ;;
    esac
done

# Default command
COMMAND=${COMMAND:-"build"}

# Main execution
log_info "SBR Agent Build Script"
log_info "Image: ${IMAGE_NAME}:${IMAGE_TAG}"
log_info "Command: ${COMMAND}"
echo ""

case ${COMMAND} in
    build)
        check_prerequisites
        test_go_build
        build_image
        ;;
    test)
        test_image
        ;;
    run)
        test_run
        ;;
    info)
        show_image_info
        ;;
    clean)
        clean_images
        ;;
    all)
        check_prerequisites
        test_go_build
        build_image
        test_image
        show_image_info
        ;;
    *)
        log_error "Unknown command: ${COMMAND}"
        show_usage
        exit 1
        ;;
esac

log_success "Script completed successfully!" 
