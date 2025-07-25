# Use UBI9 as base image for building Go binaries
FROM registry.redhat.io/ubi9/ubi:latest AS builder

# Install Go compiler
RUN dnf install -y golang git

# Set working directory
WORKDIR /app

# Copy Go modules files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the test binaries
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o panic-reboot-test ./test/destructive/panic-reboot-test.go
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o watchdog-reboot-test ./test/destructive/watchdog-reboot-test.go

# Use UBI9 minimal for runtime
FROM registry.redhat.io/ubi9/ubi-minimal:latest

# Install required packages for debugging
RUN microdnf install -y util-linux procps-ng && microdnf clean all

# Copy the binaries from builder
COPY --from=builder /app/panic-reboot-test /usr/local/bin/
COPY --from=builder /app/watchdog-reboot-test /usr/local/bin/

# Create a non-root user (though tests will need to run as root for reboot access)
RUN groupadd -g 1001 testuser && useradd -r -u 1001 -g testuser testuser

# Default command
CMD ["/bin/bash"] 