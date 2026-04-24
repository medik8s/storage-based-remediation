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

import "time"

// Package-level constants for hardcoded configuration values.
// These values are no longer configurable via the StorageBasedRemediationConfig CR.

const (
	// PetIntervalMultiple defines the hardcoded multiple used to calculate the pet interval
	// from the watchdog timeout: pet interval = watchdog timeout / pet interval multiple.
	PetIntervalMultiple = 4

	// WatchdogTimeoutDefault is the default timeout when watchdog timeout discovery fails
	WatchdogTimeoutDefault = 60 * time.Second
)
