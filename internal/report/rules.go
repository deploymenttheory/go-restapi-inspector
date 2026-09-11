package report

import (
	"fmt"
	"sort"
	"strings"

	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
)

func ruleView(r model.Rule, all []model.Rule) Rule {
	fields := r.Fields()
	if r.Field != "" {
		fields = unique(append(fields, r.Field))
	}
	summary := predicate(r.Assert)
	if r.When != nil {
		summary = "When " + predicate(*r.When) + ", " + summary
	}
	labels := map[string]string{"fieldIsOptional": "Omittable in a recorded valid context", "fieldIsRequired": "Required in sampled inputs", "fieldAcceptsValue": "Repeatedly accepts " + pretty(r.Value), "fieldOmissionClears": "Omission clears the stored value", "fieldOmissionPreserves": "Omission preserves the stored value", "fieldOmissionResets": "Omission resets the stored value", "fieldOmissionRemoves": "Omission removes the stored field", "fieldIsCoerced": "Accepted input is converted to another type", "fieldIsNormalized": "Accepted input is normalized on storage", "fieldIsMutable": "Stored value changes after update", "fieldIsServerGenerated": "Omission produces varying server-generated values", "fieldObservedDefault": "Observed default: " + pretty(r.Value), "fieldAcceptedWithoutObservableEffect": "Accepted input has no observed effect on the read representation", "fieldIsRequiredForCreateOnly": "Required for create in the compared contexts", "fieldIsRequiredForUpdateOnly": "Required for update in the compared contexts"}
	if label, ok := labels[r.Kind]; ok {
		summary = label
	}
	trialLabel := fmt.Sprintf("%d matched confirmation trials", r.Trials)
	if r.Scope["responsePointer"] != "" || r.Kind == "fieldObservedDefault" || r.Kind == "fieldIsServerGenerated" {
		trialLabel = fmt.Sprintf("%d readback observations", r.Trials)
	}
	if r.Trials == 0 {
		trialLabel = "Not confirmed"
	}
	implied := false
	if r.When == nil && r.Assert.Op == "any" {
		for _, a := range r.Assert.Args {
			if a.Op != "present" {
				continue
			}
			for _, other := range all {
				if other.Status == "supported" && other.Operation == r.Operation && other.When == nil && other.Assert.Op == "present" && other.Assert.Field == a.Field {
					implied = true
				}
			}
		}
	}
	relation := ""
	if len(fields) > 1 {
		relation = predicateLabel(r.Assert.Op)
		if r.When != nil {
			relation = "IF / THEN"
		}
	}
	var condition any
	if r.When != nil {
		condition = canonicalPredicate(*r.When)
	}
	scope := map[string]string{}
	for k, v := range r.Scope {
		if k != "meaning" {
			scope[k] = v
		}
	}
	semantic := model.ID(r.Operation, r.Kind, r.Field, condition, canonicalPredicate(r.Assert), canonicalValue(r.Value), scope)
	family := model.ID(r.Operation, r.Kind, r.Field, condition, r.Assert.Op, r.Assert.Field, r.Assert.Other, scope)
	return Rule{ID: r.ID, Operation: r.Operation, Field: r.Field, Fields: fields, Kind: r.Kind, Status: r.Status, Summary: summary, Trials: trialLabel, Evidence: unique(append(append([]string{}, r.Accepted...), r.Rejected...)), JSON: pretty(r), Implied: implied, Relation: relation, semantic: semantic, family: family}
}

func canonicalPredicate(p model.Predicate) any {
	args := []any{}
	for _, child := range p.Args {
		args = append(args, canonicalPredicate(child))
	}
	switch p.Op {
	case "all", "any", "one", "atMostOne", "allOrNone":
		sort.Slice(args, func(i, j int) bool { return model.ID(args[i]) < model.ID(args[j]) })
	}
	return []any{p.Op, p.Field, p.Other, canonicalValue(p.Value), args}
}

func canonicalValue(v any) any {
	if n, ok := model.Number(v); ok {
		return []any{"number", n.RatString()}
	}
	switch x := v.(type) {
	case []any:
		out := []any{}
		for _, value := range x {
			out = append(out, canonicalValue(value))
		}
		return []any{"array", out}
	case map[string]any:
		out := map[string]any{}
		for key, value := range x {
			out[key] = canonicalValue(value)
		}
		return []any{"object", out}
	default:
		return []any{model.Type(v), v}
	}
}
func predicate(p model.Predicate) string {
	f := strings.TrimPrefix(p.Field, "body:")
	switch p.Op {
	case "present":
		return f + " is present"
	case "absent":
		return f + " is omitted"
	case "null":
		return f + " is null"
	case "type":
		return f + " has type " + fmt.Sprint(p.Value)
	case "notType":
		return f + " rejects type " + fmt.Sprint(p.Value)
	case "true":
		return "No additional input constraint"
	case "eq", "neq":
		return f + " " + map[string]string{"eq": "equals", "neq": "does not equal"}[p.Op] + " " + pretty(p.Value)
	case "not":
		if len(p.Args) == 1 {
			return "NOT (" + predicate(p.Args[0]) + ")"
		}
	case "all", "any", "one", "atMostOne", "allOrNone":
		args := []string{}
		for _, a := range p.Args {
			args = append(args, predicate(a))
		}
		return predicateLabel(p.Op) + " (" + strings.Join(args, "; ") + ")"
	}
	if p.Other != "" {
		return f + " " + p.Op + " " + p.Other
	}
	return f + " " + p.Op + " " + pretty(p.Value)
}

func predicateLabel(op string) string {
	labels := map[string]string{"any": "OR", "all": "AND", "one": "EXACTLY ONE", "atMostOne": "AT MOST ONE", "allOrNone": "ALL OR NONE"}
	if label, ok := labels[op]; ok {
		return label
	}
	return strings.ToUpper(op)
}
