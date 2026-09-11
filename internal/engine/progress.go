package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/deploymenttheory/go-restapi-inspector/internal/config"
	"github.com/deploymenttheory/go-restapi-inspector/internal/graph"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
	"github.com/deploymenttheory/go-restapi-inspector/internal/spec"
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
	signature := r.observationSignature(op)
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
	op, _ := r.Plan.Find(operation)
	return acquisitionSignature(c, r.observationSignature(op), op)
}

func acquisitionSignature(c config.Config, signature string, op model.Operation) string {
	domains := map[string][]model.State{}
	for _, f := range op.Fields {
		if states, ok := c.Domains[f.ID]; ok {
			domains[f.ID] = states
		}
	}
	var rules []model.Rule
	for _, rule := range c.Rules {
		if rule.Operation == "" || rule.Operation == op.Key || rule.Operation == op.ID {
			rule.Operation = op.Identity()
			rule.Identify()
			rules = append(rules, rule)
		}
	}
	return model.ID("acquisition-v3", signature, c.Mode, c.Strategy, c.InteractionOrder, c.ValidationTrials, c.BoundarySteps, domains, rules)
}

// dependencyContext includes fixture producers, readback, polling and cleanup.
// The visited set makes companion cycles deterministic without recursion loops.
func (r *Runner) dependencyContext(op model.Operation) map[string]any {
	out := map[string]any{}
	var visit func(model.Operation)
	visit = func(op model.Operation) {
		id := op.Identity()
		if _, ok := out[id]; ok {
			return
		}
		out[id] = nil
		hint := r.Config.Hint(op)
		auth := r.Config.Auth
		profiles := map[string]config.Auth{}
		if hint.Auth != "" {
			profiles[hint.Auth] = r.Config.AuthProfiles[hint.Auth]
		} else {
			for _, requirement := range spec.Slice(op.Raw["security"]) {
				for scheme := range spec.Map(requirement) {
					if name := r.Config.Security[scheme]; name != "" {
						profiles[name] = r.Config.AuthProfiles[name]
					}
				}
			}
		}
		// Refresh timing does not change which principal is being observed.
		auth = authContext(auth)
		principals := map[string]string{}
		principal := func(a config.Auth) {
			for _, name := range []string{a.ClientIDEnv, a.UsernameEnv} {
				if name != "" {
					principals[name] = os.Getenv(name)
				}
			}
		}
		principal(auth)
		for name, a := range profiles {
			a = authContext(a)
			profiles[name] = a
			principal(a)
		}
		var edges []any
		for _, edge := range r.Plan.Bindings(op.Key) {
			producer, ok := r.Plan.Find(edge.Producer)
			if ok {
				edges = append(edges, []any{producer.Identity(), edge.Field, edge.Pointer, edge.Source})
				visit(producer)
			}
		}
		var companions []string
		for _, method := range []string{"GET", "DELETE"} {
			key := r.Plan.Companion(op, method, r.Config)
			if companion, ok := r.Plan.Find(key); ok {
				companions = append(companions, companion.Identity())
				visit(companion)
			}
		}
		if hint.Poll != nil {
			if poll, ok := r.Plan.Find(hint.Poll.Operation); ok {
				companions = append(companions, poll.Identity())
				visit(poll)
			}
		}
		contextHint := model.Clone(hint)
		for field, binding := range contextHint.Bindings {
			if target, ok := r.Plan.Find(binding.Operation); ok {
				binding.Operation = target.Identity()
				contextHint.Bindings[field] = binding
			}
		}
		if target, ok := r.Plan.Find(contextHint.Read); ok {
			contextHint.Read = target.Identity()
		}
		if target, ok := r.Plan.Find(contextHint.Delete); ok {
			contextHint.Delete = target.Identity()
		}
		if contextHint.Poll != nil {
			if target, ok := r.Plan.Find(contextHint.Poll.Operation); ok {
				contextHint.Poll.Operation = target.Identity()
			}
		}
		out[id] = []any{r.Fingerprints[id].Full, contextHint, auth, profiles, principals, edges, companions}
	}
	visit(op)
	return out
}

func authContext(a config.Auth) config.Auth {
	if a.Type == "" || a.Type == "none" {
		return config.Auth{}
	}
	a.TokenRefreshBuffer = 0
	if a.Type == "oauth2-client-credentials" && a.ClientAuthMethod == "" {
		a.ClientAuthMethod = "client_secret_basic"
	}
	if a.Type != "oauth2-client-credentials" {
		a.ClientAuthMethod = ""
	}
	return a
}

func (r *Runner) observationSignature(op model.Operation) string {
	c := r.Config
	files := map[string]string{}
	for _, path := range []string{c.TLS.CAFile, c.TLS.CertFile} {
		if path != "" {
			data, err := os.ReadFile(path)
			if err != nil {
				files[path] = "unavailable"
			} else {
				files[path] = model.ID(data)
			}
		}
	}
	var sensitive []string
	if len(c.SensitiveFields) > 0 {
		sensitive = c.SensitiveFields
	}
	return "v2:" + model.ID(c.BaseURL, c.EvidenceContext, c.Oracle, c.TLS, files, sensitive, r.dependencyContext(op))
}

func reusable(o model.Observation) bool {
	b, err := json.Marshal(o.Input)
	return err == nil && o.Sent && !strings.Contains(string(b), "[REDACTED]") && (o.Outcome == "accepted" || o.Outcome == "input-rejected")
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
