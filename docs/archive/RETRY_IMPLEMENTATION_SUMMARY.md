# Advanced Error Handling, Exponential Backoff, and Retry Mechanisms Implementation

## Overview

This implementation adds comprehensive retry mechanisms and advanced error handling to both the SBD Agent and SBD Operator components of the sbd-operator project. The solution provides robust handling of transient errors while avoiding endless retries for permanent failures.

## Part 1: SBD Agent Implementation (`cmd/sbd-agent/main.go`)

### Key Features

1. **Retry Utility Package** (`pkg/retry/retry.go`)
   - Configurable exponential backoff with jitter
   - Intelligent error classification (transient vs permanent)
   - Context-aware cancellation support
   - Comprehensive logging of retry attempts

2. **Enhanced Watchdog and SBD Device Packages**
   - `pkg/watchdog/watchdog.go`: Added retry mechanisms for watchdog operations
   - `pkg/blockdevice/blockdevice.go`: Enhanced with retry support for I/O operations

3. **Failure Tracking and Self-Fencing**
   - Consecutive failure counting for critical operations
   - Automatic failure count reset after time intervals
   - Self-fencing trigger when failure thresholds are exceeded
   - Thread-safe failure tracking with mutex protection

### Configuration Constants

```go
const (
    // Retry configuration for critical operations
    MaxCriticalRetries = 3
    InitialCriticalRetryDelay = 200 * time.Millisecond
    MaxCriticalRetryDelay = 2 * time.Second
    CriticalRetryBackoffFactor = 2.0

    // Failure tracking configuration
    MaxConsecutiveFailures = 5
    FailureCountResetInterval = 30 * time.Second
)
```

### Enhanced Agent Operations

1. **Watchdog Loop**
   - Retry mechanism for watchdog petting operations
   - Failure tracking with automatic self-fencing
   - Graceful handling of transient device errors

2. **SBD Device Loop**
   - Retry mechanisms for SBD device write operations
   - Device reinitialization on persistent failures
   - Health status tracking and metrics updates

3. **Heartbeat Loop**
   - Retry mechanisms for heartbeat write operations
   - Failure counting and recovery detection
   - Automatic device recovery handling

### Error Classification

The implementation distinguishes between:
- **Transient Errors**: EIO, EAGAIN, EBUSY, device busy, timeouts
- **Permanent Errors**: ENOENT, EACCES, invalid arguments, missing devices

## Part 2: SBD Operator Implementation

### SBD Config Controller (`internal/controller/sbdconfig_controller.go`)

#### Enhanced Features

1. **Retry Configuration**
   ```go
   const (
       MaxSBDConfigRetries = 3
       InitialSBDConfigRetryDelay = 500 * time.Millisecond
       MaxSBDConfigRetryDelay = 10 * time.Second
       SBDConfigRetryBackoffFactor = 2.0
   )
   ```

2. **Kubernetes API Operations with Retry**
   - DaemonSet creation/update operations
   - Namespace creation operations
   - Status update operations
   - Intelligent error classification for Kubernetes errors

3. **Enhanced Reconcile Method**
   - Retry logic for all critical Kubernetes API calls
   - Proper requeue handling with backoff for transient errors
   - Detailed error logging and event emission

### SBD Remediation Controller (`internal/controller/sbdremediation_controller.go`)

#### Enhanced Features

1. **Multiple Retry Configurations**
   ```go
   const (
       // Fencing operations (most critical)
       MaxFencingRetries = 5
       InitialFencingRetryDelay = 1 * time.Second
       MaxFencingRetryDelay = 30 * time.Second
       
       // Status updates
       MaxStatusUpdateRetries = 10
       InitialStatusUpdateDelay = 100 * time.Millisecond
       
       // Kubernetes API operations
       MaxKubernetesAPIRetries = 3
       InitialKubernetesAPIDelay = 200 * time.Millisecond
   )
   ```

2. **Advanced Error Handling**
   - Separate retry configurations for different operation types
   - Kubernetes-specific error classification
   - Leadership-aware fencing operations with retries

## Key Implementation Details

### 1. Retry Utility Package (`pkg/retry/retry.go`)

```go
type Config struct {
    MaxRetries    int
    InitialDelay  time.Duration
    MaxDelay      time.Duration
    BackoffFactor float64
    Jitter        float64
    Logger        logr.Logger
}

func Do(ctx context.Context, config Config, operation string, fn func() error) error
```

**Features:**
- Exponential backoff with configurable jitter
- Context cancellation support
- Automatic error classification
- Comprehensive logging

### 2. Error Classification

```go
func IsTransientError(err error) bool {
    // Checks for syscall errors, path errors, and custom retryable errors
    // Returns true for EIO, EAGAIN, EBUSY, timeouts
    // Returns false for ENOENT, EACCES, permanent failures
}
```

### 3. Failure Tracking

```go
type SBDAgent struct {
    // ... existing fields ...
    
    // Failure tracking and retry configuration
    watchdogFailureCount  int
    sbdFailureCount       int
    heartbeatFailureCount int
    lastFailureReset      time.Time
    failureCountMutex     sync.RWMutex
    retryConfig           retry.Config
}
```

## Testing

### Comprehensive Test Coverage

1. **Retry Package Tests** (`pkg/retry/retry_test.go`)
   - Error classification tests
   - Exponential backoff calculation tests
   - Context cancellation tests
   - Success and failure scenario tests

2. **SBD Agent Retry Tests** (`cmd/sbd-agent/retry_test.go`)
   - Failure tracking tests
   - Self-fence threshold tests
   - Retry configuration tests
   - Watchdog and heartbeat retry mechanism tests

3. **Controller Integration Tests**
   - Kubernetes API retry scenarios
   - Transient vs permanent error handling
   - Requeue behavior validation

## Benefits

1. **Improved Reliability**
   - Handles transient network and device issues gracefully
   - Reduces false positives in failure detection
   - Provides robust recovery mechanisms

2. **Better Observability**
   - Detailed logging of retry attempts and failures
   - Prometheus metrics for failure tracking
   - Clear distinction between transient and permanent errors

3. **Intelligent Self-Fencing**
   - Prevents premature self-fencing due to temporary issues
   - Tracks consecutive failures across different operation types
   - Automatic failure count reset prevents accumulation of old failures

4. **Configurable Behavior**
   - Tunable retry parameters for different environments
   - Separate configurations for different operation criticality levels
   - Context-aware cancellation for graceful shutdowns

## Usage Examples

### SBD Agent Retry Configuration

```go
retryConfig := retry.Config{
    MaxRetries:    3,
    InitialDelay:  200 * time.Millisecond,
    MaxDelay:      2 * time.Second,
    BackoffFactor: 2.0,
    Logger:        logger.WithName("sbd-agent-retry"),
}

err := retry.Do(ctx, retryConfig, "pet watchdog", func() error {
    return watchdog.Pet()
})
```

### Controller Retry Configuration

```go
err := r.performKubernetesAPIOperationWithRetry(ctx, "create DaemonSet", func() error {
    return r.Client.Create(ctx, daemonSet)
}, logger)
```

## Migration and Compatibility

- **Backward Compatibility**: All existing functionality is preserved
- **Gradual Adoption**: Retry mechanisms can be enabled incrementally
- **Configuration**: Default values provide sensible behavior out of the box
- **Monitoring**: Enhanced metrics provide visibility into retry behavior

This implementation significantly improves the robustness and reliability of the SBD operator while maintaining compatibility with existing deployments and providing comprehensive observability into the retry behavior. 