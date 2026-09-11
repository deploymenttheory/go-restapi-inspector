package spec

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestExternalReferencesAndSchemaSiblings(t *testing.T) {
	dir := t.TempDir()
	external := []byte("Name:\n  type: string\n  maxLength: 5\n")
	main := []byte("openapi: 3.1.0\ninfo: {title: Test, version: '1'}\npaths:\n  /widgets:\n    post:\n      operationId: create\n      requestBody:\n        content:\n          application/json:\n            schema:\n              type: object\n              properties:\n                name:\n                  $ref: './types.yaml#/Name'\n                  minLength: 3\n      responses:\n        '200': {description: OK}\n")
	if err := os.WriteFile(filepath.Join(dir, "types.yaml"), external, 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "api.yaml")
	if err := os.WriteFile(path, main, 0600); err != nil {
		t.Fatal(err)
	}
	d, err := Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	op, _ := d.Find("create")
	schema, err := d.Compile(op.Schema)
	if err != nil {
		t.Fatal(err)
	}
	for value, accepted := range map[string]bool{"ab": false, "abc": true, "abcdef": false} {
		if actual := schema.Validate(map[string]any{"name": value}) == nil; actual != accepted {
			t.Errorf("reference sibling intersection lost for %s", value)
		}
	}
	if len(Map(d.Raw["x-inspector-documents"])) != 1 {
		t.Fatal("external document was not bundled")
	}
}
func TestNormalize30DoesNotRewriteExamplePayload(t *testing.T) {
	example := map[string]any{"nullable": true, "type": "business-data", "exclusiveMinimum": true, "minimum": 3}
	schema := map[string]any{"type": "object", "properties": map[string]any{"example": map[string]any{"type": "string", "nullable": true}, "amount": map[string]any{"type": "number", "minimum": 0, "exclusiveMinimum": true}}, "example": example}
	Normalize30(schema)
	if len(Slice(Map(Map(schema["properties"])["example"])["type"])) != 2 {
		t.Fatal("property literally named example was skipped")
	}
	if example["type"] != "business-data" || example["nullable"] != true {
		t.Fatal("example payload was rewritten as a schema")
	}
	amount := Map(Map(schema["properties"])["amount"])
	if amount["exclusiveMinimum"] != 0 || amount["minimum"] != nil {
		t.Fatalf("incorrect numeric migration %v", amount)
	}
}
