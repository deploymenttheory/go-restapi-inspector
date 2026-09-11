package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"

	"github.com/deploymenttheory/go-restapi-inspector/internal/generate"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
	"github.com/deploymenttheory/go-restapi-inspector/internal/spec"
)

// discoverBounds brackets integer transitions, then challenges their fractional
// neighbours. The rules describe sampled boundaries, not global monotonicity.
func (r *Runner) discoverBounds(ctx context.Context, op model.Operation, base model.Input, domains *[]model.Domain, known func() []model.Observation, visit func(model.Input) (model.Observation, error)) (rules []model.Rule, err error) {
	if r.Config.BoundarySteps == 0 {
		return nil, nil
	}
	for index := range *domains {
		field := (*domains)[index].Field
		typ := spec.Str(spec.Shape(field.Schema)["type"])
		if generate.Bound(field) || (typ != "integer" && typ != "number") {
			continue
		}
		if _, ok := r.Config.Domains[field.ID]; ok {
			continue
		}
		if _, ok := r.Config.Domains[op.Key+"#"+field.ID]; ok {
			continue
		}
		value, present := base.Get(field.ID)
		n, ok := model.Number(value)
		if !present || !ok || !n.IsInt() || !n.Num().IsInt64() {
			continue
		}
		start := n.Num().Int64()
		if start < -(1<<40) || start > 1<<40 {
			continue
		}
		probe := func(value any) (bool, error) {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			input := model.Clone(base)
			if err := input.Set(field.ID, model.State{Present: true, Value: value}); err != nil {
				return false, err
			}
			observation, err := visit(input)
			if err != nil {
				return false, err
			}
			appendState(&(*domains)[index], model.State{Present: true, Value: value})
			if observation.Outcome == "inconclusive" {
				return false, fmt.Errorf("boundary search for %s was inconclusive: %s", field.ID, observation.Reason)
			}
			return observation.Outcome == "accepted", nil
		}
		integerBounds := map[string]any{}
		acceptsIntegerString := false
		for _, observation := range known() {
			v, _ := observation.Input.Get(field.ID)
			if text, ok := v.(string); ok && observation.Outcome == "accepted" {
				_, valid := model.IntegerString(text)
				acceptsIntegerString = acceptsIntegerString || valid
			}
		}
		for _, direction := range []int64{-1, 1} {
			good, bad, bracket := start, int64(0), false
			for _, observation := range known() {
				v, has := observation.Input.Get(field.ID)
				q, numeric := model.Number(v)
				if !has || !numeric || !q.IsInt() || !q.Num().IsInt64() || observation.Outcome != "input-rejected" {
					continue
				}
				candidate := q.Num().Int64()
				if (candidate-start)*direction <= 0 {
					continue
				}
				copy := model.Clone(observation.Input)
				_ = copy.Set(field.ID, model.State{Present: true, Value: value})
				if model.ID(copy) != model.ID(base) {
					continue
				}
				if !bracket || (candidate-start)*direction < (bad-start)*direction {
					bad, bracket = candidate, true
				}
			}
			for step := 0; !bracket && step < r.Config.BoundarySteps; step++ {
				candidate := start + direction*(int64(1)<<step)
				accepted, e := probe(json.Number(strconv.FormatInt(candidate, 10)))
				if e != nil {
					return rules, e
				}
				if accepted {
					good = candidate
				} else {
					bad, bracket = candidate, true
				}
			}
			if !bracket {
				continue
			}
			for distance := good - bad; distance > 1 || distance < -1; distance = good - bad {
				mid := bad + (good-bad)/2
				accepted, e := probe(json.Number(strconv.FormatInt(mid, 10)))
				if e != nil {
					return rules, e
				}
				if accepted {
					good = mid
				} else {
					bad = mid
				}
			}
			acceptedOutside := true
			for _, fraction := range []float64{0.5, 0.999} {
				v := float64(good) + float64(direction)*fraction
				accepted, e := probe(json.Number(strconv.FormatFloat(v, 'f', 3, 64)))
				if e != nil {
					return rules, e
				}
				acceptedOutside = acceptedOutside && accepted
			}
			// An interior fractional witness distinguishes a numeric bound from
			// rejection of fractional values everywhere in an integer field.
			if _, e := probe(json.Number(strconv.FormatFloat(float64(good)-float64(direction)*0.001, 'f', 3, 64))); e != nil {
				return rules, e
			}
			predicate := model.Predicate{Field: field.ID}
			if direction < 0 {
				predicate.Op = "minimum"
			} else {
				predicate.Op = "maximum"
			}
			bound := good
			if acceptedOutside {
				bound = bad
				if direction < 0 {
					predicate.Op = "exclusiveMinimum"
				} else {
					predicate.Op = "exclusiveMaximum"
				}
			}
			predicate.Value = json.Number(strconv.FormatInt(bound, 10))
			key := "minimum"
			if direction > 0 {
				key = "maximum"
			}
			integerBounds[key] = json.Number(strconv.FormatInt(good, 10))
			if acceptsIntegerString {
				for _, integer := range []int64{good, bad} {
					if _, e := probe(strconv.FormatInt(integer, 10)); e != nil {
						return rules, e
					}
				}
			}
			rule := model.Rule{Operation: op.Key, Kind: "fieldConstraint", Field: field.ID, Assert: predicate, Status: "hypothesis", Scope: map[string]string{"basis": "bounded transition search with fractional neighbours", "acceptedInteger": strconv.FormatInt(good, 10), "rejectedInteger": strconv.FormatInt(bad, 10), "integerBracketWidth": fmt.Sprint(int64(math.Abs(float64(good - bad))))}}
			rule.Identify()
			rules = append(rules, rule)
		}
		if acceptsIntegerString && len(integerBounds) == 2 {
			rule := model.Rule{Operation: op.Key, Kind: "fieldConstraint", Field: field.ID, Assert: model.Predicate{Op: "integerStringRange", Field: field.ID, Value: integerBounds}, Status: "hypothesis", Scope: map[string]string{"basis": "integer-string boundary challenges"}}
			rule.Identify()
			rules = append(rules, rule)
		}
	}
	return rules, nil
}

func appendState(domain *model.Domain, state model.State) {
	for _, existing := range domain.States {
		if existing.Present == state.Present && model.Equal(existing.Value, state.Value) {
			return
		}
	}
	domain.States = append(domain.States, state)
}
