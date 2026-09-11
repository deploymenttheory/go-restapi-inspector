package learn

import (
	"context"
	"errors"
	"testing"

	"github.com/deploymenttheory/go-restapi-inspector/internal/config"
	"github.com/deploymenttheory/go-restapi-inspector/internal/generate"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
)

func boolSpace(n int) (model.Operation, model.Input, []model.Domain) {
	op := model.Operation{Key: "POST /test"}
	base := model.Input{HasBody: true, Body: map[string]any{}}
	var ds []model.Domain
	for i := 0; i < n; i++ {
		name := string(rune('a' + i))
		f := model.Field{ID: "body:/" + name, In: "body", Name: name, Pointer: "/" + name, Schema: map[string]any{"type": "boolean"}}
		op.Fields = append(op.Fields, f)
		ds = append(ds, model.Domain{Field: f, States: []model.State{{}, {Present: true, Value: true}}})
	}
	return op, base, ds
}
func TestActiveLearnerTruthTable(t *testing.T) {
	ctx := context.Background()
	op, base, domains := boolSpace(3)
	c := config.Defaults()
	c.InteractionOrder = 3
	candidates, err := Candidates(op, domains, c)
	if err != nil {
		t.Fatal(err)
	}
	l := New(candidates)
	truth := model.Rule{When: &model.Predicate{Op: "present", Field: "body:/a"}, Assert: model.Predicate{Op: "present", Field: "body:/b"}}
	for queries := 0; ; queries++ {
		if queries > 8 {
			t.Fatal("active learner repeated classified assignments")
		}
		row, found, err := l.Witness(ctx, base, domains, nil, 100000)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			break
		}
		input, err := generate.Materialize(base, domains, row)
		if err != nil {
			t.Fatal(err)
		}
		outcome := "input-rejected"
		if truth.Holds(input) {
			outcome = "accepted"
		}
		if err := l.Observe(model.Observation{Input: input, Outcome: outcome}); err != nil {
			t.Fatal(err)
		}
	}
	// Compare the learned network with an independent complete truth table.
	for _, row := range [][]int{{0, 0, 0}, {0, 0, 1}, {0, 1, 0}, {0, 1, 1}, {1, 0, 0}, {1, 0, 1}, {1, 1, 0}, {1, 1, 1}} {
		input, _ := generate.Materialize(base, domains, row)
		prediction := true
		for i, r := range l.Rules {
			if l.Survives(i) && !r.Holds(input) {
				prediction = false
			}
		}
		if prediction != truth.Holds(input) {
			t.Fatalf("wrong model on %v", row)
		}
	}
}
func TestConvergenceDoesNotImplyDomainCompleteness(t *testing.T) {
	ctx := context.Background()
	_, base, ds := boolSpace(3)
	// Restrict the language to signed clauses involving at most two fields.
	// Target is a OR b OR c; hide 001. The remaining seven observations admit
	// only the incorrect two-field model a OR b.
	var candidates []model.Rule
	fields := []string{"body:/a", "body:/b", "body:/c"}
	for size := 1; size <= 2; size++ {
		for _, comb := range generate.Combinations(3, size) {
			_ = generate.Cartesian(ctx, makeSizes(size, 2), func(sign []int) error {
				var args []model.Predicate
				for i, k := range comb {
					op := "present"
					if sign[i] == 1 {
						op = "absent"
					}
					args = append(args, model.Predicate{Op: op, Field: fields[k]})
				}
				candidates = append(candidates, model.Rule{Assert: model.Predicate{Op: "any", Args: args}})
				return nil
			})
		}
	}
	l := New(candidates)
	_ = generate.Cartesian(ctx, []int{2, 2, 2}, func(row []int) error {
		if row[0] == 0 && row[1] == 0 && row[2] == 1 {
			return nil
		}
		in, _ := generate.Materialize(base, ds, row)
		outcome := "accepted"
		if row[0]+row[1]+row[2] == 0 {
			outcome = "input-rejected"
		}
		if err := l.Observe(model.Observation{Input: in, Outcome: outcome}); err != nil {
			t.Fatal(err)
		}
		return nil
	})
	_, found, err := l.Witness(ctx, base, ds, nil, 100000)
	if err != nil || found {
		t.Fatalf("expected language convergence: found=%v err=%v", found, err)
	}
	in, _ := generate.Materialize(base, ds, []int{0, 0, 1})
	if err := l.Observe(model.Observation{Input: in, Outcome: "accepted"}); !errors.Is(err, ErrOutsideModel) {
		t.Fatalf("independent probe must expose missing grammar: %v", err)
	}
}
func makeSizes(n, value int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = value
	}
	return out
}
func TestInconclusiveDoesNotConstrainModels(t *testing.T) {
	_, base, ds := boolSpace(1)
	l := New([]model.Rule{{Assert: model.Predicate{Op: "present", Field: "body:/a"}}})
	if err := l.Observe(model.Observation{Input: base, Outcome: "inconclusive", Status: 401}); err != nil {
		t.Fatal(err)
	}
	_, found, err := l.Witness(context.Background(), base, ds, nil, 10000)
	if err != nil || !found {
		t.Fatalf("inconclusive evidence changed the model: %v %v", found, err)
	}
}
