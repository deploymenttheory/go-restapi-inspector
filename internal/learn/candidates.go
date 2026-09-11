// Package learn performs constraint acquisition over explicit finite domains.
// A rule is a hypothesis until experiments distinguish it from alternatives.
package learn

import (
	"fmt"
	"sort"
	"strings"

	"github.com/deploymenttheory/go-restapi-inspector/internal/config"
	"github.com/deploymenttheory/go-restapi-inspector/internal/generate"
	"github.com/deploymenttheory/go-restapi-inspector/internal/journal"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
	"github.com/deploymenttheory/go-restapi-inspector/internal/spec"
)

func Candidates(op model.Operation, domains []model.Domain, c config.Config) ([]model.Rule, error) {
	var out []model.Rule
	seen := map[string]bool{}
	add := func(kind, field string, when *model.Predicate, p model.Predicate) error {
		r := model.Rule{Operation: op.Key, Kind: kind, Field: field, When: when, Assert: p, Status: "hypothesis"}
		r.Identify()
		if !seen[r.ID] {
			if len(out) >= c.MaxPlanningTuples {
				return fmt.Errorf("candidate count exceeds max-planning-tuples=%d", c.MaxPlanningTuples)
			}
			seen[r.ID] = true
			out = append(out, r)
		}
		return nil
	}
	literals := make([][]model.Predicate, len(domains))
	redactor := journal.NewRedactor(c.SensitiveFields)
	for i, d := range domains {
		id := d.Field.ID
		literals[i] = []model.Predicate{{Op: "present", Field: id}, {Op: "absent", Field: id}}
		for _, s := range d.States {
			if s.Present && s.Value != nil && model.Type(s.Value) != "object" && model.Type(s.Value) != "array" && !generate.Bound(d.Field) && !redactor.Sensitive(d.Field.ID) {
				p := model.Predicate{Op: "eq", Field: id, Value: s.Value}
				duplicate := false
				for _, v := range literals[i] {
					if model.ID(v) == model.ID(p) {
						duplicate = true
					}
				}
				if !duplicate {
					literals[i] = append(literals[i], p)
				}
			}
		}
		for _, a := range []struct{ kind, op string }{{"fieldIsRequired", "present"}, {"fieldIsForbidden", "absent"}} {
			if e := add(a.kind, id, nil, model.Predicate{Op: a.op, Field: id}); e != nil {
				return nil, e
			}
		}
		if generate.Bound(d.Field) {
			continue
		}
		s := spec.Shape(d.Field.Schema)
		if s["type"] == "integer" || s["type"] == "number" {
			if e := add("fieldConstraint", id, nil, model.Predicate{Op: "pattern", Field: id, Value: model.IntegerStringPattern}); e != nil {
				return nil, e
			}
		}
		types := []string{"string", "integer", "number", "boolean", "object", "array", "null"}
		for _, t := range types {
			if e := add("fieldType", id, nil, model.Predicate{Op: "notType", Field: id, Value: t}); e != nil {
				return nil, e
			}
			p := model.Predicate{Op: "type", Field: id, Value: t}
			if e := add("fieldType", id, nil, p); e != nil {
				return nil, e
			}
			if t != "null" {
				nullable := model.Predicate{Op: "any", Args: []model.Predicate{p, {Op: "null", Field: id}}}
				if e := add("fieldType", id, nil, nullable); e != nil {
					return nil, e
				}
			}
		}
		// Bounds, patterns and enum membership remain falsifiable input claims.
		// Successful examples never manufacture a closed enum.
		for _, key := range []string{"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "minLength", "maxLength", "minItems", "maxItems", "enum", "pattern", "format"} {
			if v, ok := s[key]; ok {
				if key == "format" && !knownFormat(fmt.Sprint(v)) {
					continue
				}
				if e := add("fieldConstraint", id, nil, model.Predicate{Op: key, Field: id, Value: v}); e != nil {
					return nil, e
				}
			}
		}
		for _, a := range []struct {
			kind string
			p    model.Predicate
		}{{"fieldRejectsNull", model.Predicate{Op: "not", Args: []model.Predicate{{Op: "null", Field: id}}}}, {"fieldRejectsEmpty", model.Predicate{Op: "not", Args: []model.Predicate{{Op: "eq", Field: id, Value: ""}}}}} {
			if e := add(a.kind, id, nil, a.p); e != nil {
				return nil, e
			}
		}
		if e := add("fieldRejectsBlank", id, nil, model.Predicate{Op: "pattern", Field: id, Value: `\S`}); e != nil {
			return nil, e
		}
	}
	for target, d := range domains {
		var controllers []int
		for i := range domains {
			if i != target && !related(d.Field, domains[i].Field) {
				controllers = append(controllers, i)
			}
		}
		for size := 1; size < c.InteractionOrder && size <= len(controllers); size++ {
			if err := generate.CheckCombinations(len(controllers), size, c.MaxPlanningTuples); err != nil {
				return nil, err
			}
			for _, comb := range generate.Combinations(len(controllers), size) {
				sizes := make([]int, size)
				for i, k := range comb {
					sizes[i] = len(literals[controllers[k]])
				}
				rows, err := literalRows(sizes, c.MaxPlanningTuples)
				if err != nil {
					return nil, err
				}
				for _, row := range rows {
					args := make([]model.Predicate, size)
					valueCondition := false
					for i, k := range comb {
						args[i] = literals[controllers[k]][row[i]]
						if args[i].Op != "present" {
							valueCondition = true
						}
					}
					when := model.Predicate{Op: "all", Args: args}
					if len(args) == 1 {
						when = args[0]
					}
					kind := "fieldRequiredWith"
					if valueCondition {
						kind = "fieldRequiredWhenFieldValueIs"
					}
					if e := add(kind, d.Field.ID, &when, model.Predicate{Op: "present", Field: d.Field.ID}); e != nil {
						return nil, e
					}
					if e := add("fieldBlockedWhen", d.Field.ID, &when, model.Predicate{Op: "absent", Field: d.Field.ID}); e != nil {
						return nil, e
					}
				}
			}
		}
	}
	for size := 2; size <= c.InteractionOrder && size <= len(domains); size++ {
		if err := generate.CheckCombinations(len(domains), size, c.MaxPlanningTuples); err != nil {
			return nil, err
		}
		for _, comb := range generate.Combinations(len(domains), size) {
			args := make([]model.Predicate, size)
			overlap := false
			for i, k := range comb {
				args[i] = model.Predicate{Op: "present", Field: domains[k].Field.ID}
				for _, j := range comb[:i] {
					overlap = overlap || related(domains[k].Field, domains[j].Field)
				}
			}
			if overlap {
				continue
			}
			for _, kind := range []string{"any", "one", "atMostOne", "allOrNone"} {
				if e := add(kind, "", nil, model.Predicate{Op: kind, Args: args}); e != nil {
					return nil, e
				}
			}
		}
	}
	for i, a := range domains {
		for _, b := range domains[i+1:] {
			ta := spec.Str(spec.Shape(a.Field.Schema)["type"])
			tb := spec.Str(spec.Shape(b.Field.Schema)["type"])
			numeric := func(t string) bool { return t == "integer" || t == "number" }
			dates := spec.Str(a.Field.Schema["format"]) == "date-time" && spec.Str(b.Field.Schema["format"]) == "date-time"
			if !generate.Bound(a.Field) && !generate.Bound(b.Field) && (numeric(ta) && numeric(tb) || dates) {
				for _, op := range []string{"le", "lt", "ge", "gt", "equalFields"} {
					if e := add("fieldRelation", a.Field.ID, nil, model.Predicate{Op: op, Field: a.Field.ID, Other: b.Field.ID}); e != nil {
						return nil, e
					}
				}
			}
		}
	}
	for _, r := range c.Rules {
		if r.Operation != op.Key && r.Operation != op.ID {
			continue
		}
		r.Operation = op.Key
		r.Status = "hypothesis"
		r.Identify()
		if !seen[r.ID] {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func related(a, b model.Field) bool {
	return a.In == "body" && b.In == "body" && (strings.HasPrefix(a.Pointer, b.Pointer+"/") || strings.HasPrefix(b.Pointer, a.Pointer+"/"))
}
func knownFormat(s string) bool {
	switch s {
	case "date", "date-time", "uuid", "email", "uri", "url", "ipv4", "ipv6":
		return true
	}
	return false
}
func literalRows(sizes []int, limit int) ([][]int, error) {
	rows := [][]int{{}}
	for _, n := range sizes {
		if n > 0 && len(rows) > limit/n {
			return nil, fmt.Errorf("candidate literal product exceeds planning guard")
		}
		var next [][]int
		for _, r := range rows {
			for v := 0; v < n; v++ {
				next = append(next, append(append([]int{}, r...), v))
			}
		}
		rows = next
	}
	return rows, nil
}
