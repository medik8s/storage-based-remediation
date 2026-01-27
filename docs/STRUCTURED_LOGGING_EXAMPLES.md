# Structured Logging Implementation Examples

This document provides examples of how structured logging has been implemented in both the SBD Agent and SBD Operator components.

## SBD Agent (`cmd/sbd-agent/main.go`)

### Logger Initialization

```go
// Global logger instance
var logger logr.Logger

// initializeLogger initializes the structured logger with the specified log level
func initializeLogger(level string) error {
 // Parse log level
 var zapLevel zapcore.Level
 switch level {
 case "debug":
  zapLevel = zapcore.DebugLevel
 case "info":
  zapLevel = zapcore.InfoLevel
 case "warn", "warning":
  zapLevel = zapcore.WarnLevel
 case "error":
  zapLevel = zapcore.ErrorLevel
 default:
  return fmt.Errorf("invalid log level: %s (valid: debug, info, warn, error)", level)
 }

 // Create zap config for structured JSON logging
 config := zap.NewProductionConfig()
 config.Level = zap.NewAtomicLevelAt(zapLevel)
 config.Development = false
 config.Encoding = "json"
 config.EncoderConfig.TimeKey = "timestamp"
 config.EncoderConfig.LevelKey = "level"
 config.EncoderConfig.MessageKey = "message"
 config.EncoderConfig.CallerKey = "caller"
 config.EncoderConfig.StacktraceKey = "stacktrace"
 config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
 config.EncoderConfig.EncodeLevel = zapcore.LowercaseLevelEncoder
 config.EncoderConfig.EncodeCaller = zapcore.ShortCallerEncoder

 // Build the logger
 zapLogger, err := config.Build()
 if err != nil {
  return fmt.Errorf("failed to build logger: %w", err)
 }

 // Create logr logger from zap
 logger = zapr.NewLogger(zapLogger)
 
 return nil
}
```

### Configuration and Startup

```go
func main() {
 flag.Parse()

 // Initialize structured logger first
 if err := initializeLogger(*logLevel); err != nil {
  fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
  os.Exit(1)
 }

 logger.Info("SBD Agent starting", "version", "development")
}
```

### Structured Logging Examples

#### Informational Messages with Context

```go
logger.Info("Starting SBD Agent",
 "watchdogDevice", s.watchdog.Path(),
 "sbdDevice", s.sbdDevicePath,
 "nodeName", s.nodeName,
 "nodeID", s.nodeID,
 "petInterval", s.petInterval,
 "sbdUpdateInterval", s.sbdUpdateInterval,
 "heartbeatInterval", s.heartbeatInterval,
 "peerCheckInterval", s.peerCheckInterval)
```

#### Debug Messages

```go
logger.V(1).Info("Successfully wrote heartbeat message",
 "sequence", sequence,
 "nodeID", s.nodeID,
 "slotOffset", slotOffset)
```

#### Warning Messages

```go
logger.Error(nil, "Skipping watchdog pet - SBD device is unhealthy",
 "sbdDevicePath", s.sbdDevicePath,
 "sbdHealthy", s.isSBDHealthy())
```

#### Error Messages with Error Objects

```go
logger.Error(err, "Failed to write heartbeat to SBD device",
 "devicePath", s.sbdDevicePath,
 "nodeID", s.nodeID)
```

#### Critical Self-Fencing Events

```go
logger.Error(nil, "Self-fencing initiated",
 "reason", reason,
 "rebootMethod", s.rebootMethod,
 "nodeID", s.nodeID,
 "nodeName", s.nodeName)
```

### Peer Monitoring with Context

```go
// PeerMonitor with dedicated logger
type PeerMonitor struct {
 peers             map[uint16]*PeerStatus
 peersMutex        sync.RWMutex
 sbdTimeoutSeconds uint
 ownNodeID         uint16
 nodeManager       *sbdprotocol.NodeManager
 logger            logr.Logger
}

func NewPeerMonitor(sbdTimeoutSeconds uint, ownNodeID uint16, nodeManager *sbdprotocol.NodeManager, logger logr.Logger) *PeerMonitor {
 return &PeerMonitor{
  peers:             make(map[uint16]*PeerStatus),
  sbdTimeoutSeconds: sbdTimeoutSeconds,
  ownNodeID:         ownNodeID,
  nodeManager:       nodeManager,
  logger:            logger.WithName("peer-monitor"),
 }
}

// Example peer monitoring logs
pm.logger.Info("Discovered new peer node",
 "nodeID", nodeID,
 "timestamp", timestamp,
 "sequence", sequence)

pm.logger.Error(nil, "Peer node became unhealthy",
 "nodeID", nodeID,
 "timeSinceLastSeen", timeSinceLastSeen,
 "timeout", timeout,
 "lastTimestamp", peer.LastTimestamp,
 "lastSequence", peer.LastSequence)
```

## SBD Operator Controllers

### SBDConfig Controller Enhanced Logging

```go
func (r *SBDConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
 logger := log.FromContext(ctx).WithName("sbdconfig-controller").WithValues(
  "request", req.NamespacedName,
  "controller", "SBDConfig",
 )

 // Add resource-specific context to logger
 logger = logger.WithValues(
  "sbdconfig.name", sbdConfig.Name,
  "sbdconfig.namespace", sbdConfig.Namespace,
  "sbdconfig.generation", sbdConfig.Generation,
  "sbdconfig.resourceVersion", sbdConfig.ResourceVersion,
 )

 logger.V(1).Info("Starting SBDConfig reconciliation",
  "spec.image", sbdConfig.Spec.Image,
  "spec.namespace", sbdConfig.Spec.Namespace,
  "spec.sbdWatchdogPath", sbdConfig.Spec.SbdWatchdogPath)
}
```

### SBDRemediation Controller Enhanced Logging

```go
func (r *SBDRemediationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
 logger := logf.FromContext(ctx).WithName("sbdremediation-controller").WithValues(
  "request", req.NamespacedName,
  "controller", "SBDRemediation",
 )

 // Add resource-specific context to logger
 logger = logger.WithValues(
  "sbdremediation.name", sbdRemediation.Name,
  "sbdremediation.namespace", sbdRemediation.Namespace,
  "sbdremediation.generation", sbdRemediation.Generation,
  "sbdremediation.resourceVersion", sbdRemediation.ResourceVersion,
  "spec.nodeName", sbdRemediation.Spec.NodeName,
  "status.ready", sbdRemediation.IsReady(),
  "status.fencingSucceeded", sbdRemediation.IsFencingSucceeded(),
 )

 logger.V(1).Info("Starting SBDRemediation reconciliation",
  "spec.nodeName", sbdRemediation.Spec.NodeName,
  "status.nodeID", sbdRemediation.Status.NodeID,
  "status.operatorInstance", sbdRemediation.Status.OperatorInstance)
}
```

### Fencing Operations with Detailed Context

```go
logger.Info("Leader confirmed - proceeding with fencing operations",
 "nodeName", sbdRemediation.Spec.NodeName,
 "isLeader", r.IsLeader(),
 "operation", "fencing-initiation")

logger.Error(err, "Failed to map node name to node ID",
 "nodeName", sbdRemediation.Spec.NodeName,
 "operation", "node-name-to-id-mapping")

logger.Info("Successfully fenced node",
 "nodeName", sbdRemediation.Spec.NodeName,
 "nodeID", targetNodeID,
 "operation", "fencing-completed")
```

### Controller Setup with Logging

```go
func (r *SBDConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
 logger := mgr.GetLogger().WithName("setup").WithValues("controller", "SBDConfig")
 
 logger.Info("Setting up SBDConfig controller")
 
 err := ctrl.NewControllerManagedBy(mgr).
  For(&medik8sv1alpha1.SBDConfig{}).
  Owns(&appsv1.DaemonSet{}).
  Named("sbdconfig").
  Complete(r)
 
 if err != nil {
  logger.Error(err, "Failed to setup SBDConfig controller")
  return err
 }
 
 logger.Info("SBDConfig controller setup completed successfully")
 return nil
}
```

## Log Level Configuration

### SBD Agent

The SBD Agent supports configurable log levels via command line flag or environment variable:

```bash
# Command line flag
./sbd-agent --log-level=debug

# Environment variable
export LOG_LEVEL=info
```

Supported levels: `debug`, `info`, `warn`, `error`

### SBD Operator

The SBD Operator uses the standard controller-runtime logging configuration through zap flags:

```bash
# Development mode with debug logging
./manager --zap-devel --zap-log-level=debug

# Production mode with info logging
./manager --zap-log-level=info --zap-encoder=json
```

## Sample Log Output

### SBD Agent JSON Output

```json
{
  "timestamp": "2025-01-27T10:30:45.123Z",
  "level": "info",
  "message": "Starting SBD Agent",
  "watchdogDevice": "/dev/watchdog",
  "sbdDevice": "/dev/sdb",
  "nodeName": "worker-1",
  "nodeID": 1,
  "petInterval": "30s",
  "sbdUpdateInterval": "5s",
  "heartbeatInterval": "15s",
  "peerCheckInterval": "5s"
}

{
  "timestamp": "2025-01-27T10:30:50.456Z",
  "level": "error",
  "message": "Peer node became unhealthy",
  "nodeID": 2,
  "timeSinceLastSeen": "35s",
  "timeout": "30s",
  "lastTimestamp": 1737975045,
  "lastSequence": 123456
}
```

### SBD Operator JSON Output

```json
{
  "timestamp": "2025-01-27T10:30:45.789Z",
  "level": "info",
  "message": "Leader confirmed - proceeding with fencing operations",
  "controller": "SBDRemediation",
  "request": "default/fence-worker-2",
  "sbdremediation.name": "fence-worker-2",
  "sbdremediation.namespace": "default",
  "spec.nodeName": "worker-2",
  "status.ready": false,
  "status.fencingSucceeded": false,
  "status.fencingInProgress": true,
  "nodeName": "worker-2",
  "isLeader": true,
  "operation": "fencing-initiation"
}
```

## Benefits of Structured Logging

1. **Machine Readable**: JSON format enables automated log processing
2. **Rich Context**: Key-value pairs provide detailed operational context
3. **Searchable**: Structured fields enable efficient log queries
4. **Consistent**: Standardized logging patterns across components
5. **Debuggable**: Enhanced context makes troubleshooting easier
6. **Observable**: Integrates well with monitoring and alerting systems

## Security Considerations

- Sensitive information (authentication tokens, passwords) is never logged
- Node IDs and names are considered safe operational metadata
- Error messages are sanitized to avoid information disclosure
- Log levels can be adjusted to reduce verbosity in production
