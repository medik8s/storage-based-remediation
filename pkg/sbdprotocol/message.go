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

// Package sbdprotocol implements the SBD (Storage-Based Death) protocol message format
// for inter-node communication in cluster environments.
package sbdprotocol

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"time"
)

// SBD Protocol Constants
const (
	// SBD_MAGIC is the magic string used to identify valid SBD messages
	SBD_MAGIC = "SBDMSG01"

	// SBD_HEADER_SIZE is the size in bytes of the SBD message header
	SBD_HEADER_SIZE = 33 // 8 + 2 + 1 + 2 + 8 + 8 + 4 = 33 bytes

	// SBD_SLOT_SIZE is the size in bytes of a single SBD slot on the device
	SBD_SLOT_SIZE = 512

	// SBD_MAX_NODES is the maximum number of nodes supported in an SBD cluster
	SBD_MAX_NODES = 255

	// SBD_MSG_TYPE_HEARTBEAT identifies heartbeat messages
	SBD_MSG_TYPE_HEARTBEAT byte = 0x01

	// SBD_MSG_TYPE_FENCE identifies fence messages
	SBD_MSG_TYPE_FENCE byte = 0x02
)

// SBD Fence Reason Constants
const (
	// FENCE_REASON_HEARTBEAT_TIMEOUT indicates fencing due to missed heartbeats
	FENCE_REASON_HEARTBEAT_TIMEOUT uint8 = 0x01

	// FENCE_REASON_MANUAL indicates manual fencing request
	FENCE_REASON_MANUAL uint8 = 0x02

	// FENCE_REASON_SPLIT_BRAIN indicates fencing due to split-brain detection
	FENCE_REASON_SPLIT_BRAIN uint8 = 0x03

	// FENCE_REASON_RESOURCE_CONFLICT indicates fencing due to resource conflicts
	FENCE_REASON_RESOURCE_CONFLICT uint8 = 0x04
)

// SBDMessageHeader represents the common header for all SBD messages.
// This header is present in all SBD protocol messages and contains essential
// metadata for message validation, routing, and ordering.
type SBDMessageHeader struct {
	// Magic contains the magic string for message identification and validation
	Magic [8]byte

	// Version specifies the SBD protocol version (currently 1)
	Version uint16

	// Type identifies the message type (heartbeat, fence, etc.)
	Type byte

	// NodeID is the unique identifier of the sending node
	NodeID uint16

	// Timestamp contains the Unix timestamp (nanoseconds) when the message was created
	Timestamp uint64

	// Sequence is an incremental sequence number for message ordering
	Sequence uint64

	// Checksum contains the CRC32 checksum of the entire message for validation
	Checksum uint32
}

// SBDHeartbeatMessage represents a heartbeat message in the SBD protocol.
// Heartbeat messages are used to indicate that a node is alive and functioning.
// Currently, heartbeat messages only contain the header information.
type SBDHeartbeatMessage struct {
	Header SBDMessageHeader
}

// SBDFenceMessage represents a fence message in the SBD protocol.
// Fence messages are used to request that a specific node be fenced (shut down)
// due to various reasons such as missed heartbeats or resource conflicts.
type SBDFenceMessage struct {
	Header       SBDMessageHeader
	TargetNodeID uint16 // ID of the node to be fenced
	Reason       uint8  // Reason for fencing
	NodeID       uint16 // ID of the node that is fencing the target node
}

// NewHeartbeat creates a new SBD heartbeat message header.
// It initializes all required fields with appropriate values and sets the current timestamp.
//
// Parameters:
//   - nodeID: Unique identifier of the sending node
//   - sequence: Incremental sequence number for message ordering
//
// Returns:
//   - SBDMessageHeader: Initialized heartbeat message header
func NewHeartbeat(nodeID uint16, sequence uint64) SBDMessageHeader {
	var magic [8]byte
	copy(magic[:], SBD_MAGIC)

	return SBDMessageHeader{
		Magic:     magic,
		Version:   1,
		Type:      SBD_MSG_TYPE_HEARTBEAT,
		NodeID:    nodeID,
		Timestamp: uint64(time.Now().UnixNano()),
		Sequence:  sequence,
		Checksum:  0, // Will be calculated during marshaling
	}
}

// NewFence creates a new SBD fence message.
// It initializes all required fields for a fence request with the specified target and reason.
//
// Parameters:
//   - nodeID: Unique identifier of the sending node
//   - targetNodeID: Unique identifier of the node to be fenced
//   - sequence: Incremental sequence number for message ordering
//   - reason: Reason code for the fencing request
//
// Returns:
//   - SBDFenceMessage: Initialized fence message
func NewFence(nodeID, targetNodeID uint16, sequence uint64, reason uint8) SBDFenceMessage {
	var magic [8]byte
	copy(magic[:], SBD_MAGIC)

	return SBDFenceMessage{
		Header: SBDMessageHeader{
			Magic:     magic,
			Version:   1,
			Type:      SBD_MSG_TYPE_FENCE,
			NodeID:    targetNodeID,
			Timestamp: uint64(time.Now().UnixNano()),
			Sequence:  sequence,
			Checksum:  0, // Will be calculated during marshaling
		},
		TargetNodeID: targetNodeID,
		Reason:       reason,
		NodeID:       nodeID,
	}
}

// Marshal serializes an SBDMessageHeader to a byte slice using binary encoding.
// It calculates and includes the CRC32 checksum of the message for validation.
//
// The marshaled format uses little-endian byte order for consistency across
// different architectures.
//
// Parameters:
//   - msg: The SBD message header to serialize
//
// Returns:
//   - []byte: Serialized message as byte slice
//   - error: Error if marshaling fails
func Marshal(msg SBDMessageHeader) ([]byte, error) {
	buf := new(bytes.Buffer)

	// Write all fields in little-endian format
	if err := binary.Write(buf, binary.LittleEndian, msg.Magic); err != nil {
		return nil, fmt.Errorf("failed to write magic: %w", err)
	}
	if err := binary.Write(buf, binary.LittleEndian, msg.Version); err != nil {
		return nil, fmt.Errorf("failed to write version: %w", err)
	}
	if err := binary.Write(buf, binary.LittleEndian, msg.Type); err != nil {
		return nil, fmt.Errorf("failed to write type: %w", err)
	}
	if err := binary.Write(buf, binary.LittleEndian, msg.NodeID); err != nil {
		return nil, fmt.Errorf("failed to write node ID: %w", err)
	}
	if err := binary.Write(buf, binary.LittleEndian, msg.Timestamp); err != nil {
		return nil, fmt.Errorf("failed to write timestamp: %w", err)
	}
	if err := binary.Write(buf, binary.LittleEndian, msg.Sequence); err != nil {
		return nil, fmt.Errorf("failed to write sequence: %w", err)
	}

	// Calculate checksum of the data so far (excluding checksum field)
	data := buf.Bytes()
	checksum := CalculateChecksum(data)

	// Write the calculated checksum
	if err := binary.Write(buf, binary.LittleEndian, checksum); err != nil {
		return nil, fmt.Errorf("failed to write checksum: %w", err)
	}

	return buf.Bytes(), nil
}

// Unmarshal deserializes a byte slice into an SBDMessageHeader using binary decoding.
// It validates the magic string and verifies the CRC32 checksum of the message.
//
// Parameters:
//   - data: Byte slice containing the serialized message
//
// Returns:
//   - *SBDMessageHeader: Pointer to the deserialized message header
//   - error: Error if unmarshaling fails or validation fails
func Unmarshal(data []byte) (*SBDMessageHeader, error) {
	if len(data) < SBD_HEADER_SIZE {
		return nil, fmt.Errorf("data too short: expected at least %d bytes, got %d", SBD_HEADER_SIZE, len(data))
	}

	buf := bytes.NewReader(data)
	msg := &SBDMessageHeader{}

	// Read all fields in little-endian format
	if err := binary.Read(buf, binary.LittleEndian, &msg.Magic); err != nil {
		return nil, fmt.Errorf("failed to read magic: %w", err)
	}
	if err := binary.Read(buf, binary.LittleEndian, &msg.Version); err != nil {
		return nil, fmt.Errorf("failed to read version: %w", err)
	}
	if err := binary.Read(buf, binary.LittleEndian, &msg.Type); err != nil {
		return nil, fmt.Errorf("failed to read type: %w", err)
	}
	if err := binary.Read(buf, binary.LittleEndian, &msg.NodeID); err != nil {
		return nil, fmt.Errorf("failed to read node ID: %w", err)
	}
	if err := binary.Read(buf, binary.LittleEndian, &msg.Timestamp); err != nil {
		return nil, fmt.Errorf("failed to read timestamp: %w", err)
	}
	if err := binary.Read(buf, binary.LittleEndian, &msg.Sequence); err != nil {
		return nil, fmt.Errorf("failed to read sequence: %w", err)
	}
	if err := binary.Read(buf, binary.LittleEndian, &msg.Checksum); err != nil {
		return nil, fmt.Errorf("failed to read checksum: %w", err)
	}

	// Validate magic string
	expectedMagic := [8]byte{}
	copy(expectedMagic[:], SBD_MAGIC)
	if msg.Magic != expectedMagic {
		return nil, fmt.Errorf("invalid magic string: expected %q, got %q", SBD_MAGIC, string(msg.Magic[:]))
	}

	// Validate checksum by recalculating it from the data (excluding checksum field)
	dataWithoutChecksum := data[:len(data)-4] // Exclude last 4 bytes (checksum)
	expectedChecksum := CalculateChecksum(dataWithoutChecksum)
	if msg.Checksum != expectedChecksum {
		return nil, fmt.Errorf("checksum mismatch: expected 0x%08x, got 0x%08x", expectedChecksum, msg.Checksum)
	}

	return msg, nil
}

// MarshalHeartbeat serializes a complete SBD heartbeat message to a byte slice.
//
// Parameters:
//   - msg: The heartbeat message to serialize
//
// Returns:
//   - []byte: Serialized heartbeat message
//   - error: Error if marshaling fails
func MarshalHeartbeat(msg SBDHeartbeatMessage) ([]byte, error) {
	return Marshal(msg.Header)
}

// MarshalFence serializes a complete SBD fence message to a byte slice.
// It includes both the header and fence-specific fields.
//
// Parameters:
//   - msg: The fence message to serialize
//
// Returns:
//   - []byte: Serialized fence message
//   - error: Error if marshaling fails
func MarshalFence(msg SBDFenceMessage) ([]byte, error) {
	// First marshal the header
	headerData, err := Marshal(msg.Header)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal header: %w", err)
	}

	// Create a buffer and write header data
	buf := new(bytes.Buffer)
	if _, err := buf.Write(headerData); err != nil {
		return nil, fmt.Errorf("failed to write header: %w", err)
	}

	// Write fence-specific fields
	if err := binary.Write(buf, binary.LittleEndian, msg.TargetNodeID); err != nil {
		return nil, fmt.Errorf("failed to write target node ID: %w", err)
	}
	if err := binary.Write(buf, binary.LittleEndian, msg.Reason); err != nil {
		return nil, fmt.Errorf("failed to write reason: %w", err)
	}

	return buf.Bytes(), nil
}

// UnmarshalHeartbeat deserializes a byte slice into an SBD heartbeat message.
//
// Parameters:
//   - data: Byte slice containing the serialized heartbeat message
//
// Returns:
//   - *SBDHeartbeatMessage: Pointer to the deserialized heartbeat message
//   - error: Error if unmarshaling fails
func UnmarshalHeartbeat(data []byte) (*SBDHeartbeatMessage, error) {
	header, err := Unmarshal(data)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal header: %w", err)
	}

	if header.Type != SBD_MSG_TYPE_HEARTBEAT {
		return nil, fmt.Errorf("invalid message type for heartbeat: expected %d, got %d", SBD_MSG_TYPE_HEARTBEAT, header.Type)
	}

	return &SBDHeartbeatMessage{Header: *header}, nil
}

// UnmarshalFence deserializes a byte slice into an SBD fence message.
//
// Parameters:
//   - data: Byte slice containing the serialized fence message
//
// Returns:
//   - *SBDFenceMessage: Pointer to the deserialized fence message
//   - error: Error if unmarshaling fails
func UnmarshalFence(data []byte) (*SBDFenceMessage, error) {
	if len(data) < SBD_HEADER_SIZE+3 { // Header + TargetNodeID (2) + Reason (1)
		return nil, fmt.Errorf("data too short for fence message: expected at least %d bytes, got %d", SBD_HEADER_SIZE+3, len(data))
	}

	// First unmarshal the header
	header, err := Unmarshal(data[:SBD_HEADER_SIZE])
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal header: %w", err)
	}

	if header.Type != SBD_MSG_TYPE_FENCE {
		return nil, fmt.Errorf("invalid message type for fence: expected %d, got %d", SBD_MSG_TYPE_FENCE, header.Type)
	}

	// Read fence-specific fields
	buf := bytes.NewReader(data[SBD_HEADER_SIZE:])
	msg := &SBDFenceMessage{Header: *header}

	if err := binary.Read(buf, binary.LittleEndian, &msg.TargetNodeID); err != nil {
		return nil, fmt.Errorf("failed to read target node ID: %w", err)
	}
	if err := binary.Read(buf, binary.LittleEndian, &msg.Reason); err != nil {
		return nil, fmt.Errorf("failed to read reason: %w", err)
	}

	return msg, nil
}

// CalculateChecksum computes the CRC32 checksum of the provided data.
// This function uses the IEEE polynomial for CRC32 calculation, which is
// the standard for most applications.
//
// Parameters:
//   - data: Byte slice for which to calculate the checksum
//
// Returns:
//   - uint32: CRC32 checksum of the input data
func CalculateChecksum(data []byte) uint32 {
	return crc32.ChecksumIEEE(data)
}

// IsValidMessageType checks if the given message type is valid.
//
// Parameters:
//   - msgType: Message type byte to validate
//
// Returns:
//   - bool: True if the message type is valid, false otherwise
func IsValidMessageType(msgType byte) bool {
	return msgType == SBD_MSG_TYPE_HEARTBEAT || msgType == SBD_MSG_TYPE_FENCE
}

// IsValidNodeID checks if the given node ID is valid.
//
// Parameters:
//   - nodeID: Node ID to validate
//
// Returns:
//   - bool: True if the node ID is valid, false otherwise
func IsValidNodeID(nodeID uint16) bool {
	return nodeID > 0 && nodeID <= SBD_MAX_NODES
}

// GetMessageTypeName returns a human-readable name for the message type.
//
// Parameters:
//   - msgType: Message type byte
//
// Returns:
//   - string: Human-readable name for the message type
func GetMessageTypeName(msgType byte) string {
	switch msgType {
	case SBD_MSG_TYPE_HEARTBEAT:
		return "HEARTBEAT"
	case SBD_MSG_TYPE_FENCE:
		return "FENCE"
	default:
		return "UNKNOWN"
	}
}

// GetFenceReasonName returns a human-readable name for the fence reason.
//
// Parameters:
//   - reason: Fence reason code
//
// Returns:
//   - string: Human-readable name for the fence reason
func GetFenceReasonName(reason uint8) string {
	switch reason {
	case FENCE_REASON_HEARTBEAT_TIMEOUT:
		return "HEARTBEAT_TIMEOUT"
	case FENCE_REASON_MANUAL:
		return "MANUAL"
	case FENCE_REASON_SPLIT_BRAIN:
		return "SPLIT_BRAIN"
	case FENCE_REASON_RESOURCE_CONFLICT:
		return "RESOURCE_CONFLICT"
	default:
		return "UNKNOWN"
	}
}
