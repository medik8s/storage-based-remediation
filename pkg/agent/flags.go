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

// SBD Agent command line flag constants
// These constants define the command line flags accepted by the sbd-agent.
// They are shared between the agent implementation and the controller
// to ensure consistency and prevent mismatches.
const (
	// FlagWatchdogPath specifies the path to the watchdog device
	FlagWatchdogPath = "watchdog-path"

	// FlagWatchdogTimeout specifies the watchdog timeout duration
	FlagWatchdogTimeout = "watchdog-timeout"

	// FlagPetInterval specifies the pet interval (how often to pet the watchdog)
	FlagPetInterval = "pet-interval"

	// FlagSBDDevice specifies the path to the SBD block device
	FlagSBDDevice = "sbd-device"

	// FlagSBDFileLocking enables file locking for SBD device operations
	FlagSBDFileLocking = "sbd-file-locking"

	// FlagNodeName specifies the name of this Kubernetes node
	FlagNodeName = "node-name"

	// FlagClusterName specifies the name of the cluster for node mapping
	FlagClusterName = "cluster-name"

	// FlagNodeID specifies the unique numeric ID for this node (deprecated)
	FlagNodeID = "node-id"

	// FlagSBDTimeoutSeconds specifies the SBD timeout in seconds
	FlagSBDTimeoutSeconds = "sbd-timeout-seconds"

	// FlagSBDUpdateInterval specifies the interval for updating SBD device with node status
	FlagSBDUpdateInterval = "sbd-update-interval"

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
)

// Default values for SBD Agent flags
// These constants define the default values used by the sbd-agent
const (
	// DefaultWatchdogPath is the default path to the watchdog device
	DefaultWatchdogPath = "/dev/watchdog"

	// DefaultWatchdogTimeout is the default watchdog timeout duration
	DefaultWatchdogTimeout = "60s"

	// DefaultPetInterval is the default pet interval
	DefaultPetInterval = "15s"

	// DefaultSBDDevice is the default SBD device path (empty means no SBD device)
	DefaultSBDDevice = ""

	// DefaultSBDFileLocking is the default SBD file locking setting
	DefaultSBDFileLocking = true

	// DefaultNodeName is the default node name (empty means get from environment)
	DefaultNodeName = ""

	// DefaultClusterName is the default cluster name
	DefaultClusterName = "default-cluster"

	// DefaultNodeID is the default node ID (0 means use hash-based mapping)
	DefaultNodeID = 0

	// DefaultSBDTimeoutSeconds is the default SBD timeout in seconds
	DefaultSBDTimeoutSeconds = 30

	// DefaultSBDUpdateInterval is the default SBD update interval
	DefaultSBDUpdateInterval = "5s"

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
	// SharedStorageSBDDeviceFile is the filename for the SBD device within shared storage
	SharedStorageSBDDeviceFile = "sbd-device"

	// SharedStorageNodeMappingFile is the filename for the node mapping within shared storage
	SharedStorageNodeMappingSuffix = ".nodemap"

	// SharedStorageSBDDeviceDirectory is the directory for the SBD device within shared storage
	SharedStorageSBDDeviceDirectory = "/sbd-shared"
)
