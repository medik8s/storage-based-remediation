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
COPY hack/ hack/
COPY .git/ .git/

RUN go version

USER root
RUN mkdir -p bin && chown -R default:root bin
USER default

RUN git config --global --add safe.directory /workspace
RUN ./hack/build.sh -o bin/manager ./cmd/main.go

# Use UBI minimal as base image to package the manager binary
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest
WORKDIR /
COPY --from=builder /workspace/bin/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
