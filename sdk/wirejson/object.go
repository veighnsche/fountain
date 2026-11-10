// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package wirejson supplies strict JSON wire mechanics. Callers own size
// bounds, field schemas, nullable fields and semantic validation.
package wirejson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

func RejectDuplicateKeys(data []byte, what string) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := checkValue(decoder); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s must contain one value", what)
	}
	return nil
}

func checkValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok {
				return errors.New("invalid JSON object key")
			}
			if seen[key] {
				return fmt.Errorf("duplicate JSON object key %q", key)
			}
			seen[key] = true
			if err := checkValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := checkValue(decoder); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	_, err = decoder.Token()
	return err
}

func ObjectFields(data []byte, what string) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, fmt.Errorf("%s must be an object", what)
	}
	if err := RejectDuplicateKeys(trimmed, what); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

func RequireFields(fields map[string]json.RawMessage, what string, required, nonNull []string) error {
	for _, field := range required {
		if _, ok := fields[field]; !ok {
			return fmt.Errorf("%s is missing required field %q", what, field)
		}
	}
	for _, field := range nonNull {
		if value, ok := fields[field]; ok && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("%s field %q must not be null", what, field)
		}
	}
	return nil
}
