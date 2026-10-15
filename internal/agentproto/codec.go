package agentproto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ErrDepth is returned when a document nests deeper than MaxJSONDepth.
var ErrDepth = errors.New("agentproto: json nesting too deep")

// DecodeStrict decodes exactly one JSON value from data into v. It rejects
// unknown fields, nesting deeper than MaxJSONDepth and any trailing bytes.
// Callers bound len(data) themselves before calling.
func DecodeStrict(data []byte, v any) error {
	if err := checkDepth(data, MaxJSONDepth); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("agentproto: decode: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("agentproto: trailing data after json value")
	}
	return nil
}

// checkDepth scans data once, tracking string state so brackets inside
// strings do not count, and fails when nesting exceeds limit.
func checkDepth(data []byte, limit int) error {
	depth := 0
	inStr, esc := false, false
	for _, c := range data {
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{', '[':
			depth++
			if depth > limit {
				return ErrDepth
			}
		case '}', ']':
			depth--
		}
	}
	return nil
}

// VersionOK reports whether a peer's protocol version is accepted.
func VersionOK(v int) bool {
	return v == ProtoVersion || v == MinProtoVersion
}
