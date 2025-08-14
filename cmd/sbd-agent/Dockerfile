# ==============================================================================
# Build Stage - Compile the SBD Agent Go application
# ==============================================================================
FROM registry.access.redhat.com/ubi9/go-toolset:latest AS builder

ARG TARGETOS
ARG TARGETARCH

# Set GOTOOLCHAIN to auto to allow Go to download newer versions
ENV GOTOOLCHAIN=auto

# Set the working directory inside the container
WORKDIR /workspace

# Copy go.mod and go.sum files for dependency caching
# This allows Docker to cache the dependency download step
COPY go.mod go.sum ./
COPY api/ api/
COPY cmd/ cmd/
COPY pkg/ pkg/
COPY vendor/ vendor/
COPY .git/ .git/

# Copy the entire source code
RUN ls -al
RUN go version

USER root
RUN mkdir -p bin && chown -R default:root bin
USER default

# Calculate version information from git and build the SBD Agent binary
# CGO_ENABLED=0: Disable CGO for a static binary
# GOOS=linux: Target Linux OS
# GOARCH=amd64: Target x86_64 architecture
# -a: Force rebuilding of packages
# -installsuffix cgo: Add suffix for CGO-disabled builds
# -ldflags: Linker flags to reduce binary size and remove debug info
RUN export GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown") && \
    export GIT_DESCRIBE=$(git describe --tags --dirty 2>/dev/null || echo "unknown") && \
    export BUILD_DATE=$(date -u +"%Y-%m-%dT%H:%M:%SZ") && \
    echo "Building SBD Agent with:" && \
    echo "  GIT_COMMIT=$GIT_COMMIT" && \
    echo "  GIT_DESCRIBE=$GIT_DESCRIBE" && \
    echo "  BUILD_DATE=$BUILD_DATE" && \
    CGO_ENABLED=${CGO_ENABLED:-0} GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build \
        -a -installsuffix cgo \
        -ldflags="-w -s -extldflags '-static' \
        -X 'github.com/medik8s/sbd-operator/pkg/version.GitCommit=$GIT_COMMIT' \
        -X 'github.com/medik8s/sbd-operator/pkg/version.GitDescribe=$GIT_DESCRIBE' \
        -X 'github.com/medik8s/sbd-operator/pkg/version.BuildDate=$BUILD_DATE'" \
        -o bin/sbd-agent \
        ./cmd/sbd-agent/main.go

# ==============================================================================
# Runtime Stage - Minimal container for running the SBD Agent
# ==============================================================================
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest

# Install runtime dependencies
# - ca-certificates: For HTTPS connections
# - util-linux: Provides device utilities that might be needed
RUN microdnf install -y ca-certificates util-linux util-linux-core && microdnf clean all

# Import the user and group files from the builder stage
# This ensures we have a non-root user available

# Create directories that the application might need
RUN mkdir -p /var/log/sbd-agent && \
    mkdir -p /etc/sbd-agent



# Copy the compiled binary from the builder stage
COPY --from=builder /workspace/bin/sbd-agent /usr/local/bin/sbd-agent

# Make the binary executable
RUN chmod +x /usr/local/bin/sbd-agent


# Set the working directory
WORKDIR /

# Expose any ports if needed (currently none required)
# EXPOSE 8080

# Add labels for better container management
LABEL maintainer="medik8s" \
      description="SBD Agent for hardware watchdog and SBD device management" \
      version="latest" \
      org.opencontainers.image.title="SBD Agent" \
      org.opencontainers.image.description="SBD Agent for Linux watchdog and SBD device management" \
      org.opencontainers.image.vendor="medik8s" \
      org.opencontainers.image.licenses="Apache-2.0"

# Define the entrypoint
# This is the command that will be executed when the container starts
ENTRYPOINT ["/usr/local/bin/sbd-agent"]

# Default command arguments
# These can be overridden when running the container
CMD ["--watchdog-path=/dev/watchdog", "--watchdog-timeout=30s", "--log-level=info"]

# ==============================================================================
# Container Usage Instructions
# ==============================================================================
# To build the image:
#   docker build -f cmd/sbd-agent/Dockerfile -t sbd-agent:latest .
#
# To run the container with required privileges:
#   docker run --privileged \
#              --cap-add=SYS_ADMIN \
#              --device=/dev/watchdog \
#              --volume=/dev:/dev \
#              --volume=/path/to/sbd/device:/dev/sbd \
#              sbd-agent:latest \
#              --sbd-device=/dev/sbd \
#              --watchdog-timeout=15s
#
# For Kubernetes deployment, ensure the pod has:
#   securityContext:
#     privileged: true
#     capabilities:
#       add:
#         - SYS_ADMIN
#   volumeMounts:
#     - name: dev
#       mountPath: /dev
#     - name: watchdog
#       mountPath: /dev/watchdog
# ============================================================================== 
