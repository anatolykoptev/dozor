package keyfile

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	fileVersion = "1"
	endMarker   = "end: true"
	maxFileSize = 1 << 20
	digestLen   = 32

	maxAttrs     = 32
	maxAttrValue = 256
)

var (
	nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	hexRE  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	lineRE = regexp.MustCompile(`line (\d+)`)

	attrKeyRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
)

// rawFile is decoded strictly. Version and End are nodes so that only the
// exact integer 1 and the exact boolean true are accepted.
type rawFile struct {
	Version yaml.Node `yaml:"version"`
	Keys    []rawKey  `yaml:"keys"`
	End     yaml.Node `yaml:"end"`
}

type rawKey struct {
	Name    string         `yaml:"name"`
	SHA256  string         `yaml:"sha256"`
	Expires string         `yaml:"expires"`
	Tools   []string       `yaml:"tools"`
	Attrs   map[string]any `yaml:"attrs"`
}

// keyEntry is one validated key. tools and attrs are never mutated after load.
type keyEntry struct {
	name    string
	digest  [digestLen]byte
	expires time.Time
	tools   []string
	attrs   map[string]string
}

// loadError describes why a file could not be used. Its text is a fixed
// category (plus a YAML line number): parser messages can quote file content,
// which may include hashes, so they are never logged.
type loadError struct{ kind string }

func (e *loadError) Error() string { return e.kind }

// lastNonBlankLine returns the last line of data that is not blank, without a
// trailing carriage return.
func lastNonBlankLine(data []byte) string {
	lines := strings.Split(string(data), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return strings.TrimSuffix(lines[i], "\r")
		}
	}
	return ""
}

func isScalar(n *yaml.Node, tag, value string) bool {
	return n.Kind == yaml.ScalarNode && n.ShortTag() == tag && n.Value == value
}

// parse strictly decodes data. A structural problem (YAML error, unknown
// field, wrong version, missing end marker) returns an error and the whole
// file is invalid. An invalid, duplicated or vetoed individual entry is
// logged and dropped on its own.
func (s *Store) parse(data []byte) ([]*keyEntry, error) {
	if len(data) > maxFileSize {
		return nil, &loadError{kindTooLarge}
	}
	// A truncated file must never look complete: the last line is the marker.
	if lastNonBlankLine(data) != endMarker {
		return nil, &loadError{"missing end marker"}
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var f rawFile
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, &loadError{"empty file"}
		}
		kind := "yaml error"
		if m := lineRE.FindStringSubmatch(err.Error()); m != nil {
			kind += " near line " + m[1]
		}
		return nil, &loadError{kind}
	}
	var extra rawFile
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, &loadError{"multiple yaml documents"}
	}
	if !isScalar(&f.Version, "!!int", fileVersion) {
		return nil, &loadError{"unsupported version"}
	}
	if !isScalar(&f.End, "!!bool", "true") {
		return nil, &loadError{"missing end marker"}
	}
	return s.entries(f.Keys), nil
}

// entries validates each raw entry on its own. Two entries claiming one name
// or one hash are ambiguous, so every copy is dropped.
func (s *Store) entries(keys []rawKey) []*keyEntry {
	names := make(map[string]int, len(keys))
	hashes := make(map[string]int, len(keys))
	for _, k := range keys {
		names[k.Name]++
		hashes[k.SHA256]++
	}
	var out []*keyEntry
	for i, k := range keys {
		reason := ""
		var e *keyEntry
		switch {
		case names[k.Name] > 1:
			reason = "duplicate name"
		case k.SHA256 != "" && hashes[k.SHA256] > 1:
			reason = "duplicate sha256"
		default:
			e, reason = s.validate(k)
		}
		if reason != "" {
			name := ""
			if nameRE.MatchString(k.Name) {
				name = k.Name
			}
			s.logger.Error("keyfile: entry dropped", slog.Int("index", i),
				slog.String("name", name), slog.String("reason", reason))
			continue
		}
		out = append(out, e)
	}
	return out
}

// validate returns the entry, or a non-empty drop reason.
func (s *Store) validate(k rawKey) (*keyEntry, string) {
	if !nameRE.MatchString(k.Name) {
		return nil, "invalid name"
	}
	if s.isReserved(k.Name) {
		return nil, "reserved name"
	}
	if !hexRE.MatchString(k.SHA256) {
		return nil, "invalid sha256"
	}
	expires, ok := parseExpiry(k.Expires)
	if !ok {
		return nil, "expires missing, not RFC 3339, or not UTC"
	}
	if !s.now().Before(expires) {
		return nil, "expired"
	}
	if reason := s.checkTools(k.Tools); reason != "" {
		return nil, reason
	}
	attrs, reason := checkAttrs(k.Attrs)
	if reason != "" {
		return nil, reason
	}
	if reason := s.runValidator(k.Name, attrs); reason != "" {
		return nil, reason
	}
	raw, err := hex.DecodeString(k.SHA256)
	if err != nil || len(raw) != digestLen {
		return nil, "invalid sha256"
	}
	e := &keyEntry{name: k.Name, expires: expires, tools: dedupe(k.Tools), attrs: attrs}
	copy(e.digest[:], raw)
	return e, ""
}

func (s *Store) checkTools(tools []string) string {
	if len(tools) == 0 {
		return "no tools"
	}
	for _, t := range tools {
		if t == "*" {
			return "wildcard tool not allowed"
		}
		if _, known := s.tools[t]; !known {
			return "tool not registered"
		}
	}
	return ""
}

// checkAttrs enforces the attrs shape: at most 32 flat string-to-string pairs.
// The library never interprets keys or values beyond that.
func checkAttrs(in map[string]any) (map[string]string, string) {
	if len(in) > maxAttrs {
		return nil, "too many attrs"
	}
	out := make(map[string]string, len(in))
	for key, v := range in {
		if !attrKeyRE.MatchString(key) {
			return nil, "invalid attr key"
		}
		val, ok := v.(string)
		if !ok {
			return nil, "attr value is not a string"
		}
		if len(val) > maxAttrValue {
			return nil, "attr value too long"
		}
		out[key] = val
	}
	return out, ""
}

// runValidator applies the consumer veto. A panic in consumer code drops the
// entry rather than unwinding through the reload.
func (s *Store) runValidator(name string, attrs map[string]string) (reason string) {
	if s.validator == nil {
		return ""
	}
	defer func() {
		if r := recover(); r != nil {
			reason = "validator panic"
		}
	}()
	if err := s.validator(name, maps.Clone(attrs)); err != nil {
		return fmt.Sprintf("rejected by consumer validator: %v", err)
	}
	return ""
}

func (s *Store) isReserved(name string) bool {
	if slices.Contains(reservedExact, name) {
		return true
	}
	for _, p := range s.reserved {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// parseExpiry accepts only RFC 3339 timestamps written in UTC with a literal "Z".
func parseExpiry(v string) (time.Time, bool) {
	if !strings.HasSuffix(v, "Z") {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

func dedupe(in []string) []string {
	out := make([]string, 0, len(in))
	for _, t := range in {
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

const kindTooLarge = "file too large"

// kindParentWritable is the refusal of a group- or world-writable parent
// directory. It is not recorded against the file signature; see attempt.
const kindParentWritable = "parent directory is group- or world-writable"
