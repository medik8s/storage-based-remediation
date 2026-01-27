# Cloud-Native SBD Implementation Blueprint

This document outlines a detailed blueprint and a series of iterative prompts for building a cloud-native Storage-Based Death (SBD) solution for Kubernetes, integrated with Medik8s' Node Healthcheck Operator. The project is broken down into small, manageable steps to ensure incremental progress, robust testing, and adherence to best practices.

Here is the step-by-step blueprint:

## Project Blueprint: Cloud-Native SBD for Node Healthcheck (Medik8s)

### **Phase 1: Foundation - CRDs and Operator Scaffolding**

**Objective:** Establish the custom resource definitions (CRDs) and set up the basic Kubernetes Operator framework.

* **Chunk 1.1: `SBDConfig` CRD Definition**
  * Define the `SBDConfig` custom resource with fields for `sbdDevicePVCName`, `sbdTimeoutSeconds`, `sbdWatchdogPath`, `nodeExclusionList`, and `rebootMethod`.
  * Include schema validation.

* **Chunk 1.2: `SBDRemediation` CRD Definition**
  * Define the `SBDRemediation` custom resource with fields for `nodeName`, `remediationRequestTime`, and a `status` block (`Phase`, `Message`).
  * Include schema validation.

* **Chunk 1.3: Operator Project Initialization**

  * Initialize a new Kubebuilder (or Operator SDK) project.
  * Generate the API and controller for `SBDConfig` and `SBDRemediation`.

* **Chunk 1.4: Basic `SBDConfig` Controller Reconciliation**
  * Implement a minimal reconciler for `SBDConfig` that logs received events but performs no actions yet.
  * Add basic unit tests for the reconciler.

### **Phase 2: SBD Agent - Local Watchdog Integration**

**Objective:** Develop the SBD Agent's ability to interact with the local kernel watchdog and deploy it as a DaemonSet managed by the Operator.

* **Chunk 2.1: Local Kernel Watchdog Implementation**
  * Develop Go code for opening, petting, and closing the `/dev/watchdog` device.
  * Include error handling for watchdog device access.

* **Chunk 2.2: SBD Agent Containerization**
  * Create a Dockerfile for the SBD Agent, based on a minimal Go runtime image.
  * Ensure necessary permissions/capabilities for `/dev/watchdog`.

* **Chunk 2.3: Basic SBD Agent DaemonSet Manifest**
  * Draft a Kubernetes `DaemonSet` manifest for the SBD Agent, including a `privileged` security context and host path mount for `/dev/watchdog`.

* **Chunk 2.4: Operator Deployment of SBD Agent DaemonSet**
  * Enhance the `SBDConfig` reconciler to create and manage the SBD Agent `DaemonSet` based on parameters from the `SBDConfig` CR.
  * Add integration tests to verify DaemonSet deployment.

### **Phase 3: SBD Agent - Shared Block PV Interaction**

**Objective:** Enable the SBD Agent to access and perform raw I/O operations on the shared block PersistentVolume.

* **Chunk 3.1: Raw Block Device I/O in Go**
  * Develop Go functions for opening a raw block device path (e.g., `/dev/sbd-watchdog`), seeking to offsets, and performing direct read/write operations.
  * Include robust error handling for I/O operations.

* **Chunk 3.2: SBD Agent Integration with Shared PV**
  * Integrate the raw block device I/O functions into the SBD Agent.
  * Implement logic to write a unique node identifier to a pre-defined offset on the shared PV.

* **Chunk 3.3: DaemonSet Shared PV Mounting**
  * Update the SBD Agent `DaemonSet` manifest to include `volumeMode: Block` and mount the specified PersistentVolumeClaim as a raw block device.
  * Ensure appropriate `volumeDevices` and `volumeMounts` are configured.

* **Chunk 3.4: `SBDConfig` PV Parameter Extension**
  * Add fields to the `SBDConfig` CR for specifying the shared block PV's PVC name and the path where it will be mounted within the SBD Agent pod.
  * Update the `SBDConfig` reconciler to use these new fields when generating the DaemonSet.

### **Phase 4: SBD Protocol Implementation (Heartbeating)**

**Objective:** Implement the core SBD protocol for heartbeating and peer liveness detection on the shared block device within the SBD Agent.

* **Chunk 4.1: SBD Message Structure Definition**
  * Define Go structs representing the SBD protocol's message format, including header, node ID, timestamp, sequence number, and checksum fields.

* **Chunk 4.2: Agent Heartbeat Writing**
  * Implement a goroutine within the SBD Agent that periodically constructs and writes the agent's own heartbeat message to its designated slot on the shared SBD PV.
  * Include checksum calculation.

* **Chunk 4.3: Peer Heartbeat Reading and Liveness Detection**
  * Implement a goroutine within the SBD Agent that continuously reads heartbeats from all expected peer node slots on the shared SBD PV.
  * Develop logic to determine peer liveness based on heartbeat freshness and sequence numbers.

* **Chunk 4.4: Local Watchdog Integration with Shared PV Status**
  * Modify the local kernel watchdog petting logic to stop petting if the SBD Agent loses the ability to write to or read from the shared SBD PV (indicating an issue with shared storage connectivity).

### **Phase 5: SBD Agent - Self-Fencing**

**Objective:** Implement the SBD Agent's ability to self-fence (panic and reboot) if it detects a fence message directed at itself on the shared SBD device.

* **Chunk 5.1: Self-Fence Message Detection**
  * Enhance the SBD Agent's shared PV reading logic to specifically check its own slot for a "fence" message.

* **Chunk 5.2: Self-Fencing Action**
  * If a self-fence message is detected, implement logic to immediately cease petting the local kernel watchdog, leading to a node panic and reboot.
  * Add logging for self-fencing events.

### **Phase 6: SBD Operator - External Fencing Orchestration**

**Objective:** Implement the SBD Operator's role in orchestrating external fencing actions based on `SBDRemediation` CRs, including leader election for coordination.

* **Chunk 6.1: Leader Election for Fencing**
  * Implement leader election within the SBD Operator's controller responsible for writing external fence messages to the shared PV. This ensures only one instance attempts to fence at a time.

* **Chunk 6.2: `SBDRemediation` Reconciler**
  * Implement the `SBDRemediation` reconciler to watch for newly created `SBDRemediation` CRs.
  * Only the leader instance proceeds with fencing logic.

* **Chunk 6.3: Peer Fence Message Writing**
  * If the leader and an `SBDRemediation` CR targets a specific node, implement logic to write the appropriate "fence" message to that target node's slot on the shared SBD PV.
  * Include retry mechanisms for write operations.

* **Chunk 6.4: `SBDRemediation` Status Updates**
  * Update the `SBDRemediation` CR's `status` field to reflect the progress and outcome of the fencing attempt (e.g., `FencingInProgress`, `FencedSuccessfully`, `FailedToFence`).

### **Phase 7: Monitoring and Observability**

**Objective:** Integrate comprehensive monitoring and logging into both the SBD Agent and Operator for better troubleshooting and operational visibility.

* **Chunk 7.1: SBD Agent Prometheus Metrics**
  * Add Prometheus metrics endpoints to the SBD Agent, exposing metrics such as `sbd_agent_status_healthy`, `sbd_device_io_errors_total`, `sbd_watchdog_pets_total`, `sbd_peer_status` (per node), and `sbd_self_fenced_total`.

* **Chunk 7.2: ServiceMonitor Definition**
  * Generate a `ServiceMonitor` (for Prometheus Operator) manifest to scrape metrics from the SBD Agent DaemonSet.

* **Chunk 7.3: Kubernetes Event Emission**
  * Modify the SBD Operator to emit Kubernetes Events for key lifecycle actions (e.g., `SBDConfig` reconciled, `DaemonSet` deployed/updated, `SBDRemediation` initiated, `NodeFenced`).

* **Chunk 7.4: Structured Logging**
  * Implement structured logging (e.g., using `logr` or `zap`) for both the SBD Agent and Operator, with configurable log levels.
  * Ensure logs provide sufficient context for debugging.

### **Phase 8: Security and Robustness**

**Objective:** Enhance the solution with critical security measures and robust error handling to ensure reliability in production environments.

* **Chunk 8.1: Granular RBAC Refinement**
  * Review and refine the RBAC roles for both the SBD Operator and the SBD Agent ServiceAccounts to adhere to the Principle of Least Privilege.
  * Ensure the Agent does not have direct node deletion/update permissions.

* **Chunk 8.2: Pod Security Standards**
  * Apply appropriate Pod Security Context configurations to the SBD Agent DaemonSet, acknowledging the necessity for `privileged` mode due to raw device access, but limiting other capabilities where possible.
  * Document the security implications.

* **Chunk 8.3: Agent Pre-Flight Checks**
  * Add robust pre-flight checks to the SBD Agent startup logic to verify availability of `/dev/watchdog` and initial access to the shared SBD PV.
  * Agent should refuse to participate or enter a degraded state if critical dependencies are unavailable.

* **Chunk 8.4: Advanced Error Handling and Retries**
  * Implement comprehensive error handling, exponential backoff, and retry mechanisms for all critical operations (e.g., shared PV I/O, K8s API calls, watchdog petting).
  * Ensure graceful degradation and informative error messages.

---

## Prompts for Code Generation LLM

Here's the series of prompts to guide a code-generation LLM through the implementation, building incrementally. Each prompt is self-contained.

---

### **Prompt 1: Define SBDConfig CRD**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 2: Define SBDRemediation CRD**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDRemediation`.

**CRD Name:** `sbdremediations.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Namespaced`

**Spec Fields:**
- `nodeName` (string, required): The name of the node to be remediated.
- `remediationRequestTime` (string, optional, format: date-time): Timestamp of when the remediation was requested.

**Status Fields:**
- `phase` (string, optional): Current phase of the remediation (e.g., "Pending", "FencingInProgress", "FencedSuccessfully", "Failed").
- `message` (string, optional): A human-readable message describing the current status or error.

**Description:**
The `SBDRemediation` CRD is created by an external operator (e.g., Node Healthcheck Operator) to request SBD-based fencing for a specific unhealthy node. The SBD Operator will update its status.

**Validation:**
- `nodeName` must not be empty.

Please provide the complete YAML for the CRD.
```

---

### **Prompt 3: Initialize Kubebuilder Project and Generate APIs**

```text
Assume a new directory `sbd-operator`.

Generate the initial Kubebuilder project structure within this directory.
Then, generate the API and controller for both `SBDConfig` (cluster-scoped) and `SBDRemediation` (namespaced) using `v1alpha1` as the version and `medik8s.io` as the API group.

Do not generate any controller logic yet, just the API definitions and controller boilerplate.
```

---

### **Prompt 4: Basic SBDConfig Controller Reconciliation**

```text
Using the Kubebuilder project generated in the previous step, implement a basic reconciler for the `SBDConfig` controller.

**File:** `controllers/sbdconfig_controller.go`

**Requirements:**
- Implement the `Reconcile` method for `SBDConfig`.
- Inside `Reconcile`, retrieve the `SBDConfig` object.
- Log a simple informational message when an `SBDConfig` is reconciled, including its name.
- If the `SBDConfig` is not found (deleted), log that it's gone and return without error.
- Return `reconcile.Result{}, nil` on successful reconciliation.
- Add basic unit tests for the `Reconcile` function to ensure it retrieves and logs the SBDConfig correctly.
```

---

### **Prompt 5: Go Code for Local Kernel Watchdog Interaction**

```text
Generate Go code for a utility package that can interact with the local kernel watchdog device (e.g., `/dev/watchdog`).

**Package Name:** `watchdog`
**File:** `pkg/watchdog/watchdog.go`

**Functions:**
- `New(path string) (*Watchdog, error)`: Constructor to open the watchdog device at the given path.
- `(*Watchdog) Pet() error`: Method to "pet" (reset) the watchdog timer.
- `(*Watchdog) Close() error`: Method to close the watchdog device.

**Requirements:**
- Use `os.OpenFile` to open the device.
- Use `unix.IoctlSetInt` or similar appropriate syscall for `TIOCSPGRP` (or similar watchdog specific ioctl, e.g., `WDIOC_KEEPALIVE`) to pet the watchdog. Ensure the `ioctl` command is correct for Linux watchdog devices.
- Handle `os.PathError` and other potential errors during file operations and ioctls.
- Provide comprehensive comments for each function and important code blocks.
- Include a simple `main` function (for testing purposes, can be removed later) that opens, pets a few times, and closes the watchdog.
- Provide unit tests for `Pet()` and `Close()` (mocking file operations if necessary).
```

---

### **Prompt 6: Dockerfile for SBD Agent**

```text
Create a Dockerfile for the SBD Agent application.

**Context:**
- The SBD Agent Go application is located at `cmd/sbd-agent/main.go` and depends on the `pkg/watchdog` package (from previous steps) and will be compiled into an executable named `sbd-agent`.
- The agent needs access to `/dev/watchdog` and a shared block device (which will be mounted into the container later).
- The container should run with `CAP_SYS_ADMIN` and `privileged` mode for watchdog and raw device access.

**Requirements:**
- Use a multi-stage build: one stage for building the Go application and another for the minimal runtime image.
- Use a suitable base image for compilation (e.g., `golang:1.22-alpine`).
- Use a minimal base image for the final runtime (e.g., `alpine:3.18`).
- Set the entrypoint to the compiled `sbd-agent` executable.
- Ensure the final image is as small as possible.
- Provide necessary `USER`, `WORKDIR`, and `COPY` instructions.
- Add comments explaining each section of the Dockerfile.
```

---

### **Prompt 7: Initial SBD Agent DaemonSet Manifest**

```text
Generate a Kubernetes `DaemonSet` manifest for the SBD Agent.

**Requirements:**
- **Name:** `sbd-agent`
- **Namespace:** `sbd-system` (assume this namespace will be created)
- **Image:** `sbd-agent:latest` (placeholder, will be replaced with actual image in CI/CD)
- **Security Context:**
    - `privileged: true`
    - `capabilities:`
        - `add: ["SYS_ADMIN"]` (for watchdog access)
- **Volume Mounts:**
    - Mount `/dev/watchdog` from the host.
- **Node Affinity/Selector:** Ensure it runs on all Linux nodes.
- **Restart Policy:** `Always`
- **Resource Limits/Requests:** Minimal placeholders (e.g., 50m CPU, 128Mi Memory).

Please provide the complete YAML for the `DaemonSet`.
```

---

### **Prompt 8: Operator Deployment of SBD Agent DaemonSet**

```text
Modify the `SBDConfig` controller's `Reconcile` method (`controllers/sbdconfig_controller.go`) to deploy and manage the SBD Agent DaemonSet.

**Requirements:**
- Inside `Reconcile`, after retrieving the `SBDConfig` object, define the `DaemonSet` based on the configuration parameters.
- Use the `sbdWatchdogPath` from `SBDConfig` for the hostPath mount in the DaemonSet.
- The DaemonSet should be named `sbd-agent-<sbdconfig-name>`.
- Set the `SBDConfig` as the owner reference for the DaemonSet so it's garbage-collected when the `SBDConfig` is deleted.
- Use `controllerutil.CreateOrUpdate` to ensure the DaemonSet is created if it doesn't exist or updated if it drifts from the desired state.
- Handle errors from Kubernetes API calls.
- Add an integration test to verify that applying an `SBDConfig` CR successfully creates a `DaemonSet` with the correct configuration. This test should use a fake client or a test environment.
```

---

### **Prompt 9: Go Code for Raw Block Device I/O**

```text
Generate a new Go package for raw block device interaction.

**Package Name:** `blockdevice`
**File:** `pkg/blockdevice/blockdevice.go`

**Functions:**
- `Open(path string) (*Device, error)`: Constructor to open a raw block device.
- `(*Device) ReadAt(p []byte, off int64) (n int, err error)`: Reads `len(p)` bytes from the device at `off` offset.
- `(*Device) WriteAt(p []byte, off int64) (n int, err error)`: Writes `p` bytes to the device at `off` offset.
- `(*Device) Sync() error`: Flushes any buffered writes to disk (important for SBD).
- `(*Device) Close() error`: Closes the device.

**Requirements:**
- Use `os.OpenFile` with appropriate flags (`os.O_RDWR`, `os.O_SYNC`).
- Implement `ReadAt` and `WriteAt` using standard `io.ReaderAt` and `io.WriterAt` interfaces.
- Ensure `Sync()` explicitly calls `file.Sync()`.
- Provide comprehensive comments.
- Include unit tests for read/write/sync operations by creating a temporary file or using an in-memory mock to simulate a block device.
```

---

### **Prompt 10: SBD Agent Shared Block PV Integration**

```text
Modify the SBD Agent (`cmd/sbd-agent/main.go`) to integrate with the `blockdevice` package and write its own node ID to the shared block PV.

**Context:**
- The agent needs to know its `nodeName` and the `sbdDevicePath` (e.g., `/dev/sbd-watchdog`) via command-line arguments or environment variables.
- The agent will write its node ID to a fixed, pre-defined offset (e.g., `0`) for now.

**Requirements:**
- In `main.go`, parse command-line arguments or environment variables for `nodeName` and `sbdDevicePath`.
- Use the `blockdevice.Open` function to open the shared SBD device.
- In a loop (or periodic ticker), write the `nodeName` string (e.g., as `[]byte`) to offset `0` on the device.
- Call `Sync()` after writing.
- Implement error handling for block device operations; if writing fails, it should log the error and potentially stop petting the local watchdog (preliminary step for later self-fencing logic).
- Integrate with the existing local watchdog petting logic: if the SBD device cannot be accessed/written to, stop petting the local watchdog.
- Add unit tests for the agent's logic of interacting with the mocked block device.
```

---

### **Prompt 11: DaemonSet Shared PV Mounting & SBDConfig PV Parameters**

```text
Modify the SBD Agent `DaemonSet` manifest (as generated by the Operator) and the `SBDConfig` CRD/Controller to properly mount the shared SBD Block PV.

**Part 1: Update `SBDConfig` CRD (if not done already in prompt 1)**
- Ensure `sbdDevicePVCName` (string, required) is present in `SBDConfig` spec.

**Part 2: Update `SBDConfig` Controller (`controllers/sbdconfig_controller.go`)**
- When generating the `DaemonSet` based on `SBDConfig`, add a `volumeDevice` and `volumeMount` for the shared SBD PV.
- The `volumeDevice` should reference a `PersistentVolumeClaim` named by `sbdConfig.Spec.SBDDevicePVCName`.
- The `volumeMount` should be for `/dev/sbd-watchdog` (or a path derived from `sbdConfig.Spec.SBDWatchdogPath` if it's reused for the PV mount point). The `name` of the volume should be generic, e.g., `sbd-block-device`.
- Ensure `volumeMode: Block` is specified for the `PersistentVolumeClaim` in the DaemonSet.
- Pass the actual mount path of the SBD device (e.g., `/dev/sbd-watchdog`) to the SBD Agent via a command-line argument or environment variable.

**Part 3: Update SBD Agent `main.go`**
- Adjust `main.go` to receive the `sbdDevicePath` (the path where the PV is mounted, e.g., `/dev/sbd-watchdog`) via an environment variable or command-line argument.

**Testing:**
- Add an integration test that creates an `SBDConfig` with a PVC name, and verifies that the resulting DaemonSet manifest contains the correct `volumeDevice` and `volumeMount` configurations.
```

---

### **Prompt 12: SBD Protocol Message Structure Definition**

```text
Generate Go structs for the SBD protocol message format. This will be used by the SBD Agent.

**Package:** `sbdprotocol`
**File:** `pkg/sbdprotocol/message.go`

**Constants:**
- `SBD_MAGIC` (string): A magic string for validation.
- `SBD_HEADER_SIZE` (int): Size of the SBD message header.
- `SBD_SLOT_SIZE` (int): Size of a single SBD slot on the device.
- `SBD_MAX_NODES` (int): Maximum number of nodes supported.
- `SBD_MSG_TYPE_HEARTBEAT` (byte): Message type for heartbeat.
- `SBD_MSG_TYPE_FENCE` (byte): Message type for fence.

**Structs:**
- `SBDMessageHeader`: Represents the common header for all SBD messages.
    - `Magic` ([8]byte): Magic string for identification.
    - `Version` (uint16): Protocol version.
    - `Type` (byte): Message type (heartbeat, fence).
    - `NodeID` (uint16): Unique ID of the sending node.
    - `Timestamp` (uint64): Unix timestamp of message creation.
    - `Sequence` (uint64): Incremental sequence number.
    - `Checksum` (uint32): CRC32 checksum of the message.
- `SBDHeartbeatMessage`: Specific fields for heartbeat messages (if any beyond header). For now, assume heartbeats are just the header.
- `SBDFenceMessage`: Specific fields for fence messages.
    - `TargetNodeID` (uint16): ID of the node to be fenced.
    - `Reason` (uint8): Reason for fencing.

**Functions:**
- `NewHeartbeat(nodeID uint16, sequence uint64) SBDMessageHeader`: Creates a new heartbeat header.
- `NewFence(nodeID, targetNodeID uint16, sequence uint64, reason uint8) SBDMessageHeader`: Creates a new fence header.
- `Marshal(msg SBDMessageHeader) ([]byte, error)`: Serializes an `SBDMessageHeader` to a byte slice, including checksum calculation.
- `Unmarshal(data []byte) (*SBDMessageHeader, error)`: Deserializes a byte slice into an `SBDMessageHeader`, including checksum validation.
- `CalculateChecksum(data []byte) uint32`: Helper function to calculate CRC32 checksum.

**Requirements:**
- Use `encoding/binary` for byte-level marshaling/unmarshaling.
- Use `hash/crc32` for checksum calculation.
- Provide comprehensive comments for structs and functions.
- Include unit tests for `Marshal`, `Unmarshal`, and `CalculateChecksum`.
```

---

### **Prompt 13: SBD Agent Heartbeat Writing**

```text
Modify the SBD Agent (`cmd/sbd-agent/main.go`) to continuously write its own SBD heartbeat messages to its designated slot on the shared SBD PV.

**Context:**
- The agent will use the `sbdprotocol` package from the previous step.
- Assume each node gets a unique `NodeID` (e.g., from an environment variable or determined by parsing a range from the `SBDConfig`). For this step, use a placeholder `NodeID` (e.g., 1).
- The `sbdTimeoutSeconds` from `SBDConfig` determines the heartbeat interval.
- Each node will write to its `NodeID * SBD_SLOT_SIZE` offset.

**Requirements:**
- In `main.go`, obtain the `sbdTimeoutSeconds` (e.g., from `SBDConfig` directly or passed as an environment variable from the DaemonSet).
- Implement a goroutine that:
    1.  Initializes a sequence number.
    2.  In a loop, creates a new `SBDHeartbeatMessage` using `sbdprotocol.NewHeartbeat`.
    3.  Marshals the message using `sbdprotocol.Marshal`.
    4.  Writes the marshaled bytes to the shared SBD device at the correct `slotOffset` (`NodeID * SBD_SLOT_SIZE`).
    5.  Calls `blockdevice.Sync()`.
    6.  Increments the sequence number.
    7.  Waits for `sbdTimeoutSeconds / 2` (or similar interval) before the next heartbeat.
- Implement robust error handling for block device writes. If a write fails, log the error and stop petting the local watchdog.
- Add unit tests for the heartbeat writing logic, mocking the `blockdevice` interface.
```

---

### **Prompt 14: SBD Agent Peer Heartbeat Reading and Liveness Detection**

```text
Modify the SBD Agent (`cmd/sbd-agent/main.go`) to continuously read peer heartbeats from the shared SBD PV and detect peer liveness.

**Context:**
- The agent needs to know the maximum number of nodes (`SBD_MAX_NODES`) and the `sbdTimeoutSeconds` to determine peer liveness.
- Each node's heartbeat is in its `NodeID * SBD_SLOT_SIZE` offset.

**Requirements:**
- In `main.go`, implement a separate goroutine dedicated to reading and processing peer heartbeats.
- This goroutine should:
    1.  Continuously iterate through all possible `NodeID` slots (from `0` to `SBD_MAX_NODES - 1`, excluding its own `NodeID`).
    2.  For each slot, read `SBD_SLOT_SIZE` bytes from the shared SBD device.
    3.  Unmarshal the bytes into an `SBDMessageHeader` using `sbdprotocol.Unmarshal`.
    4.  Validate the message (magic, version, checksum, type == HEARTBEAT).
    5.  Maintain a map or similar structure to store the last observed `Timestamp` and `Sequence` for each `NodeID`.
    6.  Implement liveness detection: If a peer's heartbeat has not been updated within `sbdTimeoutSeconds` (or a multiple thereof), or if its sequence number significantly lags, mark that peer as "unhealthy" or "down".
    7.  Log changes in peer status.
- Add unit tests for the peer heartbeat reading and liveness detection logic, mocking the `blockdevice` interface and providing various scenarios (stale heartbeats, missing heartbeats).
```

---

### **Prompt 15: SBD Agent Self-Fencing Logic**

```text
Modify the SBD Agent (`cmd/sbd-agent/main.go`) to detect a fence message directed at itself on the shared SBD PV and initiate self-fencing.

**Context:**
- The agent should continue to read its *own* slot for `SBDFenceMessage` types.
- The `rebootMethod` from `SBDConfig` (passed via environment variable to the agent) determines the action.

**Requirements:**
- In the peer heartbeat reading goroutine (or a dedicated self-fence monitoring goroutine):
    1.  After reading and unmarshaling the message from its *own* slot, check if `message.Type == SBD_MSG_TYPE_FENCE`.
    2.  If it's a fence message, log a critical message indicating self-fencing initiated.
    3.  Based on the `rebootMethod`:
        - If `panic` (default), immediately stop petting the local watchdog and exit with `log.Fatal` or `panic()`.
        - If `systemctl-reboot`, attempt to execute `sudo systemctl reboot` (this will require careful DaemonSet permissions and might be a future enhancement for a privileged agent). For now, prioritize the panic method as it's more reliable for immediate fencing.
    4.  Ensure that once a self-fence is detected, the agent **never** pets the local watchdog again.
- Add unit tests that simulate a self-fence message being written to the agent's slot and verify that the agent's internal state reflects an impending reboot/panic.
```

---

### **Prompt 16: SBD Operator Leader Election**

```text
Modify the SBD Operator (`controllers/sbdremediation_controller.go` or a new dedicated fencing controller) to implement leader election.

**Requirements:**
- Use `k8s.io/client-go/tools/leaderelection` or `sigs.k8s.io/controller-runtime/pkg/manager.Options.LeaderElection`.
- The `SBDRemediation` reconciler should only attempt to write fence messages to the shared PV if it is the current leader.
- Implement the leader election configuration in the `main.go` of the operator.
- Add a log message when an operator instance becomes the leader and when it loses leadership.
- Ensure the reconciliation logic for `SBDRemediation` checks for leadership before proceeding with external fencing actions.
- Provide a clear comment block explaining why leader election is crucial here.
```

---

### **Prompt 17: SBDRemediation Reconciler & External Fence Message Writing (Leader Only)**

```text
Modify the `SBDRemediation` controller's `Reconcile` method (`controllers/sbdremediation_controller.go`) to, as the leader, write a fence message for the target node to the shared SBD PV.

**Context:**
- This controller needs access to the shared SBD block device. This implies the operator *itself* will need to mount the shared PV, or there will be a separate component that does. For simplicity in this step, let's assume the operator `Pod` can access a temporary mount of the SBD device, or that the `blockdevice` functions can operate on a path the operator *can* access (e.g., if the operator also gets a `volumeDevice` mount). A more robust solution might involve the leader sending an instruction to a designated SBD Agent pod to perform the write. For now, let's assume the operator has direct mount capabilities.

**Assumptions for this prompt:**
- The Operator's pod will also have the shared SBD Block PV mounted at a known path (e.g., `/mnt/sbd-operator-device`).
- The Operator knows the mapping of `nodeName` to `NodeID` (e.g., via a `SBDConfig` field or a helper function that translates). For now, assume a simple mapping like `NodeID = 1` for `node-1`, `NodeID = 2` for `node-2`, etc.

**Requirements:**
- In `SBDRemediation` `Reconcile`:
    1.  Retrieve the `SBDRemediation` object.
    2.  Check if this operator instance is the leader (using the leader election status from Prompt 16). If not, log and requeue.
    3.  If a `SBDRemediation` is found and it has not completed successfully (not Ready=True with FencingSucceeded=True):
        - Determine the `NodeID` of the `remediation.Spec.NodeName`.
        - Create an `SBDFenceMessage` using `sbdprotocol.NewFence`.
        - Use the `blockdevice` package to open the shared SBD device at `/mnt/sbd-operator-device`.
        - Write the marshaled fence message to the target node's slot (`targetNodeID * SBD_SLOT_SIZE`).
        - Call `blockdevice.Sync()`.
        - Handle errors from block device operations (e.g., temporary errors might requeue).
        - Update the `SBDRemediation` status to `FencingInProgress`.
    4.  If the `SBDRemediation` is being deleted, handle cleanup (though SBD fence messages are usually persistent).
- Update the operator's `Deployment` manifest (or assume it's part of the general operator deployment) to include the necessary `volumeDevice` and `volumeMount` for the shared SBD PV.
- Add integration tests for the `SBDRemediation` reconciler, verifying that as a leader, it attempts to write a fence message for a target node when a new `SBDRemediation` CR is applied.
```

---

### **Prompt 18: SBDRemediation Status Updates**

```text
Enhance the `SBDRemediation` controller (`controllers/sbdremediation_controller.go`) to robustly update the `SBDRemediation` CR's `status` field.

**Requirements:**
- After a successful write of a fence message to the shared SBD PV, update the `SBDRemediation` conditions:
  - Set `FencingSucceeded=True` with reason "FencingCompleted" and message "Node successfully fenced via SBD."
  - Set `Ready=True` with reason "Succeeded" and message "Fencing completed successfully."
- If an error occurs during the fencing process (e.g., cannot open device, write error, checksum error from previous read):
    - Set `FencingSucceeded=False` with reason "FencingFailed" and appropriate error message
    - Set `Ready=True` with reason "Failed" and error details
    - Update `status.message` with a detailed error description.
    - Implement retry logic (e.g., requeue with backoff) for transient errors before marking as `Failed`.
- Use `client.Status().Update()` to persist status changes.
- Ensure the `status` update logic is idempotent and handles concurrent updates correctly.
- Add integration tests for status updates:
    - Verify `FencedSuccessfully` status after a simulated successful fence write.
    - Verify `Failed` status after a simulated fence write failure.
```

---

### **Prompt 19: SBD Agent Prometheus Metrics**

```text
Modify the SBD Agent (`cmd/sbd-agent/main.go`) to expose Prometheus metrics.

**Requirements:**
- Use the `github.com/prometheus/client_golang/prometheus` and `github.com/prometheus/client_golang/prometheus/promhttp` libraries.
- Expose an HTTP endpoint (e.g., `/metrics`) on a configurable port (e.g., `8080`).
- Define and register the following metrics:
    - `sbd_agent_status_healthy` (Gauge): `1` if the agent is healthy, `0` otherwise (e.g., if it can't access watchdog or SBD device).
    - `sbd_device_io_errors_total` (Counter): Total number of I/O errors encountered when interacting with the shared SBD device.
    - `sbd_watchdog_pets_total` (Counter): Total number of times the local kernel watchdog has been successfully "petted".
    - `sbd_peer_status` (GaugeVec with labels: `node_id`, `node_name`, `status`): Current liveness status of each peer node (e.g., `1` for alive, `0` for unhealthy/down).
    - `sbd_self_fenced_total` (Counter): Total number of times the agent has initiated a self-fence.
- Increment/set these metrics at appropriate points in the SBD Agent's logic.
- Add comments explaining each metric.
- Ensure the HTTP server runs in its own goroutine.
```

---

### **Prompt 20: ServiceMonitor Definition for SBD Agent**

```text
Generate a Kubernetes `Service` and `ServiceMonitor` manifest for scraping Prometheus metrics from the SBD Agent DaemonSet.

**Requirements:**
- **Service:**
    - Name: `sbd-agent-metrics`
    - Namespace: `sbd-system`
    - Selector: Match labels of the SBD Agent DaemonSet (`app: sbd-agent`).
    - Port: Expose the metrics port (e.g., `8080`, named `metrics`).
- **ServiceMonitor (assuming Prometheus Operator is installed):**
    - Name: `sbd-agent`
    - Namespace: `sbd-system`
    - Selector: Match labels of the `sbd-agent-metrics` Service.
    - Endpoints: Target the `metrics` port.
    - Path: `/metrics`
    - Interval: `30s` (or suitable interval).

Please provide the complete YAML for both the `Service` and `ServiceMonitor`.
```

---

### **Prompt 21: Kubernetes Event Emission in SBD Operator**

```text
Modify the SBD Operator (`controllers/sbdconfig_controller.go` and `controllers/sbdremediation_controller.go`) to emit Kubernetes Events for key lifecycle actions.

**Requirements:**
- Use `k8s.io/client-go/tools/record.EventRecorder`.
- Inject the `EventRecorder` into both `SBDConfigReconciler` and `SBDRemediationReconciler`.
- Emit events for:
    - `SBDConfig` reconciled: `Normal`, `SBDConfigReconciled`, "SBDConfig '%s' successfully reconciled."
    - `DaemonSet` deployed/updated: `Normal`, `DaemonSetManaged`, "DaemonSet '%s' for SBD Agent managed."
    - `SBDRemediation` initiated: `Normal`, `FencingInitiated`, "Fencing initiated for node '%s' via SBD."
    - `NodeFencedSuccessfully`: `Normal`, `NodeFenced`, "Node '%s' successfully fenced via SBD."
    - `NodeFencingFailed`: `Warning`, `FencingFailed`, "Failed to fence node '%s' via SBD: %s"
    - Other significant events (e.g., errors accessing SBD device by operator).
- Add a helper function or method to simplify event emission.
- Add unit tests for event emission, mocking the `EventRecorder`.
```

---

### **Prompt 22: Structured Logging for SBD Agent and Operator**

```text
Implement structured logging for both the SBD Agent and the SBD Operator.

**Part 1: SBD Agent (`cmd/sbd-agent/main.go`)**
- Use `github.com/go-logr/zapr` (or similar structured logging library like `logrus` or `zap` directly) for logging.
- Initialize a global logger or pass it around to relevant functions.
- Replace all `fmt.Println`, `log.Printf`, etc., with structured log calls (e.g., `log.Info("message", "key", "value")`).
- Add a flag or environment variable for configurable log levels (e.g., debug, info, warn, error).

**Part 2: SBD Operator (`main.go` and controller files)**
- The Kubebuilder project already uses `logr`. Ensure it's used consistently.
- Review existing logs and enhance them with additional key-value pairs for better context (e.g., `Request.NamespacedName`, `SBDConfig.Name`, `NodeName`).
- Ensure error logging includes the actual error object.
- Make sure `setupWithManager` for both controllers also gets the logger.

**Requirements:**
- All log messages should be structured.
- Sensitive information should not be logged.
- Log levels should be respected.
- Provide examples of how to log informational messages, warnings, and errors with context.
```

---

### **Prompt 23: Granular RBAC Refinement for SBD Agent and Operator**

```text
Review and refine the RBAC roles for the SBD Agent and SBD Operator.

**Part 1: SBD Agent (`config/rbac/sbd_agent_role.yaml` and `config/rbac/sbd_agent_role_binding.yaml`)**
- Define a `Role` (or `ClusterRole` if needed) for the SBD Agent ServiceAccount.
- The Agent should only need permissions to:
    - Read its own `Pod` (for node name).
    - Read `Nodes` (to get node IDs, potentially).
    - **Crucially, it should NOT have permissions to delete/update `Nodes` or `Pods` directly.** Its fencing action is via the SBD device and local panic.
- Ensure the `ServiceAccount` and `RoleBinding` are correctly configured.

**Part 2: SBD Operator (`config/rbac/sbd_operator_role.yaml` and `config/rbac/sbd_operator_role_binding.yaml`)**
- Review the Kubebuilder generated `ClusterRole` for the operator.
- The Operator needs permissions to:
    - Watch, Get, List `SBDConfig` and `SBDRemediation` CRs.
    - Create, Update, Delete `DaemonSets`.
    - Update `SBDRemediation` status.
    - Access `Pods` (for leader election).
    - Access `Events` (to emit them).
    - Access `Nodes` (read-only, to resolve node names or get node IDs).
- Ensure the `ServiceAccount` and `RoleBinding` are correctly configured.

**Requirements:**
- Generate only the YAML for the RBAC resources (ServiceAccounts, Roles/ClusterRoles, RoleBindings/ClusterRoleBindings).
- Add comments explaining each permission.
- Adhere strictly to the Principle of Least Privilege.
```

---

### **Prompt 24: SBD Agent Pre-Flight Checks**

```text
Implement robust pre-flight checks in the SBD Agent's startup logic (`cmd/sbd-agent/main.go`).

**Requirements:**
- Before entering the main event loop (heartbeating, peer monitoring):
    1.  **Watchdog Device Availability:** Verify that the configured `sbdWatchdogPath` exists and can be opened. If not, log a critical error and exit immediately (e.g., `os.Exit(1)` or `log.Fatal`).
    2.  **Shared SBD Device Accessibility:** Attempt to open and perform a minimal read/write test (e.g., write its node ID to its slot and read it back) on the `sbdDevicePath`. If this fails, log a critical error and exit.
    3.  **Node ID/Name Resolution:** Ensure the agent can successfully determine its own `nodeName` and map it to a valid SBD `NodeID`. If resolution fails, exit.
- These checks should happen once at startup. If any fail, the agent should not proceed to participate in the SBD cluster.
- Add unit tests for these pre-flight checks, simulating success and various failure scenarios (missing files, permission errors).
```

---

### **Prompt 25: Advanced Error Handling and Retries**

```text
Implement advanced error handling, exponential backoff, and retry mechanisms for critical operations in both the SBD Agent and SBD Operator.

**Part 1: SBD Agent (`cmd/sbd-agent/main.go` and `pkg/blockdevice`)**
- For all interactions with `/dev/watchdog` and the shared SBD block device (`blockdevice` package and its usage in agent), implement:
    - Retries with exponential backoff for transient errors (e.g., `EIO`, device not ready). Limit the number of retries.
    - Specific error handling for non-transient errors (e.g., `ENOENT` for missing device).
    - If a critical operation (like petting watchdog or writing heartbeat) consistently fails after retries, trigger a self-fence or a graceful shutdown.

**Part 2: SBD Operator (`controllers/sbdconfig_controller.go`, `controllers/sbdremediation_controller.go`)**
- For Kubernetes API calls (creating/updating DaemonSets, updating SBDRemediation status):
    - Use `reconcile.Result{RequeueAfter: ...}` for transient errors to trigger a requeue with backoff.
    - Log detailed errors, distinguishing between transient and permanent issues.
    - For `SBDRemediation` fencing writes, implement retries to the shared SBD device as a leader.

**Requirements:**
- Use `time.Sleep` for simple backoff, or a more sophisticated retry library if preferred.
- Define constants for max retries and initial backoff duration.
- Ensure that permanent errors are not endlessly retried.
- Add comments explaining the retry logic.
- Update relevant tests to cover retry scenarios.
```

---

### **Prompt 26: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 27: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 28: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 29: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 30: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 31: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 32: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 33: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 34: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 35: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 36: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 37: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 38: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 39: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 40: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 41: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 42: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 43: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 44: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 45: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 46: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 47: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 48: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 49: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 50: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 51: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 52: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 53: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 54: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 55: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 56: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 57: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 58: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 59: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 60: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 61: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 62: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 63: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 64: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 65: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 66: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 67: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 68: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 69: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 70: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 71: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 72: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 73: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 74: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 75: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 76: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 77: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 78: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 79: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 80: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 81: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 82: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 83: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 84: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 85: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 86: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 87: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 88: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 89: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 90: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 91: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 92: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 93: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 94: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 95: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 96: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 97: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 98: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 99: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 100: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 101: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 102: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 103: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 104: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 105: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 106: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 107: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 108: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 109: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 110: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 111: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 112: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 113: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 114: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 115: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 116: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 117: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 118: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 119: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 120: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 121: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 122: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 123: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 124: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 125: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 126: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 127: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 128: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 129: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 130: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 131: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 132: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 133: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 134: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-fencing (e.g., "panic", "systemctl-reboot").

**Description:**
The `SBDConfig` CRD defines cluster-wide parameters for the Storage-Based Death (SBD) fencing mechanism.

**Validation:**
- `sbdTimeoutSeconds` must be a positive integer.
- `sbdDevicePVCName` and `sbdWatchdogPath` must not be empty.
- `rebootMethod` should validate against "panic" or "systemctl-reboot".

Please provide the complete YAML for the CRD.
```

---

### **Prompt 135: Updated SBDConfig API**

```text
Please generate the Kubernetes CustomResourceDefinition (CRD) YAML for `SBDConfig`.

**CRD Name:** `sbdconfigs.medik8s.io`
**Group:** `medik8s.io`
**Version:** `v1alpha1`
**Scope:** `Cluster`

**Spec Fields:**
- `sbdDevicePVCName` (string, required): The name of the PVC providing the shared block device.
- `sbdTimeoutSeconds` (integer, required): The SBD timeout in seconds.
- `sbdWatchdogPath` (string, required): The path to the local kernel watchdog device (e.g., `/dev/watchdog`).
- `nodeExclusionList` (array of strings, optional): A list of node names to exclude from SBD management.
- `rebootMethod` (string, optional, default: "panic"): The method to use for self-reboot
