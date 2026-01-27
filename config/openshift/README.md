# OpenShift SecurityContextConstraints for SBD Operator

This directory contains OpenShift-specific resources required for the SBD Operator to function properly on OpenShift clusters.

## SecurityContextConstraints (SCC)

The SBD Agent requires privileged access to hardware watchdog devices and block devices. The `sbd-agent-scc.yaml` file defines a custom SecurityContextConstraints that allows the SBD Agent pods to:

- Run with privileged containers
- Access host devices (`/dev`, `/sys`, `/proc`)
- Use the `SYS_ADMIN` capability
- Run as any user (including root when needed)

## Required Permissions

The SCC grants the following permissions:

- `allowPrivilegedContainer: true` - Required for hardware watchdog access
- `allowHostDirVolumePlugin: true` - Required to mount host directories like `/dev`
- `allowHostNetwork: true` - Required for network access
- `allowHostPID: true` - Required for process management
- `allowedCapabilities: [SYS_ADMIN]` - Required for device management

## Installation

### Option 1: Using the OpenShift Installer

```bash
make build-openshift-installer
kubectl apply -f dist/install-openshift.yaml
```

### Option 2: Manual Installation

```bash
kubectl apply -f config/openshift/
```

### Option 3: Using Kustomize

```bash
kubectl apply -k config/openshift-default/
```

## Service Account Binding

The SCC is automatically bound to the `sbd-agent` service account in the `sbd-system` namespace through:

1. A ClusterRole (`sbd-agent-scc-user`) that grants permission to use the SCC
2. A ClusterRoleBinding that binds the service account to the ClusterRole
3. Direct user reference in the SCC (`system:serviceaccount:sbd-system:sbd-agent`)

## Security Considerations

The SBD Agent requires these elevated privileges because it needs to:

- Access hardware watchdog devices (`/dev/watchdog*`)
- Read/write SBD (STONITH Block Device) devices
- Monitor system health and perform emergency reboots
- Interact with low-level system components

These permissions are necessary for the SBD Agent to function as a cluster fencing mechanism in high-availability environments.

## Troubleshooting

If SBD Agent pods fail to start with permission errors:

1. Verify the SCC is created:

   ```bash
   oc get scc sbd-agent-privileged
   ```

2. Check if the service account can use the SCC:

   ```bash
   oc adm policy who-can use scc sbd-agent-privileged
   ```

3. Verify the service account has the SCC assigned:

   ```bash
   oc describe scc sbd-agent-privileged
   ```

4. Check pod security context:

   ```bash
   kubectl describe pod <sbd-agent-pod-name> -n sbd-system
   ```
