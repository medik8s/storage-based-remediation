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

package sbdprotocol

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

func TestConstants(t *testing.T) {
	// Test that constants have expected values
	if SBD_MAGIC != "SBDMSG01" {
		t.Errorf("Expected SBD_MAGIC to be 'SBDMSG01', got %q", SBD_MAGIC)
	}

	if SBD_HEADER_SIZE != 33 {
		t.Errorf("Expected SBD_HEADER_SIZE to be 33, got %d", SBD_HEADER_SIZE)
	}

	if SBD_SLOT_SIZE != 512 {
		t.Errorf("Expected SBD_SLOT_SIZE to be 512, got %d", SBD_SLOT_SIZE)
	}

	if SBD_MAX_NODES != 255 {
		t.Errorf("Expected SBD_MAX_NODES to be 255, got %d", SBD_MAX_NODES)
	}

	if SBD_MSG_TYPE_HEARTBEAT != 0x01 {
		t.Errorf("Expected SBD_MSG_TYPE_HEARTBEAT to be 0x01, got 0x%02x", SBD_MSG_TYPE_HEARTBEAT)
	}

	if SBD_MSG_TYPE_FENCE != 0x02 {
		t.Errorf("Expected SBD_MSG_TYPE_FENCE to be 0x02, got 0x%02x", SBD_MSG_TYPE_FENCE)
	}
}

func TestNewHeartbeat(t *testing.T) {
	nodeID := uint16(42)
	sequence := uint64(123)

	before := time.Now().UnixNano()
	msg := NewHeartbeat(nodeID, sequence)
	after := time.Now().UnixNano()

	// Check magic string
	expectedMagic := [8]byte{}
	copy(expectedMagic[:], SBD_MAGIC)
	if msg.Magic != expectedMagic {
		t.Errorf("Expected magic %v, got %v", expectedMagic, msg.Magic)
	}

	// Check other fields
	if msg.Version != 1 {
		t.Errorf("Expected version 1, got %d", msg.Version)
	}

	if msg.Type != SBD_MSG_TYPE_HEARTBEAT {
		t.Errorf("Expected type %d, got %d", SBD_MSG_TYPE_HEARTBEAT, msg.Type)
	}

	if msg.NodeID != nodeID {
		t.Errorf("Expected nodeID %d, got %d", nodeID, msg.NodeID)
	}

	if msg.Sequence != sequence {
		t.Errorf("Expected sequence %d, got %d", sequence, msg.Sequence)
	}

	// Check timestamp is within reasonable range
	if msg.Timestamp < uint64(before) || msg.Timestamp > uint64(after) {
		t.Errorf("Timestamp %d not within expected range [%d, %d]", msg.Timestamp, before, after)
	}

	if msg.Checksum != 0 {
		t.Errorf("Expected checksum 0 (before marshaling), got %d", msg.Checksum)
	}
}

func TestNewFence(t *testing.T) {
	nodeID := uint16(42)
	targetNodeID := uint16(99)
	sequence := uint64(456)
	reason := uint8(FENCE_REASON_HEARTBEAT_TIMEOUT)

	before := time.Now().UnixNano()
	msg := NewFence(nodeID, targetNodeID, sequence, reason)
	after := time.Now().UnixNano()

	// Check magic string
	expectedMagic := [8]byte{}
	copy(expectedMagic[:], SBD_MAGIC)
	if msg.Magic != expectedMagic {
		t.Errorf("Expected magic %v, got %v", expectedMagic, msg.Magic)
	}

	// Check other fields
	if msg.Version != 1 {
		t.Errorf("Expected version 1, got %d", msg.Version)
	}

	if msg.Type != SBD_MSG_TYPE_FENCE {
		t.Errorf("Expected type %d, got %d", SBD_MSG_TYPE_FENCE, msg.Type)
	}

	if msg.NodeID != nodeID {
		t.Errorf("Expected nodeID %d, got %d", nodeID, msg.NodeID)
	}

	if msg.Sequence != sequence {
		t.Errorf("Expected sequence %d, got %d", sequence, msg.Sequence)
	}

	// Check timestamp is within reasonable range
	if msg.Timestamp < uint64(before) || msg.Timestamp > uint64(after) {
		t.Errorf("Timestamp %d not within expected range [%d, %d]", msg.Timestamp, before, after)
	}

	if msg.Checksum != 0 {
		t.Errorf("Expected checksum 0 (before marshaling), got %d", msg.Checksum)
	}
}

func TestCalculateChecksum(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected uint32
	}{
		{
			name:     "empty data",
			data:     []byte{},
			expected: 0x00000000,
		},
		{
			name:     "hello world",
			data:     []byte("hello world"),
			expected: 0x0d4a1185,
		},
		{
			name:     "known bytes",
			data:     []byte{0x01, 0x02, 0x03, 0x04},
			expected: 0xb63cfbcd,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CalculateChecksum(tt.data)
			if result != tt.expected {
				t.Errorf("Expected checksum 0x%08x, got 0x%08x", tt.expected, result)
			}
		})
	}
}

func TestMarshalUnmarshal(t *testing.T) {
	tests := []struct {
		name     string
		original SBDMessageHeader
	}{
		{
			name:     "heartbeat message",
			original: NewHeartbeat(1, 100),
		},
		{
			name:     "fence message",
			original: NewFence(2, 3, 200, FENCE_REASON_MANUAL),
		},
		{
			name: "message with max values",
			original: SBDMessageHeader{
				Magic:     [8]byte{'S', 'B', 'D', 'M', 'S', 'G', '0', '1'},
				Version:   65535,
				Type:      255,
				NodeID:    65535,
				Timestamp: ^uint64(0), // Max uint64
				Sequence:  ^uint64(0), // Max uint64
				Checksum:  0,          // Will be calculated
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Marshal the message
			data, err := Marshal(tt.original)
			if err != nil {
				t.Fatalf("Marshal failed: %v", err)
			}

			// Check that data has expected size
			if len(data) != SBD_HEADER_SIZE {
				t.Errorf("Expected marshaled data size %d, got %d", SBD_HEADER_SIZE, len(data))
			}

			// Unmarshal the data
			unmarshaled, err := Unmarshal(data)
			if err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}

			// Compare all fields except checksum (which is calculated during marshal)
			if unmarshaled.Magic != tt.original.Magic {
				t.Errorf("Magic mismatch: expected %v, got %v", tt.original.Magic, unmarshaled.Magic)
			}
			if unmarshaled.Version != tt.original.Version {
				t.Errorf("Version mismatch: expected %d, got %d", tt.original.Version, unmarshaled.Version)
			}
			if unmarshaled.Type != tt.original.Type {
				t.Errorf("Type mismatch: expected %d, got %d", tt.original.Type, unmarshaled.Type)
			}
			if unmarshaled.NodeID != tt.original.NodeID {
				t.Errorf("NodeID mismatch: expected %d, got %d", tt.original.NodeID, unmarshaled.NodeID)
			}
			if unmarshaled.Timestamp != tt.original.Timestamp {
				t.Errorf("Timestamp mismatch: expected %d, got %d", tt.original.Timestamp, unmarshaled.Timestamp)
			}
			if unmarshaled.Sequence != tt.original.Sequence {
				t.Errorf("Sequence mismatch: expected %d, got %d", tt.original.Sequence, unmarshaled.Sequence)
			}
			// Checksum should be non-zero after marshaling
			if unmarshaled.Checksum == 0 {
				t.Error("Expected non-zero checksum after marshaling")
			}
		})
	}
}

func TestUnmarshalErrors(t *testing.T) {
	tests := []struct {
		name          string
		data          []byte
		expectedError string
	}{
		{
			name:          "too short data",
			data:          []byte{1, 2, 3},
			expectedError: "data too short",
		},
		{
			name:          "invalid magic",
			data:          bytes.Repeat([]byte{0}, SBD_HEADER_SIZE),
			expectedError: "invalid magic string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Unmarshal(tt.data)
			if err == nil {
				t.Error("Expected error but got none")
			}
			if err != nil && tt.expectedError != "" {
				if !bytes.Contains([]byte(err.Error()), []byte(tt.expectedError)) {
					t.Errorf("Expected error containing %q, got %q", tt.expectedError, err.Error())
				}
			}
		})
	}
}

func TestChecksumValidation(t *testing.T) {
	// Create a valid message
	msg := NewHeartbeat(1, 100)
	data, err := Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	// Corrupt the checksum
	corruptedData := make([]byte, len(data))
	copy(corruptedData, data)
	corruptedData[len(corruptedData)-1] ^= 0xFF // Flip last byte of checksum

	// Try to unmarshal corrupted data
	_, err = Unmarshal(corruptedData)
	if err == nil {
		t.Error("Expected checksum validation error but got none")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("checksum mismatch")) {
		t.Errorf("Expected checksum mismatch error, got %q", err.Error())
	}
}

func TestMarshalHeartbeat(t *testing.T) {
	heartbeat := SBDHeartbeatMessage{
		Header: NewHeartbeat(1, 100),
	}

	data, err := MarshalHeartbeat(heartbeat)
	if err != nil {
		t.Fatalf("MarshalHeartbeat failed: %v", err)
	}

	if len(data) != SBD_HEADER_SIZE {
		t.Errorf("Expected data size %d, got %d", SBD_HEADER_SIZE, len(data))
	}

	// Verify we can unmarshal it as a heartbeat
	result, err := UnmarshalHeartbeat(data)
	if err != nil {
		t.Fatalf("UnmarshalHeartbeat failed: %v", err)
	}

	if result.Header.Type != SBD_MSG_TYPE_HEARTBEAT {
		t.Errorf("Expected heartbeat type, got %d", result.Header.Type)
	}
}

func TestMarshalFence(t *testing.T) {
	fence := SBDFenceMessage{
		Header:       NewFence(1, 2, 100, FENCE_REASON_MANUAL),
		TargetNodeID: 2,
		Reason:       FENCE_REASON_MANUAL,
	}

	data, err := MarshalFence(fence)
	if err != nil {
		t.Fatalf("MarshalFence failed: %v", err)
	}

	expectedSize := SBD_HEADER_SIZE + 3 // Header + TargetNodeID (2) + Reason (1)
	if len(data) != expectedSize {
		t.Errorf("Expected data size %d, got %d", expectedSize, len(data))
	}

	// Verify we can unmarshal it as a fence message
	result, err := UnmarshalFence(data)
	if err != nil {
		t.Fatalf("UnmarshalFence failed: %v", err)
	}

	if result.Header.Type != SBD_MSG_TYPE_FENCE {
		t.Errorf("Expected fence type, got %d", result.Header.Type)
	}

	if result.TargetNodeID != fence.TargetNodeID {
		t.Errorf("Expected target node ID %d, got %d", fence.TargetNodeID, result.TargetNodeID)
	}

	if result.Reason != fence.Reason {
		t.Errorf("Expected reason %d, got %d", fence.Reason, result.Reason)
	}
}

func TestUnmarshalHeartbeatErrors(t *testing.T) {
	// Create a fence message and try to unmarshal as heartbeat
	fence := NewFence(1, 2, 100, FENCE_REASON_MANUAL)
	data, err := Marshal(fence)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	_, err = UnmarshalHeartbeat(data)
	if err == nil {
		t.Error("Expected error when unmarshaling fence as heartbeat")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("invalid message type")) {
		t.Errorf("Expected message type error, got %q", err.Error())
	}
}

func TestUnmarshalFenceErrors(t *testing.T) {
	tests := []struct {
		name          string
		setup         func() []byte
		expectedError string
	}{
		{
			name: "too short data",
			setup: func() []byte {
				return []byte{1, 2, 3}
			},
			expectedError: "data too short",
		},
		{
			name: "wrong message type",
			setup: func() []byte {
				// Create a fence message with correct length, then change the type to heartbeat
				fence := SBDFenceMessage{
					Header:       NewFence(1, 2, 100, FENCE_REASON_MANUAL),
					TargetNodeID: 2,
					Reason:       FENCE_REASON_MANUAL,
				}
				// Change the type to heartbeat to trigger type validation error
				fence.Header.Type = SBD_MSG_TYPE_HEARTBEAT
				data, _ := MarshalFence(fence)
				return data
			},
			expectedError: "invalid message type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := tt.setup()
			_, err := UnmarshalFence(data)
			if err == nil {
				t.Error("Expected error but got none")
			}
			if !bytes.Contains([]byte(err.Error()), []byte(tt.expectedError)) {
				t.Errorf("Expected error containing %q, got %q", tt.expectedError, err.Error())
			}
		})
	}
}

func TestIsValidMessageType(t *testing.T) {
	tests := []struct {
		msgType  byte
		expected bool
	}{
		{SBD_MSG_TYPE_HEARTBEAT, true},
		{SBD_MSG_TYPE_FENCE, true},
		{0x00, false},
		{0x03, false},
		{0xFF, false},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			result := IsValidMessageType(tt.msgType)
			if result != tt.expected {
				t.Errorf("IsValidMessageType(0x%02x) = %v, expected %v", tt.msgType, result, tt.expected)
			}
		})
	}
}

func TestIsValidNodeID(t *testing.T) {
	tests := []struct {
		nodeID   uint16
		expected bool
	}{
		{0, false},     // Invalid: zero
		{1, true},      // Valid: minimum
		{255, true},    // Valid: maximum
		{256, false},   // Invalid: too large
		{65535, false}, // Invalid: way too large
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			result := IsValidNodeID(tt.nodeID)
			if result != tt.expected {
				t.Errorf("IsValidNodeID(%d) = %v, expected %v", tt.nodeID, result, tt.expected)
			}
		})
	}
}

func TestGetMessageTypeName(t *testing.T) {
	tests := []struct {
		msgType  byte
		expected string
	}{
		{SBD_MSG_TYPE_HEARTBEAT, "HEARTBEAT"},
		{SBD_MSG_TYPE_FENCE, "FENCE"},
		{0x00, "UNKNOWN"},
		{0xFF, "UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			result := GetMessageTypeName(tt.msgType)
			if result != tt.expected {
				t.Errorf("GetMessageTypeName(0x%02x) = %q, expected %q", tt.msgType, result, tt.expected)
			}
		})
	}
}

func TestGetFenceReasonName(t *testing.T) {
	tests := []struct {
		reason   uint8
		expected string
	}{
		{FENCE_REASON_HEARTBEAT_TIMEOUT, "HEARTBEAT_TIMEOUT"},
		{FENCE_REASON_MANUAL, "MANUAL"},
		{FENCE_REASON_SPLIT_BRAIN, "SPLIT_BRAIN"},
		{FENCE_REASON_RESOURCE_CONFLICT, "RESOURCE_CONFLICT"},
		{0x00, "UNKNOWN"},
		{0xFF, "UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			result := GetFenceReasonName(tt.reason)
			if result != tt.expected {
				t.Errorf("GetFenceReasonName(%d) = %q, expected %q", tt.reason, result, tt.expected)
			}
		})
	}
}

func TestRoundTripConsistency(t *testing.T) {
	// Test that marshaling and unmarshaling is consistent
	original := NewHeartbeat(42, 12345)
	original.Timestamp = 1640995200000000000 // Fixed timestamp for consistency

	// First round trip
	data1, err := Marshal(original)
	if err != nil {
		t.Fatalf("First marshal failed: %v", err)
	}

	unmarshaled1, err := Unmarshal(data1)
	if err != nil {
		t.Fatalf("First unmarshal failed: %v", err)
	}

	// Second round trip
	data2, err := Marshal(*unmarshaled1)
	if err != nil {
		t.Fatalf("Second marshal failed: %v", err)
	}

	unmarshaled2, err := Unmarshal(data2)
	if err != nil {
		t.Fatalf("Second unmarshal failed: %v", err)
	}

	// Data should be identical
	if !bytes.Equal(data1, data2) {
		t.Error("Marshaled data differs between round trips")
	}

	// Unmarshaled objects should be identical
	if *unmarshaled1 != *unmarshaled2 {
		t.Error("Unmarshaled objects differ between round trips")
	}
}

func TestBinaryCompatibility(t *testing.T) {
	// Test that our binary format is consistent
	msg := SBDMessageHeader{
		Magic:     [8]byte{'S', 'B', 'D', 'M', 'S', 'G', '0', '1'},
		Version:   1,
		Type:      SBD_MSG_TYPE_HEARTBEAT,
		NodeID:    42,
		Timestamp: 1640995200000000000,
		Sequence:  12345,
		Checksum:  0, // Will be calculated
	}

	data, err := Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	// Verify specific byte patterns (little-endian)
	if !bytes.Equal(data[0:8], []byte("SBDMSG01")) {
		t.Error("Magic bytes mismatch")
	}

	if data[8] != 1 || data[9] != 0 { // Version = 1 (little-endian)
		t.Errorf("Version bytes mismatch: got [%d, %d]", data[8], data[9])
	}

	if data[10] != SBD_MSG_TYPE_HEARTBEAT {
		t.Errorf("Type byte mismatch: got %d", data[10])
	}

	// NodeID = 42 (little-endian)
	if data[11] != 42 || data[12] != 0 {
		t.Errorf("NodeID bytes mismatch: got [%d, %d]", data[11], data[12])
	}
}

func BenchmarkMarshal(b *testing.B) {
	msg := NewHeartbeat(1, 100)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := Marshal(msg)
		if err != nil {
			b.Fatalf("Marshal failed: %v", err)
		}
	}
}

func BenchmarkUnmarshal(b *testing.B) {
	msg := NewHeartbeat(1, 100)
	data, err := Marshal(msg)
	if err != nil {
		b.Fatalf("Setup marshal failed: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := Unmarshal(data)
		if err != nil {
			b.Fatalf("Unmarshal failed: %v", err)
		}
	}
}

func BenchmarkCalculateChecksum(b *testing.B) {
	data := bytes.Repeat([]byte{0x42}, 1024)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = CalculateChecksum(data)
	}
}

func BenchmarkHeartbeatRoundTrip(b *testing.B) {
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		msg := NewHeartbeat(1, uint64(i))
		data, err := Marshal(msg)
		if err != nil {
			b.Fatalf("Marshal failed: %v", err)
		}

		_, err = Unmarshal(data)
		if err != nil {
			b.Fatalf("Unmarshal failed: %v", err)
		}
	}
}

func TestSBDMarshalUnmarshalRoundtrip(t *testing.T) {
	// Test heartbeat message roundtrip
	t.Run("HeartbeatMessage", func(t *testing.T) {
		originalMsg := SBDHeartbeatMessage{
			Header: NewHeartbeat(42, 123),
		}

		// Marshal
		data, err := MarshalHeartbeat(originalMsg)
		if err != nil {
			t.Fatalf("MarshalHeartbeat failed: %v", err)
		}

		// Unmarshal
		unmarshaledMsg, err := UnmarshalHeartbeat(data)
		if err != nil {
			t.Fatalf("UnmarshalHeartbeat failed: %v", err)
		}

		// Compare
		if originalMsg.Header.NodeID != unmarshaledMsg.Header.NodeID {
			t.Errorf("NodeID mismatch: expected %d, got %d", originalMsg.Header.NodeID, unmarshaledMsg.Header.NodeID)
		}
		if originalMsg.Header.Sequence != unmarshaledMsg.Header.Sequence {
			t.Errorf("Sequence mismatch: expected %d, got %d", originalMsg.Header.Sequence, unmarshaledMsg.Header.Sequence)
		}
	})

	// Test fence message roundtrip
	t.Run("FenceMessage", func(t *testing.T) {
		originalMsg := SBDFenceMessage{
			Header:       NewFence(1, 42, 456, FENCE_REASON_HEARTBEAT_TIMEOUT),
			TargetNodeID: 42,
			Reason:       FENCE_REASON_HEARTBEAT_TIMEOUT,
		}

		// Marshal
		data, err := MarshalFence(originalMsg)
		if err != nil {
			t.Fatalf("MarshalFence failed: %v", err)
		}

		// Unmarshal
		unmarshaledMsg, err := UnmarshalFence(data)
		if err != nil {
			t.Fatalf("UnmarshalFence failed: %v", err)
		}

		// Compare
		if originalMsg.Header.NodeID != unmarshaledMsg.Header.NodeID {
			t.Errorf("NodeID mismatch: expected %d, got %d", originalMsg.Header.NodeID, unmarshaledMsg.Header.NodeID)
		}
		if originalMsg.TargetNodeID != unmarshaledMsg.TargetNodeID {
			t.Errorf("TargetNodeID mismatch: expected %d, got %d", originalMsg.TargetNodeID, unmarshaledMsg.TargetNodeID)
		}
		if originalMsg.Reason != unmarshaledMsg.Reason {
			t.Errorf("Reason mismatch: expected %d, got %d", originalMsg.Reason, unmarshaledMsg.Reason)
		}
	})
}

func TestSBDCorruptedData(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{
			name: "empty data",
			data: []byte{},
		},
		{
			name: "truncated data",
			data: []byte{0x01, 0x02},
		},
		{
			name: "corrupted header",
			data: []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF},
		},
		{
			name: "invalid magic",
			data: append([]byte("BADMAGIC"), make([]byte, 25)...),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Unmarshal(tt.data)
			if err == nil {
				t.Error("Expected error when unmarshaling corrupted data, but got none")
			}
		})
	}
}

func TestSBDMessageValidation(t *testing.T) {
	t.Run("ValidMessageTypes", func(t *testing.T) {
		validTypes := []byte{SBD_MSG_TYPE_HEARTBEAT, SBD_MSG_TYPE_FENCE}
		for _, msgType := range validTypes {
			if !IsValidMessageType(msgType) {
				t.Errorf("Expected message type %d to be valid", msgType)
			}
		}
	})

	t.Run("InvalidMessageTypes", func(t *testing.T) {
		invalidTypes := []byte{0x00, 0x03, 0xFF}
		for _, msgType := range invalidTypes {
			if IsValidMessageType(msgType) {
				t.Errorf("Expected message type %d to be invalid", msgType)
			}
		}
	})
}

func TestSBDGetMessageTypeName(t *testing.T) {
	tests := []struct {
		msgType      byte
		expectedName string
	}{
		{SBD_MSG_TYPE_HEARTBEAT, "HEARTBEAT"},
		{SBD_MSG_TYPE_FENCE, "FENCE"},
		{0x99, "UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("MessageType_%d", tt.msgType), func(t *testing.T) {
			name := GetMessageTypeName(tt.msgType)
			if name != tt.expectedName {
				t.Errorf("Expected message type name %s, got %s", tt.expectedName, name)
			}
		})
	}
}

func TestSBDGetFenceReasonName(t *testing.T) {
	tests := []struct {
		reason       uint8
		expectedName string
	}{
		{FENCE_REASON_HEARTBEAT_TIMEOUT, "HEARTBEAT_TIMEOUT"},
		{FENCE_REASON_MANUAL, "MANUAL"},
		{FENCE_REASON_SPLIT_BRAIN, "SPLIT_BRAIN"},
		{FENCE_REASON_RESOURCE_CONFLICT, "RESOURCE_CONFLICT"},
		{99, "UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("FenceReason_%d", tt.reason), func(t *testing.T) {
			name := GetFenceReasonName(tt.reason)
			if name != tt.expectedName {
				t.Errorf("Expected fence reason name %s, got %s", tt.expectedName, name)
			}
		})
	}
}

func TestSBDChecksumValidation(t *testing.T) {
	// Create a test message
	msg := NewHeartbeat(1, 100)
	data, err := Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	// Verify checksum validation during unmarshal
	unmarshaledMsg, err := Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if unmarshaledMsg.NodeID != msg.NodeID {
		t.Errorf("NodeID mismatch after checksum validation: expected %d, got %d", msg.NodeID, unmarshaledMsg.NodeID)
	}

	// Test corrupted checksum
	corruptedData := make([]byte, len(data))
	copy(corruptedData, data)
	// Modify the last 4 bytes (checksum)
	corruptedData[len(corruptedData)-1] ^= 0xFF

	_, err = Unmarshal(corruptedData)
	if err == nil {
		t.Error("Expected error when unmarshaling data with corrupted checksum, but got none")
	}
}
