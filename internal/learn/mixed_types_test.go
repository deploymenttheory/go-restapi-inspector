package learn

import (
	"context"
	"testing"

	"github.com/deploymenttheory/go-restapi-inspector/internal/config"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
)

func TestMixedScalarAcceptanceHasConsistentTypeModel(t *testing.T) {
	field := model.Field{ID: "body:/name", In: "body", Pointer: "/name", Schema: map[string]any{"type": "string"}}
	domain := model.Domain{Field: field, States: []model.State{{}, {Present: true, Value: nil}, {Present: true, Value: "name"}, {Present: true, Value: 9}, {Present: true, Value: 1.5}, {Present: true, Value: true}, {Present: true, Value: []any{}}, {Present: true, Value: map[string]any{}}}}
	op := model.Operation{Key: "POST /x", Fields: []model.Field{field}}
	rules, err := Candidates(op, []model.Domain{domain}, config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	learner := New(rules)
	base := model.Input{HasBody: true, Body: map[string]any{}}
	for _, state := range domain.States {
		input := model.Clone(base)
		_ = input.Set(field.ID, state)
		outcome := "accepted"
		if model.Type(state.Value) == "array" || model.Type(state.Value) == "object" {
			outcome = "input-rejected"
		}
		if err = learner.Observe(model.Observation{Input: input, Outcome: outcome}); err != nil {
			t.Fatal(err)
		}
	}
	if _, found, err := learner.Witness(context.Background(), base, []model.Domain{domain}, nil, 100000); err != nil || found {
		t.Fatalf("classified type domain did not converge: found=%v err=%v", found, err)
	}
}
