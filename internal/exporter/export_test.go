package exporter

import (
	"testing"

	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
	"github.com/deploymenttheory/go-restapi-inspector/internal/spec"
)

func TestOptionalCorrectionIsOperationLocalAndTraversesAllOf(t *testing.T) {
	shared := map[string]any{"type": "object", "allOf": []any{map[string]any{"required": []any{"name"}, "properties": map[string]any{"name": map[string]any{"type": "string"}}}}}
	operation := func(id string) map[string]any {
		return map[string]any{"operationId": id, "requestBody": map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/Shared"}}}}, "responses": map[string]any{"200": map[string]any{"description": "OK"}}}
	}
	raw := map[string]any{"openapi": "3.1.0", "info": map[string]any{"title": "Test", "version": "1"}, "paths": map[string]any{"/widgets": map[string]any{"post": operation("create"), "patch": operation("update")}}, "components": map[string]any{"schemas": map[string]any{"Shared": shared}}}
	d, err := spec.FromMap(raw)
	if err != nil {
		t.Fatal(err)
	}
	report := model.Report{Rules: []model.Rule{{ID: "optional", Operation: "PATCH /widgets", Kind: "fieldIsOptional", Field: "body:/name", Status: "supported", Trials: 3, Accepted: []string{"a", "b", "c"}}}}
	out, _, err := Build(d, report, nil)
	if err != nil {
		t.Fatal(err)
	}
	exported, err := spec.FromMap(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"create", "update"} {
		op, _ := exported.Find(id)
		compiled, err := exported.Compile(op.Schema)
		if err != nil {
			t.Fatal(err)
		}
		accepted := compiled.Validate(map[string]any{}) == nil
		if accepted != (id == "update") {
			t.Fatalf("shared-schema contamination for %s: accepted=%v", id, accepted)
		}
	}
	component := spec.Map(spec.Map(out["components"])["schemas"])["Shared"]
	if !model.Equal(component, shared) {
		t.Fatal("shared component was edited")
	}
}
func TestNestedAndCardinalityPredicatesMatchJSONSchema(t *testing.T) {
	d := &spec.Document{Raw: map[string]any{}}
	predicates := []model.Predicate{{Op: "present", Field: "body:/outer/value"}, {Op: "not", Args: []model.Predicate{{Op: "null", Field: "body:/outer/value"}}}, {Op: "atMostOne", Args: []model.Predicate{{Op: "present", Field: "body:/a"}, {Op: "present", Field: "body:/b"}}}, {Op: "allOrNone", Args: []model.Predicate{{Op: "present", Field: "body:/a"}, {Op: "present", Field: "body:/b"}}}}
	bodies := []any{nil, map[string]any{}, map[string]any{"outer": nil}, map[string]any{"outer": map[string]any{"value": nil}}, map[string]any{"outer": map[string]any{"value": "x"}}, map[string]any{"a": 1}, map[string]any{"a": 1, "b": 2}}
	for _, p := range predicates {
		s, ok := PredicateSchema(p)
		if !ok {
			t.Fatalf("unsupported %v", p)
		}
		compiled, err := d.Compile(s)
		if err != nil {
			t.Fatal(err)
		}
		for _, body := range bodies {
			if got := compiled.Validate(model.Clone(body)) == nil; got != p.Eval(model.Input{HasBody: true, Body: body}) {
				t.Fatalf("schema disagrees with predicate %v on %v", p, body)
			}
		}
	}
}
func TestResponseInferenceDoesNotInventRequirednessOrEnums(t *testing.T) {
	inferred := InferResponse(map[string]any{"id": "1", "state": "active"})
	if inferred["required"] != nil {
		t.Fatal("inferred requiredness from field frequency")
	}
	if spec.Map(spec.Map(inferred["properties"])["state"])["enum"] != nil {
		t.Fatal("inferred a closed enum from success")
	}
}
