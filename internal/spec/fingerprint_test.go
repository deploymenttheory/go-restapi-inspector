package spec

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
)

func revisionFixture() map[string]any {
	return map[string]any{
		"openapi": "3.1.0", "info": map[string]any{"title": "Test", "version": "production"},
		"paths": map[string]any{
			"/items":      map[string]any{"post": map[string]any{"requestBody": map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/Item"}}}}, "responses": map[string]any{"200": map[string]any{"description": "OK"}}}},
			"/items/{id}": map[string]any{"get": map[string]any{"responses": map[string]any{"200": map[string]any{"description": "OK", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/Item"}}}}}}},
			"/unchanged":  map[string]any{"get": map[string]any{"responses": map[string]any{"200": map[string]any{"description": "OK"}}}},
		},
		"components": map[string]any{"schemas": map[string]any{"Item": map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string", "maxLength": 10}, "mode": map[string]any{"enum": []any{"a", "b"}}, "child": map[string]any{"$ref": "#/components/schemas/Item"}}}, "Unused": map[string]any{"type": "string"}}},
	}
}

func fingerprintDocument(t *testing.T, raw map[string]any) map[string]OperationFingerprint {
	t.Helper()
	d, err := FromMap(raw)
	if err != nil {
		t.Fatal(err)
	}
	f, err := d.Fingerprints()
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestOperationReferenceFingerprints(t *testing.T) {
	shared := []string{"GET /items/{id} []", "POST /items [application/json]"}
	cases := []struct {
		name   string
		change func(map[string]any)
		want   []string
	}{
		{"version only", func(d map[string]any) { Map(d["info"])["version"] = "2" }, nil},
		{"unused component", func(d map[string]any) {
			Map(Map(d["components"])["schemas"])["Unused"] = map[string]any{"type": "integer"}
		}, nil},
		{"added field", func(d map[string]any) { revisionProperties(d)["new"] = map[string]any{"type": "integer"} }, shared},
		{"removed field", func(d map[string]any) { delete(revisionProperties(d), "name") }, shared},
		{"description", func(d map[string]any) {
			Map(revisionProperties(d)["name"])["description"] = "Now unique within the tenant"
		}, shared},
		{"enum expansion", func(d map[string]any) { Map(revisionProperties(d)["mode"])["enum"] = []any{"a", "b", "c"} }, shared},
		{"bound", func(d map[string]any) { Map(revisionProperties(d)["name"])["maxLength"] = 20 }, shared},
		{"numeric spelling", func(d map[string]any) { Map(revisionProperties(d)["name"])["maxLength"] = json.Number("10.0") }, nil},
		{"required", func(d map[string]any) { Map(Map(Map(d["components"])["schemas"])["Item"])["required"] = []any{"name"} }, shared},
		{"privilege", func(d map[string]any) {
			Map(Map(Map(d["paths"])["/unchanged"])["get"])["x-required-privileges"] = []any{"Read Updated"}
		}, []string{"GET /unchanged []"}},
		{"global description", func(d map[string]any) { Map(d["info"])["description"] = "New semantics" }, []string{"GET /items/{id} []", "GET /unchanged []", "POST /items [application/json]"}},
		{"inherited security", func(d map[string]any) { d["security"] = []any{map[string]any{"Auth": []any{}}} }, []string{"GET /items/{id} []", "GET /unchanged []", "POST /items [application/json]"}},
		{"inherited parameter", func(d map[string]any) {
			Map(Map(d["paths"])["/unchanged"])["parameters"] = []any{map[string]any{"in": "query", "name": "q", "schema": map[string]any{"type": "string"}}}
		}, []string{"GET /unchanged []"}},
		{"literal ref example", func(d map[string]any) {
			Map(revisionProperties(d)["name"])["example"] = map[string]any{"$ref": "./not-a-file"}
		}, shared},
		{"schema examples", func(d map[string]any) {
			Map(revisionProperties(d)["name"])["examples"] = []any{map[string]any{"$ref": "./not-a-file"}}
		}, shared},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			a := revisionFixture()
			b := model.Clone(a)
			test.change(b)
			before, after := fingerprintDocument(t, a), fingerprintDocument(t, b)
			var changed []string
			for key, f := range before {
				if after[key].Full != f.Full {
					changed = append(changed, key)
				}
			}
			sort.Strings(changed)
			if !reflect.DeepEqual(changed, test.want) {
				t.Fatalf("changed %v, want %v", changed, test.want)
			}
		})
	}
}

func revisionProperties(d map[string]any) map[string]any {
	return Map(Map(Map(Map(d["components"])["schemas"])["Item"])["properties"])
}

func TestMediaIdentityAndKeywordNamedProperties(t *testing.T) {
	a := revisionFixture()
	props := revisionProperties(a)
	props["default"] = map[string]any{"$ref": "#/components/schemas/Unused"}
	b := model.Clone(a)
	Map(Map(b["components"])["schemas"])["Unused"] = map[string]any{"type": "number"}
	if fingerprintDocument(t, a)["POST /items [application/json]"].Full == fingerprintDocument(t, b)["POST /items [application/json]"].Full {
		t.Fatal("reference in field named default was ignored")
	}
	b = model.Clone(a)
	body := Map(Map(Map(Map(b["paths"])["/items"])["post"])["requestBody"])
	Map(body["content"])["application/merge-patch+json"] = map[string]any{"schema": map[string]any{"type": "object"}}
	before, after := fingerprintDocument(t, a), fingerprintDocument(t, b)
	if len(after) != len(before)+1 || before["POST /items [application/json]"] != after["POST /items [application/json]"] {
		t.Fatal("new representation invalidated existing representation")
	}
}

func TestInheritedDescriptionAndNamedSecurityReferences(t *testing.T) {
	a := revisionFixture()
	item := Map(Map(a["paths"])["/unchanged"])
	item["description"] = "Path meaning v1"
	Map(item["get"])["description"] = "Operation meaning"
	b := model.Clone(a)
	Map(Map(b["paths"])["/unchanged"])["description"] = "Path meaning v2"
	if fingerprintDocument(t, a)["GET /unchanged []"].Full == fingerprintDocument(t, b)["GET /unchanged []"].Full {
		t.Fatal("operation description hid a path description change")
	}
	a["security"] = []any{map[string]any{"default": []any{}}}
	Map(a["components"])["securitySchemes"] = map[string]any{"default": map[string]any{"$ref": "#/components/securitySchemes/Actual"}, "Actual": map[string]any{"type": "http", "scheme": "bearer"}}
	b = model.Clone(a)
	Map(Map(Map(b["components"])["securitySchemes"])["Actual"])["description"] = "New privilege scope"
	if fingerprintDocument(t, a)["GET /unchanged []"].Security == fingerprintDocument(t, b)["GET /unchanged []"].Security {
		t.Fatal("reference through a security scheme named default was ignored")
	}
}

func TestCanonicalIdentityIncludesExternalChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api.json")
	external := filepath.Join(dir, "types.json")
	raw := revisionFixture()
	revisionProperties(raw)["name"] = map[string]any{"$ref": "./types.json#/Name"}
	b, _ := json.Marshal(raw)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	write := func(value string) {
		t.Helper()
		if err := os.WriteFile(external, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	load := func() *Document {
		t.Helper()
		d, err := Load(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	write(`{"Name":{"type":"string","maxLength":9007199254740993}}`)
	a := load()
	write(`{ "Name": {"maxLength":9007199254740993.0, "type":"string"} }`)
	bdoc := load()
	if a.Identity("").Canonical != bdoc.Identity("").Canonical {
		t.Fatal("formatting changed canonical fingerprint")
	}
	write(`{"Name":{"type":"string","maxLength":9007199254740992}}`)
	c := load()
	if a.Hash != c.Hash || a.Identity("").Canonical == c.Identity("").Canonical {
		t.Fatal("external change missed despite identical root bytes")
	}
}

// Set JAMF_SPEC_DELTA_DIR to the two pinned snapshot directories to reproduce
// the real-world comparison without network access or live API requests.
func TestJamfVersionDelta(t *testing.T) {
	dir := os.Getenv("JAMF_SPEC_DELTA_DIR")
	if dir == "" {
		t.Skip("set JAMF_SPEC_DELTA_DIR for pinned Jamf snapshots")
	}
	docs := make([]*Document, 2)
	fingerprints := make([]map[string]OperationFingerprint, 2)
	for n, version := range []string{"11.30.2", "11.31.1"} {
		var err error
		docs[n], err = Load(context.Background(), filepath.Join(dir, version, "api-schema.json"))
		if err != nil {
			t.Fatal(err)
		}
		fingerprints[n], err = docs[n].Fingerprints()
		if err != nil {
			t.Fatal(err)
		}
		if docs[n].Identity(version).Version != "production" {
			t.Fatal("unexpected declared version")
		}
	}
	status := map[string]string{}
	for _, op := range docs[0].Operations {
		status[op.Method+" "+op.Path] = "removed"
	}
	for _, op := range docs[1].Operations {
		key := op.Method + " " + op.Path
		old, exists := fingerprints[0][op.Identity()]
		next := "unchanged"
		if !exists {
			next = "added"
		} else if old.Full != fingerprints[1][op.Identity()].Full {
			next = "changed"
		}
		if status[key] != "changed" && status[key] != "added" {
			status[key] = next
		}
	}
	counts := map[string]int{}
	for _, value := range status {
		counts[value]++
	}
	if !reflect.DeepEqual(counts, map[string]int{"added": 3, "changed": 8, "unchanged": 809}) {
		t.Fatalf("unexpected Jamf delta: %v", counts)
	}
	for _, key := range []string{"GET /v2/smtp-server", "PUT /v2/smtp-server"} {
		if status[key] != "changed" {
			t.Fatal("shared SMTP description change missed")
		}
	}
	t.Logf("Jamf 11.30.2 -> 11.31.1: %v", counts)
}
