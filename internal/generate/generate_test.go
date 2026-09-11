package generate

import (
	"context"
	"testing"

	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
)

func TestCoverContainsEveryInteraction(t *testing.T) {
	ctx := context.Background()
	sizes := []int{2, 3, 2, 4, 2, 3}
	for strength := 1; strength <= 3; strength++ {
		rows, err := Cover(ctx, sizes, strength, 100000)
		if err != nil {
			t.Fatal(err)
		}
		for _, scope := range Combinations(len(sizes), strength) {
			sub := make([]int, len(scope))
			for i, k := range scope {
				sub[i] = sizes[k]
			}
			err = Cartesian(ctx, sub, func(tuple []int) error {
				for _, row := range rows {
					match := true
					for i, k := range scope {
						if row[k] != tuple[i] {
							match = false
							break
						}
					}
					if match {
						return nil
					}
				}
				t.Fatalf("strength %d missing tuple %v on %v", strength, tuple, scope)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
}
func TestNestedMaterializationPreservesAbsence(t *testing.T) {
	base := model.Input{HasBody: true, Body: map[string]any{"outer": map[string]any{"inner": "v"}}}
	ds := []model.Domain{{Field: model.Field{ID: "body:/outer", In: "body", Pointer: "/outer"}, States: []model.State{{}, {Present: true, Value: nil}, {Present: true, Value: map[string]any{"inner": "v"}}}}, {Field: model.Field{ID: "body:/outer/inner", In: "body", Pointer: "/outer/inner"}, States: []model.State{{}, {Present: true, Value: "v"}}}}
	if _, err := Materialize(base, ds, []int{0, 1}); err == nil {
		t.Fatal("invented a parent for an impossible assignment")
	}
	in, err := Materialize(base, ds, []int{0, 0})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := in.Get("body:/outer"); exists {
		t.Fatal("omitted parent was reinserted")
	}
	in, err = Materialize(base, ds, []int{1, 0})
	if err != nil {
		t.Fatal(err)
	}
	if v, exists := in.Get("body:/outer"); !exists || v != nil {
		t.Fatal("null was confused with omission")
	}
}
func TestDeltaDebuggingFindsCause(t *testing.T) {
	base := model.Input{HasBody: true, Body: map[string]any{"a": true, "b": true, "c": true}}
	failed := model.Input{HasBody: true, Body: map[string]any{}}
	out, err := Minimize(context.Background(), base, failed, []string{"body:/a", "body:/b", "body:/c"}, func(in model.Input) (bool, error) {
		_, a := in.Get("body:/a")
		_, b := in.Get("body:/b")
		return !a && !b, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Get("body:/c"); !ok {
		t.Fatal("irrelevant c removal was not minimized")
	}
}
