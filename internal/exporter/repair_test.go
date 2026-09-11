package exporter

import (
	"fmt"
	"testing"

	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
	"github.com/deploymenttheory/go-restapi-inspector/internal/spec"
)

func TestIntegerStringRangeMatchesSchema(t *testing.T) {
	d := &spec.Document{Raw: map[string]any{}}
	for _, limits := range [][2]int{{1, 20}, {-20, -1}, {-15, 23}, {0, 0}, {98, 102}, {-102, -98}} {
		predicate := model.Predicate{Op: "integerStringRange", Field: "body:/value", Value: map[string]any{"minimum": limits[0], "maximum": limits[1]}}
		schema, ok := PredicateSchema(predicate)
		if !ok {
			t.Fatal("range not exportable")
		}
		validator, err := d.Compile(schema)
		if err != nil {
			t.Fatal(err)
		}
		for n := -120; n <= 120; n++ {
			for _, text := range []string{fmt.Sprint(n), fmt.Sprintf(" %+d ", n), fmt.Sprintf("%04d", n)} {
				body := map[string]any{"value": text}
				want := predicate.Eval(model.Input{HasBody: true, Body: body})
				if got := validator.Validate(body) == nil; got != want {
					t.Fatalf("%v on %q: schema=%v predicate=%v", limits, text, got, want)
				}
			}
		}
		for _, body := range []any{map[string]any{}, map[string]any{"value": nil}, map[string]any{"value": 2.5}, map[string]any{"value": "1.5"}} {
			if got := validator.Validate(body) == nil; got != predicate.Eval(model.Input{HasBody: true, Body: body}) {
				t.Fatalf("range semantics differ on %v", body)
			}
		}
	}
}

func TestPrimitiveRepairPreservesStructuralAssertions(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"child"}, "properties": map[string]any{"child": map[string]any{"type": "string"}}, "additionalProperties": false, "allOf": []any{map[string]any{"type": "object"}}}
	if !RelaxValueSchema(schema, nil) {
		t.Fatal("nullable witness made no correction")
	}
	d := &spec.Document{Raw: map[string]any{}}
	validator, err := d.Compile(schema)
	if err != nil {
		t.Fatal(err)
	}
	if validator.Validate(nil) != nil {
		t.Fatal("confirmed null still rejected")
	}
	if validator.Validate(map[string]any{}) == nil {
		t.Fatal("unrelated child requiredness was removed")
	}
	if validator.Validate(map[string]any{"child": "x", "extra": 1}) == nil {
		t.Fatal("additionalProperties was relaxed")
	}
	id := map[string]any{"type": "string", "minLength": 1}
	if !RelaxValueSchema(id, "") || !model.Equal(id["minLength"], 0) {
		t.Fatal("empty string witness did not correct minLength")
	}
}

func TestExporterMarksAcceptedRejectionsUnresolved(t *testing.T) {
	raw := map[string]any{"openapi": "3.1.0", "info": map[string]any{"title": "Fixture", "version": "1"}, "paths": map[string]any{"/x": map[string]any{"post": map[string]any{"requestBody": map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object"}}}}, "responses": map[string]any{"200": map[string]any{"description": "OK"}}}}}}
	d, err := spec.FromMap(raw)
	if err != nil {
		t.Fatal(err)
	}
	r := model.Report{State: "complete", Coverage: []model.Coverage{{Operation: "POST /x", State: "complete"}}}
	obs := []model.Observation{{ID: "rejected", Operation: "POST /x", Phase: "probe", Outcome: "input-rejected", Status: 400, Input: model.Input{HasBody: true, Body: map[string]any{"value": "bad"}}}}
	if err = Save(t.TempDir(), d, &r, obs, nil); err != nil {
		t.Fatal(err)
	}
	if r.State != "partial" || r.Coverage[0].State != "partial" {
		t.Fatal("known false acceptance did not block completion")
	}
}
