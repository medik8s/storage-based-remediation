# SBR Operator Debugging Scripts

This directory contains useful scripts for debugging and troubleshooting the SBR Operator and its agents.

## Scripts Overview

### 🔍 `list-agent-pods.sh` - Agent Pod Overview
Lists all SBR agent pods across OpenShift nodes with their status.

**Use this when you want to:**
- Get an overview of SBR agent deployment across the cluster
- Check which nodes have agents running
- See pod status at a glance
- Identify problematic pods before digging into logs

**Example output:**
```bash
$ ./list-agent-pods.sh
[2025-01-24 10:30:15] SBR Agent Pod Listing Tool
[2025-01-24 10:30:15] Auto-detected SBR namespace: sbr-system
[2025-01-24 10:30:15] SBR Agent Pods in namespace: sbr-system
==============================================

POD NAME                                 NODE NAME                      STATUS         
---------------------------------------- ------------------------------ ---------------
sbr-agent-test-config-abc123            worker-1.example.com           Running        
sbr-agent-test-config-def456            worker-2.example.com           Running        
sbr-agent-test-config-ghi789            master-1.example.com           Running        

[2025-01-24 10:30:15] Summary: 3 total pods, 3 running, 0 pending, 0 failed
```

### 📋 `get-agent-logs.sh` - Node-Specific Logs
Retrieves logs from the SBR agent pod running on a specific OpenShift node.

**Use this when you want to:**
- Debug issues on a specific node
- Follow real-time logs from an agent
- Get previous logs after a pod restart
- Troubleshoot node-specific SBR problems

**Example output:**
```bash
$ ./get-agent-logs.sh worker-1.example.com --tail 50
[2025-01-24 10:31:22] SBR Agent Log Retrieval Tool
[2025-01-24 10:31:22] Node: worker-1.example.com
[2025-01-24 10:31:22] Auto-detected SBR namespace: sbr-system
[2025-01-24 10:31:22] Found SBR agent pod: sbr-agent-test-config-abc123 (status: Running)
[2025-01-24 10:31:22] Retrieving logs from SBR agent pod 'sbr-agent-test-config-abc123'...
==============================================
```

### 🗺️ `show-node-map.sh` - SBR Agent Node Mapping
Displays the SBR agent node mapping showing which nodes are assigned to which slots by reading from running SBR agent pods.

**Use this when you want to:**
- View current node-to-slot assignments in the SBR device
- Check which nodes are active in the cluster
- Debug slot assignment conflicts or issues
- Monitor node heartbeat status
- Understand the SBR agent coordination structure

**Key Features:**
- Reads directly from SBR agent pods via Kubernetes API
- Shows hash-based slot assignments for each node
- Displays last seen timestamps for cluster health
- Optional real-time heartbeat status from SBR device
- Supports JSON output for automation
- Color-coded status indicators (OK/STALE/OFFLINE)
- Auto-detects SBR namespace and running pods

**Example output:**
```bash
$ ./show-node-map.sh --heartbeats
═══════════════════════════════════════════════════════════════
                     SBR Agent Node Mapping
═══════════════════════════════════════════════════════════════

Cluster Name: my-openshift-cluster
Version: 15
Last Update: 2m ago
Access Mode: Kubernetes (namespace: sbr-system)
SBR Device: /sbr-shared/sbr-device
Total Nodes: 3

SLOT NODE NAME                     HASH       LAST SEEN      HEARTBEAT    STATUS
────────────────────────────────────────────────────────────────────────────────────────
2    worker-1.example.com          3284157891 1m ago         ACTIVE       OK    
5    worker-2.example.com          1756284092 45s ago        ACTIVE       OK    
8    master-1.example.com          2947382845 30s ago        ACTIVE       OK    
```

### ⚠️ `emergency-reboot-node.sh` - Emergency Node Reboot
Immediately and ungracefully reboots a specified OpenShift node using `oc debug`.

**⚠️ DANGER: This script performs IMMEDIATE, UNGRACEFUL node reboots! Use with extreme caution!**

**Use this when you want to:**
- Emergency fence/reboot an unresponsive node
- Simulate SBR fencing behavior for testing
- Force reboot a node when normal remediation has failed
- Test cluster resilience to sudden node failures

**Key Features:**
- Uses `oc debug node/<name>` with `nsenter` to access the host
- Executes `systemctl reboot --force --force` for immediate reboot
- Includes comprehensive safety checks and confirmations
- Supports dry-run mode for testing
- Warns about control plane node reboots
- Requires explicit "YES" confirmation (unless forced)

**Example usage:**
```bash
# Interactive reboot with confirmation
$ ./emergency-reboot-node.sh worker-node-1
[INFO] Starting emergency-reboot-node.sh v1.0.0
[INFO] Target node: worker-node-1
[WARN] ⚠️  DANGER ZONE  ⚠️
Are you absolutely sure you want to reboot 'worker-node-1'? (type 'YES' to confirm): YES
[WARN] Executing EMERGENCY REBOOT on node 'worker-node-1'...
[SUCCESS] Emergency reboot initiated on node 'worker-node-1'

# Force reboot without confirmation (dangerous!)
$ ./emergency-reboot-node.sh --force worker-node-1

# Dry run to see what would happen
$ ./emergency-reboot-node.sh --dry-run worker-node-1
[INFO] DRY RUN: Would execute emergency reboot on node 'worker-node-1'
[INFO] DRY RUN: Command that would be executed:
  oc debug node/worker-node-1 -- nsenter -t 1 -m -u -i -n -p -- systemctl reboot --force --force
```

**Safety Features:**
- ✅ **Node validation:** Checks if node exists before proceeding
- ✅ **Control plane warnings:** Special warnings for master nodes
- ✅ **Confirmation prompt:** Requires typing "YES" to confirm
- ✅ **Dry-run mode:** Test what would happen without executing
- ✅ **Force mode:** Skip confirmation for automated scenarios
- ✅ **Comprehensive logging:** Detailed status and error messages

## Quick Start

### 1. Get an overview of all agent pods
```bash
./scripts/list-agent-pods.sh
```

### 2. Look at logs from a specific node
```bash
./scripts/get-agent-logs.sh <node-name>
```

### 3. Follow logs in real-time
```bash
./scripts/get-agent-logs.sh <node-name> --follow
```

### 4. Display SBR agent node mapping
```bash
# Show basic node-to-slot mapping
./scripts/show-node-map.sh

# Show mapping with current heartbeat status
./scripts/show-node-map.sh --heartbeats

# Use specific namespace
./scripts/show-node-map.sh --namespace sbr-system

# JSON output for automation
./scripts/show-node-map.sh --json
```

### 5. Emergency node reboot (use with extreme caution!)
```bash
# Dry run first to see what would happen
./scripts/emergency-reboot-node.sh --dry-run <node-name>

# Interactive reboot with confirmation
./scripts/emergency-reboot-node.sh <node-name>
```

## Common Usage Patterns

### Basic Troubleshooting Workflow
1. **Check overall status:** `./list-agent-pods.sh`
2. **View node coordination:** `./show-node-map.sh --kubernetes`
3. **Identify problem nodes:** Look for non-Running status or STALE/OFFLINE nodes
4. **Get detailed logs:** `./get-agent-logs.sh <problematic-node>`
5. **Follow real-time:** `./get-agent-logs.sh <node> --follow --tail 100`

### SBR Coordination Debugging Workflow
1. **Check node mapping:** `./show-node-map.sh --kubernetes --verbose`
2. **Monitor heartbeats:** `./show-node-map.sh --kubernetes --heartbeats`
3. **Check for slot conflicts:** Look for hash collisions or duplicate assignments
4. **Verify agent logs:** `./get-agent-logs.sh <node> --tail 200`
5. **JSON analysis:** `./show-node-map.sh --kubernetes --json | jq '.entries'`

### Emergency Node Remediation Workflow
1. **Test first:** `./emergency-reboot-node.sh --dry-run <unresponsive-node>`
2. **Verify node status:** `oc get node <node> -o wide`
3. **Emergency reboot:** `./emergency-reboot-node.sh <unresponsive-node>`
4. **Monitor recovery:** `oc get node <node> -w`

### Debugging Agent Startup Issues
```bash
# Check if agents are starting on all nodes
./list-agent-pods.sh --details

# Get startup logs from a specific node
./get-agent-logs.sh worker-1 --tail 200

# Follow logs during agent restart
oc delete pod sbr-agent-xxx -n sbr-system
./get-agent-logs.sh worker-1 --follow
```

### Investigating Pod Restarts
```bash
# Check restart counts
./list-agent-pods.sh --details

# Get logs from previous container instance
./get-agent-logs.sh worker-1 --previous

# Compare with current logs
./get-agent-logs.sh worker-1 --tail 50
```

### Cluster-Wide Analysis
```bash
# Get all pods in JSON format for analysis
./list-agent-pods.sh --output json > agent-pods.json

# Check specific namespace
./list-agent-pods.sh -n my-sbr-namespace
```

## Script Features

### Auto-Detection
Both scripts automatically detect:
- ✅ **Namespace:** Finds SBR agents in common namespaces
- ✅ **Command:** Uses `oc` if available, falls back to `kubectl`
- ✅ **Cluster:** Validates connection before proceeding

### Environment Variables
Set these for easier usage:
- `SBR_NAMESPACE`: Default namespace for SBR agents
- `KUBECONFIG`: Path to kubeconfig file

### Error Handling
Scripts provide clear error messages for:
- Missing dependencies (`oc`/`kubectl`)
- No cluster connectivity
- Node not found
- No agent pods found
- Permission issues

## Dependencies

### For monitoring/debugging scripts (`list-agent-pods.sh`, `get-agent-logs.sh`):
- ✅ OpenShift CLI (`oc`) or Kubernetes CLI (`kubectl`)
- ✅ Access to OpenShift/Kubernetes cluster
- ✅ Read permissions for pods and logs in SBR namespace

### For node mapping script (`show-node-map.sh`):
- ✅ OpenShift CLI (`oc`) or Kubernetes CLI (`kubectl`)
- ✅ KUBECONFIG configured for target cluster
- ✅ Read permissions for pods in SBR namespace
- ✅ `jq` command for JSON parsing
- ✅ Optional: `hexdump` for SBR device heartbeat analysis
- ✅ Optional: `date` command for timestamp formatting

### For emergency reboot script (`emergency-reboot-node.sh`):
- ✅ OpenShift CLI (`oc`) or Kubernetes CLI (`kubectl`)
- ✅ AWS CLI (`aws`) installed and configured
- ✅ AWS credentials with EC2 permissions (ec2:RebootInstances)
- ✅ Access to AWS-hosted OpenShift/Kubernetes cluster
- ⚠️ **Warning:** This script causes immediate ungraceful reboots and can cause cluster disruption!

## Troubleshooting the Scripts

### "No SBR agent pods found"
- Check if SBR operator is deployed: `oc get pods -A | grep sbr`
- Verify namespace: `oc get pods -n <sbr-namespace>`
- Check labels: `oc get pods -l app=sbr-agent -A`

### "Node not found"
- List available nodes: `oc get nodes`
- Use exact node name (case-sensitive)
- Check node labels: `oc get node <node-name> --show-labels`

### "Cannot connect to cluster"
- Check kubeconfig: `oc cluster-info`
- Verify login: `oc whoami`
- Test connectivity: `oc get nodes`

### "Node mapping file not found"
- Ensure SBR agent is running: `./list-agent-pods.sh`
- Check if SBR agent pods have the shared storage mounted
- Verify SBR agent has created the mapping: `oc exec <pod> -- ls -la /sbr-shared/`
- Check pod volume mounts: `oc describe pod <sbr-agent-pod>`

### "Emergency reboot failed"
- Check permissions: `oc auth can-i debug node`
- Verify node accessibility: `oc debug node/<node-name> -- echo "test"`
- Check node status: `oc get node <node-name> -o wide`
- Review script logs for specific error messages
- Ensure sufficient privileges for `systemctl reboot` operations

## Advanced Usage

### Custom Output Formats
```bash
# Get pod details in wide format
./list-agent-pods.sh --output wide --details

# Export agent status as YAML
./list-agent-pods.sh --output yaml > agent-status.yaml
```

### Log Analysis
```bash
# Get last hour of logs
./get-agent-logs.sh worker-1 --since 1h

# Save logs to file
./get-agent-logs.sh worker-1 > agent-logs.txt

# Monitor logs without timestamps (cleaner output)
./get-agent-logs.sh worker-1 --follow --no-timestamps
```

### Multiple Nodes
```bash
# Get logs from all nodes (example)
for node in $(oc get nodes --no-headers -o custom-columns=NAME:.metadata.name); do
    echo "=== Logs from $node ==="
    ./get-agent-logs.sh "$node" --tail 10
done
```

## Contributing

When adding new debugging scripts:
1. Follow the same patterns for argument parsing and error handling
2. Add comprehensive help messages with examples
3. Support both `oc` and `kubectl` commands
4. Include namespace auto-detection
5. Add documentation to this README

## See Also

- [SBR Operator Documentation](../docs/)
- [OpenShift CLI Reference](https://docs.openshift.com/container-platform/latest/cli_reference/openshift_cli/getting-started-cli.html)
- [Kubernetes Debugging Guide](https://kubernetes.io/docs/tasks/debug-application-cluster/) 