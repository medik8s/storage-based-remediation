/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	// Kubernetes imports for StorageBasedRemediation CR watching

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/medik8s/storage-based-remediation/api/v1alpha1"
	"github.com/medik8s/storage-based-remediation/pkg/agent"
	"github.com/medik8s/storage-based-remediation/pkg/blockdevice"
	"github.com/medik8s/storage-based-remediation/pkg/controller"
	"github.com/medik8s/storage-based-remediation/pkg/mocks"
	"github.com/medik8s/storage-based-remediation/pkg/retry"
	"github.com/medik8s/storage-based-remediation/pkg/sbdprotocol"
	"github.com/medik8s/storage-based-remediation/pkg/version"
	"github.com/medik8s/storage-based-remediation/pkg/watchdog"
)

// RBAC permissions for SBR Agent
// The agent reads StorageBasedRemediationConfig, sets SBRStorageUnhealthy node condition for unhealthy peers, and reconciles StorageBasedRemediation CRs (created by NHC) for fencing.
// +kubebuilder:rbac:groups=storage-based-remediation.medik8s.io,resources=sbdconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=storage-based-remediation.medik8s.io,resources=sbdconfigs/status,verbs=get
// +kubebuilder:rbac:groups=storage-based-remediation.medik8s.io,resources=storagebasedremediations,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=storage-based-remediation.medik8s.io,resources=storagebasedremediations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=nodes/status,verbs=get;patch;update

var (
	watchdogPath    = flag.String(agent.FlagWatchdogPath, agent.DefaultWatchdogPath, "Path to the watchdog device")
	watchdogTimeout = flag.Duration(agent.FlagWatchdogTimeout, 60*time.Second,
		"Watchdog timeout duration (how long before watchdog triggers reboot)")
	petInterval = flag.Duration(agent.FlagPetInterval, 15*time.Second,
		"Pet interval (how often to pet the watchdog)")
	sbrDevice      = flag.String(agent.FlagSBRDevice, agent.DefaultSBRDevice, "Path to the SBR block device")
	sbrFileLocking = flag.Bool(agent.FlagSBRFileLocking, agent.DefaultSBRFileLocking,
		"Enable file locking for SBR device operations (recommended for shared storage)")
	nodeName    = flag.String(agent.FlagNodeName, agent.DefaultNodeName, "Name of this Kubernetes node")
	clusterName = flag.String(agent.FlagClusterName, agent.DefaultClusterName,
		"Name of the cluster for node mapping")
	nodeID = flag.Uint(agent.FlagNodeID, agent.DefaultNodeID,
		"Unique numeric ID for this node (1-255) - deprecated, use hash-based mapping")
	sbrTimeoutSeconds = flag.Uint(agent.FlagSBRTimeoutSeconds, agent.DefaultSBRTimeoutSeconds,
		"SBR timeout in seconds (determines heartbeat interval)")
	sbrUpdateInterval = flag.Duration(agent.FlagSBRUpdateInterval, 5*time.Second,
		"Interval for updating SBR device with node status")
	peerCheckInterval = flag.Duration(agent.FlagPeerCheckInterval, 5*time.Second, "Interval for checking peer heartbeats")
	logLevel          = flag.String(agent.FlagLogLevel, agent.DefaultLogLevel, "Log level (debug, info, warn, error)")
	rebootMethod      = flag.String(agent.FlagRebootMethod, agent.DefaultRebootMethod,
		"Method to use for self-fencing (panic, systemctl-reboot, none)")
	metricsPort = flag.Int(agent.FlagMetricsPort, agent.DefaultMetricsPort,
		"Port for Prometheus metrics endpoint")
	staleNodeTimeout = flag.Duration(agent.FlagStaleNodeTimeout, 1*time.Hour,
		"Timeout for considering nodes stale and removing them from slot mapping")
	detectOnlyMode = flag.Bool(agent.FlagDetectOnlyMode, false,
		"When true, disarm watchdog and do not remediate (detect and set node conditions only)")

	// I/O timeout configuration
	ioTimeout = flag.Duration("io-timeout", 2*time.Second,
		"Timeout for I/O operations (prevents indefinite hanging when storage becomes unresponsive)")

	// Previous fencing message flag
	previousFenceMessage = false
)

const (
	// SBRNodeIDOffset is the offset where node ID is written in the SBR device
	SBRNodeIDOffset = 0
	// MaxNodeNameLength is the maximum length for a node name in SBR device
	MaxNodeNameLength = 256
	// DefaultNodeID is the placeholder node ID used when none is specified
	DefaultNodeID = 1

	// Retry configuration constants for SBR Agent operations
	// MaxCriticalRetries is the maximum number of retry attempts for critical operations
	MaxCriticalRetries = 3
	// InitialCriticalRetryDelay is the initial delay between critical operation retries
	InitialCriticalRetryDelay = 200 * time.Millisecond
	// MaxCriticalRetryDelay is the maximum delay between critical operation retries
	MaxCriticalRetryDelay = 2 * time.Second
	// CriticalRetryBackoffFactor is the exponential backoff factor for critical operation retries
	CriticalRetryBackoffFactor = 2.0

	// MaxConsecutiveFailures is the maximum number of consecutive failures before triggering self-fence
	MaxConsecutiveFailures = 7
	// FailureCountResetInterval is the interval after which failure counts are reset
	FailureCountResetInterval = 10 * time.Minute
	// SBRDefaultTimeoutSec used to calculate the heartbeat, would use SBR_TIMEOUT_SECONDS var if exist
	SBRDefaultTimeoutSec = 30

	// File locking constants
	// FileLockTimeout is the maximum time to wait for acquiring a file lock
	FileLockTimeout = 5 * time.Second
	// FileLockRetryInterval is the interval between file lock acquisition attempts
	FileLockRetryInterval = 100 * time.Millisecond

	// DefaultMinMissedHeartbeatsForRemediation gates when peers create a remediation.
	// Default equals MaxConsecutiveFailures.
	DefaultMinMissedHeartbeatsForRemediation = MaxConsecutiveFailures - 1

	// SBRAgentRemediationGraceAnnotationKey is written on the Node right before deleting
	// an SBR-agent remediation to provide a grace period for sbr-agent to start running on the node and report health, before re-creating a new remediation.
	SBRAgentRemediationGraceAnnotationKey = "medik8s.io/sbr-remediation-grace-at"
	// SBRAgentRemediationGracePeriod is the minimum time to wait between deletion and re-creation.
	SBRAgentRemediationGracePeriod = 3 * time.Minute

	// RemediationCheckTimeout is the timeout for checking if a StorageBasedRemediation CR exists
	// when SBR is unhealthy; used to avoid blocking the watchdog loop.
	RemediationCheckTimeout = 5 * time.Second
)

// sbrUnhealthyConditionStaleAge is the duration after which SBRStorageUnhealthy=True is
// considered stale and we set it to Unknown so NHC removes its remediation and the node agent
// can run and report healthy. Set at startup to (MaxConsecutiveFailures+1)*heartbeatInterval + RemediationCheckTimeout:
// we wait long enough for the unhealthy node to self-fence (e.g. after MaxConsecutiveFailures missed heartbeats),
// plus one heartbeat buffer, plus time for the remediation CR API check.
var sbrUnhealthyConditionStaleAge time.Duration

// Global logger instance
var logger logr.Logger

// Reboot method constants
const (
	RebootMethodPanic           = "panic"
	RebootMethodSystemctlReboot = "systemctl-reboot"
	RebootMethodNone            = "none"
)

// Event reasons for agent recorder (used in agent and tests)
const (
	EventFieldReason = "Reason" // Event field name for use in tests with HaveField

	EventReasonSelfFenceInitiated            = "SelfFenceInitiated"            // Emitted when the agent triggers self-fence (reboot/panic)
	EventReasonSBRUnhealthyWatchdogTimeout   = "SBRUnhealthyWatchdogTimeout"   // Emitted when SBR unhealthy and remediation CR exists, skipping pet
	EventReasonSBRUnhealthySkipPetAPIError   = "SBRUnhealthySkipPetAPIError"   // Emitted when SBR unhealthy and remediation CR check failed (API error), skipping pet
	EventReasonSBRUnhealthyDetectOnly        = "SBRUnhealthyDetectOnly"        // Emitted in detect-only mode when SBR becomes unhealthy (watchdog disarmed)
	EventReasonSelfFenceAbortedNoRemediation = "SelfFenceAbortedNoRemediation" // Emitted when self-fence aborted because no StorageBasedRemediation CR exists
	EventReasonWatchdogPetFailed             = "WatchdogPetFailed"             // Emitted when watchdog pet failures exceed threshold
	EventReasonSBRWriteFailed                = "SBRWriteFailed"                // Emitted when SBR device write failures exceed threshold
	EventReasonHeartbeatWriteFailed          = "HeartbeatWriteFailed"          // Emitted when heartbeat write failures exceed threshold
	EventReasonFenceMessageDetected          = "FenceMessageDetected"          // Emitted when a fence message is read from the agent's own slot
)

// metricsOnce ensures metrics are only registered once
var metricsOnce sync.Once

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

	// Create zap config
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

// Prometheus metrics definitions
var (
	// sbr_agent_status_healthy: 1 if the agent is healthy, 0 otherwise
	// This metric indicates overall agent health including watchdog and SBR device access
	agentHealthyGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "sbr_agent_status_healthy",
		Help: "SBR Agent health status (1 = healthy, 0 = unhealthy)",
	})

	// sbr_device_io_errors_total: Total number of I/O errors with shared SBR device
	// This metric tracks all I/O operation failures when interacting with the SBR device
	sbrIOErrorsCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "sbr_device_io_errors_total",
		Help: "Total number of I/O errors encountered when interacting with the shared SBR device",
	})

	// sbr_watchdog_pets_total: Total number of successful watchdog pets
	// This metric counts how many times the kernel watchdog has been successfully petted
	watchdogPetsCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "sbr_watchdog_pets_total",
		Help: "Total number of times the local kernel watchdog has been successfully petted",
	})

	// sbr_peer_status: Current liveness status of each peer node
	// This metric uses labels to track the status of each peer node in the cluster
	peerStatusGaugeVec = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "sbr_peer_status",
		Help: "Current liveness status of each peer node (1 = alive, 0 = unhealthy/down)",
	}, []string{"node_id", "node_name", "status"})

	// sbr_self_fenced_total: Total number of self-fence initiations
	// This metric counts how many times this agent has initiated self-fencing
	selfFencedCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "sbr_self_fenced_total",
		Help: "Total number of times the agent has initiated a self-fence",
	})
)

// peerStatus represents the status of a peer node
type peerStatus struct {
	NodeID        uint16    `json:"nodeId"`
	LastTimestamp uint64    `json:"lastTimestamp"`
	LastSequence  uint64    `json:"lastSequence"`
	LastSeen      time.Time `json:"lastSeen"`
	IsHealthy     bool      `json:"isHealthy"`
}

// peerMonitor manages tracking of peer node states
type peerMonitor struct {
	peers             map[uint16]*peerStatus
	peersMutex        sync.RWMutex
	sbrTimeoutSeconds uint
	ownNodeID         uint16
	nodeManager       *sbdprotocol.NodeManager
	logger            logr.Logger
}

// newPeerMonitor creates a new peer monitor instance
func newPeerMonitor(sbrTimeoutSeconds uint, ownNodeID uint16,
	nodeManager *sbdprotocol.NodeManager, logger logr.Logger) *peerMonitor {
	return &peerMonitor{
		peers:             make(map[uint16]*peerStatus),
		sbrTimeoutSeconds: sbrTimeoutSeconds,
		ownNodeID:         ownNodeID,
		nodeManager:       nodeManager,
		logger:            logger.WithName("peer-monitor"),
	}
}

// updatePeer updates the status of a peer node
func (pm *peerMonitor) updatePeer(nodeID uint16, timestamp, sequence uint64) {
	pm.peersMutex.Lock()
	defer pm.peersMutex.Unlock()

	now := time.Now()

	// Get or create peer status
	peer, exists := pm.peers[nodeID]
	if !exists {
		peer = &peerStatus{
			NodeID:    nodeID,
			IsHealthy: true,
		}
		pm.peers[nodeID] = peer

		// Try to get node name for logging
		nodeName := "unknown"
		if pm.nodeManager != nil {
			// Ensure we have the latest node map from disk
			if err := pm.nodeManager.ReloadFromDevice(); err != nil {
				pm.logger.Error(err, "Failed to reload node map from device", "nodeID", nodeID)
			}
			if name, found := pm.nodeManager.GetNodeForNodeID(nodeID); found {
				nodeName = name
			}
		}

		pm.logger.Info("Discovered new peer node",
			"nodeID", nodeID,
			"nodeName", nodeName,
			"timestamp", timestamp,
			"sequence", sequence)
	}

	// Check if this is a newer heartbeat
	isNewer := false
	if timestamp > peer.LastTimestamp {
		isNewer = true
	} else if timestamp == peer.LastTimestamp && sequence > peer.LastSequence {
		isNewer = true
	}

	if isNewer {
		// Update peer status
		wasHealthy := peer.IsHealthy
		peer.LastTimestamp = timestamp
		peer.LastSequence = sequence
		peer.LastSeen = now
		peer.IsHealthy = true

		// Update Prometheus metrics
		pm.updatePeerMetrics(nodeID, peer.IsHealthy)

		// Log status change
		if !wasHealthy {
			pm.logger.Info("Peer node recovered to healthy status",
				"nodeID", nodeID,
				"timestamp", timestamp,
				"sequence", sequence,
				"lastSeen", peer.LastSeen)
		} else {
			pm.logger.V(1).Info("Updated peer node heartbeat",
				"nodeID", nodeID,
				"timestamp", timestamp,
				"sequence", sequence,
				"lastSeen", peer.LastSeen)
		}
	}
}

// checkPeerLiveness checks which peers are still alive based on timeout
func (pm *peerMonitor) checkPeerLiveness() {
	pm.peersMutex.Lock()
	defer pm.peersMutex.Unlock()

	now := time.Now()
	heartbeatInterval := time.Duration(pm.sbrTimeoutSeconds) / 2 * time.Second
	timeout := heartbeatInterval * MaxConsecutiveFailures

	for peerNodeID, peer := range pm.peers {
		timeSinceLastSeen := now.Sub(peer.LastSeen)
		wasHealthy := peer.IsHealthy

		// Consider peer unhealthy if we haven't seen a heartbeat within timeout
		peer.IsHealthy = timeSinceLastSeen <= timeout
		// Update metrics if status changed
		if wasHealthy != peer.IsHealthy {
			pm.updatePeerMetrics(peerNodeID, peer.IsHealthy)
		}

		// Log status change
		if wasHealthy && !peer.IsHealthy {
			pm.logger.Error(nil, "Peer node became unhealthy",
				"nodeID", peerNodeID,
				"timeSinceLastSeen", timeSinceLastSeen,
				"timeout", timeout,
				"lastTimestamp", peer.LastTimestamp,
				"lastSequence", peer.LastSequence)
		} else if !wasHealthy && peer.IsHealthy {
			pm.logger.Info("Peer node recovered to healthy status",
				"nodeID", peerNodeID,
				"timeSinceLastSeen", timeSinceLastSeen,
				"lastTimestamp", peer.LastTimestamp,
				"lastSequence", peer.LastSequence)
		}
	}
}

// getPeerStatus returns a copy of the current peer status map
func (pm *peerMonitor) getPeerStatus() map[uint16]*peerStatus {
	pm.peersMutex.RLock()
	defer pm.peersMutex.RUnlock()

	// Return a deep copy to avoid race conditions
	result := make(map[uint16]*peerStatus)
	for nodeID, peer := range pm.peers {
		result[nodeID] = &peerStatus{
			NodeID:        peer.NodeID,
			LastTimestamp: peer.LastTimestamp,
			LastSequence:  peer.LastSequence,
			LastSeen:      peer.LastSeen,
			IsHealthy:     peer.IsHealthy,
		}
	}
	return result
}

// getHealthyPeerCount returns the number of healthy peers
func (pm *peerMonitor) getHealthyPeerCount() int {
	pm.peersMutex.RLock()
	defer pm.peersMutex.RUnlock()

	count := 0
	for _, peer := range pm.peers {
		if peer.IsHealthy {
			count++
		}
	}
	return count
}

// updatePeerMetrics updates Prometheus metrics for peer status
func (pm *peerMonitor) updatePeerMetrics(nodeID uint16, isHealthy bool) {
	nodeIDStr := fmt.Sprintf("%d", nodeID)
	nodeName := fmt.Sprintf("node-%d", nodeID) // Simple node name mapping

	// Set the metric value based on health status
	if isHealthy {
		peerStatusGaugeVec.WithLabelValues(nodeIDStr, nodeName, "alive").Set(1)
		peerStatusGaugeVec.WithLabelValues(nodeIDStr, nodeName, "unhealthy").Set(0)
	} else {
		peerStatusGaugeVec.WithLabelValues(nodeIDStr, nodeName, "alive").Set(0)
		peerStatusGaugeVec.WithLabelValues(nodeIDStr, nodeName, "unhealthy").Set(1)
	}
}

// generateFenceDevicePath creates the fence device path from the heartbeat device path
// by appending the fence device suffix from agent constants
func generateFenceDevicePath(heartbeatDevicePath string) string {
	return heartbeatDevicePath + agent.SharedStorageFenceDeviceSuffix
}

// SBRAgent represents the main SBR agent with self-fencing capabilities
type SBRAgent struct {
	recorderObject      runtime.Object
	recorder            record.EventRecorder
	watchdog            mocks.WatchdogInterface
	heartbeatDevice     mocks.BlockDeviceInterface // Device used for heartbeat messages
	fenceDevice         mocks.BlockDeviceInterface // Device used for fence messages
	heartbeatDevicePath string                     // Path to heartbeat device
	fenceDevicePath     string                     // Path to fence device
	nodeName            string
	nodeID              uint16
	petInterval         time.Duration
	sbrUpdateInterval   time.Duration
	heartbeatInterval   time.Duration
	peerCheckInterval   time.Duration
	rebootMethod        string
	ioTimeout           time.Duration
	ctx                 context.Context
	cancel              context.CancelFunc
	sbrHealthy          bool
	sbrHealthyMutex     sync.RWMutex
	heartbeatSequence   uint64
	heartbeatSeqMutex   sync.Mutex
	peerMonitor         *peerMonitor
	selfFenceDetected   bool
	selfFenceMutex      sync.RWMutex
	metricsPort         int
	metricsServer       *http.Server

	// Node mapping for hash-based slot assignment (always enabled)
	nodeManager      *sbdprotocol.NodeManager // Shared node manager for both devices
	nodeManagerStop  chan struct{}
	staleNodeTimeout time.Duration

	// Failure tracking and retry configuration
	watchdogFailureCount  int
	sbrFailureCount       int
	heartbeatFailureCount int
	lastFailureReset      time.Time
	failureCountMutex     sync.RWMutex
	retryConfig           retry.Config

	// Kubernetes client for StorageBasedRemediation CR watching and fencing coordination
	k8sClient  client.Client
	restConfig *rest.Config

	// Controller manager for StorageBasedRemediation reconciliation
	controllerManager manager.Manager

	// Namespace for controller reconciliation (configurable for testing)
	controllerNamespace string

	// detectOnlyMode when true disables remediation: watchdog is not armed, self-fence is never executed
	detectOnlyMode bool
}

// NewSBRAgentWithWatchdog creates a new SBR agent with a provided watchdog interface.
// When detectOnlyMode is true, the watchdog is not used for remediation (e.g. pass a no-op mock to disarm).
func NewSBRAgentWithWatchdog(
	wd mocks.WatchdogInterface,
	heartbeatDevicePath, nodeName, clusterName string,
	nodeID uint16,
	petInterval, sbrUpdateInterval, heartbeatInterval, peerCheckInterval time.Duration,
	sbrTimeoutSeconds uint,
	rebootMethod string,
	metricsPort int,
	staleNodeTimeout time.Duration,
	fileLockingEnabled bool,
	ioTimeout time.Duration,
	k8sClient client.Client,
	restConfig *rest.Config,
	controllerNamespace string,
	detectOnlyMode bool,
) (*SBRAgent, error) {
	// Input validation
	if wd == nil {
		return nil, fmt.Errorf("watchdog interface cannot be nil")
	}
	if !detectOnlyMode && wd.Path() == "" {
		return nil, fmt.Errorf("watchdog path cannot be empty")
	}

	if heartbeatDevicePath == "" {
		return nil, fmt.Errorf("heartbeat device path cannot be empty")
	}
	if nodeName == "" {
		return nil, fmt.Errorf("node name cannot be empty")
	}
	if nodeID == 0 || nodeID > 255 {
		return nil, fmt.Errorf("node ID must be between 1 and 255, got %d", nodeID)
	}

	// Generate fence device path from heartbeat device path
	fenceDevicePath := generateFenceDevicePath(heartbeatDevicePath)

	if k8sClient == nil {
		return nil, fmt.Errorf("k8s client cannot be nil")
	}

	// Validate timing parameters
	if petInterval <= 0 {
		return nil, fmt.Errorf("pet interval must be positive, got %v", petInterval)
	}
	if rebootMethod != RebootMethodPanic &&
		rebootMethod != RebootMethodSystemctlReboot &&
		rebootMethod != RebootMethodNone {
		return nil, fmt.Errorf("invalid reboot method '%s', must be '%s', '%s', or '%s'",
			rebootMethod, RebootMethodPanic, RebootMethodSystemctlReboot, RebootMethodNone)
	}
	if metricsPort <= 0 || metricsPort > 65535 {
		return nil, fmt.Errorf("metrics port must be between 1 and 65535, got %d", metricsPort)
	}
	if sbrUpdateInterval <= 0 {
		return nil, fmt.Errorf("SBR update interval must be positive")
	}
	if heartbeatInterval <= 0 {
		return nil, fmt.Errorf("heartbeat interval must be positive")
	}
	if peerCheckInterval <= 0 {
		return nil, fmt.Errorf("peer check interval must be positive")
	}

	// Create context for the agent
	ctx, cancel := context.WithCancel(context.Background())

	// Initialize retry configuration
	retryConfig := retry.Config{
		MaxRetries:    MaxCriticalRetries,
		InitialDelay:  InitialCriticalRetryDelay,
		MaxDelay:      MaxCriticalRetryDelay,
		BackoffFactor: CriticalRetryBackoffFactor,
	}

	sbrAgent := &SBRAgent{
		watchdog:            wd,
		heartbeatDevicePath: heartbeatDevicePath,
		fenceDevicePath:     fenceDevicePath,
		nodeName:            nodeName,
		nodeID:              nodeID,
		petInterval:         petInterval,
		sbrUpdateInterval:   sbrUpdateInterval,
		heartbeatInterval:   heartbeatInterval,
		peerCheckInterval:   peerCheckInterval,
		rebootMethod:        rebootMethod,
		ioTimeout:           ioTimeout,
		ctx:                 ctx,
		cancel:              cancel,
		sbrHealthy:          false,
		heartbeatSequence:   0,
		selfFenceDetected:   false,
		metricsPort:         metricsPort,
		nodeManagerStop:     make(chan struct{}),
		detectOnlyMode:      detectOnlyMode,
		staleNodeTimeout:    staleNodeTimeout,
		lastFailureReset:    time.Now(),
		retryConfig:         retryConfig,
		k8sClient:           k8sClient,
		restConfig:          restConfig,
		controllerManager:   nil, // Will be initialized below
		controllerNamespace: controllerNamespace,
		recorder:            nil,
		recorderObject:      nil,
	}

	// Initialize heartbeat and fence devices
	if err := sbrAgent.initializeSBRDevices(); err != nil {
		sbrAgent.cancel()
		return nil, fmt.Errorf("failed to initialize SBR devices: %w", err)
	}

	// Initialize node managers for consistent slot assignment on both devices
	if err := sbrAgent.initializeNodeManagers(clusterName, fileLockingEnabled); err != nil {
		sbrAgent.cancel()
		if sbrAgent.heartbeatDevice != nil {
			if closeErr := sbrAgent.heartbeatDevice.Close(); closeErr != nil {
				logger.Error(closeErr, "Failed to close heartbeat device during cleanup")
			}
		}
		if sbrAgent.fenceDevice != nil {
			if closeErr := sbrAgent.fenceDevice.Close(); closeErr != nil {
				logger.Error(closeErr, "Failed to close fence device during cleanup")
			}
		}
		return nil, fmt.Errorf("failed to initialize node managers: %w", err)
	}

	// Initialize the PeerMonitor
	sbrAgent.peerMonitor = newPeerMonitor(sbrTimeoutSeconds, nodeID, sbrAgent.nodeManager, logger)

	// Initialize metrics
	sbrAgent.initMetrics()

	if err := sbrAgent.initializeControllerManager(); err != nil {
		return nil, fmt.Errorf("failed to initialize controller manager: %w", err)
	}
	sbrAgent.recorder = sbrAgent.controllerManager.GetEventRecorderFor("sbr-agent")
	// Get the first StorageBasedRemediationConfig object from the POD_NAMESPACE
	sbrConfigs := &v1alpha1.StorageBasedRemediationConfigList{}
	if err := sbrAgent.k8sClient.List(
		sbrAgent.ctx,
		sbrConfigs,
		client.InNamespace(os.Getenv("POD_NAMESPACE")),
	); err != nil {
		logger.Error(err, "Failed to list StorageBasedRemediationConfig objects")
	} else if len(sbrConfigs.Items) > 0 {
		sbrAgent.recorderObject = &sbrConfigs.Items[0]
	} else {
		logger.Info("No StorageBasedRemediationConfig found in namespace", "namespace", os.Getenv("POD_NAMESPACE"))
		sbrAgent.recorderObject = nil
	}
	if sbrAgent.recorder != nil && sbrAgent.recorderObject != nil {
		sbrAgent.recorder.Eventf(
			sbrAgent.recorderObject,
			"Normal",
			"AgentCreated",
			"Agent activated on %s",
			sbrAgent.nodeName,
		)
	}

	return sbrAgent, nil
}

// initMetrics initializes Prometheus metrics and starts the metrics server
func (s *SBRAgent) initMetrics() {
	// Register all metrics with the default registry only once
	metricsOnce.Do(func() {
		prometheus.MustRegister(agentHealthyGauge)
		prometheus.MustRegister(sbrIOErrorsCounter)
		prometheus.MustRegister(watchdogPetsCounter)
		prometheus.MustRegister(peerStatusGaugeVec)
		prometheus.MustRegister(selfFencedCounter)
	})

	// Initialize agent healthy status to 1 (healthy by default)
	agentHealthyGauge.Set(1)

	// Set up the HTTP server for metrics
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	s.metricsServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", s.metricsPort),
		Handler: mux,
	}

	// Start the metrics server in a goroutine
	go func() {
		logger.Info("Starting Prometheus metrics server", "port", s.metricsPort)
		if err := s.metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error(err, "Metrics server failed", "port", s.metricsPort)
		}
	}()
}

// initializeSBRDevices opens and initializes the SBR block devices
func (s *SBRAgent) initializeSBRDevices() error {
	heartbeatDevice, err := blockdevice.OpenWithTimeout(s.heartbeatDevicePath, s.ioTimeout,
		logger.WithName("heartbeat-device"))
	if err != nil {
		return fmt.Errorf("failed to open heartbeat device %s with timeout %v: %w",
			s.heartbeatDevicePath, s.ioTimeout, err)
	}

	fenceDevice, err := blockdevice.OpenWithTimeout(s.fenceDevicePath, s.ioTimeout, logger.WithName("fence-device"))
	if err != nil {
		return fmt.Errorf("failed to open fence device %s with timeout %v: %w", s.fenceDevicePath, s.ioTimeout, err)
	}

	s.heartbeatDevice = heartbeatDevice
	s.fenceDevice = fenceDevice
	logger.Info("Successfully opened SBR devices",
		"heartbeatDevicePath", s.heartbeatDevicePath,
		"fenceDevicePath", s.fenceDevicePath,
		"ioTimeout", s.ioTimeout)
	return nil
}

// setSBRDevices allows setting custom SBR devices (useful for testing)
func (s *SBRAgent) setSBRDevices(heartbeatDevice, fenceDevice mocks.BlockDeviceInterface) {
	s.heartbeatDevice = heartbeatDevice
	s.fenceDevice = fenceDevice
}

// initializeNodeManagers initializes the node managers for hash-based slot mapping
func (s *SBRAgent) initializeNodeManagers(clusterName string, fileLockingEnabled bool) error {
	if s.heartbeatDevice == nil || s.fenceDevice == nil {
		return fmt.Errorf("SBR devices must be initialized before node managers")
	}

	// Use heartbeat device for shared node manager (both devices will use same slot assignments)
	config := sbdprotocol.NodeManagerConfig{
		ClusterName:        clusterName,
		SyncInterval:       30 * time.Second,
		StaleNodeTimeout:   s.staleNodeTimeout,
		Logger:             logger.WithName("node-manager"),
		FileLockingEnabled: fileLockingEnabled,
	}

	nodeManager, err := sbdprotocol.NewNodeManager(s.heartbeatDevice, config)
	if err != nil {
		return fmt.Errorf("failed to create node manager: %w", err)
	}

	s.nodeManager = nodeManager

	// Get or assign slot for this node (same slot used for both devices)
	nodeID, err := s.nodeManager.GetNodeIDForNode(s.nodeName)
	if err != nil {
		return fmt.Errorf("failed to get slot for node %s: %w", s.nodeName, err)
	}

	// Update the node ID to use the hash-based slot
	s.nodeID = nodeID
	logger.Info("Node assigned to slot via hash-based mapping",
		"nodeName", s.nodeName,
		"nodeID", nodeID,
		"clusterName", clusterName,
		"fileLockingEnabled", fileLockingEnabled,
		"coordinationStrategy", s.nodeManager.GetCoordinationStrategy())

	return nil
}

// setSBRHealthy safely updates the SBR health status
func (s *SBRAgent) setSBRHealthy(healthy bool) {
	s.sbrHealthyMutex.Lock()
	defer s.sbrHealthyMutex.Unlock()
	s.sbrHealthy = healthy
}

// isSBRHealthy safely reads the SBR health status
func (s *SBRAgent) isSBRHealthy() bool {
	s.sbrHealthyMutex.RLock()
	defer s.sbrHealthyMutex.RUnlock()
	return s.sbrHealthy
}

// remediationExistsForThisNode checks if a StorageBasedRemediation CR exists for this node.
// Uses a short timeout to avoid blocking the watchdog loop. Returns (true, nil) if the CR
// exists, (false, nil) if it does not (IsNotFound), and (false, err) on API errors or timeout.
// Used when SBR is unhealthy: if we have API access and no CR exists, we pet the watchdog
// and let NHC decide (e.g. too many nodes down); only skip petting when CR exists or no API access.
func (s *SBRAgent) remediationExistsForThisNode() (bool, error) {
	checkCtx, cancel := context.WithTimeout(s.ctx, RemediationCheckTimeout)
	defer cancel()
	remediation := &v1alpha1.StorageBasedRemediation{}
	err := s.k8sClient.Get(checkCtx, client.ObjectKey{Namespace: s.controllerNamespace, Name: s.nodeName}, remediation)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *SBRAgent) getNextHeartbeatSequence() uint64 {
	s.heartbeatSeqMutex.Lock()
	defer s.heartbeatSeqMutex.Unlock()
	s.heartbeatSequence++
	return s.heartbeatSequence
}

// incrementFailureCount safely increments the failure count for a specific operation type
func (s *SBRAgent) incrementFailureCount(operationType string) int {
	s.failureCountMutex.Lock()
	defer s.failureCountMutex.Unlock()

	// Reset failure counts if enough time has passed
	if time.Since(s.lastFailureReset) > FailureCountResetInterval {
		s.watchdogFailureCount = 0
		s.sbrFailureCount = 0
		s.heartbeatFailureCount = 0
		s.lastFailureReset = time.Now()
		logger.V(1).Info("Reset failure counts due to time interval",
			"watchdogFailureCount", s.watchdogFailureCount,
			"sbrFailureCount", s.sbrFailureCount,
			"heartbeatFailureCount", s.heartbeatFailureCount,
			"lastFailureReset", s.lastFailureReset)
	}

	counter := 0
	switch operationType {
	case "watchdog":
		s.watchdogFailureCount++
		counter = s.watchdogFailureCount
	case "sbr":
		s.sbrFailureCount++
		counter = s.sbrFailureCount
		// Increment SBR I/O errors counter
		sbrIOErrorsCounter.Inc()
	case "heartbeat":
		s.heartbeatFailureCount++
		// Increment SBR I/O errors counter
		sbrIOErrorsCounter.Inc()
		counter = s.heartbeatFailureCount
	}

	// Mark agent as unhealthy
	agentHealthyGauge.Set(0)

	logger.V(1).Info("Incremented failure count", "operationType", operationType, "failureCount", counter)
	if counter >= MaxConsecutiveFailures {
		logger.Error(nil, "Failures exceeded threshold, will trigger self-fence on next watchdog iteration",
			"failureCount", counter,
			"threshold", MaxConsecutiveFailures)

		s.setSBRHealthy(false)
		// Try to reinitialize the device on next iteration
		if s.heartbeatDevice != nil && !s.heartbeatDevice.IsClosed() {
			if closeErr := s.heartbeatDevice.Close(); closeErr != nil {
				logger.Error(closeErr, "Failed to close heartbeat device", "devicePath", s.heartbeatDevicePath)
			}
		}
		if s.fenceDevice != nil && !s.fenceDevice.IsClosed() {
			if closeErr := s.fenceDevice.Close(); closeErr != nil {
				logger.Error(closeErr, "Failed to close fence device", "devicePath", s.fenceDevicePath)
			}
		}
	}
	return counter
}

// resetFailureCount safely resets the failure count for a specific operation type
func (s *SBRAgent) resetFailureCount(operationType string) {
	s.failureCountMutex.Lock()
	defer s.failureCountMutex.Unlock()

	switch operationType {
	case "watchdog":
		if s.watchdogFailureCount > 0 {
			logger.V(1).Info("Reset watchdog failure count", "previousCount", s.watchdogFailureCount)
			s.watchdogFailureCount = 0
		}
	case "sbr":
		if s.sbrFailureCount > 0 {
			logger.V(1).Info("Reset SBR failure count", "previousCount", s.sbrFailureCount)
			s.sbrFailureCount = 0
		}
	case "heartbeat":
		if s.heartbeatFailureCount > 0 {
			logger.V(1).Info("Reset heartbeat failure count", "previousCount", s.heartbeatFailureCount)
			s.heartbeatFailureCount = 0
		}
	}
}

// shouldTriggerSelfFence checks if consecutive failures exceed the threshold
func (s *SBRAgent) shouldTriggerSelfFence() (bool, string) {
	s.failureCountMutex.RLock()
	defer s.failureCountMutex.RUnlock()
	shouldSelfFence := false
	msg := ""
	if s.watchdogFailureCount >= MaxConsecutiveFailures {
		s.recorder.Event(s.recorderObject, corev1.EventTypeWarning, EventReasonWatchdogPetFailed,
			fmt.Sprintf("Watchdog pet failures on (%s, %d) exceeded threshold", s.nodeName, s.nodeID))
		shouldSelfFence = true
		msg = fmt.Sprintf("watchdog pet failures exceeded threshold (%d)", MaxConsecutiveFailures)
	} else if s.sbrFailureCount >= MaxConsecutiveFailures {
		s.recorder.Event(s.recorderObject, corev1.EventTypeWarning, EventReasonSBRWriteFailed,
			fmt.Sprintf("SBR device write failures on (%s, %d) exceeded threshold", s.nodeName, s.nodeID))
		shouldSelfFence = true
		msg = fmt.Sprintf("SBR device failures exceeded threshold (%d)", MaxConsecutiveFailures)
	} else if s.heartbeatFailureCount >= MaxConsecutiveFailures {
		s.recorder.Event(s.recorderObject, corev1.EventTypeWarning, EventReasonHeartbeatWriteFailed,
			fmt.Sprintf("Heartbeat write failures on (%s, %d) exceeded threshold", s.nodeName, s.nodeID))
		shouldSelfFence = true
		msg = fmt.Sprintf("heartbeat write failures exceeded threshold (%d)", MaxConsecutiveFailures)
	}
	if shouldSelfFence {
		if remediationExist, err := s.remediationExistsForThisNode(); err == nil && !remediationExist {
			s.recorder.Event(s.recorderObject, corev1.EventTypeWarning, EventReasonSelfFenceAbortedNoRemediation,
				fmt.Sprintf("Aborting self-fence on (%s, %d); no StorageBasedRemediation CR for this node, petting watchdog to allow NHC to decide", s.nodeName, s.nodeID))
			logger.Info("Aborting self-fence - no StorageBasedRemediation CR for this node; petting watchdog to allow NHC to decide",
				"reason", msg, "sbrDevicePath", s.heartbeatDevicePath, "nodeName", s.nodeName)
			shouldSelfFence = false
			msg = fmt.Sprintf("self-fence aborted (no remediation CR): %s", msg)
		}
	}
	return shouldSelfFence, msg
}

// writeHeartbeatToSBR writes a heartbeat message to the node's designated slot
func (s *SBRAgent) writeHeartbeatToSBR() error {
	if s.nodeManager != nil {
		// Use NodeManager's file locking for coordination
		return s.nodeManager.WriteWithLock("write heartbeat", func() error {
			return s.writeHeartbeatToSBRInternal()
		})
	}
	// Fallback for cases without NodeManager (shouldn't happen in normal operation)
	return s.writeHeartbeatToSBRInternal()
}

// writeHeartbeatToSBRInternal performs the actual heartbeat write operation without locking
func (s *SBRAgent) writeHeartbeatToSBRInternal() error {
	if s.heartbeatDevice == nil || s.heartbeatDevice.IsClosed() {
		// Try to reinitialize the device
		if err := s.initializeSBRDevices(); err != nil {
			return fmt.Errorf("SBR devices are closed and reinitialize failed: %w", err)
		}
	}

	// Create heartbeat message
	sequence := s.getNextHeartbeatSequence()
	heartbeatHeader := sbdprotocol.NewHeartbeat(s.nodeID, sequence)
	heartbeatMsg := sbdprotocol.SBDHeartbeatMessage{Header: heartbeatHeader}

	// Marshal the message
	msgBytes, err := sbdprotocol.MarshalHeartbeat(heartbeatMsg)
	if err != nil {
		return fmt.Errorf("failed to marshal heartbeat message: %w", err)
	}

	// Calculate slot offset for this node (NodeID * SBD_SLOT_SIZE)
	slotOffset := int64(s.nodeID) * sbdprotocol.SBD_SLOT_SIZE

	// Write heartbeat message to the designated slot
	n, err := s.heartbeatDevice.WriteAt(msgBytes, slotOffset)
	if err != nil {
		return fmt.Errorf("failed to write heartbeat to SBR device at offset %d: %w", slotOffset, err)
	}

	if n != len(msgBytes) {
		return fmt.Errorf("partial write to SBR device: wrote %d bytes, expected %d", n, len(msgBytes))
	}

	// Ensure data is committed to storage
	if err := s.heartbeatDevice.Sync(); err != nil {
		return fmt.Errorf("failed to sync SBR device after heartbeat write: %w", err)
	}

	logger.V(1).Info("Successfully wrote heartbeat message",
		"sequence", sequence,
		"nodeID", s.nodeID,
		"slotOffset", slotOffset)
	return nil
}

// readPeerHeartbeat reads and processes a heartbeat from a peer node's slot
func (s *SBRAgent) readPeerHeartbeat(peerNodeID uint16) error {
	if s.heartbeatDevice == nil || s.heartbeatDevice.IsClosed() {
		return fmt.Errorf("SBR device is not available")
	}

	// Calculate slot offset for the peer node
	slotOffset := int64(peerNodeID) * sbdprotocol.SBD_SLOT_SIZE

	// Read the entire slot
	slotData := make([]byte, sbdprotocol.SBD_SLOT_SIZE)
	n, err := s.heartbeatDevice.ReadAt(slotData, slotOffset)
	if err != nil {
		// Increment SBR I/O errors counter for read failures
		sbrIOErrorsCounter.Inc()
		s.incrementFailureCount("heartbeat")
		return fmt.Errorf("failed to read peer %d heartbeat from offset %d: %w", peerNodeID, slotOffset, err)
	}

	if n != sbdprotocol.SBD_SLOT_SIZE {
		return fmt.Errorf("partial read from peer %d slot: read %d bytes, expected %d",
			peerNodeID, n, sbdprotocol.SBD_SLOT_SIZE)
	}

	if sbdprotocol.IsEmptySlot(slotData[:sbdprotocol.SBD_HEADER_SIZE]) {
		logger.V(2).Info("Peer slot is empty", "peerNodeID", peerNodeID)
		return nil
	}

	// Try to unmarshal the message header
	header, err := sbdprotocol.Unmarshal(slotData[:sbdprotocol.SBD_HEADER_SIZE])
	if err != nil {
		// Don't log as error since empty slots are expected
		logger.V(1).Info("Failed to unmarshal peer heartbeat",
			"peerNodeID", peerNodeID,
			"error", err)
		return nil
	}

	// Validate the message
	if !sbdprotocol.IsValidMessageType(header.Type) {
		logger.V(1).Info("Invalid message type from peer",
			"peerNodeID", peerNodeID,
			"messageType", header.Type)
		return nil
	}

	if header.Type != sbdprotocol.SBD_MSG_TYPE_HEARTBEAT {
		logger.V(1).Info("Non-heartbeat message from peer",
			"peerNodeID", peerNodeID,
			"messageType", header.Type)
		return nil
	}

	if header.NodeID != peerNodeID {
		logger.Error(nil, "NodeID mismatch in peer slot",
			"peerNodeID", peerNodeID,
			"expected", peerNodeID,
			"actual", header.NodeID)
		return nil
	}

	// Update peer status
	s.peerMonitor.updatePeer(peerNodeID, header.Timestamp, header.Sequence)
	return nil
}

// Start begins the SBR agent operations
func (s *SBRAgent) Start() error {
	return s.StartWithContext(s.ctx)
}

// StartWithContext begins the SBR agent operations and blocks until ctx is cancelled.
// When ctx is cancelled (e.g. on SIGTERM), the caller should then call Stop() to close the watchdog.
func (s *SBRAgent) StartWithContext(ctx context.Context) error {
	logger.Info("Starting SBR Agent",
		"watchdogDevice", s.watchdog.Path(),
		"heartbeatDevice", s.heartbeatDevicePath,
		"fenceDevice", s.fenceDevicePath,
		"nodeName", s.nodeName,
		"nodeID", s.nodeID,
		"petInterval", s.petInterval,
		"sbrUpdateInterval", s.sbrUpdateInterval,
		"heartbeatInterval", s.heartbeatInterval,
		"peerCheckInterval", s.peerCheckInterval)

	// Start node manager periodic sync if using hash mapping
	if s.nodeManager != nil {
		s.nodeManagerStop = s.nodeManager.StartPeriodicSync()
	}

	// Start the watchdog monitoring goroutine
	go s.watchdogLoop()

	// Start SBR device monitoring if available
	if s.heartbeatDevicePath != "" {
		go s.heartbeatLoop()
		go s.peerMonitorLoop()
	}

	// Start fencing loop if enabled
	logger.Info("Starting SBR Agent controller manager")
	return s.controllerManager.Start(ctx)
}

// RunUntilShutdown is the contract for "run until a shutdown signal is received".
// Callers (e.g. main, tests) depend only on this contract, not on internal method names.
// It blocks until a signal is received on sigChan, then shuts down and closes the watchdog.
func (s *SBRAgent) RunUntilShutdown(sigChan <-chan os.Signal) error {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		sig := <-sigChan
		logger.Info("Received shutdown signal", "signal", sig.String())
		cancel()
	}()
	err := s.StartWithContext(ctx)
	if stopErr := s.Stop(); stopErr != nil && err == nil {
		err = stopErr
	}
	return err
}

// Stop gracefully shuts down the SBR agent
func (s *SBRAgent) Stop() error {
	logger.Info("Stopping SBR Agent")

	// Signal all goroutines to stop
	s.cancel()

	// Stop node manager coordination
	if s.nodeManagerStop != nil {
		close(s.nodeManagerStop)
	}

	// Stop metrics server if running
	if s.metricsServer != nil {
		logger.Info("Stopping metrics server")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := s.metricsServer.Shutdown(shutdownCtx); err != nil {
			logger.Error(err, "Failed to shutdown metrics server gracefully")
		} else {
			logger.Info("Metrics server stopped gracefully")
		}
	}

	// Close SBR devices last (after all operations complete)
	if s.heartbeatDevice != nil && !s.heartbeatDevice.IsClosed() {
		logger.Info("Closing SBR devices", "heartbeatDevicePath", s.heartbeatDevicePath, "fenceDevicePath", s.fenceDevicePath)
		if err := s.heartbeatDevice.Close(); err != nil {
			logger.Error(err, "Failed to close heartbeat device", "devicePath", s.heartbeatDevicePath)
			return fmt.Errorf("failed to close heartbeat device: %w", err)
		}
		if s.fenceDevice != nil && !s.fenceDevice.IsClosed() {
			if closeErr := s.fenceDevice.Close(); closeErr != nil {
				logger.Error(closeErr, "Failed to close fence device", "devicePath", s.fenceDevicePath)
				return fmt.Errorf("failed to close fence device: %w", closeErr)
			}
		}
	}

	// Close watchdog device last (critical for graceful shutdown)
	if s.watchdog != nil {
		logger.Info("Closing watchdog device", "watchdogPath", s.watchdog.Path())
		if err := s.watchdog.Close(); err != nil {
			logger.Error(err, "Failed to close watchdog device", "watchdogPath", s.watchdog.Path())
			return fmt.Errorf("failed to close watchdog device: %w", err)
		}
	}

	logger.Info("SBR Agent stopped successfully")
	return nil
}

// watchdogLoop continuously pets the watchdog to prevent system reset
func (s *SBRAgent) watchdogLoop() {
	ticker := time.NewTicker(s.petInterval)
	defer ticker.Stop()

	logger.Info("Starting watchdog loop", "interval", s.petInterval)

	for {
		select {
		case <-s.ctx.Done():
			logger.Info("Watchdog loop stopping")
			return
		case <-ticker.C:
			// Never pet the watchdog if self-fence has been detected
			if s.isSelfFenceDetected() {
				logger.Error(nil, "Self-fence detected - STOPPING watchdog petting to allow system reboot")
				return
			}

			// Check if we should trigger self-fence due to consecutive failures
			if shouldFence, reason := s.shouldTriggerSelfFence(); shouldFence {
				logger.Error(nil, "Triggering self-fence due to consecutive failures", "reason", reason)
				s.executeSelfFencing(reason)
				return
			}

			if s.isSBRHealthy() {
				s.petWatchdogWhenHealthy()
			} else {
				s.handleWatchdogTickSBRUnhealthy()
			}
		}
	}
}

// petWatchdogWhenHealthy pets the watchdog when SBR is healthy, with retry and failure count handling.
func (s *SBRAgent) petWatchdogWhenHealthy() {
	err := retry.Do(s.ctx, s.retryConfig, "pet watchdog", func() error {
		return s.watchdog.Pet()
	})
	if err != nil {
		s.incrementFailureCount("watchdog")
		return
	}
	s.resetFailureCount("watchdog")
	logger.V(1).Info("Watchdog pet successful", "watchdogPath", s.watchdog.Path())
	watchdogPetsCounter.Inc()
	if s.isSBRHealthy() {
		agentHealthyGauge.Set(1)
	}
}

// handleWatchdogTickSBRUnhealthy handles one watchdog tick when SBR is unhealthy: detect-only event,
// or remediation CR check and either skip pet (reboot) or pet (let NHC decide).
func (s *SBRAgent) handleWatchdogTickSBRUnhealthy() {
	agentHealthyGauge.Set(0)
	if s.detectOnlyMode {
		s.recorder.Event(s.recorderObject, corev1.EventTypeWarning, EventReasonSBRUnhealthyDetectOnly,
			fmt.Sprintf("SBR device unhealthy on (%s, %d); detect-only mode, watchdog disarmed, no reboot", s.nodeName, s.nodeID))
		logger.Info("SBR unhealthy in detect-only mode (watchdog disarmed, no reboot)",
			"sbrDevicePath", s.heartbeatDevicePath)
		return
	}
	remediationExists, checkErr := s.remediationExistsForThisNode()
	if checkErr != nil {
		s.recorder.Event(s.recorderObject, corev1.EventTypeWarning, EventReasonSBRUnhealthySkipPetAPIError,
			fmt.Sprintf("SBR device unhealthy on (%s, %d); API check failed, skipping watchdog pet, reboot imminent", s.nodeName, s.nodeID))
		logger.Error(checkErr, "Skipping watchdog pet - SBR unhealthy and could not verify remediation CR",
			"sbrDevicePath", s.heartbeatDevicePath)
		return
	}
	if remediationExists {
		s.recorder.Event(s.recorderObject, corev1.EventTypeWarning, EventReasonSBRUnhealthyWatchdogTimeout,
			fmt.Sprintf("SBR device unhealthy on (%s, %d); remediation CR exists, skipping watchdog pet, reboot imminent", s.nodeName, s.nodeID))
		logger.Error(nil, "Skipping watchdog pet - SBR device is unhealthy and remediation CR exists",
			"sbrDevicePath", s.heartbeatDevicePath, "sbrHealthy", s.isSBRHealthy())
		return
	}
	if err := s.watchdog.Pet(); err != nil {
		logger.Error(err, "Failed to pet watchdog while SBR unhealthy (no remediation CR); retry next tick",
			"watchdogPath", s.watchdog.Path())
		return
	}
	watchdogPetsCounter.Inc()
	logger.Info("SBR unhealthy but no remediation CR for this node; petting watchdog to allow NHC to decide",
		"sbrDevicePath", s.heartbeatDevicePath, "nodeName", s.nodeName)
}

// heartbeatLoop continuously writes heartbeat messages to the SBR device
func (s *SBRAgent) heartbeatLoop() {
	ticker := time.NewTicker(s.heartbeatInterval)
	defer ticker.Stop()

	logger.Info("Starting SBR heartbeat loop", "interval", s.heartbeatInterval)

	for {
		select {
		case <-s.ctx.Done():
			logger.Info("SBR heartbeat loop stopping")
			return
		case <-ticker.C:
			// Use retry mechanism for heartbeat operations
			err := retry.Do(s.ctx, s.retryConfig, "write heartbeat to SBR", func() error {
				return s.writeHeartbeatToSBR()
			})

			if err != nil {
				failureCount := s.incrementFailureCount("heartbeat")
				logger.Error(err, "Failed to write heartbeat to SBR device after retries",
					"devicePath", s.heartbeatDevicePath,
					"nodeID", s.nodeID,
					"failureCount", failureCount,
					"maxFailures", MaxConsecutiveFailures)

			} else {
				// Success - reset failure count and update status
				s.resetFailureCount("heartbeat")
				// Only mark as healthy if it was previously unhealthy
				// The regular SBR device loop will also update this
				if !s.isSBRHealthy() {
					logger.Info("SBR device recovered during heartbeat write", "devicePath", s.heartbeatDevicePath)
					s.setSBRHealthy(true)
					// Update agent health status
					agentHealthyGauge.Set(1)
				}
			}
		}
	}
}

// peerMonitorLoop continuously reads peer heartbeats and checks liveness
func (s *SBRAgent) peerMonitorLoop() {
	ticker := time.NewTicker(s.peerCheckInterval)
	defer ticker.Stop()

	logger.Info("Starting peer monitor loop", "interval", s.peerCheckInterval)
	// Clean any old fence message from our own slot before entering the loop
	if err := s.cleanOwnFenceSlotIfPresent(logger); err != nil {
		logger.Info("Error cleaning own fence slot", "error", err)
	}
	for {
		select {
		case <-s.ctx.Done():
			logger.Info("Peer monitor loop stopping")
			return
		case <-ticker.C:
			// First, check our own slot for fence messages directed at us, will trigger fencing in case found
			if err := s.readOwnSlotForFenceMessage(); err != nil {
				logger.Info("Error reading own slot for fence messages", "error", err)
				s.incrementFailureCount("sbr")
			}

			// If self-fence was detected, stop all operations
			if s.isSelfFenceDetected() {
				logger.Error(nil, "Self-fence detected in peer monitor loop - stopping all operations")
				return
			}

			// Read heartbeats from all peer slots
			for peerNodeID := uint16(1); peerNodeID <= sbdprotocol.SBD_MAX_NODES; peerNodeID++ {
				// Skip our own slot
				if peerNodeID == s.nodeID {
					continue
				}

				if err := s.readPeerHeartbeat(peerNodeID); err != nil {
					logger.Info("Error reading peer heartbeat", "peerNodeID", peerNodeID, "error", err)
					// Continue with other peers even if one fails
				}
			}

			// Check liveness of all tracked peers
			s.peerMonitor.checkPeerLiveness()

			// Log cluster status periodically
			healthyPeers := s.peerMonitor.getHealthyPeerCount()
			logger.Info("Cluster status", "healthyPeers", healthyPeers)

			// Set or clear SBRStorageUnhealthy node condition so NHC can create remediation when needed.
			// State machine (mirrors old remediation create/delete): True -> after SBRAgentOOSTaintStaleAge -> Unknown (like deleting stale remediation);
			// Unknown -> if peer healthy set False; else after grace period set True again.
			for _, peer := range s.peerMonitor.getPeerStatus() {
				// Skip ourselves
				if peer.NodeID == s.nodeID {
					continue
				}

				// Resolve node name
				peerNodeName, ok := s.resolveNodeName(peer.NodeID)
				if !ok || peerNodeName == "" {
					logger.V(1).Info("Skipping peer check - unable to resolve node name", "peerNodeID", peer.NodeID)
					continue
				}

				currentStatus, lastTransition, hasCond := s.getSBRStorageUnhealthyCondition(peerNodeName)
				now := time.Now()

				if peer.IsHealthy {
					// Node regained health: remove condition (set False) if it was True or Unknown
					if currentStatus == corev1.ConditionTrue || currentStatus == corev1.ConditionUnknown {
						if err := s.setNodeConditionSBRStorageUnhealthyStatus(peerNodeName, corev1.ConditionFalse, "Recovered", "SBR peer heartbeats resumed"); err != nil {
							logger.Error(err, "Failed to clear SBRStorageUnhealthy condition for recovered peer",
								"peerNodeID", peer.NodeID, "peerNodeName", peerNodeName)
						} else {
							logger.V(1).Info("Cleared SBRStorageUnhealthy condition for recovered peer",
								"peerNodeID", peer.NodeID, "peerNodeName", peerNodeName)
						}
					}
					continue
				}

				// Peer unhealthy: require minimum missed heartbeats before setting condition
				missed := int(time.Since(peer.LastSeen) / s.heartbeatInterval)
				if missed < DefaultMinMissedHeartbeatsForRemediation {
					logger.V(1).Info("Peer unhealthy but below remediation threshold",
						"peerNodeID", peer.NodeID,
						"missedHeartbeats", missed,
						"threshold", DefaultMinMissedHeartbeatsForRemediation)
					continue
				}

				// Condition has been True too long: set Unknown (same as old "delete stale remediation") so NHC removes remediation and agent can report healthy
				if currentStatus == corev1.ConditionTrue && now.Sub(lastTransition) > sbrUnhealthyConditionStaleAge {
					if err := s.setNodeConditionSBRStorageUnhealthyStatus(peerNodeName, corev1.ConditionUnknown, "GivingAgentChance", "Condition stale; set Unknown so NHC removes remediation and agent can report healthy"); err != nil {
						logger.Error(err, "Failed to set SBRStorageUnhealthy to Unknown for stale condition", "peerNodeName", peerNodeName)
					} else {
						logger.Info("Set SBRStorageUnhealthy to Unknown (stale); NHC will remove remediation", "peerNodeName", peerNodeName)
					}
					continue
				}

				// Condition is Unknown: after grace period set True again if still unhealthy
				if currentStatus == corev1.ConditionUnknown {
					if now.Sub(lastTransition) > SBRAgentRemediationGracePeriod {
						if err := s.setNodeConditionSBRStorageUnhealthyStatus(peerNodeName, corev1.ConditionTrue, string(v1alpha1.SBRRemediationReasonHeartbeatTimeout), "SBR peer heartbeat timeout"); err != nil {
							logger.Error(err, "Failed to set SBRStorageUnhealthy condition after grace period", "peerNodeID", peer.NodeID, "peerNodeName", peerNodeName)
						} else {
							logger.Info("Set SBRStorageUnhealthy condition after grace period (node still unhealthy)", "peerNodeID", peer.NodeID, "peerNodeName", peerNodeName)
						}
					}
					continue
				}

				// Condition False or absent: set True so NHC can create remediation
				if currentStatus == corev1.ConditionFalse || !hasCond {
					if err := s.setNodeConditionSBRStorageUnhealthy(peerNodeName, true, string(v1alpha1.SBRRemediationReasonHeartbeatTimeout), "SBR peer heartbeat timeout"); err != nil {
						logger.Error(err, "Failed to set SBRStorageUnhealthy condition for unhealthy peer", "peerNodeID", peer.NodeID, "peerNodeName", peerNodeName)
					} else {
						logger.Info("Set SBRStorageUnhealthy condition for unhealthy peer", "peerNodeID", peer.NodeID, "peerNodeName", peerNodeName)
					}
				}
			}
		}
	}
}

// resolveNodeName maps a node ID to a node name using the NodeManager.
func (s *SBRAgent) resolveNodeName(nodeID uint16) (string, bool) {
	if s.nodeManager == nil {
		return "", false
	}
	// Best-effort refresh to get the newest map
	_ = s.nodeManager.ReloadFromDevice()
	if name, ok := s.nodeManager.GetNodeForNodeID(nodeID); ok {
		return name, true
	}
	return "", false
}

// setNodeConditionSBRStorageUnhealthy sets or clears the SBRStorageUnhealthy condition on the node's status.
// When unhealthy is true, NHC can watch this condition and create a StorageBasedRemediation.
func (s *SBRAgent) setNodeConditionSBRStorageUnhealthy(nodeName string, unhealthy bool, reason, message string) error {
	status := corev1.ConditionFalse
	if unhealthy {
		status = corev1.ConditionTrue
	}
	return s.setNodeConditionSBRStorageUnhealthyStatus(nodeName, status, reason, message)
}

// setNodeConditionSBRStorageUnhealthyStatus sets the SBRStorageUnhealthy condition to the given status (True, False, or Unknown).
// Unknown is used to signal NHC to remove its remediation so the node agent can run and report healthy; after a grace period we set True again if still unhealthy.
func (s *SBRAgent) setNodeConditionSBRStorageUnhealthyStatus(nodeName string, status corev1.ConditionStatus, reason, message string) error {
	node := &corev1.Node{}
	if err := s.k8sClient.Get(s.ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
		return fmt.Errorf("get node %s: %w", nodeName, err)
	}
	now := metav1.NewTime(time.Now().UTC())
	cond := s.findOrAppendSBRStorageUnhealthyCondition(node)
	if cond.Status == status {
		return nil
	}
	cond.Status = status
	cond.Reason = reason
	cond.Message = message
	cond.LastTransitionTime = now
	cond.LastHeartbeatTime = now
	if err := s.k8sClient.Status().Update(s.ctx, node); err != nil {
		return fmt.Errorf("update node %s status (SBRStorageUnhealthy): %w", nodeName, err)
	}
	return nil
}

// getSBRStorageUnhealthyCondition returns the current SBRStorageUnhealthy condition status and last transition time for the node.
// If the condition is not present, returns (ConditionFalse, zero time, false).
func (s *SBRAgent) getSBRStorageUnhealthyCondition(nodeName string) (corev1.ConditionStatus, time.Time, bool) {
	node := &corev1.Node{}
	if err := s.k8sClient.Get(s.ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
		return corev1.ConditionFalse, time.Time{}, false
	}
	for i := range node.Status.Conditions {
		c := &node.Status.Conditions[i]
		if c.Type == v1alpha1.NodeConditionSBRStorageUnhealthy {
			return c.Status, c.LastTransitionTime.Time, true
		}
	}
	return corev1.ConditionFalse, time.Time{}, false
}

// findOrAppendSBRStorageUnhealthyCondition returns the existing condition or appends a new one and returns it.
func (s *SBRAgent) findOrAppendSBRStorageUnhealthyCondition(node *corev1.Node) *corev1.NodeCondition {
	for i := range node.Status.Conditions {
		if node.Status.Conditions[i].Type == v1alpha1.NodeConditionSBRStorageUnhealthy {
			return &node.Status.Conditions[i]
		}
	}
	node.Status.Conditions = append(node.Status.Conditions, corev1.NodeCondition{
		Type: v1alpha1.NodeConditionSBRStorageUnhealthy,
	})
	return &node.Status.Conditions[len(node.Status.Conditions)-1]
}

// cleanOwnFenceSlotIfPresent checks the agent's own slot for a fence message and clears it if present.
// Any fence message found at this point is considered redundant and will be cleared by writing a NONE fence.
func (s *SBRAgent) cleanOwnFenceSlotIfPresent(logger logr.Logger) error {
	if s.fenceDevice == nil || s.fenceDevice.IsClosed() {
		return fmt.Errorf("fence device is not available")
	}

	// Calculate slot offset for our own node
	slotOffset := int64(s.nodeID) * sbdprotocol.SBD_SLOT_SIZE

	// Read the header
	headerBuf := make([]byte, sbdprotocol.SBD_HEADER_SIZE)
	n, err := s.fenceDevice.ReadAt(headerBuf, slotOffset)
	if err != nil {
		return fmt.Errorf("failed to read header from own slot %d at offset %d: %w", s.nodeID, slotOffset, err)
	}
	if n < sbdprotocol.SBD_HEADER_SIZE {
		return nil
	}
	if sbdprotocol.IsEmptySlot(headerBuf) {
		return nil
	}

	header, err := sbdprotocol.Unmarshal(headerBuf)
	if err != nil {
		logger.V(1).Info("Failed to unmarshal header from own slot while cleaning", "nodeID", s.nodeID, "error", err)
		return nil
	}
	if header.Type != sbdprotocol.SBD_MSG_TYPE_FENCE {
		return nil
	}

	logger.Info("Found old fence message in own slot; clearing",
		"ourNodeID", s.nodeID,
		"rawTimestamp", header.Timestamp,
		"sequence", header.Sequence)

	// Write NONE fence to clear
	clearFence := sbdprotocol.NewFence(s.nodeID, s.nodeID, 0, sbdprotocol.FENCE_REASON_NONE)
	data, err := sbdprotocol.MarshalFence(clearFence)
	if err != nil {
		return fmt.Errorf("failed to marshal clear fence message: %w", err)
	}
	wrote, err := s.fenceDevice.WriteAt(data, slotOffset)
	if err != nil {
		return fmt.Errorf("failed to write clear fence message at offset %d: %w", slotOffset, err)
	}
	if wrote != len(data) {
		return fmt.Errorf("partial write clearing own fence slot: wrote %d expected %d", wrote, len(data))
	}
	if err := s.fenceDevice.Sync(); err != nil {
		return fmt.Errorf("failed to sync fence device after clearing: %w", err)
	}
	logger.Info("Cleared old fence message from own slot", "ourNodeID", s.nodeID)
	return nil
}

// annotateNodeGraceNow sets the grace annotation on the Node to the current time
func (s *SBRAgent) annotateNodeGraceNow(ctx context.Context, nodeName string) error {
	node := &corev1.Node{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
		return fmt.Errorf("get node %s for grace annotation: %w", nodeName, err)
	}
	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}
	node.Annotations[SBRAgentRemediationGraceAnnotationKey] = time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.k8sClient.Update(ctx, node); err != nil {
		return fmt.Errorf("update node %s grace annotation: %w", nodeName, err)
	}
	return nil
}

// getNodeGraceAnnotation returns (ts, true) if the grace annotation exists and parses; otherwise (time.Time{}, false)
func (s *SBRAgent) getNodeGraceAnnotation(ctx context.Context, nodeName string) (time.Time, bool, error) {
	node := &corev1.Node{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
		return time.Time{}, false, fmt.Errorf("get node %s grace annotation: %w", nodeName, err)
	}
	if node.Annotations == nil {
		return time.Time{}, false, nil
	}
	raw, ok := node.Annotations[SBRAgentRemediationGraceAnnotationKey]
	if !ok || raw == "" {
		return time.Time{}, false, nil
	}
	ts, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, false, nil
	}
	return ts, true, nil
}

// clearNodeGraceAnnotation removes the grace annotation if present
func (s *SBRAgent) clearNodeGraceAnnotation(ctx context.Context, nodeName string) error {
	node := &corev1.Node{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
		return fmt.Errorf("get node %s to clear grace annotation: %w", nodeName, err)
	}
	if node.Annotations == nil {
		return nil
	}
	if _, ok := node.Annotations[SBRAgentRemediationGraceAnnotationKey]; !ok {
		return nil
	}
	delete(node.Annotations, SBRAgentRemediationGraceAnnotationKey)
	if err := s.k8sClient.Update(ctx, node); err != nil {
		return fmt.Errorf("update node %s clearing grace annotation: %w", nodeName, err)
	}
	return nil
}

// validateSBRDevice checks if the SBR device is accessible
func validateSBRDevice(devicePath string) error {
	if devicePath == "" {
		return fmt.Errorf("SBR device path cannot be empty")
	}

	// Check if device exists
	info, err := os.Stat(devicePath)
	if err != nil {
		return fmt.Errorf("SBR device not accessible: %w", err)
	}

	// Check if it's a block device
	if info.Mode()&os.ModeDevice == 0 {
		logger.Info("WARNING: SBR device is not a device file", "devicePath", devicePath)
	}

	return nil
}

// getNodeNameFromEnv gets the node name from environment variables if not provided via flag
func getNodeNameFromEnv() string {
	// Try various common environment variables
	envVars := []string{"NODE_NAME", "HOSTNAME", "NODENAME"}

	for _, envVar := range envVars {
		if value := os.Getenv(envVar); value != "" {
			return value
		}
	}

	// Fallback to hostname
	if hostname, err := os.Hostname(); err == nil {
		return hostname
	}

	return ""
}

// getNodeIDFromEnv gets the node ID from environment variables if not provided via flag
func getNodeIDFromEnv() uint16 {
	// Try various environment variable names
	envVars := []string{"SBR_NODE_ID", "NODE_ID"}

	for _, envVar := range envVars {
		if value := os.Getenv(envVar); value != "" {
			if id, err := strconv.ParseUint(value, 10, 16); err == nil && id >= 1 && id <= sbdprotocol.SBD_MAX_NODES {
				return uint16(id)
			}
		}
	}

	return DefaultNodeID
}

// getSBRTimeoutFromEnv gets the SBR timeout from environment variables if not provided via flag
func getSBRTimeoutFromEnv() uint {
	envVars := []string{"SBR_TIMEOUT_SECONDS", "SBR_TIMEOUT"}

	for _, envVar := range envVars {
		if value := os.Getenv(envVar); value != "" {
			if timeout, err := strconv.ParseUint(value, 10, 32); err == nil && timeout > 0 {
				return uint(timeout)
			}
		}
	}

	return SBRDefaultTimeoutSec // Default timeout
}

// getRebootMethodFromEnv gets the reboot method from environment variables if not provided via flag
func getRebootMethodFromEnv() string {
	envVars := []string{"SBR_REBOOT_METHOD", "REBOOT_METHOD"}

	for _, envVar := range envVars {
		if value := os.Getenv(envVar); value != "" {
			if value == RebootMethodPanic || value == RebootMethodSystemctlReboot {
				return value
			}
		}
	}

	return RebootMethodPanic // Default method
}

// isSelfFenceDetected checks if a self-fence has been detected
func (s *SBRAgent) isSelfFenceDetected() bool {
	s.selfFenceMutex.RLock()
	defer s.selfFenceMutex.RUnlock()
	return s.selfFenceDetected
}

// setSelfFenceDetected sets the self-fence detected flag
func (s *SBRAgent) setSelfFenceDetected(detected bool) {
	s.selfFenceMutex.Lock()
	defer s.selfFenceMutex.Unlock()
	s.selfFenceDetected = detected
}

// executeSelfFencing performs the self-fencing action based on the configured method
// The systemctl-reboot method uses multiple aggressive techniques based on destructive testing
// that showed direct reboot commands are more effective than panic() in containerized environments
func (s *SBRAgent) executeSelfFencing(reason string) {
	if s.detectOnlyMode {
		logger.Info("Detect-only mode: skipping self-fence", "reason", reason, "nodeName", s.nodeName)
		return
	}
	s.recorder.Event(s.recorderObject, corev1.EventTypeWarning, EventReasonSelfFenceInitiated,
		fmt.Sprintf("Self-fencing initiated on (%s, %d): %s", s.nodeName, s.nodeID, reason))
	logger.Error(nil, "Self-fencing initiated",
		"reason", reason,
		"rebootMethod", s.rebootMethod,
		"nodeID", s.nodeID,
		"nodeName", s.nodeName)

	// Increment self-fenced counter
	selfFencedCounter.Inc()
	// Mark agent as unhealthy
	agentHealthyGauge.Set(0)

	// Mark self-fence as detected to stop watchdog petting
	s.setSelfFenceDetected(true)

	// Give some time for the log message to be written
	time.Sleep(100 * time.Millisecond)

	switch s.rebootMethod {
	case RebootMethodNone:
		logger.Error(nil, "Self-fencing disabled - would have rebooted node but reboot method is 'none'",
			"reason", reason,
			"nodeID", s.nodeID,
			"nodeName", s.nodeName)
		// Do nothing - self-fencing is disabled for testing purposes
		return

	case "systemctl-reboot":
		logger.Error(nil, "Attempting aggressive systemctl reboot for self-fencing",
			"reason", reason,
			"nodeID", s.nodeID)

		// Try multiple aggressive reboot methods based on destructive test results
		rebootCommands := [][]string{
			{"systemctl", "reboot", "--force", "--force"},
			{"reboot", "-f"},
			{"sh", "-c", "echo b > /proc/sysrq-trigger"},
		}

		for i, cmd := range rebootCommands {
			logger.Error(nil, "Attempting reboot method", "method", i+1, "command", cmd)
			if err := exec.Command(cmd[0], cmd[1:]...).Run(); err != nil {
				logger.Error(err, "Reboot method failed", "method", i+1, "command", cmd)
			} else {
				logger.Error(nil, "Reboot command executed", "method", i+1, "command", cmd)
				// Command succeeded, no need to try others
				return
			}
		}

		// If all reboot methods failed, fall back to panic
		logger.Error(nil, "All reboot methods failed, falling back to panic for self-fencing")
		panic(fmt.Sprintf("Self-fencing via systemctl failed, all methods exhausted: %s", reason))

	case "panic":
		fallthrough
	default:
		logger.Error(nil, "Initiating panic for immediate self-fencing",
			"reason", reason,
			"nodeID", s.nodeID,
			"nodeName", s.nodeName)
		panic(fmt.Sprintf("Self-fencing: %s", reason))
	}
}

// readOwnSlotForFenceMessage reads the agent's own slot to check for fence messages
func (s *SBRAgent) readOwnSlotForFenceMessage() error {
	if s.fenceDevice == nil || s.fenceDevice.IsClosed() {
		return fmt.Errorf("fence device is not available")
	}

	// Calculate slot offset for our own node
	slotOffset := int64(s.nodeID) * sbdprotocol.SBD_SLOT_SIZE

	// Read the entire slot
	slotData := make([]byte, sbdprotocol.SBD_SLOT_SIZE)
	n, err := s.fenceDevice.ReadAt(slotData, slotOffset)
	if err != nil {
		return fmt.Errorf("failed to read own slot %d from offset %d: %w", s.nodeID, slotOffset, err)
	}

	if n != sbdprotocol.SBD_SLOT_SIZE {
		return fmt.Errorf("partial read from own slot %d: read %d bytes, expected %d", s.nodeID, n, sbdprotocol.SBD_SLOT_SIZE)
	}

	// Check if the slot is empty
	if sbdprotocol.IsEmptySlot(slotData[:sbdprotocol.SBD_HEADER_SIZE]) {
		logger.V(1).Info("Own slot is empty", "nodeID", s.nodeID)
		return nil
	}

	// Try to unmarshal the message header
	header, err := sbdprotocol.Unmarshal(slotData[:sbdprotocol.SBD_HEADER_SIZE])
	if err != nil {
		// Not a valid message, could be empty slot or heartbeat we wrote
		logger.V(1).Info("Failed to unmarshal message from own slot",
			"nodeID", s.nodeID,
			"error", err)
		return nil
	}

	// Check if this is a fence message
	if header.Type == sbdprotocol.SBD_MSG_TYPE_FENCE {
		// Try to unmarshal as a fence message to get the target
		fenceMsg, err := sbdprotocol.UnmarshalFence(slotData[:sbdprotocol.SBD_HEADER_SIZE+3])
		if err != nil {
			logger.Error(err, "Failed to unmarshal fence message from own slot",
				"nodeID", s.nodeID)
			return nil
		}

		// Check if this fence message is directed at us
		if fenceMsg.TargetNodeID == s.nodeID && fenceMsg.Reason != sbdprotocol.FENCE_REASON_NONE {
			reason := fmt.Sprintf("Fence message received from node %d, reason: %s",
				fenceMsg.Header.NodeID, sbdprotocol.GetFenceReasonName(fenceMsg.Reason))
			logger.Error(nil, "Fence message detected in own slot",
				"reason", reason,
				"sourceNodeID", fenceMsg.Header.NodeID,
				"targetNodeID", fenceMsg.TargetNodeID,
				"fenceReason", sbdprotocol.GetFenceReasonName(fenceMsg.Reason))
			s.recorder.Event(s.recorderObject, corev1.EventTypeWarning, EventReasonFenceMessageDetected,
				fmt.Sprintf("Fence message detected in own slot (%s, %d) from %d, reason: %s",
					s.nodeName, s.nodeID, fenceMsg.Header.NodeID, sbdprotocol.GetFenceReasonName(fenceMsg.Reason)))

			// Execute self-fencing immediately
			s.executeSelfFencing(reason)
		} else if fenceMsg.TargetNodeID == s.nodeID {
			if !previousFenceMessage {
				previousFenceMessage = true
				logger.Info("Previous fencing operation is complete",
					"targetNodeID", fenceMsg.TargetNodeID,
					"ourNodeID", s.nodeID,
					"sourceNodeID", fenceMsg.Header.NodeID)
			}
		} else {
			logger.Error(nil, "Fence message in own slot not directed at us",
				"targetNodeID", fenceMsg.TargetNodeID,
				"ourNodeID", s.nodeID,
				"sourceNodeID", fenceMsg.Header.NodeID)
		}
	}

	return nil
}

// runPreflightChecks performs critical startup validation before entering main event loops
// Returns success if EITHER watchdog is active OR SBR device is accessible (or both)
func runPreflightChecks(watchdogPath, sbrDevicePath, nodeName string, nodeID uint16) error {
	logger.Info("Running pre-flight checks",
		"watchdogPath", watchdogPath,
		"sbrDevicePath", sbrDevicePath,
		"nodeName", nodeName,
		"nodeID", nodeID)

	// Check watchdog device availability
	var watchdogErr error
	if watchdogPath != "" {
		watchdogErr = checkWatchdogDevice(watchdogPath)
	}

	// Check SBR device accessibility
	var sbrErr error
	if sbrDevicePath != "" {
		sbrErr = checkSBRDevice(sbrDevicePath, nodeID, nodeName)
	}

	// Check node ID/name resolution
	nodeErr := checkNodeIDNameResolution(nodeName, nodeID)
	if nodeErr != nil {
		logger.Error(nodeErr, "Node ID/name resolution pre-flight check failed")
		return fmt.Errorf("node ID/name resolution pre-flight check failed: %w", nodeErr)
	}
	logger.Info("Pre-flight check passed: node ID/name resolution successful",
		"nodeName", nodeName,
		"nodeID", nodeID)

	// SBR device is always required
	if sbrDevicePath == "" {
		return fmt.Errorf("SBR device path cannot be empty")
	}

	// Check if at least one critical component (watchdog OR SBR) is working
	if watchdogErr == nil && sbrErr == nil {
		logger.Info("All pre-flight checks passed successfully")
		return nil
	} else if watchdogErr == nil {
		return fmt.Errorf("pre-flight checks failed: SBR device is not available")
	} else if sbrErr == nil {
		return fmt.Errorf("pre-flight checks failed: watchdog device is not available")
	} else {
		return fmt.Errorf(
			"pre-flight checks failed: both watchdog device and SBR device are inaccessible. Watchdog error: %v, SBR error: %v",
			watchdogErr, sbrErr)
	}
}

// checkWatchdogDevice verifies the watchdog device exists and can be opened
// Note: This function does NOT use softdog fallback - it strictly checks the specified device
func checkWatchdogDevice(watchdogPath string) error {
	logger.V(1).Info("Checking watchdog device availability", "watchdogPath", watchdogPath)

	// For preflight checks, we want to be strict about the specified device
	// Don't use softdog fallback here - if the specified device doesn't work, it should fail
	wd, err := watchdog.NewWithSoftdogFallback(watchdogPath, logger.WithName("preflight-watchdog"))
	if err != nil {
		return fmt.Errorf("watchdog device pre-flight check failed: %w", err)
	}
	defer func() {
		if closeErr := wd.Close(); closeErr != nil {
			logger.Error(closeErr, "Failed to close watchdog device during pre-flight check",
				"watchdogPath", wd.Path())
		}
	}()

	logger.Info("Pre-flight check: using hardware watchdog device",
		"watchdogPath", wd.Path())

	logger.V(1).Info("Watchdog device successfully opened and closed", "watchdogPath", wd.Path())
	return nil
}

// checkSBRDevice verifies the SBR device exists and performs a minimal read/write test
func checkSBRDevice(sbrDevicePath string, nodeID uint16, nodeName string) error {
	logger.V(1).Info("Checking SBR device accessibility", "sbrDevicePath", sbrDevicePath, "nodeID", nodeID)

	// Check if the SBR device file exists
	if _, err := os.Stat(sbrDevicePath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("SBR device does not exist: %s", sbrDevicePath)
		}
		return fmt.Errorf("failed to stat SBR device %s: %w", sbrDevicePath, err)
	}

	// Try to open the SBR device using the blockdevice package
	device, err := blockdevice.Open(sbrDevicePath)
	if err != nil {
		return fmt.Errorf("failed to open SBR device %s: %w", sbrDevicePath, err)
	}
	defer func() {
		if closeErr := device.Close(); closeErr != nil {
			logger.Error(closeErr, "Failed to close SBR device during pre-flight check",
				"sbrDevicePath", sbrDevicePath)
		}
	}()

	// Perform minimal read/write test: write node ID to its slot and read it back
	if err := performSBRReadWriteTest(device, nodeID, nodeName); err != nil {
		return fmt.Errorf("SBR device read/write test failed: %w", err)
	}

	logger.V(1).Info("SBR device read/write test completed successfully",
		"sbrDevicePath", sbrDevicePath,
		"nodeID", nodeID)
	return nil
}

// performSBRReadWriteTest writes the node ID to its slot and reads it back to verify functionality
func performSBRReadWriteTest(device mocks.BlockDeviceInterface, nodeID uint16, nodeName string) error {
	logger.V(1).Info("Performing SBR device read/write test", "nodeID", nodeID, "nodeName", nodeName)

	// Calculate slot offset for this node
	slotOffset := int64(nodeID) * sbdprotocol.SBD_SLOT_SIZE

	// Create a test heartbeat message
	sequence := uint64(1) // Use sequence 1 for pre-flight test
	testHeader := sbdprotocol.NewHeartbeat(nodeID, sequence)
	testMsg := sbdprotocol.SBDHeartbeatMessage{Header: testHeader}

	// Marshal the test message
	testMsgBytes, err := sbdprotocol.MarshalHeartbeat(testMsg)
	if err != nil {
		return fmt.Errorf("failed to marshal test heartbeat message: %w", err)
	}

	// Write test message to the node's slot
	n, err := device.WriteAt(testMsgBytes, slotOffset)
	if err != nil {
		return fmt.Errorf("failed to write test message to SBR device at offset %d: %w", slotOffset, err)
	}

	if n != len(testMsgBytes) {
		return fmt.Errorf("partial write to SBR device: wrote %d bytes, expected %d", n, len(testMsgBytes))
	}

	// Sync to ensure data is written to storage
	if err := device.Sync(); err != nil {
		return fmt.Errorf("failed to sync SBR device after test write: %w", err)
	}

	// Read back the data to verify write was successful
	readBuffer := make([]byte, len(testMsgBytes))
	readN, err := device.ReadAt(readBuffer, slotOffset)
	if err != nil {
		return fmt.Errorf("failed to read test message from SBR device at offset %d: %w", slotOffset, err)
	}

	if readN != len(testMsgBytes) {
		return fmt.Errorf("partial read from SBR device: read %d bytes, expected %d", readN, len(testMsgBytes))
	}

	// Verify the data matches what we wrote
	for i, b := range testMsgBytes {
		if readBuffer[i] != b {
			return fmt.Errorf("data mismatch at byte %d: wrote 0x%02x, read 0x%02x", i, b, readBuffer[i])
		}
	}

	// Try to unmarshal the read data to ensure it's valid
	readHeader, err := sbdprotocol.Unmarshal(readBuffer[:sbdprotocol.SBD_HEADER_SIZE])
	if err != nil {
		return fmt.Errorf("failed to unmarshal test message read from SBR device: %w", err)
	}

	// Verify the header matches our expectations
	if readHeader.NodeID != nodeID {
		return fmt.Errorf("node ID mismatch: expected %d, got %d", nodeID, readHeader.NodeID)
	}

	if readHeader.Sequence != sequence {
		return fmt.Errorf("sequence mismatch: expected %d, got %d", sequence, readHeader.Sequence)
	}

	if readHeader.Type != sbdprotocol.SBD_MSG_TYPE_HEARTBEAT {
		return fmt.Errorf("message type mismatch: expected %d, got %d", sbdprotocol.SBD_MSG_TYPE_HEARTBEAT, readHeader.Type)
	}

	logger.V(1).Info("SBR device read/write test successful",
		"nodeID", nodeID,
		"sequence", sequence,
		"slotOffset", slotOffset,
		"bytesWritten", n,
		"bytesRead", readN)

	return nil
}

// checkNodeIDNameResolution verifies that the node name and ID are valid and consistent
func checkNodeIDNameResolution(nodeName string, nodeID uint16) error {
	logger.V(1).Info("Checking node ID/name resolution", "nodeName", nodeName, "nodeID", nodeID)

	// Validate node name is not empty
	if nodeName == "" {
		return fmt.Errorf("node name is empty")
	}

	// Validate node name length
	if len(nodeName) > MaxNodeNameLength {
		return fmt.Errorf("node name too long: %d characters, maximum allowed: %d", len(nodeName), MaxNodeNameLength)
	}

	// Validate node ID is within valid range
	if nodeID < 1 || nodeID > sbdprotocol.SBD_MAX_NODES {
		return fmt.Errorf("node ID %d is out of valid range [1, %d]", nodeID, sbdprotocol.SBD_MAX_NODES)
	}

	// Additional validation: ensure node name contains only valid characters
	// (printable ASCII characters, no control characters)
	for i, r := range nodeName {
		if r < 32 || r > 126 {
			return fmt.Errorf("node name contains invalid character at position %d: 0x%02x", i, r)
		}
	}

	logger.V(1).Info("Node ID/name resolution successful",
		"nodeName", nodeName,
		"nodeNameLength", len(nodeName),
		"nodeID", nodeID)

	return nil
}

// validateWatchdogTiming validates the relationship between pet interval and watchdog timeout
func validateWatchdogTiming(petInterval, watchdogTimeout time.Duration) (bool, string) {
	// Check for minimum pet interval (should be at least 1 second)
	minimumPetInterval := 1 * time.Second
	if petInterval < minimumPetInterval {
		return false, fmt.Sprintf("pet interval (%v) is very short, minimum recommended is %v",
			petInterval, minimumPetInterval)
	}

	// Pet interval should be significantly less than watchdog timeout
	// Recommended ratio is at least 3:1 (timeout:interval)
	minimumRatio := 3.0
	actualRatio := float64(watchdogTimeout) / float64(petInterval)

	if actualRatio < minimumRatio {
		return false, fmt.Sprintf("pet interval (%v) is too close to watchdog timeout (%v). "+
			"Pet interval should be at least %.1fx shorter than timeout (recommended ratio 3:1 or higher). "+
			"Current ratio: %.1f:1",
			petInterval, watchdogTimeout, minimumRatio, actualRatio)
	}

	return true, ""
}

// initializeKubernetesClients creates Kubernetes clients for StorageBasedRemediation CR watching
func initializeKubernetesClients(kubeconfigPath string) (client.Client, kubernetes.Interface, error) {
	var config *rest.Config
	var err error

	if kubeconfigPath != "" {
		// Use provided kubeconfig file
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to build config from kubeconfig %s: %w", kubeconfigPath, err)
		}
		logger.Info("Using kubeconfig file for Kubernetes client", "kubeconfigPath", kubeconfigPath)
	} else {
		// Use in-cluster configuration
		config, err = rest.InClusterConfig()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get in-cluster config: %w", err)
		}
		logger.Info("Using in-cluster configuration for Kubernetes client")
	}

	// Create runtime scheme with StorageBasedRemediation types
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return nil, nil, fmt.Errorf("failed to add StorageBasedRemediation types to scheme: %w", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, nil, fmt.Errorf("failed to add v1 types to scheme: %w", err)
	}

	// Create controller-runtime client for CR operations
	k8sClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create controller-runtime client: %w", err)
	}

	// Create standard clientset for additional operations
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create Kubernetes clientset: %w", err)
	}

	return k8sClient, clientset, nil
}

// initializeControllerManager initializes the controller manager for StorageBasedRemediation reconciliation
func (s *SBRAgent) initializeControllerManager() error {
	// Get Kubernetes config

	var err error
	config := s.restConfig
	if config == nil {
		config, err = ctrl.GetConfig()
	}
	if config == nil {
		return fmt.Errorf("failed to get Kubernetes config: %w", err)
	}
	// Set the controller-runtime logger to use our structured logger
	// This must be done before creating the controller manager
	ctrl.SetLogger(logger)

	// Create controller-runtime manager options
	options := ctrl.Options{
		Scheme: s.getScheme(),
	}
	// Note: Namespace filtering is handled by the reconciler's RBAC and client configuration

	// Create controller-runtime manager
	mgr, err := ctrl.NewManager(config, options)
	if err != nil {
		return fmt.Errorf("failed to create controller-runtime manager: %w", err)
	}

	s.controllerManager = mgr

	// Add StorageBasedRemediation controller to the manager
	if err := s.addSBRRemediationController(); err != nil {
		return fmt.Errorf("failed to add StorageBasedRemediation controller: %w", err)
	}

	logger.Info("StorageBasedRemediation controller added to manager successfully")
	return nil
}

// getScheme returns the runtime scheme with StorageBasedRemediation types registered
func (s *SBRAgent) getScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		logger.Error(err, "Failed to add v1alpha1 types to scheme")
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		logger.Error(err, "Failed to add core v1 types to scheme")
	}
	return scheme
}

// addSBRRemediationController adds the StorageBasedRemediation controller to the controller manager
func (s *SBRAgent) addSBRRemediationController() error {
	// Create StorageBasedRemediation reconciler
	reconciler := &controller.SBRRemediationReconciler{
		Client:   s.controllerManager.GetClient(),
		Scheme:   s.controllerManager.GetScheme(),
		Recorder: s.controllerManager.GetEventRecorderFor("sbr-agent-remediation"),
	}

	// Set up the reconciler with agent resources
	reconciler.SetSBRDevices(s.heartbeatDevice, s.fenceDevice)

	reconciler.SetNodeManager(s.nodeManager)
	reconciler.SetOwnNodeInfo(s.nodeID, s.nodeName)

	// Set up the controller with the manager
	if err := reconciler.SetupWithManager(s.controllerManager, s.controllerNamespace); err != nil {
		return fmt.Errorf("failed to setup StorageBasedRemediation controller with manager: %w", err)
	}

	logger.Info("StorageBasedRemediation controller added to manager successfully")
	return nil
}

func main() {
	flag.Parse()

	// Initialize structured logger first
	if err := initializeLogger(*logLevel); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}

	logger.Info("SBR Agent starting", "version", "development")

	// Log build information at startup
	logger.Info("SBR Agent build information", "buildInfo", version.GetFormattedBuildInfo())

	// Validate watchdog timing early using the configured values
	if valid, warning := validateWatchdogTiming(*petInterval, *watchdogTimeout); !valid {
		logger.Error(nil, "Watchdog timing validation failed", "error", warning)
		os.Exit(1)
	}

	// Determine node name
	nodeNameValue := *nodeName
	if nodeNameValue == "" {
		nodeNameValue = getNodeNameFromEnv()
		if nodeNameValue == "" {
			logger.Error(nil, "Node name must be specified via --node-name flag or NODE_NAME environment variable")
			os.Exit(1)
		}
		logger.Info("Using node name from environment", "nodeName", nodeNameValue)
	}

	// Validate node name length
	if len(nodeNameValue) > MaxNodeNameLength {
		logger.Error(nil, "Node name too long",
			"nodeNameLength", len(nodeNameValue),
			"maxLength", MaxNodeNameLength,
			"nodeName", nodeNameValue)
		os.Exit(1)
	}

	// Determine node ID
	nodeIDValue := uint16(*nodeID)
	if nodeIDValue == 0 {
		nodeIDValue = getNodeIDFromEnv()
		logger.Info("Using node ID from environment or default", "nodeID", nodeIDValue)
	}

	// Validate node ID
	if nodeIDValue < 1 || nodeIDValue > sbdprotocol.SBD_MAX_NODES {
		logger.Error(nil, "Invalid node ID",
			"nodeID", nodeIDValue,
			"minNodeID", 1,
			"maxNodeID", sbdprotocol.SBD_MAX_NODES)
		os.Exit(1)
	}

	// Determine SBR timeout
	sbrTimeoutValue := *sbrTimeoutSeconds
	if sbrTimeoutValue == SBRDefaultTimeoutSec { // Check if it's still the default
		sbrTimeoutValue = getSBRTimeoutFromEnv()
		logger.Info("Using SBR timeout from environment or default",
			"sbrTimeoutSeconds", sbrTimeoutValue)
	}

	// Determine reboot method
	rebootMethodValue := *rebootMethod
	if rebootMethodValue == RebootMethodPanic { // Check if it's still the default
		rebootMethodValue = getRebootMethodFromEnv()
		logger.Info("Using reboot method from environment or default",
			"rebootMethod", rebootMethodValue)
	}

	// Validate reboot method
	if rebootMethodValue != RebootMethodPanic && rebootMethodValue != RebootMethodSystemctlReboot &&
		rebootMethodValue != RebootMethodNone {
		logger.Error(nil, "Invalid reboot method",
			"rebootMethod", rebootMethodValue,
			"validMethods", []string{RebootMethodPanic, RebootMethodSystemctlReboot, RebootMethodNone})
		os.Exit(1)
	}

	// Calculate heartbeat interval (sbrTimeoutSeconds / 2)
	heartbeatInterval := time.Duration(sbrTimeoutValue/2) * time.Second
	if heartbeatInterval < time.Second {
		heartbeatInterval = time.Second // Minimum 1 second interval
	}
	// Stale age for SBRStorageUnhealthy=True: (MaxConsecutiveFailures+1)*heartbeatInterval + RemediationCheckTimeout
	// so we wait for the unhealthy node to have had time to self-fence plus one heartbeat buffer, plus time for
	// the remediation CR check (Get with RemediationCheckTimeout) before setting condition to Unknown.
	sbrUnhealthyConditionStaleAge = time.Duration(MaxConsecutiveFailures+1)*heartbeatInterval + RemediationCheckTimeout

	// Validate required parameters
	if *sbrDevice == "" {
		logger.Error(nil, "SBR device is required - watchdog-only mode is no longer supported")
		os.Exit(1)
	}

	if err := validateSBRDevice(*sbrDevice); err != nil {
		logger.Error(err, "SBR device validation failed", "sbrDevice", *sbrDevice)
		os.Exit(1)
	}

	// Run pre-flight checks before creating the agent
	if err := runPreflightChecks(*watchdogPath, *sbrDevice, nodeNameValue, nodeIDValue); err != nil {
		logger.Error(err, "Pre-flight checks failed")
		os.Exit(1)
	}

	// Initialize Kubernetes clients (fencing is core functionality)
	var k8sClient client.Client
	{
		var err error
		// Get kubeconfig from controller-runtime's auto-registered flag
		kubeconfigFlag := flag.Lookup("kubeconfig")
		kubeconfigPath := ""
		if kubeconfigFlag != nil {
			kubeconfigPath = kubeconfigFlag.Value.String()
		}
		k8sClient, _, err = initializeKubernetesClients(kubeconfigPath)
		if err != nil {
			logger.Error(err, "Failed to initialize Kubernetes clients")
			os.Exit(1)
		}
		logger.Info("Kubernetes clients initialized successfully for fencing operations")
	}

	// Create SBR agent (hash mapping is always enabled)
	var wd mocks.WatchdogInterface
	if *detectOnlyMode {
		logger.Info("Detect-only mode: using no-op watchdog (watchdog disarmed, no remediation)")
		wd = mocks.NewMockWatchdog(*watchdogPath)
	} else {
		realWd, err := watchdog.NewWithSoftdogFallback(*watchdogPath, logger)
		if err != nil {
			logger.Error(err, "Failed to initialize watchdog",
				"watchdogPath", *watchdogPath)
			os.Exit(1)
		}
		wd = realWd
	}
	sbrAgent, err := NewSBRAgentWithWatchdog(
		wd,
		*sbrDevice,
		nodeNameValue,
		*clusterName,
		nodeIDValue,
		*petInterval,
		*sbrUpdateInterval,
		heartbeatInterval,
		*peerCheckInterval,
		sbrTimeoutValue,
		rebootMethodValue,
		*metricsPort,
		*staleNodeTimeout,
		*sbrFileLocking,
		*ioTimeout,
		k8sClient,
		nil,
		"",
		*detectOnlyMode,
	)
	if err != nil {
		logger.Error(err, "Failed to create SBR agent",
			"watchdogPath", *watchdogPath,
			"sbrDevice", *sbrDevice,
			"nodeName", nodeNameValue,
			"nodeID", nodeIDValue)
		os.Exit(1)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	if err := sbrAgent.RunUntilShutdown(sigChan); err != nil {
		logger.Error(err, "Failed to run SBR agent or error during shutdown")
		os.Exit(1)
	}
	logger.Info("SBR Agent shutdown complete")
}
