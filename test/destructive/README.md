# Destructive Node Reboot Tests

⚠️ **EXTREMELY DANGEROUS - THESE TESTS WILL REBOOT OPENSHIFT NODES** ⚠️

This directory contains standalone tests that validate node self-fencing mechanisms by **actually rebooting OpenShift nodes**. These tests should only be run in isolated test environments.

## ⚠️ CRITICAL SAFETY WARNINGS

- **THESE TESTS WILL REBOOT THE TARGET NODE IMMEDIATELY**
- **DO NOT RUN IN PRODUCTION ENVIRONMENTS**
- **SAVE ALL WORK BEFORE RUNNING**
- **ENSURE TARGET NODE CAN BE SAFELY REBOOTED**
- **HAVE MONITORING IN PLACE TO VERIFY REBOOTS**

## Test Overview

### 1. Panic Reboot Test (`panic-reboot-test.go`)
- **Purpose**: Validates that Go panic() can trigger node reboot
- **Method**: Calls `panic()` after a configurable delay
- **Expected Result**: Node reboots immediately when panic occurs
- **Use Case**: Tests panic-based self-fencing mechanism

### 2. Watchdog Reboot Test (`watchdog-reboot-test.go`)
- **Purpose**: Validates that watchdog timeout can trigger node reboot
- **Method**: Opens `/dev/watchdog`, pets it several times, then stops
- **Expected Result**: Node reboots when watchdog timeout expires
- **Use Case**: Tests kernel watchdog-based self-fencing mechanism

## Architecture

```
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│   UBI9 Image    │───▶│  Go Test Binary  │───▶│  Node Reboot    │
│                 │    │                  │    │                 │
│ registry.redhat.│    │ panic() or       │    │ Immediate       │
│ io/ubi9/ubi     │    │ watchdog timeout │    │ ungraceful      │
└─────────────────┘    └──────────────────┘    └─────────────────┘
```

## Prerequisites

### Environment Requirements
- **Isolated test environment** (not production!)
- OpenShift/Kubernetes cluster
- Cluster admin permissions
- Target worker node that can be safely rebooted
- Access to build and push container images

### Node Requirements
- `/dev/watchdog` device available (for watchdog test)
- Kernel watchdog functionality enabled
- Privileged containers allowed
- Node can be safely rebooted without data loss

## Setup Instructions

### 1. Build Test Image

```bash
# Build the UBI-based image with test binaries
docker build -t sbd-destructive-tests:latest -f test/destructive/Dockerfile .

# Tag for your registry
docker tag sbd-destructive-tests:latest your-registry.com/sbd-destructive-tests:latest

# Push to registry
docker push your-registry.com/sbd-destructive-tests:latest
```

### 2. Update Manifests

Edit the YAML files and update:
- `TARGET_NODE_NAME_HERE` → actual node name to test
- Image references to point to your registry

### 3. Verify Target Node

```bash
# Check if watchdog device exists on target node
oc debug node/TARGET_NODE_NAME -- chroot /host ls -la /dev/watchdog*

# Check node details
oc describe node TARGET_NODE_NAME

# Ensure node is Ready and schedulable
oc get nodes TARGET_NODE_NAME
```

## Running the Tests

### Option 1: Pre-built UBI Image (Simpler)

If you just want to test with basic binaries compiled on-the-fly:

```bash
# 1. Edit the manifests to set target node name
sed -i 's/TARGET_NODE_NAME_HERE/your-target-node/g' test/destructive/*.yaml

# 2. Apply the ConfigMaps for documentation
kubectl apply -f test/destructive/panic-reboot-test.yaml
kubectl apply -f test/destructive/watchdog-reboot-test.yaml
```

**Note**: The pre-built option uses basic UBI image and may need the Go binaries to be compiled differently.

### Option 2: Custom Built Image (Recommended)

```bash
# 1. Build and push your image (see Setup section)

# 2. Update image references in YAML files
sed -i 's|registry.redhat.io/ubi9/ubi:latest|your-registry.com/sbd-destructive-tests:latest|g' test/destructive/*.yaml

# 3. Set target node name
sed -i 's/TARGET_NODE_NAME_HERE/your-target-node/g' test/destructive/*.yaml

# 4. Run panic test
kubectl apply -f test/destructive/panic-reboot-test.yaml

# 5. Monitor (node will reboot during this)
kubectl logs -f panic-reboot-test

# 6. After node comes back, run watchdog test
kubectl apply -f test/destructive/watchdog-reboot-test.yaml
kubectl logs -f watchdog-reboot-test
```

## Monitoring and Verification

### Real-time Monitoring

```bash
# Monitor test pod logs
kubectl logs -f panic-reboot-test       # or watchdog-reboot-test

# Monitor node status (from another terminal)
watch -n 2 "kubectl get nodes TARGET_NODE_NAME"

# Monitor all events
kubectl get events --sort-by='.lastTimestamp' -w
```

### Expected Behavior

#### Panic Test Timeline:
1. **T+0s**: Pod starts, shows 30-second countdown
2. **T+30s**: Triggers `panic()`
3. **T+30s**: Node begins reboot process
4. **T+30-90s**: Node becomes `NotReady`
5. **T+2-5min**: Node reboots and becomes `Ready` again

#### Watchdog Test Timeline:
1. **T+0s**: Pod starts, shows 30-second countdown
2. **T+30s**: Opens `/dev/watchdog` (activates watchdog)
3. **T+30-60s**: Pets watchdog 10 times over 30 seconds
4. **T+60s**: Stops petting watchdog
5. **T+60-120s**: Watchdog timeout triggers reboot
6. **T+2-5min**: Node reboots and becomes `Ready` again

### Verification Commands

```bash
# Check node uptime after reboot
oc debug node/TARGET_NODE_NAME -- chroot /host uptime

# Check system logs for reboot cause
oc debug node/TARGET_NODE_NAME -- chroot /host journalctl -b -0 | grep -i "reboot\|panic\|watchdog"

# Check kernel messages
oc debug node/TARGET_NODE_NAME -- chroot /host dmesg | grep -i "reboot\|panic\|watchdog"
```

## Troubleshooting

### Common Issues

#### "Pod stays in Pending state"
- Check node selector matches actual node name
- Verify node is schedulable: `kubectl get nodes`
- Check tolerations if node has taints

#### "Permission denied accessing /dev/watchdog"
- Ensure `privileged: true` in securityContext
- Verify `hostPath` volume mounts are correct
- Check if watchdog device exists on host

#### "Node doesn't reboot"
- For panic test: Check if container has proper capabilities
- For watchdog test: Verify `/dev/watchdog` exists and is functional
- Check system logs: `dmesg | grep watchdog`
- Some virtualized environments may not support hardware watchdog

#### "Test binary not found"
- If using custom image, ensure build completed successfully
- Check image pull policy and registry access
- Verify Dockerfile builds binaries correctly

### Debug Commands

```bash
# Check if watchdog is available
oc debug node/TARGET_NODE_NAME -- chroot /host lsmod | grep watchdog

# Test watchdog manually
oc debug node/TARGET_NODE_NAME -- chroot /host timeout 5 sh -c 'echo 1 > /dev/watchdog'

# Check container image contents
kubectl run debug-image --rm -i --tty --image=your-registry.com/sbd-destructive-tests:latest -- /bin/bash
```

## Cleanup

```bash
# Delete test pods (may already be gone due to node reboot)
kubectl delete pod panic-reboot-test watchdog-reboot-test --ignore-not-found

# Delete ConfigMaps
kubectl delete configmap panic-test-instructions watchdog-test-instructions --ignore-not-found

# Clean up any failed pods
kubectl delete pods --field-selector=status.phase=Failed
```

## Security Considerations

- Tests require `privileged: true` containers
- Tests need access to host devices (`/dev/watchdog`)
- Tests run as root user (UID 0)
- Tests have `SYS_BOOT` and `SYS_ADMIN` capabilities
- **Never run these tests in production environments**

## Integration with SBD Operator

These tests validate the same self-fencing mechanisms used by the SBD operator:

- **Panic test** validates the `rebootMethod: panic` setting in SBD agent
- **Watchdog test** validates the watchdog petting and timeout logic
- Both tests confirm that node reboots work as expected for self-fencing

## Contributing

When modifying these tests:

1. Ensure all safety warnings remain prominent
2. Test in isolated environments only
3. Update documentation for any new parameters
4. Verify tests work with UBI9 images
5. Follow Go best practices for binary compilation

## Support

These are **destructive tests** intended for validation only. For production SBD operator issues, see the main project documentation.

**Remember: These tests WILL reboot nodes. Use with extreme caution!** 