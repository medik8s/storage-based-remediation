/*
Copyright 2026.

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

package bootunit

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// IncidentIDBytes is the size of a generated incident identifier: 128 bits,
// enough that two incidents on one host cannot collide and that an identifier
// cannot be guessed by anything reading the announced condition.
const IncidentIDBytes = 16

// NewIncidentID returns an unpredictable identifier for one remediation
// incident.
//
// The ID is what makes a boot unit's announcement and the agent's own state
// describing the same event: the boot unit copies it into the
// SBRRejoinPending condition, and the release protocol matches on it. It must
// therefore be random rather than derived from the node, the time or the
// reason — a derived value lets a stale announcement be mistaken for a fresh
// one, which is the confusion the identifier exists to prevent.
func NewIncidentID() (string, error) {
	var b [IncidentIDBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate incident ID: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
