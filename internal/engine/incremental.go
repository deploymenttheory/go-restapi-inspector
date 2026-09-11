package engine

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/deploymenttheory/go-restapi-inspector/internal/config"
	"github.com/deploymenttheory/go-restapi-inspector/internal/generate"
	"github.com/deploymenttheory/go-restapi-inspector/internal/graph"
	"github.com/deploymenttheory/go-restapi-inspector/internal/journal"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
	"github.com/deploymenttheory/go-restapi-inspector/internal/spec"
)

const reuseAssumption = "Inherited evidence assumes unchanged backend behaviour, tenant configuration and principal privileges for compatible operations. Spec equality does not prove server equivalence; set evidence-context when that context changes."

type Inheritance struct {
	Lineage      model.Lineage          `json:"lineage"`
	Observations []model.Observation    `json:"observations"`
	Progress     []operationProgress    `json:"progress"`
	Experiments  []model.Observation    `json:"experiments"`
	Effects      []Effect               `json:"effects"`
	Rules        []model.Rule           `json:"rules"`
	Coverage     []model.Coverage       `json:"coverage"`
	Plans        []model.ExperimentPlan `json:"plans"`
}

type IncrementalPlan struct {
	Config       config.Config  `json:"-"`
	Plan         *graph.Plan    `json:"plan"`
	Lineage      *model.Lineage `json:"baseline,omitempty"`
	parent       Snapshot
	observations []model.Observation
	events       []journal.Event
	progress     map[string]*operationProgress
	effects      []Effect
}

// PrepareIncremental is read-only and never initializes credentials or transport.
func PrepareIncremental(c config.Config, d *spec.Document) (*IncrementalPlan, error) {
	i := &IncrementalPlan{Config: c}
	if c.BaselineRun == "" {
		p, err := graph.Build(d, c)
		i.Plan = p
		return i, err
	}
	parent, observations, events, err := Load(c.BaselineRun)
	if err != nil {
		return nil, fmt.Errorf("baseline: %w", err)
	}
	if c.BaseURL != parent.Config.BaseURL {
		return nil, fmt.Errorf("baseline target differs from base-url")
	}
	for _, resource := range parent.Report.Resources {
		if resource.State != "deleted" {
			return nil, fmt.Errorf("baseline has an unresolved %s resource %s; reconcile it with cleanup before starting an incremental run", resource.State, resource.ID)
		}
	}
	c = remapOperationSettings(c, parent.documentModel, d)
	// Preserve the recorded allowlist by stable identity, including renamed IDs
	// and removed operations. An empty surviving allowlist must not become 'all'.
	if len(c.Operations) == 0 && !c.SelectionExplicit {
		c.Operations = parent.Config.Operations
		c.SelectionExplicit = parent.Config.SelectionExplicit
	}
	if len(c.Operations) > 0 {
		c.SelectionExplicit = true
		var selected []string
		for _, selector := range c.Operations {
			matched := false
			for _, old := range parent.documentModel.Operations {
				if old.Key != selector && old.ID != selector {
					continue
				}
				matched = true
				for _, next := range d.Operations {
					if next.Identity() == old.Identity() {
						selected = append(selected, next.Key)
					}
				}
			}
			if !matched {
				selected = append(selected, selector)
			}
		}
		c.Operations = selected
	}
	p, err := graph.Build(d, c)
	if err != nil {
		return nil, err
	}
	fingerprints, err := d.Fingerprints()
	if err != nil {
		return nil, err
	}
	oldFingerprints := parent.Fingerprints
	if oldFingerprints == nil {
		oldFingerprints, err = parent.documentModel.Fingerprints()
		if err != nil {
			return nil, err
		}
	}
	oldRunner := &Runner{Config: parent.Config, Document: parent.documentModel, Plan: parent.plan, Fingerprints: oldFingerprints}
	if err := oldRunner.restoreProgress(parent, events); err != nil {
		return nil, err
	}
	newRunner := &Runner{Config: c, Document: d, Plan: p, Fingerprints: fingerprints}
	identity := recordedIdentity(parent)
	lineage := &model.Lineage{RunID: parent.Report.RunID, JournalHash: events[len(events)-1].Hash, Spec: identity, Assumption: reuseAssumption}
	oldOps := map[string]model.Operation{}
	for _, op := range parent.documentModel.Operations {
		oldOps[op.Identity()] = op
	}
	for _, op := range p.Operations {
		delta := model.OperationDelta{Identity: op.Identity(), Operation: op.Key, Change: "added", Action: "inspect", Selected: newRunner.selected(op), Reasons: []string{"new operation or request media type"}}
		if old, ok := oldOps[op.Identity()]; ok {
			delete(oldOps, op.Identity())
			delta.Previous = old.Key
			delta.Change = "unchanged"
			delta.Reasons = nil
			a, b := oldFingerprints[op.Identity()], fingerprints[op.Identity()]
			if a.Full != b.Full {
				delta.Change = "changed"
				for _, facet := range []struct{ name, before, after string }{{"request or referenced field description", a.Request, b.Request}, {"response or referenced field description", a.Response, b.Response}, {"documentation", a.Documentation, b.Documentation}, {"security", a.Security, b.Security}} {
					if facet.before != facet.after {
						delta.Reasons = append(delta.Reasons, facet.name+" changed")
					}
				}
				if len(delta.Reasons) == 0 {
					delta.Reasons = append(delta.Reasons, "operation definition changed")
				}
			} else if oldRunner.observationSignature(old) != newRunner.observationSignature(op) {
				delta.Change = "context-changed"
				delta.Reasons = []string{"observation context or fixture/readback dependency changed"}
			} else if progress := oldRunner.Progress[old.Key]; progress != nil && compatibleProgress(oldRunner, old, progress) {
				delta.ReusedCases, delta.ReusedPairs = progressCounts(progress)
				if delta.ReusedCases > 0 {
					delta.Action = "resume"
					delta.Reasons = []string{"reuse compatible classified cases and complete confirmation pairs; finish pending work"}
					for _, coverage := range parent.Report.Coverage {
						if coverage.Operation == old.Key && coverage.State == "complete" && allClassifiedReusable(progress) && oldRunner.coverageCompatible(old, coverage) && acquisitionSignature(parent.Config, "context", old) == acquisitionSignature(c, "context", op) {
							delta.Action = "inherit"
							delta.Reasons = []string{"compatible evidence completes the configured discovery scope"}
						}
					}
				}
			}
			if delta.Action == "inspect" && delta.Change == "unchanged" {
				delta.Reasons = []string{"no compatible classified inspection evidence; transport/readback traffic is not coverage"}
			}
		}
		if !delta.Selected {
			delta.Action = "out-of-scope"
			delta.ReusedCases = 0
			delta.ReusedPairs = 0
		} else if delta.Action == "inspect" || delta.Action == "resume" {
			for _, field := range op.Fields {
				if field.In == "path" {
					if _, fixed := c.Hint(op).Values[field.ID]; !fixed && !generate.Bound(field) {
						delta.Action = "blocked"
						delta.Reasons = append(delta.Reasons, "unbound path parameter "+field.ID)
					}
				}
			}
			if op.MediaType != "" && (!spec.JSONMedia(op.MediaType) || op.MediaType == "application/json-patch+json") {
				delta.Action = "blocked"
				delta.Reasons = append(delta.Reasons, "unsupported mutation model for "+op.MediaType)
			}
			if delta.Action == "blocked" {
				delta.ReusedCases = 0
				delta.ReusedPairs = 0
			}
		}
		lineage.Operations = append(lineage.Operations, delta)
	}
	for _, id := range spec.Keys(oldOps) {
		op := oldOps[id]
		lineage.Operations = append(lineage.Operations, model.OperationDelta{Identity: id, Operation: op.Key, Previous: op.Key, Change: "removed", Action: "history", Reasons: []string{"absent from the new specification"}})
	}
	i.Config, i.Plan, i.Lineage, i.parent, i.observations, i.events, i.progress, i.effects = c, p, lineage, parent, observations, events, oldRunner.Progress, oldRunner.Effects
	return i, nil
}

// Keep settings attached to the original representation when display keys gain
// media suffixes. Explicit settings for the new key take precedence.
func remapOperationSettings(c config.Config, before, after *spec.Document) config.Config {
	keys := map[string]string{}
	for _, old := range before.Operations {
		for _, next := range after.Operations {
			if old.Identity() == next.Identity() && old.Key != next.Key {
				keys[old.Key] = next.Key
			}
		}
	}
	if len(keys) == 0 {
		return c
	}
	c.Hints = model.Clone(c.Hints)
	for old, next := range keys {
		if hint, ok := c.Hints[old]; ok {
			if _, exists := c.Hints[next]; !exists {
				c.Hints[next] = hint
			}
			delete(c.Hints, old)
		}
	}
	for key, hint := range c.Hints {
		if next, ok := keys[hint.Read]; ok {
			hint.Read = next
		}
		if next, ok := keys[hint.Delete]; ok {
			hint.Delete = next
		}
		for field, binding := range hint.Bindings {
			if next, ok := keys[binding.Operation]; ok {
				binding.Operation = next
				hint.Bindings[field] = binding
			}
		}
		if hint.Poll != nil {
			if next, ok := keys[hint.Poll.Operation]; ok {
				hint.Poll.Operation = next
			}
		}
		c.Hints[key] = hint
	}
	c.Rules = model.Clone(c.Rules)
	for n, rule := range c.Rules {
		if next, ok := keys[rule.Operation]; ok {
			rule.Operation = next
			rule.Identify()
			c.Rules[n] = rule
		}
	}
	return c
}

func recordedIdentity(saved Snapshot) model.SpecIdentity {
	if saved.Report.SpecIdentity != nil {
		return *saved.Report.SpecIdentity
	}
	return saved.documentModel.Identity(saved.Config.SpecRelease)
}

func compatibleProgress(r *Runner, op model.Operation, p *operationProgress) bool {
	if strings.HasPrefix(p.Signature, "v2:") {
		return p.Signature == r.observationSignature(op)
	}
	return p.Signature == observationSignature(r.Config, r.Document.Hash, op.Key)
}

func (r *Runner) coverageCompatible(op model.Operation, c model.Coverage) bool {
	if c.PlanSignature == r.planSignature(op.Key) {
		return true
	}
	s := r.Config
	return c.PlanSignature == model.ID("acquisition-v2", observationSignature(s, r.Document.Hash, op.Key), s.Mode, s.Strategy, s.InteractionOrder, s.ValidationTrials, s.BoundarySteps, s.Domains, s.Rules)
}

func progressCounts(p *operationProgress) (int, int) {
	cases, controls := map[string]bool{}, map[string]bool{}
	pairs := 0
	for _, o := range p.Experiments {
		if !reusable(o) {
			continue
		}
		cases[model.ID(o.Input)] = true
		if o.Phase == "validation-control" {
			controls[o.ID] = true
		}
		if o.Phase == "validation" && controls[o.Control] {
			pairs++
			delete(controls, o.Control)
		}
	}
	return len(cases), pairs
}

func allClassifiedReusable(p *operationProgress) bool {
	for _, o := range p.Experiments {
		if (o.Outcome == "accepted" || o.Outcome == "input-rejected") && !reusable(o) {
			return false
		}
	}
	return true
}

func (i *IncrementalPlan) inherit(r *Runner) error {
	if i.Lineage == nil {
		return nil
	}
	// Keep the complete verified parent journal for portable history. It is not
	// replayed as child requests or resource ownership.
	var history []byte
	for _, event := range i.events {
		b, err := json.Marshal(event)
		if err != nil {
			return err
		}
		history = append(history, b...)
		history = append(history, '\n')
	}
	if err := journal.WriteFile(r.Journal.Dir, "baseline-journal.ndjson", history); err != nil {
		return err
	}
	state := Inheritance{Lineage: *i.Lineage}
	eligibleIDs := map[string]bool{}
	eligibleExperiments := map[string]bool{}
	operations := map[string]string{}
	complete := map[string]bool{}
	for _, delta := range i.Lineage.Operations {
		if delta.Action != "inherit" && delta.Action != "resume" {
			continue
		}
		operations[delta.Previous] = delta.Operation
		complete[delta.Previous] = delta.Action == "inherit"
		op, _ := r.Plan.Find(delta.Operation)
		state.Progress = append(state.Progress, operationProgress{Operation: op.Key, Signature: r.observationSignature(op)})
		for _, o := range i.progress[delta.Previous].Experiments {
			if !reusable(o) {
				continue
			}
			state.Experiments = append(state.Experiments, i.inheritedObservation(o, op.Key))
			eligibleIDs[o.ID] = true
			eligibleExperiments[o.ExperimentID] = true
		}
	}
	// Wire evidence for fixture/readback dependencies is carried for explanation,
	// but only current eligible operations contribute facts to contract export.
	for _, effect := range i.effects {
		if key, ok := operations[effect.Operation]; ok && eligibleIDs[effect.Experiment] {
			effect.Operation = key
			state.Effects = append(state.Effects, effect)
		}
	}
	supporting := map[string]bool{}
	for _, effect := range state.Effects {
		supporting[effect.ReadEvidence] = true
		supporting[effect.BeforeEvidence] = true
	}
	for _, o := range state.Experiments {
		supporting[o.Control] = true
	}
	for _, o := range i.observations {
		key, applicable := operations[o.Operation]
		if !applicable && !supporting[o.ID] {
			continue
		}
		if !applicable {
			key = o.Operation
		}
		o = i.inheritedObservation(o, key)
		o.EvidenceOnly = o.EvidenceOnly || !applicable || !eligibleIDs[o.ID]
		state.Observations = append(state.Observations, o)
	}
	for _, rule := range i.parent.Report.Rules {
		if key, ok := operations[rule.Operation]; ok && complete[rule.Operation] {
			if rule.Origin == nil {
				rule.Origin = &model.EvidenceOrigin{RunID: i.parent.Report.RunID, Spec: i.Lineage.Spec, Operation: rule.Operation}
			}
			if rule.Operation != key {
				rule.Operation = key
				rule.Identify()
			}
			state.Rules = append(state.Rules, rule)
		}
	}
	for _, coverage := range i.parent.Report.Coverage {
		if key, ok := operations[coverage.Operation]; ok && complete[coverage.Operation] {
			if coverage.Origin == nil {
				coverage.Origin = &model.EvidenceOrigin{RunID: i.parent.Report.RunID, Spec: i.Lineage.Spec, Operation: coverage.Operation}
			}
			coverage.Operation = key
			coverage.PlanSignature = r.planSignature(key)
			state.Coverage = append(state.Coverage, coverage)
		}
	}
	for _, event := range i.events {
		if event.Kind == "inheritance" {
			var previous Inheritance
			if err := journal.Decode(event.Data, &previous); err != nil {
				return err
			}
			for _, plan := range previous.Plans {
				if key, ok := operations[plan.Operation]; ok && eligibleExperiments[plan.ID] {
					plan.Operation = key
					state.Plans = append(state.Plans, plan)
				}
			}
		}
		if event.Kind == "experiment-plan" {
			var plan model.ExperimentPlan
			if err := journal.Decode(event.Data, &plan); err != nil {
				return err
			}
			if key, ok := operations[plan.Operation]; ok && eligibleExperiments[plan.ID] {
				plan.Operation = key
				state.Plans = append(state.Plans, plan)
			}
		}
	}
	state.Lineage.InheritedObservations = len(state.Observations)
	if err := r.Journal.Append("inheritance", state); err != nil {
		return err
	}
	r.applyInheritance(state)
	return r.checkpoint()
}

func (i *IncrementalPlan) inheritedObservation(o model.Observation, key string) model.Observation {
	if o.Origin == nil {
		o.Origin = &model.EvidenceOrigin{RunID: i.parent.Report.RunID, Spec: i.Lineage.Spec, Operation: o.Operation}
	}
	o.Operation = key
	return o
}

func (r *Runner) applyInheritance(state Inheritance) {
	r.Report.Baseline = &state.Lineage
	r.Report.Rules = append(r.Report.Rules, state.Rules...)
	r.Report.Coverage = append(r.Report.Coverage, state.Coverage...)
	r.Report.Warnings = append(r.Report.Warnings, state.Lineage.Assumption)
	r.Observations = append(r.Observations, state.Observations...)
	if r.Progress == nil {
		r.Progress = map[string]*operationProgress{}
	}
	for _, p := range state.Progress {
		r.Progress[p.Operation] = &p
	}
	for _, o := range state.Experiments {
		if p := r.Progress[o.Operation]; p != nil {
			p.Experiments = append(p.Experiments, o)
		}
	}
	r.Effects = append(r.Effects, state.Effects...)
}
