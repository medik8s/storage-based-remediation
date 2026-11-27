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

package budget

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrUnknownSchema reports a persisted ledger written by a different
// format version. The caller must treat it as unknown history, never
// overwrite it with a fresh state, and surface ReasonHistoryUnknown.
var ErrUnknownSchema = errors.New("reset-budget ledger has an unsupported schema version")

// Encode serializes the state for the Agent.Extra map entry.
func Encode(s State) (string, error) {
	s.SchemaVersion = SchemaVersion
	data, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("encode reset-budget state: %w", err)
	}
	return string(data), nil
}

// Decode parses a persisted Agent.Extra entry. A version other than the
// current one is ErrUnknownSchema; malformed JSON is an error. Both leave
// the decision to the caller: unknown history permits an attempt and is
// reported as HistoryUnknown, and the persisted value is never overwritten
// on a failed read.
func Decode(raw string) (*State, error) {
	if raw == "" {
		return nil, fmt.Errorf("empty reset-budget entry")
	}
	var s State
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil, fmt.Errorf("decode reset-budget state: %w", err)
	}
	if s.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("%w: persisted %d, supported %d",
			ErrUnknownSchema, s.SchemaVersion, SchemaVersion)
	}
	s.HistoryKnown = true
	return &s, nil
}
