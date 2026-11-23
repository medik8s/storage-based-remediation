package nodestate

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// This file implements the one thing Go's encoding/json will not do for you:
// preserving object members the running build does not know about.
//
// Why it matters. Two operators on different release cadences read and write
// the same on-disk format. During a staggered upgrade, the newer one adds a
// field; if the older one then performs a read-modify-write, a plain
// round-trip through a Go struct silently deletes it. The agent that wrote the
// field has no way to detect the loss, and the data is gone from the only copy
// that existed.
//
// The fix is mechanical: decode twice, once into the struct and once into a
// map, and carry forward every member the struct did not claim. Re-emit them
// on encode, never overriding a typed field.
//
// The cost is a second decode and encode per document. At the sizes involved
// — a few kilobytes, a few times a minute — that is not worth optimising.

// knownJSONKeys returns the lower-cased set of JSON object members that t's
// fields consume. Lower-cased because encoding/json matches member names
// case-insensitively when an exact match is not found, so "Node" is consumed
// by the Node field and must not also be recorded as unknown.
//
// Results are cached: reflection runs once per type per process.
var knownKeyCache sync.Map // reflect.Type -> map[string]struct{}

func knownJSONKeys(t reflect.Type) map[string]struct{} {
	if cached, ok := knownKeyCache.Load(t); ok {
		return cached.(map[string]struct{})
	}
	keys := make(map[string]struct{}, t.NumField())
	collectJSONKeys(t, keys)
	knownKeyCache.Store(t, keys)
	return keys
}

func collectJSONKeys(t reflect.Type, into map[string]struct{}) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" && !f.Anonymous {
			continue // unexported
		}
		tag := f.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "-" && !strings.Contains(tag, ",") {
			continue
		}
		if name == "" {
			if f.Anonymous && f.Type.Kind() == reflect.Struct {
				collectJSONKeys(f.Type, into) // embedded, promoted members
				continue
			}
			name = f.Name
		}
		into[strings.ToLower(name)] = struct{}{}
	}
}

// decodeObject unmarshals data into alias, which must be a pointer to a struct
// whose JSON tags mirror the public type, and returns every top-level member
// of data that alias did not consume.
//
// A JSON null decodes to a zero value with no unknown members, matching the
// behaviour of encoding/json for a pointer-free struct.
func decodeObject(data []byte, alias interface{}, aliasType reflect.Type) (map[string]json.RawMessage, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return nil, err
	}
	if members == nil { // literal null
		return nil, nil
	}
	if err := json.Unmarshal(data, alias); err != nil {
		return nil, err
	}

	known := knownJSONKeys(aliasType)
	var unknown map[string]json.RawMessage
	for name, raw := range members {
		if _, ok := known[strings.ToLower(name)]; ok {
			continue
		}
		if unknown == nil {
			unknown = make(map[string]json.RawMessage, 4)
		}
		// Copy: the decoder's slices alias the input buffer, which the caller
		// is free to reuse.
		unknown[name] = append(json.RawMessage(nil), raw...)
	}
	return unknown, nil
}

// encodeObject marshals alias and merges unknown members back in. A member
// whose name collides with a typed field is dropped: the typed field is the
// authority, and silently letting a stale carried-over value win would be a
// correctness bug that only shows up after an upgrade.
//
// Member order is struct order when there is nothing to merge, and sorted by
// key when there is, because encoding/json sorts map keys. Either way it is
// deterministic for a given document, so an unchanged document encodes to the
// same bytes twice.
func encodeObject(alias interface{}, unknown map[string]json.RawMessage, aliasType reflect.Type) ([]byte, error) {
	encoded, err := json.Marshal(alias)
	if err != nil {
		return nil, err
	}
	if len(unknown) == 0 {
		return encoded, nil
	}

	var members map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &members); err != nil {
		return nil, fmt.Errorf("re-reading the encoded %s: %w", aliasType.Name(), err)
	}
	known := knownJSONKeys(aliasType)
	for name, raw := range unknown {
		if _, ok := known[strings.ToLower(name)]; ok {
			continue
		}
		if _, ok := members[name]; ok {
			continue
		}
		if !json.Valid(raw) {
			// Refuse to emit a document we could not read back.
			return nil, fmt.Errorf("%w: preserved member %q is not valid JSON", ErrCorrupt, name)
		}
		members[name] = raw
	}
	return json.Marshal(members)
}

// cloneRawMap deep-copies a preserved-member map, including the byte slices.
func cloneRawMap(in map[string]json.RawMessage) map[string]json.RawMessage {
	if in == nil {
		return nil
	}
	out := make(map[string]json.RawMessage, len(in))
	for k, v := range in {
		out[k] = append(json.RawMessage(nil), v...)
	}
	return out
}
