// Package journal implements a durable, single-writer, append-only run log.
package journal

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Event struct {
	Version  int             `json:"version"`
	Sequence int             `json:"sequence"`
	Time     time.Time       `json:"time"`
	Kind     string          `json:"kind"`
	Data     json.RawMessage `json:"data"`
	Previous string          `json:"previous"`
	Hash     string          `json:"hash"`
}

type Journal struct {
	mu       sync.Mutex
	file     *os.File
	seq      int
	last     string
	Dir      string
	Redactor *Redactor
}

func Open(dir string, redactor *Redactor) (*Journal, error) {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(filepath.Join(dir, "journal.ndjson"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = lock(f); e != nil {
		f.Close()
		return nil, fmt.Errorf("run is already in use: %w", e)
	}
	events, n, e := read(f)
	if e != nil {
		f.Close()
		return nil, e
	}
	if e = f.Truncate(n); e != nil {
		f.Close()
		return nil, e
	}
	if _, e = f.Seek(0, io.SeekEnd); e != nil {
		f.Close()
		return nil, e
	}
	j := &Journal{file: f, Dir: dir, Redactor: redactor}
	if len(events) > 0 {
		last := events[len(events)-1]
		j.seq = last.Sequence
		j.last = last.Hash
	}
	return j, nil
}
func (j *Journal) Close() error { j.mu.Lock(); defer j.mu.Unlock(); return j.file.Close() }
func (j *Journal) Append(kind string, v any) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.Redactor != nil {
		v = j.Redactor.Redact(v)
	}
	data, e := json.Marshal(v)
	if e != nil {
		return e
	}
	event := Event{Version: 1, Sequence: j.seq + 1, Time: time.Now().UTC(), Kind: kind, Data: data, Previous: j.last}
	event.Hash = checksum(event)
	line, e := json.Marshal(event)
	if e != nil {
		return e
	}
	line = append(line, '\n')
	if _, e = j.file.Write(line); e != nil {
		return e
	}
	if e = j.file.Sync(); e != nil {
		return e
	}
	j.seq = event.Sequence
	j.last = event.Hash
	return nil
}
func Read(dir string) ([]Event, error) {
	f, e := os.Open(filepath.Join(dir, "journal.ndjson"))
	if e != nil {
		return nil, e
	}
	defer f.Close()
	events, _, e := read(f)
	return events, e
}
func read(r io.Reader) ([]Event, int64, error) {
	reader := bufio.NewReader(r)
	var events []Event
	var offset int64
	prev := ""
	for {
		line, e := reader.ReadBytes('\n')
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return nil, offset, e
		}
		var event Event
		if e = Decode(line, &event); e != nil {
			return nil, offset, fmt.Errorf("corrupt journal at byte %d: %w", offset, e)
		}
		if event.Version != 1 || event.Sequence != len(events)+1 || event.Previous != prev || event.Hash != checksum(event) {
			return nil, offset, fmt.Errorf("journal integrity failure at event %d", event.Sequence)
		}
		events = append(events, event)
		offset += int64(len(line))
		prev = event.Hash
	}
	return events, offset, nil
}
func checksum(e Event) string {
	e.Hash = ""
	b, _ := json.Marshal(e)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func Decode(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	return d.Decode(v)
}

func WriteJSON(dir, name string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return WriteFile(dir, name, append(b, '\n'))
}
func WriteFile(dir, name string, b []byte) error {
	if filepath.Base(name) != name {
		return fmt.Errorf("output name must not contain a directory")
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".inspector-*")
	if e != nil {
		return e
	}
	path := f.Name()
	defer os.Remove(path)
	if e = f.Chmod(0600); e != nil {
		f.Close()
		return e
	}
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(path, filepath.Join(dir, name))
}

type Redactor struct {
	mu      sync.RWMutex
	keys    map[string]bool
	secrets []string
}

func NewRedactor(fields []string) *Redactor {
	r := &Redactor{keys: map[string]bool{}}
	for _, k := range append([]string{"authorization", "proxy-authorization", "cookie", "set-cookie", "password", "certificatePassword", "apiKey", "token", "access_token", "refresh_token", "client_secret", "secret", "privateKey"}, fields...) {
		r.keys[normal(k)] = true
	}
	return r
}
func normal(k string) string {
	if i := strings.LastIndexAny(k, ":/"); i >= 0 {
		k = k[i+1:]
	}
	return strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(k))
}
func (r *Redactor) Secret(s string) {
	if s == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range r.secrets {
		if s == v {
			return
		}
	}
	r.secrets = append(r.secrets, s)
}
func (r *Redactor) Sensitive(k string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.keys[normal(k)]
}
func (r *Redactor) Redact(v any) any {
	b, e := json.Marshal(v)
	if e != nil {
		return nil
	}
	var data any
	if e = Decode(b, &data); e != nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var walk func(any, bool, bool, bool) any
	walk = func(v any, hide, schema, payload bool) any {
		switch x := v.(type) {
		case map[string]any:
			fieldName, _ := x["field"].(string)
			if field, ok := x["field"].(map[string]any); ok {
				fieldName, _ = field["id"].(string)
			}
			sensitiveField := r.keys[normal(fieldName)]
			if id, ok := x["id"].(string); ok && r.keys[normal(id)] {
				if definition, ok := x["schema"]; ok {
					x["schema"] = stripSecretExamples(definition)
				}
			}
			for k, v := range x {
				if !payload && !hide && x["openapi"] != nil && k == "paths" {
					if paths, ok := v.(map[string]any); ok {
						for path, item := range paths {
							paths[path] = walk(item, false, false, false)
						}
						x[k] = paths
						continue
					}
				}
				if !payload && !hide && x["openapi"] != nil && k == "components" {
					if components, ok := v.(map[string]any); ok {
						for section, entries := range components {
							if entries, ok := entries.(map[string]any); ok {
								for name, definition := range entries {
									if r.keys[normal(name)] {
										definition = stripSecretExamples(definition)
									}
									entries[name] = walk(definition, false, section == "schemas", false)
								}
								components[section] = entries
							}
						}
						x[k] = components
						continue
					}
				}
				if !hide && k == "value-envs" {
					x[k] = v
					continue
				}
				if sensitiveField && k == "states" {
					if states, ok := v.([]any); ok {
						for _, state := range states {
							if m, ok := state.(map[string]any); ok {
								m["value"] = walk(m["value"], true, false, true)
							}
						}
						x[k] = states
						continue
					}
				}
				if sensitiveField && k == "value" && v != nil && v != "" && (x["op"] == "eq" || x["op"] == "neq" || x["op"] == "enum" || x["kind"] != nil) {
					x[k] = walk(v, true, false, true)
					continue
				}
				// Recognize schema containers by their structural position, never
				// merely by an arbitrary payload having a property named "type".
				if !payload && !hide && (k == "schemas" || schema && (k == "properties" || k == "patternProperties" || k == "$defs")) {
					if properties, ok := v.(map[string]any); ok {
						for name, definition := range properties {
							if r.keys[normal(name)] {
								definition = stripSecretExamples(definition)
							}
							properties[name] = walk(definition, false, true, false)
						}
						x[k] = properties
						continue
					}
				}
				childPayload := payload || !schema && (k == "body" || k == "input" || k == "response" || k == "before" || k == "after")
				childSchema := !childPayload && (schema || k == "schema")
				if schema && (k == "example" || k == "examples" || k == "default" || k == "const" || k == "enum") {
					childSchema = false
				}
				x[k] = walk(v, hide || !schema && r.keys[normal(k)], childSchema, childPayload)
			}
			return x
		case []any:
			for i, v := range x {
				x[i] = walk(v, hide, schema, payload)
			}
			return x
		case string:
			if hide {
				return "[REDACTED]"
			}
			for _, s := range r.secrets {
				x = strings.ReplaceAll(x, s, "[REDACTED]")
			}
			return x
		default:
			if hide && v != nil {
				return nil
			}
			return v
		}
	}
	return walk(data, false, false, false)
}

func stripSecretExamples(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for _, key := range []string{"example", "examples", "default"} {
			delete(x, key)
		}
		for k, v := range x {
			x[k] = stripSecretExamples(v)
		}
	case []any:
		for i, v := range x {
			x[i] = stripSecretExamples(v)
		}
	}
	return v
}
