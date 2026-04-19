# SBR Agent Docker Container

This document provides comprehensive instructions for building, deploying, and managing the SBR Agent Docker container.

## Overview

The SBR Agent is a containerized application that manages hardware watchdog devices and SBD (Storage-Based Death) devices for high-availability cluster systems. It provides:

- Hardware watchdog management and monitoring
- SBD device monitoring and fencing operations
- Cluster node health monitoring
- Automatic system reset capabilities

## Prerequisites

### System Requirements

- Linux system with hardware watchdog support
- Docker or container runtime (Podman, containerd)
- Access to `/dev/watchdog` device
- Shared block device for SBD operations
- Root privileges or appropriate capabilities

### Build Requirements

- Go 1.24 or later
- Docker or compatible container runtime
- Make (optional, for using Makefile targets)

## Building the Container

### Method 1: Using the Build Script (Recommended)

```bash
# Build the image
./scripts/build-sbr-agent.sh build

# Build with custom tag
./scripts/build-sbr-agent.sh -t v1.0.0 build

# Run all build and test steps
./scripts/build-sbr-agent.sh all
```

### Method 2: Using Docker Directly

```bash
# Build the image
docker build -f cmd/sbr-agent/Dockerfile -t sbr-agent:latest .

# Build with build arguments
docker build -f cmd/sbr-agent/Dockerfile \
  --build-arg GO_VERSION=1.24 \
  -t sbr-agent:latest .
```

## Running the Container

### Basic Usage

```bash
# Run with mock devices (for testing)
docker run --rm \
  --name sbr-agent \
  sbr-agent:latest \
  --watchdog-path=/tmp/mock/watchdog \
  --watchdog-timeout=30s \
  --log-level=info
```

### Production Usage

```bash
# Run with real hardware access
docker run -d \
  --name sbr-agent \
  --privileged \
  --cap-add=SYS_ADMIN \
  --cap-add=SYS_RAWIO \
  --device=/dev/watchdog \
  --volume=/dev:/dev \
  --volume=/path/to/sbd/device:/dev/sbd \
  --restart=unless-stopped \
  sbr-agent:latest \
  --watchdog-path=/dev/watchdog \
  --sbr-device=/dev/sbd \
  --watchdog-timeout=15s \
  --log-level=info
```

### Container Options

| Option | Description | Example |
|--------|-------------|---------|
| `--privileged` | Full container privileges | Required for device access |
| `--cap-add=SYS_ADMIN` | System administration capabilities | Required for watchdog |
| `--cap-add=SYS_RAWIO` | Raw I/O access | Required for block devices |
| `--device=/dev/watchdog` | Expose watchdog device | Map host device to container |
| `--volume=/dev:/dev` | Mount entire /dev directory | For comprehensive device access |
| `--restart=unless-stopped` | Restart policy | Keep container running |

## Command Line Arguments

| Argument | Description | Default | Example |
|----------|-------------|---------|---------|
| `--watchdog-path` | Path to watchdog device | `/dev/watchdog` | `--watchdog-path=/dev/watchdog0` |
| `--watchdog-timeout` | Pet interval for watchdog | `30s` | `--watchdog-timeout=15s` |
| `--sbr-device` | Path to SBD block device | (empty) | `--sbr-device=/dev/disk/by-id/sbr-device` |
| `--log-level` | Logging level | `info` | `--log-level=debug` |

## Kubernetes Deployment

### Using the DaemonSet

```bash
# Deploy the SBR Agent DaemonSet
kubectl apply -f deploy/sbr-agent-daemonset.yaml

# Check deployment status
kubectl get daemonset sbr-agent -n kube-system

# View logs
kubectl logs -f daemonset/sbr-agent -n kube-system
```

### Manual Pod Deployment

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: sbr-agent
  namespace: kube-system
spec:
  hostNetwork: true
  hostPID: true
  containers:
  - name: sbr-agent
    image: sbr-agent:latest
    securityContext:
      privileged: true
      runAsUser: 0
      capabilities:
        add:
        - SYS_ADMIN
        - SYS_RAWIO
    volumeMounts:
    - name: dev
      mountPath: /dev
    args:
    - "--watchdog-path=/dev/watchdog"
    - "--sbr-device=/dev/disk/by-id/sbr-device"
    - "--watchdog-timeout=30s"
  volumes:
  - name: dev
    hostPath:
      path: /dev
  nodeSelector:
    node-role.kubernetes.io/control-plane: ""
```

## Security Considerations

### Required Privileges

The SBR Agent requires elevated privileges to function:

- **Privileged mode**: For unrestricted device access
- **SYS_ADMIN capability**: For watchdog device operations
- **SYS_RAWIO capability**: For raw block device access
- **Root user**: For device file access permissions

### Security Best Practices

1. **Limit deployment scope**: Only deploy on nodes that require SBD functionality
2. **Use specific devices**: Mount only required devices instead of entire `/dev`
3. **Network isolation**: Use host network only when necessary
4. **Resource limits**: Set appropriate CPU and memory limits
5. **Log monitoring**: Monitor logs for security events

### Example Secure Configuration

```yaml
securityContext:
  privileged: true
  runAsUser: 0
  runAsNonRoot: false
  readOnlyRootFilesystem: true
  capabilities:
    add:
    - SYS_ADMIN
    - SYS_RAWIO
    drop:
    - ALL
resources:
  requests:
    memory: "64Mi"
    cpu: "100m"
  limits:
    memory: "128Mi"
    cpu: "200m"
```

## Monitoring and Logging

### Health Checks

The container includes health check endpoints:

```bash
# Check if watchdog device is accessible
docker exec sbr-agent test -e /dev/watchdog

# Check process status
docker exec sbr-agent ps aux | grep sbr-agent
```

### Log Analysis

```bash
# View container logs
docker logs sbr-agent

# Follow logs in real-time
docker logs -f sbr-agent

# View logs with timestamps
docker logs -t sbr-agent

# In Kubernetes
kubectl logs -f pod/sbr-agent-xxx -n kube-system
```

### Metrics and Monitoring

The SBR Agent can be integrated with monitoring systems:

- **Prometheus**: Expose metrics for watchdog pet status
- **Grafana**: Visualize SBD device health
- **AlertManager**: Alert on watchdog failures

## Troubleshooting

### Common Issues

#### 1. Permission Denied on Watchdog Device

```bash
# Symptom
Error: failed to open watchdog device: permission denied

# Solutions
- Ensure container runs with --privileged
- Check device permissions on host
- Verify CAP_SYS_ADMIN capability
```

#### 2. Device Not Found

```bash
# Symptom
Error: failed to open watchdog device: no such file or directory

# Solutions
- Check if watchdog device exists on host
- Verify device path in container arguments
- Ensure device is properly mounted
```

#### 3. IOCTL Operation Failed

```bash
# Symptom
Error: failed to pet watchdog: inappropriate ioctl for device

# Solutions
- Verify device is a real watchdog (not regular file)
- Check hardware watchdog driver is loaded
- Ensure device supports required ioctl operations
```

### Diagnostic Commands

```bash
# Check host watchdog devices
ls -la /dev/watchdog*

# Check kernel modules
lsmod | grep watchdog

# Check hardware watchdog info
dmesg | grep -i watchdog

# Test device accessibility
echo 'V' > /dev/watchdog  # Be careful - this may reset system!
```

## Development and Testing

### Building for Development

```bash
# Build development image
IMAGE_TAG=dev ./scripts/build-sbr-agent.sh build

# Run with debug logging
docker run --rm sbr-agent:dev \
  --log-level=debug \
  --watchdog-timeout=5s
```

### Testing with Mock Devices

```bash
# Create mock devices
mkdir -p /tmp/mock-devices
touch /tmp/mock-devices/watchdog
touch /tmp/mock-devices/sbd

# Run with mock devices
docker run --rm \
  -v /tmp/mock-devices:/tmp/mock \
  sbr-agent:latest \
  --watchdog-path=/tmp/mock/watchdog \
  --sbr-device=/tmp/mock/sbd
```

### Integration Testing

```bash
# Run automated tests
./scripts/build-sbr-agent.sh test

# Run comprehensive test suite
./scripts/build-sbr-agent.sh all
```

## Production Deployment

### High Availability Setup

For production HA clusters:

1. **Deploy on all control nodes**: Use DaemonSet with appropriate node selectors
2. **Configure SBD devices**: Ensure shared block devices are accessible
3. **Set appropriate timeouts**: Balance between responsiveness and stability
4. **Monitor continuously**: Implement comprehensive monitoring
5. **Test regularly**: Verify fencing operations work correctly

### Backup and Recovery

- **Container images**: Store in reliable container registry
- **Configuration**: Version control all deployment manifests
- **SBD devices**: Ensure block devices are properly backed up
- **Recovery procedures**: Document and test recovery processes

## Support and Maintenance

### Updates and Upgrades

```bash
# Update to new version
docker pull sbr-agent:v1.1.0
kubectl set image daemonset/sbr-agent sbr-agent=sbr-agent:v1.1.0 -n kube-system

# Rollback if needed
kubectl rollout undo daemonset/sbr-agent -n kube-system
```

### Log Rotation

Configure log rotation to prevent disk space issues:

```yaml
spec:
  containers:
  - name: sbr-agent
    env:
    - name: LOG_ROTATE_SIZE
      value: "100M"
    - name: LOG_ROTATE_COUNT
      value: "5"
```

## References

- [Linux Watchdog Documentation](https://www.kernel.org/doc/Documentation/watchdog/watchdog-api.txt)
- [SBD Fencing Documentation](https://clusterlabs.org/doc/)
- [Kubernetes Security Contexts](https://kubernetes.io/docs/tasks/configure-pod-container/security-context/)
- [Docker Security Best Practices](https://docs.docker.com/engine/security/) 
