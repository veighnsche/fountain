// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package nativeoperation

import (
	"bytes"
	"encoding/json" //nolint:depguard // Preserve original reservation witness bytes.
	"errors"
	"io"
)

type jsonSpan struct{ start, end int }

type scopeObject struct {
	raw    string
	fields map[string]jsonSpan
	starts map[string]int
	keys   []string
	close  int
}

func parseScopeObject(raw string) (*scopeObject, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("reservation scope is not an object")
	}
	object := &scopeObject{raw: raw, fields: make(map[string]jsonSpan), starts: make(map[string]int)}
	for decoder.More() {
		start := int(decoder.InputOffset())
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("invalid reservation scope key")
		}
		if _, duplicate := object.fields[key]; duplicate {
			return nil, errors.New("ambiguous reservation scope key")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		end := int(decoder.InputOffset())
		object.fields[key] = jsonSpan{end - len(value), end}
		object.starts[key] = start
		object.keys = append(object.keys, key)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	object.close = int(decoder.InputOffset()) - 1
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("reservation scope must contain one object")
	}
	return object, nil
}

func (object *scopeObject) value(field string) (string, bool) {
	span, ok := object.fields[field]
	if !ok {
		return "", false
	}
	return object.raw[span.start:span.end], true
}

func (object *scopeObject) withValue(field, value string) string {
	if span, ok := object.fields[field]; ok {
		return object.raw[:span.start] + value + object.raw[span.end:]
	}
	key, _ := json.Marshal(field)
	prefix := ""
	if len(object.fields) != 0 {
		prefix = ","
	}
	return object.raw[:object.close] + prefix + string(key) + ":" + value + object.raw[object.close:]
}

func (object *scopeObject) without(field string) string {
	span, found := object.fields[field]
	if !found {
		return object.raw
	}
	start := object.starts[field]
	end := span.end
	if len(object.keys) > 1 && object.keys[0] == field {
		if comma := bytes.IndexByte([]byte(object.raw[end:]), ','); comma >= 0 {
			end += comma + 1
		}
	}
	return object.raw[:start] + object.raw[end:]
}

// ScopeDocument retains the exact witness used for compare-and-swap. Typed
// updates replace only the intended values, including inside existing refs.
type ScopeDocument struct {
	object *scopeObject
	scope  Scope
}

func DecodeScopeDocument(raw string) (*ScopeDocument, error) {
	object, err := parseScopeObject(raw)
	if err != nil {
		return nil, err
	}
	scope, err := DecodeScope(raw)
	if err != nil {
		return nil, err
	}
	return &ScopeDocument{object: object, scope: scope}, nil
}

func (document *ScopeDocument) Original() string { return document.object.raw }

func (document *ScopeDocument) Scope() Scope {
	scope := document.scope
	scope.Refs = append([]ScopedRef(nil), scope.Refs...)
	scope.LifecycleReviewerIDs = append([]int64(nil), scope.LifecycleReviewerIDs...)
	return scope
}

func (document *ScopeDocument) WithNewOID(oid string) (string, error) {
	if document.scope.NewOID != "" && document.scope.NewOID != oid {
		return "", ErrScopeConflict
	}
	return document.WithRefResult(oid)
}

func (document *ScopeDocument) WithRefResult(oid string) (string, error) {
	if document.scope.NewOID == oid {
		return document.Original(), nil
	}
	value, err := json.Marshal(oid)
	if err != nil {
		return "", err
	}
	return document.object.withValue("new_oid", string(value)), nil
}

func (document *ScopeDocument) WithNativeRename(plan *RefRenamePlan) (string, error) {
	if plan == nil {
		return document.object.without("native_rename"), nil
	}
	value, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	return document.object.withValue("native_rename", string(value)), nil
}

func (document *ScopeDocument) WithCompletion(ref, oldOID, newOID, phase string) (string, error) {
	raw := document.Original()
	for _, field := range []struct{ key, value string }{
		{"completion_ref", ref},
		{"completion_old_oid", oldOID},
		{"completion_new_oid", newOID},
		{"completion_phase", phase},
	} {
		object, err := parseScopeObject(raw)
		if err != nil {
			return "", err
		}
		if previous, found := object.value(field.key); found {
			var value string
			if json.Unmarshal([]byte(previous), &value) == nil && value == field.value {
				continue
			}
		}
		value, _ := json.Marshal(field.value)
		raw = object.withValue(field.key, string(value))
	}
	return raw, nil
}

func (document *ScopeDocument) WithReceivePushOptions(hasOptions bool) (string, error) {
	if document.scope.ReceiveHasPushOptions == hasOptions {
		return document.Original(), nil
	}
	value, _ := json.Marshal(hasOptions)
	return document.object.withValue("receive_has_push_options", string(value)), nil
}

func (document *ScopeDocument) UpdateRefs(refs []ScopedRef) (string, error) {
	return document.refineRefs(refs, false)
}

func (document *ScopeDocument) AppendRefs(refs []ScopedRef) (string, error) {
	return document.refineRefs(refs, true)
}

func (document *ScopeDocument) refineRefs(refs []ScopedRef, appendOnly bool) (string, error) {
	raw, ok := document.object.value("refs")
	if !ok || bytes.Equal(bytes.TrimSpace([]byte(raw)), []byte("null")) {
		raw = "[]"
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return "", errors.New("reservation refs are not an array")
	}
	var elements []string
	var spans []jsonSpan
	index := make(map[string]int)
	for decoder.More() {
		var element json.RawMessage
		if err := decoder.Decode(&element); err != nil {
			return "", err
		}
		var ref ScopedRef
		if err := json.Unmarshal(element, &ref); err != nil {
			return "", err
		}
		end := int(decoder.InputOffset())
		spans = append(spans, jsonSpan{end - len(element), end})
		index[ref.Ref] = len(elements)
		elements = append(elements, string(element))
	}
	if _, err := decoder.Token(); err != nil {
		return "", err
	}
	closeAt := int(decoder.InputOffset()) - 1
	originalCount := len(elements)
	for _, next := range refs {
		nextTarget, _, err := ScopedRefRepositoryTarget(next)
		if err != nil {
			return "", ErrScopeConflict
		}
		if position, found := index[next.Ref]; found {
			var previous ScopedRef
			if err := json.Unmarshal([]byte(elements[position]), &previous); err != nil {
				return "", err
			}
			previousTarget, _, err := ScopedRefRepositoryTarget(previous)
			if err != nil || previousTarget != nextTarget {
				return "", ErrScopeConflict
			}
			if appendOnly {
				if previous.OldOID != next.OldOID || previous.NewOID != next.NewOID {
					return "", ErrScopeConflict
				}
				continue
			}
			if previous.NewOID == next.NewOID {
				continue
			}
			object, err := parseScopeObject(elements[position])
			if err != nil {
				return "", err
			}
			value, _ := json.Marshal(next.NewOID)
			elements[position] = object.withValue("new_oid", string(value))
			continue
		}
		if !appendOnly {
			next.OldOID = ""
		}
		value, err := json.Marshal(next)
		if err != nil {
			return "", err
		}
		index[next.Ref] = len(elements)
		elements = append(elements, string(value))
	}
	var additions bytes.Buffer
	for position := originalCount; position < len(elements); position++ {
		if position > 0 {
			additions.WriteByte(',')
		}
		additions.WriteString(elements[position])
	}
	raw = raw[:closeAt] + additions.String() + raw[closeAt:]
	for position := originalCount - 1; position >= 0; position-- {
		span := spans[position]
		raw = raw[:span.start] + elements[position] + raw[span.end:]
	}
	if previous, present := document.object.value("refs"); present && previous == raw {
		return document.Original(), nil
	}
	if len(refs) == 0 {
		return document.Original(), nil
	}
	return document.object.withValue("refs", raw), nil
}
