package journal

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestJournalRecoversPartialTailAndDetectsTampering(t *testing.T) {
	dir := t.TempDir()
	j, err := Open(dir, NewRedactor(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err = j.Append("intent", map[string]any{"id": "one"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, NewRedactor(nil)); err == nil {
		t.Fatal("concurrent writer obtained lock")
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "journal.ndjson")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"version":1,"seq`)
	f.Close()
	j, err = Open(dir, NewRedactor(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err = j.Append("observation", map[string]any{"id": "two"}); err != nil {
		t.Fatal(err)
	}
	j.Close()
	events, err := Read(dir)
	if err != nil || len(events) != 2 {
		t.Fatalf("partial tail recovery: %d %v", len(events), err)
	}
	data, _ := os.ReadFile(path)
	data = bytes.Replace(data, []byte(`"one"`), []byte(`"bad"`), 1)
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); err == nil {
		t.Fatal("tampered event was accepted")
	}
}
func TestRedactionPreservesSchemasButHidesSecretObjects(t *testing.T) {
	r := NewRedactor(nil)
	r.Secret("runtime-secret-123")
	input := map[string]any{"response": map[string]any{"password": map[string]any{"type": "raw-secret", "value": "hidden-value"}, "message": "echo runtime-secret-123"}, "schema": map[string]any{"type": "object", "properties": map[string]any{"password": map[string]any{"allOf": []any{map[string]any{"type": "string", "default": "must-not-leak"}}}}}}
	out := r.Redact(input)
	b, _ := json.Marshal(out)
	for _, secret := range []string{"raw-secret", "hidden-value", "runtime-secret-123", "must-not-leak"} {
		if bytes.Contains(b, []byte(secret)) {
			t.Fatalf("leaked %s: %s", secret, b)
		}
	}
	if !bytes.Contains(b, []byte(`"type":"string"`)) {
		t.Fatalf("redaction damaged schema: %s", b)
	}
}

func TestRedactionPreservesSensitiveNamedOpenAPIMetadata(t *testing.T) {
	input := map[string]any{
		"openapi": "3.1.0",
		"paths": map[string]any{
			"/oauth/token":    map[string]any{"post": map[string]any{"requestBody": map[string]any{"$ref": "#/components/requestBodies/token"}}},
			"/users/password": map[string]any{"put": map[string]any{"responses": map[string]any{"200": map[string]any{"$ref": "#/components/responses/token"}}}},
		},
		"components": map[string]any{
			"schemas":         map[string]any{"apiKey": map[string]any{"type": "string", "example": "secret-example"}},
			"requestBodies":   map[string]any{"token": map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/apiKey"}}}}},
			"responses":       map[string]any{"token": map[string]any{"description": "An access token"}},
			"securitySchemes": map[string]any{"apiKey": map[string]any{"type": "apiKey", "in": "header", "name": "X-API-Key"}},
		},
	}
	out := NewRedactor(nil).Redact(input)
	b, _ := json.Marshal(out)
	if bytes.Contains(b, []byte("[REDACTED]")) || bytes.Contains(b, []byte("secret-example")) {
		t.Fatalf("schema metadata corrupted or example leaked: %s", b)
	}
	for _, preserved := range []string{"#/components/requestBodies/token", "#/components/responses/token", "#/components/schemas/apiKey", `"type":"apiKey"`, `"type":"string"`} {
		if !bytes.Contains(b, []byte(preserved)) {
			t.Fatalf("missing metadata %s: %s", preserved, b)
		}
	}
}

func TestPayloadCannotMasqueradeAsOpenAPIMetadata(t *testing.T) {
	input := map[string]any{"response": map[string]any{
		"openapi": "3.1.0",
		"paths":   map[string]any{"/oauth/token": "hidden-path-value"},
		"schemas": map[string]any{"password": map[string]any{"type": "hidden-type-value"}},
		"schema":  map[string]any{"properties": map[string]any{"apiKey": "hidden-schema-value"}},
	}}
	b, _ := json.Marshal(NewRedactor(nil).Redact(input))
	for _, value := range []string{"hidden-path-value", "hidden-type-value", "hidden-schema-value"} {
		if bytes.Contains(b, []byte(value)) {
			t.Fatalf("payload bypassed redaction: %s", b)
		}
	}
}
