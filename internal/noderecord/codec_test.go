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
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	medik8sv1alpha1 "github.com/medik8s/storage-based-remediation/v5/api/v1alpha1"
)

func nodeWithAnnotation(t *testing.T, raw string) *corev1.Node {
	t.Helper()
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}
	if raw != "" {
		n.Annotations = map[string]string{AnnotationKey: raw}
	}
	return n
}

func sampleRecord() *Record {
	offeredAt := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	deadline := time.Date(2026, 11, 20, 10, 5, 0, 0, time.UTC)
	cordon := true
	oosTaint := false

	return &Record{
		SchemaVersion: SchemaVersion,
		NodeUID:       types.UID("node-uid-1"),
		Execution: &Execution{
			CR: CRRef{
				Namespace: "openshift-workload-availability",
				Name:      "node-1",
				UID:       types.UID("cr-uid-1"),
			},
			Config: ConfigRef{
				UID:        types.UID("config-uid-1"),
				VolumeMode: medik8sv1alpha1.SharedStorageVolumeModeBlock,
			},
			InitialBootID: "boot-id-1",
			Phase:         PhaseAccepted,
		},
		PreviousExecutions: []Execution{
			{
				CR:            CRRef{Namespace: "ns", Name: "old", UID: types.UID("cr-uid-0")},
				Config:        ConfigRef{UID: types.UID("config-uid-0"), VolumeMode: medik8sv1alpha1.SharedStorageVolumeModeFilesystem},
				InitialBootID: "boot-id-0",
				Phase:         PhaseSuperseded,
			},
		},
		Policy: &Policy{
			RequestUID:      types.UID("request-uid-1"),
			TaintPolicy:     PolicyOnTimeout,
			OfferedAt:       offeredAt,
			RebootDeadline:  deadline,
			TaintConfirmed:  false,
			CanStartOrRetry: true,
		},
		Budget: Budget{
			HistoryKnown: true,
			OutageIDs:    []string{"outage-1", "outage-2"},
		},
		Restrictions: Restrictions{
			OriginalCordon:   &cordon,
			OriginalOOSTaint: &oosTaint,
			RequiredByCRs:    []types.UID{types.UID("cr-uid-1")},
		},
		CoordinationRevision: 7,
	}
}

func TestDecode_MissingAnnotation(t *testing.T) {
	node := nodeWithAnnotation(t, "")

	_, err := Decode(node)
	if !errors.Is(err, ErrRecordMissing) {
		t.Fatalf("expected ErrRecordMissing, got %v", err)
	}
}

func TestDecode_InvalidJSON(t *testing.T) {
	node := nodeWithAnnotation(t, "{not valid json")

	_, err := Decode(node)
	if !errors.Is(err, ErrRecordInvalid) {
		t.Fatalf("expected ErrRecordInvalid, got %v", err)
	}
}

func TestDecode_InvalidJSON_IsNotMissing(t *testing.T) {
	node := nodeWithAnnotation(t, "{not valid json")

	_, err := Decode(node)
	if errors.Is(err, ErrRecordMissing) {
		t.Fatalf("invalid annotation must not be reported as missing: %v", err)
	}
}

func TestDecode_UnsupportedSchemaVersion(t *testing.T) {
	raw := `{"schemaVersion":999,"nodeUID":"node-uid-1","budget":{},"restrictions":{},"coordinationRevision":0}`
	node := nodeWithAnnotation(t, raw)

	_, err := Decode(node)
	if !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("expected ErrUnsupportedSchema, got %v", err)
	}
}

func TestEncodeDecode_RoundTrip(t *testing.T) {
	want := sampleRecord()

	raw, err := Encode(want)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	node := nodeWithAnnotation(t, raw)

	got, err := Decode(node)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round-tripped record does not match original\nwant: %+v\ngot:  %+v", want, got)
	}
}

func TestEncode_WritesCurrentSchemaVersion(t *testing.T) {
	rec := sampleRecord()
	rec.SchemaVersion = SchemaVersion

	raw, err := Encode(rec)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("Encode did not produce valid JSON: %v", err)
	}

	sv, ok := decoded["schemaVersion"]
	if !ok {
		t.Fatal("encoded record is missing schemaVersion")
	}
	if int(sv.(float64)) != SchemaVersion {
		t.Fatalf("expected schemaVersion %d, got %v", SchemaVersion, sv)
	}
}

func TestRecord_UnmarshalMarshal_PreservesUnknownFields(t *testing.T) {
	input := `{
		"schemaVersion": 1,
		"nodeUID": "node-uid-1",
		"budget": {"historyKnown": false},
		"restrictions": {},
		"coordinationRevision": 3,
		"futureTopLevelField": "some-value",
		"futureNestedField": {"a": 1, "b": [1, 2, 3]}
	}`

	var rec Record
	if err := json.Unmarshal([]byte(input), &rec); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}

	if rec.CoordinationRevision != 3 {
		t.Fatalf("expected coordinationRevision 3, got %d", rec.CoordinationRevision)
	}

	out, err := json.Marshal(&rec)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	var roundTripped map[string]any
	if err := json.Unmarshal(out, &roundTripped); err != nil {
		t.Fatalf("re-unmarshal of marshaled output: %v", err)
	}

	if _, ok := roundTripped["futureTopLevelField"]; !ok {
		t.Fatal("unknown top-level field was dropped on round-trip")
	}
	if got := roundTripped["futureTopLevelField"]; got != "some-value" {
		t.Fatalf("unknown top-level field value changed: got %v", got)
	}

	nested, ok := roundTripped["futureNestedField"].(map[string]any)
	if !ok {
		t.Fatal("unknown nested field was dropped or changed type on round-trip")
	}
	if nested["a"] != float64(1) {
		t.Fatalf("unknown nested field contents changed: %v", nested)
	}
}

func TestRecord_UnmarshalMarshal_NoUnknownFieldsRoundTrips(t *testing.T) {
	want := sampleRecord()

	out, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	var got Record
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}

	if !reflect.DeepEqual(*want, got) {
		t.Fatalf("record without unknown fields does not round-trip\nwant: %+v\ngot:  %+v", *want, got)
	}

	if len(got.Unknown) != 0 {
		t.Fatalf("expected no unknown fields, got %v", got.Unknown)
	}
}

func TestRecord_UnmarshalJSON_UnsupportedSchemaVersionNotMutated(t *testing.T) {
	input := `{"schemaVersion":999,"nodeUID":"node-uid-1","budget":{},"restrictions":{},"coordinationRevision":5}`

	rec := Record{CoordinationRevision: -1}
	err := json.Unmarshal([]byte(input), &rec)
	if !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("expected ErrUnsupportedSchema, got %v", err)
	}

	if rec.CoordinationRevision != -1 {
		t.Fatalf("unsupported schema must not mutate the record in place, got coordinationRevision=%d", rec.CoordinationRevision)
	}
}

// knownKeys drives the unknown-field split, so drift between it and Record is
// not cosmetic. A declared field missing from knownKeys is decoded into its
// own field and copied into Unknown, and MarshalJSON then overlays the stale
// copy back on top — silently discarding whatever a mutate function changed.
func TestKnownKeysMatchesRecordFields(t *testing.T) {
	declared := map[string]bool{}
	recordType := reflect.TypeOf(Record{})
	for i := range recordType.NumField() {
		name, _, _ := strings.Cut(recordType.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		declared[name] = true
	}

	known := map[string]bool{}
	for _, key := range knownKeys {
		known[key] = true
	}

	for name := range declared {
		if !known[name] {
			t.Errorf("Record declares %q but knownKeys omits it; a mutation of that field would be silently discarded", name)
		}
	}
	for name := range known {
		if !declared[name] {
			t.Errorf("knownKeys lists %q, which Record no longer declares", name)
		}
	}
}

func TestCheckSize_UnderLimit(t *testing.T) {
	encoded := strings.Repeat("a", MaxRecordBytes-1)

	if err := checkSize(encoded); err != nil {
		t.Fatalf("expected no error under the limit, got %v", err)
	}
}

func TestCheckSize_AtLimit(t *testing.T) {
	encoded := strings.Repeat("a", MaxRecordBytes)

	if err := checkSize(encoded); err != nil {
		t.Fatalf("expected no error exactly at the limit, got %v", err)
	}
}

func TestCheckSize_OverLimit(t *testing.T) {
	encoded := strings.Repeat("a", MaxRecordBytes+1)

	err := checkSize(encoded)
	if !errors.Is(err, ErrRecordFull) {
		t.Fatalf("expected ErrRecordFull, got %v", err)
	}
}

func TestCheckSize_Empty(t *testing.T) {
	if err := checkSize(""); err != nil {
		t.Fatalf("expected no error for an empty string, got %v", err)
	}
}
