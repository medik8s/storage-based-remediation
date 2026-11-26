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

package noderecord

import (
	"encoding/json"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

// ErrRecordMissing means the Node carries no record at all. It stays distinct
// from a record that failed to parse, because only the caller creating the
// first record may act on it.
var ErrRecordMissing = errors.New("remediation-owner annotation missing")

// ErrRecordInvalid means the annotation is present but unreadable. It never
// stands in for "absent": a record that cannot be read has to be repaired,
// never silently replaced, or a release would be granted on invented state.
var ErrRecordInvalid = errors.New("remediation-owner annotation invalid")

// ErrUnsupportedSchema means another SBR version wrote this record. It is
// reported apart from ErrRecordInvalid because the right answer is to stop
// rather than repair: rewriting it would drop fields this build cannot hold.
var ErrUnsupportedSchema = errors.New("unsupported schemaVersion")

// ErrRecordFull means the encoded record no longer fits the annotation.
// Callers surface it as a visible reason and refuse the new offer; they must
// not make room by discarding live holders or attempt history.
var ErrRecordFull = errors.New("remediation-owner annotation capacity reached")

// checkSize keeps the record inside its annotation budget. Growth is bounded
// by refusing new entries, never by evicting existing ones, since a holder or
// an attempt dropped to save space would release a restriction or grant a
// reboot that the real history forbids.
func checkSize(encoded string) error {
	if len(encoded) > MaxRecordBytes {
		return ErrRecordFull
	}
	return nil
}

// Encode renders the record for storage, including any unknown fields carried
// over from whoever wrote it last.
func Encode(rec *Record) (string, error) {
	if rec == nil {
		return "", ErrRecordInvalid
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return "", err
	}

	encoded := string(data)

	if err := checkSize(encoded); err != nil {
		return "", err
	}

	return encoded, nil
}

// Decode reads the record off the Node. An unsupported schema is returned
// unwrapped so callers can match it with errors.Is and leave the annotation
// untouched; anything else that fails to parse becomes ErrRecordInvalid.
func Decode(node *corev1.Node) (*Record, error) {
	raw, ok := node.Annotations[AnnotationKey]
	if !ok {
		return nil, ErrRecordMissing
	}
	rec := &Record{}
	if err := json.Unmarshal([]byte(raw), rec); err != nil {
		if errors.Is(err, ErrUnsupportedSchema) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", ErrRecordInvalid, err)
	}

	return rec, nil
}

// UnmarshalJSON decodes the declared fields and keeps everything else in
// Unknown. It parses the object twice, once into the alias for the typed
// fields and once into a raw map, because encoding/json gives no way to
// collect the keys it did not recognize.
//
// The version check runs before the receiver is assigned, so a record this
// build cannot represent leaves the caller's Record as it was.
func (r *Record) UnmarshalJSON(data []byte) error {
	var alias recordAlias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	if alias.SchemaVersion != SchemaVersion {
		return ErrUnsupportedSchema
	}
	var allRawFields map[string]json.RawMessage
	if err := json.Unmarshal(data, &allRawFields); err != nil {
		return err
	}

	for _, key := range knownKeys {
		delete(allRawFields, key)
	}
	*r = Record(alias)
	if len(allRawFields) > 0 {
		r.Unknown = allRawFields
	}
	return nil
}

// MarshalJSON writes the declared fields and merges the preserved unknown ones
// back in, so a field this build never understood survives the round trip.
func (r Record) MarshalJSON() ([]byte, error) {
	data, err := json.Marshal(recordAlias(r))
	if err != nil {
		return nil, err
	}
	if len(r.Unknown) == 0 {
		return data, nil
	}

	var allFields map[string]json.RawMessage
	if err := json.Unmarshal(data, &allFields); err != nil {
		return nil, err
	}
	for key, value := range r.Unknown {
		allFields[key] = value
	}
	return json.Marshal(allFields)

}
