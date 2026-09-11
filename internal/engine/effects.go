package engine

import (
	"github.com/deploymenttheory/go-restapi-inspector/internal/graph"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
)

type Effect struct {
	Operation      string      `json:"operation"`
	Experiment     string      `json:"experiment"`
	ReadEvidence   string      `json:"readEvidence"`
	BeforeEvidence string      `json:"beforeEvidence,omitempty"`
	Input          model.Input `json:"input"`
	Before         any         `json:"before,omitempty"`
	After          any         `json:"after"`
}

func (r *Runner) effectRules() []model.Rule {
	var out []model.Rule
	for _, op := range r.Plan.Operations {
		for _, field := range op.Fields {
			if field.In != "body" || r.Client.Redactor.Sensitive(field.ID) {
				continue
			}
			pointer := field.Pointer
			if configured, ok := r.Config.Hint(op).Observe[field.ID]; ok {
				pointer = configured
			}
			groups := map[string][]Effect{}
			var defaults []any
			for _, effect := range r.Effects {
				if effect.Operation != op.Key {
					continue
				}
				sent, present := effect.Input.Get(field.ID)
				after, observed := model.Get(effect.After, pointer)
				before, hadBefore := model.Get(effect.Before, pointer)
				role := graph.Role(op, r.Config)
				if !present && observed && role == "create" {
					groups["fieldObservedDefault"] = append(groups["fieldObservedDefault"], effect)
					defaults = append(defaults, after)
					continue
				}
				if !present && hadBefore && role == "update" {
					kind := "fieldOmissionResets"
					switch {
					case !observed:
						kind = "fieldOmissionRemoves"
					case model.Equal(before, after):
						kind = "fieldOmissionPreserves"
					case after == nil || model.Equal(after, "") || model.Equal(after, []any{}) || model.Equal(after, map[string]any{}):
						kind = "fieldOmissionClears"
					}
					groups[kind] = append(groups[kind], effect)
					continue
				}
				if !present || !observed {
					continue
				}
				if role == "update" && !hadBefore {
					continue
				}
				if role == "update" && model.Equal(before, sent) {
					continue
				}
				kind := ""
				if model.Equal(sent, after) {
					if role == "update" {
						kind = "fieldIsMutable"
					}
				} else if role == "update" && model.Equal(before, after) {
					kind = "fieldAcceptedWithoutObservableEffect"
				} else if model.Type(sent) == model.Type(after) {
					kind = "fieldIsNormalized"
				} else {
					kind = "fieldIsCoerced"
				}
				if kind != "" {
					groups[kind] = append(groups[kind], effect)
				}
			}
			for kind, effects := range groups {
				if len(effects) < r.Config.ValidationTrials {
					continue
				}
				rule := model.Rule{Operation: op.Key, Kind: kind, Field: field.ID, Assert: model.Predicate{Op: "true"}, Status: "supported", Trials: len(effects), Scope: map[string]string{"responsePointer": pointer, "state": graph.Role(op, r.Config), "meaning": "read-after-write observations under fresh fixtures"}}
				if kind == "fieldObservedDefault" {
					stable := true
					for _, v := range defaults[1:] {
						if !model.Equal(v, defaults[0]) {
							stable = false
							break
						}
					}
					if stable {
						rule.Value = defaults[0]
					} else {
						rule.Kind = "fieldIsServerGenerated"
						rule.Notes = []string{"Omitted input produced varying observed values; the generation mechanism is unknown."}
					}
				}
				if kind == "fieldAcceptedWithoutObservableEffect" {
					rule.Notes = []string{"The read representation did not change; this does not prove that the server ignored the field internally."}
				}
				for _, e := range effects {
					rule.Accepted = append(rule.Accepted, e.Experiment, e.ReadEvidence)
					if e.BeforeEvidence != "" {
						rule.Accepted = append(rule.Accepted, e.BeforeEvidence)
					}
				}
				rule.Identify()
				out = append(out, rule)
			}
		}
	}
	return out
}
