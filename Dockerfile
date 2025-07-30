# Build the manager binary
# podman search registry.access.redhat.com/ubi9/go-toolset --list-tags --limit 200 
FROM registry.access.redhat.com/ubi9/go-toolset:latest AS builder
ARG TARGETOS
ARG TARGETARCH

# Build arguments for injecting version information
ARG GIT_COMMIT=unknown
ARG GIT_DESCRIBE=unknown
ARG BUILD_DATE=unknown

# Set GOTOOLCHAIN to auto to allow Go to download newer versions
# Set to local to avoid downloading newer versions of Go
ENV GOTOOLCHAIN=auto

WORKDIR /workspace
USER default

# Copy the Go Modules manifests
# COPY --chown=default go.mod go.sum ./
COPY go.mod go.sum ./

# Copy the go source
COPY cmd/main.go cmd/main.go
COPY api/ api/
COPY pkg/ pkg/
COPY vendor/ vendor/
RUN go version
# Build
# the GOARCH has not a default value to allow the binary be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
RUN CGO_ENABLED=${CGO_ENABLED:-0} GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build \
    -a -installsuffix cgo \
    -ldflags="-w -s -extldflags '-static' \
    -X 'github.com/medik8s/sbd-operator/pkg/version.GitCommit=${GIT_COMMIT}' \
    -X 'github.com/medik8s/sbd-operator/pkg/version.GitDescribe=${GIT_DESCRIBE}' \
    -X 'github.com/medik8s/sbd-operator/pkg/version.BuildDate=${BUILD_DATE}'" \
    -o ./manager \
    ./cmd/main.go

# Use UBI minimal as base image to package the manager binary
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
