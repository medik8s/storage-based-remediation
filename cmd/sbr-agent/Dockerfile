# ==============================================================================
# Build Stage - Compile the SBR Agent Go application
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
COPY hack/ hack/
COPY .git/ .git/

# Copy the entire source code
RUN ls -al
RUN go version

USER root
RUN mkdir -p bin && chown -R default:root bin
USER default

RUN git config --global --add safe.directory /workspace
RUN ./hack/build.sh -o bin/sbr-agent ./cmd/sbr-agent/main.go

# ==============================================================================
# Runtime Stage - Minimal container for running the SBR Agent
# ==============================================================================
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest

# Install runtime dependencies
# - ca-certificates: For HTTPS connections
# - util-linux: Provides device utilities that might be needed
RUN microdnf install -y ca-certificates util-linux util-linux-core && microdnf clean all

# Import the user and group files from the builder stage
# This ensures we have a non-root user available

# Create directories that the application might need
RUN mkdir -p /var/log/sbr-agent && \
    mkdir -p /etc/sbr-agent



# Copy the compiled binary from the builder stage
COPY --from=builder /workspace/bin/sbr-agent /usr/local/bin/sbr-agent

# Make the binary executable
RUN chmod +x /usr/local/bin/sbr-agent


# Set the working directory
WORKDIR /

# Expose any ports if needed (currently none required)
# EXPOSE 8080

# Add labels for better container management
LABEL maintainer="medik8s" \
      description="SBR Agent for hardware watchdog and SBR device management" \
      version="latest" \
      org.opencontainers.image.title="SBR Agent" \
      org.opencontainers.image.description="SBR Agent for Linux watchdog and SBR device management" \
      org.opencontainers.image.vendor="medik8s" \
      org.opencontainers.image.licenses="Apache-2.0"

# Define the entrypoint
# This is the command that will be executed when the container starts
ENTRYPOINT ["/usr/local/bin/sbr-agent"]

# Default command arguments
# These can be overridden when running the container
CMD ["--watchdog-path=/dev/watchdog", "--log-level=info"]

# ==============================================================================
# Container Usage Instructions
# ==============================================================================
# To build the image:
#   docker build -f cmd/sbr-agent/Dockerfile -t sbr-agent:latest .
#
# To run the container with required privileges:
#   docker run --privileged \
#              --cap-add=SYS_ADMIN \
#              --device=/dev/watchdog \
#              --volume=/dev:/dev \
#              --volume=/path/to/sbr/device:/dev/sbr \
#              sbr-agent:latest \
#              --sbr-device=/dev/sbr \
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
