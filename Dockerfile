# Build the manager binary
# podman search registry.access.redhat.com/ubi9/go-toolset --list-tags --limit 200 
FROM registry.access.redhat.com/ubi9/go-toolset:latest AS builder
ARG TARGETOS
ARG TARGETARCH

USER default

# Set GOTOOLCHAIN to auto to allow Go to download newer versions
# Set to local to avoid downloading newer versions of Go
ENV GOTOOLCHAIN=auto

WORKDIR /workspace

# Copy the Go Modules manifests
COPY go.mod go.sum ./

# Copy the go source and .git directory for version info
COPY cmd/main.go cmd/main.go
COPY api/ api/
COPY pkg/ pkg/
COPY vendor/ vendor/
COPY .git/ .git/

RUN go version

# Calculate version information from git
RUN export GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown") && \
    export GIT_DESCRIBE=$(git describe --tags --dirty 2>/dev/null || echo "unknown") && \
    export BUILD_DATE=$(date -u +"%Y-%m-%dT%H:%M:%SZ") && \
    echo "Building with:" && \
    echo "  GIT_COMMIT=$GIT_COMMIT" && \
    echo "  GIT_DESCRIBE=$GIT_DESCRIBE" && \
    echo "  BUILD_DATE=$BUILD_DATE" && \
    CGO_ENABLED=${CGO_ENABLED:-0} GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build \
        -a -installsuffix cgo \
        -ldflags="-w -s -extldflags '-static' \
        -X 'github.com/medik8s/sbd-operator/pkg/version.GitCommit=$GIT_COMMIT' \
        -X 'github.com/medik8s/sbd-operator/pkg/version.GitDescribe=$GIT_DESCRIBE' \
        -X 'github.com/medik8s/sbd-operator/pkg/version.BuildDate=$BUILD_DATE'" \
        -o ./manager \
        ./cmd/main.go

# Use UBI minimal as base image to package the manager binary
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
