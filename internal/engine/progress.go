package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/deploymenttheory/go-restapi-inspector/internal/config"
	"github.com/deploymenttheory/go-restapi-inspector/internal/graph"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
)

type operationProgress struct {
	Operation   string              `json:"operation"`
	Signature   string              `json:"signature"`
	Experiments []model.Observation `json:"-"`
}

var ErrConfirmationReserve = errors.New("exploration stopped to reserve requests for confirmation; resume with a larger request budget")

func (r *Runner) explorationBudget(op model.Operation) error {
	if r.Config.MaxRequests == 0 || r.Config.ConfirmationReserve < 0 {
		return nil
	}
	remaining := r.Config.MaxRequests
	for phase, count := range r.Client.Counts() {
		if phase != "cleanup" {
			remaining -= count
		}
	}
	reserve := r.Config.ConfirmationReserve
	if reserve == 0 {
		reserve = min(r.Config.MaxRequests/4, 2*r.Config.ValidationTrials*len(op.Fields)*r.experimentCost(op))
	}
	if remaining < reserve+2*r.experimentCost(op) {
		return ErrConfirmationReserve
	}
	return nil
}

// progressFor reuses classified logical inputs only when the experiment
// configuration is unchanged. Every new write still receives fresh fixtures.
func (r *Runner) progressFor(op model.Operation) (*operationProgress, error) {
	signature := observationSignature(r.Config, r.Document.Hash, op.Key)
	if r.Progress == nil {
		r.Progress = map[string]*operationProgress{}
	}
	p := r.Progress[op.Key]
	if p == nil || p.Signature != signature {
		var effects []Effect
		for _, effect := range r.Effects {
			if effect.Operation != op.Key {
				effects = append(effects, effect)
			}
		}
		r.Effects = effects
		p = &operationProgress{Operation: op.Key, Signature: signature}
		if err := r.Journal.Append("operation-progress", p); err != nil {
			return nil, err
		}
		r.Progress[op.Key] = p
	}
	return p, nil
}

func observationSignature(c config.Config, hash, operation string) string {
	if len(c.AuthProfiles) == 0 {
		c.AuthProfiles = nil
	}
	if len(c.Security) == 0 {
		c.Security = nil
	}
	if len(c.Hints) == 0 {
		c.Hints = nil
	}
	if len(c.SensitiveFields) == 0 {
		c.SensitiveFields = nil
	}
	return model.ID("observation-context-v1", hash, operation, c.BaseURL, c.Auth, c.AuthProfiles, c.Security, c.Hints, c.Oracle, c.TLS, c.SensitiveFields)
}

func (r *Runner) planSignature(operation string) string {
	c := r.Config
	return model.ID("acquisition-v2", observationSignature(c, r.Document.Hash, operation), c.Mode, c.Strategy, c.InteractionOrder, c.ValidationTrials, c.BoundarySteps, c.Domains, c.Rules)
}

func reusable(o model.Observation) bool {
	b, err := json.Marshal(o.Input)
	return err == nil && !strings.Contains(string(b), "[REDACTED]") && (o.Outcome == "accepted" || o.Outcome == "input-rejected")
}

// confirmPair counts complete, independent control/experiment pairs. A crash
// between the two requests cannot count as a completed confirmation trial.
func (r *Runner) confirmPair(ctx context.Context, op model.Operation, good, trial model.Input, expected string) ([]contrast, error) {
	ctx = model.WithProbe(ctx, "confirmation", good, model.ID(op.Key, good, trial))
	p := r.Progress[op.Key]
	var pairs []contrast
	controls := map[string]model.Observation{}
	if p != nil {
		for _, o := range p.Experiments {
			if !reusable(o) {
				continue
			}
			if o.Phase == "validation-control" && model.ID(o.Input) == model.ID(good) {
				controls[o.ID] = o
			}
			if o.Phase != "validation" || model.ID(o.Input) != model.ID(trial) {
				continue
			}
			if control, ok := controls[o.Control]; ok {
				delete(controls, o.Control)
				if control.Outcome != "accepted" || o.Outcome != expected {
					return pairs, fmt.Errorf("confirmation contradicted the expected %s outcome", expected)
				}
				pairs = append(pairs, contrast{accepted: control, rejected: o})
			}
		}
	}
	for len(pairs) < r.Config.ValidationTrials {
		control, err := r.experiment(ctx, op, good, "validation-control", "")
		if err != nil {
			return pairs, err
		}
		if control.Outcome != "accepted" {
			return pairs, fmt.Errorf("confirmation control stopped succeeding: %s", control.Reason)
		}
		observation, err := r.experiment(ctx, op, trial, "validation", control.ID)
		if err != nil {
			return pairs, err
		}
		if observation.Outcome != expected {
			return pairs, fmt.Errorf("confirmation expected %s, observed %s: %s", expected, observation.Outcome, observation.Reason)
		}
		pairs = append(pairs, contrast{accepted: control, rejected: observation})
	}
	return pairs, nil
}

func (r *Runner) experimentCost(op model.Operation) int {
	cost := 1
	if r.Plan.Companion(op, "GET", r.Config) != "" {
		cost++
	}
	if graph.Role(op, r.Config) == "update" {
		cost++
	}
	seen := map[string]bool{}
	var setup func(string)
	setup = func(key string) {
		for _, edge := range r.Plan.Bindings(key) {
			if !seen[edge.Producer] {
				seen[edge.Producer] = true
				cost++
				setup(edge.Producer)
			}
		}
	}
	setup(op.Key)
	return cost
}
