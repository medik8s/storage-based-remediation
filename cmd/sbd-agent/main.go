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
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	// Kubernetes imports for SBDRemediation CR watching

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/sbd-operator/api/v1alpha1"
	"github.com/medik8s/sbd-operator/pkg/agent"
	"github.com/medik8s/sbd-operator/pkg/blockdevice"
	"github.com/medik8s/sbd-operator/pkg/retry"
	"github.com/medik8s/sbd-operator/pkg/sbdprotocol"
	"github.com/medik8s/sbd-operator/pkg/version"
	"github.com/medik8s/sbd-operator/pkg/watchdog"
)

// RBAC permissions for SBD Agent
// The agent needs to list and process SBDRemediation CRs for fencing operations
// +kubebuilder:rbac:groups=medik8s.medik8s.io,resources=sbdremediations,verbs=get;list;watch
// +kubebuilder:rbac:groups=medik8s.medik8s.io,resources=sbdremediations/status,verbs=update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

var (
	watchdogPath      = flag.String(agent.FlagWatchdogPath, agent.DefaultWatchdogPath, "Path to the watchdog device")
	watchdogTimeout   = flag.Duration(agent.FlagWatchdogTimeout, 60*time.Second, "Watchdog timeout duration (how long before watchdog triggers reboot)")
	petInterval       = flag.Duration(agent.FlagPetInterval, 15*time.Second, "Pet interval (how often to pet the watchdog)")
	sbdDevice         = flag.String(agent.FlagSBDDevice, agent.DefaultSBDDevice, "Path to the SBD block device")
	sbdFileLocking    = flag.Bool(agent.FlagSBDFileLocking, agent.DefaultSBDFileLocking, "Enable file locking for SBD device operations (recommended for shared storage)")
	nodeName          = flag.String(agent.FlagNodeName, agent.DefaultNodeName, "Name of this Kubernetes node")
	clusterName       = flag.String(agent.FlagClusterName, agent.DefaultClusterName, "Name of the cluster for node mapping")
	nodeID            = flag.Uint(agent.FlagNodeID, agent.DefaultNodeID, "Unique numeric ID for this node (1-255) - deprecated, use hash-based mapping")
	sbdTimeoutSeconds = flag.Uint(agent.FlagSBDTimeoutSeconds, agent.DefaultSBDTimeoutSeconds, "SBD timeout in seconds (determines heartbeat interval)")
	sbdUpdateInterval = flag.Duration(agent.FlagSBDUpdateInterval, 5*time.Second, "Interval for updating SBD device with node status")
	peerCheckInterval = flag.Duration(agent.FlagPeerCheckInterval, 5*time.Second, "Interval for checking peer heartbeats")
	logLevel          = flag.String(agent.FlagLogLevel, agent.DefaultLogLevel, "Log level (debug, info, warn, error)")
	rebootMethod      = flag.String(agent.FlagRebootMethod, agent.DefaultRebootMethod, "Method to use for self-fencing (panic, systemctl-reboot)")
	metricsPort       = flag.Int(agent.FlagMetricsPort, agent.DefaultMetricsPort, "Port for Prometheus metrics endpoint")
	staleNodeTimeout  = flag.Duration(agent.FlagStaleNodeTimeout, 1*time.Hour, "Timeout for considering nodes stale and removing them from slot mapping")

	// Kubernetes client configuration flags (kubeconfig is auto-registered by controller-runtime)
	namespace     = flag.String("namespace", "", "Namespace to watch for SBDRemediation CRs (optional, watches all namespaces if not specified)")
	enableFencing = flag.Bool("enable-fencing", true, "Enable agent-based fencing capabilities (watch and process SBDRemediation CRs)")

	// I/O timeout configuration
	ioTimeout = flag.Duration("io-timeout", 2*time.Second, "Timeout for I/O operations (prevents indefinite hanging when storage becomes unresponsive)")
)

const (
	// SBDNodeIDOffset is the offset where node ID is written in the SBD device
	SBDNodeIDOffset = 0
	// MaxNodeNameLength is the maximum length for a node name in SBD device
	MaxNodeNameLength = 256
	// DefaultNodeID is the placeholder node ID used when none is specified
	DefaultNodeID = 1

	// Retry configuration constants for SBD Agent operations
	// MaxCriticalRetries is the maximum number of retry attempts for critical operations
	MaxCriticalRetries = 3
	// InitialCriticalRetryDelay is the initial delay between critical operation retries
	InitialCriticalRetryDelay = 200 * time.Millisecond
	// MaxCriticalRetryDelay is the maximum delay between critical operation retries
	MaxCriticalRetryDelay = 2 * time.Second
	// CriticalRetryBackoffFactor is the exponential backoff factor for critical operation retries
	CriticalRetryBackoffFactor = 2.0

	// MaxConsecutiveFailures is the maximum number of consecutive failures before triggering self-fence
	MaxConsecutiveFailures = 5
	// FailureCountResetInterval is the interval after which failure counts are reset
	FailureCountResetInterval = 10 * time.Minute

	// File locking constants
	// FileLockTimeout is the maximum time to wait for acquiring a file lock
	FileLockTimeout = 5 * time.Second
	// FileLockRetryInterval is the interval between file lock acquisition attempts
	FileLockRetryInterval = 100 * time.Millisecond
)

// Global logger instance
var logger logr.Logger

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
	// sbd_agent_status_healthy: 1 if the agent is healthy, 0 otherwise
	// This metric indicates overall agent health including watchdog and SBD device access
	agentHealthyGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "sbd_agent_status_healthy",
		Help: "SBD Agent health status (1 = healthy, 0 = unhealthy)",
	})

	// sbd_device_io_errors_total: Total number of I/O errors with shared SBD device
	// This metric tracks all I/O operation failures when interacting with the SBD device
	sbdIOErrorsCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "sbd_device_io_errors_total",
		Help: "Total number of I/O errors encountered when interacting with the shared SBD device",
	})

	// sbd_watchdog_pets_total: Total number of successful watchdog pets
	// This metric counts how many times the kernel watchdog has been successfully petted
	watchdogPetsCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "sbd_watchdog_pets_total",
		Help: "Total number of times the local kernel watchdog has been successfully petted",
	})

	// sbd_peer_status: Current liveness status of each peer node
	// This metric uses labels to track the status of each peer node in the cluster
	peerStatusGaugeVec = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "sbd_peer_status",
		Help: "Current liveness status of each peer node (1 = alive, 0 = unhealthy/down)",
	}, []string{"node_id", "node_name", "status"})

	// sbd_self_fenced_total: Total number of self-fence initiations
	// This metric counts how many times this agent has initiated self-fencing
	selfFencedCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "sbd_self_fenced_total",
		Help: "Total number of times the agent has initiated a self-fence",
	})
)

// BlockDevice defines the interface for block device operations
type BlockDevice interface {
	io.ReaderAt
	io.WriterAt
	Sync() error
	Close() error
	Path() string
	IsClosed() bool
}

// WatchdogInterface defines the interface for watchdog operations
type WatchdogInterface interface {
	Pet() error
	Close() error
	Path() string
}

// PeerStatus represents the status of a peer node
type PeerStatus struct {
	NodeID        uint16    `json:"nodeId"`
	LastTimestamp uint64    `json:"lastTimestamp"`
	LastSequence  uint64    `json:"lastSequence"`
	LastSeen      time.Time `json:"lastSeen"`
	IsHealthy     bool      `json:"isHealthy"`
}

// PeerMonitor manages tracking of peer node states
type PeerMonitor struct {
	peers             map[uint16]*PeerStatus
	peersMutex        sync.RWMutex
	sbdTimeoutSeconds uint
	ownNodeID         uint16
	nodeManager       *sbdprotocol.NodeManager
	logger            logr.Logger
}

// NewPeerMonitor creates a new peer monitor instance
func NewPeerMonitor(sbdTimeoutSeconds uint, ownNodeID uint16, nodeManager *sbdprotocol.NodeManager, logger logr.Logger) *PeerMonitor {
	return &PeerMonitor{
		peers:             make(map[uint16]*PeerStatus),
		sbdTimeoutSeconds: sbdTimeoutSeconds,
		ownNodeID:         ownNodeID,
		nodeManager:       nodeManager,
		logger:            logger.WithName("peer-monitor"),
	}
}

// UpdatePeer updates the status of a peer node
func (pm *PeerMonitor) UpdatePeer(nodeID uint16, timestamp, sequence uint64) {
	pm.peersMutex.Lock()
	defer pm.peersMutex.Unlock()

	now := time.Now()

	// Get or create peer status
	peer, exists := pm.peers[nodeID]
	if !exists {
		peer = &PeerStatus{
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
			if name, found := pm.nodeManager.GetNodeForSlot(nodeID); found {
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

// CheckPeerLiveness checks which peers are still alive based on timeout
func (pm *PeerMonitor) CheckPeerLiveness() {
	pm.peersMutex.Lock()
	defer pm.peersMutex.Unlock()

	now := time.Now()
	timeout := time.Duration(pm.sbdTimeoutSeconds) * time.Second

	for nodeID, peer := range pm.peers {
		timeSinceLastSeen := now.Sub(peer.LastSeen)
		wasHealthy := peer.IsHealthy

		// Consider peer unhealthy if we haven't seen a heartbeat within timeout
		peer.IsHealthy = timeSinceLastSeen <= timeout

		// Update metrics if status changed
		if wasHealthy != peer.IsHealthy {
			pm.updatePeerMetrics(nodeID, peer.IsHealthy)
		}

		// Log status change
		if wasHealthy && !peer.IsHealthy {
			pm.logger.Error(nil, "Peer node became unhealthy",
				"nodeID", nodeID,
				"timeSinceLastSeen", timeSinceLastSeen,
				"timeout", timeout,
				"lastTimestamp", peer.LastTimestamp,
				"lastSequence", peer.LastSequence)
		} else if !wasHealthy && peer.IsHealthy {
			pm.logger.Info("Peer node recovered to healthy status",
				"nodeID", nodeID,
				"timeSinceLastSeen", timeSinceLastSeen,
				"lastTimestamp", peer.LastTimestamp,
				"lastSequence", peer.LastSequence)
		}
	}
}

// GetPeerStatus returns a copy of the current peer status map
func (pm *PeerMonitor) GetPeerStatus() map[uint16]*PeerStatus {
	pm.peersMutex.RLock()
	defer pm.peersMutex.RUnlock()

	// Return a deep copy to avoid race conditions
	result := make(map[uint16]*PeerStatus)
	for nodeID, peer := range pm.peers {
		result[nodeID] = &PeerStatus{
			NodeID:        peer.NodeID,
			LastTimestamp: peer.LastTimestamp,
			LastSequence:  peer.LastSequence,
			LastSeen:      peer.LastSeen,
			IsHealthy:     peer.IsHealthy,
		}
	}
	return result
}

// GetHealthyPeerCount returns the number of healthy peers
func (pm *PeerMonitor) GetHealthyPeerCount() int {
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
func (pm *PeerMonitor) updatePeerMetrics(nodeID uint16, isHealthy bool) {
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

// LeaderElectionConfig contains configuration for leader election
type LeaderElectionConfig struct {
	LeaseDuration time.Duration
	RenewDeadline time.Duration
	RetryPeriod   time.Duration
	Namespace     string
}

// RemediationLeaderElector manages leader election for a specific SBDRemediation
type RemediationLeaderElector struct {
	remediationKey string // namespace/name
	leaderElector  *leaderelection.LeaderElector
	isLeader       bool
	isLeaderMutex  sync.RWMutex
	stopCh         chan struct{}
	logger         logr.Logger
}

// NewRemediationLeaderElector creates a new leader elector for a specific remediation
func NewRemediationLeaderElector(remediationKey, nodeName string, k8sClientset kubernetes.Interface, config LeaderElectionConfig, logger logr.Logger) (*RemediationLeaderElector, error) {
	rle := &RemediationLeaderElector{
		remediationKey: remediationKey,
		isLeader:       false,
		stopCh:         make(chan struct{}),
		logger:         logger,
	}

	// Create a deterministic but unique lease name for this remediation
	h := sha256.New()
	h.Write([]byte(remediationKey))
	leaseName := fmt.Sprintf("sbd-remediation-%x", h.Sum(nil)[:8])

	// Create resource lock for leader election
	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      leaseName,
			Namespace: config.Namespace,
		},
		Client: k8sClientset.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: nodeName,
		},
	}

	// Configure leader election
	leaderElectionConfig := leaderelection.LeaderElectionConfig{
		Lock:          lock,
		LeaseDuration: config.LeaseDuration,
		RenewDeadline: config.RenewDeadline,
		RetryPeriod:   config.RetryPeriod,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				rle.setLeader(true)
				rle.logger.Info("Started leading remediation", "remediation", remediationKey, "leaseName", leaseName)
			},
			OnStoppedLeading: func() {
				rle.setLeader(false)
				rle.logger.Info("Stopped leading remediation", "remediation", remediationKey, "leaseName", leaseName)
			},
			OnNewLeader: func(identity string) {
				rle.logger.V(1).Info("New leader elected for remediation", "remediation", remediationKey, "leader", identity, "leaseName", leaseName)
			},
		},
	}

	elector, err := leaderelection.NewLeaderElector(leaderElectionConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create leader elector for remediation %s: %w", remediationKey, err)
	}

	rle.leaderElector = elector
	return rle, nil
}

// Start begins the leader election process
func (rle *RemediationLeaderElector) Start(ctx context.Context) {
	go func() {
		rle.logger.Info("Starting leader election", "remediation", rle.remediationKey)
		rle.leaderElector.Run(ctx)
	}()
}

// Stop terminates the leader election
func (rle *RemediationLeaderElector) Stop() {
	close(rle.stopCh)
}

// IsLeader returns whether this node is currently the leader for this remediation
func (rle *RemediationLeaderElector) IsLeader() bool {
	rle.isLeaderMutex.RLock()
	defer rle.isLeaderMutex.RUnlock()
	return rle.isLeader
}

// setLeader updates the leader status
func (rle *RemediationLeaderElector) setLeader(leader bool) {
	rle.isLeaderMutex.Lock()
	defer rle.isLeaderMutex.Unlock()
	rle.isLeader = leader
}

// SBDAgent represents the main SBD agent with self-fencing capabilities
type SBDAgent struct {
	watchdog          WatchdogInterface
	sbdDevice         BlockDevice
	sbdDevicePath     string
	nodeName          string
	nodeID            uint16
	petInterval       time.Duration
	sbdUpdateInterval time.Duration
	heartbeatInterval time.Duration
	peerCheckInterval time.Duration
	rebootMethod      string
	ioTimeout         time.Duration
	ctx               context.Context
	cancel            context.CancelFunc
	sbdHealthy        bool
	sbdHealthyMutex   sync.RWMutex
	heartbeatSequence uint64
	heartbeatSeqMutex sync.Mutex
	peerMonitor       *PeerMonitor
	selfFenceDetected bool
	selfFenceMutex    sync.RWMutex
	metricsPort       int
	metricsServer     *http.Server

	// Node mapping for hash-based slot assignment (always enabled)
	nodeManager      *sbdprotocol.NodeManager
	nodeManagerStop  chan struct{}
	staleNodeTimeout time.Duration

	// Failure tracking and retry configuration
	watchdogFailureCount  int
	sbdFailureCount       int
	heartbeatFailureCount int
	lastFailureReset      time.Time
	failureCountMutex     sync.RWMutex
	retryConfig           retry.Config

	// Kubernetes client for SBDRemediation CR watching and fencing coordination
	k8sClient      client.Client
	k8sClientset   kubernetes.Interface
	watchNamespace string
	enableFencing  bool
	fencingStopCh  chan struct{}

	// Leader election for coordinating SBDRemediation processing across agents
	leaderElectors       map[string]*RemediationLeaderElector // key: remediation namespace/name
	leaderElectorsMutex  sync.RWMutex
	leaderElectionConfig LeaderElectionConfig
}

// NewSBDAgent creates a new SBD agent with the specified configuration
func NewSBDAgent(watchdogPath, sbdDevicePath, nodeName, clusterName string, nodeID uint16, petInterval, sbdUpdateInterval, heartbeatInterval, peerCheckInterval time.Duration, sbdTimeoutSeconds uint, rebootMethod string, metricsPort int, staleNodeTimeout time.Duration, fileLockingEnabled bool, ioTimeout time.Duration, k8sClient client.Client, k8sClientset kubernetes.Interface, watchNamespace string, enableFencing bool) (*SBDAgent, error) {
	// Initialize watchdog first (always required) with softdog fallback for systems without hardware watchdog
	wd, err := watchdog.NewWithSoftdogFallback(watchdogPath, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize watchdog %s: %w", watchdogPath, err)
	}

	return NewSBDAgentWithWatchdog(wd, sbdDevicePath, nodeName, clusterName, nodeID, petInterval, sbdUpdateInterval, heartbeatInterval, peerCheckInterval, sbdTimeoutSeconds, rebootMethod, metricsPort, staleNodeTimeout, fileLockingEnabled, ioTimeout, k8sClient, k8sClientset, watchNamespace, enableFencing)
}

// NewSBDAgentWithWatchdog creates a new SBD agent with a provided watchdog interface
func NewSBDAgentWithWatchdog(wd WatchdogInterface, sbdDevicePath, nodeName, clusterName string, nodeID uint16, petInterval, sbdUpdateInterval, heartbeatInterval, peerCheckInterval time.Duration, sbdTimeoutSeconds uint, rebootMethod string, metricsPort int, staleNodeTimeout time.Duration, fileLockingEnabled bool, ioTimeout time.Duration, k8sClient client.Client, k8sClientset kubernetes.Interface, watchNamespace string, enableFencing bool) (*SBDAgent, error) {
	// Input validation
	if wd == nil {
		return nil, fmt.Errorf("watchdog interface cannot be nil")
	}
	if wd.Path() == "" {
		return nil, fmt.Errorf("watchdog path cannot be empty")
	}

	if sbdDevicePath == "" {
		return nil, fmt.Errorf("SBD device path cannot be empty")
	}
	if nodeName == "" {
		return nil, fmt.Errorf("node name cannot be empty")
	}
	if nodeID == 0 || nodeID > 255 {
		return nil, fmt.Errorf("node ID must be between 1 and 255, got %d", nodeID)
	}

	// Validate timing parameters
	if petInterval <= 0 {
		return nil, fmt.Errorf("pet interval must be positive, got %v", petInterval)
	}
	if rebootMethod != "panic" && rebootMethod != "systemctl-reboot" {
		return nil, fmt.Errorf("invalid reboot method '%s', must be 'panic' or 'systemctl-reboot'", rebootMethod)
	}
	if metricsPort <= 0 || metricsPort > 65535 {
		return nil, fmt.Errorf("metrics port must be between 1 and 65535, got %d", metricsPort)
	}
	if sbdUpdateInterval <= 0 {
		return nil, fmt.Errorf("SBD update interval must be positive")
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

	// Configure leader election settings
	leaderElectionConfig := LeaderElectionConfig{
		LeaseDuration: 15 * time.Second, // How long the leader holds the lease
		RenewDeadline: 10 * time.Second, // Deadline for renewing the lease
		RetryPeriod:   2 * time.Second,  // Frequency of retries
		Namespace:     watchNamespace,   // Use the watch namespace for leases
	}

	// If watch namespace is empty, use default namespace
	if leaderElectionConfig.Namespace == "" {
		leaderElectionConfig.Namespace = "default"
	}

	agent := &SBDAgent{
		watchdog:             wd,
		sbdDevicePath:        sbdDevicePath,
		nodeName:             nodeName,
		nodeID:               nodeID,
		petInterval:          petInterval,
		sbdUpdateInterval:    sbdUpdateInterval,
		heartbeatInterval:    heartbeatInterval,
		peerCheckInterval:    peerCheckInterval,
		rebootMethod:         rebootMethod,
		ioTimeout:            ioTimeout,
		ctx:                  ctx,
		cancel:               cancel,
		sbdHealthy:           false,
		heartbeatSequence:    0,
		selfFenceDetected:    false,
		metricsPort:          metricsPort,
		nodeManagerStop:      make(chan struct{}),
		staleNodeTimeout:     staleNodeTimeout,
		lastFailureReset:     time.Now(),
		retryConfig:          retryConfig,
		k8sClient:            k8sClient,
		k8sClientset:         k8sClientset,
		watchNamespace:       watchNamespace,
		enableFencing:        enableFencing,
		fencingStopCh:        make(chan struct{}),
		leaderElectors:       make(map[string]*RemediationLeaderElector),
		leaderElectionConfig: leaderElectionConfig,
	}

	// Initialize SBD device
	if err := agent.initializeSBDDevice(); err != nil {
		agent.cancel()
		return nil, fmt.Errorf("failed to initialize SBD device: %w", err)
	}

	// Initialize node manager for consistent slot assignment
	if err := agent.initializeNodeManager(clusterName, fileLockingEnabled); err != nil {
		agent.cancel()
		if agent.sbdDevice != nil {
			agent.sbdDevice.Close()
		}
		return nil, fmt.Errorf("failed to initialize node manager: %w", err)
	}

	// Initialize the PeerMonitor
	agent.peerMonitor = NewPeerMonitor(sbdTimeoutSeconds, nodeID, agent.nodeManager, logger)

	// Initialize metrics
	if err := agent.initMetrics(); err != nil {
		agent.cancel()
		if agent.sbdDevice != nil {
			agent.sbdDevice.Close()
		}
		return nil, fmt.Errorf("failed to initialize metrics: %w", err)
	}

	return agent, nil
}

// initMetrics initializes Prometheus metrics and starts the metrics server
func (s *SBDAgent) initMetrics() error {
	// Register all metrics with the default registry only once
	metricsOnce.Do(func() {
		prometheus.MustRegister(agentHealthyGauge)
		prometheus.MustRegister(sbdIOErrorsCounter)
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

	return nil
}

// initializeSBDDevice opens and initializes the SBD block device
func (s *SBDAgent) initializeSBDDevice() error {
	device, err := blockdevice.OpenWithTimeout(s.sbdDevicePath, s.ioTimeout, logger.WithName("sbd-device"))
	if err != nil {
		return fmt.Errorf("failed to open SBD device %s with timeout %v: %w", s.sbdDevicePath, s.ioTimeout, err)
	}

	s.sbdDevice = device
	logger.Info("Successfully opened SBD device", "devicePath", s.sbdDevicePath, "ioTimeout", s.ioTimeout)
	return nil
}

// setSBDDevice allows setting a custom SBD device (useful for testing)
func (s *SBDAgent) setSBDDevice(device BlockDevice) {
	s.sbdDevice = device
}

// initializeNodeManager initializes the node manager for hash-based slot mapping
func (s *SBDAgent) initializeNodeManager(clusterName string, fileLockingEnabled bool) error {
	if s.sbdDevice == nil {
		return fmt.Errorf("SBD device must be initialized before node manager")
	}

	config := sbdprotocol.NodeManagerConfig{
		ClusterName:        clusterName,
		SyncInterval:       30 * time.Second,
		StaleNodeTimeout:   s.staleNodeTimeout,
		Logger:             logger.WithName("node-manager"),
		FileLockingEnabled: fileLockingEnabled,
	}

	nodeManager, err := sbdprotocol.NewNodeManager(s.sbdDevice, config)
	if err != nil {
		return fmt.Errorf("failed to create node manager: %w", err)
	}

	s.nodeManager = nodeManager

	// Get or assign slot for this node
	slotID, err := s.nodeManager.GetSlotForNode(s.nodeName)
	if err != nil {
		return fmt.Errorf("failed to get slot for node %s: %w", s.nodeName, err)
	}

	// Update the node ID to use the hash-based slot
	s.nodeID = slotID
	logger.Info("Node assigned to slot via hash-based mapping",
		"nodeName", s.nodeName,
		"slotID", slotID,
		"clusterName", clusterName,
		"fileLockingEnabled", fileLockingEnabled,
		"coordinationStrategy", s.nodeManager.GetCoordinationStrategy())

	return nil
}

// setSBDHealthy safely updates the SBD health status
func (s *SBDAgent) setSBDHealthy(healthy bool) {
	s.sbdHealthyMutex.Lock()
	defer s.sbdHealthyMutex.Unlock()
	s.sbdHealthy = healthy
}

// isSBDHealthy safely reads the SBD health status
func (s *SBDAgent) isSBDHealthy() bool {
	s.sbdHealthyMutex.RLock()
	defer s.sbdHealthyMutex.RUnlock()
	return s.sbdHealthy
}

// getNextHeartbeatSequence safely increments and returns the next sequence number
func (s *SBDAgent) getNextHeartbeatSequence() uint64 {
	s.heartbeatSeqMutex.Lock()
	defer s.heartbeatSeqMutex.Unlock()
	s.heartbeatSequence++
	return s.heartbeatSequence
}

// incrementFailureCount safely increments the failure count for a specific operation type
func (s *SBDAgent) incrementFailureCount(operationType string) int {
	s.failureCountMutex.Lock()
	defer s.failureCountMutex.Unlock()

	// Reset failure counts if enough time has passed
	if time.Since(s.lastFailureReset) > FailureCountResetInterval {
		s.watchdogFailureCount = 0
		s.sbdFailureCount = 0
		s.heartbeatFailureCount = 0
		s.lastFailureReset = time.Now()
		logger.V(1).Info("Reset failure counts due to time interval",
			"watchdogFailureCount", s.watchdogFailureCount,
			"sbdFailureCount", s.sbdFailureCount,
			"heartbeatFailureCount", s.heartbeatFailureCount,
			"lastFailureReset", s.lastFailureReset)
	}

	counter := 0
	switch operationType {
	case "watchdog":
		s.watchdogFailureCount++
		counter = s.watchdogFailureCount
	case "sbd":
		s.sbdFailureCount++
		counter = s.sbdFailureCount
		// Increment SBD I/O errors counter
		sbdIOErrorsCounter.Inc()
	case "heartbeat":
		s.heartbeatFailureCount++
		// Increment SBD I/O errors counter
		sbdIOErrorsCounter.Inc()
		counter = s.heartbeatFailureCount
	}

	s.setSBDHealthy(false)
	// Mark agent as unhealthy
	agentHealthyGauge.Set(0)

	logger.V(1).Info("Incremented failure count", "operationType", operationType, "failureCount", counter)
	if counter >= MaxConsecutiveFailures {
		logger.Error(nil, "Failures exceeded threshold, will trigger self-fence on next watchdog iteration",
			"failureCount", counter,
			"threshold", MaxConsecutiveFailures)

		// Try to reinitialize the device on next iteration
		if s.sbdDevice != nil && !s.sbdDevice.IsClosed() {
			if closeErr := s.sbdDevice.Close(); closeErr != nil {
				logger.Error(closeErr, "Failed to close SBD device", "devicePath", s.sbdDevicePath)
			}
		}
	}
	return counter
}

// resetFailureCount safely resets the failure count for a specific operation type
func (s *SBDAgent) resetFailureCount(operationType string) {
	s.failureCountMutex.Lock()
	defer s.failureCountMutex.Unlock()

	switch operationType {
	case "watchdog":
		if s.watchdogFailureCount > 0 {
			logger.V(1).Info("Reset watchdog failure count", "previousCount", s.watchdogFailureCount)
			s.watchdogFailureCount = 0
		}
	case "sbd":
		if s.sbdFailureCount > 0 {
			logger.V(1).Info("Reset SBD failure count", "previousCount", s.sbdFailureCount)
			s.sbdFailureCount = 0
		}
	case "heartbeat":
		if s.heartbeatFailureCount > 0 {
			logger.V(1).Info("Reset heartbeat failure count", "previousCount", s.heartbeatFailureCount)
			s.heartbeatFailureCount = 0
		}
	}
}

// shouldTriggerSelfFence checks if consecutive failures exceed the threshold
func (s *SBDAgent) shouldTriggerSelfFence() (bool, string) {
	s.failureCountMutex.RLock()
	defer s.failureCountMutex.RUnlock()

	if s.watchdogFailureCount >= MaxConsecutiveFailures {
		return true, fmt.Sprintf("watchdog pet failures exceeded threshold (%d)", MaxConsecutiveFailures)
	}
	if s.sbdFailureCount >= MaxConsecutiveFailures {
		return true, fmt.Sprintf("SBD device failures exceeded threshold (%d)", MaxConsecutiveFailures)
	}
	if s.heartbeatFailureCount >= MaxConsecutiveFailures {
		return true, fmt.Sprintf("heartbeat write failures exceeded threshold (%d)", MaxConsecutiveFailures)
	}

	return false, ""
}

// writeNodeIDToSBD writes the node name to the SBD device at the predefined offset
func (s *SBDAgent) writeNodeIDToSBD() error {
	if s.nodeManager != nil {
		// Use NodeManager's file locking for coordination
		return s.nodeManager.WriteWithLock("write node ID", func() error {
			return s.writeNodeIDToSBDInternal()
		})
	}
	// Fallback for cases without NodeManager (shouldn't happen in normal operation)
	return s.writeNodeIDToSBDInternal()
}

// writeNodeIDToSBDInternal performs the actual write operation without locking
func (s *SBDAgent) writeNodeIDToSBDInternal() error {
	if s.sbdDevice == nil || s.sbdDevice.IsClosed() {
		// Try to reinitialize the device
		if err := s.initializeSBDDevice(); err != nil {
			return fmt.Errorf("SBD device is closed and reinitialize failed: %w", err)
		}
	}

	// Prepare node name data with fixed size
	nodeData := make([]byte, MaxNodeNameLength)
	copy(nodeData, []byte(s.nodeName))

	// Write node name to SBD device
	n, err := s.sbdDevice.WriteAt(nodeData, SBDNodeIDOffset)
	if err != nil {
		return fmt.Errorf("failed to write node ID to SBD device: %w", err)
	}

	if n != len(nodeData) {
		return fmt.Errorf("partial write to SBD device: wrote %d bytes, expected %d", n, len(nodeData))
	}

	// Ensure data is committed to storage
	if err := s.sbdDevice.Sync(); err != nil {
		return fmt.Errorf("failed to sync SBD device: %w", err)
	}
	logger.V(1).Info("Successfully wrote node ID to SBD device",
		"devicePath", s.sbdDevicePath,
		"nodeName", s.nodeName,
		"offset", SBDNodeIDOffset,
		"bytesWritten", len(nodeData))

	return nil
}

// writeHeartbeatToSBD writes a heartbeat message to the node's designated slot
func (s *SBDAgent) writeHeartbeatToSBD() error {
	if s.nodeManager != nil {
		// Use NodeManager's file locking for coordination
		return s.nodeManager.WriteWithLock("write heartbeat", func() error {
			return s.writeHeartbeatToSBDInternal()
		})
	}
	// Fallback for cases without NodeManager (shouldn't happen in normal operation)
	return s.writeHeartbeatToSBDInternal()
}

// writeHeartbeatToSBDInternal performs the actual heartbeat write operation without locking
func (s *SBDAgent) writeHeartbeatToSBDInternal() error {
	if s.sbdDevice == nil || s.sbdDevice.IsClosed() {
		// Try to reinitialize the device
		if err := s.initializeSBDDevice(); err != nil {
			return fmt.Errorf("SBD device is closed and reinitialize failed: %w", err)
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
	n, err := s.sbdDevice.WriteAt(msgBytes, slotOffset)
	if err != nil {
		return fmt.Errorf("failed to write heartbeat to SBD device at offset %d: %w", slotOffset, err)
	}

	if n != len(msgBytes) {
		return fmt.Errorf("partial write to SBD device: wrote %d bytes, expected %d", n, len(msgBytes))
	}

	// Ensure data is committed to storage
	if err := s.sbdDevice.Sync(); err != nil {
		return fmt.Errorf("failed to sync SBD device after heartbeat write: %w", err)
	}

	logger.V(1).Info("Successfully wrote heartbeat message",
		"sequence", sequence,
		"nodeID", s.nodeID,
		"slotOffset", slotOffset)
	return nil
}

// readPeerHeartbeat reads and processes a heartbeat from a peer node's slot
func (s *SBDAgent) readPeerHeartbeat(peerNodeID uint16) error {
	if s.sbdDevice == nil || s.sbdDevice.IsClosed() {
		return fmt.Errorf("SBD device is not available")
	}

	// Calculate slot offset for the peer node
	slotOffset := int64(peerNodeID) * sbdprotocol.SBD_SLOT_SIZE

	// Read the entire slot
	slotData := make([]byte, sbdprotocol.SBD_SLOT_SIZE)
	n, err := s.sbdDevice.ReadAt(slotData, slotOffset)
	if err != nil {
		// Increment SBD I/O errors counter for read failures
		sbdIOErrorsCounter.Inc()
		s.incrementFailureCount("heartbeat")
		return fmt.Errorf("failed to read peer %d heartbeat from offset %d: %w", peerNodeID, slotOffset, err)
	}

	if n != sbdprotocol.SBD_SLOT_SIZE {
		return fmt.Errorf("partial read from peer %d slot: read %d bytes, expected %d", peerNodeID, n, sbdprotocol.SBD_SLOT_SIZE)
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
	s.peerMonitor.UpdatePeer(peerNodeID, header.Timestamp, header.Sequence)
	return nil
}

// Start begins the SBD agent operations
func (s *SBDAgent) Start() error {
	logger.Info("Starting SBD Agent",
		"watchdogDevice", s.watchdog.Path(),
		"sbdDevice", s.sbdDevicePath,
		"nodeName", s.nodeName,
		"nodeID", s.nodeID,
		"petInterval", s.petInterval,
		"sbdUpdateInterval", s.sbdUpdateInterval,
		"heartbeatInterval", s.heartbeatInterval,
		"peerCheckInterval", s.peerCheckInterval)

	// Start node manager periodic sync if using hash mapping
	if s.nodeManager != nil {
		s.nodeManagerStop = s.nodeManager.StartPeriodicSync()
	}

	// Start the watchdog monitoring goroutine
	go s.watchdogLoop()

	// Start SBD device monitoring if available
	if s.sbdDevicePath != "" {
		go s.sbdDeviceLoop()
		go s.heartbeatLoop()
		go s.peerMonitorLoop()
	}

	// Start fencing loop if enabled
	if s.enableFencing {
		s.fencingStopCh = make(chan struct{})
		go s.fencingLoop()
	}

	logger.Info("SBD Agent started successfully")
	return nil
}

// Stop gracefully shuts down the SBD agent
func (s *SBDAgent) Stop() error {
	logger.Info("Stopping SBD Agent")

	// Signal all goroutines to stop
	s.cancel()

	// Stop leader election for all remediations
	if s.enableFencing {
		logger.Info("Stopping all leader electors")
		s.stopAllLeaderElectors()

		// Close fencing stop channel
		close(s.fencingStopCh)
	}

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

	// Close SBD device last (after all operations complete)
	if s.sbdDevice != nil && !s.sbdDevice.IsClosed() {
		logger.Info("Closing SBD device", "devicePath", s.sbdDevicePath)
		if err := s.sbdDevice.Close(); err != nil {
			logger.Error(err, "Failed to close SBD device", "devicePath", s.sbdDevicePath)
			return fmt.Errorf("failed to close SBD device: %w", err)
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

	logger.Info("SBD Agent stopped successfully")
	return nil
}

// cleanupCompletedLeaderElectors removes leader electors for completed remediations
func (s *SBDAgent) cleanupCompletedLeaderElectors() {
	s.leaderElectorsMutex.Lock()
	defer s.leaderElectorsMutex.Unlock()

	for remediationKey, elector := range s.leaderElectors {
		// Check if the remediation still exists
		parts := strings.Split(remediationKey, "/")
		if len(parts) != 2 {
			continue
		}
		namespace, name := parts[0], parts[1]

		var remediation v1alpha1.SBDRemediation
		err := s.k8sClient.Get(context.Background(), types.NamespacedName{
			Namespace: namespace,
			Name:      name,
		}, &remediation)

		// If remediation doesn't exist or is completed, clean up the elector
		if apierrors.IsNotFound(err) || (err == nil && remediation.IsFencingSucceeded()) {
			logger.Info("Cleaning up leader elector for completed/deleted remediation",
				"remediation", remediationKey)
			elector.Stop()
			delete(s.leaderElectors, remediationKey)
		}
	}
}

// stopAllLeaderElectors stops all active leader electors
func (s *SBDAgent) stopAllLeaderElectors() {
	s.leaderElectorsMutex.Lock()
	defer s.leaderElectorsMutex.Unlock()

	for remediationKey, elector := range s.leaderElectors {
		logger.Info("Stopping leader elector", "remediation", remediationKey)
		elector.Stop()
	}
	s.leaderElectors = make(map[string]*RemediationLeaderElector)
}

// watchdogLoop continuously pets the watchdog to prevent system reset
func (s *SBDAgent) watchdogLoop() {
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

			// Only pet the watchdog if SBD device is healthy
			if s.isSBDHealthy() {
				// Use retry mechanism for watchdog petting
				err := retry.Do(s.ctx, s.retryConfig, "pet watchdog", func() error {
					return s.watchdog.Pet()
				})

				if err != nil {
					s.incrementFailureCount("watchdog")
					// Continue trying - don't exit on pet failure, let the failure count mechanism handle it
				} else {
					// Success - reset failure count and update metrics
					s.resetFailureCount("watchdog")
					logger.V(1).Info("Watchdog pet successful", "watchdogPath", s.watchdog.Path())

					// Increment successful watchdog pets counter
					watchdogPetsCounter.Inc()

					// Update agent health status based on SBD health
					if s.isSBDHealthy() {
						agentHealthyGauge.Set(1)
					}
				}
			} else {
				logger.Error(nil, "Skipping watchdog pet - SBD device is unhealthy",
					"sbdDevicePath", s.sbdDevicePath,
					"sbdHealthy", s.isSBDHealthy())
				// Mark agent as unhealthy when SBD device is unhealthy
				agentHealthyGauge.Set(0)
				// This will cause the system to reboot via watchdog timeout
				// This is the desired behavior for self-fencing when SBD fails
			}
		}
	}
}

// sbdDeviceLoop continuously updates the SBD device with node status
func (s *SBDAgent) sbdDeviceLoop() {
	ticker := time.NewTicker(s.sbdUpdateInterval)
	defer ticker.Stop()

	logger.Info("Starting SBD device loop", "interval", s.sbdUpdateInterval)

	for {
		select {
		case <-s.ctx.Done():
			logger.Info("SBD device loop stopping")
			return
		case <-ticker.C:
			// Use retry mechanism for SBD device operations
			err := retry.Do(s.ctx, s.retryConfig, "write node ID to SBD", func() error {
				return s.writeNodeIDToSBD()
			})

			if err != nil {
				failureCount := s.incrementFailureCount("sbd")
				logger.Error(err, "Failed to write node ID to SBD device after retries",
					"devicePath", s.sbdDevicePath,
					"nodeID", s.nodeID,
					"failureCount", failureCount,
					"maxFailures", MaxConsecutiveFailures)

			} else {
				// Success - reset failure count and update status
				s.resetFailureCount("sbd")
				logger.V(1).Info("Successfully updated SBD device with node ID",
					"devicePath", s.sbdDevicePath,
					"nodeID", s.nodeID)
				s.setSBDHealthy(true)
				// Update agent health status
				agentHealthyGauge.Set(1)
			}
		}
	}
}

// heartbeatLoop continuously writes heartbeat messages to the SBD device
func (s *SBDAgent) heartbeatLoop() {
	ticker := time.NewTicker(s.heartbeatInterval)
	defer ticker.Stop()

	logger.Info("Starting SBD heartbeat loop", "interval", s.heartbeatInterval)

	for {
		select {
		case <-s.ctx.Done():
			logger.Info("SBD heartbeat loop stopping")
			return
		case <-ticker.C:
			// Use retry mechanism for heartbeat operations
			err := retry.Do(s.ctx, s.retryConfig, "write heartbeat to SBD", func() error {
				return s.writeHeartbeatToSBD()
			})

			if err != nil {
				failureCount := s.incrementFailureCount("heartbeat")
				logger.Error(err, "Failed to write heartbeat to SBD device after retries",
					"devicePath", s.sbdDevicePath,
					"nodeID", s.nodeID,
					"failureCount", failureCount,
					"maxFailures", MaxConsecutiveFailures)

			} else {
				// Success - reset failure count and update status
				s.resetFailureCount("heartbeat")
				// Only mark as healthy if it was previously unhealthy
				// The regular SBD device loop will also update this
				if !s.isSBDHealthy() {
					logger.Info("SBD device recovered during heartbeat write", "devicePath", s.sbdDevicePath)
					s.setSBDHealthy(true)
					// Update agent health status
					agentHealthyGauge.Set(1)
				}
			}
		}
	}
}

// peerMonitorLoop continuously reads peer heartbeats and checks liveness
func (s *SBDAgent) peerMonitorLoop() {
	ticker := time.NewTicker(s.peerCheckInterval)
	defer ticker.Stop()

	logger.Info("Starting peer monitor loop", "interval", s.peerCheckInterval)

	for {
		select {
		case <-s.ctx.Done():
			logger.Info("Peer monitor loop stopping")
			return
		case <-ticker.C:
			// First, check our own slot for fence messages directed at us
			if err := s.readOwnSlotForFenceMessage(); err != nil {
				logger.Info("Error reading own slot for fence messages", "error", err)
				s.incrementFailureCount("sbd")
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
			s.peerMonitor.CheckPeerLiveness()

			// Log cluster status periodically
			healthyPeers := s.peerMonitor.GetHealthyPeerCount()
			logger.Info("Cluster status", "healthyPeers", healthyPeers)
		}
	}
}

// validateSBDDevice checks if the SBD device is accessible
func validateSBDDevice(devicePath string) error {
	if devicePath == "" {
		return fmt.Errorf("SBD device path cannot be empty")
	}

	// Check if device exists
	info, err := os.Stat(devicePath)
	if err != nil {
		return fmt.Errorf("SBD device not accessible: %w", err)
	}

	// Check if it's a block device
	if info.Mode()&os.ModeDevice == 0 {
		logger.Info("WARNING: SBD device is not a device file", "devicePath", devicePath)
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
	envVars := []string{"SBD_NODE_ID", "NODE_ID"}

	for _, envVar := range envVars {
		if value := os.Getenv(envVar); value != "" {
			if id, err := strconv.ParseUint(value, 10, 16); err == nil && id >= 1 && id <= sbdprotocol.SBD_MAX_NODES {
				return uint16(id)
			}
		}
	}

	return DefaultNodeID
}

// getSBDTimeoutFromEnv gets the SBD timeout from environment variables if not provided via flag
func getSBDTimeoutFromEnv() uint {
	envVars := []string{"SBD_TIMEOUT_SECONDS", "SBD_TIMEOUT"}

	for _, envVar := range envVars {
		if value := os.Getenv(envVar); value != "" {
			if timeout, err := strconv.ParseUint(value, 10, 32); err == nil && timeout > 0 {
				return uint(timeout)
			}
		}
	}

	return 30 // Default timeout
}

// getRebootMethodFromEnv gets the reboot method from environment variables if not provided via flag
func getRebootMethodFromEnv() string {
	envVars := []string{"SBD_REBOOT_METHOD", "REBOOT_METHOD"}

	for _, envVar := range envVars {
		if value := os.Getenv(envVar); value != "" {
			if value == "panic" || value == "systemctl-reboot" {
				return value
			}
		}
	}

	return "panic" // Default method
}

// isSelfFenceDetected checks if a self-fence has been detected
func (s *SBDAgent) isSelfFenceDetected() bool {
	s.selfFenceMutex.RLock()
	defer s.selfFenceMutex.RUnlock()
	return s.selfFenceDetected
}

// setSelfFenceDetected sets the self-fence detected flag
func (s *SBDAgent) setSelfFenceDetected(detected bool) {
	s.selfFenceMutex.Lock()
	defer s.selfFenceMutex.Unlock()
	s.selfFenceDetected = detected
}

// executeSelfFencing performs the self-fencing action based on the configured method
// The systemctl-reboot method uses multiple aggressive techniques based on destructive testing
// that showed direct reboot commands are more effective than panic() in containerized environments
func (s *SBDAgent) executeSelfFencing(reason string) {
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
func (s *SBDAgent) readOwnSlotForFenceMessage() error {
	if s.sbdDevice == nil || s.sbdDevice.IsClosed() {
		return fmt.Errorf("SBD device is not available")
	}

	// Calculate slot offset for our own node
	slotOffset := int64(s.nodeID) * sbdprotocol.SBD_SLOT_SIZE

	// Read the entire slot
	slotData := make([]byte, sbdprotocol.SBD_SLOT_SIZE)
	n, err := s.sbdDevice.ReadAt(slotData, slotOffset)
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
		if fenceMsg.TargetNodeID == s.nodeID {
			reason := fmt.Sprintf("Fence message received from node %d, reason: %s",
				fenceMsg.Header.NodeID, sbdprotocol.GetFenceReasonName(fenceMsg.Reason))
			logger.Error(nil, "Fence message detected in own slot",
				"reason", reason,
				"sourceNodeID", fenceMsg.Header.NodeID,
				"targetNodeID", fenceMsg.TargetNodeID,
				"fenceReason", sbdprotocol.GetFenceReasonName(fenceMsg.Reason))

			// Execute self-fencing immediately
			s.executeSelfFencing(reason)
		} else {
			logger.V(1).Info("Fence message in own slot not directed at us",
				"targetNodeID", fenceMsg.TargetNodeID,
				"ourNodeID", s.nodeID,
				"sourceNodeID", fenceMsg.Header.NodeID)
		}
	}

	return nil
}

// runPreflightChecks performs critical startup validation before entering main event loops
// Returns success if EITHER watchdog is active OR SBD device is accessible (or both)
func runPreflightChecks(watchdogPath, sbdDevicePath, nodeName string, nodeID uint16) error {
	logger.Info("Running pre-flight checks",
		"watchdogPath", watchdogPath,
		"sbdDevicePath", sbdDevicePath,
		"nodeName", nodeName,
		"nodeID", nodeID)

	// Check watchdog device availability
	var watchdogErr error
	if watchdogPath != "" {
		watchdogErr = checkWatchdogDevice(watchdogPath)
	}

	// Check SBD device accessibility
	var sbdErr error
	if sbdDevicePath != "" {
		sbdErr = checkSBDDevice(sbdDevicePath, nodeID, nodeName)
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

	// SBD device is always required
	if sbdDevicePath == "" {
		return fmt.Errorf("SBD device path cannot be empty")
	}

	// Check if at least one critical component (watchdog OR SBD) is working
	if watchdogErr == nil && sbdErr == nil {
		logger.Info("All pre-flight checks passed successfully")
		return nil
	} else if watchdogErr == nil {
		return fmt.Errorf("pre-flight checks failed: SBD device is not available")
	} else if sbdErr == nil {
		return fmt.Errorf("pre-flight checks failed: watchdog device is not available")
	} else {
		return fmt.Errorf("pre-flight checks failed: both watchdog device and SBD device are inaccessible. Watchdog error: %v, SBD error: %v", watchdogErr, sbdErr)
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

// checkSBDDevice verifies the SBD device exists and performs a minimal read/write test
func checkSBDDevice(sbdDevicePath string, nodeID uint16, nodeName string) error {
	logger.V(1).Info("Checking SBD device accessibility", "sbdDevicePath", sbdDevicePath, "nodeID", nodeID)

	// Check if the SBD device file exists
	if _, err := os.Stat(sbdDevicePath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("SBD device does not exist: %s", sbdDevicePath)
		}
		return fmt.Errorf("failed to stat SBD device %s: %w", sbdDevicePath, err)
	}

	// Try to open the SBD device using the blockdevice package
	device, err := blockdevice.Open(sbdDevicePath)
	if err != nil {
		return fmt.Errorf("failed to open SBD device %s: %w", sbdDevicePath, err)
	}
	defer func() {
		if closeErr := device.Close(); closeErr != nil {
			logger.Error(closeErr, "Failed to close SBD device during pre-flight check",
				"sbdDevicePath", sbdDevicePath)
		}
	}()

	// Perform minimal read/write test: write node ID to its slot and read it back
	if err := performSBDReadWriteTest(device, nodeID, nodeName); err != nil {
		return fmt.Errorf("SBD device read/write test failed: %w", err)
	}

	logger.V(1).Info("SBD device read/write test completed successfully",
		"sbdDevicePath", sbdDevicePath,
		"nodeID", nodeID)
	return nil
}

// performSBDReadWriteTest writes the node ID to its slot and reads it back to verify functionality
func performSBDReadWriteTest(device BlockDevice, nodeID uint16, nodeName string) error {
	logger.V(1).Info("Performing SBD device read/write test", "nodeID", nodeID, "nodeName", nodeName)

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
		return fmt.Errorf("failed to write test message to SBD device at offset %d: %w", slotOffset, err)
	}

	if n != len(testMsgBytes) {
		return fmt.Errorf("partial write to SBD device: wrote %d bytes, expected %d", n, len(testMsgBytes))
	}

	// Sync to ensure data is written to storage
	if err := device.Sync(); err != nil {
		return fmt.Errorf("failed to sync SBD device after test write: %w", err)
	}

	// Read back the data to verify write was successful
	readBuffer := make([]byte, len(testMsgBytes))
	readN, err := device.ReadAt(readBuffer, slotOffset)
	if err != nil {
		return fmt.Errorf("failed to read test message from SBD device at offset %d: %w", slotOffset, err)
	}

	if readN != len(testMsgBytes) {
		return fmt.Errorf("partial read from SBD device: read %d bytes, expected %d", readN, len(testMsgBytes))
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
		return fmt.Errorf("failed to unmarshal test message read from SBD device: %w", err)
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

	logger.V(1).Info("SBD device read/write test successful",
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

// initializeKubernetesClients creates Kubernetes clients for SBDRemediation CR watching
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

	// Create runtime scheme with SBDRemediation types
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return nil, nil, fmt.Errorf("failed to add SBDRemediation types to scheme: %w", err)
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

// fencingLoop watches for SBDRemediation CRs and processes fencing requests
func (s *SBDAgent) fencingLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Ticker for periodic cleanup of completed leader electors
	cleanupTicker := time.NewTicker(30 * time.Second)
	defer cleanupTicker.Stop()

	logger.Info("Starting SBD fencing loop", "watchNamespace", s.watchNamespace)

	for {
		select {
		case <-s.ctx.Done():
			logger.Info("SBD fencing loop stopping")
			return
		case <-s.fencingStopCh:
			logger.Info("SBD fencing loop received stop signal")
			return
		case <-ticker.C:
			if err := s.processSBDRemediationCRs(); err != nil {
				logger.Error(err, "Failed to process SBDRemediation CRs")
			}
		case <-cleanupTicker.C:
			// Periodically clean up leader electors for completed/deleted remediations
			logger.V(1).Info("Performing periodic cleanup of completed leader electors")
			s.cleanupCompletedLeaderElectors()
		}
	}
}

// processSBDRemediationCRs lists and processes SBDRemediation CRs that need fencing
func (s *SBDAgent) processSBDRemediationCRs() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// List SBDRemediation CRs
	var remediations v1alpha1.SBDRemediationList
	listOpts := []client.ListOption{}

	// If a specific namespace is configured, limit to that namespace
	if s.watchNamespace != "" {
		listOpts = append(listOpts, client.InNamespace(s.watchNamespace))
	}

	if err := s.k8sClient.List(ctx, &remediations, listOpts...); err != nil {
		return fmt.Errorf("failed to list SBDRemediation CRs: %w", err)
	}

	logger.V(1).Info("Found SBDRemediation CRs to process", "count", len(remediations.Items))

	// Process each remediation
	for _, remediation := range remediations.Items {
		if err := s.processSingleRemediation(ctx, &remediation); err != nil {
			logger.Error(err, "Failed to process SBDRemediation",
				"name", remediation.Name,
				"namespace", remediation.Namespace,
				"targetNode", remediation.Spec.NodeName)
		}
	}

	return nil
}

// processSingleRemediation processes a single SBDRemediation CR
func (s *SBDAgent) processSingleRemediation(ctx context.Context, remediation *v1alpha1.SBDRemediation) error {
	// Skip if already completed
	if remediation.IsFencingSucceeded() {
		logger.V(1).Info("SBDRemediation already completed, skipping",
			"name", remediation.Name,
			"namespace", remediation.Namespace)
		return nil
	}

	// Skip if fencing is already in progress by another agent
	if remediation.IsFencingInProgress() {
		logger.V(1).Info("SBDRemediation fencing in progress by another agent, skipping",
			"name", remediation.Name,
			"namespace", remediation.Namespace)
		return nil
	}

	// Check if this agent should handle this remediation (simple leader election)
	// For now, we'll use the agent that has the lexicographically smallest node name
	// TODO: Implement proper leader election among agents
	if !s.shouldHandleRemediation(ctx, remediation) {
		logger.V(1).Info("Another agent should handle this remediation",
			"name", remediation.Name,
			"namespace", remediation.Namespace,
			"targetNode", remediation.Spec.NodeName,
			"ourNode", s.nodeName)
		return nil
	}

	logger.Info("Processing SBDRemediation for fencing",
		"name", remediation.Name,
		"namespace", remediation.Namespace,
		"targetNode", remediation.Spec.NodeName,
		"reason", remediation.Spec.Reason)

	// Mark fencing as in progress
	s.updateRemediationCondition(ctx, remediation, v1alpha1.SBDRemediationConditionFencingInProgress,
		metav1.ConditionTrue, "FencingInProgress", "Fencing operation started")

	// Execute fencing operation
	if err := s.executeFencing(ctx, remediation); err != nil {
		s.updateRemediationCondition(ctx, remediation, v1alpha1.SBDRemediationConditionFencingSucceeded,
			metav1.ConditionFalse, "FencingFailed", fmt.Sprintf("Failed to execute fencing: %v", err))
		s.updateRemediationCondition(ctx, remediation, v1alpha1.SBDRemediationConditionReady,
			metav1.ConditionTrue, "FencingFailed", fmt.Sprintf("Fencing failed: %v", err))
		return fmt.Errorf("failed to execute fencing for %s: %w", remediation.Spec.NodeName, err)
	}

	return nil
}

// shouldHandleRemediation determines if this agent should handle the remediation using leader election
func (s *SBDAgent) shouldHandleRemediation(ctx context.Context, remediation *v1alpha1.SBDRemediation) bool {
	remediationKey := fmt.Sprintf("%s/%s", remediation.Namespace, remediation.Name)

	// Check if we already have a leader elector for this remediation
	s.leaderElectorsMutex.RLock()
	elector, exists := s.leaderElectors[remediationKey]
	s.leaderElectorsMutex.RUnlock()

	if exists {
		// We have an existing leader elector, check if we're the leader
		isLeader := elector.IsLeader()
		if isLeader {
			logger.V(1).Info("Agent is leader for remediation",
				"remediation", remediationKey,
				"agentNode", s.nodeName)
		}
		return isLeader
	}

	// No leader elector exists for this remediation, create one
	logger.Info("Creating leader elector for new remediation",
		"remediation", remediationKey,
		"agentNode", s.nodeName)

	newElector, err := NewRemediationLeaderElector(
		remediationKey,
		s.nodeName,
		s.k8sClientset,
		s.leaderElectionConfig,
		logger.WithValues("remediation", remediationKey),
	)
	if err != nil {
		logger.Error(err, "Failed to create leader elector for remediation",
			"remediation", remediationKey,
			"agentNode", s.nodeName)
		return false
	}

	// Store the new elector
	s.leaderElectorsMutex.Lock()
	s.leaderElectors[remediationKey] = newElector
	s.leaderElectorsMutex.Unlock()

	// Start the leader election process
	newElector.Start(ctx)

	// Wait a short time for initial leader election to complete
	// This prevents race conditions where multiple agents create electors simultaneously
	select {
	case <-time.After(500 * time.Millisecond):
		// Timeout reached, check if we became leader
		isLeader := newElector.IsLeader()
		if isLeader {
			logger.Info("Agent elected as leader for remediation",
				"remediation", remediationKey,
				"agentNode", s.nodeName)
		} else {
			logger.V(1).Info("Agent not elected as leader for remediation",
				"remediation", remediationKey,
				"agentNode", s.nodeName)
		}
		return isLeader
	case <-ctx.Done():
		// Context cancelled
		return false
	}
}

// executeFencing performs the actual fencing operation via SBD device
func (s *SBDAgent) executeFencing(ctx context.Context, remediation *v1alpha1.SBDRemediation) error {
	targetNodeName := remediation.Spec.NodeName

	// Get target node ID using node manager
	targetNodeID, err := s.getNodeIDForName(targetNodeName)
	if err != nil {
		return fmt.Errorf("failed to get node ID for target node %s: %w", targetNodeName, err)
	}

	logger.Info("Writing fence message to SBD device",
		"targetNode", targetNodeName,
		"targetNodeID", targetNodeID,
		"reason", remediation.Spec.Reason)

	// Write fence message to target node's slot
	if err := s.writeFenceMessage(targetNodeID, remediation.Spec.Reason); err != nil {
		// Update remediation conditions to failed
		s.updateRemediationCondition(ctx, remediation, v1alpha1.SBDRemediationConditionFencingSucceeded,
			metav1.ConditionFalse, "FencingFailed", fmt.Sprintf("Failed to write fence message: %v", err))
		s.updateRemediationCondition(ctx, remediation, v1alpha1.SBDRemediationConditionReady,
			metav1.ConditionTrue, "FencingFailed", fmt.Sprintf("Fencing failed: %v", err))
		return fmt.Errorf("failed to write fence message to target node %d: %w", targetNodeID, err)
	}

	// Update remediation conditions to completed
	s.updateRemediationCondition(ctx, remediation, v1alpha1.SBDRemediationConditionFencingSucceeded,
		metav1.ConditionTrue, "FencingSucceeded", fmt.Sprintf("Fence message written to node %s (ID: %d)", targetNodeName, targetNodeID))
	s.updateRemediationCondition(ctx, remediation, v1alpha1.SBDRemediationConditionReady,
		metav1.ConditionTrue, "FencingSucceeded", fmt.Sprintf("Fencing completed successfully"))

	logger.Info("Fencing operation completed successfully",
		"targetNode", targetNodeName,
		"targetNodeID", targetNodeID)

	return nil
}

// getNodeIDForName gets the node ID for a given node name using the node manager
func (s *SBDAgent) getNodeIDForName(nodeName string) (uint16, error) {
	if s.nodeManager == nil {
		return 0, fmt.Errorf("node manager not initialized")
	}

	return s.nodeManager.GetSlotForNode(nodeName)
}

// writeFenceMessage writes a fence message to the target node's slot in the SBD device
func (s *SBDAgent) writeFenceMessage(targetNodeID uint16, reason v1alpha1.SBDRemediationReason) error {
	if s.sbdDevice == nil || s.sbdDevice.IsClosed() {
		return fmt.Errorf("SBD device is not available")
	}

	// Create fence message
	fenceReason := sbdprotocol.FENCE_REASON_MANUAL // Map from CR reason to SBD reason
	switch reason {
	case v1alpha1.SBDRemediationReasonHeartbeatTimeout:
		fenceReason = sbdprotocol.FENCE_REASON_HEARTBEAT_TIMEOUT
	case v1alpha1.SBDRemediationReasonNodeUnresponsive:
		fenceReason = sbdprotocol.FENCE_REASON_MANUAL
	case v1alpha1.SBDRemediationReasonManualFencing:
		fenceReason = sbdprotocol.FENCE_REASON_MANUAL
	}

	fenceMsg := sbdprotocol.SBDFenceMessage{
		Header:       sbdprotocol.NewFence(s.nodeID, targetNodeID, s.getNextHeartbeatSequence(), fenceReason),
		TargetNodeID: targetNodeID,
		Reason:       fenceReason,
	}
	msgData, err := sbdprotocol.MarshalFence(fenceMsg)
	if err != nil {
		return fmt.Errorf("failed to marshal fence message: %w", err)
	}

	// Calculate slot offset for the target node
	slotOffset := int64(targetNodeID) * sbdprotocol.SBD_SLOT_SIZE

	// Write fence message to target node's slot
	n, err := s.sbdDevice.WriteAt(msgData, slotOffset)
	if err != nil {
		sbdIOErrorsCounter.Inc()
		return fmt.Errorf("failed to write fence message to slot %d (offset %d): %w", targetNodeID, slotOffset, err)
	}

	if n != len(msgData) {
		return fmt.Errorf("partial write to slot %d: wrote %d bytes, expected %d", targetNodeID, n, len(msgData))
	}

	// Sync to ensure data is written to storage
	if err := s.sbdDevice.Sync(); err != nil {
		return fmt.Errorf("failed to sync fence message to storage: %w", err)
	}

	logger.Info("Fence message written successfully",
		"targetNodeID", targetNodeID,
		"sourceNodeID", s.nodeID,
		"reason", fenceReason,
		"slotOffset", slotOffset,
		"messageSize", len(msgData))

	return nil
}

// updateRemediationCondition updates a condition on an SBDRemediation CR
func (s *SBDAgent) updateRemediationCondition(ctx context.Context, remediation *v1alpha1.SBDRemediation, conditionType v1alpha1.SBDRemediationConditionType, status metav1.ConditionStatus, reason, message string) error {
	// Create a copy for status update
	updated := remediation.DeepCopy()
	updated.SetCondition(conditionType, status, reason, message)
	updated.Status.OperatorInstance = s.nodeName
	updated.Status.LastUpdateTime = &metav1.Time{Time: metav1.Now().Time}

	if err := s.k8sClient.Status().Update(ctx, updated); err != nil {
		logger.Error(err, "Failed to update SBDRemediation condition",
			"name", remediation.Name,
			"namespace", remediation.Namespace,
			"conditionType", conditionType,
			"status", status,
			"reason", reason,
			"message", message)
		return err
	}

	logger.Info("Updated SBDRemediation condition",
		"name", remediation.Name,
		"namespace", remediation.Namespace,
		"conditionType", conditionType,
		"status", status,
		"reason", reason,
		"message", message)

	return nil
}

func main() {
	flag.Parse()

	// Initialize structured logger first
	if err := initializeLogger(*logLevel); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}

	logger.Info("SBD Agent starting", "version", "development")

	// Log build information at startup
	logger.Info("SBD Agent build information", "buildInfo", version.GetFormattedBuildInfo())

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

	// Determine SBD timeout
	sbdTimeoutValue := *sbdTimeoutSeconds
	if sbdTimeoutValue == 30 { // Check if it's still the default
		sbdTimeoutValue = getSBDTimeoutFromEnv()
		logger.Info("Using SBD timeout from environment or default",
			"sbdTimeoutSeconds", sbdTimeoutValue)
	}

	// Determine reboot method
	rebootMethodValue := *rebootMethod
	if rebootMethodValue == "panic" { // Check if it's still the default
		rebootMethodValue = getRebootMethodFromEnv()
		logger.Info("Using reboot method from environment or default",
			"rebootMethod", rebootMethodValue)
	}

	// Validate reboot method
	if rebootMethodValue != "panic" && rebootMethodValue != "systemctl-reboot" {
		logger.Error(nil, "Invalid reboot method",
			"rebootMethod", rebootMethodValue,
			"validMethods", []string{"panic", "systemctl-reboot"})
		os.Exit(1)
	}

	// Calculate heartbeat interval (sbdTimeoutSeconds / 2)
	heartbeatInterval := time.Duration(sbdTimeoutValue/2) * time.Second
	if heartbeatInterval < time.Second {
		heartbeatInterval = time.Second // Minimum 1 second interval
	}

	// Validate required parameters
	if *sbdDevice == "" {
		logger.Error(nil, "SBD device is required - watchdog-only mode is no longer supported")
		os.Exit(1)
	}

	if err := validateSBDDevice(*sbdDevice); err != nil {
		logger.Error(err, "SBD device validation failed", "sbdDevice", *sbdDevice)
		os.Exit(1)
	}

	// Run pre-flight checks before creating the agent
	if err := runPreflightChecks(*watchdogPath, *sbdDevice, nodeNameValue, nodeIDValue); err != nil {
		logger.Error(err, "Pre-flight checks failed")
		os.Exit(1)
	}

	// Initialize Kubernetes clients if fencing is enabled
	var k8sClient client.Client
	var k8sClientset kubernetes.Interface
	if *enableFencing {
		var err error
		// Get kubeconfig from controller-runtime's auto-registered flag
		kubeconfigFlag := flag.Lookup("kubeconfig")
		kubeconfigPath := ""
		if kubeconfigFlag != nil {
			kubeconfigPath = kubeconfigFlag.Value.String()
		}
		k8sClient, k8sClientset, err = initializeKubernetesClients(kubeconfigPath)
		if err != nil {
			logger.Error(err, "Failed to initialize Kubernetes clients", "enableFencing", *enableFencing)
			os.Exit(1)
		}
		logger.Info("Kubernetes clients initialized successfully for fencing operations",
			"namespace", *namespace,
			"enableFencing", *enableFencing)
	} else {
		logger.Info("Fencing disabled - running in monitoring-only mode")
	}

	// Create SBD agent (hash mapping is always enabled)
	agent, err := NewSBDAgent(*watchdogPath, *sbdDevice, nodeNameValue, *clusterName, nodeIDValue, *petInterval, *sbdUpdateInterval, heartbeatInterval, *peerCheckInterval, sbdTimeoutValue, rebootMethodValue, *metricsPort, *staleNodeTimeout, *sbdFileLocking, *ioTimeout, k8sClient, k8sClientset, *namespace, *enableFencing)
	if err != nil {
		logger.Error(err, "Failed to create SBD agent",
			"watchdogPath", *watchdogPath,
			"sbdDevice", *sbdDevice,
			"nodeName", nodeNameValue,
			"nodeID", nodeIDValue)
		os.Exit(1)
	}

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start the agent
	if err := agent.Start(); err != nil {
		logger.Error(err, "Failed to start SBD agent")
		os.Exit(1)
	}

	// Wait for shutdown signal
	sig := <-sigChan
	logger.Info("Received shutdown signal", "signal", sig.String())

	// Stop the agent
	if err := agent.Stop(); err != nil {
		logger.Error(err, "Error during shutdown")
	}

	logger.Info("SBD Agent shutdown complete")
}
