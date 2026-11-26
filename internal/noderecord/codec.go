package noderecord

import (
	"encoding/json"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

var ErrRecordMissing = errors.New("remediation-owner annotation missing")
var ErrRecordInvalid = errors.New("remediation-owner annotation invalid")
var ErrUnsupportedSchema = errors.New("unsupported schemaVersion")
var ErrRecordFull = errors.New("remediation-owner annotation capacity reached")

func checkSize(encoded string) error {
	if len(encoded) > MaxRecordBytes {
		return ErrRecordFull
	}
	return nil
}
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
