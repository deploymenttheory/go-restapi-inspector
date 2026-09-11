package engine

import (
	"context"

	"github.com/deploymenttheory/go-restapi-inspector/internal/exporter"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
	"github.com/deploymenttheory/go-restapi-inspector/internal/spec"
)

func (r *Runner) confirmSchemaRepairs(ctx context.Context, op model.Operation, base model.Input, observations []model.Observation) ([]model.Rule, error) {
	var rules []model.Rule
	for _, field := range op.Fields {
		if field.In != "body" || r.Client.Redactor.Sensitive(field.ID) {
			continue
		}
		working := model.Clone(field.Schema)
		spec.Normalize30(working)
		for _, observation := range observations {
			if observation.Outcome != "accepted" {
				continue
			}
			value, present := observation.Input.Get(field.ID)
			if !present {
				continue
			}
			proposed := model.Clone(working)
			if !exporter.RelaxValueSchema(proposed, value) {
				continue
			}
			pairs, err := r.confirmPair(ctx, op, base, observation.Input, "accepted")
			if err != nil {
				return rules, err
			}
			rule := model.Rule{Operation: op.Key, Kind: "fieldAcceptsValue", Field: field.ID, Assert: model.Predicate{Op: "true"}, Value: value, Status: "supported", Trials: len(pairs), Scope: map[string]string{"meaning": "repeated acceptance falsifies a primitive declared assertion in this observed context"}}
			for _, pair := range pairs {
				rule.Accepted = append(rule.Accepted, pair.rejected.ID)
			}
			// Values distinguish separate acceptance witnesses for the same field.
			rule.ID = model.ID(op.Key, rule.Kind, field.ID, value)
			rules = append(rules, rule)
			working = proposed
		}
	}
	return rules, nil
}
