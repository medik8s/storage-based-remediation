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

package mocks

import (
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/runtime"
)

// MockEventRecorder is a mock implementation of record.EventRecorder for testing
type MockEventRecorder struct {
	Events []Event
	mutex  sync.RWMutex
}

// Event represents a recorded event for testing
type Event struct {
	Object    runtime.Object
	EventType string
	Reason    string
	Message   string
}

// Event records an event for testing
func (m *MockEventRecorder) Event(object runtime.Object, eventtype, reason, message string) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.Events = append(m.Events, Event{
		Object:    object,
		EventType: eventtype,
		Reason:    reason,
		Message:   message,
	})
}

// Eventf records a formatted event for testing
func (m *MockEventRecorder) Eventf(object runtime.Object, eventtype, reason, messageFmt string, args ...interface{}) {
	m.Event(object, eventtype, reason, fmt.Sprintf(messageFmt, args...))
}

// AnnotatedEventf records an annotated formatted event for testing
func (m *MockEventRecorder) AnnotatedEventf(object runtime.Object, annotations map[string]string,
	eventtype, reason, messageFmt string, args ...interface{}) {
	m.Eventf(object, eventtype, reason, messageFmt, args...)
}

// GetEvents returns a copy of recorded events for testing
func (m *MockEventRecorder) GetEvents() []Event {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	events := make([]Event, len(m.Events))
	copy(events, m.Events)
	return events
}

// Reset clears all recorded events
func (m *MockEventRecorder) Reset() {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.Events = nil
}

// NewMockEventRecorder creates a new MockEventRecorder
func NewMockEventRecorder() *MockEventRecorder {
	return &MockEventRecorder{
		Events: make([]Event, 0),
	}
}
