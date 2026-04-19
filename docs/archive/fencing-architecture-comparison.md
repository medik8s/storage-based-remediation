# SBD Fencing Architecture Comparison

## Overview

This document compares two architectural approaches for SBD (STONITH Block Device) fencing in the sbd-operator when the remediation controller needs to access shared storage devices.

## The Problem

The SBD remediation controller cannot directly access the shared storage that SBD agents use because:
- **SBD Agents** (DaemonSet pods) have shared storage PVCs mounted at `/sbd-shared/sbd-device`
- **SBD Remediation Controller** (Manager pod) has no PVC mounts and cannot access the shared storage

## Architecture Options

### Option 1: Agent-Based Fencing

**Concept**: SBD agents detect and process fencing requests themselves, while the controller only manages StorageBasedRemediation CRs.

```
┌─────────────────┐    ┌─────────────────┐
│   Controller    │    │  SBDRemediation │
│                 │───▶│       CR        │
│ - Validate CR   │    │  - NodeName     │
│ - Update Status │    │  - Reason       │
│ - Emit Events   │    │  - Status       │
└─────────────────┘    └─────────────────┘
                                │
                                │ Watch
                                ▼
                       ┌─────────────────┐
                       │   SBD Agents    │
                       │                 │
                       │ - Watch SBDRem  │
                       │ - Write to SBD  │
                       │ - Update Status │
                       │ - File Locking  │
                       └─────────────────┘
                                │
                                ▼
                       ┌─────────────────┐
                       │ Shared Storage  │
                       │   SBD Device    │
                       └─────────────────┘
```

### Option 2: Dedicated Fencing Pods

**Concept**: Controller creates temporary pods with PVC access for each fencing operation.

```
┌─────────────────┐    ┌─────────────────┐
│   Controller    │    │  SBDRemediation │
│                 │───▶│       CR        │
│ - Create Job    │    │  - NodeName     │
│ - Monitor Job   │    │  - Reason       │
│ - Update Status │    │  - Status       │
└─────────────────┘    └─────────────────┘
          │
          │ Creates
          ▼
┌─────────────────┐
│  Fencing Job    │
│                 │
│ - Mount PVC     │
│ - Write to SBD  │
│ - Report Status │
│ - Self-cleanup  │
└─────────────────┘
          │
          ▼
┌─────────────────┐
│ Shared Storage  │
│   SBD Device    │
└─────────────────┘
```

## Detailed Comparison

### 1. Architecture Complexity

#### Option 1: Agent-Based Fencing ⭐⭐⭐⭐⭐
- **Low complexity**: Extends existing agent functionality
- **Single responsibility**: Each component has clear role
- **Familiar pattern**: Follows standard Kubernetes operator patterns
- **Event-driven**: Uses existing CR watch mechanisms

#### Option 2: Dedicated Fencing Pods ⭐⭐⭐
- **Medium complexity**: Requires Job/Pod lifecycle management
- **Multiple concerns**: Controller manages both CRs and temporary pods
- **Additional patterns**: Requires batch job management logic
- **Cleanup complexity**: Must handle pod cleanup and failure scenarios

### 2. Performance & Scalability

#### Option 1: Agent-Based Fencing ⭐⭐⭐⭐⭐
- **Instant response**: Agents already running, no pod startup time
- **Distributed processing**: Each agent handles its own fencing requests
- **No resource contention**: Uses existing agent resources
- **Concurrent fencing**: Multiple agents can fence simultaneously
- **Resource efficient**: No additional pod creation overhead

#### Option 2: Dedicated Fencing Pods ⭐⭐⭐
- **Pod startup delay**: 10-30 seconds for pod creation and mount
- **Resource overhead**: Each fencing operation requires a new pod
- **Scheduling delays**: Subject to cluster resource availability
- **Sequential bottleneck**: Controller must manage multiple concurrent jobs
- **Storage contention**: Multiple pods may compete for PVC access

### 3. Security Model

#### Option 1: Agent-Based Fencing ⭐⭐⭐⭐
- **Existing security**: Leverages agent's existing privileged access
- **Minimal exposure**: No new privileged components
- **Principle of least privilege**: Only agents need device access
- **Established permissions**: Uses existing agent RBAC and SCCs
- **Audit trail**: Fencing actions logged by agents

#### Option 2: Dedicated Fencing Pods ⭐⭐⭐
- **New privileged pods**: Each fencing pod needs privileged access
- **Broader attack surface**: More components with elevated privileges
- **Complex RBAC**: Controller needs permissions to create privileged pods
- **Credential management**: Pods need service account with SBD access
- **Audit complexity**: Actions performed by temporary pods

### 4. Reliability & Error Handling

#### Option 1: Agent-Based Fencing ⭐⭐⭐⭐
- **Proven reliability**: Agents already handle SBD operations reliably
- **Local error handling**: Agents can retry and recover locally
- **Persistent state**: Agents maintain state between operations
- **Self-healing**: Agents can detect and recover from failures
- **Distributed resilience**: No single point of failure

#### Option 2: Dedicated Fencing Pods ⭐⭐⭐
- **Pod failure handling**: Must handle pod crashes and restarts
- **Network dependencies**: Pod creation depends on API server availability
- **Storage mount failures**: Complex error scenarios with PVC mounting
- **Orphaned pods**: Risk of pods not being cleaned up properly
- **Transient failures**: Requires retry logic for pod creation

### 5. Operational Complexity

#### Option 1: Agent-Based Fencing ⭐⭐⭐⭐⭐
- **Familiar operations**: Same monitoring as existing agents
- **Unified logging**: All agent operations in same log stream
- **Simple troubleshooting**: Single component to debug
- **Consistent monitoring**: Uses existing agent metrics and health checks
- **No additional resources**: No new pods/jobs to monitor

#### Option 2: Dedicated Fencing Pods ⭐⭐
- **Job management**: Must monitor and clean up temporary jobs
- **Split logging**: Fencing logs separate from agent logs
- **Complex troubleshooting**: Must debug both controller and pods
- **Additional monitoring**: Need metrics for job success/failure rates
- **Resource cleanup**: Risk of accumulating failed/completed jobs

### 6. Development & Maintenance

#### Option 1: Agent-Based Fencing ⭐⭐⭐⭐⭐
- **Simpler codebase**: Extends existing agent logic
- **Unified testing**: Test agent functionality in one place
- **Clear ownership**: Agent team owns all SBD device operations
- **Easier debugging**: Single component for SBD operations
- **Consistent patterns**: Follows existing agent architecture

#### Option 2: Dedicated Fencing Pods ⭐⭐⭐
- **Split codebase**: Logic distributed between controller and pods
- **Complex testing**: Must test Job lifecycle, pod creation, cleanup
- **Shared ownership**: Multiple teams may need to understand fencing pods
- **Debugging complexity**: Issues may span controller and pods
- **New patterns**: Requires batch job management expertise

### 7. Implementation Effort

#### Option 1: Agent-Based Fencing ⭐⭐⭐⭐⭐
- **Low effort**: Extend existing agent CR watching
- **Reuse existing code**: Leverage current SBD device handling
- **Minimal controller changes**: Remove problematic device access
- **Standard patterns**: Use existing CR update mechanisms
- **Quick delivery**: Can be implemented in 1-2 sprints

#### Option 2: Dedicated Fencing Pods ⭐⭐⭐
- **Medium effort**: Implement Job/Pod lifecycle management
- **New code required**: Pod template generation, monitoring, cleanup
- **Complex controller logic**: Handle concurrent job management
- **Error scenario handling**: Robust cleanup and retry logic
- **Longer delivery**: Requires 3-4 sprints for production-ready implementation

### 8. Resource Efficiency

#### Option 1: Agent-Based Fencing ⭐⭐⭐⭐⭐
- **No additional resources**: Uses existing agent CPU/memory
- **Efficient scaling**: Scales with node count (natural limit)
- **Minimal overhead**: Only CR watch and processing overhead
- **Shared resources**: Multiple fencing operations share agent resources
- **Predictable usage**: Resource usage is consistent and predictable

#### Option 2: Dedicated Fencing Pods ⭐⭐
- **High resource usage**: Each fencing operation creates a new pod
- **Unpredictable scaling**: Resource usage depends on fencing frequency
- **Startup overhead**: CPU/memory for pod creation and scheduling
- **Storage I/O**: Additional PVC mount/unmount operations
- **Cleanup overhead**: Resources held until pod cleanup

## Specific Technical Considerations

### Agent-Based Fencing Implementation Details

**Advantages:**
- **File locking coordination**: Agents already implement file locking for SBD device access
- **Node-local operations**: Each agent can handle fencing for its own node or others
- **Existing infrastructure**: Leverages established agent-to-SBD-device communication
- **Status feedback**: Agents can directly update StorageBasedRemediation status
- **Event correlation**: Fencing events naturally correlate with agent activities

**Challenges:**
- **Cross-node coordination**: Agents need to coordinate who handles which fencing request
- **Leader election**: May need leader election among agents for fencing operations
- **Status race conditions**: Multiple agents updating same StorageBasedRemediation CR

### Dedicated Fencing Pods Implementation Details

**Advantages:**
- **Isolated operations**: Each fencing operation is independent
- **Controller orchestration**: Central control over all fencing operations
- **Flexible scheduling**: Can schedule fencing pods on specific nodes
- **Resource isolation**: Each operation has dedicated resources
- **Clean state**: Each operation starts with clean state

**Challenges:**
- **PVC discovery**: Must discover correct PVC for target node's SBDConfig
- **Privileged access**: Fencing pods need elevated privileges
- **Concurrent access**: Multiple pods may access same SBD device
- **Storage latency**: PVC mount time affects fencing speed
- **Orphaned resources**: Risk of pods not being cleaned up

## Recommendation Matrix

| Criteria | Agent-Based | Dedicated Pods | Winner |
|----------|-------------|----------------|--------|
| **Complexity** | Low | Medium | Agent-Based |
| **Performance** | High | Medium | Agent-Based |
| **Security** | Good | Fair | Agent-Based |
| **Reliability** | High | Medium | Agent-Based |
| **Operations** | Simple | Complex | Agent-Based |
| **Development** | Easy | Medium | Agent-Based |
| **Resources** | Efficient | Heavy | Agent-Based |
| **Flexibility** | Medium | High | Dedicated Pods |

## Final Recommendation

**Choose Option 1: Agent-Based Fencing**

### Rationale:
1. **Architectural fit**: Aligns with existing agent responsibilities
2. **Performance**: Faster response times with no pod startup delays
3. **Simplicity**: Reduces overall system complexity
4. **Reliability**: Leverages proven agent infrastructure
5. **Efficiency**: Minimal resource overhead
6. **Security**: Maintains existing security model

### Implementation Strategy:
1. **Phase 1**: Extend agents to watch StorageBasedRemediation CRs
2. **Phase 2**: Implement agent-side fencing logic with coordination
3. **Phase 3**: Remove problematic device access from controller
4. **Phase 4**: Add enhanced status reporting and event emission

### Future Considerations:
- **Hybrid approach**: Could later add dedicated pods for special cases
- **Agent coordination**: May need to enhance inter-agent coordination
- **Performance monitoring**: Add metrics for fencing operation timing
- **Failure scenarios**: Enhanced error handling for agent-based fencing

---

**Decision**: Proceed with **Agent-Based Fencing** architecture for its simplicity, performance, and alignment with existing operator patterns. 