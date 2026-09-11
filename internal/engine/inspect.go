package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/deploymenttheory/go-restapi-inspector/internal/generate"
	"github.com/deploymenttheory/go-restapi-inspector/internal/graph"
	"github.com/deploymenttheory/go-restapi-inspector/internal/learn"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
	"github.com/deploymenttheory/go-restapi-inspector/internal/spec"
)

type contrast struct{ accepted, rejected model.Observation }

func (r *Runner) inspectOperation(ctx context.Context, op model.Operation) (coverage model.Coverage, rules []model.Rule, runErr error) {
	coverage = model.Coverage{Operation: op.Key, Mode: r.Config.Mode, InteractionOrder: r.Config.InteractionOrder, State: "partial"}
	coverage.PlanSignature = r.planSignature(op.Key)
	defer func() {
		if runErr != nil {
			coverage.State = "partial"
			if !slices.Contains(coverage.Reasons, runErr.Error()) {
				coverage.Reasons = append(coverage.Reasons, runErr.Error())
			}
		}
	}()
	if op.MediaType != "" && (!spec.JSONMedia(op.MediaType) || op.MediaType == "application/json-patch+json") {
		coverage.Reasons = []string{"request media type requires a dedicated mutation model: " + op.MediaType}
		return
	}
	for _, f := range op.Fields {
		if f.In == "path" {
			_, fixed := r.Config.Hint(op).Values[f.ID]
			bound := generate.Bound(f)
			if !fixed && !bound {
				coverage.Reasons = append(coverage.Reasons, "unbound path parameter "+f.ID)
				return
			}
		}
	}
	var base model.Input
	var baseline model.Observation
	var observations []model.Observation
	seen := map[string]model.Observation{}
	progress, err := r.progressFor(op)
	if err != nil {
		runErr = err
		return
	}
	for _, obs := range progress.Experiments {
		if !reusable(obs) {
			continue
		}
		observations = append(observations, obs)
		if _, exists := seen[model.ID(obs.Input)]; !exists && obs.Phase == "probe" {
			coverage.Tested++
		}
		seen[model.ID(obs.Input)] = obs
		if baseline.ID == "" && obs.Phase == "baseline" && obs.Outcome == "accepted" {
			base, baseline = obs.Input, obs
		}
	}
	if baseline.ID != "" {
		r.log("Reusing classified experiments for " + op.Key + "; checking a fresh baseline")
		control, e := r.experiment(ctx, op, base, "resume-control", baseline.ID)
		if e != nil {
			runErr = e
			return
		}
		if control.Outcome != "accepted" {
			runErr = fmt.Errorf("resumed baseline no longer succeeds: %s", control.Reason)
			return
		}
		observations = append(observations, control)
	}
	for _, in := range r.baselines(op) {
		if baseline.ID != "" {
			break
		}
		obs, err := r.experiment(ctx, op, in, "baseline", "")
		if err != nil {
			runErr = err
			coverage.Reasons = append(coverage.Reasons, err.Error())
			return
		}
		observations = append(observations, obs)
		seen[model.ID(in)] = obs
		if obs.Outcome == "accepted" {
			base = in
			baseline = obs
			break
		}
		if obs.Outcome == "inconclusive" {
			coverage.Reasons = append(coverage.Reasons, "baseline was inconclusive: "+obs.Reason)
			return
		}
	}
	if baseline.ID == "" {
		coverage.Reasons = append(coverage.Reasons, "no accepted baseline; configure hints.values with a viable request")
		return
	}
	interactionDomains := generate.Domains(op, base, r.Config, true)
	wide := generate.Domains(op, base, r.Config, false)
	domains := wide
	coverage.InteractionDomains = interactionDomains
	coverage.Domains = domains
	coverage.TotalCombinations = generate.Cardinality(domains)
	candidates, err := learn.Candidates(op, interactionDomains, r.Config)
	if err != nil {
		runErr = err
		coverage.Reasons = append(coverage.Reasons, err.Error())
		return
	}
	learner := learn.New(candidates)
	outside := false
	observe := func(obs model.Observation) {
		if e := learner.Observe(obs); e != nil {
			outside = true
		}
	}
	for _, obs := range observations {
		observe(obs)
	}
	purpose := "unary"
	visitInput := func(in model.Input) (model.Observation, error) {
		if old, ok := seen[model.ID(in)]; ok {
			return old, nil
		}
		if err := r.explorationBudget(op); err != nil {
			return model.Observation{}, err
		}
		probeCtx := model.WithProbe(ctx, purpose, base, "")
		control, e := r.experiment(probeCtx, op, base, "control", baseline.ID)
		if e != nil {
			return control, e
		}
		if control.Outcome != "accepted" {
			return control, fmt.Errorf("matched baseline control stopped succeeding: %s", control.Reason)
		}
		obs, e := r.experiment(probeCtx, op, in, "probe", control.ID)
		if e != nil {
			return obs, e
		}
		observations = append(observations, obs)
		observe(obs)
		seen[model.ID(in)] = obs
		coverage.Tested++
		return obs, nil
	}
	visitRow := func(row []int) error {
		in, e := generate.Materialize(base, domains, row)
		if e != nil {
			return nil
		}
		_, e = visitInput(in)
		return e
	}
	explorationErr := func() error {
		// Unary value challenges include types, null, empty, enum outsiders and
		// boundary neighbours, independently of the current learned network.
		for _, d := range wide {
			for _, state := range d.States {
				in := model.Clone(base)
				if e := in.Set(d.Field.ID, state); e != nil {
					continue
				}
				if _, e := visitInput(in); e != nil {
					return e
				}
			}
		}
		purpose = "boundary search"
		boundaryRules, boundaryErr := r.discoverBounds(ctx, op, base, &domains, func() []model.Observation { return observations }, visitInput)
		if boundaryErr != nil {
			return boundaryErr
		}
		wide = domains
		coverage.Domains = domains
		coverage.TotalCombinations = generate.Cardinality(domains)
		for _, rule := range boundaryRules {
			duplicate := false
			for _, existing := range candidates {
				duplicate = duplicate || existing.ID == rule.ID
			}
			if !duplicate {
				candidates = append(candidates, rule)
			}
		}
		learner = learn.New(candidates)
		outside = false
		for _, obs := range observations {
			observe(obs)
		}
		if r.Config.Mode == "exhaustive" {
			purpose = "exhaustive enumeration"
			if err := generate.Cartesian(ctx, generate.Sizes(domains), visitRow); err != nil {
				return err
			}
			coverage.InputComplete = true
			coverage.InteractionComplete = true
		} else if r.Config.Strategy != "unary" {
			purpose = "interaction covering"
			rows, e := generate.Cover(ctx, generate.Sizes(interactionDomains), r.Config.InteractionOrder, r.Config.MaxPlanningTuples)
			if e != nil {
				return e
			}
			for _, row := range rows {
				in, materialErr := generate.Materialize(base, interactionDomains, row)
				if materialErr != nil {
					continue
				}
				if _, e := visitInput(in); e != nil {
					return e
				}
			}
			coverage.InteractionComplete = true
		}
		// Covering experiments are deliberately performed even when the current
		// hypothesis language already appears to have converged.
		if r.Config.Strategy == "active" && !outside {
			purpose = "distinguishing query"
			var blocked [][]int
			for {
				row, found, e := learner.Witness(ctx, base, domains, blocked, r.Config.MaxPlanningTuples)
				if e != nil {
					return e
				}
				if !found {
					coverage.ModelConverged = true
					break
				}
				blocked = append(blocked, row)
				if e := visitRow(row); e != nil {
					return e
				}
				if outside {
					break
				}
			}
		}
		return nil
	}()
	if explorationErr != nil {
		if !errors.Is(explorationErr, ErrConfirmationReserve) {
			runErr = explorationErr
			return
		}
		coverage.Reasons = append(coverage.Reasons, explorationErr.Error())
		r.log("Exploration paused; confirming available findings for " + op.Key)
	}

	inconclusive := false
	for _, obs := range observations {
		if obs.Outcome == "inconclusive" {
			inconclusive = true
		}
	}
	if inconclusive {
		coverage.InputComplete = false
		coverage.InteractionComplete = false
		coverage.ModelConverged = false
		coverage.Reasons = append(coverage.Reasons, "unclassified experiments leave coverage gaps")
	}
	if outside {
		purpose = "failure minimization"
		coverage.ModelConverged = false
		coverage.Reasons = append(coverage.Reasons, learn.ErrOutsideModel.Error())
		// Find a small distinguishing failure to make a missing rule actionable.
		for _, obs := range observations {
			if obs.Outcome != "input-rejected" {
				continue
			}
			explained := false
			for i, rule := range candidates {
				if learner.Survives(i) && !rule.Holds(obs.Input) {
					explained = true
					break
				}
			}
			if explained {
				continue
			}
			fields := changedFields(base, obs.Input, op)
			minimal, e := generate.Minimize(ctx, base, obs.Input, fields, func(in model.Input) (bool, error) {
				o, e := visitInput(in)
				if e != nil {
					return false, e
				}
				if o.Outcome == "inconclusive" {
					return false, fmt.Errorf("minimization inconclusive")
				}
				return o.Outcome == "input-rejected", nil
			})
			if e == nil {
				if e = r.Journal.Append("unexplained", map[string]any{"operation": op.Key, "input": minimal, "differences": changedFields(base, minimal, op), "evidence": obs.ID}); e != nil {
					runErr = e
					return
				}
			}
			break
		}
	}
	// Prefer weaker antecedents, and omit rules already implied by a confirmed
	// rule with the same conclusion. All alternatives stay in the journal.
	sort.SliceStable(candidates, func(i, j int) bool { return complexity(candidates[i]) < complexity(candidates[j]) })
	// Rebuild after ordering to preserve the SAT selector/rule correspondence.
	learner = learn.New(candidates)
	for _, obs := range observations {
		_ = learner.Observe(obs)
	}
	cache := map[string][]contrast{}
	validationIssue := false
	for i, candidate := range candidates {
		if !learner.Survives(i) {
			continue
		}
		pair, ok := findContrast(candidate, observations, op)
		if !ok {
			positive, negative := false, false
			for _, obs := range observations {
				if obs.Outcome == "accepted" && (candidate.When == nil || candidate.When.Eval(obs.Input)) && candidate.Holds(obs.Input) {
					positive = true
				}
				if obs.Outcome == "input-rejected" && !candidate.Holds(obs.Input) {
					negative = true
				}
			}
			if !positive || !negative {
				continue
			}
		}
		if redundant(candidate, rules) {
			continue
		}
		if candidate.Scope == nil {
			candidate.Scope = map[string]string{}
		}
		candidate.Scope["operation"], candidate.Scope["method"], candidate.Scope["state"] = op.Key, op.Method, graph.Role(op, r.Config)
		candidate.Scope["evidence"] = "finite representative inputs; no universal completeness claim"
		candidate.Status = "hypothesis"
		candidate.Accepted = []string{pair.accepted.ID}
		candidate.Rejected = []string{pair.rejected.ID}
		if !learner.Consistent() {
			candidate.Notes = append(candidate.Notes, "candidate language cannot explain every observation")
			rules = append(rules, candidate)
			continue
		}
		entailed, e := learner.Entailed(ctx, i, base, wide, r.Config.MaxPlanningTuples)
		if e != nil {
			runErr = e
			coverage.Reasons = append(coverage.Reasons, e.Error())
			return
		}
		if !entailed {
			continue
		}
		if !ok {
			good, bad, found, e := learner.Contrast(ctx, i, base, wide, r.Config.MaxPlanningTuples)
			if e != nil {
				runErr = e
				return
			}
			if !found {
				continue
			}
			pair = contrast{accepted: model.Observation{Input: good, Outcome: "accepted"}, rejected: model.Observation{Input: bad, Outcome: "input-rejected"}}
			if _, valid := findContrast(candidate, []model.Observation{pair.accepted, pair.rejected}, op); !valid {
				continue
			}
		}
		key := model.ID(pair.accepted.Input, pair.rejected.Input)
		trials, exists := cache[key]
		if !exists {
			var e error
			trials, e = r.confirmPair(ctx, op, pair.accepted.Input, pair.rejected.Input, "input-rejected")
			if e != nil {
				runErr = e
				return
			}
			for _, trial := range trials {
				observations = append(observations, trial.accepted, trial.rejected)
				_ = learner.Observe(trial.accepted)
				_ = learner.Observe(trial.rejected)
			}
			cache[key] = trials
		}
		candidate.Accepted = nil
		candidate.Rejected = nil
		for _, trial := range trials {
			if trial.accepted.Outcome != "accepted" || trial.rejected.Outcome != "input-rejected" {
				candidate.Notes = append(candidate.Notes, "contrast did not reproduce")
				break
			}
			candidate.Trials++
			candidate.Accepted = append(candidate.Accepted, trial.accepted.ID)
			candidate.Rejected = append(candidate.Rejected, trial.rejected.ID)
		}
		if candidate.Trials >= r.Config.ValidationTrials && learner.Survives(i) {
			candidate.Status = "supported"
		}
		rules = append(rules, candidate)
	}
	// One reproducible accepted omission disproves an unconditional required
	// claim. It does not disprove a conditional requirement in another context.
	for _, field := range op.Fields {
		if field.In == "path" {
			continue
		}
		var omission *model.Observation
		for i := range observations {
			obs := &observations[i]
			if _, present := obs.Input.Get(field.ID); !present && obs.Outcome == "accepted" {
				omission = obs
				break
			}
		}
		if omission == nil {
			continue
		}
		rule := model.Rule{Operation: op.Key, Kind: "fieldIsOptional", Field: field.ID, Assert: model.Predicate{Op: "true"}, Status: "hypothesis", Scope: map[string]string{"meaning": "omittable in at least one observed valid context", "state": graph.Role(op, r.Config)}}
		rule.Identify()
		pairs, e := r.confirmPair(ctx, op, base, omission.Input, "accepted")
		if e != nil {
			runErr = e
			return
		}
		for _, pair := range pairs {
			observations = append(observations, pair.accepted, pair.rejected)
			_ = learner.Observe(pair.accepted)
			_ = learner.Observe(pair.rejected)
			rule.Accepted = append(rule.Accepted, pair.rejected.ID)
			rule.Trials++
		}
		if rule.Trials == r.Config.ValidationTrials {
			rule.Status = "supported"
		}
		rules = append(rules, rule)
	}
	repairs, e := r.confirmSchemaRepairs(ctx, op, base, observations)
	if e != nil {
		runErr = e
		return
	}
	rules = append(rules, repairs...)

	for i := range rules {
		if rules[i].Kind == "fieldIsOptional" {
			continue
		}
		for _, obs := range observations {
			if obs.Outcome == "accepted" && !rules[i].Holds(obs.Input) {
				rules[i].Status = "counterexample"
				rules[i].Notes = append(rules[i].Notes, "contradicted by "+obs.ID)
				break
			}
		}
	}
	outside = outside || !learner.Consistent()
	if validationIssue || outside {
		coverage.ModelConverged = false
		coverage.Reasons = append(coverage.Reasons, "confirmation experiments left uncertainty or contradicted the acquired model")
	}
	if !outside && !inconclusive && !validationIssue && explorationErr == nil {
		coverage.State = "complete"
	}
	if r.Config.Strategy == "unary" && r.Config.Mode != "exhaustive" {
		coverage.Reasons = append(coverage.Reasons, "unary strategy does not establish interaction coverage")
	}
	if !coverage.InputComplete {
		coverage.Reasons = append(coverage.Reasons, "discovery covers finite representatives, not every possible request")
	}
	if explorationErr != nil {
		runErr = explorationErr
	}
	if err := r.Journal.Append("analysis", map[string]any{"coverage": coverage, "rules": rules}); err != nil {
		runErr = err
	}
	return
}

func findContrast(rule model.Rule, observations []model.Observation, op model.Operation) (contrast, bool) {
	allowed := map[string]bool{}
	for _, id := range rule.Assert.Fields() {
		allowed[id] = true
	}
	for _, bad := range observations {
		if bad.Outcome != "input-rejected" || rule.Holds(bad.Input) {
			continue
		}
		for _, good := range observations {
			if good.Outcome != "accepted" || !rule.Holds(good.Input) || rule.When != nil && !rule.When.Eval(good.Input) {
				continue
			}
			diff := changedFields(good.Input, bad.Input, op)
			if len(diff) == 0 {
				continue
			}
			matched := true
			for _, id := range diff {
				if !allowed[id] { // Parent values necessarily change when a nested child changes.
					parent := false
					for child := range allowed {
						if strings.HasPrefix(child, id+"/") {
							parent = true
						}
					}
					if !parent {
						matched = false
						break
					}
				}
			}
			if matched {
				return contrast{good, bad}, true
			}
		}
	}
	return contrast{}, false
}
func changedFields(a, b model.Input, op model.Operation) []string {
	var out []string
	for _, f := range op.Fields {
		av, ap := a.Get(f.ID)
		bv, bp := b.Get(f.ID)
		if ap != bp || !model.Equal(av, bv) {
			out = append(out, f.ID)
		}
	}
	return out
}
func complexity(r model.Rule) int {
	if r.When == nil {
		return 0
	}
	if r.When.Op == "all" {
		return len(r.When.Args) * 10
	}
	if r.When.Op == "present" {
		return 1
	}
	return 2
}
func redundant(candidate model.Rule, confirmed []model.Rule) bool {
	for _, r := range confirmed {
		if r.Status != "supported" || model.ID(r.Assert) != model.ID(candidate.Assert) {
			continue
		}
		if r.When == nil {
			return true
		}
		if candidate.When != nil {
			if model.ID(*r.When) == model.ID(*candidate.When) {
				return true
			}
			if r.When.Op == "present" && candidate.When.Field == r.When.Field && candidate.When.Op == "eq" {
				return true
			}
			if candidate.When.Op == "all" {
				for _, a := range candidate.When.Args {
					if model.ID(a) == model.ID(*r.When) {
						return true
					}
				}
			}
		}
	}
	return false
}
func (r *Runner) addOperationRequiredness() {
	var extra []model.Rule
	for _, rule := range r.Report.Rules {
		if rule.Kind != "fieldIsRequired" || rule.Status != "supported" {
			continue
		}
		op, ok := r.Plan.Find(rule.Operation)
		if !ok {
			continue
		}
		role := graph.Role(op, r.Config)
		if role != "create" && role != "update" {
			continue
		}
		for _, other := range r.Report.Rules {
			if other.Kind != "fieldIsOptional" || other.Status != "supported" || other.Field != rule.Field {
				continue
			}
			target, ok := r.Plan.Find(other.Operation)
			if !ok || graph.Role(target, r.Config) == role {
				continue
			}
			linked := false
			for _, edge := range r.Plan.Edges {
				if edge.Producer == op.Key && edge.Consumer == target.Key || edge.Producer == target.Key && edge.Consumer == op.Key {
					linked = true
				}
			}
			if !linked {
				continue
			}
			derived := model.Clone(rule)
			derived.Kind = "fieldIsRequiredForCreateOnly"
			if role == "update" {
				derived.Kind = "fieldIsRequiredForUpdateOnly"
			}
			derived.Accepted = append(derived.Accepted, other.Accepted...)
			derived.Notes = append(derived.Notes, "compared with "+target.Key+"; scoped to these operations and resource state")
			derived.Identify()
			extra = append(extra, derived)
			break
		}
	}
	r.Report.Rules = append(r.Report.Rules, extra...)
}
