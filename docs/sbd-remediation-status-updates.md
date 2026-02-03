# StorageBasedRemediation Controller Status Updates Enhancement

This document describes the enhanced status update functionality implemented in the StorageBasedRemediation controller to provide robust, idempotent, and conflict-resistant status management using Kubernetes conditions.

## Overview

The StorageBasedRemediation controller has been enhanced with comprehensive status update capabilities that include:

- **Conditions-based Status**: Uses Kubernetes-standard conditions instead of phases
- **Robust Error Handling**: Proper distinction between retryable and non-retryable errors
- **Retry Logic**: Exponential backoff for transient failures
- **Idempotent Updates**: Avoid unnecessary API calls when status hasn't changed
- **Conflict Resolution**: Handle concurrent updates gracefully
- **Comprehensive Testing**: Integration tests covering all scenarios

## Key Features

### 1. Conditions-based Status Management

The StorageBasedRemediation now uses standard Kubernetes conditions for status tracking:

```go
type SBDRemediationConditionType string

const (
    SBDRemediationConditionLeadershipAcquired SBDRemediationConditionType = "LeadershipAcquired"
    SBDRemediationConditionFencingInProgress  SBDRemediationConditionType = "FencingInProgress"
    SBDRemediationConditionFencingSucceeded   SBDRemediationConditionType = "FencingSucceeded"
    SBDRemediationConditionReady              SBDRemediationConditionType = "Ready"
)
```

### 2. Robust Status Updates (`updateStatusWithConditions`)

The `updateStatusWithConditions` method provides comprehensive status update functionality:

```go
func (r *SBDRemediationReconciler) updateStatusWithConditions(ctx context.Context, 
    sbdRemediation *medik8sv1alpha1.SBDRemediation, 
    conditions map[medik8sv1alpha1.SBDRemediationConditionType]conditionUpdate, 
    logger logr.Logger) (ctrl.Result, error)
```

**Features:**
- **Idempotency**: Skips update if conditions already match the desired state
- **Automatic Timestamping**: Updates `LastTransitionTime` on condition changes
- **Operator Instance Tracking**: Records which operator instance made the update
- **Condition-based Requeuing**: Different requeue behavior based on the condition states
- **Conflict Handling**: Uses retry logic to handle concurrent updates

### 3. Conflict-Resistant Updates (`updateStatusWithRetry`)

The `updateStatusWithRetry` method handles optimistic locking conflicts:

```go
func (r *SBDRemediationReconciler) updateStatusWithRetry(ctx context.Context, 
    sbdRemediation *medik8sv1alpha1.SBDRemediation) error
```

**Features:**
- **Exponential Backoff**: Configurable retry delays with jitter
- **Conflict Detection**: Automatically retries on `IsConflict` errors
- **Fresh Resource Retrieval**: Gets latest version before each retry
- **Transient Error Handling**: Retries on server timeouts and rate limits
- **Permanent Error Detection**: Stops retrying on non-recoverable errors

### 4. Fencing Error Classification (`FencingError`)

Custom error type for comprehensive fencing operation error handling:

```go
type FencingError struct {
    Operation   string
    Underlying  error
    Retryable   bool
    NodeName    string
    NodeID      uint16
}
```

**Error Categories:**
- **Retryable**: Device I/O errors, sync failures, network issues
- **Non-retryable**: Marshaling errors, invalid configuration, permanent failures

### 5. Retry Logic with Backoff (`performFencingWithRetry`)

The fencing operation includes intelligent retry logic:

```go
func (r *SBDRemediationReconciler) performFencingWithRetry(ctx context.Context, 
    sbdRemediation *medik8sv1alpha1.SBDRemediation, 
    targetNodeID uint16) error
```

**Configuration:**
- **Max Attempts**: 3 (configurable via `maxRetryAttempts`)
- **Base Delay**: 5 seconds (configurable via `baseRetryDelay`)
- **Max Delay**: 30 seconds (configurable via `maxRetryDelay`)
- **Smart Backoff**: Exponential increase with maximum cap

## Status Update Workflow

### Success Path

1. **LeadershipAcquired=True** → Operator has leadership for fencing
2. **FencingInProgress=True** → Writing fence message to SBD device
3. **FencingSucceeded=True** → Fence message written and verified
4. **Ready=True** → Overall remediation completed successfully

### Error Handling

1. **Node ID Mapping Error** → `FencingSucceeded=False`, `Ready=True` (failed)
2. **SBD Device Access Error** → Retry up to 3 times → `FencingSucceeded=False`, `Ready=True`
3. **Write/Sync Error** → Retry up to 3 times → `FencingSucceeded=False`, `Ready=True`
4. **Leadership Wait** → `LeadershipAcquired=False`, `Ready=False` (requeue every 10s)

### Status Fields Updated

- `Conditions`: Array of condition objects with type, status, reason, and message
- `NodeID`: Numeric ID assigned to target node
- `FenceMessageWritten`: Boolean flag for successful writes
- `OperatorInstance`: Instance that handled the remediation
- `LastUpdateTime`: Timestamp of last update

## Configuration

### Retry Configuration

```go
const (
    MaxFencingRetries         = 5
    InitialFencingRetryDelay  = 1 * time.Second
    MaxFencingRetryDelay      = 30 * time.Second
    FencingRetryBackoffFactor = 2.0
)
```

### Status Update Configuration

```go
const (
    MaxStatusUpdateRetries    = 10
    InitialStatusUpdateDelay  = 100 * time.Millisecond
    MaxStatusUpdateDelay      = 5 * time.Second
    StatusUpdateBackoffFactor = 1.5
)
```

## Error Scenarios and Responses

| Error Type | Retryable | Max Attempts | Final Conditions | Requeue Behavior |
|------------|-----------|--------------|------------------|------------------|
| Node ID mapping | No | 1 | FencingSucceeded=False, Ready=True | No requeue |
| SBD device open | Yes | 5 | FencingSucceeded=False, Ready=True | No requeue after final failure |
| Write/sync error | Yes | 5 | FencingSucceeded=False, Ready=True | No requeue after final failure |
| Marshaling error | No | 1 | FencingSucceeded=False, Ready=True | No requeue |
| Status update conflict | Yes | 10 | Varies | Retry with backoff |
| Leadership unavailable | N/A | N/A | LeadershipAcquired=False, Ready=False | 10s requeue |

## Integration Tests

The enhanced controller includes comprehensive integration tests covering:

### Success Scenarios
- **Complete fencing workflow**: All conditions properly set through success path
- **Status field verification**: All status fields properly populated
- **SBD device verification**: Fence message actually written to device

### Error Scenarios
- **Invalid node names**: Proper error handling and condition updates
- **Device access failures**: Retry logic and final failure handling
- **Status update idempotency**: Multiple updates with same values

### Leadership Scenarios
- **Leadership availability**: Processing when leader
- **Non-leader behavior**: Proper handling when not leader

### Retry Logic Tests
- **Transient error retry**: Multiple attempts for recoverable errors
- **Non-retryable errors**: Immediate failure for permanent errors
- **Status update conflicts**: Conflict resolution mechanisms

## Usage Examples

### Basic Status Update

```go
conditions := map[medik8sv1alpha1.SBDRemediationConditionType]conditionUpdate{
    medik8sv1alpha1.SBDRemediationConditionFencingInProgress: {
        status:  metav1.ConditionTrue,
        reason:  "FencingInitiated",
        message: "Writing fence message to SBD device",
    },
    medik8sv1alpha1.SBDRemediationConditionReady: {
        status:  metav1.ConditionFalse,
        reason:  "FencingInProgress",
        message: "Fencing operation in progress",
    },
}

result, err := r.updateStatusWithConditions(ctx, sbdRemediation, conditions, logger)
if err != nil {
    return result, err
}
```

### Error Handling with Classification

```go
if err := r.performFencingWithRetry(ctx, sbdRemediation, targetNodeID); err != nil {
    var fencingErr *FencingError
    var message string
    if errors.As(err, &fencingErr) {
        message = fmt.Sprintf("Fencing operation failed: %s", fencingErr.Error())
    } else {
        message = fmt.Sprintf("Fencing operation failed: %v", err)
    }
    
    conditions := map[medik8sv1alpha1.SBDRemediationConditionType]conditionUpdate{
        medik8sv1alpha1.SBDRemediationConditionFencingSucceeded: {
            status:  metav1.ConditionFalse,
            reason:  "FencingFailed",
            message: message,
        },
        medik8sv1alpha1.SBDRemediationConditionReady: {
            status:  metav1.ConditionTrue,
            reason:  "Failed",
            message: message,
        },
    }
    
    _, updateErr := r.updateStatusWithConditions(ctx, sbdRemediation, conditions, logger)
    return ctrl.Result{}, updateErr
}
```

## Condition Helper Methods

The StorageBasedRemediation type includes several helper methods for working with conditions:

```go
// Check condition states
remediation.IsFencingSucceeded()     // Returns true if FencingSucceeded=True
remediation.IsFencingInProgress()    // Returns true if FencingInProgress=True
remediation.IsReady()                // Returns true if Ready=True
remediation.HasLeadership()          // Returns true if LeadershipAcquired=True

// Get specific conditions
condition := remediation.GetCondition(SBDRemediationConditionReady)

// Set conditions
remediation.SetCondition(SBDRemediationConditionReady, 
    metav1.ConditionTrue, "Succeeded", "Fencing completed successfully")
```

## Best Practices

1. **Always use `updateStatusWithConditions`** for status updates instead of direct API calls
2. **Use appropriate condition types** for different states (leadership, progress, completion)
3. **Classify errors properly** using `FencingError` for better retry behavior
4. **Handle conflicts gracefully** by relying on the built-in retry mechanisms
5. **Monitor requeue behavior** to avoid infinite loops
6. **Use helper methods** for condition checking to maintain consistency

## Monitoring and Observability

The enhanced controller provides comprehensive logging:

- **Debug Level**: Status update details, conflict retries
- **Info Level**: Phase transitions, successful operations
- **Error Level**: Permanent failures, retry exhaustion

Key log messages to monitor:
- `Status updated successfully`: Successful status transitions
- `Status already up to date, skipping update`: Idempotent behavior
- `Conflict during status update, retrying`: Conflict resolution
- `Fencing operation failed after X attempts`: Retry exhaustion

## Migration Notes

Existing StorageBasedRemediation resources will automatically benefit from the enhanced status update logic. No manual migration is required.

The enhanced controller is backward compatible with existing API structures while providing improved reliability and observability. 