# OpenShift SecurityContextConstraints for SBR Operator

This directory contains OpenShift-specific resources required for the SBR Operator to function properly on OpenShift clusters.

## SecurityContextConstraints (SCC)

The SBR Agent requires privileged access to hardware watchdog devices and block devices. The `sbr-agent-scc.yaml` file defines a custom SecurityContextConstraints that allows the SBR Agent pods to:

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

The SCC is automatically bound to the `sbr-agent` service account in the `sbr-system` namespace through:
1. A ClusterRole (`sbr-agent-scc-user`) that grants permission to use the SCC
2. A ClusterRoleBinding that binds the service account to the ClusterRole
3. Direct user reference in the SCC (`system:serviceaccount:sbr-system:sbr-agent`)

## Security Considerations

The SBR Agent requires these elevated privileges because it needs to:
- Access hardware watchdog devices (`/dev/watchdog*`)
- Read/write SBR (Storage Based Remediation) block devices
- Monitor system health and perform emergency reboots
- Interact with low-level system components

These permissions are necessary for the SBR Agent to function as a cluster fencing mechanism in high-availability environments.

## Troubleshooting

If SBR Agent pods fail to start with permission errors:

1. Verify the SCC is created:
   ```bash
   oc get scc sbr-agent-privileged
   ```

2. Check if the service account can use the SCC:
   ```bash
   oc adm policy who-can use scc sbr-agent-privileged
   ```

3. Verify the service account has the SCC assigned:
   ```bash
   oc describe scc sbr-agent-privileged
   ```

4. Check pod security context:
   ```bash
   kubectl describe pod <sbr-agent-pod-name> -n sbr-system
   ```
