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

package agent

// SBR Agent command line flag constants
// These constants define the command line flags accepted by the sbr-agent.
// They are shared between the agent implementation and the controller
// to ensure consistency and prevent mismatches.
const (
	// FlagWatchdogPath specifies the path to the watchdog device
	FlagWatchdogPath = "watchdog-path"

	// FlagWatchdogTimeout specifies the watchdog timeout duration
	FlagWatchdogTimeout = "watchdog-timeout"

	// FlagPetInterval specifies the pet interval (how often to pet the watchdog)
	FlagPetInterval = "pet-interval"

	// FlagSBRDevice specifies the path to the SBR block device
	FlagSBRDevice = "sbr-device"

	// FlagSBRFileLocking enables file locking for SBR device operations
	FlagSBRFileLocking = "sbr-file-locking"

	// FlagNodeName specifies the name of this Kubernetes node
	FlagNodeName = "node-name"

	// FlagClusterName specifies the name of the cluster for node mapping
	FlagClusterName = "cluster-name"

	// FlagNodeID specifies the unique numeric ID for this node (deprecated)
	FlagNodeID = "node-id"

	// FlagSBRTimeoutSeconds specifies the SBR timeout in seconds
	FlagSBRTimeoutSeconds = "sbr-timeout-seconds"

	// FlagSBRUpdateInterval specifies the interval for updating SBR device with node status
	FlagSBRUpdateInterval = "sbr-update-interval"

	// FlagPeerCheckInterval specifies the interval for checking peer heartbeats
	FlagPeerCheckInterval = "peer-check-interval"

	// FlagLogLevel specifies the log level (debug, info, warn, error)
	FlagLogLevel = "log-level"

	// FlagRebootMethod specifies the method to use for self-fencing
	FlagRebootMethod = "reboot-method"

	// FlagMetricsPort specifies the port for Prometheus metrics endpoint
	FlagMetricsPort = "metrics-port"

	// FlagStaleNodeTimeout specifies the timeout for considering nodes stale
	FlagStaleNodeTimeout = "stale-node-timeout"

	// FlagDetectOnlyMode disables remediation: watchdog is disarmed, no self-fence
	FlagDetectOnlyMode = "detect-only-mode"
)

// Default values for SBR Agent flags
// These constants define the default values used by the sbr-agent
const (
	// DefaultWatchdogPath is the default path to the watchdog device
	DefaultWatchdogPath = "/dev/watchdog"

	// DefaultWatchdogTimeout is the default watchdog timeout duration
	DefaultWatchdogTimeout = "60s"

	// DefaultPetInterval is the default pet interval
	DefaultPetInterval = "15s"

	// DefaultSBRDevice is the default SBR device path (empty means no SBR device)
	DefaultSBRDevice = ""

	// DefaultSBRFileLocking is the default SBR file locking setting
	DefaultSBRFileLocking = true

	// DefaultNodeName is the default node name (empty means get from environment)
	DefaultNodeName = ""

	// DefaultClusterName is the default cluster name
	DefaultClusterName = "default-cluster"

	// DefaultNodeID is the default node ID (0 means use hash-based mapping)
	DefaultNodeID = 0

	// DefaultSBRTimeoutSeconds is the default SBR timeout in seconds
	DefaultSBRTimeoutSeconds = 30

	// DefaultSBRUpdateInterval is the default SBR update interval
	DefaultSBRUpdateInterval = "5s"

	// DefaultPeerCheckInterval is the default peer check interval
	DefaultPeerCheckInterval = "5s"

	// DefaultLogLevel is the default log level
	DefaultLogLevel = "info"

	// DefaultRebootMethod is the default reboot method
	DefaultRebootMethod = "systemctl-reboot"

	// DefaultMetricsPort is the default metrics port
	DefaultMetricsPort = 8082

	// DefaultStaleNodeTimeout is the default stale node timeout
	DefaultStaleNodeTimeout = "1h"
)

// Shared storage constants
const (
	// SharedStorageSBRDeviceFile is the filename for the SBR device within shared storage
	SharedStorageSBRDeviceFile = "sbr-device"

	// SharedStorageFenceDeviceFile is the filename for the fence device within shared storage
	SharedStorageFenceDeviceSuffix = "-fence"

	// SharedStorageNodeMappingFile is the filename for the node mapping within shared storage
	SharedStorageNodeMappingSuffix = "-nodemap"

	// SharedStorageSBRDeviceDirectory is the directory for the SBR device within shared storage
	SharedStorageSBRDeviceDirectory = "/dev/sbr"
)
